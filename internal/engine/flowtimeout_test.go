package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// The flow timeout tests (FX-52) run synthetic flows on the real
// QuickJS-ng engine, with steps answered in Go; the fx-52-flow-timeout
// and cf-27-flow-timeouts fixtures of conformance/flows run the same
// limits on the real Bun harness (cmd/wltest, TestFlowSuite).

// hangs answers f/t/zero as a step that never settles until its context
// ends, f/t/obj as one that takes 1.5 s and returns 1500, and every other
// script as scripts does.
type hangs struct{ scripts }

func (h *hangs) Execute(ctx context.Context, inv Invocation) (StepOutcome, error) {
	switch inv.Script {
	case "f/t/zero":
		return waits{}.Execute(ctx, inv)
	case "f/t/obj":
		defer inv.Report.Settled(SettleCompleted)
		select {
		case <-time.After(1500 * time.Millisecond):
			return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer([]byte("1500"))}, nil
		case <-ctx.Done():
			return StepOutcome{}, context.Cause(ctx)
		}
	}
	return h.scripts.Execute(ctx, inv)
}

// The values of a flow that its timeout of 1 s ended (FX-52 rules 3 to 6):
// the flow job's canceled value, the timeout error its failure module and
// its caller's step get, and a running step's killed job's error.
const (
	timedOutValue = `{"error":{"message":"Job canceled: the flow exceeded its 1s timeout by timeout","name":"Canceled","reason":"the flow exceeded its 1s timeout","canceler":"timeout"}}`
	timeoutError  = `{"error":{"message":"flow terminated due to timeout: the flow exceeded its 1s timeout","name":"ExecutionErr"}}`
)

func killedByTimeout(step string) string {
	return `{"error":{"message":"execution error:\njob process terminated due to cancelled by timeout (reason: the flow exceeded its 1s timeout)","name":"ExecutionErr","step_id":"` + step + `"}}`
}

// withValue adds the flow-level fields fields to a flow value.
func withValue(flow, fields string) string {
	return strings.TrimSuffix(flow, "}") + "," + fields + "}"
}

// FX-52: a flow's value.timeout limits its run's wall-clock time. Once it
// has passed, the flow is canceled with its steps, a running one killed;
// its failure module runs once with the timeout error, and the flow ends
// canceled with the canceled value, whatever that module returned. A
// value of 0 or less is unset. A subflow step's positive timeout replaces
// its subflow's own; a subflow that its own limit ended is a failed step,
// whose job is canceled, carrying the timeout error or the subflow's
// failure module's result, under its caller's retry, continue_on_error
// and failure module. A subflow that an ancestor's timeout canceled ends
// as any canceled child, without its failure module.
func TestFlowTimeout(t *testing.T) {
	fm := `"failure_module":` + mod("failure", "echo", inputs("e", js("error")), "")
	hung := flowOf(mod("x", "zero", "", ""))
	for _, c := range []struct {
		name  string
		flows map[string]string
		check func(t *testing.T, tr *Trace)
	}{
		{"root", map[string]string{"f/t/main": withValue(flowOf(mod("a", "zero", "", "")), `"timeout":1`)}, func(t *testing.T, tr *Trace) {
			wantStatus(t, tr, conformance.RunCanceled, timedOutValue)
			a := step(t, tr, "a")
			if a.Status != conformance.StepCanceled || !a.Abandoned {
				t.Fatalf("a %+v", a)
			}
			sameJSON(t, "a's result", a.Result, killedByTimeout("a"))
		}},
		{"root failure module", map[string]string{"f/t/main": withValue(flowOf(mod("a", "zero", "", "")), `"timeout":1,`+fm)}, func(t *testing.T, tr *Trace) {
			wantStatus(t, tr, conformance.RunCanceled, timedOutValue)
			f := at(t, tr, "s:failure")
			if f.Status != conformance.StepSuccess {
				t.Fatalf("the failure module %+v", f)
			}
			sameJSON(t, "the failure module's result", f.Result, `{"e":{"message":"flow terminated due to timeout: the flow exceeded its 1s timeout","name":"ExecutionErr"}}`)
		}},
		{"recovering failure module", map[string]string{"f/t/main": withValue(flowOf(mod("a", "zero", "", "")), `"timeout":1,"failure_module":`+mod("failure", "value", inputs("v", static(`{"recover":true}`)), ""))}, func(t *testing.T, tr *Trace) {
			wantStatus(t, tr, conformance.RunCanceled, timedOutValue)
		}},
		// A step waiting on the sleep before it is canceled at its pickup,
		// with the canceled value, without running.
		{"sleeping", map[string]string{"f/t/main": withValue(flowOf(mod("a", "value", inputs("v", static("1")), `"sleep":`+static("6")), mod("b", "value", inputs("v", static("2")), "")), `"timeout":1`)}, func(t *testing.T, tr *Trace) {
			wantStatus(t, tr, conformance.RunCanceled, timedOutValue)
			b := step(t, tr, "b")
			if b.Status != conformance.StepCanceled {
				t.Fatalf("b %+v", b)
			}
			sameJSON(t, "b's result", b.Result, timedOutValue)
		}},
		{"zero", map[string]string{"f/t/main": withValue(flowOf(mod("a", "value", inputs("v", static("1")), "")), `"timeout":0`)}, func(t *testing.T, tr *Trace) {
			wantStatus(t, tr, conformance.RunSuccess, `1`)
		}},
		{"negative", map[string]string{"f/t/main": withValue(flowOf(mod("a", "obj", "", "")), `"timeout":-1`)}, func(t *testing.T, tr *Trace) {
			wantStatus(t, tr, conformance.RunSuccess, `1500`)
		}},
		{"subflow's own", map[string]string{
			"f/t/sub":  withValue(hung, `"timeout":1`),
			"f/t/main": flowOf(subflowOf("c", "f/t/sub", "", "")),
		}, func(t *testing.T, tr *Trace) {
			wantStatus(t, tr, conformance.RunFailure, timeoutError)
			c := at(t, tr, "m:c")
			if c.Status != conformance.StepCanceled || c.Module != conformance.ModuleFailure {
				t.Fatalf("c %+v", c)
			}
			sameJSON(t, "c's job", c.Result, timedOutValue)
			sameJSON(t, "c's value", c.Carried, timeoutError)
			x := at(t, tr, "m:c/m:x")
			if x.Status != conformance.StepCanceled {
				t.Fatalf("x %+v", x)
			}
			sameJSON(t, "x's result", x.Result, killedByTimeout("x"))
		}},
		{"a step's timeout replaces the subflow's", map[string]string{
			"f/t/sub":  withValue(hung, `"timeout":30`),
			"f/t/main": flowOf(subflowOf("c", "f/t/sub", "", `"timeout":`+static("1"))),
		}, func(t *testing.T, tr *Trace) {
			wantStatus(t, tr, conformance.RunFailure, timeoutError)
		}},
		{"a step's longer timeout replaces the subflow's", map[string]string{
			"f/t/sub":  withValue(flowOf(mod("x", "obj", "", "")), `"timeout":1`),
			"f/t/main": flowOf(subflowOf("c", "f/t/sub", "", `"timeout":`+js("10"))),
		}, func(t *testing.T, tr *Trace) {
			wantStatus(t, tr, conformance.RunSuccess, `1500`)
		}},
		{"a step's zero timeout leaves the subflow's", map[string]string{
			"f/t/sub":  withValue(hung, `"timeout":1`),
			"f/t/main": flowOf(subflowOf("c", "f/t/sub", "", `"timeout":`+static("0"))),
		}, func(t *testing.T, tr *Trace) {
			wantStatus(t, tr, conformance.RunFailure, timeoutError)
		}},
		{"tolerated", map[string]string{
			"f/t/sub":  withValue(hung, `"timeout":1`),
			"f/t/main": flowOf(subflowOf("c", "f/t/sub", "", `"continue_on_error":true`), mod("d", "echo", inputs("x", js("previous_result")), "")),
		}, func(t *testing.T, tr *Trace) {
			wantStatus(t, tr, conformance.RunSuccess, `{"x":`+timeoutError+`}`)
		}},
		{"retried", map[string]string{
			"f/t/sub":  withValue(hung, `"timeout":1`),
			"f/t/main": flowOf(subflowOf("c", "f/t/sub", "", `"retry":{"constant":{"attempts":1,"seconds":0}}`)),
		}, func(t *testing.T, tr *Trace) {
			wantStatus(t, tr, conformance.RunFailure, timeoutError)
			if r0 := record(t, tr, attemptSite("c", 0)); r0.Status != conformance.StepCanceled {
				t.Fatalf("the first attempt %+v", r0)
			}
		}},
		{"subflow failure module", map[string]string{
			"f/t/sub":  withValue(hung, `"timeout":1,`+fm),
			"f/t/main": flowOf(subflowOf("c", "f/t/sub", "", "")),
		}, func(t *testing.T, tr *Trace) {
			wantStatus(t, tr, conformance.RunFailure, `{"e":{"message":"flow terminated due to timeout: the flow exceeded its 1s timeout","name":"ExecutionErr"}}`)
			c := at(t, tr, "m:c")
			if c.Status != conformance.StepCanceled {
				t.Fatalf("c %+v", c)
			}
			sameJSON(t, "c's job", c.Result, timedOutValue)
			if f := at(t, tr, "m:c/s:failure"); f.Status != conformance.StepSuccess {
				t.Fatalf("the subflow's failure module %+v", f)
			}
		}},
		{"an ancestor's timeout", map[string]string{
			"f/t/sub":  withValue(hung, fm),
			"f/t/main": withValue(flowOf(subflowOf("c", "f/t/sub", "", "")), `"timeout":1`),
		}, func(t *testing.T, tr *Trace) {
			wantStatus(t, tr, conformance.RunCanceled, timedOutValue)
			if c := at(t, tr, "m:c"); c.Status != conformance.StepCanceled {
				t.Fatalf("c %+v", c)
			}
			sameJSON(t, "x's result", at(t, tr, "m:c/m:x").Result, killedByTimeout("x"))
			noRecord(t, tr, "m:c/s:failure")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := mustOpen(t, c.flows, &hangs{})
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			start := time.Now()
			tr, err := e.Run(ctx, RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{}`)), Seed: 1})
			if err != nil {
				t.Fatal(err)
			}
			c.check(t, tr)
			// Nothing waits for a limit of 30 s or for the test's deadline.
			if d := time.Since(start); d > 10*time.Second {
				t.Fatalf("the run took %v", d)
			}
		})
	}
}
