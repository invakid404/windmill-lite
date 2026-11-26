package wl

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// runEndPlugin is a host plugin whose onRunEnd answers from state that
// only its process holds: the modules whose start it saw for the run. The
// run's data chooses what onRunEnd returns:
//
//   - none: a RunEnd with a warning run_end/<status> and the answer
//     {status, cause, latched, modules};
//   - {"mode":"null"}: the answer null;
//   - {"mode":"warnings"}: the warning alone, as an array;
//   - {"mode":"nothing"}: nothing;
//   - {"mode":"throw"}: a throw;
//   - {"mode":"hang"}: a promise that never settles;
//   - {"mode":"size","n":<n>}: the warning and an answer of a string of n
//     characters x, whose JSON text is n + 2 bytes.
const runEndPlugin = `const seen = new Map<string, string[]>();
export default {
  onModuleStart({ run, site }) {
    seen.set(run, [...(seen.get(run) ?? []), site.module]);
  },
  onRunEnd(t) {
    const modules = seen.get(t.run) ?? [];
    seen.delete(t.run);
    const d = (globalThis as any).wl.run.data ?? {};
    const warning = { code: "run_end", message: t.status };
    switch (d.mode) {
      case "null": return { answer: null };
      case "warnings": return [warning];
      case "nothing": return;
      case "throw": throw new Error("the plugin's run end failed");
      case "hang": return new Promise(() => {});
      case "size": return { warnings: [warning], answer: "x".repeat(d.n) };
    }
    return { warnings: [warning], answer: { status: t.status, cause: t.cancel_cause ?? null, latched: t.latched !== undefined, modules } };
  },
};
`

// runEndStep is a Bun rawscript module whose step runs body.
func runEndStep(id, body string) string {
	return `{"id":"` + id + `","value":{"type":"rawscript","language":"bun","content":"export async function main() { ` + body + ` }","input_transforms":{}}}`
}

// openRunEnd builds a bundle of flows that end each way a run can end, and
// opens it with runEndPlugin and the plugin timeout given:
//
//   - f/a/ok returns 1 from its one step a;
//   - f/a/fail fails at its step a;
//   - f/a/latch latches the run from its step a (wl.latch);
//   - f/a/two runs steps a and b;
//   - f/a/die kills its harness process from its step a.
func openRunEnd(t *testing.T, timeout time.Duration) *Runtime {
	t.Helper()
	bun := bunOrSkip(t)
	flow := func(modules ...string) string {
		return `{"summary":"t","value":{"modules":[` + strings.Join(modules, ",") + `]}}`
	}
	out, runtimeDir := buildBunBundle(t, bun, map[string]string{
		"wmill.yaml":               "defaultTs: bun\n",
		"f/a/ok.flow/flow.yaml":    flow(runEndStep("a", "return 1")),
		"f/a/fail.flow/flow.yaml":  flow(runEndStep("a", `throw new Error('the step failed')`)),
		"f/a/latch.flow/flow.yaml": flow(runEndStep("a", `(globalThis as any).wl.latch(new TypeError('the step latched')); return 1`)),
		"f/a/two.flow/flow.yaml":   flow(runEndStep("a", "return 1"), runEndStep("b", "return 2")),
		"f/a/die.flow/flow.yaml":   flow(runEndStep("a", `process.kill(process.pid, 'SIGKILL'); await new Promise(() => {})`)),
	})
	plugin := filepath.Join(t.TempDir(), "plugin.ts")
	if err := os.WriteFile(plugin, []byte(runEndPlugin), 0o644); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, Plugin: plugin, PluginTimeout: timeout})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rt.Close() })
	return rt
}

// runEnd runs path pinned, so that every hook call of the run reaches the
// process that holds the plugin's state for it, with data as its data.
func runEnd(t *testing.T, ctx context.Context, rt *Runtime, path, data string, hooks *Hooks) *Result {
	t.Helper()
	res, err := rt.Run(ctx, path, nil, RunOptions{Pin: true, Data: json.RawMessage(data), Hooks: hooks})
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return res
}

// TestPluginRunEndAnswer: the host plugin's onRunEnd gives every way a run
// ends an answer, which Result.Answer carries as its JSON text: a
// successful, a failed and a latched run, one canceled after its first
// step, and one whose deadline had passed, which ends before any step. The
// answer comes from the plugin's own state, and the hook's warnings stay
// the run's. An answer of null is the text null.
func TestPluginRunEndAnswer(t *testing.T) {
	rt := openRunEnd(t, 0)
	// A Go hook cancels the run once step a has ended, so step b never
	// starts.
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	cancelAfterA := &Hooks{OnModuleEnd: func(_ context.Context, _ RunHandle, s Site, _ StepRecord) error {
		if s.Module == "a" {
			cancel(&Cancellation{Reason: "the host stopped it", Canceler: "the test"})
		}
		return nil
	}}
	passed, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	for _, c := range []struct {
		name, path string
		ctx        context.Context
		hooks      *Hooks
		status     string
		answer     string
	}{
		{"success", "f/a/ok", context.Background(), nil, StatusSuccess, `{"status":"success","cause":null,"latched":false,"modules":["a"]}`},
		{"failure", "f/a/fail", context.Background(), nil, StatusFailure, `{"status":"failure","cause":null,"latched":false,"modules":["a"]}`},
		{"latched", "f/a/latch", context.Background(), nil, StatusLatched, `{"status":"latched","cause":null,"latched":true,"modules":["a"]}`},
		{"canceled", "f/a/two", ctx, cancelAfterA, StatusCanceled, `{"status":"canceled","cause":"canceled","latched":false,"modules":["a"]}`},
		{"ended before any step", "f/a/two", passed, nil, StatusCanceled, `{"status":"canceled","cause":"deadline","latched":false,"modules":[]}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			res := runEnd(t, c.ctx, rt, c.path, "", c.hooks)
			if res.Status != c.status {
				t.Fatalf("status %s, want %s; warnings %+v", res.Status, c.status, res.Warnings)
			}
			if string(res.Answer) != c.answer {
				t.Fatalf("answer %s, want %s; warnings %+v", res.Answer, c.answer, res.Warnings)
			}
			if !slices.Contains(warningTexts(res.Warnings), "run_end: "+c.status) {
				t.Fatalf("warnings %+v: the hook's warning is missing", res.Warnings)
			}
		})
	}
	t.Run("null", func(t *testing.T) {
		res := runEnd(t, context.Background(), rt, "f/a/ok", `{"mode":"null"}`, nil)
		if res.Status != StatusSuccess || string(res.Answer) != "null" || len(res.Warnings) != 0 {
			t.Fatalf("status %s, answer %q, warnings %+v; want success, the text null and no warning", res.Status, res.Answer, res.Warnings)
		}
	})
}

// TestPluginRunEndAnswerFailed: a run whose onRunEnd throws, gets no reply
// within the plugin timeout, or cannot reach its pinned process, which its
// step killed, has no answer and one plugin_error warning that says why;
// its status stays its own.
func TestPluginRunEndAnswerFailed(t *testing.T) {
	rt := openRunEnd(t, time.Second)
	for _, c := range []struct {
		name, path, data, status, why string
	}{
		{"throws", "f/a/ok", `{"mode":"throw"}`, StatusSuccess, "the plugin's run end failed"},
		{"times out", "f/a/ok", `{"mode":"hang"}`, StatusSuccess, "host_call_timeout"},
		{"process lost", "f/a/die", "", StatusFailure, ErrPinnedProcessLost.Error()},
	} {
		t.Run(c.name, func(t *testing.T) {
			res := runEnd(t, context.Background(), rt, c.path, c.data, nil)
			if res.Status != c.status {
				t.Fatalf("status %s, want %s", res.Status, c.status)
			}
			if res.Answer != nil {
				t.Fatalf("answer %s, want none", res.Answer)
			}
			var failed []Warning
			for _, w := range res.Warnings {
				if w.Code == "plugin_error" {
					failed = append(failed, w)
				}
			}
			if len(failed) != 1 || !strings.HasPrefix(failed[0].Message, "the host plugin's onRunEnd: ") || !strings.Contains(failed[0].Message, c.why) {
				t.Fatalf("warnings %+v; want one plugin_error of onRunEnd that names %q", res.Warnings, c.why)
			}
		})
	}
}

// TestPluginRunEndAnswerUnchanged: an onRunEnd that returns warnings, or
// nothing, gives the run its warnings, or none, and no answer; its status
// and result stay the run's own. A Go host's own OnRunEnd beside it adds
// its warnings first, and no answer either.
func TestPluginRunEndAnswerUnchanged(t *testing.T) {
	rt := openRunEnd(t, 0)
	goEnd := &Hooks{OnRunEnd: func(context.Context, *Trace) []Warning {
		return []Warning{{Code: "go_end", Message: "the Go hook"}}
	}}
	for _, c := range []struct {
		name, data string
		hooks      *Hooks
		warnings   []string
	}{
		{"warnings", `{"mode":"warnings"}`, nil, []string{"run_end: success"}},
		{"nothing", `{"mode":"nothing"}`, nil, nil},
		{"a Go hook's warnings first", `{"mode":"warnings"}`, goEnd, []string{"go_end: the Go hook", "run_end: success"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			res := runEnd(t, context.Background(), rt, "f/a/ok", c.data, c.hooks)
			if res.Status != StatusSuccess || string(res.Result) != "1" || res.Latched != nil || res.CancelCause != "" {
				t.Fatalf("result %+v; want success with result 1", res)
			}
			if res.Answer != nil {
				t.Fatalf("answer %q, want none", res.Answer)
			}
			if !slices.Equal(warningTexts(res.Warnings), c.warnings) {
				t.Fatalf("warnings %+v, want %+v", res.Warnings, c.warnings)
			}
		})
	}
}

// TestPluginRunEndAnswerBound: an answer whose JSON text is
// MaxRunEndAnswerBytes long reaches the result whole. One byte more, and
// the run has no answer: it keeps the hook's warning, and gets a
// plugin_error warning after it that names both sizes.
func TestPluginRunEndAnswerBound(t *testing.T) {
	rt := openRunEnd(t, 0)
	at := runEnd(t, context.Background(), rt, "f/a/ok", fmt.Sprintf(`{"mode":"size","n":%d}`, MaxRunEndAnswerBytes-2), nil)
	if len(at.Answer) != MaxRunEndAnswerBytes || string(at.Answer) != `"`+strings.Repeat("x", MaxRunEndAnswerBytes-2)+`"` {
		t.Fatalf("an answer at the bound: %d bytes, want %d", len(at.Answer), MaxRunEndAnswerBytes)
	}
	if !slices.Equal(warningTexts(at.Warnings), []string{"run_end: success"}) {
		t.Fatalf("an answer at the bound: warnings %+v", at.Warnings)
	}
	over := runEnd(t, context.Background(), rt, "f/a/ok", fmt.Sprintf(`{"mode":"size","n":%d}`, MaxRunEndAnswerBytes-1), nil)
	if over.Status != StatusSuccess || over.Answer != nil {
		t.Fatalf("an answer over the bound: status %s, %d bytes of answer; want success and none", over.Status, len(over.Answer))
	}
	want := []string{
		"run_end: success",
		"plugin_error: the host plugin's onRunEnd: an answer of 1048577 bytes is over the bound of 1048576 bytes, so the run has no answer",
	}
	if !slices.Equal(warningTexts(over.Warnings), want) {
		t.Fatalf("an answer over the bound: warnings %+v, want %+v", over.Warnings, want)
	}
}

// warningTexts is each warning as "<code>: <message>".
func warningTexts(ws []Warning) []string {
	var out []string
	for _, w := range ws {
		out = append(out, w.Code+": "+w.Message)
	}
	return out
}
