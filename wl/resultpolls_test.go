package wl

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/bundle"
)

// buildResultPollsBundle builds testdata/resultpolls/windmill: the flows
// f/rp/large, f/rp/canceled and f/rp/childflow, whose last step, the path
// script f/rp/drive, polls the jobs of the steps before it, and
// f/rp/latched, whose step polls the job of a child flow that latched in a
// run it dispatched.
func buildResultPollsBundle(t *testing.T, bun string) (out, runtimeDir string) {
	t.Helper()
	repo, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(t.TempDir(), "windmill")
	if err := os.CopyFS(folder, os.DirFS(filepath.Join("testdata", "resultpolls", "windmill"))); err != nil {
		t.Fatal(err)
	}
	runtimeDir = filepath.Join(repo, "harnesses", "bun", "runtime")
	out = filepath.Join(t.TempDir(), "bundle")
	if _, err := bundle.Build(context.Background(), bundle.Config{
		Folder:         folder,
		EngineVersion:  Version(),
		Bun:            bun,
		Helper:         filepath.Join(repo, "harnesses", "bun", "build", "main.ts"),
		HarnessRuntime: runtimeDir,
		Out:            out,
		Work:           filepath.Join(t.TempDir(), "work"),
	}); err != nil {
		t.Fatal(err)
	}
	return out, runtimeDir
}

// polled is one poll's answer as f/rp/drive returns it.
type polled struct {
	Status int    `json:"status"`
	Length int    `json:"length"`
	Body   string `json:"body"`
}

// TestResultPollsRealStates checks Options.ResultPollsForAllJobs on the
// engine's own job states, on the pinned Bun 1.4.3 in warm mode: a step of
// a live run polls, through get_result_maybe and get_result, the jobs of
// the steps that ran before it.
//
//   - f/rp/large: a step's 120,000-character result, which the job read's
//     record replaces with its placeholder "WINDMILL_TOO_BIG", answers
//     whole on both routes, while the job read keeps the placeholder; and
//     a step that returns that very text answers it as its result.
//   - f/rp/canceled: a step that its 1 s timeout canceled, whose record the
//     job read does not serve, answers 501 on both routes, so the 501
//     policy fails the polling step; with the option the refusal names the
//     reason, "the job was canceled".
//   - f/rp/childflow: a child flow that its flow step's timeout canceled
//     answers 501 alike, with the option naming "the flow job ended
//     canceled".
//   - f/rp/latched: the polling step dispatches f/rp/latching linked to its
//     own job and root, so that run's jobs belong to this run's tree, and
//     waits for it to complete. A step of its child flow latched it
//     (wl.latch), so the child flow's job, which no step dispatched, ended
//     latched while this run, the tree's root, still runs: its polls
//     answer 501, with the option naming "the flow job ended latched".
//
// Without the option every one of these polls answers the default 501,
// which names no reason.
func TestResultPollsRealStates(t *testing.T) {
	const (
		maybeRoute  = "GET /w/{ws}/jobs_u/completed/get_result_maybe/{id}"
		resultRoute = "GET /w/{ws}/jobs_u/completed/get_result/{id}"
		refused     = "Not implemented in windmill-lite: " + maybeRoute
		envelope    = `{"started":true,"success":true,"completed":true,"result":`
		size        = 120000 // f/rp/large's big, a JSON string of size characters
	)
	bun := bunOrSkip(t)
	out, runtimeDir := buildResultPollsBundle(t, bun)
	for _, on := range []bool{true, false} {
		t.Run(map[bool]string{true: "with the option", false: "without the option"}[on], func(t *testing.T) {
			rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, ModuleState: ModuleStateWarm, ResultPollsForAllJobs: on})
			if err != nil {
				t.Fatal(err)
			}
			defer rt.Close()
			run := func(flow string) *Result {
				res, err := rt.Run(context.Background(), flow, []byte(`{}`), RunOptions{})
				if err != nil {
					t.Fatal(err)
				}
				return res
			}
			// refusedWith checks a run whose polling step the 501 policy
			// failed: both routes counted, the first refusal quoted.
			refusedWith := func(flow, message string) {
				res := run(flow)
				var got struct {
					Error struct {
						Name    string   `json:"name"`
						Calls   []string `json:"calls"`
						Message string   `json:"message"`
					} `json:"error"`
				}
				if err := json.Unmarshal(res.Result, &got); err != nil || res.Status != StatusFailure || got.Error.Name != "UnsupportedPlatformCall" ||
					strings.Join(got.Error.Calls, "|") != maybeRoute+"|"+resultRoute || got.Error.Message != message {
					t.Errorf("%s: %s %s, want UnsupportedPlatformCall %q", flow, res.Status, res.Result, message)
				}
			}
			if !on {
				for _, flow := range []string{"f/rp/large", "f/rp/canceled", "f/rp/childflow", "f/rp/latched"} {
					res := run(flow)
					if res.Status != StatusFailure || !strings.Contains(string(res.Result), `"message":"`+refused+`"`) {
						t.Errorf("%s: %s %s, want the default 501 %q", flow, res.Status, res.Result, refused)
					}
				}
				return
			}
			res := run("f/rp/large")
			var got struct {
				Big, S struct{ Maybe, Result polled }
				Record string
			}
			if err := json.Unmarshal(res.Result, &got); err != nil || res.Status != StatusSuccess {
				t.Fatalf("f/rp/large: %s %s (%v)", res.Status, res.Result, err)
			}
			if m := got.Big.Maybe; m.Status != 200 || m.Length != len(envelope)+size+3 || !strings.HasPrefix(m.Body, envelope+`"`) {
				t.Errorf("big, get_result_maybe: %+v, want 200 with the whole result, %d characters", m, len(envelope)+size+3)
			}
			if r := got.Big.Result; r.Status != 200 || r.Length != size+2 || !strings.HasPrefix(r.Body, `"`) || strings.Contains(r.Body, "WINDMILL_TOO_BIG") {
				t.Errorf("big, get_result: %+v, want 200 with the whole result, %d characters", r, size+2)
			}
			if got.Record != "WINDMILL_TOO_BIG" {
				t.Errorf("big, the job read's result: %.40q, want the placeholder", got.Record)
			}
			if m, r := got.S.Maybe, got.S.Result; m.Status != 200 || m.Body != envelope+`"WINDMILL_TOO_BIG"}` || r.Status != 200 || r.Body != `"WINDMILL_TOO_BIG"` {
				t.Errorf("s, the placeholder's text as a result: %+v %+v", m, r)
			}
			refusedWith("f/rp/canceled", refused+": the job was canceled")
			refusedWith("f/rp/childflow", refused+": the flow job ended canceled")
			refusedWith("f/rp/latched", refused+": the flow job ended latched")
		})
	}
}
