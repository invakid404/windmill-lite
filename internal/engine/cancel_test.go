package engine

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// [FX-38]/b, [CF-28] rule 2: a flow step whose job waits on the previous
// step's sleep when the run is canceled fails at pickup with the Canceled
// value, a failure and not a canceled job. Its job is the subflow's flow
// job, which starts none of the subflow's modules, and the trace lists the
// subflow with its modules and its failure module, none of which ran.
//
// [FX-38]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/execution.md#fx-38
// [CF-28]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/control-flow.md#cf-28
func TestSubflowCanceledWhileWaiting(t *testing.T) {
	s := &scripts{}
	e := mustOpen(t, map[string]string{
		"f/t/child": withFailure(handler("echo", "", ""), mod("c", "value", inputs("v", static("2")), "")),
		"f/t/main":  flowOf(mod("a", "value", inputs("v", static("1")), `"sleep":`+static("30")), subflowOf("b", "f/t/child", "", "")),
	}, s)
	ctx, cancel := context.WithCancelCause(context.Background())
	time.AfterFunc(100*time.Millisecond, func() { cancel(&Cancellation{Reason: "r", Canceler: "u"}) })
	start := time.Now()
	tr, err := e.Run(ctx, RunRequest{Path: "f/t/main", Args: jsonval.FromProducer([]byte(`{}`))})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("the sleep was not cut short")
	}
	const canceled = `{"error":{"message":"Job canceled: r by u","name":"Canceled","reason":"r","canceler":"u"}}`
	wantStatus(t, tr, conformance.RunCanceled, canceled)
	b := step(t, tr, "b")
	if b.Status != conformance.StepFailure || !b.Ran || b.Module != conformance.ModuleFailure || b.Job == "" {
		t.Fatalf("b %+v", b)
	}
	sameJSON(t, "b's result", b.Result, canceled)
	if len(tr.Children) != 1 {
		t.Fatalf("children %+v", tr.Children)
	}
	if c := tr.Children[0]; c.Site.String() != "m:b" || c.ID != b.Job || !slices.Equal(c.Modules, []string{"c"}) || !c.Failure {
		t.Fatalf("the subflow %+v", c)
	}
	noRecord(t, tr, "m:b/m:c")
	noRecord(t, tr, "m:b/s:failure")
	if calls := s.calls(); !slices.Equal(calls, []string{"m:a"}) {
		t.Fatalf("calls %v", calls)
	}
}
