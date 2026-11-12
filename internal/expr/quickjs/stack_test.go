package quickjs

import (
	"context"
	"fmt"
	"math"
	"runtime"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/expr"
)

func TestStackLimitScalesToTheCallStack(t *testing.T) {
	for _, tc := range []struct{ stackBytes, perCall, want int }{
		{1 << 20, referenceCallStack, 1 << 20},
		{1 << 20, 700, 1 << 20}, // smaller frames keep the configured stack
		{1 << 20, 1536, 2 << 20},
		{1 << 20, 1728, 2359296},
		{64 << 10, 1008, 86016},
		{1, 769, 2}, // rounded up
		{math.MaxInt / 2, 2000, math.MaxInt},
	} {
		if got := stackLimit(tc.stackBytes, tc.perCall); got != tc.want {
			t.Errorf("stackLimit(%d, %d) = %d, want %d", tc.stackBytes, tc.perCall, got, tc.want)
		}
	}
}

func TestThreadStack(t *testing.T) {
	for _, tc := range []struct{ limit, want int }{
		{1 << 20, 8 << 20},
		{4 << 20, 8 << 20},
		{4<<20 + 1, 9 << 20},
		{16 << 20, 20 << 20},
		{2359296, 8 << 20},
		{14155776, 18 << 20},
	} {
		if got := threadStack(tc.limit); got != tc.want {
			t.Errorf("threadStack(%d) = %d, want %d", tc.limit, got, tc.want)
		}
	}
}

func TestStackLimitTooLarge(t *testing.T) {
	l := expr.DefaultLimits()
	l.StackBytes = math.MaxInt - 1
	e, err := New(Options{Limits: l, Slots: 1})
	if err == nil {
		e.Close()
		t.Fatal("a stack limit that no thread can hold was accepted")
	}
	if !strings.Contains(err.Error(), "too large for a thread") {
		t.Fatalf("New: %v", err)
	}
}

func TestCallStackBytes(t *testing.T) {
	perCall, err := CallStackBytes()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s/%s: %d bytes of C stack per call", runtime.GOOS, runtime.GOARCH, perCall)
	// The probe runs once per process; the builds CI measures take 700 to
	// 1 800 bytes. Anything outside these bounds is a broken probe.
	if perCall < 128 || perCall > 64<<10 {
		t.Fatalf("%d bytes per call", perCall)
	}
	if again, _ := CallStackBytes(); again != perCall {
		t.Fatalf("measured %d, then %d", perCall, again)
	}
	for _, stackBytes := range []int{64 << 10, 1 << 20, 6 << 20} {
		l := expr.DefaultLimits()
		l.StackBytes = stackBytes
		e := newEngine(t, Options{Limits: l, Slots: 1})
		if got, want := e.StackLimit(), stackLimit(stackBytes, perCall); got != want {
			t.Errorf("StackBytes %d: stack limit %d, want %d", stackBytes, got, want)
		}
		if e.Limits().StackBytes != stackBytes {
			t.Errorf("StackBytes %d: Limits reports %d", stackBytes, e.Limits().StackBytes)
		}
	}
}

// je48Depth is the largest k for which the recursion of depth k fits on e,
// in the expression of the depth-1000 case of [JE-48/a].
//
// [JE-48/a]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/conformance.md#je-48
func je48Depth(t *testing.T, e *Engine) int {
	t.Helper()
	fits := func(k int) bool {
		src := strings.ReplaceAll(`String((() => { const f = n => n === 0 ? 0 : 1 + f(n - 1); return f(K); })())`, "K", fmt.Sprint(k))
		out, err := e.Eval(context.Background(), compile(t, e, src), expr.Input{})
		switch {
		case err == nil && string(out) == fmt.Sprint(k):
			return true
		case err != nil && strings.Contains(err.Error(), "RangeError: Maximum call stack size exceeded"):
			return false
		}
		t.Fatalf("depth %d: %s, %v", k, out, err)
		return false
	}
	lo, hi := 0, 1
	for fits(hi) {
		lo, hi = hi, 2*hi
	}
	for hi-lo > 1 {
		if mid := (lo + hi) / 2; fits(mid) {
			lo = mid
		} else {
			hi = mid
		}
	}
	return lo
}

func TestProbeMatchesEvaluations(t *testing.T) {
	// The probe measures the stack per call on a runtime of its own. An
	// evaluation's calls must cost the same: the depth that JE-48/a's
	// expression reaches grows by one call per perCall bytes of scaled
	// limit, and at the default limit it fits StackBytes / 768 calls, less
	// the few that the frames above the recursion take. Between the 256 KiB
	// and 2 MiB limits, rounding moves the slope by at most 2 bytes, and the
	// probe's own measurement by 1; a slot of 16 bytes more or less per
	// call, as another function shape would take, moves it by 16.
	perCall, err := CallStackBytes()
	if err != nil {
		t.Fatal(err)
	}
	limits := []int{256 << 10, 1 << 20, 2 << 20}
	var depth, scaled [3]int
	for i, stackBytes := range limits {
		l := expr.DefaultLimits()
		l.StackBytes = stackBytes
		e := newEngine(t, Options{Limits: l, Slots: 1, CacheEntries: -1})
		depth[i], scaled[i] = je48Depth(t, e), e.StackLimit()
	}
	slope := float64(scaled[2]-scaled[0]) / float64(depth[2]-depth[0])
	t.Logf("%s/%s: the probe measured %d bytes per call, evaluations %.1f; depth %d at 256 KiB, %d at 1 MiB and %d at 2 MiB", runtime.GOOS, runtime.GOARCH, perCall, slope, depth[0], depth[1], depth[2])
	if math.Abs(slope-float64(perCall)) > 6 {
		t.Errorf("evaluations take %.1f bytes per call, the probe measured %d", slope, perCall)
	}
	if least := limits[1] / referenceCallStack * 99 / 100; depth[1] < least {
		t.Errorf("JE-48/a's recursion fits to depth %d at the default limit, want at least %d", depth[1], least)
	}
}
