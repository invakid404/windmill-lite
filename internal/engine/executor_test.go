package engine

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
)

func TestCancelReasonOf(t *testing.T) {
	for name, tc := range map[string]struct {
		ctx  func() context.Context
		want CancelReason
	}{
		"step timeout": {func() context.Context {
			ctx, cancel := context.WithCancelCause(context.Background())
			cancel(CancelTimeout)
			return ctx
		}, CancelTimeout},
		"run ended": {func() context.Context {
			ctx, cancel := context.WithCancelCause(context.Background())
			cancel(CancelRunEnded)
			return ctx
		}, CancelRunEnded},
		"wrapped cause": {func() context.Context {
			ctx, cancel := context.WithCancelCause(context.Background())
			cancel(fmt.Errorf("step a: %w", CancelTimeout))
			return ctx
		}, CancelTimeout},
		"timeout with a cause": {func() context.Context {
			ctx, cancel := context.WithTimeoutCause(context.Background(), time.Nanosecond, CancelTimeout)
			t.Cleanup(cancel)
			<-ctx.Done()
			return ctx
		}, CancelTimeout},
		"plain cancellation": {func() context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}, CancelCanceled},
		"host deadline": {func() context.Context {
			ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			t.Cleanup(cancel)
			return ctx
		}, CancelCanceled},
	} {
		t.Run(name, func(t *testing.T) {
			if got := CancelReasonOf(tc.ctx()); got != tc.want {
				t.Errorf("CancelReasonOf = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTraceStepLooksUpBySite(t *testing.T) {
	a := conformance.SitePath{}.Child(conformance.Module("a"))
	aRetry := a.Child(conformance.Attempt(0))
	failure := conformance.SitePath{}.Child(conformance.FailureSlot())
	tr := &Trace{Steps: []StepRecord{
		{Site: aRetry, Attempt: 0, Status: conformance.StepFailure},
		{Site: a, Attempt: 1, Status: conformance.StepSuccess},
	}}
	if rec, ok := tr.Step(a); !ok || rec.Attempt != 1 || rec.Status != conformance.StepSuccess {
		t.Errorf("Step(%s) = %+v, %v", a, rec, ok)
	}
	if rec, ok := tr.Step(aRetry); !ok || rec.Status != conformance.StepFailure {
		t.Errorf("Step(%s) = %+v, %v", aRetry, rec, ok)
	}
	if _, ok := tr.Step(failure); ok {
		t.Error("found a site that was never recorded")
	}
}

func TestOriginGenerated(t *testing.T) {
	// ADR 0001 §2: only messages windmill-lite generates in Windmill's place
	// may lose the oracle's Rust location suffix in a comparison.
	for o, want := range map[Origin]bool{
		OriginNone: false, OriginUser: false, OriginHost: false,
		OriginEngine: true, OriginHarness: true, OriginFacade: true,
	} {
		if o.Generated() != want {
			t.Errorf("%q.Generated() = %v", o, !want)
		}
	}
}
