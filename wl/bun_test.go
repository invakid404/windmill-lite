package wl

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/bundle"
	"github.com/invakid404/windmill-lite/internal/platform"
)

// bunOrSkip returns the Bun of WL_BUN, the pinned 1.3.10; without it the
// test is skipped, unless WL_REQUIRE_BUN is set, as CI sets it.
func bunOrSkip(t *testing.T) string {
	t.Helper()
	bun := os.Getenv("WL_BUN")
	if bun == "" {
		if os.Getenv("WL_REQUIRE_BUN") != "" {
			t.Fatal("WL_REQUIRE_BUN is set but WL_BUN is not")
		}
		t.Skip("WL_BUN is not set")
	}
	return bun
}

// setScript reads the variable v through the job API, sets it to "second"
// and returns what it read (WA-11); when it read "slow", it waits a minute
// first, after the update.
const setScript = `export async function main(v: string) {
  const base = ` + "`${process.env.BASE_URL}/api/w/${process.env.WM_WORKSPACE}/variables`" + `;
  const headers = { Authorization: ` + "`Bearer ${process.env.WM_TOKEN}`" + `, "Content-Type": "application/json" };
  const r = await fetch(` + "`${base}/get_value/${v}`" + `, { headers });
  if (!r.ok) throw new Error("read: " + r.status);
  const old = await r.json();
  const u = await fetch(` + "`${base}/update/${v}`" + `, { method: "POST", headers, body: JSON.stringify({ value: "second" }) });
  if (!u.ok) throw new Error("update: " + u.status);
  if (old === "slow") await new Promise((r) => setTimeout(r, 60000));
  return old;
}
`

// buildBundle builds a real bundle (bundle.Build) of a folder with the
// script f/t/set and the flow f/t/flow, which runs it on u/t/v, and the
// flow f/t/env, whose inline step returns its WL_TENANT variable, the flow
// f/t/writes, whose steps w1 and w2 return 1 and 2 with a 1 s sleep between
// them, the flow f/t/subs, which calls f/t/env twice, as steps one and two,
// the flow f/t/par, whose parallel branchall runs p1 and p2, and the
// flow f/t/cached, whose inline step returns a random number with a
// cache_ttl (FX-40: a path script's step would drop it).
func buildBundle(t *testing.T, bun string) (out, runtimeDir string) {
	t.Helper()
	repo, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	folder := t.TempDir()
	for p, s := range map[string]string{
		"wmill.yaml":              "defaultTs: bun\n",
		"f/t/set.ts":              setScript,
		"f/t/flow.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/t/set","input_transforms":{"v":{"type":"static","value":"u/t/v"}}}}]}}`,
		"f/t/cached.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","cache_ttl":600,"value":{"type":"rawscript","language":"bun",` +
			`"content":"export async function main() { return Math.random() }","input_transforms":{}}}]}}`,
		"f/t/env.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"rawscript","language":"bun",` +
			`"content":"export async function main() { return process.env.WL_TENANT ?? null }","input_transforms":{}}}]}}`,
		"f/t/writes.flow/flow.yaml": `{"summary":"t","value":{"modules":[` +
			`{"id":"w1","sleep":{"type":"static","value":1},"value":{"type":"rawscript","language":"bun","content":"export async function main() { return 1 }","input_transforms":{}}},` +
			`{"id":"w2","value":{"type":"rawscript","language":"bun","content":"export async function main() { return 2 }","input_transforms":{}}}]}}`,
		"f/t/subs.flow/flow.yaml": `{"summary":"t","value":{"modules":[` +
			`{"id":"one","value":{"type":"flow","path":"f/t/env","input_transforms":{}}},` +
			`{"id":"two","value":{"type":"flow","path":"f/t/env","input_transforms":{}}}]}}`,
		"f/t/par.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"all","value":{"type":"branchall","parallel":true,"branches":[` +
			`{"skip_failure":false,"modules":[{"id":"p1","value":{"type":"rawscript","language":"bun","content":"export async function main() { return 1 }","input_transforms":{}}}]},` +
			`{"skip_failure":false,"modules":[{"id":"p2","value":{"type":"rawscript","language":"bun","content":"export async function main() { return 2 }","input_transforms":{}}}]}]}}]}}`,
	} {
		f := filepath.Join(folder, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runtimeDir = filepath.Join(repo, "harnesses", "bun", "runtime")
	out = filepath.Join(t.TempDir(), "bundle")
	if _, err := bundle.Build(context.Background(), bundle.Config{
		Folder:         folder,
		EngineVersion:  Version(),
		Bun:            bun,
		Helper:         filepath.Join(repo, "harnesses", "bun", "build", "main.ts"),
		HarnessRuntime: runtimeDir,
		Out:            out,
		Work:           filepath.Join(t.TempDir(), "work"),
	}); err != nil {
		t.Fatal(err)
	}
	return out, runtimeDir
}

// A real bundle runs its step on the Bun harness in warm and in fresh
// mode. The step reads and updates a host's variable through the job API
// (WA-11): the run's trace records both platform calls, attributed to the
// step, and the next run reads the update, which the runtime keeps.
func TestRunOnBun(t *testing.T) {
	bun := bunOrSkip(t)
	out, runtimeDir := buildBundle(t, bun)
	for _, mode := range []string{ModuleStateWarm, ModuleStateFresh} {
		t.Run(mode, func(t *testing.T) {
			rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, ModuleState: mode, Variables: map[string]Variable{"u/t/v": {Value: "first"}}})
			if err != nil {
				t.Fatal(err)
			}
			defer rt.Close()
			for i, want := range []string{`"first"`, `"second"`} {
				res, err := rt.Run(context.Background(), "f/t/flow", json.RawMessage(`{}`), RunOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if res.Status != StatusSuccess || string(res.Result) != want {
					t.Fatalf("run %d: %s %s", i, res.Status, res.Result)
				}
				calls := res.Trace.PlatformCalls
				if len(calls) != 2 || calls[0].Route != "GET /w/{ws}/variables/get_value/{path}" || calls[1].Route != "POST /w/{ws}/variables/update/{path}" ||
					calls[0].Status != 200 || calls[1].Status != 200 || calls[0].Site.String() != "m:a" || calls[0].Job != res.Trace.Steps[0].Job {
					t.Fatalf("run %d: platform calls %+v", i, calls)
				}
			}
			// The script is a root too: it runs as a script job (WA-4).
			res, err := rt.Run(context.Background(), "f/t/set", json.RawMessage(`{"v":"u/t/v"}`), RunOptions{Kind: KindScript})
			if err != nil || res.Status != StatusSuccess || string(res.Result) != `"second"` || res.Trace != nil {
				t.Fatalf("script run: %+v %v", res, err)
			}
		})
	}
	// The harness runtime must be the one the bundle was built for.
	if _, err := Open(out, Options{Bun: bun}); err == nil {
		t.Error("opened a bundle with steps and no harness runtime")
	}
	if _, err := Open(out, Options{Bun: bun, HarnessRuntime: t.TempDir()}); err == nil {
		t.Error("opened a bundle with another harness runtime")
	}
}

// countingCache is a host's result cache that counts what it stores.
type countingCache struct {
	ResultCache
	mu   sync.Mutex
	puts int
}

func (c *countingCache) Put(ctx context.Context, key string, value []byte, expires time.Time) error {
	c.mu.Lock()
	c.puts++
	c.mu.Unlock()
	return c.ResultCache.Put(ctx, key, value, expires)
}

// A host's result cache (Options.Results, N4) keeps a cache_ttl step's
// result for the runs of every runtime that shares it (FX-40, D4): a second
// runtime of the bundle reads the first one's result, and stores nothing.
func TestRunSharedResultCache(t *testing.T) {
	bun := bunOrSkip(t)
	out, runtimeDir := buildBundle(t, bun)
	cache := &countingCache{ResultCache: platform.NewMemoryResults()}
	var results []string
	for range 2 {
		rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, Results: cache})
		if err != nil {
			t.Fatal(err)
		}
		res, err := rt.Run(context.Background(), "f/t/cached", nil, RunOptions{})
		rt.Close()
		if err != nil || res.Status != StatusSuccess {
			t.Fatalf("%+v %v", res, err)
		}
		results = append(results, string(res.Result))
	}
	if results[0] != results[1] || cache.puts != 1 {
		t.Fatalf("results %q, %d stores", results, cache.puts)
	}
}

// A run canceled while its step waits keeps the platform calls the step
// was answered before (ADR 0008 H9, FX-38): the read and the update of the
// variable, which the store already holds, in warm mode, where the step is
// abandoned, and in fresh mode, where its process is killed.
func TestRunCanceledKeepsCalls(t *testing.T) {
	bun := bunOrSkip(t)
	out, runtimeDir := buildBundle(t, bun)
	for _, mode := range []string{ModuleStateWarm, ModuleStateFresh} {
		t.Run(mode, func(t *testing.T) {
			rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, ModuleState: mode, Variables: map[string]Variable{"u/t/v": {Value: "slow"}}})
			if err != nil {
				t.Fatal(err)
			}
			defer rt.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan *Result, 1)
			go func() {
				res, err := rt.Run(ctx, "f/t/flow", nil, RunOptions{})
				if err != nil {
					t.Error(err)
				}
				done <- res
			}()
			deadline := time.After(10 * time.Second)
			for {
				if v, err := rt.services.VariableValue(context.Background(), "u/t/v"); err != nil {
					t.Fatal(err)
				} else if v == "second" {
					break
				}
				select {
				case <-deadline:
					t.Fatal("the step never updated the variable")
				case <-time.After(10 * time.Millisecond):
				}
			}
			// The update's answer reaches the step after the store changed.
			time.Sleep(100 * time.Millisecond)
			cancel()
			res := <-done
			if res == nil || res.Status != StatusCanceled {
				t.Fatalf("%+v", res)
			}
			calls := res.Trace.PlatformCalls
			if len(calls) != 2 || calls[0].Route != "GET /w/{ws}/variables/get_value/{path}" || calls[1].Route != "POST /w/{ws}/variables/update/{path}" ||
				calls[1].Status != 200 || calls[1].Site.String() != "m:a" || calls[1].Job == "" {
				t.Fatalf("platform calls %+v", calls)
			}
		})
	}
}

// H12 and H13 on the Bun harness, in warm and fresh mode: the variable an
// InvocationEnv hook sets is in the step's environment; a run's own
// variable is what its step reads through the job API, and its update
// changes the run's variable, not the runtime's.
func TestHooksOnBun(t *testing.T) {
	bun := bunOrSkip(t)
	out, runtimeDir := buildBundle(t, bun)
	for _, mode := range []string{ModuleStateWarm, ModuleStateFresh} {
		t.Run(mode, func(t *testing.T) {
			rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, ModuleState: mode, Variables: map[string]Variable{"u/t/v": {Value: "first"}}})
			if err != nil {
				t.Fatal(err)
			}
			defer rt.Close()
			h := &Hooks{InvocationEnv: func(_ context.Context, _ RunHandle, s Site, env map[string]string) error {
				env["WL_TENANT"] = "tenant-" + s.Module
				return nil
			}}
			res, err := rt.Run(context.Background(), "f/t/env", nil, RunOptions{Hooks: h})
			if err != nil || res.Status != StatusSuccess || string(res.Result) != `"tenant-a"` {
				t.Fatalf("H12: %+v %v", res, err)
			}
			if res, err := rt.Run(context.Background(), "f/t/env", nil, RunOptions{}); err != nil || string(res.Result) != `null` {
				t.Fatalf("a run without the hook: %+v %v", res, err)
			}
			p := &RunPlatform{Variables: map[string]Variable{"u/t/v": {Value: "run-own"}}}
			res, err = rt.Run(context.Background(), "f/t/flow", nil, RunOptions{Platform: p})
			if err != nil || res.Status != StatusSuccess || string(res.Result) != `"run-own"` {
				t.Fatalf("H13: %+v %v", res, err)
			}
			if v, err := rt.services.VariableValue(context.Background(), "u/t/v"); err != nil || v != "first" {
				t.Fatalf("the runtime's variable after the run's update: %q %v", v, err)
			}
		})
	}
}

// H9's traces carry the run's platform calls as the returned trace does
// (ADR 0008): the trace OnRunEnd gets and the one OnRunSettled gets hold
// the step's read and update, the calls as of the run's end, in warm and
// fresh mode, and so do they for a run canceled while its step waits,
// which settles once its abandoned invocation does (fresh mode, where the
// cancellation ends the step's process).
func TestRunEndKeepsCalls(t *testing.T) {
	bun := bunOrSkip(t)
	out, runtimeDir := buildBundle(t, bun)
	check := func(t *testing.T, mode, first string, cancelAfterUpdate bool) {
		rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir, ModuleState: mode, Variables: map[string]Variable{"u/t/v": {Value: first}}})
		if err != nil {
			t.Fatal(err)
		}
		defer rt.Close()
		ended := make(chan []PlatformCall, 1)
		settled := make(chan []PlatformCall, 1)
		h := &Hooks{
			OnRunEnd: func(_ context.Context, tr *Trace) []Warning {
				ended <- tr.PlatformCalls
				return nil
			},
			OnRunSettled: func(tr *Trace) { settled <- tr.PlatformCalls },
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan *Result, 1)
		go func() {
			res, err := rt.Run(ctx, "f/t/flow", nil, RunOptions{Hooks: h})
			if err != nil {
				t.Error(err)
			}
			done <- res
		}()
		if cancelAfterUpdate {
			deadline := time.After(10 * time.Second)
			for {
				if v, _ := rt.services.VariableValue(context.Background(), "u/t/v"); v == "second" {
					break
				}
				select {
				case <-deadline:
					t.Fatal("the step never updated the variable")
				case <-time.After(10 * time.Millisecond):
				}
			}
			time.Sleep(100 * time.Millisecond)
			cancel()
		}
		res := <-done
		if res == nil {
			t.Fatal("no result")
		}
		var settledCalls []PlatformCall
		select {
		case settledCalls = <-settled:
		case <-time.After(15 * time.Second):
			t.Fatal("the run never settled")
		}
		endCalls := <-ended
		routes := func(cs []PlatformCall) string {
			var b strings.Builder
			for _, c := range cs {
				b.WriteString(c.Route + " " + strconv.Itoa(c.Status) + ";")
			}
			return b.String()
		}
		want := "GET /w/{ws}/variables/get_value/{path} 200;POST /w/{ws}/variables/update/{path} 200;"
		if got := routes(res.Trace.PlatformCalls); got != want || routes(endCalls) != want || routes(settledCalls) != want {
			t.Fatalf("%s: returned %q, OnRunEnd %q, OnRunSettled %q", res.Status, got, routes(endCalls), routes(settledCalls))
		}
	}
	for _, mode := range []string{ModuleStateWarm, ModuleStateFresh} {
		t.Run(mode, func(t *testing.T) { check(t, mode, "first", false) })
	}
	t.Run("fresh canceled", func(t *testing.T) { check(t, ModuleStateFresh, "slow", true) })
}

// H3 and H4 on the Bun harness (ADR 0008): before the first write-capable
// step launches, BeforeFirstWrite is asked once; a veto latches the run
// and launches nothing, while an approval lets every write-capable step
// launch and the run report MayHaveMutated. Parallel write-capable launches
// wait for the one verdict, and a veto stops them all. A write-capable
// subflow call is asked about before its child flow starts. The approval
// is the commit point, which a later H12 veto does not undo. The pre-write
// deadline applies only until the first write: after it, a sleep longer
// than the deadline is waited out; without a write, the deadline ends the
// run canceled.
func TestWritesAndDeadlines(t *testing.T) {
	bun := bunOrSkip(t)
	out, runtimeDir := buildBundle(t, bun)
	rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	w1, w2 := Selector{Script: "f/t/writes/w1"}, Selector{Script: "f/t/writes/w2"}
	var mu sync.Mutex
	var asked []string
	ask := func(verdict error) func(context.Context, RunHandle, Site) error {
		return func(_ context.Context, _ RunHandle, s Site) error {
			mu.Lock()
			asked = append(asked, s.Script)
			mu.Unlock()
			time.Sleep(50 * time.Millisecond)
			return verdict
		}
	}
	run := func(flow string, opts RunOptions) *Result {
		t.Helper()
		mu.Lock()
		asked = nil
		mu.Unlock()
		res, err := rt.Run(context.Background(), flow, nil, opts)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	launched := func(res *Result, site string) bool {
		rec, ok := res.Trace.Step(mustSite(t, site))
		return ok && rec.Job != ""
	}

	veto := errors.New("host: no writes")
	res := run("f/t/writes", RunOptions{Hooks: &Hooks{WriteCapable: []Selector{w1, w2}, BeforeFirstWrite: ask(veto)}})
	if res.Status != StatusLatched || !errors.Is(res.Latched, veto) || res.MayHaveMutated || launched(res, "m:w1") || len(asked) != 1 {
		t.Fatalf("a veto: %s %v, mutated %v, asked %q", res.Status, res.Latched, res.MayHaveMutated, asked)
	}
	res = run("f/t/writes", RunOptions{Hooks: &Hooks{WriteCapable: []Selector{w1, w2}, BeforeFirstWrite: ask(nil)}})
	if res.Status != StatusSuccess || !res.MayHaveMutated || !launched(res, "m:w1") || !launched(res, "m:w2") || len(asked) != 1 || asked[0] != "f/t/writes/w1" {
		t.Fatalf("an approval: %s, mutated %v, asked %q", res.Status, res.MayHaveMutated, asked)
	}
	res = run("f/t/writes", RunOptions{})
	if res.Status != StatusSuccess || res.MayHaveMutated {
		t.Fatalf("no write-capable site: %s, mutated %v", res.Status, res.MayHaveMutated)
	}

	sels := []Selector{{Script: "f/t/par/p1"}, {Script: "f/t/par/p2"}}
	res = run("f/t/par", RunOptions{Hooks: &Hooks{WriteCapable: sels, BeforeFirstWrite: ask(veto)}})
	if res.Status != StatusLatched || launched(res, "m:all/b:0/m:p1") || launched(res, "m:all/b:1/m:p2") || len(asked) != 1 {
		t.Fatalf("parallel writes vetoed: %s, asked %q", res.Status, asked)
	}

	// A write-capable subflow call is asked about before its child flow
	// starts: a veto stops every step below it, an approval lets them run.
	// Of two calls of one subflow, only the marked one asks.
	one, two := module("f/t/subs", "one"), module("f/t/subs", "two")
	res = run("f/t/subs", RunOptions{Hooks: &Hooks{WriteCapable: []Selector{one}, BeforeFirstWrite: ask(veto)}})
	if res.Status != StatusLatched || res.MayHaveMutated || launched(res, "m:one/m:a") || launched(res, "m:two/m:a") || len(asked) != 1 {
		t.Fatalf("a vetoed subflow call: %s, mutated %v, asked %q", res.Status, res.MayHaveMutated, asked)
	}
	res = run("f/t/subs", RunOptions{Hooks: &Hooks{WriteCapable: []Selector{one, two}, BeforeFirstWrite: ask(nil)}})
	if res.Status != StatusSuccess || !res.MayHaveMutated || !launched(res, "m:one/m:a") || !launched(res, "m:two/m:a") || len(asked) != 1 {
		t.Fatalf("an approved subflow call: %s, mutated %v, asked %q", res.Status, res.MayHaveMutated, asked)
	}
	res = run("f/t/subs", RunOptions{Hooks: &Hooks{WriteCapable: []Selector{two}, BeforeFirstWrite: ask(veto)}})
	if res.Status != StatusLatched || !launched(res, "m:one/m:a") || launched(res, "m:two/m:a") || len(asked) != 1 {
		t.Fatalf("only the second call marked: %s, asked %q", res.Status, asked)
	}

	// The approval is the first write's commit point: an H12 veto of the
	// approved step's invocation still leaves the run counted as written.
	envVeto := errors.New("host: no environment")
	res = run("f/t/writes", RunOptions{Hooks: &Hooks{WriteCapable: []Selector{w1}, BeforeFirstWrite: ask(nil),
		InvocationEnv: func(context.Context, RunHandle, Site, map[string]string) error { return envVeto }}})
	if res.Status != StatusLatched || !errors.Is(res.Latched, envVeto) || !res.MayHaveMutated || launched(res, "m:w1") {
		t.Fatalf("an H12 veto after the approval: %s %v, mutated %v", res.Status, res.Latched, res.MayHaveMutated)
	}

	deadline := func() time.Time { return time.Now().Add(400 * time.Millisecond) }
	res = run("f/t/writes", RunOptions{PreWriteDeadline: deadline(), Hooks: &Hooks{WriteCapable: []Selector{w1}}})
	if res.Status != StatusSuccess || string(res.Result) != `2` {
		t.Fatalf("a deadline disarmed by the first write: %s %s", res.Status, res.Result)
	}
	res = run("f/t/writes", RunOptions{PreWriteDeadline: deadline()})
	if res.Status != StatusCanceled || launched(res, "m:w2") {
		t.Fatalf("a deadline with no write: %s %s", res.Status, res.Result)
	}
}
