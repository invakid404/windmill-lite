package quickjs

/*
#cgo CFLAGS: -O2 -D_GNU_SOURCE -funsigned-char -Wno-unused-parameter -Wno-sign-compare
#cgo LDFLAGS: -lm
#include "wl_quickjs.h"
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"runtime/cgo"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/invakid404/windmill-lite/internal/expr"
)

// minThreadStack is the least stack a slot thread gets, and threadStackSlack
// the least it gets beyond the engine's stack limit. The engine measures its
// limit from where each call enters it (JE-48); a thread stack well above
// that limit makes the engine's RangeError, never a thread overflow, what a
// deep recursion meets. Threads that the Go runtime creates have the C
// library's default stack, which musl sets at 128 KiB, so slots make their
// own.
const (
	minThreadStack   = 8 << 20
	threadStackSlack = 4 << 20
)

func threadStack(l expr.Limits) int {
	return max(minThreadStack, l.StackBytes+threadStackSlack)
}

// slot is one evaluation slot: a C thread running loop, and the C state of
// its current runtime.
type slot struct {
	e     *Engine
	c     *C.wl_slot
	ready chan error

	// mu orders interrupt requests against the start and end of
	// evaluations: an interrupt counts only while gen is the generation it
	// was armed for.
	mu  sync.Mutex
	gen uint64
}

func (e *Engine) startSlot() error {
	s := &slot{e: e, c: C.wl_slot_new(), ready: make(chan error, 1)}
	if s.c == nil {
		return errors.New("expr/quickjs: cannot allocate a slot")
	}
	h := cgo.NewHandle(s)
	e.wg.Add(1)
	if rc := C.wl_spawn(C.uintptr_t(h), C.size_t(threadStack(e.limits))); rc != 0 {
		e.wg.Done()
		h.Delete()
		C.wl_slot_free(s.c)
		return fmt.Errorf("expr/quickjs: cannot start a slot thread: %w", syscall.Errno(rc))
	}
	return <-s.ready
}

//export wlSlotMain
func wlSlotMain(h C.uintptr_t) {
	handle := cgo.Handle(h)
	s := handle.Value().(*slot)
	defer handle.Delete()
	defer s.e.wg.Done()
	defer C.wl_slot_free(s.c)
	s.loop()
}

// errPrepare reports a runtime or context that could not be created.
var errPrepare = errors.New("expr/quickjs: cannot create a runtime")

// loop prepares a runtime, waits for a job, runs it, frees the runtime and
// starts over, until the Engine closes.
func (s *slot) loop() {
	first := true
	for {
		if C.wl_prepare(s.c, C.size_t(s.e.limits.MemoryMB)<<20, C.size_t(s.e.limits.StackBytes)) != C.WL_OK {
			if first {
				s.ready <- errPrepare
				return
			}
			// The process is out of memory; try again shortly.
			select {
			case <-time.After(10 * time.Millisecond):
				continue
			case <-s.e.closing:
				return
			}
		}
		if first {
			s.ready <- nil
			first = false
		}
		for used := false; !used; {
			select {
			case j := <-s.e.jobs:
				var r reply
				r, used = s.run(j)
				j.reply <- r
			case <-s.e.closing:
				C.wl_release(s.c)
				return
			}
		}
		C.wl_release(s.c)
	}
}

// run runs j on the prepared runtime and reports whether it used it.
func (s *slot) run(j *job) (reply, bool) {
	if j.compile {
		s.e.stats.compiles.Add(1)
		return s.compile(j.source), true
	}
	if j.ctx.Err() != nil {
		// The caller gave up while the job was being handed over; the
		// runtime stays fresh for the next job.
		return reply{err: context.Cause(j.ctx)}, false
	}
	s.e.stats.evaluations.Add(1)
	return s.eval(j), true
}

func (s *slot) compile(source string) reply {
	var bc *C.uint8_t
	var n C.size_t
	if rc := C.wl_compile(s.c, cstr(source), C.size_t(len(source)), &bc, &n); rc != C.WL_OK {
		return reply{err: s.failure(rc, nil)}
	}
	return reply{out: C.GoBytes(unsafe.Pointer(bc), C.int(n))}
}

// arm starts an evaluation's generation and withdraws any interrupt request
// left over from the last one.
func (s *slot) arm() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gen++
	C.wl_clear_interrupt(s.c)
	return s.gen
}

// disarm ends the generation, so that a late request does nothing, and
// withdraws a request that came after the program finished.
func (s *slot) disarm() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gen++
	C.wl_clear_interrupt(s.c)
}

// interrupt asks the program of generation gen to stop. Any goroutine may
// call it.
func (s *slot) interrupt(gen uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gen == gen {
		C.wl_interrupt(s.c)
	}
}

type hostResult struct {
	id  C.uint32_t
	out []byte
	err error
}

func (s *slot) eval(j *job) reply {
	attempt, cancel := context.WithTimeoutCause(j.ctx, s.e.limits.Attempt, expr.ErrAttemptTimeout)
	defer cancel()
	gen := s.arm()
	stop := context.AfterFunc(attempt, func() { s.interrupt(gen) })
	defer func() {
		stop()
		s.disarm()
	}()

	for _, hs := range j.in.Strings {
		if rc := C.wl_def_string(s.c, cstr(hs.Name), C.size_t(len(hs.Name)), cbytes(hs.Text), C.size_t(len(hs.Text))); rc != C.WL_OK {
			return reply{err: s.failure(rc, attempt)}
		}
	}
	for i, hf := range j.in.Funcs {
		if rc := C.wl_def_func(s.c, cstr(hf.Name), C.size_t(len(hf.Name)), C.int(i)); rc != C.WL_OK {
			return reply{err: s.failure(rc, attempt)}
		}
	}
	bc := j.prog.bc
	if rc := C.wl_start(s.c, (*C.uint8_t)(unsafe.Pointer(unsafe.SliceData(bc))), C.size_t(len(bc))); rc != C.WL_OK {
		return reply{err: s.failure(rc, attempt)}
	}

	done := make(chan struct{})
	defer close(done)
	results := make(chan hostResult)
	pending := 0
	for {
		rc := C.wl_run(s.c)
		switch rc {
		case C.WL_OK:
			return s.result(attempt)
		case C.WL_WAITING:
		default:
			return reply{err: s.failure(rc, attempt)}
		}
		pending += s.dispatch(attempt, j.in.Funcs, results, done)
		// Nothing runs until a host call settles; with none outstanding the
		// result can never settle, and only the attempt limit or the caller
		// ends the evaluation (JE-2).
		var r hostResult
		select {
		case r = <-results:
			pending--
		case <-attempt.Done():
			return reply{err: s.interrupted(attempt)}
		}
		ok := C.int(1)
		out := r.out
		if r.err != nil {
			ok, out = 0, []byte(r.err.Error())
		}
		if rc := C.wl_settle(s.c, r.id, ok, cbytes(out), C.size_t(len(out))); rc != C.WL_OK {
			return reply{err: s.failure(rc, attempt)}
		}
	}
}

// dispatch starts a goroutine for every queued host call and returns how
// many it started.
func (s *slot) dispatch(ctx context.Context, funcs []expr.HostFunc, results chan<- hostResult, done <-chan struct{}) int {
	n := 0
	for {
		var id C.uint32_t
		var index C.int
		var arg C.wl_text
		if C.wl_take_call(s.c, &id, &index, &arg) == 0 {
			return n
		}
		n++
		call := funcs[index].Call
		a := C.GoStringN(arg.ptr, C.int(arg.len))
		go func() {
			out, err := call(ctx, a)
			select {
			case results <- hostResult{id: id, out: out, err: err}:
			case <-done:
			}
		}()
	}
}

func (s *slot) result(attempt context.Context) reply {
	var t C.wl_text
	switch rc := C.wl_result(s.c, &t); rc {
	case C.WL_OK:
		return reply{out: C.GoBytes(unsafe.Pointer(t.ptr), C.int(t.len))}
	case C.WL_NOT_STRING:
		return reply{err: expr.ErrNotString}
	default:
		return reply{err: s.failure(rc, attempt)}
	}
}

// failure turns a C status into the evaluation's error.
func (s *slot) failure(rc C.int, attempt context.Context) error {
	switch rc {
	case C.WL_EXCEPTION:
		var x C.wl_exc
		if C.wl_exception(s.c, &x) == C.WL_INTERRUPTED && attempt != nil {
			return s.interrupted(attempt)
		}
		return exception(&x)
	case C.WL_INTERRUPTED:
		if attempt != nil {
			return s.interrupted(attempt)
		}
	case C.WL_NOT_STRING:
		return expr.ErrNotString
	}
	return fmt.Errorf("expr/quickjs: unexpected engine status %d", int(rc))
}

// interrupted is the error of an attempt that the interrupt stopped: the
// attempt limit, or the caller's context.
func (s *slot) interrupted(attempt context.Context) error {
	err := context.Cause(attempt)
	if errors.Is(err, expr.ErrAttemptTimeout) {
		s.e.stats.timeouts.Add(1)
		return expr.ErrAttemptTimeout
	}
	s.e.stats.canceled.Add(1)
	return err
}

func exception(x *C.wl_exc) *expr.Exception {
	ex := &expr.Exception{Text: gotext(x.text)}
	switch x.thrown {
	case C.WL_THROWN_ERROR:
		ex.Thrown = expr.ThrownError
		ex.Name = gotext(x.name)
		ex.Message = gotext(x.message)
		ex.Stack = gotext(x.stack)
	case C.WL_THROWN_NULL:
		ex.Thrown, ex.Text = expr.ThrownNull, "null"
	case C.WL_THROWN_UNDEFINED:
		ex.Thrown, ex.Text = expr.ThrownUndefined, "undefined"
	default:
		ex.Thrown = expr.ThrownOther
	}
	return ex
}

func gotext(t C.wl_text) string { return C.GoStringN(t.ptr, C.int(t.len)) }

// cstr and cbytes pass Go memory to C for the length of one call; the C side
// copies what it keeps.
func cstr(s string) *C.char { return (*C.char)(unsafe.Pointer(unsafe.StringData(s))) }

func cbytes(b []byte) *C.char { return (*C.char)(unsafe.Pointer(unsafe.SliceData(b))) }
