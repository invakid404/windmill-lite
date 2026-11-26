package wl

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/harness"
	"github.com/invakid404/windmill-lite/internal/platform"
)

// The harness environment tests run real steps and a real host plugin in
// Bun harness processes that the runtime spawns, and read what those
// processes see (Options.HarnessEnv).

// harnessEnvFiles is the synthetic folder of the harness environment tests.
var harnessEnvFiles = map[string]string{
	"wmill.yaml": "defaultTs: bun\n",
	"f/t/env.ts": `import { createHash } from "node:crypto";
// Returns, for each name, the value of that variable of process.env, or
// null; for each name in hashed, the SHA-256 of its value instead.
export async function main(names: string[], hashed: string[]) {
  const out: Record<string, string | null> = {};
  for (const n of names) {
    const v = process.env[n];
    out[n] = v === undefined ? null : hashed.includes(n) ? createHash("sha256").update(v).digest("hex") : v;
  }
  return out;
}
`,
	"f/t/all.ts": `// Returns every variable of process.env.
export async function main() {
  return { ...process.env };
}
`,
	"f/t/own.ts": `// Returns its WL_TEST_SHARED variable, which its own envs set.
export async function main() {
  return process.env.WL_TEST_SHARED ?? null;
}
`,
	"f/t/own.script.yaml": "summary: ''\ndescription: ''\nkind: script\nenvs:\n  - WL_TEST_SHARED=from-the-script\nschema:\n  type: object\n  properties: {}\n  required: []\n",
	"f/t/native.ts": `//native
// Returns its WL_TEST_PLAIN variable.
export async function main() {
  return process.env.WL_TEST_PLAIN ?? null;
}
`,
	"f/t/shared.ts": `// Returns its WL_TEST_SHARED variable.
export async function main() {
  return process.env.WL_TEST_SHARED ?? null;
}
`,
	"f/t/env.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/t/env",` +
		`"input_transforms":{"names":{"type":"javascript","expr":"flow_input.names"},"hashed":{"type":"javascript","expr":"flow_input.hashed"}}}}]}}`,
	"f/t/shared.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/t/shared","input_transforms":{}}}]}}`,
	"f/t/echo.ts": `// Returns its argument.
export async function main(x: unknown) {
  return x;
}
`,
	"f/t/thrower.ts": `// Fails with its WL_TEST_SECRET variable in its error.
export async function main() {
  throw new Error("failed with " + process.env.WL_TEST_SECRET);
}
`,
	"f/t/latcher.ts": `// Latches its run with its WL_TEST_SECRET variable in the error.
export async function main() {
  (globalThis as any).wl.latch(new Error("latched with " + process.env.WL_TEST_SECRET));
  return 1;
}
`,
	"f/t/escape.ts": `// Logs its WL_TEST_SECRET variable, then exits with status 3.
export async function main() {
  console.log("leaving with " + process.env.WL_TEST_SECRET);
  process.exit(3);
}
`,
	// f/t/reveal: step a returns WL_TEST_SECRET, and step b echoes it.
	"f/t/reveal.flow/flow.yaml": `{"summary":"t","value":{"modules":[` +
		`{"id":"a","value":{"type":"script","path":"f/t/env","input_transforms":{"names":{"type":"static","value":["WL_TEST_SECRET"]},"hashed":{"type":"static","value":[]}}}},` +
		`{"id":"b","value":{"type":"script","path":"f/t/echo","input_transforms":{"x":{"type":"javascript","expr":"results.a"}}}}]}}`,
	"f/t/thrower.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/t/thrower","input_transforms":{}}}]}}`,
	"f/t/number.ts": `// Returns its WL_TEST_NUMBER variable as a number, beside a plain value.
export async function main() {
  return { n: Number(process.env.WL_TEST_NUMBER), plain: "kept" };
}
`,
	"f/t/number.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/t/number","input_transforms":{}}}]}}`,
	"f/t/late.ts": `// Fixes its outcome with process.exit(0) and swallows the exit, then logs
// and returns its WL_TEST_SECRET variable, which both come late.
export async function main() {
  try {
    process.exit(0);
  } catch {}
  console.log("late with " + process.env.WL_TEST_SECRET);
  await Bun.sleep(50);
  return "late result with " + process.env.WL_TEST_SECRET;
}
`,
	"f/t/late.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/t/late","input_transforms":{}}}]}}`,
	"f/t/call.ts": `// Calls the platform facade with WL_TEST_URL_SECRET, escaped, in a path
// and a query, on a route the facade serves and on one it does not, and
// as URLSearchParams writes it, with + for a space, and as it is, where it
// spans two pairs before a plain parameter, in the first call's query; with
// WL_TEST_METHOD_SECRET as a method; with WL_TEST_SLASH_SECRET as it is,
// across a path's segments after an escaped slash; and with
// WL_TEST_SPACE_SECRET as it is on a route the facade does not serve, where
// the URL escapes its space and keeps its slash. Returns the statuses.
export async function main() {
  const e = process.env;
  const api = ` + "`${e.BASE_URL}/api/w/${e.WM_WORKSPACE}`" + `;
  const headers = { Authorization: ` + "`Bearer ${e.WM_TOKEN}`" + ` };
  const s = encodeURIComponent(e.WL_TEST_URL_SECRET ?? "");
  const form = new URLSearchParams({ form: e.WL_TEST_URL_SECRET ?? "" }).toString();
  const a = await fetch(api + "/variables/get_value/" + s + "?path=" + s + "&" + form + "&raw=" + e.WL_TEST_URL_SECRET + "&tail=kept", { headers });
  const b = await fetch(api + "/variables/get_value/u/t/x", { method: e.WL_TEST_METHOD_SECRET, headers });
  const c = await fetch(api + "/no/such/route/" + s + "?q=" + s, { headers });
  const d = await fetch(api + "/variables/get_value/keep%2Fme/" + e.WL_TEST_SLASH_SECRET, { headers });
  const f = await fetch(api + "/no/such/route/" + e.WL_TEST_SPACE_SECRET, { headers });
  return [a.status, b.status, c.status, d.status, f.status];
}
`,
	"f/t/parsed.ts": `// Returns its WL_TEST_JSON_SECRET variable, a JSON document, parsed.
export async function main() {
  return JSON.parse(process.env.WL_TEST_JSON_SECRET ?? "null");
}
`,
	"f/t/callone.ts": `// Calls a route that the platform facade does not serve, with
// WL_TEST_URL_SECRET escaped in its path, and nothing else.
export async function main() {
  const e = process.env;
  const r = await fetch(` + "`${e.BASE_URL}/api/w/${e.WM_WORKSPACE}/no/such/route/`" + ` + encodeURIComponent(e.WL_TEST_URL_SECRET ?? ""), {
    headers: { Authorization: ` + "`Bearer ${e.WM_TOKEN}`" + ` },
  });
  return r.status;
}
`,
	"f/t/callone.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/t/callone","input_transforms":{}}}]}}`,
	"f/t/fragment.ts": `// Returns the two strings that WL_TEST_FRAGMENT joins with a quote, a
// comma and a quote, as an array, whose JSON holds the whole fragment.
export async function main() {
  return (process.env.WL_TEST_FRAGMENT ?? "").split('","');
}
`,
	"f/t/fragment.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/t/fragment","input_transforms":{}}}]}}`,
	"f/t/parsed.flow/flow.yaml":   `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/t/parsed","input_transforms":{}}}]}}`,
	// f/t/tolerated: a step's error, which the flow carries on past, is the
	// result of a run that succeeds; f/t/skipped: a run that a module's
	// stop_after_if skips keeps that module's ordinary result.
	// f/t/aggtolerated and f/t/aggfailed: a sequential branchall whose
	// branches return WL_TEST_SECRET as data and throw with it; the throw's
	// branch tolerates its failure in the first, not in the second.
	"f/t/aggtolerated.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"all","value":{"type":"branchall","parallel":false,"branches":[` +
		`{"skip_failure":false,"modules":[{"id":"x","value":{"type":"script","path":"f/t/env","input_transforms":{"names":{"type":"static","value":["WL_TEST_SECRET"]},"hashed":{"type":"static","value":[]}}}}]},` +
		`{"skip_failure":true,"modules":[{"id":"y","value":{"type":"script","path":"f/t/thrower","input_transforms":{}}}]}]}}]}}`,
	"f/t/aggfailed.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"all","value":{"type":"branchall","parallel":false,"branches":[` +
		`{"skip_failure":false,"modules":[{"id":"x","value":{"type":"script","path":"f/t/env","input_transforms":{"names":{"type":"static","value":["WL_TEST_SECRET"]},"hashed":{"type":"static","value":[]}}}}]},` +
		`{"skip_failure":false,"modules":[{"id":"y","value":{"type":"script","path":"f/t/thrower","input_transforms":{}}}]}]}}]}}`,
	"f/t/tolerated.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","continue_on_error":true,"value":{"type":"script","path":"f/t/thrower","input_transforms":{}}}]}}`,
	"f/t/skipped.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","stop_after_if":{"expr":"true","skip_if_stopped":true},` +
		`"value":{"type":"script","path":"f/t/env","input_transforms":{"names":{"type":"static","value":["WL_TEST_SECRET"]},"hashed":{"type":"static","value":[]}}}}]}}`,
	// f/t/cachedsecret: a cache_ttl step returns WL_TEST_SECRET;
	// f/t/cachedplain: one returns a plain value.
	"f/t/cachedsecret.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","cache_ttl":600,"value":{"type":"rawscript","language":"bun",` +
		`"content":"export async function main() { return process.env.WL_TEST_SECRET ?? null }","input_transforms":{}}}]}}`,
	"f/t/cacheddoc.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","cache_ttl":600,"value":{"type":"rawscript","language":"bun",` +
		`"content":"export async function main() { return JSON.parse(process.env.WL_TEST_DOC ?? 'null') }","input_transforms":{}}}]}}`,
	"f/t/cachedplain.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","cache_ttl":600,"value":{"type":"rawscript","language":"bun",` +
		`"content":"export async function main() { return 'plain' }","input_transforms":{}}}]}}`,
	// f/t/escaping: a branchall whose first branch's step escapes, which
	// the branch tolerates, and whose second returns 1: the escaped error,
	// which its log tail ends, is part of the result of a run that
	// succeeds.
	"f/t/escaping.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"all","value":{"type":"branchall","parallel":false,"branches":[` +
		`{"skip_failure":true,"modules":[{"id":"x","value":{"type":"script","path":"f/t/escape","input_transforms":{}}}]},` +
		`{"skip_failure":false,"modules":[{"id":"y","value":{"type":"rawscript","language":"bun","content":"export async function main() { return 1 }","input_transforms":{}}}]}]}}]}}`,
	"f/t/call.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/t/call","input_transforms":{}}}]}}`,
	"f/t/literal.ts": `// Calls a route that the platform facade serves with
// WL_TEST_LITERAL_SECRET, which holds an escape and a plus, as it is in a
// path and in a query, which keep both.
export async function main() {
  const e = process.env;
  const s = e.WL_TEST_LITERAL_SECRET ?? "";
  const r = await fetch(` + "`${e.BASE_URL}/api/w/${e.WM_WORKSPACE}/variables/get_value/`" + ` + s + "?path=" + s + "&tail=kept", {
    headers: { Authorization: ` + "`Bearer ${e.WM_TOKEN}`" + ` },
  });
  return r.status;
}
`,
	"f/t/literal.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/t/literal","input_transforms":{}}}]}}`,
	"f/t/prefix.ts": `// Calls the platform facade with WL_TEST_LONG_SECRET, of which it
// escapes only the slash, in a path, on a route the facade serves and on
// one it does not.
export async function main() {
  const e = process.env;
  const api = ` + "`${e.BASE_URL}/api/w/${e.WM_WORKSPACE}`" + `;
  const headers = { Authorization: ` + "`Bearer ${e.WM_TOKEN}`" + ` };
  const s = (e.WL_TEST_LONG_SECRET ?? "").replace("/", "%2F");
  const a = await fetch(api + "/variables/get_value/" + s, { headers });
  const b = await fetch(api + "/no/such/route/" + s, { headers });
  return [a.status, b.status];
}
`,
	"f/t/prefix.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/t/prefix","input_transforms":{}}}]}}`,
	// f/t/failagg: a step that throws, and a failure module that runs the
	// flow f/t/aggtolerated, whose array ends the run.
	"f/t/failagg.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/t/thrower","input_transforms":{}}}],` +
		`"failure_module":{"id":"failure","value":{"type":"flow","path":"f/t/aggtolerated","input_transforms":{}}}}}`,
	// f/t/cachedtolerated: a flow with a cache_ttl whose step throws with
	// WL_TEST_SECRET and tolerates it, so that the run succeeds with the
	// error and the flow's result is cached; f/t/cachedagg: one whose
	// branchall returns WL_TEST_SECRET as data in one branch and tolerates
	// a throw with it in the other.
	"f/t/cachedtolerated.flow/flow.yaml": `{"summary":"t","value":{"cache_ttl":600,"modules":[{"id":"a","continue_on_error":true,"value":{"type":"script","path":"f/t/thrower","input_transforms":{}}}]}}`,
	"f/t/cachedagg.flow/flow.yaml": `{"summary":"t","value":{"cache_ttl":600,"modules":[{"id":"all","value":{"type":"branchall","parallel":false,"branches":[` +
		`{"skip_failure":false,"modules":[{"id":"x","value":{"type":"script","path":"f/t/env","input_transforms":{"names":{"type":"static","value":["WL_TEST_SECRET"]},"hashed":{"type":"static","value":[]}}}}]},` +
		`{"skip_failure":true,"modules":[{"id":"y","value":{"type":"script","path":"f/t/thrower","input_transforms":{}}}]}]}}]}}`,
	// f/t/copy: step a throws with WL_TEST_SECRET and the flow carries on;
	// step b returns a copy of that error as data, which ends the run.
	"f/t/copy.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","continue_on_error":true,"value":{"type":"script","path":"f/t/thrower","input_transforms":{}}},` +
		`{"id":"b","value":{"type":"rawscript","language":"bun","content":"export async function main(x: any) { return { copy: x } }","input_transforms":{"x":{"type":"javascript","expr":"results.a"}}}}]}}`,
	"f/t/manual.ts": `// Fails by its result: a plain wm_failure message, beside the two
// strings that WL_TEST_FRAGMENT joins, as an array.
export async function main() {
  return { wm_failure: "a plain failure", pieces: (process.env.WL_TEST_FRAGMENT ?? "").split('","') };
}
`,
	"f/t/manual.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/t/manual","input_transforms":{}}}]}}`,
	// f/t/manualagg: a branchall whose first branch returns the strings of
	// WL_TEST_FRAGMENT as data, and whose second fails by its result with
	// them beside its message, which the branch tolerates.
	"f/t/manualagg.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"all","value":{"type":"branchall","parallel":false,"branches":[` +
		`{"skip_failure":false,"modules":[{"id":"x","value":{"type":"script","path":"f/t/fragment","input_transforms":{}}}]},` +
		`{"skip_failure":true,"modules":[{"id":"y","value":{"type":"script","path":"f/t/manual","input_transforms":{}}}]}]}}]}}`,
	"f/t/cachedfragment.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","cache_ttl":600,"value":{"type":"rawscript","language":"bun",` +
		`"content":"export async function main() { return (process.env.WL_TEST_FRAGMENT ?? '').split('\",\"') }","input_transforms":{}}}]}}`,
	"f/t/tail.ts": `// Logs a line of 700 characters, then WL_TEST_SECRET and as many more
// as start the log's last 600 characters, the tail that an escaped error
// quotes, cut bytes into the secret; then exits with status 3, or, with
// die, ends its own process with status 3 once its log is out.
export async function main(cut: number, die: boolean) {
  const secret = process.env.WL_TEST_SECRET ?? "";
  console.log("A".repeat(700));
  console.log(secret + "B".repeat(600 - secret.length + cut));
  if (die) {
    await new Promise((r) => setTimeout(r, 300));
    (process as any).reallyExit(3);
  }
  process.exit(3);
}
`,
	"f/t/latcher.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/t/latcher","input_transforms":{}}}]}}`,
}

// harnessEnvBundle returns the pinned Bun, and a bundle of
// harnessEnvFiles with its harness runtime.
func harnessEnvBundle(t *testing.T) (bun, out, runtimeDir string) {
	t.Helper()
	bun = bunOrSkip(t)
	out, runtimeDir = buildBunBundle(t, bun, harnessEnvFiles)
	return bun, out, runtimeDir
}

// runScript runs the root script path with args and returns its result,
// failing the test unless the run succeeded.
func runScript(t *testing.T, rt *Runtime, path string, args any) json.RawMessage {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	data, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	res, err := rt.Run(context.Background(), path, data, RunOptions{Kind: KindScript})
	if err != nil {
		t.Fatalf("run %s: %v", path, err)
	}
	if res.Status != StatusSuccess {
		t.Fatalf("run %s: %s %s", path, res.Status, res.Result)
	}
	return res.Result
}

// readEnv runs f/t/env with names and hashed and decodes its result.
func readEnv(t *testing.T, rt *Runtime, names, hashed []string) map[string]*string {
	t.Helper()
	var got map[string]*string
	if err := json.Unmarshal(runScript(t, rt, "f/t/env", map[string]any{"names": names, "hashed": hashed}), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

// shown is a value that a step read, quoted, or null.
func shown(s *string) string {
	if s == nil {
		return "null"
	}
	return fmt.Sprintf("%q", *s)
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// syncBuf is a diagnostics writer that a test reads while processes write.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestHarnessEnvReachesStep checks that a real step, in a warm and in a
// fresh runtime, reads each entry of Options.HarnessEnv as a process
// variable: a literal value, and a synthetic secret that ValueEnv takes from
// the engine's environment, which a flow's trace, its warnings and the
// runtime's diagnostics do not hold. A native step reads none of them.
func TestHarnessEnvReachesStep(t *testing.T) {
	bun, out, runtimeDir := harnessEnvBundle(t)
	// The reference and its synthetic value, declared apart: on one line,
	// the secret scan's generic rule reads the pair as a key.
	const ref = "WL_TEST_HARNESS_SECRET_REF"
	const secret = "synthetic-harness-secret-41d9"
	t.Setenv(ref, secret)
	for _, mode := range []string{ModuleStateWarm, ModuleStateFresh} {
		t.Run(mode, func(t *testing.T) {
			diag := &syncBuf{}
			rt, err := Open(out, Options{
				Bun: bun, HarnessRuntime: runtimeDir, ModuleState: mode, Diagnostics: diag,
				HarnessEnv: map[string]EnvValue{
					"WL_TEST_PLAIN":  {Value: "a host setting"},
					"WL_TEST_SECRET": {ValueEnv: ref},
					"WL_TEST_EMPTY":  {},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer rt.Close()
			got := readEnv(t, rt, []string{"WL_TEST_PLAIN", "WL_TEST_SECRET", "WL_TEST_EMPTY", ref}, []string{"WL_TEST_SECRET"})
			want := map[string]string{"WL_TEST_PLAIN": "a host setting", "WL_TEST_SECRET": sha256Hex(secret), "WL_TEST_EMPTY": ""}
			for name, v := range want {
				if got[name] == nil || *got[name] != v {
					t.Errorf("the step's %s: %s, want %q", name, shown(got[name]), v)
				}
			}
			if got[ref] != nil {
				t.Errorf("the step reads the reference %s itself: %q; want it unset, since only the entry names it", ref, *got[ref])
			}

			res, err := rt.Run(context.Background(), "f/t/env", json.RawMessage(`{"names":["WL_TEST_SECRET"],"hashed":["WL_TEST_SECRET"]}`), RunOptions{})
			if err != nil || res.Status != StatusSuccess {
				t.Fatalf("the flow: %+v %v", res, err)
			}
			if string(res.Result) != fmt.Sprintf(`{"WL_TEST_SECRET":%q}`, sha256Hex(secret)) {
				t.Errorf("the flow's step read %s, want the secret's hash", res.Result)
			}
			trace, err := json.Marshal(traceJSON(res.Trace))
			if err != nil {
				t.Fatal(err)
			}
			warnings, _ := json.Marshal(res.Warnings)
			for what, text := range map[string]string{"the trace": string(trace), "the warnings": string(warnings), "the diagnostics": diag.String()} {
				if strings.Contains(text, secret) {
					t.Errorf("%s hold the secret's value: %s", what, text)
				}
			}

			if mode == ModuleStateWarm {
				if r := runScript(t, rt, "f/t/native", nil); string(r) != "null" {
					t.Errorf("a native step read WL_TEST_PLAIN: %s, want null", r)
				}
			}
		})
	}
}

// TestHarnessEnvReachesPlugin checks that a host plugin reads the entries
// of Options.HarnessEnv as process variables when it loads, before any
// run: its module's top-level code keeps them, and its onRunEnd reports
// them as a warning.
func TestHarnessEnvReachesPlugin(t *testing.T) {
	bun, out, runtimeDir := harnessEnvBundle(t)
	const ref = "WL_TEST_HARNESS_PLUGIN_REF"
	t.Setenv(ref, "a referenced setting")
	plugin := filepath.Join(t.TempDir(), "plugin.ts")
	src := `const atLoad = { plain: process.env.WL_TEST_PLAIN ?? null, referenced: process.env.WL_TEST_REFERENCED ?? null };
export default {
  onRunEnd() { return [{ code: "harness_env", message: JSON.stringify(atLoad) }] },
};
`
	if err := os.WriteFile(plugin, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(out, Options{
		Bun: bun, HarnessRuntime: runtimeDir, Plugin: plugin,
		HarnessEnv: map[string]EnvValue{"WL_TEST_PLAIN": {Value: "a plugin setting"}, "WL_TEST_REFERENCED": {ValueEnv: ref}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	res, err := rt.Run(context.Background(), "f/t/env", json.RawMessage(`{"names":[],"hashed":[]}`), RunOptions{})
	if err != nil || res.Status != StatusSuccess {
		t.Fatalf("the flow: %+v %v", res, err)
	}
	var seen string
	for _, w := range res.Warnings {
		if w.Code == "harness_env" {
			seen = w.Message
		}
	}
	if seen != `{"plain":"a plugin setting","referenced":"a referenced setting"}` {
		t.Errorf("the plugin read at load %q (warnings %+v), want both entries", seen, res.Warnings)
	}
}

// TestHarnessEnvReserved checks the names that an entry may not set: Open
// refuses each, naming the entry and never its value, as it refuses a bad
// name or value, Value with ValueEnv, an unset ValueEnv, and any entry in
// host mode. And a step's own variables still win over an entry: its
// script's own envs, while a step without them reads the entry.
func TestHarnessEnvReserved(t *testing.T) {
	dir := writeTestBundle(t, "test-engine", nil, nil)
	const value = "synthetic-reserved-value-5b2e"
	for _, name := range []string{
		"WM_TOKEN", "WM_WORKSPACE", "WM_JOB_ID", "WM_NOT_YET_RESERVED", "BASE_URL", "BASE_INTERNAL_URL",
		"PATH", "HOME", "TZ", "FORCE_COLOR", "DO_NOT_TRACK", "BUN_INSTALL_CACHE_DIR", "BUN_RUNTIME_TRANSPILER_CACHE_PATH", "NODE_PATH",
		"", "A=B", "A\x00B",
	} {
		_, err := Open(dir, Options{EngineVersion: "test-engine", HarnessEnv: map[string]EnvValue{name: {Value: value}}})
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("Options.HarnessEnv[%q]", name)) || strings.Contains(err.Error(), value) {
			t.Errorf("an entry named %q: %v; want a refusal naming the entry and not its value", name, err)
		}
	}
	const ref = "WL_TEST_HARNESS_UNSET_REF"
	os.Unsetenv(ref)
	for _, c := range []struct {
		what  string
		entry EnvValue
		want  string
	}{
		{"a value with a NUL byte", EnvValue{Value: "x\x00" + value}, `Options.HarnessEnv["WL_TEST_X"]`},
		{"Value with ValueEnv", EnvValue{Value: value, ValueEnv: "HOME"}, `Options.HarnessEnv["WL_TEST_X"]`},
		{"an unset ValueEnv", EnvValue{ValueEnv: ref}, ref},
	} {
		_, err := Open(dir, Options{EngineVersion: "test-engine", HarnessEnv: map[string]EnvValue{"WL_TEST_X": c.entry}})
		if err == nil || !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), value) {
			t.Errorf("%s: %v; want a refusal naming %s and not the value", c.what, err, c.want)
		}
	}
	work := t.TempDir()
	if _, err, _, w := openHost(t, dir, Options{Host: &Host{Workdir: work}, HarnessEnv: map[string]EnvValue{"WL_TEST_X": {Value: "x"}}}, work); err == nil || w.Reject == nil || !strings.Contains(err.Error(), "HarnessEnv") {
		t.Errorf("host mode with an entry: %v, welcome %+v; want a refusal naming HarnessEnv", err, w)
	}

	bun, out, runtimeDir := harnessEnvBundle(t)
	rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, HarnessEnv: map[string]EnvValue{"WL_TEST_SHARED": {Value: "from-the-host"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if r := runScript(t, rt, "f/t/own", nil); string(r) != `"from-the-script"` {
		t.Errorf("a step whose script's envs set WL_TEST_SHARED read %s, want its own value", r)
	}
	if r := runScript(t, rt, "f/t/shared", nil); string(r) != `"from-the-host"` {
		t.Errorf("a step without that envs entry read %s, want the entry's value", r)
	}
	got := readEnv(t, rt, []string{"WM_WORKSPACE", "BASE_URL"}, []string{})
	if got["WM_WORKSPACE"] == nil || *got["WM_WORKSPACE"] != "windmill-lite" || got["BASE_URL"] == nil || *got["BASE_URL"] != rt.PlatformOrigin() {
		t.Errorf("the step's WM_WORKSPACE and BASE_URL: %s %s; want the invocation's and the facade's", shown(got["WM_WORKSPACE"]), shown(got["BASE_URL"]))
	}
}

// TestHarnessEnvDefault checks a harness process's whole environment: with
// no entries, exactly today's base environment (BaseEnv and the facade's
// BASE_URL), in a warm and a fresh runtime; with entries, the base
// environment and the entries, nothing else. A warm process's environment
// is what a host plugin reads at load, outside every invocation. A fresh
// process runs no plugin, so there it is what a step reads, without the
// variables that the engine sets for each invocation. Only the names of
// variables that differ are reported, never a value.
func TestHarnessEnvDefault(t *testing.T) {
	bun, out, runtimeDir := harnessEnvBundle(t)
	plugin := filepath.Join(t.TempDir(), "plugin.ts")
	src := `const atLoad = { ...process.env };
export default { onRunEnd() { return [{ code: "process_env", message: JSON.stringify(atLoad) }] } };
`
	if err := os.WriteFile(plugin, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	own := map[string]bool{}
	for k := range (platform.Identity{}).Env(platform.Host{}) {
		own[k] = true
	}
	for _, c := range []struct {
		name  string
		mode  string
		env   map[string]EnvValue
		extra map[string]string
	}{
		{"warm, no entries", ModuleStateWarm, nil, nil},
		{"fresh, no entries", ModuleStateFresh, nil, nil},
		{"warm, an empty map", ModuleStateWarm, map[string]EnvValue{}, nil},
		{"warm, entries", ModuleStateWarm, map[string]EnvValue{"WL_TEST_A": {Value: "a"}, "WL_TEST_B": {Value: "b=c"}}, map[string]string{"WL_TEST_A": "a", "WL_TEST_B": "b=c"}},
		{"fresh, entries", ModuleStateFresh, map[string]EnvValue{"WL_TEST_A": {Value: "a"}}, map[string]string{"WL_TEST_A": "a"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			opts := Options{Bun: bun, HarnessRuntime: runtimeDir, ModuleState: c.mode, HarnessEnv: c.env}
			if c.mode == ModuleStateWarm {
				opts.Plugin = plugin
			}
			rt, err := Open(out, opts)
			if err != nil {
				t.Fatal(err)
			}
			defer rt.Close()
			var seen map[string]string
			if c.mode == ModuleStateWarm {
				res, err := rt.Run(context.Background(), "f/t/env", json.RawMessage(`{"names":[],"hashed":[]}`), RunOptions{})
				if err != nil {
					t.Fatalf("the flow did not start: %v", err)
				}
				// Neither the result nor its warnings are reported: the
				// warning holds the whole process environment.
				if res.Status != StatusSuccess {
					t.Fatalf("the flow ended %s, want success", res.Status)
				}
				for _, w := range res.Warnings {
					if w.Code == "process_env" {
						if err := json.Unmarshal([]byte(w.Message), &seen); err != nil {
							t.Fatal(err)
						}
					}
				}
				if seen == nil {
					t.Fatal("the plugin did not report its process environment")
				}
			} else {
				if err := json.Unmarshal(runScript(t, rt, "f/t/all", nil), &seen); err != nil {
					t.Fatal(err)
				}
				maps.DeleteFunc(seen, func(k, _ string) bool { return own[k] })
			}
			want := map[string]string{"BASE_URL": rt.PlatformOrigin()}
			for _, e := range harness.BaseEnv() {
				k, v, _ := strings.Cut(e, "=")
				want[k] = v
			}
			maps.Copy(want, c.extra)
			if d := envDiff(seen, want); len(d) > 0 {
				t.Errorf("the process environment differs from the base environment and the entries in %v", d)
			}
		})
	}
	// The comparison reports an unexpected variable, a WM_ one included,
	// by its name alone.
	leak := map[string]string{"PATH": "/bin", "WM_TEST_LEAK": "synthetic-leaked-value"}
	if d := envDiff(leak, map[string]string{"PATH": "/bin"}); !slices.Equal(d, []string{"WM_TEST_LEAK"}) {
		t.Errorf("envDiff of an unexpected WM_ variable: %v", d)
	}
}

// envDiff returns, sorted, the names of the variables that got and want do
// not share with one value: missing, unexpected or changed. It never
// returns a value, which may be a credential of the machine that runs the
// test.
func envDiff(got, want map[string]string) []string {
	var names []string
	for k, v := range got {
		if w, ok := want[k]; !ok || w != v {
			names = append(names, k)
		}
	}
	for k := range want {
		if _, ok := got[k]; !ok {
			names = append(names, k)
		}
	}
	slices.Sort(names)
	return names
}

// TestHarnessEnvReferenceReadOnce checks that Open reads a variable of the
// engine's environment once, however many entries name it: two entries
// that name one variable, whose value changes between two reads, give a
// step one value.
func TestHarnessEnvReferenceReadOnce(t *testing.T) {
	bun, out, runtimeDir := harnessEnvBundle(t)
	const ref = "WL_TEST_HARNESS_ROTATING_REF"
	reads := 0
	real := lookupEnv
	t.Cleanup(func() { lookupEnv = real })
	lookupEnv = func(name string) (string, bool) {
		if name != ref {
			return real(name)
		}
		reads++
		return fmt.Sprintf("rotated-value-%d", reads), true
	}
	rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, HarnessEnv: map[string]EnvValue{"WL_TEST_A": {ValueEnv: ref}, "WL_TEST_B": {ValueEnv: ref, Secret: true}}})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	got := readEnv(t, rt, []string{"WL_TEST_A", "WL_TEST_B"}, []string{})
	if got["WL_TEST_A"] == nil || got["WL_TEST_B"] == nil || *got["WL_TEST_A"] != *got["WL_TEST_B"] || reads != 1 {
		t.Errorf("two entries that name one variable: %s and %s after %d reads; want one value from one read", shown(got["WL_TEST_A"]), shown(got["WL_TEST_B"]), reads)
	}
}

// TestHarnessEnvSecretMasked checks that the engine masks the value of a
// Secret entry, a synthetic one that ValueEnv names, in the text that it
// takes from a harness process and passes on, and only there. A host
// plugin prints it at load, also in a line longer than 64 KiB, an around
// wrapper, an argument rewrite and onRunSettled throw with it, and
// onRunEnd returns it in a warning's code and message: the diagnostics, the
// latched errors, the warnings and the traces hold the mask instead, the
// rewrite's latched error is still a HostCallError, and a plain entry
// printed beside it stays. A step's result is the run's data,
// so a step that returns the secret returns it whole. A plugin that throws
// with it at load fails Open with an error that holds the mask.
func TestHarnessEnvSecretMasked(t *testing.T) {
	bun, out, runtimeDir := harnessEnvBundle(t)
	// The reference and its synthetic value, declared apart: on one line,
	// the secret scan's generic rule reads the pair as a key.
	const ref = "WL_TEST_HARNESS_MASKED_REF"
	const secret = "synthetic-masked-secret-6a0e"
	t.Setenv(ref, secret)
	dir := t.TempDir()
	plugin := filepath.Join(dir, "plugin.ts")
	// At load the plugin also writes, at once, one line of the secret 6,000
	// times, each after a "|", about 180 KB: the engine forwards it in
	// parts of about 64 KiB, cut where it cuts no secret in two.
	src := `import { writeSync } from "node:fs";
const secret = process.env.WL_TEST_SECRET ?? "";
console.error("the plugin loaded with " + secret + " and " + process.env.WL_TEST_PLAIN);
// The pipe takes a write in part: write until all of it is in.
const line = Buffer.from("|" + secret + ("|" + secret).repeat(5999) + "\n");
for (let off = 0; off < line.length; ) {
  try {
    off += writeSync(1, line, off, line.length - off);
  } catch (e) {
    if ((e as { code?: string }).code !== "EAGAIN") throw e;
    Bun.sleepSync(1);
  }
}
export default {
  around: [{ selector: { flow: "f/t/env", module: "a" }, fn: () => { throw new Error("around refused " + secret) } }],
  rewriteArgs: [{ selector: { flow: "f/t/shared", module: "a" }, fn: () => { throw new Error("rewrite refused " + secret) } }],
  onRunEnd() { return [{ code: "seen " + secret, message: "run ended with " + secret }] },
  onRunSettled() { throw new Error("settled with " + secret) },
};
`
	if err := os.WriteFile(plugin, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	// A second secret is the start of the first, so that a part of the
	// long line that ends where it ends must not end the first's mask.
	env := map[string]EnvValue{"WL_TEST_SECRET": {ValueEnv: ref, Secret: true}, "WL_TEST_PREFIX": {Value: secret[:15], Secret: true}, "WL_TEST_PLAIN": {Value: "a plain setting"}}
	diag := &syncBuf{}
	rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, Plugin: plugin, Diagnostics: diag, HarnessEnv: env})
	if err != nil {
		t.Fatal(err)
	}
	if got := readEnv(t, rt, []string{"WL_TEST_SECRET"}, []string{}); got["WL_TEST_SECRET"] == nil || *got["WL_TEST_SECRET"] != secret {
		t.Errorf("a step that returns the secret returned %s, want it whole", shown(got["WL_TEST_SECRET"]))
	}
	texts := map[string]string{}
	for flow, refused := range map[string]string{"f/t/env": "around refused " + harness.SecretMask, "f/t/shared": "rewrite refused " + harness.SecretMask} {
		res, err := rt.Run(context.Background(), flow, json.RawMessage(`{"names":[],"hashed":[]}`), RunOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != StatusLatched || res.Latched == nil || !strings.Contains(res.Latched.Error(), refused) {
			t.Errorf("%s: %s, latched %v; want latched with %q", flow, res.Status, res.Latched, refused)
		} else {
			texts[flow+" latched"] = res.Latched.Error()
		}
		// The rewrite's error is the plugin's, which keeps its type.
		var hce *HostCallError
		if flow == "f/t/shared" && (!errors.As(res.Latched, &hce) || !strings.Contains(hce.Message, refused)) {
			t.Errorf("f/t/shared: latched %T; want a HostCallError whose message holds %q", res.Latched, refused)
		}
		warnings, _ := json.Marshal(res.Warnings)
		if !slices.ContainsFunc(res.Warnings, func(w Warning) bool {
			return w.Code == "seen "+harness.SecretMask && w.Message == "run ended with "+harness.SecretMask
		}) {
			t.Errorf("%s: warnings %s; want onRunEnd's with the mask in its code and message", flow, warnings)
		}
		trace, err := json.Marshal(traceJSON(res.Trace))
		if err != nil {
			t.Fatal(err)
		}
		texts[flow+" warnings"], texts[flow+" trace"] = string(warnings), string(trace)
	}
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	texts["diagnostics"] = diag.String()
	if !strings.Contains(texts["diagnostics"], "the plugin loaded with "+harness.SecretMask+" and a plain setting") {
		t.Errorf("the diagnostics: want the plugin's line with the mask and the plain setting")
	}
	if !strings.Contains(texts["diagnostics"], "settled with "+harness.SecretMask) {
		t.Errorf("the diagnostics: want the failed onRunSettled's line with the mask")
	}
	// The long line's parts: each holds nothing but masks and separators,
	// so no part holds a piece of the secret, and the masks add up to the
	// 6,000 secrets that the plugin wrote.
	parts, masks := 0, 0
	for _, line := range strings.Split(texts["diagnostics"], "\n") {
		_, payload, ok := strings.Cut(line, " stdout: ")
		if !ok || !strings.HasPrefix(payload, "|") && !strings.HasPrefix(payload, harness.SecretMask) {
			continue
		}
		parts++
		masks += strings.Count(payload, harness.SecretMask)
		if rest := strings.NewReplacer(harness.SecretMask, "", "|", "").Replace(payload); rest != "" {
			t.Errorf("part %d of the long line holds %d bytes besides masks and separators", parts, len(rest))
		}
	}
	if parts < 2 || masks != 6000 {
		t.Errorf("the long line: %d parts with %d masks; want it in parts, with 6000 masks", parts, masks)
	}

	failing := filepath.Join(dir, "failing.ts")
	if err := os.WriteFile(failing, []byte(`throw new Error("the plugin cannot use " + process.env.WL_TEST_SECRET);`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	diag2 := &syncBuf{}
	_, err = Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, Plugin: failing, Diagnostics: diag2, HarnessEnv: env})
	if err == nil || !strings.Contains(err.Error(), "the plugin cannot use "+harness.SecretMask) {
		t.Errorf("a plugin that throws with the secret at load: %v; want an error that quotes its output with the mask", err)
	} else {
		texts["the failed Open"] = err.Error()
	}
	texts["the failed Open's diagnostics"] = diag2.String()
	for what, text := range texts {
		if strings.Contains(text, secret) {
			t.Errorf("%s hold the secret: %s", what, text)
		}
	}
}

// TestHarnessEnvSecretTrace checks that a run's trace, and the records and
// traces that hooks get, hold a mask where step data holds a Secret
// entry's value, while a successful run's requested result keeps the
// value. On one successful flow, step a returns the secret and step b
// echoes it: the requested result holds the secret, and the trace's
// result, a's result and carried value, b's arguments and result, and what
// OnModuleEnd and OnRunEnd get, hold the mask. A failed flow's and a
// failed script's result, an error text, hold the mask too, and so do a
// latched flow's trace and latch, which keeps its type. A secret of digits
// returned as a number is masked in the trace, its requested result kept;
// a warning's counts keyed by secrets are masked and added; a platform
// call's escaped path and query and its method hold no secret; late output
// and a late result in OnRunSettled's trace hold the mask. An escaped
// step's error quotes its log with the mask, in a warm and in a fresh
// runtime, also as part of the result of a run that succeeds.
func TestHarnessEnvSecretTrace(t *testing.T) {
	bun, out, runtimeDir := harnessEnvBundle(t)
	const ref = "WL_TEST_HARNESS_TRACE_REF"
	const secret = "synthetic-trace-secret-91c4"
	t.Setenv(ref, secret)
	// A secret of digits, one with URL punctuation, and one that is a
	// method's name: Bun's fetch sends only the methods that it knows.
	const number = "8675309123"
	const urlSecret = "synthetic/url secret?&=4e2b"
	const verb = "PATCH"
	// A secret with slashes that a path holds as it is, and a secret that
	// is a JSON document, which a step parses and returns.
	const slashSecret = "synthetic/segment/secret-0b7d"
	const spaceSecret = "synthetic/space secret-91aa"
	// The document has spaces inside its brackets, which the step's result
	// does not keep.
	// and numbers that it spells 1.0 and 0e401 and the step's result 1 and 0.
	const jsonSecret = `[ "synthetic-json-secret-7d21", 1.0, 0e401 ]`
	const jsonCompact = `["synthetic-json-secret-7d21",1,0]`
	// A secret that holds an escape and a plus, which a path and a query
	// keep as they are.
	const literal = "synthetic%2Fliteral+plus-6a0c"
	// A secret that is a fragment of JSON, which spans an array's elements,
	// and one with the space that JSON written with spaces puts between them.
	const fragment = `synthetic-left","synthetic-right`
	const gapFragment = `synthetic-gap-left", "synthetic-gap-right`
	env := map[string]EnvValue{
		"WL_TEST_SECRET":         {ValueEnv: ref, Secret: true},
		"WL_TEST_NUMBER":         {Value: number, Secret: true},
		"WL_TEST_URL_SECRET":     {Value: urlSecret, Secret: true},
		"WL_TEST_METHOD_SECRET":  {Value: verb, Secret: true},
		"WL_TEST_SLASH_SECRET":   {Value: slashSecret, Secret: true},
		"WL_TEST_JSON_SECRET":    {Value: jsonSecret, Secret: true},
		"WL_TEST_SPACE_SECRET":   {Value: spaceSecret, Secret: true},
		"WL_TEST_FRAGMENT":       {Value: fragment, Secret: true},
		"WL_TEST_LITERAL_SECRET": {Value: literal, Secret: true},
		"WL_TEST_GAP_FRAGMENT":   {Value: gapFragment, Secret: true},
	}
	rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, HarnessEnv: env})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	var mu sync.Mutex
	var ends []string
	var endTrace []byte
	hooks := &Hooks{
		OnModuleEnd: func(_ context.Context, _ RunHandle, _ Site, rec StepRecord) error {
			b, _ := json.Marshal(recordJSON(rec))
			mu.Lock()
			ends = append(ends, string(b))
			mu.Unlock()
			return nil
		},
		OnRunEnd: func(_ context.Context, tr *Trace) []Warning {
			b, _ := json.Marshal(traceJSON(tr))
			mu.Lock()
			endTrace = b
			mu.Unlock()
			return nil
		},
	}
	res, err := rt.Run(context.Background(), "f/t/reveal", nil, RunOptions{Hooks: hooks})
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf(`{"WL_TEST_SECRET":%q}`, secret); res.Status != StatusSuccess || string(res.Result) != want {
		t.Errorf("f/t/reveal: %s %s; want success with the secret in its result", res.Status, shownBytes(res.Result, secret))
	}
	masked := fmt.Sprintf(`{"WL_TEST_SECRET":%q}`, harness.SecretMask)
	for _, site := range []string{"m:a", "m:b"} {
		rec, ok := res.Trace.Step(mustSite(t, site))
		if !ok {
			t.Fatalf("no record of %s", site)
		}
		if got := string(rec.Result.Producer()); got != masked {
			t.Errorf("%s's result in the trace: %s, want %s", site, shownBytes([]byte(got), secret), masked)
		}
		if site == "m:b" && (string(rec.RecordedArgs.Producer()) != `{"x":`+masked+`}` || string(rec.ResolvedArgs.Producer()) != `{"x":`+masked+`}`) {
			t.Errorf("m:b's arguments in the trace: %s and %s, want the mask", shownBytes(rec.RecordedArgs.Producer(), secret), shownBytes(rec.ResolvedArgs.Producer(), secret))
		}
	}
	trace, _ := json.Marshal(traceJSON(res.Trace))
	mu.Lock()
	hookTexts := map[string]string{"the trace": string(trace), "OnRunEnd's trace": string(endTrace), "OnModuleEnd's records": strings.Join(ends, "\n")}
	mu.Unlock()
	for what, text := range hookTexts {
		if strings.Contains(text, secret) || !strings.Contains(text, harness.SecretMask) {
			t.Errorf("%s: want the mask and not the secret: %s", what, shownBytes([]byte(text), secret))
		}
	}

	// A failure's result is an error text: it holds the mask, as the trace
	// does, for a flow and for a root script.
	res, err = rt.Run(context.Background(), "f/t/thrower", nil, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	trace, _ = json.Marshal(traceJSON(res.Trace))
	if res.Status != StatusFailure || !strings.Contains(string(res.Result), "failed with "+harness.SecretMask) || strings.Contains(string(res.Result), secret) ||
		strings.Contains(string(trace), secret) || !strings.Contains(string(trace), "failed with "+harness.SecretMask) {
		t.Errorf("f/t/thrower: %s %s; want a failure whose result and trace hold the mask instead of the secret", res.Status, shownBytes(res.Result, secret))
	}
	res, err = rt.Run(context.Background(), "f/t/thrower", json.RawMessage(`{}`), RunOptions{Kind: KindScript})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusFailure || !strings.Contains(string(res.Result), "failed with "+harness.SecretMask) || strings.Contains(string(res.Result), secret) {
		t.Errorf("the script f/t/thrower: %s %s; want a failure whose result holds the mask instead of the secret", res.Status, shownBytes(res.Result, secret))
	}
	// The result is an error, a step's that the flow carried on past, of a
	// run that succeeds: it holds the mask. A run that a stop_after_if
	// skips keeps its module's ordinary result, the host's data.
	res, err = rt.Run(context.Background(), "f/t/tolerated", nil, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusSuccess || !strings.Contains(string(res.Result), "failed with "+harness.SecretMask) || strings.Contains(string(res.Result), secret) {
		t.Errorf("f/t/tolerated: %s %s; want success with the error, which holds the mask", res.Status, shownBytes(res.Result, secret))
	}
	res, err = rt.Run(context.Background(), "f/t/skipped", nil, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf(`{"WL_TEST_SECRET":%q}`, secret); res.Status != StatusSkipped || string(res.Result) != want {
		t.Errorf("f/t/skipped: %s %s; want skipped with its module's result, the secret", res.Status, shownBytes(res.Result, secret))
	}

	// A secret that is a JSON document, parsed and returned: the requested
	// result keeps it, and the trace's copy, which spells it as it was, is
	// the mask as a whole.
	res, err = rt.Run(context.Background(), "f/t/parsed", nil, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusSuccess || string(res.Result) != jsonCompact {
		t.Errorf("f/t/parsed: %s %s; want success with the parsed document", res.Status, shownBytes(res.Result, jsonCompact))
	}
	if trace, _ := json.Marshal(traceJSON(res.Trace)); strings.Contains(string(trace), jsonCompact) || strings.Contains(string(trace), "synthetic-json-secret-7d21") || !json.Valid(trace) {
		t.Errorf("f/t/parsed's trace holds the document: %s", shownBytes(trace, "synthetic-json-secret-7d21"))
	}
	if rec, ok := res.Trace.Step(mustSite(t, "m:a")); !ok || string(rec.Result.Producer()) != fmt.Sprintf("%q", harness.SecretMask) {
		t.Errorf("f/t/parsed's step result in the trace: %s; want the mask", shownBytes(rec.Result.Producer(), "synthetic-json-secret-7d21"))
	}

	// A secret that spans the elements of a step's array: the requested
	// result keeps the array, and the trace's copy is the mask as a whole.
	res, err = rt.Run(context.Background(), "f/t/fragment", nil, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusSuccess || string(res.Result) != `["synthetic-left","synthetic-right"]` {
		t.Errorf("f/t/fragment: %s %s; want success with the array", res.Status, shownBytes(res.Result, fragment))
	}
	if rec, ok := res.Trace.Step(mustSite(t, "m:a")); !ok || string(rec.Result.Producer()) != fmt.Sprintf("%q", harness.SecretMask) {
		t.Errorf("f/t/fragment's step result in the trace: %s; want the mask", shownBytes(rec.Result.Producer(), fragment))
	}

	// A throw inside a branchall's array, in a run that succeeds because
	// its branch tolerates the failure, and in one that fails: the array
	// keeps the other branch's data, the secret, and the error holds the
	// mask.
	sibling := fmt.Sprintf(`{"WL_TEST_SECRET":%q}`, secret)
	for flow, status := range map[string]string{"f/t/aggtolerated": StatusSuccess, "f/t/aggfailed": StatusFailure} {
		res, err := rt.Run(context.Background(), flow, nil, RunOptions{})
		if err != nil {
			t.Fatal(err)
		}
		r := string(res.Result)
		if res.Status != status || strings.Count(r, secret) != 1 || !strings.Contains(r, sibling) || !strings.Contains(r, "failed with "+harness.SecretMask) {
			t.Errorf("%s: %s %s; want %s, the branch's data whole and the error masked", flow, res.Status, shownBytes(res.Result, secret), status)
		}
	}

	// A secret of digits that a step returns as a number: the requested
	// result keeps the number, and the trace holds the mask, as a string.
	// The run-end hook returns a warning whose counts are keyed by two
	// secrets and a plain name: the two masked keys add their counts.
	counted := &Hooks{OnRunEnd: func(context.Context, *Trace) []Warning {
		return []Warning{{Code: "counted", Left: map[string]int{secret: 1, number: 2, "plain": 3}, Untracked: map[string]int{secret: 4}}}
	}}
	res, err = rt.Run(context.Background(), "f/t/number", nil, RunOptions{Hooks: counted})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"n":` + number + `,"plain":"kept"}`; res.Status != StatusSuccess || string(res.Result) != want {
		t.Errorf("f/t/number: %s %s; want success with the number", res.Status, shownBytes(res.Result, number))
	}
	if rec, ok := res.Trace.Step(mustSite(t, "m:a")); !ok || string(rec.Result.Producer()) != fmt.Sprintf(`{"n":%q,"plain":"kept"}`, harness.SecretMask) {
		t.Errorf("f/t/number's step result in the trace: %s; want the number masked and the plain value kept", shownBytes(rec.Result.Producer(), number))
	}
	var counts *Warning
	for i, w := range res.Warnings {
		if w.Code == "counted" {
			counts = &res.Warnings[i]
		}
	}
	if counts == nil || !maps.Equal(counts.Left, map[string]int{harness.SecretMask: 3, "plain": 3}) || !maps.Equal(counts.Untracked, map[string]int{harness.SecretMask: 4}) {
		t.Errorf("the warning's counts: %+v; want the secrets' keys masked, their counts added", counts)
	}

	// The platform calls in the trace: a secret with URL punctuation in a
	// path and a query, which the step escaped, on a route the facade
	// serves and on one it does not; a secret as a method; a secret with
	// slashes as it is, across a path's segments. The two unserved routes'
	// answers, 501, fail the step, and the failure quotes the calls. No
	// copy holds a secret in any form: the failure, the trace, and each
	// call's fields, raw and decoded.
	// A runtime with no secret that is a JSON document.
	plainEnv := maps.Clone(env)
	delete(plainEnv, "WL_TEST_JSON_SECRET")
	callRT, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, HarnessEnv: plainEnv})
	if err != nil {
		t.Fatal(err)
	}
	defer callRT.Close()
	res, err = callRT.Run(context.Background(), "f/t/call", nil, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Trace.PlatformCalls) != 5 {
		t.Fatalf("f/t/call made %d platform calls, want 5", len(res.Trace.PlatformCalls))
	}
	forms := []string{urlSecret, url.PathEscape(urlSecret), url.QueryEscape(urlSecret), jsEncodeURIComponent(urlSecret), verb, slashSecret,
		spaceSecret, strings.ReplaceAll(spaceSecret, " ", "%20")}
	trace, _ = json.Marshal(traceJSON(res.Trace))
	texts := map[string]string{"the failure": string(res.Result), "the trace": string(trace)}
	for i, c := range res.Trace.PlatformCalls {
		path, _ := url.PathUnescape(c.Path)
		query, _ := url.QueryUnescape(c.Query)
		for name, field := range map[string]string{"method": c.Method, "path": c.Path, "query": c.Query, "decoded path": path, "decoded query": query, "message": c.Message} {
			texts[fmt.Sprintf("platform call %d's %s", i, name)] = field
		}
	}
	for what, text := range texts {
		for _, f := range forms {
			if strings.Contains(text, f) {
				t.Errorf("%s holds a secret, as %d bytes", what, len(f))
			}
		}
	}
	if c := res.Trace.PlatformCalls[0]; !strings.Contains(c.Path, url.PathEscape(harness.SecretMask)) || !strings.Contains(c.Query, url.QueryEscape(harness.SecretMask)) {
		t.Errorf("the first platform call: want the mask, escaped, in its path and its query")
	}
	if c := res.Trace.PlatformCalls[1]; c.Method != harness.SecretMask {
		t.Errorf("the second platform call's method: want the mask")
	}
	if c := res.Trace.PlatformCalls[2]; !c.Unsupported || !strings.Contains(c.Message, harness.SecretMask) {
		t.Errorf("the third platform call: unsupported %v; want the 501, its message with the mask", c.Unsupported)
	}
	if c := res.Trace.PlatformCalls[3]; !strings.Contains(c.Path, url.PathEscape(harness.SecretMask)) || !strings.Contains(c.Path, "/keep%2Fme/") {
		t.Errorf("the fourth platform call: want the mask in its path, and its escaped slash kept")
	}
	if q, err := url.ParseQuery(res.Trace.PlatformCalls[0].Query); err != nil || q.Get("tail") != "kept" {
		t.Errorf("the first platform call's query: want its plain parameter kept (%v)", err)
	}
	if c := res.Trace.PlatformCalls[4]; !c.Unsupported || !strings.Contains(c.Message, harness.SecretMask) {
		t.Errorf("the fifth platform call: unsupported %v; want the 501, its message with the mask", c.Unsupported)
	}
	// A secret that holds an escape and a plus, as it is in a path and a
	// query: the fields hold the mask, escaped, and not the secret as sent
	// or as decoded, and the query keeps its plain parameter.
	res, err = callRT.Run(context.Background(), "f/t/literal", nil, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Trace.PlatformCalls) != 1 {
		t.Fatalf("f/t/literal made %d platform calls, want 1", len(res.Trace.PlatformCalls))
	}
	if c := res.Trace.PlatformCalls[0]; strings.Contains(c.Path, literal) || strings.Contains(c.Query, literal) || strings.Contains(c.Path, "synthetic/literal") ||
		!strings.Contains(c.Path, url.PathEscape(harness.SecretMask)) || !strings.Contains(c.Query, url.QueryEscape(harness.SecretMask)) || !strings.HasSuffix(c.Query, "&tail=kept") {
		t.Errorf("f/t/literal's call: path %d bytes, query %d bytes; want the mask, escaped, in both, no form of the secret, and the plain parameter", len(c.Path), len(c.Query))
	}
	// Two secrets, one the start of the other, which holds a slash that the
	// step alone escapes: the path holds the shorter as it is and the longer
	// as decoded, and the fields, the 501's message and the failure hold
	// the mask and nothing of the longer one's end.
	prefixRT, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, HarnessEnv: map[string]EnvValue{
		"WL_TEST_SHORT_SECRET": {Value: "synthetic-pre", Secret: true},
		"WL_TEST_LONG_SECRET":  {Value: "synthetic-pre/fix-tail-4d1a", Secret: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer prefixRT.Close()
	res, err = prefixRT.Run(context.Background(), "f/t/prefix", nil, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	trace, _ = json.Marshal(traceJSON(res.Trace))
	if len(res.Trace.PlatformCalls) != 2 || res.Status != StatusFailure || strings.Contains(string(res.Result), "fix-tail") || strings.Contains(string(trace), "fix-tail") || !strings.Contains(string(res.Result), harness.SecretMask) {
		t.Errorf("f/t/prefix: %s, %d calls, %s; want a failure with the mask and no end of the longer secret, in the result and the trace", res.Status, len(res.Trace.PlatformCalls), shownBytes(res.Result, "fix-tail"))
	}
	for i, c := range res.Trace.PlatformCalls {
		if strings.Contains(c.Path, "fix-tail") || strings.Contains(c.Message, "fix-tail") || !strings.Contains(c.Path, url.PathEscape(harness.SecretMask)) {
			t.Errorf("f/t/prefix's call %d: path %d bytes, message %d bytes; want the mask, escaped, and no end of the longer secret", i, len(c.Path), len(c.Message))
		}
	}
	// A failure that quotes one call, whose only secret is escaped: its
	// JSON holds no other form of a secret. Its runtime has no secret that
	// holds JSON's structure, which would have every JSON value parsed.
	oneRT, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, HarnessEnv: map[string]EnvValue{"WL_TEST_URL_SECRET": {Value: urlSecret, Secret: true}}})
	if err != nil {
		t.Fatal(err)
	}
	defer oneRT.Close()
	res, err = oneRT.Run(context.Background(), "f/t/callone", nil, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	trace, _ = json.Marshal(traceJSON(res.Trace))
	for what, text := range map[string]string{"the failure": string(res.Result), "the trace": string(trace)} {
		if strings.Contains(text, jsEncodeURIComponent(urlSecret)) || !strings.Contains(text, harness.SecretMask) {
			t.Errorf("f/t/callone: %s: want the mask and not the escaped secret", what)
		}
	}

	// The array that a secret spans, from a Go hook that writes a space
	// between its strings, in a runtime with no secret document: the
	// trace's copy is the mask as a whole.
	spaced := &Hooks{Intercept: []Interceptor{{Selector: module("f/t/fragment", "a"), Func: func(context.Context, Call) (Outcome, bool, error) {
		return Outcome{Result: json.RawMessage(`["synthetic-left", "synthetic-right"]`)}, true, nil
	}}}}
	res, err = callRT.Run(context.Background(), "f/t/fragment", nil, RunOptions{Hooks: spaced})
	if err != nil {
		t.Fatal(err)
	}
	if rec, ok := res.Trace.Step(mustSite(t, "m:a")); !ok || string(rec.Result.Producer()) != fmt.Sprintf("%q", harness.SecretMask) {
		t.Errorf("f/t/fragment, answered with spaces: the step result in the trace: %s; want the mask", shownBytes(rec.Result.Producer(), "synthetic-right"))
	}
	// And the other way round: a secret with that space, which the hook's
	// answer holds as it is and the value written compactly does not.
	gapped := &Hooks{Intercept: []Interceptor{{Selector: module("f/t/fragment", "a"), Func: func(context.Context, Call) (Outcome, bool, error) {
		return Outcome{Result: json.RawMessage(`["synthetic-gap-left", "synthetic-gap-right"]`)}, true, nil
	}}}}
	res, err = callRT.Run(context.Background(), "f/t/fragment", nil, RunOptions{Hooks: gapped})
	if err != nil {
		t.Fatal(err)
	}
	if rec, ok := res.Trace.Step(mustSite(t, "m:a")); !ok || string(rec.Result.Producer()) != fmt.Sprintf("%q", harness.SecretMask) {
		t.Errorf("f/t/fragment, answered with a secret's own spaces: the step result in the trace: %s; want the mask", shownBytes(rec.Result.Producer(), gapFragment))
	}

	// Late output that the settled callback's own trace holds: the step
	// fixes its outcome early, then logs and returns the secret, which the
	// run gets late; its invocation settles with the late result.
	settledTrace := make(chan *Trace, 1)
	res, err = rt.Run(context.Background(), "f/t/late", nil, RunOptions{Hooks: &Hooks{OnRunSettled: func(tr *Trace) { settledTrace <- tr }}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case tr := <-settledTrace:
		var late []string
		for _, st := range tr.Steps {
			for _, l := range st.Late {
				late = append(late, string(l.Body.Producer()))
			}
		}
		text := strings.Join(late, "\n")
		if !strings.Contains(text, "late with "+harness.SecretMask) || !strings.Contains(text, "late result with "+harness.SecretMask) || strings.Contains(text, secret) {
			t.Errorf("the late output in OnRunSettled's trace: %s; want the mask and not the secret", shownBytes([]byte(text), secret))
		}
	case <-time.After(30 * time.Second):
		t.Fatal("OnRunSettled was not called within 30 s")
	}

	res, err = rt.Run(context.Background(), "f/t/latcher", nil, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var latch *engine.StepLatch
	trace, _ = json.Marshal(traceJSON(res.Trace))
	if res.Status != StatusLatched || !errors.As(res.Latched, &latch) || latch.Message != "Error: latched with "+harness.SecretMask && !strings.Contains(latch.Message, "latched with "+harness.SecretMask) || strings.Contains(res.Latched.Error(), secret) || strings.Contains(string(trace), secret) {
		t.Errorf("f/t/latcher: %s, latched %T; want a step latch whose message holds the mask, and a trace without the secret", res.Status, res.Latched)
	}

	for _, mode := range []string{ModuleStateWarm, ModuleStateFresh} {
		rt := rt
		if mode == ModuleStateFresh {
			if rt, err = Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, ModuleState: mode, HarnessEnv: env}); err != nil {
				t.Fatal(err)
			}
			defer rt.Close()
		}
		res, err := rt.Run(context.Background(), "f/t/escape", json.RawMessage(`{}`), RunOptions{Kind: KindScript})
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != StatusFailure || !strings.Contains(string(res.Result), "leaving with "+harness.SecretMask) || strings.Contains(string(res.Result), secret) {
			t.Errorf("%s: an escaped step: %s %s; want a failure whose error quotes its log with the mask", mode, res.Status, shownBytes(res.Result, secret))
		}
		// The error, an error text that the engine wrote from the log tail,
		// is part of the result of a run that succeeds, which keeps its
		// result as it is: the tail itself holds the mask.
		res, err = rt.Run(context.Background(), "f/t/escaping", nil, RunOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != StatusSuccess || !strings.Contains(string(res.Result), "leaving with "+harness.SecretMask) || strings.Contains(string(res.Result), secret) {
			t.Errorf("%s: a run whose result is an escaped step's error: %s %s; want success, the error quoting the log with the mask", mode, res.Status, shownBytes(res.Result, secret))
		}
	}
}

// TestHarnessEnvSecretDeclaration checks that a host plugin that loads and
// declares what the engine refuses with a Secret entry's value in it fails
// Open with an error that holds the mask, and the value in no form, raw or
// quoted: a selector whose branch is the value, still ErrSelector, and a
// provider named by the value that Options.Providers names too. The value
// holds a quote, a backslash and an escape character, which a quoting
// error escapes.
func TestHarnessEnvSecretDeclaration(t *testing.T) {
	bun, out, runtimeDir := harnessEnvBundle(t)
	const ref = "WL_TEST_HARNESS_DECLARATION_REF"
	// A quote, a backslash and an escape character, which an error that
	// quotes the value escapes: the last one other than JSON.stringify.
	const secret = "synthetic\"declared\\secret\x1b-3f58"
	t.Setenv(ref, secret)
	dir := t.TempDir()
	branch := filepath.Join(dir, "branch.ts")
	if err := os.WriteFile(branch, []byte(`export default { haltBefore: [{ flow: "f/t/env", module: "a", branches: [process.env.WL_TEST_SECRET ?? ""] }] };`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	provider := filepath.Join(dir, "provider.ts")
	if err := os.WriteFile(provider, []byte(`export default { providers: { [process.env.WL_TEST_SECRET ?? ""]: () => ({ result: 1 }) } };`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := map[string]EnvValue{"WL_TEST_SECRET": {ValueEnv: ref, Secret: true}}
	quoted := strconv.Quote(secret)
	encoded, _ := json.Marshal(secret)
	forms := []string{secret, quoted[1 : len(quoted)-1], string(encoded[1 : len(encoded)-1])}
	for _, c := range []struct {
		what   string
		opts   Options
		target error
	}{
		{"a secret branch", Options{Bun: bun, HarnessRuntime: runtimeDir, Plugin: branch, HarnessEnv: env}, ErrSelector},
		{"a secret provider that Options.Providers names too", Options{Bun: bun, HarnessRuntime: runtimeDir, Plugin: provider, HarnessEnv: env,
			Providers: map[string]Provider{secret: func(context.Context, Call) (Outcome, bool, error) { return Outcome{}, false, nil }}}, nil},
	} {
		_, err := Open(out, c.opts)
		if err == nil || !strings.Contains(err.Error(), harness.SecretMask) || c.target != nil && !errors.Is(err, c.target) {
			t.Errorf("%s: %v; want a refusal with the mask, matching %v", c.what, err, c.target)
			continue
		}
		for _, f := range forms {
			if strings.Contains(err.Error(), f) {
				t.Errorf("%s: the refusal holds the secret as %q", c.what, f)
			}
		}
	}
}

// shownBytes is text for a test's report: itself, or, when it holds the
// secret, its length alone.
func shownBytes(text []byte, secret string) string {
	if strings.Contains(string(text), secret) {
		return fmt.Sprintf("(%d bytes that hold the secret)", len(text))
	}
	return string(text)
}

// jsEncodeURIComponent is s as JavaScript's encodeURIComponent writes it.
func jsEncodeURIComponent(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("-_.!~*'()", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// TestHarnessEnvSecretPluginAnswer checks a host plugin's answer that the
// harness refuses: an interception whose function returns a Secret entry's
// value, which is no outcome. The harness's error spells the answer as
// JSON.stringify does, which escapes the secret's control character other
// than a Go quoted string does. The latched error, the warnings and the
// trace hold the mask, and the secret in no form: raw, Go quoted or as
// JSON.stringify writes it.
func TestHarnessEnvSecretPluginAnswer(t *testing.T) {
	bun, out, runtimeDir := harnessEnvBundle(t)
	const secret = "synthetic\x1besc-secret-2c9a"
	plugin := filepath.Join(t.TempDir(), "plugin.ts")
	src := `export default { intercept: [{ selector: { flow: "f/t/env", module: "a" }, fn: () => process.env.WL_TEST_SECRET }] };
`
	if err := os.WriteFile(plugin, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, Plugin: plugin, HarnessEnv: map[string]EnvValue{"WL_TEST_SECRET": {Value: secret, Secret: true}}})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	res, err := rt.Run(context.Background(), "f/t/env", json.RawMessage(`{"names":[],"hashed":[]}`), RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusLatched || res.Latched == nil || !strings.Contains(res.Latched.Error(), harness.SecretMask) {
		t.Fatalf("%s, latched %v; want latched by the refused answer, with the mask", res.Status, res.Latched)
	}
	quoted := strconv.Quote(secret)
	forms := []string{secret, quoted[1 : len(quoted)-1], `synthetic\u001besc-secret-2c9a`}
	trace, _ := json.Marshal(traceJSON(res.Trace))
	warnings, _ := json.Marshal(res.Warnings)
	for what, text := range map[string]string{"the latched error": res.Latched.Error(), "the warnings": string(warnings), "the trace": string(trace)} {
		for _, f := range forms {
			if strings.Contains(text, f) {
				t.Errorf("%s holds the secret as %q", what, f)
			}
		}
	}
}

// TestHarnessEnvSecretResultCache checks a host's result cache that
// runtimes share (Options.Results): a cache_ttl step's result that holds a
// Secret entry's value is not stored, so a later runtime of the bundle,
// whose entry has another value or is gone, runs the step again and
// returns no earlier secret, in its result or its trace; so does a secret
// that is a JSON document with an ampersand, which the cache entry escapes.
// A result that holds no secret is still stored, also when a secret is the
// entry's own key, "result", and another runtime reads it.
func TestHarnessEnvSecretResultCache(t *testing.T) {
	bun, out, runtimeDir := harnessEnvBundle(t)
	const first = "synthetic-cached-secret-a1f3"
	const second = "synthetic-cached-secret-b2e4"
	cache := &countingCache{ResultCache: platform.NewMemoryResults()}
	for i, c := range []struct {
		env  map[string]EnvValue
		want string
	}{
		{map[string]EnvValue{"WL_TEST_SECRET": {Value: first, Secret: true}}, fmt.Sprintf("%q", first)},
		{map[string]EnvValue{"WL_TEST_SECRET": {Value: second, Secret: true}}, fmt.Sprintf("%q", second)},
		{nil, "null"},
	} {
		rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, Results: cache, HarnessEnv: c.env})
		if err != nil {
			t.Fatal(err)
		}
		res, err := rt.Run(context.Background(), "f/t/cachedsecret", nil, RunOptions{})
		rt.Close()
		if err != nil {
			t.Fatal(err)
		}
		trace, _ := json.Marshal(traceJSON(res.Trace))
		if res.Status != StatusSuccess || string(res.Result) != c.want {
			t.Errorf("runtime %d: %s, a result of %d bytes; want its own value", i, res.Status, len(res.Result))
		}
		if i > 0 && (strings.Contains(string(res.Result), first) || strings.Contains(string(trace), first)) {
			t.Errorf("runtime %d returns the first runtime's secret", i)
		}
	}
	if cache.puts != 1 {
		t.Errorf("%d results stored; want 1, the third runtime's, which holds no secret", cache.puts)
	}
	// A secret that is a JSON document with an ampersand, which the cache
	// entry escapes: a step that returns it parsed has its result kept out
	// too, and a runtime with another document gets its own.
	for i, doc := range []string{`["synthetic&doc-55e1"]`, `["another&doc-66f2"]`} {
		rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, Results: cache, HarnessEnv: map[string]EnvValue{"WL_TEST_DOC": {Value: doc, Secret: true}}})
		if err != nil {
			t.Fatal(err)
		}
		res, err := rt.Run(context.Background(), "f/t/cacheddoc", nil, RunOptions{})
		rt.Close()
		if err != nil || res.Status != StatusSuccess || string(res.Result) != doc {
			t.Errorf("document runtime %d: %v %s, %d bytes; want its own document", i, err, res.Status, len(res.Result))
		}
	}
	if cache.puts != 1 {
		t.Errorf("%d results stored after the documents; want still 1", cache.puts)
	}
	// A secret that spans an array's two strings, the first with an
	// ampersand, which the cache entry escapes: the step that returns the
	// array has it kept out, and a runtime whose secret has since changed
	// gets its own array, not the first's.
	for i, fragment := range []string{`synthetic-left&","synthetic-right`, `rotated-left&","rotated-right`} {
		rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, Results: cache, HarnessEnv: map[string]EnvValue{"WL_TEST_FRAGMENT": {Value: fragment, Secret: true}}})
		if err != nil {
			t.Fatal(err)
		}
		res, err := rt.Run(context.Background(), "f/t/cachedfragment", nil, RunOptions{})
		rt.Close()
		if want := `["` + fragment + `"]`; err != nil || res.Status != StatusSuccess || string(res.Result) != want || cache.puts != 1 {
			t.Errorf("fragment runtime %d: %v %s, %d bytes, %d stored; want its own array, not stored", i, err, res.Status, len(res.Result), cache.puts)
		}
	}
	// A plain result is stored, also by a runtime whose secret is the
	// entry's own key, and a second such runtime reads it.
	for i := range 2 {
		rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, Results: cache, HarnessEnv: map[string]EnvValue{"WL_TEST_SECRET": {Value: "result", Secret: true}}})
		if err != nil {
			t.Fatal(err)
		}
		res, err := rt.Run(context.Background(), "f/t/cachedplain", nil, RunOptions{})
		rt.Close()
		if err != nil || res.Status != StatusSuccess || string(res.Result) != `"plain"` || cache.puts != 2 {
			t.Errorf("plain runtime %d: %v %s, %d stored; want the plain result, stored once", i, err, res.Status, cache.puts)
		}
	}
}

// TestHarnessEnvSecretProvenance checks that a run's requested result is
// masked where the trace says it holds an error, and only there:
//
//   - a flow's cached result that is a tolerated error, which a runtime
//     without the Secret entry stored, and which one with it reads, with no
//     step of its own: the result holds the mask;
//   - a cached result that is a branchall's array, the data of one branch
//     and the tolerated error of the other: the data keeps the value, the
//     error holds the mask;
//   - a step's copy of an earlier error, returned as data: the result keeps
//     it, and the trace holds the mask;
//   - a failure module that runs a flow whose array holds a tolerated
//     throw beside data: the array ends the run, its error masked;
//   - an entry of an earlier engine's format, which names no thrown error:
//     missed when its result holds the secret, read when it does not;
//   - a failure that a step's result marks, with an array beside its
//     message that a secret spans: the failure, as the result and in the
//     trace, has the array masked and keeps its message; inside a
//     branchall, whose other branch returns the array as data, which the
//     result keeps.
func TestHarnessEnvSecretProvenance(t *testing.T) {
	bun, out, runtimeDir := harnessEnvBundle(t)
	const secret = "synthetic-provenance-secret-3c8d"
	const fragment = `synthetic-left","synthetic-right`
	cache := &countingCache{ResultCache: platform.NewMemoryResults()}
	open := func(secretEntry bool) *Runtime {
		rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, Results: cache, HarnessEnv: map[string]EnvValue{
			"WL_TEST_SECRET":   {Value: secret, Secret: secretEntry},
			"WL_TEST_FRAGMENT": {Value: fragment, Secret: true},
		}})
		if err != nil {
			t.Fatal(err)
		}
		return rt
	}
	plain, rt := open(false), open(true)
	defer plain.Close()
	defer rt.Close()
	sibling := fmt.Sprintf(`{"WL_TEST_SECRET":%q}`, secret)
	for _, flow := range []string{"f/t/cachedtolerated", "f/t/cachedagg"} {
		if res, err := plain.Run(context.Background(), flow, nil, RunOptions{}); err != nil || res.Status != StatusSuccess || !strings.Contains(string(res.Result), "failed with "+secret) {
			t.Fatalf("%s without the Secret entry: %v %s; want success with the error", flow, err, res.Status)
		}
		res, err := rt.Run(context.Background(), flow, nil, RunOptions{})
		if err != nil {
			t.Fatal(err)
		}
		r := string(res.Result)
		trace, _ := json.Marshal(traceJSON(res.Trace))
		if res.Status != StatusSuccess || !res.Trace.Cached || !strings.Contains(r, "failed with "+harness.SecretMask) || strings.Contains(string(trace), secret) {
			t.Errorf("%s from the cache: %s, cached %v, %s; want the cached error masked, in the result and the trace", flow, res.Status, res.Trace.Cached, shownBytes(res.Result, secret))
		}
		if flow == "f/t/cachedagg" && (strings.Count(r, secret) != 1 || !strings.Contains(r, sibling)) {
			t.Errorf("%s from the cache: %s; want the branch's data whole", flow, shownBytes(res.Result, secret))
		}
		if flow == "f/t/cachedtolerated" && strings.Contains(r, secret) {
			t.Errorf("%s from the cache: %s; want no secret", flow, shownBytes(res.Result, secret))
		}
	}

	res, err := rt.Run(context.Background(), "f/t/copy", nil, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	trace, _ := json.Marshal(traceJSON(res.Trace))
	if res.Status != StatusSuccess || !strings.HasPrefix(string(res.Result), `{"copy":{"error":`) || !strings.Contains(string(res.Result), "failed with "+secret) || strings.Contains(string(trace), secret) {
		t.Errorf("f/t/copy: %s %s; want success with the copy as it is, and a trace without the secret", res.Status, shownBytes(res.Result, secret))
	}

	// A failure module that runs a flow whose array holds a tolerated
	// throw: the run's result is that array, its error masked and the other
	// branch's data kept.
	res, err = rt.Run(context.Background(), "f/t/failagg", nil, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	trace, _ = json.Marshal(traceJSON(res.Trace))
	if r := string(res.Result); res.Status != StatusFailure || strings.Count(r, secret) != 1 || !strings.Contains(r, sibling) || !strings.Contains(r, "failed with "+harness.SecretMask) || strings.Contains(string(trace), secret) {
		t.Errorf("f/t/failagg: %s %s; want a failure with the module's array, its error masked and the data whole", res.Status, shownBytes(res.Result, secret))
	}

	// An entry in the format of an earlier engine, which names no error
	// that step code threw: one whose result holds the secret is missed, and
	// the run masks its own result's error and keeps the data; one whose
	// result holds none is read as it is.
	for _, c := range []struct {
		value string
		hit   bool
	}{{secret, false}, {"synthetic-unrelated-5e7f", true}} {
		store := &recordingCache{ResultCache: platform.NewMemoryResults()}
		writer, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, Results: store, HarnessEnv: map[string]EnvValue{"WL_TEST_SECRET": {Value: c.value}}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = writer.Run(context.Background(), "f/t/cachedagg", nil, RunOptions{})
		writer.Close()
		key, value := store.last()
		var entry map[string]json.RawMessage
		if err != nil || key == "" || json.Unmarshal(value, &entry) != nil {
			t.Fatalf("priming the entry: %v, key %q", err, key)
		}
		delete(entry, "all_errors")
		delete(entry, "errors")
		legacy, _ := json.Marshal(entry)
		if err := store.ResultCache.Put(context.Background(), key, legacy, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		reader, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, Results: store, HarnessEnv: map[string]EnvValue{"WL_TEST_SECRET": {Value: secret, Secret: true}}})
		if err != nil {
			t.Fatal(err)
		}
		res, err := reader.Run(context.Background(), "f/t/cachedagg", nil, RunOptions{})
		reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		r := string(res.Result)
		switch {
		case res.Trace.Cached != c.hit:
			t.Errorf("an earlier engine's entry holding %d bytes: cached %v, want %v", len(c.value), res.Trace.Cached, c.hit)
		case !c.hit && (strings.Count(r, secret) != 1 || !strings.Contains(r, sibling) || !strings.Contains(r, "failed with "+harness.SecretMask)):
			t.Errorf("an earlier engine's entry with the secret: %s; want the run's own result, the error masked and the data whole", shownBytes(res.Result, secret))
		case c.hit && !strings.Contains(r, "failed with "+c.value):
			t.Errorf("an earlier engine's entry without the secret: %s; want it as stored", r)
		}
	}

	masked := fmt.Sprintf(`"pieces":%q`, harness.SecretMask)
	for flow, status := range map[string]string{"f/t/manual": StatusFailure, "f/t/manualagg": StatusSuccess} {
		res, err := rt.Run(context.Background(), flow, nil, RunOptions{})
		if err != nil {
			t.Fatal(err)
		}
		r := string(res.Result)
		trace, _ := json.Marshal(traceJSON(res.Trace))
		if res.Status != status || !strings.Contains(r, masked) || !strings.Contains(r, `"a plain failure"`) || strings.Contains(string(trace), fragment) {
			t.Errorf("%s: %s %s; want %s, the failure's array masked and its message kept, and a trace without the fragment", flow, res.Status, shownBytes(res.Result, fragment), status)
		}
		if want := strings.Count(r, fragment); flow == "f/t/manualagg" && (want != 1 || !strings.HasPrefix(r, `[["synthetic-left","synthetic-right"],`)) || flow == "f/t/manual" && want != 0 {
			t.Errorf("%s: %s; want the other branch's array whole, and no other copy", flow, shownBytes(res.Result, fragment))
		}
	}
}

// TestHarnessEnvSecretTail checks that an escaped step's error, which
// quotes the last 600 characters of its log, holds no piece of a Secret
// entry's value when those characters start inside it: one cut 1 byte and
// one cut 3 bytes into it, and one that starts and ends alike, cut 2 bytes
// into it or not at all, when the harness reports the escape (the step
// exits), and when the process dies (the step ends it), whose log the
// engine keeps itself.
func TestHarnessEnvSecretTail(t *testing.T) {
	bun, out, runtimeDir := harnessEnvBundle(t)
	// The second secret starts and ends alike, with no hex digit at its
	// start, and its tail starts with it whole, then 2 bytes into it.
	for _, c := range []struct {
		secret string
		cuts   []int
	}{{"synthetic-tail-value-6b0e", []int{1, 3}}, {"xyz7-synthetic-overlap-xyz7", []int{0, 2}}} {
		rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, HarnessEnv: map[string]EnvValue{"WL_TEST_SECRET": {Value: c.secret, Secret: true}}})
		if err != nil {
			t.Fatal(err)
		}
		defer rt.Close()
		for _, die := range []bool{false, true} {
			for _, cut := range c.cuts {
				res, err := rt.Run(context.Background(), "f/t/tail", json.RawMessage(fmt.Sprintf(`{"cut":%d,"die":%v}`, cut, die)), RunOptions{Kind: KindScript})
				if err != nil {
					t.Fatal(err)
				}
				r := string(res.Result)
				var piece bool
				for i := 0; i+6 <= len(c.secret); i++ {
					piece = piece || strings.Contains(r, c.secret[i:i+6])
				}
				if res.Status != StatusFailure || piece || !strings.Contains(r, harness.SecretMask+strings.Repeat("B", 20)) {
					t.Errorf("%d-byte secret, die %v, cut %d: %s %s; want a failure whose tail starts with the mask, and no piece of the secret", len(c.secret), die, cut, res.Status, shownBytes(res.Result, c.secret[cut:]))
				}
			}
		}
	}
}

// recordingCache is a result cache that remembers the last entry stored.
type recordingCache struct {
	ResultCache
	mu    sync.Mutex
	key   string
	value []byte
}

func (c *recordingCache) Put(ctx context.Context, key string, value []byte, expires time.Time) error {
	c.mu.Lock()
	c.key, c.value = key, append([]byte(nil), value...)
	c.mu.Unlock()
	return c.ResultCache.Put(ctx, key, value, expires)
}

// last returns the key and value of the last entry stored.
func (c *recordingCache) last() (string, []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.key, c.value
}
