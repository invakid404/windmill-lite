//go:build unix

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

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
//     (an empty one included), neither of them, an empty value_env, a field
//     or an entry given as null, and a reference that only an entry names
//     and that is not set each refuse the start, naming the entry or the
//     reference, and never a value.
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
		{"value with a null value_env", `harness_env["WL_TEST_PLAIN"].value_env: not a string`, map[string]any{"WL_TEST_PLAIN": map[string]any{"value": inline, "value_env": nil}}},
		{"a null value with value_env", `harness_env["WL_TEST_PLAIN"].value: not a string`, map[string]any{"WL_TEST_PLAIN": map[string]any{"value": nil, "value_env": ref}}},
		{"a null is_secret", `harness_env["WL_TEST_PLAIN"].is_secret`, map[string]any{"WL_TEST_PLAIN": map[string]any{"value_env": ref, "is_secret": nil}}},
		{"a null entry", `harness_env["WL_TEST_PLAIN"]: not an object`, map[string]any{"WL_TEST_PLAIN": nil}},
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
// error, and with WL_TEST_BAD_DECLARATION set to 1 it declares a halt whose
// branch is the value, which the engine refuses. Otherwise it prints the
// value, its argument rewrite for f/env/refused throws with it, and its
// onRunEnd returns it in a warning's code and message. The step of
// f/env/reveal returns it.
var harnessEnvPluginFiles = map[string]string{
	"wmill.yaml": "defaultTs: bun\n",
	"f/env/echo.ts": `// Returns its argument.
export async function main(x: unknown) {
  return x;
}
`,
	"f/env/reveal.ts": `// Returns its WL_TEST_SECRET variable.
export async function main() {
  return process.env.WL_TEST_SECRET ?? null;
}
`,
	"f/env/plugin.ts": `const secret = process.env.WL_TEST_SECRET ?? "";
if (process.env.WL_TEST_FAIL_AT_LOAD === "1") throw new Error("the plugin cannot use " + secret);
console.error("the plugin loaded with " + secret);
const plugin: Record<string, unknown> = {
  rewriteArgs: [{ selector: { flow: "f/env/refused", module: "a" }, fn: () => { throw new Error("rewrite refused " + secret); } }],
  onRunEnd() {
    return [{ code: "seen " + secret, message: "run ended with " + secret }];
  },
};
if (process.env.WL_TEST_BAD_DECLARATION === "1") plugin.haltBefore = [{ flow: "f/env/echo", module: "a", branches: [secret] }];
export default plugin;
`,
	"f/env/echo.flow/flow.yaml": `{"summary":"one step that echoes","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/env/echo",` +
		`"input_transforms":{"x":{"type":"javascript","expr":"flow_input.x"}}}}]}}`,
	"f/env/refused.flow/flow.yaml": `{"summary":"one step whose arguments the plugin refuses to rewrite","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/env/echo",` +
		`"input_transforms":{"x":{"type":"javascript","expr":"flow_input.x"}}}}]}}`,
	"f/env/reveal.flow/flow.yaml": `{"summary":"one step that returns the secret","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/env/reveal","input_transforms":{}}}]}}`,
	"f/env/thrower.ts": `// Fails with its WL_TEST_SECRET variable in its error.
export async function main() {
  throw new Error("failed with " + process.env.WL_TEST_SECRET);
}
`,
	"f/env/thrower.flow/flow.yaml": `{"summary":"one step that fails","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/env/thrower","input_transforms":{}}}]}}`,
}

const harnessEnvPluginContract = `roots:
  - flow: f/env/echo
  - flow: f/env/refused
  - flow: f/env/reveal
  - flow: f/env/thrower
plugin: f/env/plugin.ts
`

// TestPluginHarnessEnvSecretMasked checks, through `windmill-lite plugin`,
// that the engine masks a secret entry's value in what it passes on
// besides a run's requested result. The bundle's host plugin prints a
// synthetic secret at load, throws with it in an argument rewrite and
// returns it in onRunEnd's warning: the run results hold the mask instead,
// and so does the engine's whole standard error, read once the engine has
// exited. A flow whose step returns the secret has it in its result, and
// the mask in its trace; a flow that fails with it has the mask in its
// result, an error text, and in its trace. A plugin that throws with a
// secret that holds a quote and a backslash at load, and one whose
// declaration the engine refuses for it, fail the start after the
// handshake, and the engine's whole standard error holds the mask and the
// secret in no form, raw or quoted.
func TestPluginHarnessEnvSecretMasked(t *testing.T) {
	bun := absBun(t)
	out := filepath.Join(t.TempDir(), "bundle")
	if err := buildFolder(bun, harnessEnvPluginFiles, harnessEnvPluginContract, out); err != nil {
		t.Fatal(err)
	}
	// The reference and its synthetic value, declared apart: on one line,
	// the secret scan's generic rule reads the pair as a key.
	const ref = "WL_TEST_HARNESS_MASKED_REF"
	const secret = "synthetic-masked-secret-0d47"
	cfg := pluginConfigOf(t, bun, out)
	cfg["harness_env"] = map[string]any{"WL_TEST_SECRET": map[string]any{"value_env": ref, "is_secret": true}}
	e := launchPlugin(t, cfg, ref+"="+secret)
	args := []byte(`{"x":1}`)
	for path, want := range map[string]string{"f/env/echo": "seen " + harness.SecretMask, "f/env/refused": "rewrite refused " + harness.SecretMask} {
		res := e.mustRun(t, &plugin.RunRequest{Path: path, Args: args})
		text := fmt.Sprint(res)
		if !strings.Contains(text, want) || !strings.Contains(text, "run ended with "+harness.SecretMask) || strings.Contains(text, secret) {
			t.Errorf("the result of %s: want %q and onRunEnd's masked warning, and not the secret", path, want)
		}
	}
	res := e.mustRun(t, &plugin.RunRequest{Path: "f/env/reveal", Trace: "full"})
	if string(res.GetResult()) != fmt.Sprintf("%q", secret) {
		t.Errorf("f/env/reveal's result: %d bytes, want the secret", len(res.GetResult()))
	}
	if trace := string(res.GetTrace()); !strings.Contains(trace, fmt.Sprintf("%q", harness.SecretMask)) || strings.Contains(trace, secret) {
		t.Errorf("f/env/reveal's trace: want the mask as the step's result and not the secret")
	}
	// A failed flow's result is an error text, which holds the mask.
	res = e.mustRun(t, &plugin.RunRequest{Path: "f/env/thrower", Trace: "full"})
	if result := string(res.GetResult()); res.GetStatus() != "failure" || !strings.Contains(result, "failed with "+harness.SecretMask) || strings.Contains(result, secret) || strings.Contains(string(res.GetTrace()), secret) {
		t.Errorf("f/env/thrower: %s; want a failure whose result and trace hold the mask and not the secret", res.GetStatus())
	}
	e.client.Kill()
	if stderr := e.stderr.String(); !strings.Contains(stderr, "the plugin loaded with "+harness.SecretMask) || strings.Contains(stderr, secret) {
		t.Errorf("the engine's standard error, once it exited: want the plugin's line with the mask and not the secret")
	}

	// The failed starts' secret holds a quote, a backslash and an escape
	// character, which a refusal that quotes the value escapes: no form of
	// it may show.
	const quotedSecret = "synthetic\"quoted\\secret\x1b-77aa"
	quoted := strconv.Quote(quotedSecret)
	encoded, _ := json.Marshal(quotedSecret)
	forms := []string{quotedSecret, quoted[1 : len(quoted)-1], string(encoded[1 : len(encoded)-1])}
	for _, c := range []struct {
		flag, want string
	}{
		{"WL_TEST_FAIL_AT_LOAD", "the plugin cannot use " + harness.SecretMask},
		{"WL_TEST_BAD_DECLARATION", harness.SecretMask},
	} {
		failing := pluginConfigOf(t, bun, out)
		failing["harness_env"] = map[string]any{
			"WL_TEST_SECRET": map[string]any{"value_env": ref, "is_secret": true},
			c.flag:           map[string]any{"value": "1"},
		}
		code, stderr := startFails(t, failing, ref+"="+quotedSecret)
		if code != 1 || !strings.Contains(stderr, c.want) {
			t.Errorf("a start that fails with %s: exit %d; want 1, with %q in the standard error", c.flag, code, c.want)
		}
		for _, f := range forms {
			if strings.Contains(stderr, f) {
				t.Errorf("a start that fails with %s: the standard error holds the secret as %q", c.flag, f)
			}
		}
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

// TestPluginHarnessEnvSecretBoundaries checks the command's final output
// where the words that it, the runtime or the engine's server put beside a
// text make a secret entry's value with it. Four synthetic secrets by
// reference, each a span of such words and what follows them:
//   - An entry whose reference is not set, which sorts after the secrets:
//     the reference's name and its reason make the first secret. The start
//     is refused, naming the entry, and standard error holds no secret.
//   - A warm start whose Bun is missing: the command's prefix and the
//     runtime's error make the second. The engine exits with status 1, and
//     standard error holds no secret.
//   - A served engine: the Cancel of an id of no run answers NOT_FOUND,
//     whose message makes the third with the id, which holds no secret
//     there. A replacement process then fails to start, and the drain's
//     reason makes the fourth with the start's error: the engine drains,
//     and standard error holds no secret.
func TestPluginHarnessEnvSecretBoundaries(t *testing.T) {
	bun, out := pluginBundle(t)
	// The references and their synthetic values, declared apart: on one
	// line, the secret scan's generic rule reads a pair as a key.
	refs := []string{"WL_TEST_BOUNDARY_A", "WL_TEST_BOUNDARY_B", "WL_TEST_BOUNDARY_C", "WL_TEST_BOUNDARY_D"}
	values := []string{`SYNTHETIC_20" is not set`, "plugin: wl: the warm start", `the id "synthetic-20`, "a harness process: harness"}
	var env []string
	entries := map[string]any{}
	for i, ref := range refs {
		env = append(env, ref+"="+values[i])
		entries[fmt.Sprintf("WL_TEST_S%d", i)] = map[string]any{"value_env": ref, "is_secret": true}
	}
	holds := func(text string) bool {
		return slices.ContainsFunc(values, func(v string) bool { return strings.Contains(text, v) })
	}

	lookup := pluginConfigOf(t, bun, out)
	unset := maps.Clone(entries)
	unset["WL_TEST_UNSET"] = map[string]any{"value_env": "MISSING_SYNTHETIC_20", "is_secret": true}
	lookup["harness_env"] = unset
	if code, stderr := runToExit(t, lookup, env...); code != 1 || !strings.Contains(stderr, `harness_env["WL_TEST_UNSET"].value_env`) || holds(stderr) {
		t.Errorf("a reference not set after the secrets: exit %d, stderr %q; want 1, naming the entry, and no secret", code, stderr)
	}

	missing := pluginConfigOf(t, filepath.Join(t.TempDir(), "bun"), out)
	missing["pool"] = map[string]any{"min": 1, "max": 1, "warm_start": true}
	missing["harness_env"] = entries
	if code, stderr := runToExit(t, missing, env...); code != 1 || !strings.Contains(stderr, "of the harness pool: harness: start") || holds(stderr) {
		t.Errorf("a warm start whose Bun is missing: exit %d, stderr %q; want 1, naming the pool's failed start, and no secret", code, stderr)
	}

	marker := filepath.Join(t.TempDir(), "fail")
	fake := filepath.Join(t.TempDir(), "bun")
	script := fmt.Sprintf("#!/bin/sh\nif [ -e %s ]; then exit 3; fi\nexec %s \"$@\"\n", strconv.Quote(marker), strconv.Quote(bun))
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := pluginConfigOf(t, fake, out)
	cfg["pool"] = map[string]any{"min": 1, "max": 1, "warm_start": true}
	cfg["harness_env"] = entries
	d := startDirect(t, cfg, true, env...)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := d.runner.Cancel(ctx, &plugin.CancelRequest{Id: "synthetic-20-run"}); err == nil || !strings.Contains(err.Error(), harness.SecretMask) || holds(err.Error()) {
		t.Errorf("the Cancel of an id of no run: %v; want NOT_FOUND with the mask and no secret", err)
	}
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// The step of this run ignores its cancellation, so the run's
	// invocation is abandoned and taints the only process; the next run
	// needs a replacement, which fails to start.
	dir, args := newGate(t)
	tainting := d.runAsync(&plugin.RunRequest{Id: "taints", Path: "f/plugin/gate", Args: args})
	waitFile(t, dir, "entered")
	if _, err := d.runner.Cancel(ctx, &plugin.CancelRequest{Id: "taints", Reason: "taint the process", Canceler: "plugin-test"}); err != nil {
		t.Fatal(err)
	}
	<-tainting
	<-d.runAsync(&plugin.RunRequest{Path: "f/plugin/echo", Args: []byte(`{"x":1}`)})
	if code := d.exitCode(t); code != 0 || !strings.Contains(d.stderr.String(), "draining: the warm pool failed to start") || holds(d.stderr.String()) {
		t.Errorf("a replacement that fails to start: exit %d; want 0, a drain that names the failed start, and no secret\nits standard error:\n%s", code, d.stderr)
	}
}

// TestPluginOptionsMaskRefusal checks the plugin command's resolver alone,
// as the command calls it: an entry whose reference is not set sorts after
// a secret entry that resolved, whose value the refusal's message makes
// with the name that it quotes. The error names the entry and holds no
// secret, whatever writer it would go to.
func TestPluginOptionsMaskRefusal(t *testing.T) {
	const ref, resolved = "WL_TEST_OPTIONS_REF", `SYNTHETIC_20" is not set`
	t.Setenv(ref, resolved)
	raw := pluginConfigOf(t, "bun", "bundle")
	raw["harness_env"] = map[string]any{
		"WL_TEST_A": map[string]any{"value_env": ref, "is_secret": true},
		"WL_TEST_B": map[string]any{"value_env": "MISSING_SYNTHETIC_20", "is_secret": true},
	}
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := decodePluginConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = cfg.options(io.Discard)
	if err == nil || !strings.Contains(err.Error(), `harness_env["WL_TEST_B"].value_env`) || strings.Contains(err.Error(), resolved) {
		t.Errorf("a reference not set after a secret resolved: %v; want an error that names the entry and holds no secret", err)
	}
}

// TestPluginMaskingWriter checks the writer that the plugin command's
// standard error goes through: a line written in two parts, a secret split
// between them, is masked whole once it ends; a value armed later masks
// from the next line; and a last line with no end is masked when the
// command ends.
func TestPluginMaskingWriter(t *testing.T) {
	var out bytes.Buffer
	w := &maskingWriter{w: &out}
	w.add("synthetic-writer-20")
	fmt.Fprint(w, "a synthetic-wri")
	fmt.Fprint(w, "ter-20 b\n")
	w.add("later-synthetic-20")
	fmt.Fprint(w, "then later-synthetic-20\nlast synthetic-writer-20")
	w.flush()
	if want := "a [secret] b\nthen [secret]\nlast [secret]"; out.String() != want {
		t.Errorf("the writer wrote %q; want %q", out.String(), want)
	}
}

// runToExit runs the plugin command with cfg and env, whether or not it
// completes go-plugin's handshake, and returns its exit status and its
// standard error once it exits.
func runToExit(t *testing.T, cfg map[string]any, env ...string) (int, string) {
	t.Helper()
	exe, _ := engineExecutable(t)
	sockets, err := os.MkdirTemp("", "wlx-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockets) })
	cmd := exec.Command(exe, "plugin", "-config", writePluginConfig(t, cfg))
	cmd.Env = engineEnv(append([]string{plugin.Handshake.MagicCookieKey + "=" + plugin.Handshake.MagicCookieValue, "PLUGIN_UNIX_SOCKET_DIR=" + sockets}, env...)...)
	var stderr syncBuffer
	cmd.Stderr, cmd.Stdout = &stderr, io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case <-time.After(time.Minute):
		cmd.Process.Kill()
		<-done
		t.Fatalf("the engine did not exit within a minute\nits standard error:\n%s", stderr.String())
	}
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatal(err)
	}
	return cmd.ProcessState.ExitCode(), stderr.String()
}
