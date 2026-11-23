package wl

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/bundle"
	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/harness"
)

// Open refuses a bound on live processes below the pool's maximum, since
// the pool could not reach its size, and names both; a bound equal to the
// maximum opens.
func TestPoolMaxLiveBelowMax(t *testing.T) {
	dir := writeTestBundle(t, "test-engine", nil, nil)
	_, err := Open(dir, Options{EngineVersion: "test-engine", Pool: PoolOptions{Max: 2, MaxLive: 1}})
	if err == nil || !strings.Contains(err.Error(), "MaxLive 1") || !strings.Contains(err.Error(), "Max 2") {
		t.Fatalf("MaxLive 1 with Max 2: %v", err)
	}
	rt, err := Open(dir, Options{EngineVersion: "test-engine", Pool: PoolOptions{Max: 2, MaxLive: 2}})
	if err != nil {
		t.Fatalf("MaxLive 2 with Max 2: %v", err)
	}
	rt.Close()
}

// Pool settings are a warm pool's: a fresh runtime, whether the option or
// the manifest makes it fresh, and host mode refuse every one of them, and
// still open with the zero value.
func TestPoolOptionsRefusedFresh(t *testing.T) {
	dir := writeTestBundle(t, "test-engine", nil, nil)
	freshDir := writeTestBundle(t, "test-engine", nil, func(m *bundle.Manifest) { m.ModuleState = engine.ModuleStateFresh })
	settings := map[string]PoolOptions{
		"Min":             {Min: 1},
		"Max":             {Max: 2},
		"Cap":             {Cap: 1},
		"LagThreshold":    {LagThreshold: -1},
		"RecycleTimeout":  {RecycleTimeout: time.Second},
		"Recycle.RSS":     {Recycle: RecycleTriggers{RSS: 1 << 30}},
		"Recycle.Samples": {Recycle: RecycleTriggers{Samples: 3}},
		"Recycle.Modules": {Recycle: RecycleTriggers{Modules: 100}},
		"MaxLive":         {MaxLive: 2},
		"WarmStart":       {WarmStart: true},
	}
	for name, pool := range settings {
		t.Run(name, func(t *testing.T) {
			if _, err := Open(dir, Options{EngineVersion: "test-engine", ModuleState: ModuleStateFresh, Pool: pool}); err == nil || !strings.Contains(err.Error(), "Options.Pool") {
				t.Errorf("fresh by the option: %v", err)
			}
			if _, err := Open(freshDir, Options{EngineVersion: "test-engine", Pool: pool}); err == nil || !strings.Contains(err.Error(), "Options.Pool") {
				t.Errorf("fresh by the manifest: %v", err)
			}
			work := t.TempDir()
			_, err, _, w := openHost(t, dir, Options{Pool: pool, Host: &Host{Workdir: work}}, work)
			if err == nil || w.Reject == nil || w.Reject.Code != RejectEngine || !strings.Contains(w.Reject.Message, "Options.Pool") {
				t.Errorf("host mode: %v, welcome %+v", err, w)
			}
		})
	}
	for _, d := range []string{dir, freshDir} {
		rt, err := Open(d, Options{EngineVersion: "test-engine", ModuleState: ModuleStateFresh})
		if err != nil {
			t.Fatalf("fresh with the zero Pool: %v", err)
		}
		rt.Close()
	}
	work := t.TempDir()
	if rt, err, _, _ := openHost(t, dir, Options{Host: &Host{Workdir: work}}, work); err != nil || rt == nil {
		t.Fatalf("host mode with the zero Pool: %v", err)
	}
}

// poolBundle builds a real bundle of root scripts that report the pid of
// the harness process they run in: f/t/pid and f/t/other at once, f/t/spin
// once the file gate exists, blocking its process's event loop until then,
// and f/t/gate once the file gate exists, awaiting it. f/t/spin and f/t/gate
// first write their pid to the file started. The flow f/t/flow has one
// step.
func poolBundle(t *testing.T, bun string) (out, runtimeDir string) {
	t.Helper()
	return buildBunBundle(t, bun, map[string]string{
		"wmill.yaml":   "defaultTs: bun\n",
		"f/t/pid.ts":   "export async function main() { return process.pid }\n",
		"f/t/other.ts": "export async function main() { return process.pid }\n",
		"f/t/spin.ts": `import { existsSync, writeFileSync } from "node:fs";
export async function main(started: string, gate: string) {
  writeFileSync(started, String(process.pid));
  while (!existsSync(gate)) {}
  return process.pid;
}
`,
		"f/t/gate.ts": `import { existsSync, writeFileSync } from "node:fs";
export async function main(started: string, gate: string) {
  writeFileSync(started, String(process.pid));
  while (!existsSync(gate)) await new Promise((r) => setTimeout(r, 5));
  return process.pid;
}
`,
		"f/t/flow.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"rawscript","language":"bun",` +
			`"content":"export async function main() { return 1 }","input_transforms":{}}}]}}`,
	})
}

// runPID runs the root script path with args and returns the pid it
// reported.
func runPID(t *testing.T, rt *Runtime, path string, args map[string]string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	b := []byte(`{}`)
	if args != nil {
		b, _ = json.Marshal(args)
	}
	res, err := rt.Run(ctx, path, b, RunOptions{Kind: KindScript})
	if err != nil || res.Status != StatusSuccess {
		t.Fatalf("%s: %+v, %v", path, res, err)
	}
	pid, err := strconv.Atoi(string(res.Result))
	if err != nil {
		t.Fatalf("%s returned %s", path, res.Result)
	}
	return pid
}

// gatedRun starts the root script path, f/t/spin or f/t/gate, with its
// files in dir, waits until it has started, and returns the pid it runs in
// and the function that opens its gate and returns the pid it reported.
func gatedRun(t *testing.T, rt *Runtime, path, dir, name string) (int, func() int) {
	t.Helper()
	started, gate := filepath.Join(dir, name+".started"), filepath.Join(dir, name+".gate")
	t.Cleanup(func() { os.WriteFile(gate, nil, 0o600) })
	pid := make(chan int, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		b, _ := json.Marshal(map[string]string{"started": started, "gate": gate})
		res, err := rt.Run(ctx, path, b, RunOptions{Kind: KindScript})
		if err != nil || res.Status != StatusSuccess {
			t.Errorf("%s: %+v, %v", path, res, err)
			pid <- 0
			return
		}
		n, _ := strconv.Atoi(string(res.Result))
		pid <- n
	}()
	var running int
	for deadline := time.Now().Add(time.Minute); ; time.Sleep(5 * time.Millisecond) {
		if b, err := os.ReadFile(started); err == nil && len(b) > 0 {
			if running, err = strconv.Atoi(string(b)); err != nil {
				t.Fatalf("%s wrote %q", path, b)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not start", path)
		}
	}
	return running, func() int {
		os.WriteFile(gate, nil, 0o600)
		return <-pid
	}
}

// The runtime's Pool reaches its harness pool. A cap lists D15 in each
// run's trace. With Max 2, a CPU-bound step that blocks one process's
// event loop sends the next step to a second process; with Cap 1, a second
// step in flight goes to a second process; the module and RSS triggers
// replace a process.
func TestWlPoolOptions(t *testing.T) {
	t.Run("settings", func(t *testing.T) {
		got := PoolOptions{Min: 1, Max: 3, Cap: 4, LagThreshold: time.Second, RecycleTimeout: 2 * time.Second, MaxLive: 5, WarmStart: true,
			Recycle: RecycleTriggers{RSS: 6, Samples: 7, Modules: 8}}.harnessConfig()
		want := harness.PoolConfig{Min: 1, Max: 3, Cap: 4, LagThreshold: time.Second, RecycleTimeout: 2 * time.Second, MaxLive: 5,
			Recycle: harness.RecycleTriggers{RSS: 6, Samples: 7, Modules: 8}}
		if got.Min != want.Min || got.Max != want.Max || got.Cap != want.Cap || got.LagThreshold != want.LagThreshold ||
			got.RecycleTimeout != want.RecycleTimeout || got.MaxLive != want.MaxLive || got.Recycle != want.Recycle {
			t.Fatalf("harness pool settings %+v, want %+v", got, want)
		}
	})
	t.Run("cap in the trace", func(t *testing.T) {
		dir := writeTestBundle(t, "test-engine", nil, nil)
		for _, tc := range []struct {
			cap  int
			want bool
		}{{0, false}, {1, true}} {
			rt, err := Open(dir, Options{EngineVersion: "test-engine", Pool: PoolOptions{Cap: tc.cap}})
			if err != nil {
				t.Fatal(err)
			}
			res, err := rt.Run(context.Background(), "f/t/echo", json.RawMessage(`{}`), RunOptions{})
			rt.Close()
			if err != nil {
				t.Fatal(err)
			}
			if got := slices.Contains(res.Trace.Deviations, "D15"); got != tc.want {
				t.Fatalf("Cap %d: deviations %v", tc.cap, res.Trace.Deviations)
			}
		}
	})
	t.Run("processes", func(t *testing.T) {
		bun := bunOrSkip(t)
		out, runtimeDir := poolBundle(t, bun)
		open := func(pool PoolOptions) *Runtime {
			rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, Pool: pool})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { rt.Close() })
			return rt
		}
		gates := t.TempDir()

		// Max 2: a spinning step's lag sends the next step elsewhere.
		rt := open(PoolOptions{Max: 2, LagThreshold: 50 * time.Millisecond})
		a, spin := gatedRun(t, rt, "f/t/spin", gates, "spin")
		waitLag(t, rt, a, 50*time.Millisecond)
		other := make(chan int, 1)
		go func() { other <- runPID(t, rt, "f/t/pid", nil) }()
		select {
		case b := <-other:
			if b == a {
				t.Fatalf("the step ran in the spinning process %d", a)
			}
		case <-time.After(30 * time.Second):
			spin()
			t.Fatal("the step waited for the spinning process")
		}
		if got := spin(); got != a {
			t.Fatalf("the spin ran in %d, want %d", got, a)
		}

		// Cap 1: with one step in flight in a process, the next starts
		// another; lag routing is off.
		rt = open(PoolOptions{Max: 2, Cap: 1, LagThreshold: -1})
		p1, first := gatedRun(t, rt, "f/t/gate", gates, "first")
		p2, second := gatedRun(t, rt, "f/t/gate", gates, "second")
		if p1 == p2 {
			t.Fatalf("two steps in flight in process %d under Cap 1", p1)
		}
		if first() != p1 || second() != p2 {
			t.Fatal("a gated step reported another pid")
		}

		// Modules 1: the step of a second script still runs in the process,
		// and the next goes to its replacement.
		rt = open(PoolOptions{Recycle: RecycleTriggers{Modules: 1}})
		a = runPID(t, rt, "f/t/pid", nil)
		if got := runPID(t, rt, "f/t/other", nil); got != a {
			t.Fatalf("the second script ran in %d, want %d", got, a)
		}
		if got := runPID(t, rt, "f/t/pid", nil); got == a {
			t.Fatalf("the process that ran two scripts with Modules 1 got a new step")
		}

		// RSS: every process's resident set reaches 1 byte, so its
		// heartbeats trip it, and it leaves.
		rt = open(PoolOptions{Recycle: RecycleTriggers{RSS: 1}})
		a = runPID(t, rt, "f/t/pid", nil)
		for deadline := time.Now().Add(30 * time.Second); slices.ContainsFunc(rt.pool.Stats(), func(s harness.ProcessStats) bool { return s.PID == a }); time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("process %d did not leave on its heartbeats' rss: %+v", a, rt.pool.Stats())
			}
		}
		if got := runPID(t, rt, "f/t/pid", nil); got == a {
			t.Fatal("ran in the process that left")
		}
	})
}

// A step's own call to the host plugin does not count toward the cap:
// with Cap 1 and one process, a step that reads a resource that the plugin
// provides, and waits for it, gets it, rather than waiting for its own
// slot until the plugin call times out.
func TestPoolCapLeavesPluginCalls(t *testing.T) {
	bun := bunOrSkip(t)
	step, _ := json.Marshal("export async function main() {\n" +
		"  const r = await fetch(`${process.env.BASE_URL}/api/w/${process.env.WM_WORKSPACE}/resources/get_value_interpolated/u/t/r`," +
		" { headers: { Authorization: `Bearer ${process.env.WM_TOKEN}` } });\n" +
		"  if (!r.ok) throw new Error(\"read: \" + r.status);\n" +
		"  return await r.json();\n}\n")
	out, runtimeDir := buildBunBundle(t, bun, map[string]string{
		"wmill.yaml": "defaultTs: bun\n",
		"f/t/res.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"rawscript","language":"bun",` +
			`"content":` + string(step) + `,"input_transforms":{}}}]}}`,
	})
	plugin := filepath.Join(t.TempDir(), "plugin.ts")
	src := `export default { resource: (path: string) => path === "u/t/r" ? { type: "object", value: { x: 1 } } : undefined };`
	if err := os.WriteFile(plugin, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, Plugin: plugin, PluginTimeout: 5 * time.Second, Pool: PoolOptions{Cap: 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	res, err := rt.Run(context.Background(), "f/t/res", json.RawMessage(`{}`), RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusSuccess || string(res.Result) != `{"x":1}` {
		t.Fatalf("%s %s, latched %v; want the plugin's resource", res.Status, res.Result, res.Latched)
	}
}

// waitLag waits until the pool's process pid lags past d.
func waitLag(t *testing.T, rt *Runtime, pid int, d time.Duration) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		for _, s := range rt.pool.Stats() {
			if s.PID == pid && s.Lag > d {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("process %d does not lag: %+v", pid, rt.pool.Stats())
		}
	}
}

// A warm start: Open returns with Min processes started and their
// handshakes done. Without it, no process starts before the first step.
func TestWarmStart(t *testing.T) {
	bun := bunOrSkip(t)
	out, runtimeDir := poolBundle(t, bun)
	for _, tc := range []struct {
		pool PoolOptions
		want int
	}{{PoolOptions{Min: 2, Max: 2, WarmStart: true}, 2}, {PoolOptions{Min: 2, Max: 2}, 0}, {PoolOptions{WarmStart: true}, 1}} {
		rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, Pool: tc.pool})
		if err != nil {
			t.Fatal(err)
		}
		got := len(rt.pool.Stats())
		rt.Close()
		if got != tc.want {
			t.Fatalf("%+v: %d processes when Open returned, want %d", tc.pool, got, tc.want)
		}
	}
}
