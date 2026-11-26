//go:build unix

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/wl/plugin"
)

// TestPluginHarnessEnvSecrets checks harness_env through `windmill-lite
// plugin`, which the test launches with wl/plugin and calls through
// plugin.Client (ADR 0009, the plugin command).
//
//   - A step reads each entry as a process variable: a value written in the
//     file, and a synthetic secret that value_env names, which two entries
//     and a variable share.
//   - The engine reads each value_env once and then unsets it: an entry
//     whose value_env names TZ gets the engine's TZ, and the harness
//     process's base environment, which copies the engine's TZ, then has
//     none. The reference itself never reaches the step.
//   - The secret is in no line of the engine's standard error and not in
//     its build information, whose configuration digest is the SHA-256 of
//     the file.
//   - A secret written in the file, a reference that is not set, a reserved
//     name, and both value and value_env each refuse the start, naming the
//     entry or the reference, and never a value.
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
	names := []string{"WL_TEST_PLAIN", "WL_TEST_SECRET", "WL_TEST_SECRET_AGAIN", "WL_TEST_ZONE", "TZ", ref}
	args, _ := json.Marshal(map[string]any{"names": names})
	res := e.mustRun(t, &plugin.RunRequest{Kind: "script", Path: "f/plugin/env", Args: args})
	var got map[string]*string
	if err := json.Unmarshal(res.GetResult(), &got); err != nil || res.GetStatus() != "success" {
		t.Fatalf("the step's read: %s %s (%v)", res.GetStatus(), res.GetResult(), err)
	}
	want := map[string]*string{"WL_TEST_PLAIN": ptr("a host setting"), "WL_TEST_SECRET": ptr(secret), "WL_TEST_SECRET_AGAIN": ptr(secret), "WL_TEST_ZONE": ptr(zone), "TZ": nil, ref: nil}
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
	code, stderr := startRefused(t, cfg, "TZ="+zone)
	if code != 1 || !strings.Contains(stderr, ref) {
		t.Errorf("the reference not set: exit %d, stderr %q; want 1, naming %s", code, stderr, ref)
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
