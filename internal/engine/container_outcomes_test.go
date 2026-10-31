package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// What a failed container leaves its flow: the retry of a parallel loop,
// continue_on_error after a failed parallel container or a forwarded error
// stop, and a container in the failure module's slot. The flow fixtures
// cf-7-parallel, cf-16-tolerated and cf-22-retried of
// conformance/flows/control-flow run the same flows on the real Bun harness
// against the oracle's recordings (cmd/wltest, TestFlowSuite).

// zeroFails answers f/t/echo by echoing its arguments, except that an
// invocation whose argument i is 0 fails: always, or only the first such
// invocation of each run when once is set. It answers every other script
// as scripts does.
type zeroFails struct {
	scripts
	once bool
	mu   sync.Mutex
	seen map[string]bool
}

func (z *zeroFails) Execute(ctx context.Context, inv Invocation) (StepOutcome, error) {
	if inv.Script != "f/t/echo" {
		return z.scripts.Execute(ctx, inv)
	}
	z.scripts.mu.Lock()
	z.scripts.invs = append(z.scripts.invs, inv)
	z.scripts.mu.Unlock()
	defer inv.Report.Settled(SettleCompleted)
	var args struct {
		I *int `json:"i"`
	}
	if err := json.Unmarshal(inv.Args.Producer(), &args); err != nil {
		return StepOutcome{}, err
	}
	fail := args.I != nil && *args.I == 0
	if fail && z.once {
		z.mu.Lock()
		if z.seen == nil {
			z.seen = map[string]bool{}
		}
		fail = !z.seen[inv.Run]
		z.seen[inv.Run] = true
		z.mu.Unlock()
	}
	if fail {
		return StepOutcome{Kind: OutcomeError, Body: jsonval.FromProducer([]byte(`{"message":"zero","name":"Error","step_id":"` + inv.Env["WM_FLOW_STEP_ID"] + `"}`))}, nil
	}
	return StepOutcome{Kind: OutcomeOK, Body: inv.Args}, nil
}

// A retried parallel loop starts every child it pushes from the stored
// input of its last child to complete, whatever that child's outcome, and
// its entry lists no failed attempt ([CF-22] rule 5). With parallelism 1,
// iteration 0 fails first and iteration 1, which succeeds, completes last,
// so both re-run iterations see iteration 1's iter as iter_parent. When the
// retried loop fails for good in a flow with a failure module, it is marked
// handled, so the flow fails with the loop's array and its own failure
// module does not run.
//
// [CF-22]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/control-flow.md#cf-22
func TestContainerRetryParallelBase(t *testing.T) {
	body := []string{mod("b", "echo", inputs("i", js("flow_input.iter.index"), "p", js("flow_input.iter_parent ?? null")), "")}
	loop := loopOf("l", body, `"iterator":{"type":"static","value":[10,20]},"parallel":true,"parallelism":{"type":"static","value":1},"skip_failures":false`, retryOnce)
	base := `{"index":1,"value":20}`

	z := &zeroFails{once: true}
	tr := runReq(t, mustOpen(t, map[string]string{"f/t/main": flowOf(loop)}, z), RunRequest{})
	wantStatus(t, tr, conformance.RunSuccess, `[{"i":0,"p":`+base+`},{"i":1,"p":`+base+`}]`)
	if l := record(t, tr, site("l")); len(l.FailedRetries) != 0 || l.Attempt != 1 {
		t.Fatalf("l %+v", l)
	}
	if _, ok := tr.Step(attemptSite("l", 0)); ok {
		t.Fatal("a parallel loop's failed attempt has a site")
	}

	z = &zeroFails{}
	tr = runReq(t, mustOpen(t, map[string]string{"f/t/main": withFailure(handler("value", inputs("v", static("1")), ""), loop)}, z), RunRequest{})
	wantStatus(t, tr, conformance.RunFailure, `[1,{"i":1,"p":`+base+`}]`)
	if _, ok := failureStep(tr); ok {
		t.Fatal("the flow's failure module ran")
	}
	at(t, tr, "m:l/i:0/s:failure")
}

// A failed parallel loop or parallel branchall is never tolerated by its
// continue_on_error: its completion leaves the cursor on it, so the
// decision re-enters it, and the flow aborts with an InternalError, at a
// middle or the last position, with or without a failure module, which
// does not run ([CF-7], [CF-16] item 8). A retry that is due runs first.
// A branchone parent takes the aborted flow's last carried value, the
// container's array, as a chaining error at a later transition hands it
// ([CF-23] route 3).
//
// [CF-7]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/control-flow.md#cf-7
// [CF-16]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/control-flow.md#cf-16
// [CF-23]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/control-flow.md#cf-23
func TestContainerParallelFailureNotTolerated(t *testing.T) {
	boom := func(id string) string { return `{"error":{"message":"boom","name":"Error","step_id":"` + id + `"}}` }
	aborted := func(kind string) string {
		return `{"error":{"message":"Bad request: Unrecognized module status for ` + kind + ` Failure { .. }","name":"InternalError"}}`
	}
	coe := `"continue_on_error":true`
	loop := func(envelope string) string {
		return loopOf("l", []string{mod("x", "fail", "", "")}, `"iterator":{"type":"static","value":[1,2]},"parallel":true,"skip_failures":false`, envelope)
	}
	all := func(envelope string) string {
		return branchAllOf("l", []string{branch(skipFailure(false), mod("x", "fail", "", "")), branch(skipFailure(true), value("y", "2", ""))}, true, envelope)
	}
	next := value("n", `"after"`, "")
	for _, c := range []struct {
		name, flow, result string
		handler            bool
	}{
		{"loop, a middle step", flowOf(loop(coe), next), aborted("ForloopFlow"), false},
		{"loop, the last step", flowOf(loop(coe)), aborted("ForloopFlow"), false},
		{"loop, a failure module", withFailure(handler("value", inputs("v", static(`"H"`)), ""), loop(coe), next), aborted("ForloopFlow"), true},
		{"loop, a retry first", flowOf(loop(coe+","+retryOnce), next), aborted("ForloopFlow"), false},
		{"branchall, a middle step", flowOf(all(coe), next), aborted("BranchAll"), false},
		{"branchall, the last step", flowOf(all(coe)), aborted("BranchAll"), false},
		{"loop in a branchone", flowOf(branchOneOf("o", nil, []string{loop(coe), value("z", "0", "")}, ""), next), "[" + boom("x") + "," + boom("x") + "]", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := &scripts{}
			tr := runReq(t, mustOpen(t, map[string]string{"f/t/main": c.flow}, s), RunRequest{})
			wantStatus(t, tr, conformance.RunFailure, c.result)
			noRecord(t, tr, "m:n")
			if _, ok := failureStep(tr); ok {
				t.Fatal("the flow's failure module ran")
			}
			if c.handler {
				for _, p := range []string{"m:l/i:0/s:failure", "m:l/i:1/s:failure"} {
					if r := at(t, tr, p); r.Status != conformance.StepSuccess {
						t.Fatalf("%s %+v", p, r)
					}
				}
			}
			if strings.Contains(c.name, "retry") {
				if n := strings.Count(strings.Join(s.calls(), " "), "m:l/i:0/m:x"); n != 2 {
					t.Fatalf("iteration 0 ran %d times: %v", n, s.calls())
				}
			}
		})
	}
	// Without continue_on_error the failure is an ordinary one: the flow
	// fails with the loop's array.
	tr := runReq(t, mustOpen(t, map[string]string{"f/t/main": flowOf(loop(""), next)}, &scripts{}), RunRequest{})
	wantStatus(t, tr, conformance.RunFailure, "["+boom("x")+","+boom("x")+"]")
}

// The completion of a sequential container that carries an error stop
// forwarded from a child flow turns the container's continue_on_error off,
// wherever the container stands, so the flow fails and the next module
// does not run ([CF-12] item 4). A loop, which clears the stop when it
// breaks, still runs its retry; with skip_failures it succeeds and the
// flow goes on.
//
// [CF-12]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/control-flow.md#cf-12
func TestContainerForwardedStopNotTolerated(t *testing.T) {
	halt := `{"error":{"message":"halt","name":"EarlyStopError"}}`
	stopping := mod("x", "value", inputs("v", static("1")), `"stop_after_if":{"expr":"true","skip_if_stopped":false,"error_message":"halt"}`)
	coe := `"continue_on_error":true`
	loop := func(skip bool, envelope string) string {
		return loopOf("l", []string{stopping}, `"iterator":{"type":"static","value":[1,2]},"skip_failures":`+map[bool]string{true: "true", false: "false"}[skip], envelope)
	}
	next := value("n", `"after"`, "")
	for _, c := range []struct {
		name, flow, result string
		calls              int
	}{
		{"loop, a middle step", flowOf(loop(false, coe), next), "[" + halt + "]", 1},
		{"loop, the last step", flowOf(loop(false, coe)), "[" + halt + "]", 1},
		{"loop, a retry", flowOf(loop(false, coe+","+retryOnce), next), "[" + halt + "]", 2},
		{"while loop", flowOf(whileOf("l", []string{stopping}, false, coe), next), "[" + halt + "]", 1},
		{"branchone, the last step", flowOf(branchOneOf("l", nil, []string{stopping}, coe)), halt, 1},
		{"branchone, a retry", flowOf(branchOneOf("l", nil, []string{stopping}, coe+","+retryOnce)), halt, 1},
		{"sequential branchall, the last step", flowOf(branchAllOf("l", []string{branch(skipFailure(false), stopping)}, false, coe)), "[" + halt + "]", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := &scripts{}
			tr := runReq(t, mustOpen(t, map[string]string{"f/t/main": c.flow}, s), RunRequest{})
			wantStatus(t, tr, conformance.RunFailure, c.result)
			noRecord(t, tr, "m:n")
			if got := len(s.calls()); got != c.calls {
				t.Fatalf("calls %v, want %d", s.calls(), c.calls)
			}
		})
	}
	tr := runReq(t, mustOpen(t, map[string]string{"f/t/main": flowOf(loop(true, coe), next)}, &scripts{}), RunRequest{})
	wantStatus(t, tr, conformance.RunSuccess, `"after"`)
}

// Windmill accepts a container as the failure module, with outcomes that
// depend on bookkeeping by list position ([CF-31]); windmill-lite refuses
// the flow when it opens it, for every kind of container, naming the
// finding.
//
// [CF-31]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/control-flow.md#cf-31
func TestEngineRefusesAContainerAsTheFailureModule(t *testing.T) {
	for kind, value := range map[string]string{
		"forloopflow":   `{"type":"forloopflow","iterator":{"type":"static","value":[1]},"skip_failures":true,"modules":[]}`,
		"whileloopflow": `{"type":"whileloopflow","skip_failures":true,"modules":[]}`,
		"branchone":     `{"type":"branchone","branches":[],"default":[]}`,
		"branchall":     `{"type":"branchall","branches":[]}`,
	} {
		_, err := openEngine(t, map[string]string{"f/t/main": withFailure(`{"id":"failure","value":`+value+`}`, mod("a", "fail", "", ""))}, &scripts{})
		if !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "a "+kind+" as the failure module (CF-31)") {
			t.Errorf("%s: %v", kind, err)
		}
	}
}
