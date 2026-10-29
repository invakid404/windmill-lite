// The C side of the native backend; see wl_quickjs.h.

#include "wl_quickjs.h"

#include <pthread.h>
#include <stdalign.h>
#include <stdatomic.h>
#include <stddef.h>
#include <stdlib.h>
#include <string.h>

#include "_cgo_export.h"
#include "mimalloc.h"
#include "quickjs-ng/cutils.h"
#include "quickjs-ng/quickjs.h"

// The heap's allocator: mimalloc, or the C library's with the build tag
// wl_libc_malloc, which only measurements use (alloc_libc.go).
#ifdef WL_LIBC_MALLOC
#define WL_MALLOC malloc
#define WL_CALLOC calloc
#define WL_REALLOC realloc
#define WL_FREE free
#define WL_USABLE_SIZE js__malloc_usable_size
#else
#define WL_MALLOC mi_malloc
#define WL_CALLOC mi_calloc
#define WL_REALLOC mi_realloc
#define WL_FREE mi_free
#define WL_USABLE_SIZE mi_usable_size
#endif

// Reading the completion value or the exception back may allocate (UTF-8
// conversion, the exception's string form); it gets this much heap above
// the evaluation's cap.
#define WL_READBACK_HEADROOM (16u << 20)

// Every block a runtime allocates is linked into its slot's heap, so that
// whatever the engine fails to free by the end of JS_FreeRuntime can still
// be freed, and counted. Blocks come from mimalloc, whose heaps are per
// thread, so slots do not contend for one allocator lock (README,
// Allocator).
typedef union wl_block {
	struct {
		union wl_block *prev, *next;
	} link;
	max_align_t align;
} wl_block;

typedef struct wl_heap {
	wl_block head;
} wl_heap;

// A host call's record is queued when the program calls, taken when Go
// dispatches it, and free again once it settles; free records are reused,
// so the table holds one record per outstanding call.
enum { WL_CALL_FREE, WL_CALL_QUEUED, WL_CALL_TAKEN };
#define WL_NO_CALL UINT32_MAX

typedef struct wl_call {
	int index;
	char *arg; // js_malloc'd, so it counts against the heap cap
	size_t arg_len;
	JSValue resolve, reject;
	int state;
	uint32_t next; // the next queued call, or the next free record
} wl_call;

struct wl_slot {
	JSRuntime *rt;
	JSContext *ctx;
	wl_heap heap;
	size_t memory_limit;
	size_t leaked; // blocks swept at the last release
	atomic_int interrupt;
	int interrupted;
	JSValue result;
	JSValue exc;
	wl_call *calls; // js_realloc'd
	uint32_t ncalls, calls_cap;
	uint32_t free_head, queue_head, queue_tail;
	uint8_t *bc;
	// C strings handed to Go, freed at release.
	const char *cstr[8];
	int ncstr;
};

static void heap_link(wl_heap *h, wl_block *b)
{
	b->link.prev = &h->head;
	b->link.next = h->head.link.next;
	h->head.link.next->link.prev = b;
	h->head.link.next = b;
}

static void heap_unlink(wl_block *b)
{
	b->link.prev->link.next = b->link.next;
	b->link.next->link.prev = b->link.prev;
}

static void *heap_malloc(void *opaque, size_t size)
{
	wl_block *b;

	if (size > SIZE_MAX - sizeof(wl_block))
		return NULL;
	b = WL_MALLOC(sizeof(wl_block) + size);
	if (b == NULL)
		return NULL;
	heap_link(opaque, b);
	return b + 1;
}

static void *heap_calloc(void *opaque, size_t count, size_t size)
{
	wl_block *b;

	if (size != 0 && count > (SIZE_MAX - sizeof(wl_block)) / size)
		return NULL;
	b = WL_CALLOC(1, sizeof(wl_block) + count * size);
	if (b == NULL)
		return NULL;
	heap_link(opaque, b);
	return b + 1;
}

static void heap_free(void *opaque, void *ptr)
{
	wl_block *b;

	(void)opaque;
	if (ptr == NULL)
		return;
	b = (wl_block *)ptr - 1;
	heap_unlink(b);
	WL_FREE(b);
}

static void *heap_realloc(void *opaque, void *ptr, size_t size)
{
	wl_block *b, *nb;

	if (ptr == NULL)
		return heap_malloc(opaque, size);
	if (size > SIZE_MAX - sizeof(wl_block))
		return NULL;
	b = (wl_block *)ptr - 1;
	heap_unlink(b);
	nb = WL_REALLOC(b, sizeof(wl_block) + size);
	if (nb == NULL) {
		heap_link(opaque, b);
		return NULL;
	}
	heap_link(opaque, nb);
	return nb + 1;
}

// heap_usable_size is what the engine's heap accounting adds per block: the
// allocator's usable size, as with QuickJS-ng's default allocator, less the
// link.
static size_t heap_usable_size(const void *ptr)
{
	size_t n = WL_USABLE_SIZE((const wl_block *)ptr - 1);
	return n > sizeof(wl_block) ? n - sizeof(wl_block) : 0;
}

static const JSMallocFunctions heap_funcs = {
	heap_calloc, heap_malloc, heap_free, heap_realloc, heap_usable_size,
};

// heap_sweep frees every block still linked and returns how many there
// were.
static size_t heap_sweep(wl_heap *h)
{
	size_t n = 0;
	wl_block *b;

	while (h->head.link.next != &h->head) {
		b = h->head.link.next;
		heap_unlink(b);
		WL_FREE(b);
		n++;
	}
	return n;
}

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
		s->heap.head.link.prev = s->heap.head.link.next = &s->heap.head;
		s->free_head = s->queue_head = s->queue_tail = WL_NO_CALL;
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

	rt = JS_NewRuntime2(&heap_funcs, &s->heap);
	if (rt == NULL) {
		heap_sweep(&s->heap);
		return WL_NO_MEMORY;
	}
	JS_SetMemoryLimit(rt, memory_limit);
	JS_SetMaxStackSize(rt, stack_size);
	JS_SetInterruptHandler(rt, wl_interrupt_handler, s);
	ctx = JS_NewContext(rt);
	if (ctx == NULL) {
		JS_FreeRuntime(rt);
		heap_sweep(&s->heap);
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
		js_free(ctx, s->calls[i].arg);
	}
	js_free(ctx, s->calls);
	s->calls = NULL;
	s->ncalls = s->calls_cap = 0;
	s->free_head = s->queue_head = s->queue_tail = WL_NO_CALL;
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
	// QuickJS-ng can leak an object the program made unreachable (for
	// example through Error.stackTraceLimit); its blocks go here.
	s->leaked = heap_sweep(&s->heap);
}

size_t wl_leaked(wl_slot *s)
{
	return s->leaked;
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
	if (len)
		memcpy(buf, src, len);
	buf[len] = '\0';
	JS_UpdateStackTop(s->rt);
	// Windmill's embedding evaluates every script it builds as a global
	// script with strict mode forced (JE-50).
	fn = JS_Eval(ctx, buf, len, "eval_script", JS_EVAL_TYPE_GLOBAL | JS_EVAL_FLAG_STRICT | JS_EVAL_FLAG_COMPILE_ONLY);
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

void wl_begin(wl_slot *s)
{
	// The runtime was prepared before the job arrived; restart the
	// performance clock so that it counts from the evaluation's start, as
	// it would in a context created for it. This comes before the host
	// bindings, so that one named performance replaces it.
	JS_AddPerformance(s->ctx);
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

static JSValue wl_host_fn(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv, int magic, JSValueConst *data)
{
	wl_slot *s = JS_GetContextOpaque(ctx);
	JSValue funcs[2], promise;
	const char *arg;
	wl_call *c;
	int32_t index;
	uint32_t id;
	size_t len;
	char *copy;

	(void)this_val;
	(void)magic;
	if (JS_ToInt32(ctx, &index, data[0]) < 0)
		return JS_EXCEPTION;
	arg = JS_ToCStringLen(ctx, &len, argc > 0 ? argv[0] : JS_UNDEFINED);
	if (arg == NULL)
		return JS_EXCEPTION;
	// When an Error's ToString throws, the conversion falls back to its
	// message but leaves the exception pending: the call then throws it.
	if (JS_HasException(ctx)) {
		JS_FreeCString(ctx, arg);
		return JS_EXCEPTION;
	}
	copy = js_malloc(ctx, len + 1);
	if (copy != NULL && len)
		memcpy(copy, arg, len);
	JS_FreeCString(ctx, arg);
	if (copy == NULL)
		return JS_EXCEPTION;
	if (s->free_head == WL_NO_CALL && s->ncalls == s->calls_cap) {
		uint32_t cap = s->calls_cap ? 2 * s->calls_cap : 4;
		wl_call *calls = cap > s->calls_cap ? js_realloc(ctx, s->calls, (size_t)cap * sizeof(*calls)) : NULL;
		if (calls == NULL) {
			js_free(ctx, copy);
			if (!JS_HasException(ctx))
				JS_ThrowOutOfMemory(ctx);
			return JS_EXCEPTION;
		}
		s->calls = calls;
		s->calls_cap = cap;
	}
	promise = JS_NewPromiseCapability(ctx, funcs);
	if (JS_IsException(promise)) {
		js_free(ctx, copy);
		return promise;
	}
	if (s->free_head != WL_NO_CALL) {
		id = s->free_head;
		s->free_head = s->calls[id].next;
	} else {
		id = s->ncalls++;
	}
	c = &s->calls[id];
	c->index = index;
	c->arg = copy;
	c->arg_len = len;
	c->resolve = funcs[0];
	c->reject = funcs[1];
	c->state = WL_CALL_QUEUED;
	c->next = WL_NO_CALL;
	if (s->queue_tail == WL_NO_CALL)
		s->queue_head = id;
	else
		s->calls[s->queue_tail].next = id;
	s->queue_tail = id;
	return promise;
}

int wl_def_func(wl_slot *s, const char *name, size_t name_len, int index)
{
	JSValue fn, data;
	char *cname;

	cname = malloc(name_len + 1);
	if (cname == NULL) {
		JS_ThrowOutOfMemory(s->ctx);
		return take_exception(s);
	}
	if (name_len)
		memcpy(cname, name, name_len);
	cname[name_len] = '\0';
	data = JS_NewInt32(s->ctx, index);
	fn = JS_NewCFunctionData2(s->ctx, wl_host_fn, cname, 1, 0, 1, &data);
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
	// The queue empties before the completion value is read, as rquickjs's
	// poll empties it (JE-51): a job chain that never ends keeps the
	// evaluation from finishing even once its value has settled, until the
	// attempt limit interrupts it. A job that throws (a queueMicrotask
	// callback, for example) is dropped, and its exception fails nothing.
	while (JS_IsJobPending(s->rt)) {
		if (s->interrupted)
			return WL_INTERRUPTED;
		if (JS_ExecutePendingJob(s->rt, &job_ctx) < 0) {
			if (s->interrupted)
				return WL_INTERRUPTED;
			JS_FreeValue(job_ctx, JS_GetException(job_ctx));
		}
	}
	if (s->interrupted)
		return WL_INTERRUPTED;
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
	return WL_WAITING;
}

int wl_take_call(wl_slot *s, uint32_t *id, int *index, wl_text *arg)
{
	wl_call *c;

	if (s->queue_head == WL_NO_CALL)
		return 0;
	*id = s->queue_head;
	c = &s->calls[*id];
	s->queue_head = c->next;
	if (s->queue_head == WL_NO_CALL)
		s->queue_tail = WL_NO_CALL;
	c->state = WL_CALL_TAKEN;
	c->next = WL_NO_CALL;
	*index = c->index;
	arg->ptr = c->arg;
	arg->len = c->arg_len;
	return 1;
}

void wl_free_arg(wl_slot *s, uint32_t id)
{
	wl_call *c;

	if (id >= s->ncalls || s->calls[id].state != WL_CALL_TAKEN)
		return;
	c = &s->calls[id];
	js_free(s->ctx, c->arg);
	c->arg = NULL;
	c->arg_len = 0;
}

int wl_settle(wl_slot *s, uint32_t id, int ok, const char *text, size_t len)
{
	JSContext *ctx = s->ctx;
	JSValue v, msg, r, resolve, reject;
	wl_call *c;

	if (id >= s->ncalls || s->calls[id].state != WL_CALL_TAKEN)
		return WL_OK;
	// Building the Error and resolving can run the program's code, which
	// can call a host function and so move s->calls or reuse this record:
	// take what is needed, free the record, and never touch it again.
	c = &s->calls[id];
	resolve = c->resolve;
	reject = c->reject;
	js_free(ctx, c->arg);
	c->arg = NULL;
	c->arg_len = 0;
	c->resolve = JS_UNDEFINED;
	c->reject = JS_UNDEFINED;
	c->state = WL_CALL_FREE;
	c->next = s->free_head;
	s->free_head = id;
	c = NULL;
	JS_UpdateStackTop(s->rt);
	v = JS_NewStringLen(ctx, len ? text : "", len);
	if (!JS_IsException(v) && !ok) {
		// new Error(text), built from the intrinsic prototype so that a
		// program that replaced globalThis.Error cannot intercept it.
		msg = v;
		v = JS_NewError(ctx);
		if (JS_IsException(v)) {
			JS_FreeValue(ctx, msg);
		} else if (JS_DefinePropertyValueStr(ctx, v, "message", msg, JS_PROP_WRITABLE | JS_PROP_CONFIGURABLE) < 0) {
			JS_FreeValue(ctx, v);
			v = JS_EXCEPTION;
		}
	}
	if (JS_IsException(v)) {
		JS_FreeValue(ctx, resolve);
		JS_FreeValue(ctx, reject);
		return take_exception(s);
	}
	r = JS_Call(ctx, ok ? resolve : reject, JS_UNDEFINED, 1, (JSValueConst *)&v);
	JS_FreeValue(ctx, v);
	JS_FreeValue(ctx, resolve);
	JS_FreeValue(ctx, reject);
	if (JS_IsException(r))
		return take_exception(s);
	JS_FreeValue(ctx, r);
	return s->interrupted ? WL_INTERRUPTED : WL_OK;
}

static void readback_headroom(wl_slot *s)
{
	JS_SetMemoryLimit(s->rt, s->memory_limit + WL_READBACK_HEADROOM);
}

// read_cstr converts v with JS_ToCStringLen into *t and keeps the C string
// for wl_release. A failed conversion clears its exception, leaves *t "",
// and returns 0.
static int read_cstr(wl_slot *s, JSValueConst v, wl_text *t)
{
	const char *p;
	size_t len;

	t->ptr = "";
	t->len = 0;
	if (s->ncstr == (int)(sizeof(s->cstr) / sizeof(s->cstr[0])))
		return 0;
	p = JS_ToCStringLen(s->ctx, &len, v);
	if (JS_HasException(s->ctx))
		JS_FreeValue(s->ctx, JS_GetException(s->ctx));
	if (p == NULL)
		return 0;
	s->cstr[s->ncstr++] = p;
	t->ptr = p;
	t->len = len;
	return 1;
}

// keep_cstr is read_cstr's text, "" when the conversion fails.
static wl_text keep_cstr(wl_slot *s, JSValueConst v)
{
	wl_text t;

	read_cstr(s, v, &t);
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

// data_text reads obj's property name into *t without running the
// program's code, and returns whether there is one: the first data
// property of that name along the prototype chain, when its value is a
// string. An accessor, a Proxy on the chain or a value that is not a string
// ends the search with none, so no getter or trap is called.
static int data_text(wl_slot *s, JSValueConst obj, const char *name, wl_text *t)
{
	JSContext *ctx = s->ctx;
	JSAtom atom = JS_NewAtom(ctx, name);
	JSValue cur = JS_DupValue(ctx, obj), next;
	JSPropertyDescriptor desc;
	int found = 0, r, depth;

	t->ptr = "";
	t->len = 0;
	if (atom == JS_ATOM_NULL) {
		JS_FreeValue(ctx, JS_GetException(ctx));
		JS_FreeValue(ctx, cur);
		return 0;
	}
	for (depth = 0; depth < 1024 && JS_IsObject(cur) && !JS_IsProxy(cur); depth++) {
		r = JS_GetOwnProperty(ctx, &desc, cur, atom);
		if (r < 0) {
			JS_FreeValue(ctx, JS_GetException(ctx));
			break;
		}
		if (r > 0) {
			if (!(desc.flags & JS_PROP_GETSET) && JS_IsString(desc.value))
				found = read_cstr(s, desc.value, t);
			JS_FreeValue(ctx, desc.value);
			JS_FreeValue(ctx, desc.getter);
			JS_FreeValue(ctx, desc.setter);
			break;
		}
		next = JS_GetPrototype(ctx, cur);
		JS_FreeValue(ctx, cur);
		cur = next;
		if (JS_IsException(cur)) {
			JS_FreeValue(ctx, JS_GetException(ctx));
			break;
		}
	}
	JS_FreeValue(ctx, cur);
	JS_FreeAtom(ctx, atom);
	return found;
}

// coerced_text reads obj[name] into *t as rquickjs 0.11.0 reads an
// Error's message and stack (Option<Coerced<String>>), and returns whether
// there is one: none when reading the property throws, when its value is
// undefined or null, or when converting it to a string throws. The text is
// the engine's conversion, an unpaired surrogate in its three-byte form;
// that it must also be UTF-8 is the pipeline's to check (JE-52).
static int coerced_text(wl_slot *s, JSValueConst obj, const char *name, wl_text *t)
{
	JSValue v = JS_GetPropertyStr(s->ctx, obj, name), str;
	int ok = 0;

	t->ptr = "";
	t->len = 0;
	if (JS_IsException(v)) {
		JS_FreeValue(s->ctx, JS_GetException(s->ctx));
		return 0;
	}
	if (!JS_IsUndefined(v) && !JS_IsNull(v) && !JS_IsUninitialized(v)) {
		str = JS_ToString(s->ctx, v);
		if (JS_IsException(str)) {
			JS_FreeValue(s->ctx, JS_GetException(s->ctx));
		} else {
			ok = read_cstr(s, str, t);
			JS_FreeValue(s->ctx, str);
		}
	}
	JS_FreeValue(s->ctx, v);
	return ok;
}

// value_kind classifies a thrown value that is not an Error as rquickjs
// 0.11.0's Value::type_of does: by tag, and an object by the first of these
// tests that holds, in its order: array, constructor, function, promise,
// proxy (an Error is an exception there, which wl_exception has taken).
// rquickjs tests a promise by JS_PromiseState >= 0, which works because the
// quickjs-ng it vendors adds JS_PROMISE_NOT_A_PROMISE = -1 to that enum;
// this revision's enum has no negative member, so its -1 compares as
// unsigned, and the class is tested with JS_IsPromise instead.
static int value_kind(wl_slot *s, JSValueConst v)
{
	switch (JS_VALUE_GET_NORM_TAG(v)) {
	case JS_TAG_BOOL:
		return WL_KIND_BOOL;
	case JS_TAG_INT:
		return WL_KIND_INT;
	case JS_TAG_FLOAT64:
		return WL_KIND_FLOAT;
	case JS_TAG_STRING:
		return WL_KIND_STRING;
	case JS_TAG_SYMBOL:
		return WL_KIND_SYMBOL;
	case JS_TAG_BIG_INT:
	case JS_TAG_SHORT_BIG_INT:
		return WL_KIND_BIG_INT;
	case JS_TAG_OBJECT:
		if (JS_IsArray(v))
			return WL_KIND_ARRAY;
		if (JS_IsConstructor(s->ctx, v))
			return WL_KIND_CONSTRUCTOR;
		if (JS_IsFunction(s->ctx, v))
			return WL_KIND_FUNCTION;
		if (JS_IsPromise(v))
			return WL_KIND_PROMISE;
		if (JS_IsProxy(v))
			return WL_KIND_PROXY;
		return WL_KIND_OBJECT;
	}
	return WL_KIND_UNKNOWN;
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
		// The message and the stack, in that order, are what Windmill's
		// embedding reads, and it may run the program's getters and
		// conversions to read them. Nothing else here runs the program's
		// code: the name is read from data properties only, and the
		// Error's string form is left to the caller to compose.
		out->thrown = WL_THROWN_ERROR;
		out->has_message = coerced_text(s, v, "message", &out->message);
		out->has_stack = coerced_text(s, v, "stack", &out->stack);
		data_text(s, v, "name", &out->name);
	} else {
		out->thrown = WL_THROWN_OTHER;
		out->kind = value_kind(s, v);
		switch (out->kind) {
		case WL_KIND_BOOL:
			out->bool_value = JS_VALUE_GET_BOOL(v);
			break;
		case WL_KIND_INT:
			out->int_value = JS_VALUE_GET_INT(v);
			break;
		case WL_KIND_FLOAT:
			out->float_value = JS_VALUE_GET_FLOAT64(v);
			break;
		case WL_KIND_STRING:
		case WL_KIND_BIG_INT:
		case WL_KIND_UNKNOWN:
			break;
		default:
			out->address = (uintptr_t)JS_VALUE_GET_PTR(v);
			break;
		}
		// Only a string, a number and a boolean are converted to text: the
		// debug form needs a string's text, and the others cost a bounded
		// conversion that runs none of the program's code. A symbol's or an
		// object's conversion could run it (toString, Symbol.toPrimitive, a
		// Proxy's traps), and a BigInt's decimal text can take seconds to
		// write, while the debug form needs only their kind and an address
		// (JE-21).
		switch (out->kind) {
		case WL_KIND_BOOL:
		case WL_KIND_INT:
		case WL_KIND_FLOAT:
		case WL_KIND_STRING:
			out->text = keep_cstr(s, v);
			break;
		}
	}
	return s->interrupted ? WL_INTERRUPTED : WL_OK;
}
