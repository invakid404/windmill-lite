//go:build unix

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/invakid404/windmill-lite/internal/harness"
	"github.com/invakid404/windmill-lite/wl/plugin"
)

// TestPluginHarnessEnvSecrets checks harness_env through `windmill-lite
// plugin`, which the test launches with wl/plugin and calls through
// plugin.Client (ADR 0009, the plugin command).
//
//   - A step reads each entry as a process variable: a value written in the
//     file, an empty one, and a synthetic secret that value_env names, which
//     two entries and a variable share.
//   - The engine reads each value_env once and then unsets it: an entry
//     whose value_env names TZ gets the engine's TZ, and the harness
//     process's base environment, which copies the engine's TZ, then has
//     none. The reference itself never reaches the step.
//   - The secret is in no line of the engine's standard error and not in
//     its build information, whose configuration digest is the SHA-256 of
//     the file.
//   - A secret written in the file, a reserved name, value with value_env
//     (an empty one included), neither of them, an empty value_env, and a
//     reference that only an entry names and that is not set each refuse
//     the start, naming the entry or the reference, and never a value.
func TestPluginHarnessEnvSecrets(t *testing.T) {
	bun, out := pluginBundle(t)
	// The reference and its synthetic value, declared apart: on one line,
	// the secret scan's generic rule reads the pair as a key.
	const ref = "WL_TEST_HARNESS_SECRET_REF"
	const secret = "synthetic-harness-secret-c83f"
	const zone = "Etc/GMT+3"
	cfg := pluginConfigOf(t, bun, out)
	cfg["harness_env"] = map[string]any{
		"WL_TEST_PLAIN":        map[string]any{"value": "a host setting"},
		"WL_TEST_EMPTY":        map[string]any{"value": ""},
		"WL_TEST_SECRET":       map[string]any{"value_env": ref, "is_secret": true},
		"WL_TEST_SECRET_AGAIN": map[string]any{"value_env": ref, "is_secret": true},
		"WL_TEST_ZONE":         map[string]any{"value_env": "TZ"},
	}
	cfg["variables"] = map[string]any{"f/plugin/secret": map[string]any{"value_env": ref, "is_secret": true}}
	data, err := os.ReadFile(writePluginConfig(t, cfg))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)

	e := launchPlugin(t, cfg, ref+"="+secret, "TZ="+zone)
	names := []string{"WL_TEST_PLAIN", "WL_TEST_EMPTY", "WL_TEST_SECRET", "WL_TEST_SECRET_AGAIN", "WL_TEST_ZONE", "TZ", ref}
	args, _ := json.Marshal(map[string]any{"names": names})
	res := e.mustRun(t, &plugin.RunRequest{Kind: "script", Path: "f/plugin/env", Args: args})
	var got map[string]*string
	if err := json.Unmarshal(res.GetResult(), &got); err != nil || res.GetStatus() != "success" {
		t.Fatalf("the step's read: %s %s (%v)", res.GetStatus(), res.GetResult(), err)
	}
	want := map[string]*string{"WL_TEST_PLAIN": ptr("a host setting"), "WL_TEST_EMPTY": ptr(""), "WL_TEST_SECRET": ptr(secret), "WL_TEST_SECRET_AGAIN": ptr(secret), "WL_TEST_ZONE": ptr(zone), "TZ": nil, ref: nil}
	for _, n := range names {
		if g, w := got[n], want[n]; (g == nil) != (w == nil) || g != nil && *g != *w {
			t.Errorf("the step's %s: %s, want %s", n, show(g), show(w))
		}
	}
	res = e.mustRun(t, &plugin.RunRequest{Path: "f/plugin/variable"})
	var read struct {
		Status int
		Value  string
	}
	if err := json.Unmarshal(res.GetResult(), &read); err != nil || read.Status != 200 || read.Value != secret {
		t.Errorf("the variable that shares the reference: %s %s (%v); want 200 with its value", res.GetStatus(), res.GetResult(), err)
	}
	info, err := e.runner.BuildInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.GetConfigDigest() != hex.EncodeToString(sum[:]) {
		t.Errorf("config_digest %s, want the SHA-256 of the file, %x", info.GetConfigDigest(), sum)
	}
	if strings.Contains(fmt.Sprint(info), secret) {
		t.Errorf("the build information holds the secret: %v", info)
	}
	if strings.Contains(e.stderr.String(), secret) {
		t.Errorf("the engine's standard error holds the secret:\n%s", e.stderr)
	}

	const inline = "synthetic-inline-harness-secret-17e0"
	for _, c := range []struct {
		what, field string
		env         map[string]any
	}{
		{"a secret written in the file", `harness_env["WL_TEST_SECRET"].value`, map[string]any{"WL_TEST_SECRET": map[string]any{"value": inline, "is_secret": true}}},
		{"value with value_env", `harness_env["WL_TEST_PLAIN"]`, map[string]any{"WL_TEST_PLAIN": map[string]any{"value": inline, "value_env": ref}}},
		{"neither value nor value_env", `harness_env["WL_TEST_PLAIN"]`, map[string]any{"WL_TEST_PLAIN": map[string]any{"is_secret": true}}},
		{"value with an empty value_env", `harness_env["WL_TEST_PLAIN"]: value and value_env`, map[string]any{"WL_TEST_PLAIN": map[string]any{"value": inline, "value_env": ""}}},
		{"an empty value_env", `harness_env["WL_TEST_PLAIN"].value_env`, map[string]any{"WL_TEST_PLAIN": map[string]any{"value_env": "", "is_secret": true}}},
		{"a reserved variable", `harness_env["WM_TOKEN"]`, map[string]any{"WM_TOKEN": map[string]any{"value": inline}}},
		{"the facade's origin", `harness_env["BASE_URL"]`, map[string]any{"BASE_URL": map[string]any{"value": inline}}},
		{"a base variable", `harness_env["PATH"]`, map[string]any{"PATH": map[string]any{"value": inline}}},
		{"a reserved variable by reference", `harness_env["WM_WORKSPACE"]`, map[string]any{"WM_WORKSPACE": map[string]any{"value_env": ref, "is_secret": true}}},
		{"an unknown field", `unknown field "secret"`, map[string]any{"WL_TEST_PLAIN": map[string]any{"value": inline, "secret": true}}},
	} {
		bad := pluginConfigOf(t, bun, out)
		bad["harness_env"] = c.env
		code, stderr := startRefused(t, bad, ref+"="+secret)
		if code != 1 || !strings.Contains(stderr, c.field) || strings.Contains(stderr, inline) || strings.Contains(stderr, secret) {
			t.Errorf("%s: exit %d, stderr %q; want 1, naming %s and no value", c.what, code, stderr, c.field)
		}
	}
	// A reference that only the entry names, and that is not set.
	const unset = "WL_TEST_HARNESS_UNSET_REF"
	only := pluginConfigOf(t, bun, out)
	only["harness_env"] = map[string]any{"WL_TEST_ONLY": map[string]any{"value_env": unset, "is_secret": true}}
	code, stderr := startRefused(t, only)
	if code != 1 || !strings.Contains(stderr, `harness_env["WL_TEST_ONLY"].value_env`) || !strings.Contains(stderr, unset) {
		t.Errorf("the entry's reference not set: exit %d, stderr %q; want 1, naming the entry and %s", code, stderr, unset)
	}
}

// harnessEnvPluginFiles is a synthetic folder whose bundle has a host
// plugin of its own, which reads the entry WL_TEST_SECRET when it loads.
// With WL_TEST_FAIL_AT_LOAD set to 1 it throws there, with the value in its
// error. Otherwise it prints the value, its argument rewrite for
// f/env/refused throws with it, and its onRunEnd returns it in a warning.
var harnessEnvPluginFiles = map[string]string{
	"wmill.yaml": "defaultTs: bun\n",
	"f/env/echo.ts": `// Returns its argument.
export async function main(x: unknown) {
  return x;
}
`,
	"f/env/plugin.ts": `const secret = process.env.WL_TEST_SECRET ?? "";
if (process.env.WL_TEST_FAIL_AT_LOAD === "1") throw new Error("the plugin cannot use " + secret);
console.error("the plugin loaded with " + secret);
export default {
  rewriteArgs: [{ selector: { flow: "f/env/refused", module: "a" }, fn: () => { throw new Error("rewrite refused " + secret); } }],
  onRunEnd() {
    return [{ code: "seen", message: "run ended with " + secret }];
  },
};
`,
	"f/env/echo.flow/flow.yaml": `{"summary":"one step that echoes","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/env/echo",` +
		`"input_transforms":{"x":{"type":"javascript","expr":"flow_input.x"}}}}]}}`,
	"f/env/refused.flow/flow.yaml": `{"summary":"one step whose arguments the plugin refuses to rewrite","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/env/echo",` +
		`"input_transforms":{"x":{"type":"javascript","expr":"flow_input.x"}}}}]}}`,
}

const harnessEnvPluginContract = `roots:
  - flow: f/env/echo
  - flow: f/env/refused
plugin: f/env/plugin.ts
`

var harnessEnvPluginBuild struct {
	once   sync.Once
	dir    string
	bundle string
	err    error
}

// harnessEnvPluginBundle returns the pinned Bun and the bundle of
// harnessEnvPluginFiles, built once per test binary.
func harnessEnvPluginBundle(t *testing.T) (bun, out string) {
	t.Helper()
	bun = absBun(t)
	harnessEnvPluginBuild.once.Do(func() {
		if harnessEnvPluginBuild.dir, harnessEnvPluginBuild.err = os.MkdirTemp("", "wl-harnessenv-"); harnessEnvPluginBuild.err != nil {
			return
		}
		harnessEnvPluginBuild.bundle = filepath.Join(harnessEnvPluginBuild.dir, "bundle")
		harnessEnvPluginBuild.err = buildFolder(bun, harnessEnvPluginFiles, harnessEnvPluginContract, harnessEnvPluginBuild.bundle)
	})
	if harnessEnvPluginBuild.err != nil {
		t.Fatal(harnessEnvPluginBuild.err)
	}
	return bun, harnessEnvPluginBuild.bundle
}

// TestPluginHarnessEnvSecretMasked checks, through `windmill-lite plugin`,
// that the engine masks a secret entry's value in what it passes on from a
// harness process. The bundle's host plugin prints a synthetic secret at
// load, throws with it in an argument rewrite and returns it in onRunEnd's
// warning: the run results hold the mask instead, and so does the engine's
// whole standard error, read once the engine has exited. A plugin that
// throws with it at load fails the start after the handshake, and the
// engine's whole standard error holds the mask, not the secret.
func TestPluginHarnessEnvSecretMasked(t *testing.T) {
	bun, out := harnessEnvPluginBundle(t)
	// The reference and its synthetic value, declared apart: on one line,
	// the secret scan's generic rule reads the pair as a key.
	const ref = "WL_TEST_HARNESS_MASKED_REF"
	const secret = "synthetic-masked-secret-0d47"
	cfg := pluginConfigOf(t, bun, out)
	cfg["harness_env"] = map[string]any{"WL_TEST_SECRET": map[string]any{"value_env": ref, "is_secret": true}}
	e := launchPlugin(t, cfg, ref+"="+secret)
	args := []byte(`{"x":1}`)
	for path, want := range map[string]string{"f/env/echo": "run ended with " + harness.SecretMask, "f/env/refused": "rewrite refused " + harness.SecretMask} {
		res := e.mustRun(t, &plugin.RunRequest{Path: path, Args: args})
		text := fmt.Sprint(res)
		if !strings.Contains(text, want) || strings.Contains(text, secret) {
			t.Errorf("the result of %s: %s; want %q and not the secret", path, text, want)
		}
	}
	e.client.Kill()
	if stderr := e.stderr.String(); !strings.Contains(stderr, "the plugin loaded with "+harness.SecretMask) || strings.Contains(stderr, secret) {
		t.Errorf("the engine's standard error, once it exited: want the plugin's line with the mask and not the secret:\n%s", stderr)
	}

	failing := pluginConfigOf(t, bun, out)
	failing["harness_env"] = map[string]any{
		"WL_TEST_SECRET":       map[string]any{"value_env": ref, "is_secret": true},
		"WL_TEST_FAIL_AT_LOAD": map[string]any{"value": "1"},
	}
	code, stderr := startFails(t, failing, ref+"="+secret)
	if code != 1 || !strings.Contains(stderr, "the plugin cannot use "+harness.SecretMask) || strings.Contains(stderr, secret) {
		t.Errorf("a plugin that throws with the secret at load: exit %d, stderr:\n%s\nwant 1, quoting its error with the mask and not the secret", code, stderr)
	}
}

func ptr(s string) *string { return &s }

// show is a value that a step read, or null.
func show(s *string) string {
	if s == nil {
		return "null"
	}
	return fmt.Sprintf("%q", *s)
}
