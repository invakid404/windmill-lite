package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/wl/plugin"
)

// lifetimeFiles is a synthetic folder whose flow f/life/pid returns its
// harness process's pid, and whose host plugin reports as heldAge the
// number in the file that WL_TEST_AGE_FILE names, and makes onRunEnd never
// return for a run whose data is {"hang": true}.
var lifetimeFiles = map[string]string{
	"wmill.yaml": "defaultTs: bun\n",
	"f/life/plugin.ts": `import { readFileSync } from "node:fs";
export default {
  heldAge() {
    const f = process.env.WL_TEST_AGE_FILE;
    return f ? Number(readFileSync(f, "utf8")) : undefined;
  },
  onRunEnd() {
    if ((globalThis as any).wl.run.data?.hang) return new Promise(() => {});
  },
};
`,
	"f/life/pid.ts": `// Returns its process's pid.
export async function main() {
  return process.pid;
}
`,
	"f/life/pid.flow/flow.yaml": `{"summary":"one step that returns its pid","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/life/pid","input_transforms":{}}}]}}`,
}

const lifetimeContract = `roots:
  - flow: f/life/pid
plugin: f/life/plugin.ts
`

// TestPluginPoolRecycleTriggers checks, through windmill-lite plugin, that
// pool.recycle's unsettled_run_ms and held_age_ms reach the pool:
//   - held_age_ms: once the bundle's plugin reports an age at the bound,
//     the next runs reach a new process, and the engine's standard error
//     says why the first left;
//   - unsettled_run_ms: once a run's onRunEnd never returns past the
//     plugin timeout, which holds the run unsettled, the next runs reach a
//     new process.
//
// A negative value of either refuses the start, naming the field.
func TestPluginPoolRecycleTriggers(t *testing.T) {
	bun := absBun(t)
	out := filepath.Join(t.TempDir(), "bundle")
	if err := buildFolder(bun, lifetimeFiles, lifetimeContract, out); err != nil {
		t.Fatal(err)
	}
	pidOf := func(e *pluginEngine, data string) int {
		t.Helper()
		req := &plugin.RunRequest{Path: "f/life/pid"}
		if data != "" {
			req.RunData = []byte(data)
		}
		res := e.mustRun(t, req)
		pid, err := strconv.Atoi(string(res.GetResult()))
		if res.GetStatus() != "success" || err != nil {
			t.Fatalf("f/life/pid: %s, %s", res.GetStatus(), res.GetResult())
		}
		return pid
	}
	// replaced runs the flow until it reaches a process other than first.
	replaced := func(e *pluginEngine, first int, what string) {
		t.Helper()
		for deadline := time.Now().Add(30 * time.Second); pidOf(e, "") == first; time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("%s: runs still reach process %d after 30 s\nthe engine's standard error:\n%s", what, first, e.stderr)
			}
		}
	}

	t.Run("held_age_ms", func(t *testing.T) {
		ageFile := filepath.Join(t.TempDir(), "age")
		if err := os.WriteFile(ageFile, []byte("0"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg := pluginConfigOf(t, bun, out)
		cfg["harness_env"] = map[string]any{"WL_TEST_AGE_FILE": map[string]any{"value": ageFile}}
		cfg["pool"] = map[string]any{"recycle": map[string]any{"held_age_ms": 120000}}
		e := launchPlugin(t, cfg)
		first := pidOf(e, "")
		if err := os.WriteFile(ageFile, []byte("120000"), 0o600); err != nil {
			t.Fatal(err)
		}
		replaced(e, first, "an age at the bound")
		want := fmt.Sprintf("process %d leaves the pool: its host plugin reports a resource held 2m0s after its run ended (the bound is 2m0s)", first)
		for deadline := time.Now().Add(10 * time.Second); !strings.Contains(e.stderr.String(), want); time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("the engine's standard error lacks %q:\n%s", want, e.stderr)
			}
		}
	})

	t.Run("unsettled_run_ms", func(t *testing.T) {
		cfg := pluginConfigOf(t, bun, out)
		cfg["plugin_timeout_ms"] = 300
		cfg["pool"] = map[string]any{"recycle": map[string]any{"unsettled_run_ms": 1}}
		e := launchPlugin(t, cfg)
		first := pidOf(e, "")
		if pid := pidOf(e, `{"hang":true}`); pid != first {
			t.Fatalf("the run whose onRunEnd hangs reached %d, want %d", pid, first)
		}
		replaced(e, first, "a run held unsettled past the bound")
	})

	for _, field := range []string{"unsettled_run_ms", "held_age_ms"} {
		cfg := pluginConfigOf(t, bun, out)
		cfg["pool"] = map[string]any{"recycle": map[string]any{field: -1}}
		code, stderr := startRefused(t, cfg)
		if want := "pool.recycle." + field + ": not between 0 and"; code != 1 || !strings.Contains(stderr, want) {
			t.Errorf("a negative %s: exit %d; want 1, with %q in the standard error:\n%s", field, code, want, stderr)
		}
	}
}
