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
				if err != nil || res.Status != StatusSuccess {
					t.Fatalf("the flow: %v %v", res, err)
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
// plugin prints it at load, an around wrapper and an argument rewrite
// throw with it, and onRunEnd returns it in a warning: the diagnostics, the
// latched errors, the warnings and the traces hold the mask instead, and a
// plain entry printed beside it stays. A step's result is the run's data,
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
	src := `const secret = process.env.WL_TEST_SECRET ?? "";
console.error("the plugin loaded with " + secret + " and " + process.env.WL_TEST_PLAIN);
export default {
  around: [{ selector: { flow: "f/t/env", module: "a" }, fn: () => { throw new Error("around refused " + secret) } }],
  rewriteArgs: [{ selector: { flow: "f/t/shared", module: "a" }, fn: () => { throw new Error("rewrite refused " + secret) } }],
  onRunEnd() { return [{ code: "seen", message: "run ended with " + secret }] },
};
`
	if err := os.WriteFile(plugin, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	env := map[string]EnvValue{"WL_TEST_SECRET": {ValueEnv: ref, Secret: true}, "WL_TEST_PLAIN": {Value: "a plain setting"}}
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
		warnings, _ := json.Marshal(res.Warnings)
		if !strings.Contains(string(warnings), "run ended with "+harness.SecretMask) {
			t.Errorf("%s: warnings %s; want onRunEnd's with the mask", flow, warnings)
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
		t.Errorf("the diagnostics: want the plugin's line with the mask and the plain setting:\n%s", texts["diagnostics"])
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
