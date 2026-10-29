// The C side of the native backend: a thin layer over the vendored
// QuickJS-ng that the Go slot loop drives, one call per step. Every function
// except wl_spawn and wl_interrupt runs on the slot's own thread.

#ifndef WL_QUICKJS_H
#define WL_QUICKJS_H

#include <stddef.h>
#include <stdint.h>

// Statuses returned by the stepping functions.
enum {
	WL_OK = 0,
	WL_EXCEPTION = 1,   // the program threw or its Promise rejected; see wl_exception
	WL_INTERRUPTED = 2, // the interrupt handler stopped the program
	WL_WAITING = 3,     // the result Promise is pending and no job is queued
	WL_NOT_STRING = 4,  // the completion value is not a string
	WL_NO_MEMORY = 5,   // a runtime or context could not be created
};

// What a program threw, read back by wl_exception.
enum {
	WL_THROWN_ERROR = 1,
	WL_THROWN_NULL = 2,
	WL_THROWN_UNDEFINED = 3,
	WL_THROWN_OTHER = 4,
};

typedef struct wl_text {
	const char *ptr;
	size_t len;
} wl_text;

// The type of a thrown value that is not an Error, as rquickjs 0.11.0's
// Value::type_of classifies it (JE-21): by tag, and an object by the first
// of array, constructor, function, promise and proxy that it is.
enum {
	WL_KIND_UNKNOWN = 0,
	WL_KIND_BOOL = 1,
	WL_KIND_INT = 2,
	WL_KIND_FLOAT = 3,
	WL_KIND_STRING = 4,
	WL_KIND_SYMBOL = 5,
	WL_KIND_ARRAY = 6,
	WL_KIND_CONSTRUCTOR = 7,
	WL_KIND_FUNCTION = 8,
	WL_KIND_PROMISE = 9,
	WL_KIND_PROXY = 10,
	WL_KIND_OBJECT = 11,
	WL_KIND_BIG_INT = 12,
};

typedef struct wl_exc {
	int thrown;
	// name is an Error's name, read from data properties only; text is the
	// string form of a thrown string, number or boolean, and empty for
	// anything else.
	wl_text name, message, text, stack;
	// For WL_THROWN_ERROR: whether message and stack were read. Each is
	// absent when its property is undefined or null, or when reading it or
	// converting it to a string throws.
	int has_message, has_stack;
	// For WL_THROWN_OTHER: the value's kind, and its payload: int_value,
	// float_value or bool_value for the numbers and booleans, text for a
	// string, and address, the engine's pointer, for a symbol or an object.
	int kind;
	int32_t int_value;
	double float_value;
	int bool_value;
	uintptr_t address;
} wl_exc;

typedef struct wl_slot wl_slot;

// wl_spawn starts a detached thread with a stack of stack_bytes that calls
// the Go function wlSlotMain(handle). It returns 0 or an errno value.
int wl_spawn(uintptr_t handle, size_t stack_bytes);

wl_slot *wl_slot_new(void);
void wl_slot_free(wl_slot *s);

// wl_prepare creates the slot's fresh runtime and full context with the
// given heap cap and stack size (both in bytes). WL_OK or WL_NO_MEMORY.
int wl_prepare(wl_slot *s, size_t memory_limit, size_t stack_size);
// wl_release frees everything the evaluation created, then the context and
// the runtime, then every block of the runtime's heap that is still
// allocated.
void wl_release(wl_slot *s);
// wl_leaked is the number of blocks the last wl_release found still
// allocated after the runtime was freed.
size_t wl_leaked(wl_slot *s);

// wl_interrupt asks the running program to stop. Any thread may call it.
void wl_interrupt(wl_slot *s);
// wl_clear_interrupt withdraws a request that came too late to take effect.
void wl_clear_interrupt(wl_slot *s);

// wl_compile compiles src (len bytes, any content) as a strict-mode global
// script named eval_script (JE-50) and serialises its bytecode. On WL_OK,
// *bc holds *bc_len bytes, valid until wl_release.
int wl_compile(wl_slot *s, const char *src, size_t len, uint8_t **bc, size_t *bc_len);

// wl_begin starts an evaluation on the prepared runtime, before its
// bindings are defined.
void wl_begin(wl_slot *s);

// wl_def_string defines a global data property name = text (a string).
int wl_def_string(wl_slot *s, const char *name, size_t name_len, const char *text, size_t text_len);
// wl_def_func defines a global function name that queues a host call to
// function index and returns a Promise.
int wl_def_func(wl_slot *s, const char *name, size_t name_len, int index);

// wl_start runs the bytecode's top level and keeps its completion value.
int wl_start(wl_slot *s, const uint8_t *bc, size_t bc_len);
// wl_run runs every pending job, the jobs they queue included, until none
// is left, dropping the exception of a job that throws (JE-51), and then
// reads the completion value. It returns WL_OK when it is fulfilled (or is
// no Promise), WL_EXCEPTION, WL_INTERRUPTED or WL_WAITING.
int wl_run(wl_slot *s);

// wl_take_call hands over the oldest queued host call: its id, function
// index and argument text, valid until wl_free_arg, wl_settle or
// wl_release. It returns 0 when no call is queued.
int wl_take_call(wl_slot *s, uint32_t *id, int *index, wl_text *arg);
// wl_free_arg frees the argument of taken call id, once it has been copied.
void wl_free_arg(wl_slot *s, uint32_t id);
// wl_settle resolves call id with text (ok != 0) or rejects it with
// new Error(text), and frees its record for reuse. WL_OK, WL_EXCEPTION or
// WL_INTERRUPTED.
int wl_settle(wl_slot *s, uint32_t id, int ok, const char *text, size_t len);

// wl_result reads the fulfilled completion value as UTF-8, valid until
// wl_release. WL_OK, WL_NOT_STRING or WL_EXCEPTION.
int wl_result(wl_slot *s, wl_text *out);
// wl_exception describes the exception of the last WL_EXCEPTION. Its texts
// are valid until wl_release. Reading an Error's message and stack can run
// the program's own getters and conversions, as Windmill's embedding reads
// them, so it returns WL_INTERRUPTED if the interrupt stopped one of them,
// else WL_OK; nothing else it reads runs the program's code.
int wl_exception(wl_slot *s, wl_exc *out);

#endif
