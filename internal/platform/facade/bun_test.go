package facade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/harness"
	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/internal/platform"
)

// The attribution test of ADR 0007, milestone PF1's exit gate, on the
// pinned Bun 1.3.10 with the stock windmill-client@1.767.0:
//
//   - two runs at once, in one warm process, with the client first
//     imported during the first run's module load: every call of each run
//     is attributed to its own invocation on the intercepted path, although
//     both send the token the client captured at import, the first run's;
//   - a node:http call from a warm step gets "connection refused" and
//     reaches no provider: nothing records it;
//   - in fresh mode each process's TCP calls carry its own token, which
//     attributes them;
//   - a call replaying an ended invocation's token gets the 401.
//
// It needs WL_BUN and WL_WINDMILL_CLIENT, as the harness's Q-WA-4 test
// does; CI sets both and WL_REQUIRE_BUN.
func TestAttribution(t *testing.T) {
	r := newBunRig(t)

	t.Run("warm: two runs at once", func(t *testing.T) {
		f := newFacade(t, Config{})
		p := r.spawn(t, f, engine.ModuleStateWarm)
		x := f.Executor(p)
		ids := []platform.Identity{r.identity("f/t/attr", "a"), r.identity("f/t/attr", "a")}
		type res struct {
			out engine.StepOutcome
			err error
		}
		results := make([]res, 2)
		var wg sync.WaitGroup
		// The first run's invocation loads the module, which imports the
		// client: it captures the first run's token. The second starts
		// once the first is in flight; the module's barrier then holds
		// both until both have made their first call.
		started := make(chan struct{})
		for i := range ids {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if i == 1 {
					<-started
				}
				out, err := x.Execute(r.ctx(t), r.invocation(ids[i], "attr/client", `{"runs":2}`, engine.ModuleStateWarm))
				results[i] = res{out, err}
			}()
			if i == 0 {
				r.waitCalls(t, f, ids[0].JobID, 1)
				close(started)
			}
		}
		wg.Wait()
		var got [2]clientResult
		for i, res := range results {
			if res.err != nil || res.out.Kind != engine.OutcomeOK {
				t.Fatalf("run %d: %+v %s %v\n%s", i+1, res.out, res.out.Body.Producer(), res.err, r.diag)
			}
			if err := json.Unmarshal(res.out.Body.Producer(), &got[i]); err != nil {
				t.Fatal(err)
			}
			id := ids[i]
			// The step saw its own identity, and the answers to its calls
			// name its own run's root.
			if got[i].Job != id.JobID || got[i].Token != id.Token || got[i].First != id.RootJobID || got[i].Second != id.RootJobID || got[i].Raw != id.RootJobID {
				t.Errorf("run %d: %+v, want job %s, token %s, root %s", i+1, got[i], id.JobID, id.Token, id.RootJobID)
			}
			// The client was imported once, in the first run's module
			// load, so both runs send the first run's token.
			if got[i].Imported != ids[0].Token {
				t.Errorf("run %d: the client captured %q, want the first run's token %q", i+1, got[i].Imported, ids[0].Token)
			}
		}
		// Every call is recorded against the invocation that made it: the
		// job id in its path, read from the step's own environment, is
		// that invocation's, while its token is the first run's.
		for i, id := range ids {
			calls := r.recorded(id.JobID)
			if len(calls) != 3 {
				t.Fatalf("run %d: %d calls recorded, want 3: %+v", i+1, len(calls), calls)
			}
			for _, c := range calls {
				if c.Inv != id.JobID || !strings.HasSuffix(c.Path, "/jobs_u/get_root_job_id/"+id.JobID) || c.Status != 200 {
					t.Errorf("run %d: call %+v", i+1, c)
				}
			}
			if auth := calls[0].Headers["authorization"]; auth != "Bearer "+ids[0].Token {
				t.Errorf("run %d: the client's call carried %q, want the first run's token", i+1, auth)
			}
		}
		if s := f.Stray(); len(s) != 0 {
			t.Errorf("stray calls: %+v", s)
		}
		t.Logf("two warm runs: 6 of 6 calls attributed to their own invocations; both clients sent run 1's token")
	})

	t.Run("warm: node:http is refused and recorded nowhere", func(t *testing.T) {
		f := newFacade(t, Config{})
		p := r.spawn(t, f, engine.ModuleStateWarm)
		id := r.identity("f/t/attr", "b")
		out, err := f.Executor(p).Execute(r.ctx(t), r.invocation(id, "attr/http", `{}`, engine.ModuleStateWarm))
		if err != nil || string(out.Body.Producer()) != `{"error":"ECONNREFUSED"}` {
			t.Fatalf("%+v %s %v\n%s", out, out.Body.Producer(), err, r.diag)
		}
		if calls := r.recorded(id.JobID); len(calls) != 0 {
			t.Errorf("calls recorded: %+v", calls)
		}
		if s := f.Stray(); len(s) != 0 {
			t.Errorf("stray calls: %+v", s)
		}
	})

	t.Run("fresh: each process's TCP calls carry its own token; a replayed token gets the 401", func(t *testing.T) {
		f := newFacade(t, Config{})
		x := f.Executor(r.pool(t, f, engine.ModuleStateFresh))
		ids := []platform.Identity{r.identity("f/t/attr", "a"), r.identity("f/t/attr", "a")}
		var wg sync.WaitGroup
		outs := make([]engine.StepOutcome, 2)
		for i := range ids {
			wg.Add(1)
			go func() {
				defer wg.Done()
				out, err := x.Execute(r.ctx(t), r.invocation(ids[i], "attr/client", `{"runs":1}`, engine.ModuleStateFresh))
				if err != nil {
					t.Errorf("run %d: %v", i+1, err)
				}
				outs[i] = out
			}()
		}
		wg.Wait()
		for i, id := range ids {
			var got clientResult
			if outs[i].Kind != engine.OutcomeOK || json.Unmarshal(outs[i].Body.Producer(), &got) != nil {
				t.Fatalf("run %d: %+v %s\n%s", i+1, outs[i], outs[i].Body.Producer(), r.diag)
			}
			if got.Imported != id.Token || got.First != id.RootJobID || got.Raw != id.RootJobID {
				t.Errorf("run %d: %+v", i+1, got)
			}
			calls := r.recorded(id.JobID)
			if len(calls) != 3 {
				t.Fatalf("run %d: %d calls, want 3: %+v", i+1, len(calls), calls)
			}
			for _, c := range calls {
				if c.Inv != id.JobID || c.Headers["authorization"] != "Bearer "+id.Token || c.Status != 200 {
					t.Errorf("run %d: call %+v", i+1, c)
				}
			}
		}
		// The first invocation has ended: a call that replays its token
		// is refused, and recorded as a stray, not against any invocation.
		code, body := get(t, f.FreshOrigin()+"/api/w/"+r.host.Workspace+"/jobs_u/get_root_job_id/"+ids[0].JobID, ids[0].Token)
		if code != 401 || body != "Not authorized: unknown windmill-lite invocation token" {
			t.Errorf("replayed token: %d %q", code, body)
		}
		if s := f.Stray(); len(s) != 1 || s[0].Inv != "" || s[0].Status != 401 {
			t.Errorf("strays: %+v", s)
		}
		// node:http works in fresh mode, where the port listens, and
		// carries the process's token.
		id := r.identity("f/t/attr", "b")
		out, err := x.Execute(r.ctx(t), r.invocation(id, "attr/http", `{}`, engine.ModuleStateFresh))
		if err != nil || string(out.Body.Producer()) != fmt.Sprintf(`{"status":200,"body":"\"%s\""}`, id.RootJobID) {
			t.Errorf("fresh node:http: %+v %s %v", out, out.Body.Producer(), err)
		}
	})
}

// WA-30/a on the real client: the same step through the intercepted fetch
// (warm) and through loopback TCP (fresh) gives the same result and the
// same recorded calls (method, path, query, body), once job ids are
// normalised.
func TestTransportsAgree(t *testing.T) {
	r := newBunRig(t)
	results := map[engine.ModuleState]string{}
	calls := map[engine.ModuleState][]string{}
	for _, mode := range []engine.ModuleState{engine.ModuleStateWarm, engine.ModuleStateFresh} {
		f := newFacade(t, Config{Policy: PolicyObserve})
		x := f.Executor(r.pool(t, f, mode))
		id := r.identity("f/t/agree", "a")
		out, err := x.Execute(r.ctx(t), r.invocation(id, "attr/agree", `{}`, mode))
		if err != nil || out.Kind != engine.OutcomeOK {
			t.Fatalf("%s: %+v %s %v\n%s", mode, out, out.Body.Producer(), err, r.diag)
		}
		norm := strings.NewReplacer(id.JobID, "<job>", id.RootJobID, "<root>")
		results[mode] = norm.Replace(string(out.Body.Producer()))
		for _, c := range r.recorded(id.JobID) {
			calls[mode] = append(calls[mode], norm.Replace(fmt.Sprintf("%s %s ?%s %q -> %d", c.Method, c.Path, c.Query, c.Body, c.Status)))
		}
	}
	if results["warm"] != results["fresh"] {
		t.Errorf("results differ:\nwarm:  %s\nfresh: %s", results["warm"], results["fresh"])
	}
	if !slices.Equal(calls["warm"], calls["fresh"]) || len(calls["warm"]) != 4 {
		t.Errorf("recorded calls differ:\nwarm:  %q\nfresh: %q", calls["warm"], calls["fresh"])
	}
	t.Logf("WA-30/a: result %s; calls %q", results["warm"], calls["warm"])
}

// The 501 policy on the stock client (WA-41/a–b, WA-V5): runScriptByPathAsync
// resolves to the 501's body as a job id (WA-V5/a), setFlowUserState
// swallows its 501 (WA-V5/b), the strict setFlowUserState rejects with it
// (WA-V5/c), and getIdToken and JobService.getJob reject with an ApiError
// whose status is 501, statusText the facade's, message the generator's
// "Not Implemented" and body the 501's (WA-41/a). In fail mode the step
// fails with UnsupportedPlatformCall though the script returned normally,
// in both transports; in observe mode the stock behaviour stands.
func TestUnsupportedPolicyOnTheClient(t *testing.T) {
	r := newBunRig(t)
	const observed = `{"async":"Not implemented in windmill-lite: POST /w/{ws}/jobs/run/p/{path}","state":null,"strict":"Error setting flow user state at k: Not implemented in windmill-lite: POST /w/{ws}/jobs/flow/user_states/{id}/{key}","token":{"name":"ApiError","status":501,"statusText":"Not Implemented","message":"Not Implemented","body":"Not implemented in windmill-lite: POST /w/{ws}/oidc/token/{audience}"},"job":{"name":"ApiError","status":501,"statusText":"Not Implemented","message":"Not Implemented","body":"Not implemented in windmill-lite: GET /w/{ws}/jobs_u/get/{id}"}}`
	const failed = `{"name":"UnsupportedPlatformCall","message":"Not implemented in windmill-lite: POST /w/{ws}/jobs/run/p/{path}","calls":["POST /w/{ws}/jobs/run/p/{path}","POST /w/{ws}/jobs/flow/user_states/{id}/{key}","POST /w/{ws}/jobs/flow/user_states/{id}/{key}","POST /w/{ws}/oidc/token/{audience}","GET /w/{ws}/jobs_u/get/{id}"]}`
	for _, mode := range []engine.ModuleState{engine.ModuleStateWarm, engine.ModuleStateFresh} {
		for _, c := range []struct {
			policy Policy
			kind   engine.OutcomeKind
			want   string
		}{{PolicyObserve, engine.OutcomeOK, observed}, {PolicyFail, engine.OutcomeError, failed}} {
			t.Run(string(mode)+" "+string(c.policy), func(t *testing.T) {
				f := newFacade(t, Config{Policy: c.policy})
				x := f.Executor(r.pool(t, f, mode))
				id := r.identity("f/t/policy", "a")
				out, err := x.Execute(r.ctx(t), r.invocation(id, "attr/unsupported", `{}`, mode))
				if err != nil || out.Kind != c.kind || string(out.Body.Producer()) != c.want {
					t.Fatalf("%+v\n got %s\nwant %s\n%v\n%s", out, out.Body.Producer(), c.want, err, r.diag)
				}
			})
		}
	}
}

// On the intercepted path the policy sees a call the step made and never
// awaited: its platform frame arrives ahead of the step's result, and the
// facade accepts it on the reader, so the outcome is replaced every time.
func TestUnawaitedUnsupportedCall(t *testing.T) {
	r := newBunRig(t)
	f := newFacade(t, Config{})
	x := f.Executor(r.spawn(t, f, engine.ModuleStateWarm))
	for i := range 50 {
		id := r.identity("f/t/unawaited", "a")
		out, err := x.Execute(r.ctx(t), r.invocation(id, "attr/unawaited", `{}`, engine.ModuleStateWarm))
		if err != nil || out.Kind != engine.OutcomeError || !strings.Contains(string(out.Body.Producer()), `"UnsupportedPlatformCall"`) {
			t.Fatalf("run %d: %+v %s %v\n%s", i+1, out, out.Body.Producer(), err, r.diag)
		}
	}
}

// clientResult is what attr/client returns.
type clientResult struct {
	Job, Token, Imported, First, Second, Raw string
}

// bunRig is the bundle and configuration the Bun tests share.
type bunRig struct {
	bun, main, bundle, digest string
	host                      platform.Host
	diag                      *syncBuffer
	mu                        sync.Mutex
	calls                     map[string][]Record
}

func newBunRig(t *testing.T) *bunRig {
	t.Helper()
	bun := os.Getenv("WL_BUN")
	client := os.Getenv("WL_WINDMILL_CLIENT")
	if bun == "" || client == "" {
		if os.Getenv("WL_REQUIRE_BUN") != "" {
			t.Fatal("WL_REQUIRE_BUN is set but WL_BUN or WL_WINDMILL_CLIENT is not; WL_WINDMILL_CLIENT names a directory whose node_modules holds windmill-client@1.767.0")
		}
		t.Skip("WL_BUN and WL_WINDMILL_CLIENT name the pinned Bun and the stock client")
	}
	pkg, err := os.ReadFile(filepath.Join(client, "node_modules", "windmill-client", "package.json"))
	if err != nil || !strings.Contains(string(pkg), `"version": "1.767.0"`) {
		t.Fatalf("WL_WINDMILL_CLIENT must hold windmill-client 1.767.0: %v", err)
	}
	main, err := filepath.Abs(filepath.Join("..", "..", "..", "harnesses", "bun", "runtime", "main.ts"))
	if err != nil {
		t.Fatal(err)
	}
	r := &bunRig{bun: bun, main: main, bundle: filepath.Join(t.TempDir(), "bundle"), host: platform.Host{Workspace: "wl_pf1"}, diag: &syncBuffer{}, calls: map[string][]Record{}}
	write := func(rel, text string) {
		p := filepath.Join(r.bundle, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	manifest := `{"format":"windmill-lite-pf1-test","note":"the attribution test's bundle (milestone PF1)","host_modules":[]}` + "\n"
	write("manifest.json", manifest)
	sum := sha256.Sum256([]byte(manifest))
	r.digest = "sha256:" + hex.EncodeToString(sum[:])
	if err := os.MkdirAll(filepath.Join(r.bundle, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(client, "node_modules", "windmill-client"), filepath.Join(r.bundle, "node_modules", "windmill-client")); err != nil {
		t.Fatal(err)
	}
	// The steps are not bundled: the client is imported from node_modules
	// when a step module is first evaluated, in its first invocation's
	// context, so it captures that invocation's BASE_URL and WM_TOKEN.
	for name, src := range bunSteps {
		write("scripts/bun/attr/"+name+".mjs", src)
		params := `[]`
		if name == "client" {
			params = `[{"name":"runs","date":false}]`
		}
		write("scripts/bun/attr/"+name+".meta.json", `{"signatures":{"main":{"params":`+params+`}}}`)
	}
	return r
}

var bunSteps = map[string]string{
	// Three getRootJobId calls of the stock client's, the second after a
	// barrier that the module's state shares between the invocations of a
	// warm process, so that runs of it are in flight at once; and a raw
	// fetch to BASE_URL.
	"client": `import * as wmill from "windmill-client";

const imported = process.env.WM_TOKEN;
let arrived = 0;
let open;
const barrier = new Promise((resolve) => (open = resolve));

export async function main(runs) {
  const first = await wmill.getRootJobId();
  if (++arrived >= runs) open();
  await barrier;
  const second = await wmill.getRootJobId();
  const e = process.env;
  const raw = await (await fetch(e.BASE_URL + "/api/w/" + e.WM_WORKSPACE + "/jobs_u/get_root_job_id/" + e.WM_JOB_ID, { headers: { authorization: "Bearer " + e.WM_TOKEN } })).json();
  return { job: e.WM_JOB_ID, token: e.WM_TOKEN, imported, first, second, raw };
}
`,
	// node:http to the facade's origin.
	"http": `import http from "node:http";

export async function main() {
  const e = process.env;
  return await new Promise((resolve) => {
    const req = http.get(e.BASE_URL + "/api/w/" + e.WM_WORKSPACE + "/jobs_u/get_root_job_id/" + e.WM_JOB_ID, { headers: { authorization: "Bearer " + e.WM_TOKEN } }, (res) => {
      let body = "";
      res.on("data", (c) => (body += c));
      res.on("end", () => resolve({ status: res.statusCode, body }));
    });
    req.on("error", (err) => resolve({ error: err.code }));
  });
}
`,
	// A served call, a raw POST with a query and a body, and an
	// unsupported one, whose answers the step returns (WA-30/a). The raw
	// POST carries the token, as a script calling OpenAPI.BASE itself
	// would (WA-9): over TCP a call without it names no invocation.
	"agree": `import * as wmill from "windmill-client";

export async function main() {
  const e = process.env;
  const root = await wmill.getRootJobId();
  const res = await fetch(e.BASE_URL + "/api/w/" + e.WM_WORKSPACE + "/jobs/run/p/u/t/x?tag=t1", { method: "POST", headers: { "content-type": "application/json", authorization: "Bearer " + e.WM_TOKEN }, body: JSON.stringify({ a: [1, "é"] }) });
  const posted = { status: res.status, statusText: res.statusText, type: res.headers.get("content-type"), body: await res.text() };
  const again = await wmill.getRootJobId(e.WM_JOB_ID);
  let thrown;
  try {
    await wmill.getVariable("u/t/v");
  } catch (err) {
    thrown = String(err.message ?? err);
  }
  return { root, posted, again, thrown };
}
`,
	// An unsupported call the step never awaits, with a body large enough
	// to travel in part frames, and a result right after it.
	"unawaited": `export function main() {
  const e = process.env;
  void fetch(e.BASE_URL + "/api/w/" + e.WM_WORKSPACE + "/jobs/run/p/u/t/x", { method: "POST", body: "x".repeat(3 << 20) }).catch(() => {});
  return 1;
}
`,
	// WA-V5/a–c, getIdToken and WA-41/a's JobService.getJob, each answered
	// 501.
	"unsupported": `import * as wmill from "windmill-client";

export async function main() {
  const async = await wmill.runScriptByPathAsync("u/t/x");
  const state = (await wmill.setFlowUserState("k", 1)) ?? null;
  let strict;
  try {
    await wmill.setFlowUserState("k", 1, true);
  } catch (err) {
    strict = err.message;
  }
  const apiError = (err) => ({ name: err.name, status: err.status, statusText: err.statusText, message: err.message, body: err.body });
  let token;
  try {
    await wmill.getIdToken("aud");
  } catch (err) {
    token = apiError(err);
  }
  let job;
  try {
    await wmill.JobService.getJob({ workspace: process.env.WM_WORKSPACE, id: "j1" });
  } catch (err) {
    job = apiError(err);
  }
  return { async, state, strict, token, job };
}
`,
}

func (r *bunRig) processConfig(f *Facade, mode engine.ModuleState) harness.ProcessConfig {
	path, args := harness.BunCommand(r.bun, r.main, r.bundle)
	origin := f.WarmOrigin()
	if mode == engine.ModuleStateFresh {
		origin = f.FreshOrigin()
	}
	return harness.ProcessConfig{
		Path: path, Args: args, Kind: "bun", Runtime: harness.PinnedBun, Bundle: r.digest, ModuleState: mode,
		EngineVersion: "pf1-test", Diagnostics: r.diag, Facade: origin, Platform: f.Platform(),
	}
}

func (r *bunRig) spawn(t *testing.T, f *Facade, mode engine.ModuleState) *harness.Process {
	t.Helper()
	r.observe(f)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	p, err := harness.Spawn(ctx, r.processConfig(f, mode))
	if err != nil {
		t.Fatalf("Spawn: %v\n%s", err, r.diag)
	}
	t.Cleanup(func() {
		p.Kill()
		<-p.Done()
	})
	return p
}

func (r *bunRig) pool(t *testing.T, f *Facade, mode engine.ModuleState) *harness.Pool {
	t.Helper()
	r.observe(f)
	p, err := harness.NewPool(harness.PoolConfig{Key: harness.Key{Class: "bun", Runtime: harness.PinnedBun, Deps: "pf1-test", ModuleState: mode}, Process: r.processConfig(f, mode)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		p.Close(ctx)
	})
	return p
}

// observe keeps the records the facade releases, by invocation.
func (r *bunRig) observe(f *Facade) {
	f.recorded = func(inv string, calls []Record) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.calls[inv] = calls
	}
}

func (r *bunRig) recorded(inv string) []Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[inv]
}

// waitCalls waits until a registered invocation has made n calls.
func (r *bunRig) waitCalls(t *testing.T, f *Facade, inv string, n int) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		e := f.invs[inv]
		got := 0
		if e != nil {
			got = len(e.calls)
		}
		f.mu.Unlock()
		if got >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("invocation %s made fewer than %d calls\n%s", inv, n, r.diag)
}

// identity is a step's job identity directly in a flow run of its own, as
// the engine gives it.
func (r *bunRig) identity(flow, step string) platform.Identity {
	run := newID()
	return platform.Identity{JobID: newID(), FlowJobID: run, RootFlowJobID: run, RootJobID: run, FlowPath: flow, StepID: step, JobPath: flow + "/" + step, Token: platform.NewToken()}
}

func (r *bunRig) invocation(id platform.Identity, script, args string, mode engine.ModuleState) engine.Invocation {
	return engine.Invocation{
		ID: id.JobID, Run: id.RootJobID, Script: script, Entry: "main", Args: jsonval.FromProducer([]byte(args)),
		Env: id.Env(r.host), Boundary: engine.BoundaryBun, ModuleState: mode, Report: engine.NopReporter{},
	}
}

func (r *bunRig) ctx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

var idSeq struct {
	sync.Mutex
	n int
}

// newID returns a distinct id shaped like a UUID, so a normaliser that
// replaces ids replaces nothing else.
func newID() string {
	idSeq.Lock()
	defer idSeq.Unlock()
	idSeq.n++
	return fmt.Sprintf("0000%04d-0000-4000-8000-%012d", idSeq.n, time.Now().UnixNano()%1e12)
}

type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
