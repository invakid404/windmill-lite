//go:build unix

package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/wl/plugin"
)

// runEndFiles is the synthetic folder of TestRunnerRunEndAnswer. Its host
// plugin, f/end/plugin.ts, answers each run's end; the run's data chooses
// what its onRunEnd returns:
//   - none: a warning run_end/<status> and the answer {status, cause};
//   - {"mode":"null"}: the answer null;
//   - {"mode":"warnings"}: the warning alone, as an array.
var runEndFiles = map[string]string{
	"wmill.yaml": "defaultTs: bun\n",
	"f/end/plugin.ts": `export default {
  onRunEnd(t) {
    const d = (globalThis as any).wl.run.data ?? {};
    const warning = { code: "run_end", message: t.status };
    if (d.mode === "null") return { answer: null };
    if (d.mode === "warnings") return [warning];
    return { warnings: [warning], answer: { status: t.status, cause: t.cancel_cause ?? null } };
  },
};
`,
	"f/end/one.ts": `// Returns 1.
export async function main() {
  return 1;
}
`,
	"f/end/fail.ts": `// Fails.
export async function main() {
  throw new Error("the step failed");
}
`,
	"f/end/ok.flow/flow.yaml":   `{"summary":"one step that returns 1","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/end/one","input_transforms":{}}}]}}`,
	"f/end/fail.flow/flow.yaml": `{"summary":"one step that fails","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/end/fail","input_transforms":{}}}]}}`,
}

const runEndContract = `roots:
  - flow: f/end/ok
  - flow: f/end/fail
  - script: f/end/one
plugin: f/end/plugin.ts
`

// TestRunnerRunEndAnswer runs flows through windmill-lite plugin and
// wl/plugin's Client: RunResult.answer holds the JSON text of the bundle's
// host plugin's answer for a successful, a failed and a canceled run, the
// text null for an answer of null, and nothing when onRunEnd returns only
// warnings, or for a root script's run, which calls no run-end hook. A
// run's hooks on the broker answer no run: the answer of their on_run_end
// and their other members, an answer_bytes that is not a number included,
// are ignored, and their warnings stay the run's.
func TestRunnerRunEndAnswer(t *testing.T) {
	bun := absBun(t)
	out := filepath.Join(t.TempDir(), "bundle")
	if err := buildFolder(bun, runEndFiles, runEndContract, out); err != nil {
		t.Fatal(err)
	}
	e := launchPlugin(t, pluginConfigOf(t, bun, out))
	for _, c := range []struct {
		name   string
		req    *plugin.RunRequest
		status string
		answer string
		// warnings is the run's warnings field, as JSON text.
		warnings string
	}{
		{"success", &plugin.RunRequest{Path: "f/end/ok"}, "success", `{"status":"success","cause":null}`, `[{"code":"run_end","message":"success"}]`},
		{"failure", &plugin.RunRequest{Path: "f/end/fail"}, "failure", `{"status":"failure","cause":null}`, `[{"code":"run_end","message":"failure"}]`},
		{"canceled before any step", &plugin.RunRequest{Path: "f/end/ok", DeadlineMs: ms(0)}, "canceled", `{"status":"canceled","cause":"deadline"}`, `[{"code":"run_end","message":"canceled"}]`},
		{"null", &plugin.RunRequest{Path: "f/end/ok", RunData: []byte(`{"mode":"null"}`)}, "success", "null", ""},
		{"warnings alone", &plugin.RunRequest{Path: "f/end/ok", RunData: []byte(`{"mode":"warnings"}`)}, "success", "", `[{"code":"run_end","message":"success"}]`},
		{"a script run", &plugin.RunRequest{Kind: "script", Path: "f/end/one"}, "success", "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			res := e.mustRun(t, c.req)
			if res.GetStatus() != c.status || string(res.GetAnswer()) != c.answer || string(res.GetWarnings()) != c.warnings {
				t.Fatalf("status %s, answer %q, warnings %s; want %s, %q, %s", res.GetStatus(), res.GetAnswer(), res.GetWarnings(), c.status, c.answer, c.warnings)
			}
		})
	}
	t.Run("hooks on the broker", func(t *testing.T) {
		hooks := func(_ context.Context, c *plugin.HookCall) (*plugin.HookReply, error) {
			if c.GetHook() != "on_run_end" {
				return &plugin.HookReply{Payload: []byte(`{}`)}, nil
			}
			// Members beside the warnings are the host's own: the engine
			// ignores them, an answer_bytes that is not a number included.
			return &plugin.HookReply{Payload: []byte(`{"warnings":[{"code":"host","message":"the host's hook"}],"answer":"the host's answer","answer_bytes":"unknown"}`)}, nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		for _, c := range []struct {
			data, answer string
		}{
			{"", `{"status":"success","cause":null}`},
			{`{"mode":"warnings"}`, ""},
		} {
			res, err := e.runner.RunWithHooks(ctx, &plugin.RunRequest{Path: "f/end/ok", RunData: []byte(c.data)}, []byte(`{"on_run_end": true}`), hooks)
			if err != nil {
				t.Fatalf("%v\nthe engine's standard error:\n%s", err, e.stderr)
			}
			const warnings = `[{"code":"host","message":"the host's hook"},{"code":"run_end","message":"success"}]`
			if string(res.GetAnswer()) != c.answer || string(res.GetWarnings()) != warnings {
				t.Fatalf("run data %q: answer %q, warnings %s; want the host plugin's answer %q, and the broker's warning first: %s", c.data, res.GetAnswer(), res.GetWarnings(), c.answer, warnings)
			}
			if strings.Contains(string(res.GetAnswer()), "host's answer") {
				t.Fatal("the broker's answer reached the run")
			}
		}
	})
}

// runEndHostPlugin is a host plugin whose onRunEnd answers by its run's
// data: the answer null for {mode: "null"}, a warning alone for {mode:
// "warnings"}, and otherwise a warning run_end/<status> and the answer
// {status, cause, tenant}.
const runEndHostPlugin = `export default {
  onRunEnd(t) {
    const d = (globalThis as any).wl.run.data ?? {};
    const warning = { code: "run_end", message: t.status };
    if (d.mode === "null") return { answer: null };
    if (d.mode === "warnings") return [warning];
    return { warnings: [warning], answer: { status: t.status, cause: t.cancel_cause ?? null, tenant: d.tenant ?? null } };
  },
};
`

// TestHostRunEndAnswer runs flows in host mode through the Bun host SDK
// (testdata/pluginhost/host.ts, run-end-answer), with the bundle's own
// plugin: the engine's run_done carries the answer of the plugin's
// onRunEnd, which the SDK's run result gives as answer, for a successful
// run and one whose deadline had passed. An answer of null is null, and a
// run whose onRunEnd returns warnings alone has no answer field.
func TestHostRunEndAnswer(t *testing.T) {
	bun := bunForTest(t)
	var got []struct {
		Status   string          `json:"status"`
		Has      bool            `json:"has"`
		Answer   json.RawMessage `json:"answer"`
		Warnings json.RawMessage `json:"warnings"`
	}
	runPluginHost(t, bun, &got, "WL_SCENARIO=run-end-answer", "WL_BUNDLE="+pluginHostBundle(t, bun, runEndHostPlugin))
	want := []struct {
		status   string
		has      bool
		answer   string
		warnings string
	}{
		{"success", true, `{"status":"success","cause":null,"tenant":"a"}`, `[{"code":"run_end","message":"success"}]`},
		{"canceled", true, `{"status":"canceled","cause":"deadline","tenant":"b"}`, `[{"code":"run_end","message":"canceled"}]`},
		{"success", true, `null`, `[]`},
		{"success", false, `null`, `[{"code":"run_end","message":"success"}]`},
	}
	if len(got) != len(want) {
		t.Fatalf("the host reported %d runs, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		g := got[i]
		if g.Status != w.status || g.Has != w.has || string(g.Answer) != w.answer || string(g.Warnings) != w.warnings {
			t.Errorf("run %d: status %s, has an answer %v, answer %s, warnings %s; want %s, %v, %s, %s", i, g.Status, g.Has, g.Answer, g.Warnings, w.status, w.has, w.answer, w.warnings)
		}
	}
}
