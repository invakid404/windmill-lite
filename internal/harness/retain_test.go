package harness

import (
	"runtime"
	"testing"
	"time"
	"weak"
)

// An invocation's call queue keeps no function it has run. The process
// keeps its last settled invocations for their late output (D9); their
// queues must not keep what their calls closed over, the outcome among
// them, so that a finished run's values become unreachable (lever J5).
// Both ways a queue runs are checked: its own goroutine (do), and the
// goroutine that claimed it, up to an outcome (claim, runUntil).
func TestSerialKeepsNoCallItRan(t *testing.T) {
	// big returns a call that closes over 1 MiB, and a weak pointer to it.
	big := func() (func(), weak.Pointer[byte]) {
		b := make([]byte, 1<<20)
		return func() { b[0]++ }, weak.Make(&b[0])
	}
	unreachable := func(w weak.Pointer[byte]) bool {
		runtime.GC()
		runtime.GC()
		return w.Value() == nil
	}

	var s serial
	call, w := big()
	done := make(chan struct{})
	s.do(call, func() { close(done) })
	call = nil
	<-done
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		s.mu.Lock()
		idle := !s.running
		s.mu.Unlock()
		if idle || time.Now().After(deadline) {
			break
		}
	}
	if !unreachable(w) {
		t.Fatal("a queue that ran its calls on its own goroutine still holds what they closed over")
	}

	var c serial
	call2, w2 := big()
	delivered := make(chan int, 1)
	if !c.claim(call2, func() { delivered <- 1 }) {
		t.Fatal("an idle queue was not claimed")
	}
	call2 = nil
	c.runUntil(func() bool { return len(delivered) > 0 })
	if !unreachable(w2) {
		t.Fatal("a queue its claimer ran still holds what its calls closed over")
	}
	runtime.KeepAlive(&s)
	runtime.KeepAlive(&c)
}
