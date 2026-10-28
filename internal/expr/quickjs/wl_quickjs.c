// The C side of the native backend; see wl_quickjs.h.

#include "wl_quickjs.h"

#include <pthread.h>
#include <stdatomic.h>
#include <stdlib.h>
#include <string.h>

#include "_cgo_export.h"
#include "quickjs-ng/quickjs.h"

// Reading the completion value or the exception back may allocate (UTF-8
// conversion, the exception's string form); it gets this much heap above
// the evaluation's cap.
#define WL_READBACK_HEADROOM (16u << 20)

typedef struct wl_call {
	int index;
	char *arg;
	size_t arg_len;
	JSValue resolve, reject;
	int taken, settled;
} wl_call;

struct wl_slot {
	JSRuntime *rt;
	JSContext *ctx;
	size_t memory_limit;
	atomic_int interrupt;
	int interrupted;
	JSValue result;
	JSValue exc;
	wl_call *calls;
	size_t ncalls, calls_cap, next_take;
	uint8_t *bc;
	// C strings handed to Go, freed at release.
	const char *cstr[8];
	int ncstr;
};

static void *wl_thread_main(void *arg)
{
	wlSlotMain((uintptr_t)arg);
	return NULL;
}

int wl_spawn(uintptr_t handle, size_t stack_bytes)
{
	pthread_attr_t attr;
	pthread_t t;
	int err;

	err = pthread_attr_init(&attr);
	if (err != 0)
		return err;
	err = pthread_attr_setstacksize(&attr, stack_bytes);
	if (err == 0)
		err = pthread_attr_setdetachstate(&attr, PTHREAD_CREATE_DETACHED);
	if (err == 0)
		err = pthread_create(&t, &attr, wl_thread_main, (void *)handle);
	pthread_attr_destroy(&attr);
	return err;
}

wl_slot *wl_slot_new(void)
{
	wl_slot *s = calloc(1, sizeof(*s));
	if (s != NULL) {
		atomic_init(&s->interrupt, 0);
		s->result = JS_UNDEFINED;
		s->exc = JS_UNDEFINED;
	}
	return s;
}

void wl_slot_free(wl_slot *s)
{
	wl_release(s);
	free(s);
}

static int wl_interrupt_handler(JSRuntime *rt, void *opaque)
{
	wl_slot *s = opaque;
	(void)rt;
	if (atomic_load_explicit(&s->interrupt, memory_order_relaxed)) {
		s->interrupted = 1;
		return 1;
	}
	return 0;
}

int wl_prepare(wl_slot *s, size_t memory_limit, size_t stack_size)
{
	JSRuntime *rt;
	JSContext *ctx;

	rt = JS_NewRuntime();
	if (rt == NULL)
		return WL_NO_MEMORY;
	JS_SetMemoryLimit(rt, memory_limit);
	JS_SetMaxStackSize(rt, stack_size);
	JS_SetInterruptHandler(rt, wl_interrupt_handler, s);
	ctx = JS_NewContext(rt);
	if (ctx == NULL) {
		JS_FreeRuntime(rt);
		return WL_NO_MEMORY;
	}
	JS_SetContextOpaque(ctx, s);
	s->rt = rt;
	s->ctx = ctx;
	s->memory_limit = memory_limit;
	s->interrupted = 0;
	s->result = JS_UNDEFINED;
	s->exc = JS_UNDEFINED;
	return WL_OK;
}

void wl_release(wl_slot *s)
{
	JSContext *ctx = s->ctx;
	size_t i;

	if (ctx == NULL)
		return;
	for (i = 0; i < s->ncalls; i++) {
		JS_FreeValue(ctx, s->calls[i].resolve);
		JS_FreeValue(ctx, s->calls[i].reject);
		free(s->calls[i].arg);
	}
	free(s->calls);
	s->calls = NULL;
	s->ncalls = s->calls_cap = s->next_take = 0;
	for (i = 0; i < (size_t)s->ncstr; i++)
		JS_FreeCString(ctx, s->cstr[i]);
	s->ncstr = 0;
	if (s->bc != NULL) {
		js_free(ctx, s->bc);
		s->bc = NULL;
	}
	JS_FreeValue(ctx, s->result);
	JS_FreeValue(ctx, s->exc);
	s->result = JS_UNDEFINED;
	s->exc = JS_UNDEFINED;
	JS_FreeContext(ctx);
	JS_FreeRuntime(s->rt);
	s->ctx = NULL;
	s->rt = NULL;
}

void wl_interrupt(wl_slot *s)
{
	atomic_store_explicit(&s->interrupt, 1, memory_order_relaxed);
}

void wl_clear_interrupt(wl_slot *s)
{
	atomic_store_explicit(&s->interrupt, 0, memory_order_relaxed);
}

// take_exception moves the pending exception into the slot. An interrupted
// program reports the interrupt, whatever it left behind.
static int take_exception(wl_slot *s)
{
	JS_FreeValue(s->ctx, s->exc);
	s->exc = JS_GetException(s->ctx);
	return s->interrupted ? WL_INTERRUPTED : WL_EXCEPTION;
}

int wl_compile(wl_slot *s, const char *src, size_t len, uint8_t **bc, size_t *bc_len)
{
	JSContext *ctx = s->ctx;
	JSValue fn;
	char *buf;

	// JS_Eval needs a terminating NUL after the source.
	buf = malloc(len + 1);
	if (buf == NULL) {
		JS_ThrowOutOfMemory(ctx);
		return take_exception(s);
	}
	memcpy(buf, src, len);
	buf[len] = '\0';
	JS_UpdateStackTop(s->rt);
	fn = JS_Eval(ctx, buf, len, "eval_script", JS_EVAL_TYPE_GLOBAL | JS_EVAL_FLAG_COMPILE_ONLY);
	free(buf);
	if (JS_IsException(fn))
		return take_exception(s);
	s->bc = JS_WriteObject(ctx, bc_len, fn, JS_WRITE_OBJ_BYTECODE);
	JS_FreeValue(ctx, fn);
	if (s->bc == NULL) {
		if (!JS_HasException(ctx))
			JS_ThrowOutOfMemory(ctx);
		return take_exception(s);
	}
	*bc = s->bc;
	return WL_OK;
}

static int def_global(wl_slot *s, const char *name, size_t name_len, JSValue v)
{
	JSContext *ctx = s->ctx;
	JSValue global;
	JSAtom atom;
	int r;

	atom = JS_NewAtomLen(ctx, name_len ? name : "", name_len);
	if (atom == JS_ATOM_NULL) {
		JS_FreeValue(ctx, v);
		return take_exception(s);
	}
	global = JS_GetGlobalObject(ctx);
	r = JS_DefinePropertyValue(ctx, global, atom, v, JS_PROP_C_W_E | JS_PROP_THROW);
	JS_FreeValue(ctx, global);
	JS_FreeAtom(ctx, atom);
	return r < 0 ? take_exception(s) : WL_OK;
}

int wl_def_string(wl_slot *s, const char *name, size_t name_len, const char *text, size_t text_len)
{
	JSValue v = JS_NewStringLen(s->ctx, text_len ? text : "", text_len);
	if (JS_IsException(v))
		return take_exception(s);
	return def_global(s, name, name_len, v);
}

static JSValue wl_host_fn(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv, int index)
{
	wl_slot *s = JS_GetContextOpaque(ctx);
	JSValue funcs[2], promise;
	const char *arg;
	wl_call *c;
	size_t len;
	char *copy;

	(void)this_val;
	arg = JS_ToCStringLen(ctx, &len, argc > 0 ? argv[0] : JS_UNDEFINED);
	if (arg == NULL)
		return JS_EXCEPTION;
	copy = malloc(len + 1);
	if (copy != NULL)
		memcpy(copy, arg, len);
	JS_FreeCString(ctx, arg);
	if (copy == NULL)
		return JS_ThrowOutOfMemory(ctx);
	if (s->ncalls == s->calls_cap) {
		size_t cap = s->calls_cap ? 2 * s->calls_cap : 4;
		wl_call *calls = realloc(s->calls, cap * sizeof(*calls));
		if (calls == NULL) {
			free(copy);
			return JS_ThrowOutOfMemory(ctx);
		}
		s->calls = calls;
		s->calls_cap = cap;
	}
	promise = JS_NewPromiseCapability(ctx, funcs);
	if (JS_IsException(promise)) {
		free(copy);
		return promise;
	}
	c = &s->calls[s->ncalls++];
	c->index = index;
	c->arg = copy;
	c->arg_len = len;
	c->resolve = funcs[0];
	c->reject = funcs[1];
	c->taken = 0;
	c->settled = 0;
	return promise;
}

int wl_def_func(wl_slot *s, const char *name, size_t name_len, int index)
{
	JSValue fn;
	char *cname;

	cname = malloc(name_len + 1);
	if (cname == NULL) {
		JS_ThrowOutOfMemory(s->ctx);
		return take_exception(s);
	}
	if (name_len)
		memcpy(cname, name, name_len);
	cname[name_len] = '\0';
	fn = JS_NewCFunction2(s->ctx, (JSCFunction *)(void *)wl_host_fn, cname, 1, JS_CFUNC_generic_magic, index);
	free(cname);
	if (JS_IsException(fn))
		return take_exception(s);
	return def_global(s, name, name_len, fn);
}

int wl_start(wl_slot *s, const uint8_t *bc, size_t bc_len)
{
	JSContext *ctx = s->ctx;
	JSValue fn, v;

	JS_UpdateStackTop(s->rt);
	// The runtime was prepared before the job arrived; restart the
	// performance clock so that it counts from the evaluation's start, as
	// it would in a context created for it.
	JS_AddPerformance(ctx);
	fn = JS_ReadObject(ctx, bc, bc_len, JS_READ_OBJ_BYTECODE);
	if (JS_IsException(fn))
		return take_exception(s);
	v = JS_EvalFunction(ctx, fn);
	if (JS_IsException(v))
		return take_exception(s);
	JS_FreeValue(ctx, s->result);
	s->result = v;
	return s->interrupted ? WL_INTERRUPTED : WL_OK;
}

int wl_run(wl_slot *s)
{
	JSContext *ctx = s->ctx, *job_ctx;

	JS_UpdateStackTop(s->rt);
	for (;;) {
		if (s->interrupted)
			return WL_INTERRUPTED;
		// Stop as soon as the completion value is settled: jobs still
		// queued then never run.
		if (!JS_IsPromise(s->result))
			return WL_OK;
		switch (JS_PromiseState(ctx, s->result)) {
		case JS_PROMISE_FULFILLED:
			return WL_OK;
		case JS_PROMISE_REJECTED:
			JS_FreeValue(ctx, s->exc);
			s->exc = JS_PromiseResult(ctx, s->result);
			return WL_EXCEPTION;
		case JS_PROMISE_PENDING:
			break;
		}
		if (!JS_IsJobPending(s->rt))
			return WL_WAITING;
		if (JS_ExecutePendingJob(s->rt, &job_ctx) < 0)
			return take_exception(s);
	}
}

int wl_take_call(wl_slot *s, uint32_t *id, int *index, wl_text *arg)
{
	wl_call *c;

	if (s->next_take == s->ncalls)
		return 0;
	*id = (uint32_t)s->next_take;
	c = &s->calls[s->next_take++];
	c->taken = 1;
	*index = c->index;
	arg->ptr = c->arg;
	arg->len = c->arg_len;
	return 1;
}

int wl_settle(wl_slot *s, uint32_t id, int ok, const char *text, size_t len)
{
	JSContext *ctx = s->ctx;
	JSValue v, msg, r;
	wl_call *c;

	if (id >= s->ncalls || !s->calls[id].taken || s->calls[id].settled)
		return WL_OK;
	c = &s->calls[id];
	c->settled = 1;
	JS_UpdateStackTop(s->rt);
	v = JS_NewStringLen(ctx, len ? text : "", len);
	if (JS_IsException(v))
		return take_exception(s);
	if (!ok) {
		// new Error(text), built from the intrinsic prototype so that a
		// program that replaced globalThis.Error cannot intercept it.
		msg = v;
		v = JS_NewError(ctx);
		if (JS_IsException(v)) {
			JS_FreeValue(ctx, msg);
			return take_exception(s);
		}
		if (JS_DefinePropertyValueStr(ctx, v, "message", msg, JS_PROP_WRITABLE | JS_PROP_CONFIGURABLE) < 0) {
			JS_FreeValue(ctx, v);
			return take_exception(s);
		}
	}
	r = JS_Call(ctx, ok ? c->resolve : c->reject, JS_UNDEFINED, 1, (JSValueConst *)&v);
	JS_FreeValue(ctx, v);
	JS_FreeValue(ctx, c->resolve);
	JS_FreeValue(ctx, c->reject);
	c->resolve = JS_UNDEFINED;
	c->reject = JS_UNDEFINED;
	if (JS_IsException(r))
		return take_exception(s);
	JS_FreeValue(ctx, r);
	return s->interrupted ? WL_INTERRUPTED : WL_OK;
}

static void readback_headroom(wl_slot *s)
{
	JS_SetMemoryLimit(s->rt, s->memory_limit + WL_READBACK_HEADROOM);
}

// keep_cstr converts v with JS_ToCStringLen and keeps the C string for
// wl_release. A failed conversion clears its exception and gives "".
static wl_text keep_cstr(wl_slot *s, JSValueConst v)
{
	wl_text t = {"", 0};
	const char *p;
	size_t len;

	if (s->ncstr == (int)(sizeof(s->cstr) / sizeof(s->cstr[0])))
		return t;
	p = JS_ToCStringLen(s->ctx, &len, v);
	if (p == NULL) {
		JS_FreeValue(s->ctx, JS_GetException(s->ctx));
		return t;
	}
	s->cstr[s->ncstr++] = p;
	t.ptr = p;
	t.len = len;
	return t;
}

int wl_result(wl_slot *s, wl_text *out)
{
	JSContext *ctx = s->ctx;
	JSValue v;
	const char *p;
	size_t len;

	readback_headroom(s);
	if (JS_IsPromise(s->result))
		v = JS_PromiseResult(ctx, s->result);
	else
		v = JS_DupValue(ctx, s->result);
	if (!JS_IsString(v)) {
		JS_FreeValue(ctx, v);
		return WL_NOT_STRING;
	}
	p = JS_ToCStringLen(ctx, &len, v);
	JS_FreeValue(ctx, v);
	if (p == NULL)
		return take_exception(s);
	if (s->ncstr == (int)(sizeof(s->cstr) / sizeof(s->cstr[0]))) {
		JS_FreeCString(ctx, p);
		JS_ThrowInternalError(ctx, "wl_result: no room for the text");
		return take_exception(s);
	}
	s->cstr[s->ncstr++] = p;
	out->ptr = p;
	out->len = len;
	return WL_OK;
}

// prop_text reads obj[name] as text, or "" when it is absent or not a
// string and want_string is set.
static wl_text prop_text(wl_slot *s, JSValueConst obj, const char *name, int want_string)
{
	wl_text t = {"", 0};
	JSValue v = JS_GetPropertyStr(s->ctx, obj, name);

	if (JS_IsException(v)) {
		JS_FreeValue(s->ctx, JS_GetException(s->ctx));
		return t;
	}
	if (!(want_string && !JS_IsString(v)) && !JS_IsUndefined(v))
		t = keep_cstr(s, v);
	JS_FreeValue(s->ctx, v);
	return t;
}

int wl_exception(wl_slot *s, wl_exc *out)
{
	JSValueConst v = s->exc;

	memset(out, 0, sizeof(*out));
	out->name.ptr = out->message.ptr = out->text.ptr = out->stack.ptr = "";
	readback_headroom(s);
	if (JS_IsNull(v)) {
		out->thrown = WL_THROWN_NULL;
	} else if (JS_IsUndefined(v) || JS_IsUninitialized(v)) {
		// Uninitialized means nothing was thrown, which the engine does
		// when it cannot even allocate its out-of-memory error.
		out->thrown = WL_THROWN_UNDEFINED;
	} else if (JS_IsError(v)) {
		out->thrown = WL_THROWN_ERROR;
		out->name = prop_text(s, v, "name", 0);
		out->message = prop_text(s, v, "message", 0);
		out->stack = prop_text(s, v, "stack", 1);
		out->text = keep_cstr(s, v);
	} else {
		out->thrown = WL_THROWN_OTHER;
		out->text = keep_cstr(s, v);
	}
	return s->interrupted ? WL_INTERRUPTED : WL_OK;
}
