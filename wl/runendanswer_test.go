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

	"github.com/invakid404/windmill-lite/internal/harness"
	"github.com/invakid404/windmill-lite/internal/jsonval"
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
//     characters x, whose JSON text is n + 2 bytes;
//   - {"mode":"counted"}: the warning and an answer behind a getter that
//     gives it on its first read only, whose toJSON counts its calls: an
//     answer read and serialised once is {"serialised":1};
//   - {"mode":"map"}, {"mode":"empty"}, {"mode":"other"}: values outside
//     the RunEnd form: a Map, {}, and the warning beside another key;
//   - {"mode":"fill","w":<w>,"n":<n>}: a warning run_end/<w characters w>
//     and an answer of a string of n characters x.
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
      case "counted": {
        let reads = 0, serialised = 0;
        const value = { toJSON: () => ({ serialised: ++serialised }) };
        return { warnings: [warning], get answer() { return ++reads === 1 ? value : undefined; } };
      }
      case "map": return new Map();
      case "empty": return {};
      case "other": return { warnings: [warning], extra: 1 };
      case "fill": return { warnings: [{ code: "run_end", message: "w".repeat(d.w) }], answer: "x".repeat(d.n) };
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
			if got := marshalledAnswer(t, res); got != c.answer {
				t.Fatalf("json.Marshal of the result: Answer %s, want %s", got, c.answer)
			}
		})
	}
	t.Run("null", func(t *testing.T) {
		res := runEnd(t, context.Background(), rt, "f/a/ok", `{"mode":"null"}`, nil)
		if res.Status != StatusSuccess || string(res.Answer) != "null" || len(res.Warnings) != 0 {
			t.Fatalf("status %s, answer %q, warnings %+v; want success, the text null and no warning", res.Status, res.Answer, res.Warnings)
		}
		if got := marshalledAnswer(t, res); got != "null" {
			t.Fatalf("json.Marshal of the result: Answer %s, want null", got)
		}
	})
	// The answer's getter is read once, and its toJSON runs once: the reply
	// carries the text that the harness checked.
	t.Run("read and serialised once", func(t *testing.T) {
		res := runEnd(t, context.Background(), rt, "f/a/ok", `{"mode":"counted"}`, nil)
		if res.Status != StatusSuccess || string(res.Answer) != `{"serialised":1}` || !slices.Equal(warningTexts(res.Warnings), []string{"run_end: success"}) {
			t.Fatalf("status %s, answer %s, warnings %+v; want success, {\"serialised\":1} and the hook's warning", res.Status, res.Answer, res.Warnings)
		}
	})
}

// marshalledAnswer is the Answer member of res as encoding/json writes the
// result, or "absent" when it has none. It marshals a copy without the
// trace, whose values encoding/json cannot all encode, as a script run's
// result has none.
func marshalledAnswer(t *testing.T, res *Result) string {
	t.Helper()
	c := *res
	c.Trace = nil
	b, err := json.Marshal(&c)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	a, ok := m["Answer"]
	if !ok {
		return "absent"
	}
	return string(a)
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
// and result stay the run's own, and json.Marshal of the result has no
// Answer member. A Go host's own OnRunEnd beside it adds its warnings
// first, and no answer either. A value outside the RunEnd form, a Map, {}
// or an object with another key, still answers the plugin_error of
// warnings that are not {code, message} objects.
func TestPluginRunEndAnswerUnchanged(t *testing.T) {
	rt := openRunEnd(t, 0)
	goEnd := &Hooks{OnRunEnd: func(context.Context, *Trace) []Warning {
		return []Warning{{Code: "go_end", Message: "the Go hook"}}
	}}
	const refused = "plugin_error: the host plugin's onRunEnd: plugin_error: host plugin hook on_run_end: onRunEnd answered warnings that are not {code, message} objects"
	for _, c := range []struct {
		name, data string
		hooks      *Hooks
		warnings   []string
	}{
		{"warnings", `{"mode":"warnings"}`, nil, []string{"run_end: success"}},
		{"nothing", `{"mode":"nothing"}`, nil, nil},
		{"a Go hook's warnings first", `{"mode":"warnings"}`, goEnd, []string{"go_end: the Go hook", "run_end: success"}},
		{"a Map", `{"mode":"map"}`, nil, []string{refused}},
		{"an empty object", `{"mode":"empty"}`, nil, []string{refused}},
		{"an object with another key", `{"mode":"other"}`, nil, []string{refused}},
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
			if got := marshalledAnswer(t, res); got != "absent" {
				t.Fatalf("json.Marshal of the result: Answer %s, want no member", got)
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

// TestPluginRunEndAnswerTooLargeToSend: a harness process whose message
// limit, lowered to 256 KiB, cannot carry the reply with its answer sends
// the hook's warnings and the answer's size instead. The run keeps the
// warning and has no answer, with a plugin_error that names the size: over
// the bound, or within it but too large for the limit. An answer that fits
// the limit arrives whole. A warning that fills the limit, so that even the
// reply with it alone under warnings is one byte over, goes under dropped,
// one byte shorter: the run keeps it and gets a plugin_error that its
// answer did not fit. The process is real, spawned beside the runtime's
// pool for the runtime's bundle and plugin.
func TestPluginRunEndAnswerTooLargeToSend(t *testing.T) {
	rt := openRunEnd(t, 0)
	bun := bunOrSkip(t)
	plugin := filepath.Join(t.TempDir(), "plugin.ts")
	if err := os.WriteFile(plugin, []byte(runEndPlugin), 0o644); err != nil {
		t.Fatal(err)
	}
	runtimeDir, err := filepath.Abs(filepath.Join("..", "harnesses", "bun", "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	path, args := harness.BunCommand(bun, filepath.Join(runtimeDir, "main.ts"), rt.dir)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	proc, err := harness.Spawn(ctx, harness.ProcessConfig{
		Path: path, Args: append(args, "--plugin", plugin), WorkRoot: t.TempDir(), Kind: "bun", Runtime: harness.PinnedBun,
		Bundle: rt.digest, EngineVersion: rt.version, Facade: rt.PlatformOrigin(), Platform: rt.fac.Platform(), MaxMessage: 256 << 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		proc.Kill()
		<-proc.Done()
	}()
	p, err := newPlugin(proc.Hello().Plugin, rt.prog, proc.HostCall, 0, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	p.answers = true
	// The fourth call's reply, hc4, under dropped: a warning of w characters
	// makes it exactly the limit, and the same reply under warnings one byte
	// over it.
	const limit = 256 << 10
	prefix, suffix := `{"t":"host_reply","id":"hc4","payload":{"dropped":[{"code":"run_end","message":"`, `"}]}}`
	w := limit - len(prefix) - len(suffix)
	if under := len(strings.Replace(prefix, `"dropped"`, `"warnings"`, 1)) + w + len(suffix); under != limit+1 {
		t.Fatalf("the reply under warnings would be %d bytes, want %d", under, limit+1)
	}
	for i, c := range []struct {
		data     string
		answer   string
		warnings []string
	}{
		{`{"mode":"size","n":200000}`, `"` + strings.Repeat("x", 200000) + `"`, []string{"run_end: success"}},
		{`{"mode":"size","n":300000}`, "", []string{"run_end: success", "plugin_error: the host plugin's onRunEnd: an answer of 300002 bytes did not fit in a message of the harness protocol, so the run has no answer"}},
		{fmt.Sprintf(`{"mode":"size","n":%d}`, MaxRunEndAnswerBytes), "", []string{"run_end: success", "plugin_error: the host plugin's onRunEnd: an answer of 1048578 bytes is over the bound of 1048576 bytes, so the run has no answer"}},
		{fmt.Sprintf(`{"mode":"fill","w":%d,"n":%d}`, w, MaxRunEndAnswerBytes), "", []string{"run_end: " + strings.Repeat("w", w), "plugin_error: the host plugin's onRunEnd: an answer whose size did not fit beside its warnings in a message of the harness protocol, so the run has no answer"}},
	} {
		run := fmt.Sprintf("too-large-%d", i)
		data := harness.NewRunData(run, jsonval.FromProducer([]byte(c.data)))
		actx, answer := withAnswer(harness.WithRunData(context.Background(), data))
		ws := p.hooks.OnRunEnd(actx, &Trace{RunID: run, Status: "success"})
		if string(answer.get()) != c.answer || !slices.Equal(warningTexts(ws), c.warnings) {
			got := warningTexts(ws)
			for j := range got {
				if len(got[j]) > 200 {
					got[j] = fmt.Sprintf("%s… (%d bytes)", got[j][:60], len(got[j]))
				}
			}
			t.Errorf("run data %.60s: answer of %d bytes, warnings %q; want %d bytes and %d warnings", c.data, len(answer.get()), got, len(c.answer), len(c.warnings))
		}
	}
}
