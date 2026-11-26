package wl

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/invakid404/windmill-lite/internal/harness"
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

// TestHarnessEnvDefault checks what a harness process's environment holds,
// as a step reads it with its invocation's reserved WM_* variables left
// out: with no entries, exactly today's base environment (BaseEnv and the
// facade's BASE_URL), in a warm and a fresh runtime; with entries, the base
// environment and the entries, nothing else.
func TestHarnessEnvDefault(t *testing.T) {
	bun, out, runtimeDir := harnessEnvBundle(t)
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
			rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, ModuleState: c.mode, HarnessEnv: c.env})
			if err != nil {
				t.Fatal(err)
			}
			defer rt.Close()
			var seen map[string]string
			if err := json.Unmarshal(runScript(t, rt, "f/t/all", nil), &seen); err != nil {
				t.Fatal(err)
			}
			maps.DeleteFunc(seen, func(k, _ string) bool { return strings.HasPrefix(k, "WM_") })
			want := map[string]string{"BASE_URL": rt.PlatformOrigin()}
			for _, e := range harness.BaseEnv() {
				k, v, _ := strings.Cut(e, "=")
				want[k] = v
			}
			maps.Copy(want, c.extra)
			if !maps.Equal(seen, want) {
				t.Errorf("the process environment, without the invocation's WM_* variables:\n got  %v\n want %v", sortedEntries(seen), sortedEntries(want))
			}
		})
	}
}

// sortedEntries is m as NAME=value entries in order, for a readable diff.
func sortedEntries(m map[string]string) []string {
	var out []string
	for _, k := range slices.Sorted(maps.Keys(m)) {
		out = append(out, k+"="+m[k])
	}
	return out
}
