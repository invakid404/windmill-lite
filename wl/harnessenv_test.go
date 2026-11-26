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
// as it is in the first call's query, where it spans two pairs; with
// WL_TEST_METHOD_SECRET as a method; and with WL_TEST_SLASH_SECRET as it
// is, across a path's segments. Returns the statuses.
export async function main() {
  const e = process.env;
  const api = ` + "`${e.BASE_URL}/api/w/${e.WM_WORKSPACE}`" + `;
  const headers = { Authorization: ` + "`Bearer ${e.WM_TOKEN}`" + ` };
  const s = encodeURIComponent(e.WL_TEST_URL_SECRET ?? "");
  const a = await fetch(api + "/variables/get_value/" + s + "?path=" + s + "&raw=" + e.WL_TEST_URL_SECRET, { headers });
  const b = await fetch(api + "/variables/get_value/u/t/x", { method: e.WL_TEST_METHOD_SECRET, headers });
  const c = await fetch(api + "/no/such/route/" + s + "?q=" + s, { headers });
  const d = await fetch(api + "/variables/get_value/" + e.WL_TEST_SLASH_SECRET, { headers });
  return [a.status, b.status, c.status, d.status];
}
`,
	"f/t/parsed.ts": `// Returns its WL_TEST_JSON_SECRET variable, a JSON document, parsed.
export async function main() {
  return JSON.parse(process.env.WL_TEST_JSON_SECRET ?? "null");
}
`,
	"f/t/parsed.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/t/parsed","input_transforms":{}}}]}}`,
	// f/t/tolerated: a step's error, which the flow carries on past, is the
	// result of a run that succeeds; f/t/skipped: a run that a module's
	// stop_after_if skips keeps that module's ordinary result.
	"f/t/tolerated.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","continue_on_error":true,"value":{"type":"script","path":"f/t/thrower","input_transforms":{}}}]}}`,
	"f/t/skipped.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","stop_after_if":{"expr":"true","skip_if_stopped":true},` +
		`"value":{"type":"script","path":"f/t/env","input_transforms":{"names":{"type":"static","value":["WL_TEST_SECRET"]},"hashed":{"type":"static","value":[]}}}}]}}`,
	// f/t/cachedsecret: a cache_ttl step returns WL_TEST_SECRET;
	// f/t/cachedplain: one returns a plain value.
	"f/t/cachedsecret.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","cache_ttl":600,"value":{"type":"rawscript","language":"bun",` +
		`"content":"export async function main() { return process.env.WL_TEST_SECRET ?? null }","input_transforms":{}}}]}}`,
	"f/t/cachedplain.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","cache_ttl":600,"value":{"type":"rawscript","language":"bun",` +
		`"content":"export async function main() { return 'plain' }","input_transforms":{}}}]}}`,
	// f/t/escaping: a branchall whose first branch's step escapes, which
	// the branch tolerates, and whose second returns 1: the escaped error,
	// which its log tail ends, is part of the result of a run that
	// succeeds.
	"f/t/escaping.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"all","value":{"type":"branchall","parallel":false,"branches":[` +
		`{"skip_failure":true,"modules":[{"id":"x","value":{"type":"script","path":"f/t/escape","input_transforms":{}}}]},` +
		`{"skip_failure":false,"modules":[{"id":"y","value":{"type":"rawscript","language":"bun","content":"export async function main() { return 1 }","input_transforms":{}}}]}]}}]}}`,
	"f/t/call.flow/flow.yaml":    `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/t/call","input_transforms":{}}}]}}`,
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
	const jsonSecret = `["synthetic-json-secret-7d21"]`
	env := map[string]EnvValue{
		"WL_TEST_SECRET":        {ValueEnv: ref, Secret: true},
		"WL_TEST_NUMBER":        {Value: number, Secret: true},
		"WL_TEST_URL_SECRET":    {Value: urlSecret, Secret: true},
		"WL_TEST_METHOD_SECRET": {Value: verb, Secret: true},
		"WL_TEST_SLASH_SECRET":  {Value: slashSecret, Secret: true},
		"WL_TEST_JSON_SECRET":   {Value: jsonSecret, Secret: true},
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
	if res.Status != StatusSuccess || string(res.Result) != jsonSecret {
		t.Errorf("f/t/parsed: %s %s; want success with the parsed document", res.Status, shownBytes(res.Result, jsonSecret))
	}
	if trace, _ := json.Marshal(traceJSON(res.Trace)); strings.Contains(string(trace), jsonSecret) || !json.Valid(trace) {
		t.Errorf("f/t/parsed's trace holds the document: %s", shownBytes(trace, jsonSecret))
	}
	if rec, ok := res.Trace.Step(mustSite(t, "m:a")); !ok || string(rec.Result.Producer()) != fmt.Sprintf("%q", harness.SecretMask) {
		t.Errorf("f/t/parsed's step result in the trace: %s; want the mask", shownBytes(rec.Result.Producer(), jsonSecret))
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
	res, err = rt.Run(context.Background(), "f/t/call", nil, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Trace.PlatformCalls) != 4 {
		t.Fatalf("f/t/call made %d platform calls, want 4", len(res.Trace.PlatformCalls))
	}
	forms := []string{urlSecret, url.PathEscape(urlSecret), url.QueryEscape(urlSecret), jsEncodeURIComponent(urlSecret), verb, slashSecret}
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
	if c := res.Trace.PlatformCalls[3]; !strings.Contains(c.Path, url.PathEscape(harness.SecretMask)) {
		t.Errorf("the fourth platform call: want the mask in its path")
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
// returns no earlier secret, in its result or its trace. A result that
// holds no secret is still stored.
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
	rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, Results: cache, HarnessEnv: map[string]EnvValue{"WL_TEST_SECRET": {Value: first, Secret: true}}})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if res, err := rt.Run(context.Background(), "f/t/cachedplain", nil, RunOptions{}); err != nil || res.Status != StatusSuccess || cache.puts != 2 {
		t.Errorf("a plain cached result: %v, %d stored; want it stored", err, cache.puts)
	}
}
