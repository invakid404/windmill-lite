package main

import (
	"context"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/expr/quickjs"
)

// The flow suite's host mode (hostmode.go, F3b review round 1) on one
// small fixture.

func hostChecker(t *testing.T) (*flowChecker, func()) {
	t.Helper()
	bun := bunOrSkip(t)
	js, err := quickjs.New(quickjs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return &flowChecker{bun: bun, repo: repo(t), mode: ModeHost, js: js}, func() { js.Close() }
}

// A host lost after its fixture's runs have ended, while the engine proxy
// it spawned lives on, fails every run it took: one generation serves a
// fixture, whatever its runs observed.
func TestHostModeHostLossFailsTheFixture(t *testing.T) {
	fc, done := hostChecker(t)
	defer done()
	f, cleanup, err := loadFixture("../../conformance/flows/client-api/job-env")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	killed := false
	fc.afterRuns = func(h *hostRuntime) {
		if err := h.cmd.Process.Kill(); err != nil {
			t.Error(err)
			return
		}
		select {
		case <-h.rt.Done():
		case <-time.After(5 * time.Second):
			t.Error("the runtime did not see the host's connection end")
		}
		killed = true
	}
	results := fc.runFixture(context.Background(), f)
	if !killed || len(results) == 0 {
		t.Fatalf("killed %v, %d results", killed, len(results))
	}
	for _, r := range results {
		if r.unsupported == "" && !strings.Contains(strings.Join(r.problems, "\n"), "the fixture's host was lost before its engine closed") {
			t.Errorf("run %s: %q; want it failed for the lost host", r.run, r.problems)
		}
	}
}

// A fixture's host leaves no descriptor open once it has closed: its
// socket and child channel, which the runtime does not own, are closed,
// not left to a finalizer.
func TestHostModeClosesItsChannels(t *testing.T) {
	fc, done := hostChecker(t)
	defer done()
	f, cleanup, err := loadFixture("../../conformance/flows/client-api/job-env")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	run := func() {
		t.Helper()
		for _, r := range fc.runFixture(context.Background(), f) {
			if len(r.problems) > 0 || r.unsupported != "" {
				t.Fatalf("%+v", r)
			}
		}
	}
	open := func() int {
		n := 0
		for fd := 0; fd < 4096; fd++ {
			var st syscall.Stat_t
			if syscall.Fstat(fd, &st) == nil {
				n++
			}
		}
		return n
	}
	// The first run warms the poller and the build's caches.
	run()
	runtime.GC()
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	before := open()
	for range 4 {
		run()
	}
	if after := open(); after > before {
		t.Fatalf("four closed fixtures left %d descriptors open (%d before, %d after), with no garbage collection", after-before, before, after)
	}
}
