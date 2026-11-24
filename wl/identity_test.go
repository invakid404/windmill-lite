package wl

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"sync"
	"testing"
)

// identityFiles is a synthetic bundle for the job identity tests:
//
//   - f/i/env, a script and a flow that runs it, returns the job
//     variables of the identity;
//   - f/i/check returns the statuses of a variable read through the job
//     API in the step's own workspace and in windmill-lite;
//   - f/i/probe, a flow, runs f/i/first, which returns its job id, then
//     f/i/probe, whose step dispatches f/i/child, which returns its
//     workspace, sets its run's flow user
//     state, publishes its job ids in the variable u/i/<me>, waits for the
//     other run's in u/i/<other>, looks up both runs' jobs through its own
//     workspace, then publishes u/i/d<me> and returns once the other run
//     has published u/i/d<other>, so that both runs live through both
//     runs' observations;
//   - f/i/root returns the root lookup of a job id through the step's own
//     workspace;
//   - f/i/spawn dispatches the target of its route (such as p/f/s/write or
//     f/f/i/env) with its body, and returns the dispatched run's result;
//   - f/i/stateread, a flow, returns flow_user_state("k") through f/i/echo,
//     and f/i/flowpath returns its WM_FLOW_PATH;
//   - f/i/probe2 is f/i/probe at another path;
//   - f/i/hooked returns its WM_WORKSPACE and the answers of its root lookup
//     and of a variable read, in that workspace and in the one it is given;
//   - f/i/overlap publishes that it runs in u/i/r<me>, waits for the other
//     run's u/i/r<other>, then returns its root lookup;
//   - f/i/nonce returns its workspace and a fresh UUID, cached by a flow
//     (f/i/cached_flow); f/i/cached_step's inline step does so with a step
//     cache_ttl, and f/i/nonce_script as a root script with a cache_ttl;
//   - and the state writes and reads of storeFiles.
var identityFiles = func() map[string]string {
	files := map[string]string{
		"f/i/env.ts": `export async function main() {
  const e = process.env;
  return { workspace: e.WM_WORKSPACE, root_workspace: e.WM_ROOT_WORKSPACE, username: e.WM_USERNAME, email: e.WM_EMAIL,
    end_user_email: e.WM_END_USER_EMAIL, permissioned_as: e.WM_PERMISSIONED_AS, base_url: e.WM_BASE_URL, worker_group: e.WM_WORKER_GROUP };
}
`,
		"f/i/env.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/i/env","input_transforms":{}}}]}}`,
		"f/i/check.ts": `export async function main() {
  const read = async (ws: string) => (await fetch(` + "`${process.env.BASE_URL}/api/w/${ws}/variables/get_value/u/i/a`" + `,
    { headers: { Authorization: ` + "`Bearer ${process.env.WM_TOKEN}`" + ` } })).status;
  return { own: await read(process.env.WM_WORKSPACE!), placeholder: await read("windmill-lite") };
}
`,
		"f/i/check.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/i/check","input_transforms":{}}}]}}`,
		"f/i/first.ts": `export async function main() { return process.env.WM_JOB_ID; }
`,
		"f/i/child.ts": `export async function main() { return process.env.WM_WORKSPACE; }
`,
		"f/i/spawn.ts": `export async function main(route: string, body: unknown) {
  const api = ` + "`${process.env.BASE_URL}/api/w/${process.env.WM_WORKSPACE}`" + `;
  const headers = { Authorization: ` + "`Bearer ${process.env.WM_TOKEN}`" + `, "Content-Type": "application/json" };
  const d = await fetch(api + "/jobs/run/" + route, { method: "POST", headers, body: JSON.stringify(body) });
  const id = await d.text();
  if (d.status !== 201) throw new Error("dispatch: " + d.status + " " + id);
  for (let i = 0; i < 500; i++) {
    const r = await (await fetch(api + "/jobs_u/completed/get_result_maybe/" + id, { headers })).json();
    if (r.completed) return r.result;
    await Bun.sleep(10);
  }
  throw new Error("the dispatched run never completed");
}
`,
		"f/i/spawn.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/i/spawn",` +
			`"input_transforms":{"route":{"type":"javascript","expr":"flow_input.route"},"body":{"type":"javascript","expr":"flow_input.body"}}}}]}}`,
		"f/i/echo.ts": `export async function main(x: unknown) { return x ?? null; }
`,
		"f/i/flowpath.ts": `export async function main() { return process.env.WM_FLOW_PATH ?? ""; }
`,
		"f/i/stateread.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/i/echo",` +
			`"input_transforms":{"x":{"type":"javascript","expr":"flow_user_state(\"k\")"}}}}]}}`,
		"f/i/hooked.ts": `export async function main(configured: string) {
  const e = process.env;
  const call = async (ws: string, p: string) => {
    const r = await fetch(` + "`${e.BASE_URL}/api/w/${ws}${p}`" + `, { headers: { Authorization: ` + "`Bearer ${e.WM_TOKEN}`" + ` } });
    return r.status + " " + (await r.text());
  };
  const at = async (ws: string) => ({
    root: await call(ws, ` + "`/jobs_u/get_root_job_id/${e.WM_ROOT_JOB_ID}`" + `),
    variable: (await call(ws, "/variables/get_value/u/i/a")).split(" ")[0],
  });
  return { env: e.WM_WORKSPACE, hooked: await at(e.WM_WORKSPACE!), configured: configured ? await at(configured) : null };
}
`,
		"f/i/hooked.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/i/hooked",` +
			`"input_transforms":{"configured":{"type":"javascript","expr":"flow_input.configured"}}}}]}}`,
		"f/i/overlap.ts": `export async function main(me: string, other: string) {
  const api = ` + "`${process.env.BASE_URL}/api/w/${process.env.WM_WORKSPACE}`" + `;
  const headers = { Authorization: ` + "`Bearer ${process.env.WM_TOKEN}`" + `, "Content-Type": "application/json" };
  const pub = await fetch(` + "`${api}/variables/update/u/i/r${me}`" + `, { method: "POST", headers, body: JSON.stringify({ value: "1" }) });
  if (!pub.ok) throw new Error("publish: " + pub.status);
  let seen = false;
  for (let i = 0; i < 1000 && !seen; i++) {
    seen = !!JSON.parse(await (await fetch(` + "`${api}/variables/get_value/u/i/r${other}`" + `, { headers })).text());
    if (!seen) await Bun.sleep(10);
  }
  if (!seen) throw new Error("the other run never started its step");
  const r = await fetch(` + "`${api}/jobs_u/get_root_job_id/${process.env.WM_ROOT_JOB_ID}`" + `);
  return { job: process.env.WM_JOB_ID, root: r.status + " " + (await r.text()) };
}
`,
		"f/i/overlap.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/i/overlap",` +
			`"input_transforms":{"me":{"type":"javascript","expr":"flow_input.me"},"other":{"type":"javascript","expr":"flow_input.other"}}}}]}}`,
		"f/i/nonce.ts": `export async function main() { return { workspace: process.env.WM_WORKSPACE, nonce: crypto.randomUUID() }; }
`,
		"f/i/cached_step.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"rawscript","language":"bun",` +
			`"content":"export async function main() { return { workspace: process.env.WM_WORKSPACE, nonce: crypto.randomUUID() } }","input_transforms":{}},"cache_ttl":600}]}}`,
		"f/i/cached_flow.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/i/nonce","input_transforms":{}}}],"cache_ttl":600}}`,
		"f/i/nonce_script.ts": `export async function main() { return { workspace: process.env.WM_WORKSPACE, nonce: crypto.randomUUID() }; }
`,
		"f/i/nonce_script.script.yaml": "summary: a nonce, cached for 600 s\ndescription: ''\ncache_ttl: 600\nkind: script\nschema:\n  properties: {}\n  required: []\n",
		"f/i/probe.ts": `export async function main(me: string, other: string, sibling: string) {
  const api = ` + "`${process.env.BASE_URL}/api/w/${process.env.WM_WORKSPACE}`" + `;
  const headers = { Authorization: ` + "`Bearer ${process.env.WM_TOKEN}`" + `, "Content-Type": "application/json" };
  const get = async (p: string) => { const r = await fetch(api + p, { headers }); return { status: r.status, body: await r.text() }; };
  const post = async (p: string, b: unknown) => { const r = await fetch(api + p, { method: "POST", headers, body: JSON.stringify(b) }); return { status: r.status, body: await r.text() }; };
  const d = await post("/jobs/run/p/f/i/child", {});
  if (d.status !== 201) throw new Error("dispatch: " + d.status + " " + d.body);
  const child = d.body;
  let done: { completed: boolean } = { completed: false };
  for (let i = 0; i < 500 && !done.completed; i++) {
    done = JSON.parse((await get(` + "`/jobs_u/completed/get_result_maybe/${child}`" + `)).body);
    if (!done.completed) await Bun.sleep(10);
  }
  const root = process.env.WM_ROOT_JOB_ID!;
  const set = await post(` + "`/jobs/flow/user_states/${root}/k`" + `, me);
  if (set.status !== 200) throw new Error("set state: " + set.status + " " + set.body);
  const mine = { job: process.env.WM_JOB_ID!, flow: process.env.WM_FLOW_JOB_ID!, root, sibling, child };
  const pub = await post(` + "`/variables/update/u/i/${me}`" + `, { value: JSON.stringify(mine) });
  if (pub.status !== 200) throw new Error("publish: " + pub.status + " " + pub.body);
  let theirs: typeof mine | undefined;
  for (let i = 0; i < 1000 && !theirs; i++) {
    const v = JSON.parse((await get(` + "`/variables/get_value/u/i/${other}`" + `)).body);
    if (v) theirs = JSON.parse(v);
    else await Bun.sleep(10);
  }
  if (!theirs) throw new Error("the other run never published its jobs");
  const read = async (id: string) => (await get(` + "`/jobs_u/get/${id}`" + `)).status;
  const rootOf = async (id: string) => { const r = await get(` + "`/jobs_u/get_root_job_id/${id}`" + `); return r.status + " " + r.body; };
  const poll = async (id: string) => (await get(` + "`/jobs_u/completed/get_result_maybe/${id}`" + `)).body;
  const state = async (id: string) => (await get(` + "`/jobs/flow/user_states/${id}/k`" + `)).body;
  const tracked = async (rootJob: string, parentJob: string) => {
    const t = await post(` + "`/jobs/run/f/f/i/stateread?root_job=${rootJob}&parent_job=${parentJob}`" + `, {});
    if (t.status !== 201) throw new Error("tracked dispatch: " + t.status + " " + t.body);
    for (let i = 0; i < 500; i++) {
      const r = JSON.parse((await get(` + "`/jobs_u/completed/get_result_maybe/${t.body}`" + `)).body);
      if (r.completed) return JSON.stringify(r.result);
      await Bun.sleep(10);
    }
    throw new Error("the tracked dispatch never completed");
  };
  const parentPath = async (parentJob: string) => {
    const t = await post(` + "`/jobs/run/p/f/i/flowpath?parent_job=${parentJob}`" + `, {});
    if (t.status !== 201) throw new Error("tracked dispatch: " + t.status + " " + t.body);
    for (let i = 0; i < 500; i++) {
      const r = JSON.parse((await get(` + "`/jobs_u/completed/get_result_maybe/${t.body}`" + `)).body);
      if (r.completed) return r.result;
      await Bun.sleep(10);
    }
    throw new Error("the tracked dispatch never completed");
  };
  const observed = {
    ids: mine,
    own: { job: await read(mine.job), sibling: await read(sibling), root: await rootOf(mine.job), child: await poll(child), state: await state(root) },
    tracked: { own: await tracked(root, mine.job), theirs: await tracked(theirs.root, theirs.job) },
    parentPath: { own: await parentPath(mine.flow), theirs: await parentPath(theirs.flow) },
    other: {
      job: await read(theirs.job), sibling: await read(theirs.sibling), root: await rootOf(theirs.job), child: await poll(theirs.child),
      state: await state(theirs.root), setState: (await post(` + "`/jobs/flow/user_states/${theirs.root}/k`" + `, "x")).status,
    },
  };
  // Neither run ends before both have made their observations.
  const finished = await post(` + "`/variables/update/u/i/d${me}`" + `, { value: "1" });
  if (finished.status !== 200) throw new Error("finished: " + finished.status + " " + finished.body);
  let both = false;
  for (let i = 0; i < 1000 && !both; i++) {
    both = !!JSON.parse((await get(` + "`/variables/get_value/u/i/d${other}`" + `)).body);
    if (!both) await Bun.sleep(10);
  }
  if (!both) throw new Error("the other run never finished its observations");
  return observed;
}
`,
		"f/i/probe.flow/flow.yaml": `{"summary":"t","value":{"modules":[` +
			`{"id":"first","value":{"type":"script","path":"f/i/first","input_transforms":{}}},` +
			`{"id":"probe","value":{"type":"script","path":"f/i/probe","input_transforms":{` +
			`"me":{"type":"javascript","expr":"flow_input.me"},"other":{"type":"javascript","expr":"flow_input.other"},` +
			`"sibling":{"type":"javascript","expr":"results.first"}}}}]}}`,
		"f/i/probe2.flow/flow.yaml": `{"summary":"t","value":{"modules":[` +
			`{"id":"first","value":{"type":"script","path":"f/i/first","input_transforms":{}}},` +
			`{"id":"probe","value":{"type":"script","path":"f/i/probe","input_transforms":{` +
			`"me":{"type":"javascript","expr":"flow_input.me"},"other":{"type":"javascript","expr":"flow_input.other"},` +
			`"sibling":{"type":"javascript","expr":"results.first"}}}}]}}`,
		"f/i/root.ts": `export async function main(id: string) {
  const r = await fetch(` + "`${process.env.BASE_URL}/api/w/${process.env.WM_WORKSPACE}/jobs_u/get_root_job_id/${id}`" + `);
  return r.status + " " + (await r.text());
}
`,
		"f/i/root.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/i/root",` +
			`"input_transforms":{"id":{"type":"javascript","expr":"flow_input.id"}}}}]}}`,
	}
	maps.Copy(files, storeFiles)
	return files
}()

// identityRuntime builds identityFiles once per test and opens it with
// opts, closed when the test ends.
func identityRuntime(t *testing.T, opts Options) *Runtime {
	t.Helper()
	bun := bunOrSkip(t)
	out, runtimeDir := buildBunBundle(t, bun, identityFiles)
	opts.Bun, opts.HarnessRuntime = bun, runtimeDir
	rt, err := Open(out, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rt.Close() })
	return rt
}

// runJSON runs path with args and opts, and decodes its successful result
// into v.
func runJSON(t *testing.T, rt *Runtime, path, args string, opts RunOptions, v any) {
	t.Helper()
	if err := runResult(rt, path, args, opts, v); err != nil {
		t.Fatal(err)
	}
}

// runResult is runJSON's run, which returns its failure, for a goroutine
// other than the test's.
func runResult(rt *Runtime, path, args string, opts RunOptions, v any) error {
	res, err := rt.Run(context.Background(), path, json.RawMessage(args), opts)
	switch {
	case err != nil:
		return err
	case res.Status != StatusSuccess:
		return fmt.Errorf("%s: %s %s", path, res.Status, res.Result)
	}
	if err := json.Unmarshal(res.Result, v); err != nil {
		return fmt.Errorf("%s: result %s: %v", path, res.Result, err)
	}
	return nil
}

// identityEnv is what f/i/env returns.
type identityEnv struct {
	Workspace      string `json:"workspace"`
	RootWorkspace  string `json:"root_workspace"`
	Username       string `json:"username"`
	Email          string `json:"email"`
	EndUserEmail   string `json:"end_user_email"`
	PermissionedAs string `json:"permissioned_as"`
	BaseURL        string `json:"base_url"`
	WorkerGroup    string `json:"worker_group"`
}

// Options.Identity sets the job identity of every job of the runtime: a
// flow's step reads it in its environment.
func TestIdentityOption(t *testing.T) {
	id := Identity{Workspace: "ws-one", Username: "alice", Email: "alice@example.test", PermissionedAs: "g/admins", BaseURL: "https://windmill.example.test", WorkerGroup: "group-a"}
	rt := identityRuntime(t, Options{Identity: id})
	var got identityEnv
	runJSON(t, rt, "f/i/env", `{}`, RunOptions{}, &got)
	want := identityEnv{Workspace: "ws-one", RootWorkspace: "ws-one", Username: "alice", Email: "alice@example.test", EndUserEmail: "alice@example.test",
		PermissionedAs: "g/admins", BaseURL: "https://windmill.example.test", WorkerGroup: "group-a"}
	if got != want {
		t.Fatalf("the step's identity %+v, want %+v", got, want)
	}
}

// The facade accepts the configured workspace in a call's path, and answers
// 401 for the placeholder workspace, which is then another workspace.
func TestIdentityWorkspaceCheck(t *testing.T) {
	rt := identityRuntime(t, Options{Identity: Identity{Workspace: "ws-one"}, Variables: map[string]Variable{"u/i/a": {Value: "v"}}})
	var got struct{ Own, Placeholder int }
	runJSON(t, rt, "f/i/check", `{}`, RunOptions{}, &got)
	if got.Own != 200 || got.Placeholder != 401 {
		t.Fatalf("variable read: own workspace %d, windmill-lite %d; want 200 and 401", got.Own, got.Placeholder)
	}
}

// A root script's run takes RunOptions.Identity: each field it sets
// replaces the runtime's, and the others stay the runtime's.
func TestScriptRunIdentity(t *testing.T) {
	rt := identityRuntime(t, Options{Identity: Identity{Workspace: "ws-runtime", Username: "rt-user"}})
	var got identityEnv
	runJSON(t, rt, "f/i/env", `{}`, RunOptions{Kind: KindScript, Identity: Identity{Workspace: "ws-run", Email: "run@example.test"}}, &got)
	if got.Workspace != "ws-run" || got.RootWorkspace != "ws-run" || got.Username != "rt-user" || got.Email != "run@example.test" || got.PermissionedAs != "u/rt-user" {
		t.Fatalf("the script's identity %+v; want workspace ws-run, user rt-user, email run@example.test", got)
	}
	runJSON(t, rt, "f/i/env", `{}`, RunOptions{Kind: KindScript}, &got)
	if got.Workspace != "ws-runtime" || got.Email != "windmill-lite@localhost" {
		t.Fatalf("a script run without its own identity %+v; want the runtime's", got)
	}
}

// The resource store keys its entries under the workspace: two runtimes of
// two workspaces sharing one memory store keep a state each, and so do two
// runs of one runtime in two workspaces. A run that a step dispatches
// writes in the step's run's workspace, a script and a flow alike.
func TestIdentityStoreKey(t *testing.T) {
	store := NewMemoryResourceStore()
	one := identityRuntime(t, Options{Identity: Identity{Workspace: "ws-one"}, ResourceStore: store})
	two := identityRuntime(t, Options{Identity: Identity{Workspace: "ws-two"}, ResourceStore: store})
	var s string
	runJSON(t, one, "f/s/write", `{"value":1}`, RunOptions{}, &s)
	runJSON(t, two, "f/s/write", `{"value":2}`, RunOptions{}, &s)
	runJSON(t, one, "f/s/write", `{"value":3}`, RunOptions{Identity: Identity{Workspace: "ws-three"}}, &s)
	// A run that a step of ws-four dispatches writes in ws-four.
	runJSON(t, one, "f/i/spawn", `{"route":"p/f/s/write","body":{"value":4}}`, RunOptions{Identity: Identity{Workspace: "ws-four"}}, &s)
	// So does a flow that a step of ws-five dispatches.
	runJSON(t, one, "f/i/spawn", `{"route":"f/f/s/write","body":{"value":5}}`, RunOptions{Identity: Identity{Workspace: "ws-five"}}, &s)
	for _, tc := range []struct {
		rt   *Runtime
		id   Identity
		want float64
	}{{one, Identity{}, 1}, {two, Identity{}, 2}, {one, Identity{Workspace: "ws-three"}, 3}, {two, Identity{Workspace: "ws-one"}, 1}, {two, Identity{Workspace: "ws-four"}, 4}, {two, Identity{Workspace: "ws-five"}, 5}} {
		var got float64
		runJSON(t, tc.rt, "f/s/read", `{}`, RunOptions{Identity: tc.id}, &got)
		if got != tc.want {
			t.Errorf("state in workspace %q of runtime %p: %v, want %v", tc.id.Workspace, tc.rt, got, tc.want)
		}
	}
}

// probeResult is what f/i/probe returns.
type probeResult struct {
	IDs struct{ Job, Flow, Root, Sibling, Child string } `json:"ids"`
	Own struct {
		Job, Sibling int
		Root, Child  string
		State        string
	} `json:"own"`
	Tracked    struct{ Own, Theirs string } `json:"tracked"`
	ParentPath struct{ Own, Theirs string } `json:"parentPath"`
	Other      struct {
		Job, Sibling int
		Root, Child  string
		State        string
		SetState     int `json:"setState"`
	} `json:"other"`
}

// probePair runs f/i/probe as "a" with identity a and f/i/probe2 as "b"
// with identity b at once on rt, and returns each run's result by its name.
func probePair(t *testing.T, rt *Runtime, a, b Identity) map[string]probeResult {
	t.Helper()
	runs := map[string]probeResult{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, tc := range []struct {
		me, other, path string
		id              Identity
	}{{"a", "b", "f/i/probe", a}, {"b", "a", "f/i/probe2", b}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var p probeResult
			if err := runResult(rt, tc.path, `{"me":"`+tc.me+`","other":"`+tc.other+`"}`, RunOptions{Identity: tc.id}, &p); err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			runs[tc.me] = p
			mu.Unlock()
		}()
	}
	wg.Wait()
	if t.Failed() {
		t.FailNow()
	}
	return runs
}

// probeVariables are the variables through which two runs of f/i/probe
// exchange their job ids and their completion.
var probeVariables = map[string]Variable{"u/i/a": {Value: ""}, "u/i/b": {Value: ""}, "u/i/da": {Value: ""}, "u/i/db": {Value: ""}}

const (
	noRows      = `400 SqlErr: no rows returned by a query that expected to return at least one row`
	unknownPoll = `{"started":null,"success":null,"completed":false,"result":null}`
)

// Two concurrent runs of one runtime in two workspaces (RunOptions.Identity)
// each find their own run's jobs through their workspace's paths, and the
// run that they dispatched, which runs in their workspace; they find the
// other run's jobs as unknown ids: the job read answers 404, the root
// lookup 400 SqlErr, the result poll of the run the other dispatched
// nulls, and the other's flow user state is null to read and not found to
// write. A flow that a run dispatches with the other run's jobs as its
// parent_job and root_job reads null through flow_user_state(), and one
// dispatched with its own run's reads its run's state. Once both runs have
// ended, the root lookup of an ended job still answers in its workspace
// only.
func TestTwoWorkspacesOneRuntime(t *testing.T) {
	rt := identityRuntime(t, Options{Variables: probeVariables})
	runs := probePair(t, rt, Identity{Workspace: "ws-a"}, Identity{Workspace: "ws-b"})
	for me, p := range runs {
		ws := "ws-" + me
		own, other := p.Own, p.Other
		if own.Job != 200 || own.Sibling != 200 || own.Root != `200 "`+p.IDs.Root+`"` || own.State != `"`+me+`"` {
			t.Errorf("%s, its own jobs: %+v", ws, own)
		}
		var child struct {
			Completed bool
			Result    string
		}
		if json.Unmarshal([]byte(own.Child), &child) != nil || !child.Completed || child.Result != ws {
			t.Errorf("%s, the run it dispatched: %s", ws, own.Child)
		}
		if other.Job != 404 || other.Sibling != 404 || other.Root != noRows || other.Child != unknownPoll || other.State != "null" || other.SetState != 404 {
			t.Errorf("%s, the other workspace's jobs: %+v; want 404, 404, %q, %s, null, 404", ws, other, noRows, unknownPoll)
		}
		if p.Tracked.Own != `"`+me+`"` || p.Tracked.Theirs != "null" {
			t.Errorf("%s, flow_user_state() of a tracked dispatch: own root %s, the other's %s; want %q and null", ws, p.Tracked.Own, p.Tracked.Theirs, me)
		}
		if ownPath := map[string]string{"a": "f/i/probe", "b": "f/i/probe2"}[me]; p.ParentPath.Own != ownPath || p.ParentPath.Theirs != "" {
			t.Errorf("%s, the WM_FLOW_PATH of a dispatch tracked in its own run %q and in the other's %q; want %q and none", ws, p.ParentPath.Own, p.ParentPath.Theirs, ownPath)
		}
	}
	// The ended jobs keep their workspace.
	for _, tc := range []struct{ id, ws, want string }{
		{runs["a"].IDs.Job, "ws-a", `200 "` + runs["a"].IDs.Root + `"`},
		{runs["a"].IDs.Job, "ws-b", noRows},
		{runs["b"].IDs.Root, "ws-b", `200 "` + runs["b"].IDs.Root + `"`},
		{runs["b"].IDs.Root, "ws-a", noRows},
	} {
		var got string
		runJSON(t, rt, "f/i/root", `{"id":"`+tc.id+`"}`, RunOptions{Identity: Identity{Workspace: tc.ws}}, &got)
		if got != tc.want {
			t.Errorf("root lookup of the ended job %s from %s: %s, want %s", tc.id, tc.ws, got, tc.want)
		}
	}
}

// A run that names DefaultHost's workspace, windmill-lite, and one that
// names none are in one workspace, as their job environments and resource
// store keys are: each finds the other's jobs, the run the other
// dispatched and the other's flow user state.
func TestDefaultWorkspaceAlias(t *testing.T) {
	rt := identityRuntime(t, Options{Variables: probeVariables})
	runs := probePair(t, rt, Identity{}, Identity{Workspace: "windmill-lite"})
	for me, p := range runs {
		other := runs[map[string]string{"a": "b", "b": "a"}[me]]
		// The other run sets "x" on this run's root once it has read it,
		// so this run reads its own state as its own value or as "x".
		mine := map[string]bool{`"` + me + `"`: true, `"x"`: true}
		if p.Own.Job != 200 || !mine[p.Own.State] || !mine[p.Tracked.Own] {
			t.Errorf("%s, its own jobs: %+v, tracked %s", me, p.Own, p.Tracked.Own)
		}
		var child struct {
			Completed bool
			Result    string
		}
		theirs := `"` + map[string]string{"a": "b", "b": "a"}[me] + `"`
		if p.Other.Job != 200 || p.Other.Sibling != 200 || p.Other.Root != `200 "`+other.IDs.Root+`"` || p.Other.State != theirs || p.Other.SetState != 200 ||
			json.Unmarshal([]byte(p.Other.Child), &child) != nil || !child.Completed || child.Result != "windmill-lite" {
			t.Errorf("%s, the other run's jobs: %+v; want them found", me, p.Other)
		}
		if p.Tracked.Theirs != theirs {
			t.Errorf("%s, flow_user_state() of a dispatch tracked in the other run: %s, want %s", me, p.Tracked.Theirs, theirs)
		}
		if theirPath := map[string]string{"a": "f/i/probe2", "b": "f/i/probe"}[me]; p.ParentPath.Theirs != theirPath {
			t.Errorf("%s, the WM_FLOW_PATH of a dispatch tracked in the other run: %q, want %q", me, p.ParentPath.Theirs, theirPath)
		}
	}
}

// hookedResult is what f/i/hooked returns.
type hookedResult struct {
	Env                string
	Hooked, Configured *struct{ Root, Variable string }
}

// A hook that changes a job's WM_WORKSPACE (InvocationEnv) changes the
// workspace of a run without a configured one, as it always did: its
// root lookup and its variable read answer in the hook's workspace. A run
// with a configured workspace stays in it: through the hook's workspace
// its root lookup finds no job and its variable read answers 401, and
// through the configured one both answer.
func TestIdentityHookedWorkspace(t *testing.T) {
	rt := identityRuntime(t, Options{Variables: map[string]Variable{"u/i/a": {Value: "v"}}})
	hook := func(ws string) *Hooks {
		return &Hooks{InvocationEnv: func(_ context.Context, _ RunHandle, _ Site, env map[string]string) error {
			env["WM_WORKSPACE"] = ws
			return nil
		}}
	}
	const plain, configured = "0b6d2a4e-5b5d-4f00-9c3e-00000000f001", "0b6d2a4e-5b5d-4f00-9c3e-00000000f002"
	var got hookedResult
	runJSON(t, rt, "f/i/hooked", `{"configured":""}`, RunOptions{ID: plain, Hooks: hook("ws-h")}, &got)
	if got.Env != "ws-h" || got.Hooked == nil || got.Hooked.Root != `200 "`+plain+`"` || got.Hooked.Variable != "200" {
		t.Errorf("no configured workspace, hooked to ws-h: %s %+v; want the root and 200 in ws-h", got.Env, got.Hooked)
	}
	got = hookedResult{}
	runJSON(t, rt, "f/i/hooked", `{"configured":"ws-a"}`, RunOptions{ID: configured, Identity: Identity{Workspace: "ws-a"}, Hooks: hook("ws-b")}, &got)
	if got.Env != "ws-b" || got.Hooked == nil || got.Hooked.Root != noRows || got.Hooked.Variable != "401" {
		t.Errorf("configured ws-a, hooked to ws-b, through ws-b: %s %+v; want no rows and 401", got.Env, got.Hooked)
	}
	if got.Configured == nil || got.Configured.Root != `200 "`+configured+`"` || got.Configured.Variable != "200" {
		t.Errorf("configured ws-a, hooked to ws-b, through ws-a: %+v; want the root and 200", got.Configured)
	}
}

// Two live invocations of runs of two workspaces whose hook gives both
// the same WM_JOB_ID each keep their own run's workspace: each finds its
// own run's root through its workspace.
func TestOverlappingJobIDs(t *testing.T) {
	rt := identityRuntime(t, Options{Variables: map[string]Variable{"u/i/ra": {Value: ""}, "u/i/rb": {Value: ""}}})
	const shared = "0b6d2a4e-5b5d-4f00-9c3e-00000000e000"
	hooks := &Hooks{InvocationEnv: func(_ context.Context, _ RunHandle, _ Site, env map[string]string) error {
		env["WM_JOB_ID"] = shared
		return nil
	}}
	var wg sync.WaitGroup
	for _, tc := range []struct{ me, other, ws, id string }{
		{"a", "b", "ws-a", "0b6d2a4e-5b5d-4f00-9c3e-00000000e00a"},
		{"b", "a", "ws-b", "0b6d2a4e-5b5d-4f00-9c3e-00000000e00b"},
	} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var got struct{ Job, Root string }
			if err := runResult(rt, "f/i/overlap", `{"me":"`+tc.me+`","other":"`+tc.other+`"}`, RunOptions{ID: tc.id, Identity: Identity{Workspace: tc.ws}, Hooks: hooks}, &got); err != nil {
				t.Error(err)
				return
			}
			if got.Job != shared || got.Root != `200 "`+tc.id+`"` {
				t.Errorf("%s: job %s, root lookup %s; want %s and its own root", tc.ws, got.Job, got.Root, shared)
			}
		}()
	}
	wg.Wait()
}

// A script and a flow that a step dispatches take the identity of the
// step's run, a flow's or a root script's.
func TestDispatchedRunIdentity(t *testing.T) {
	rt := identityRuntime(t, Options{Identity: Identity{Workspace: "ws-runtime", Username: "rt-user"}})
	for _, kind := range []string{KindFlow, KindScript} {
		for _, route := range []string{"p/f/i/env", "f/f/i/env"} {
			var got identityEnv
			runJSON(t, rt, "f/i/spawn", `{"route":"`+route+`","body":{}}`, RunOptions{Kind: kind, Identity: Identity{Workspace: "ws-d", Email: "dee@example.test"}}, &got)
			if got.Workspace != "ws-d" || got.Username != "rt-user" || got.Email != "dee@example.test" {
				t.Errorf("%s run, %s: the dispatched run's identity %+v; want ws-d, rt-user, dee@example.test", kind, route, got)
			}
		}
	}
}

// The result cache keeps the results of runs of each workspace apart: a
// step's, a flow's and a root script's cache_ttl each give a later run of
// the same workspace the cached result, and a run of another workspace its
// own. A run that names windmill-lite shares the results of runs that name
// no workspace.
func TestIdentityResultCache(t *testing.T) {
	rt := identityRuntime(t, Options{})
	type nonce struct{ Workspace, Nonce string }
	for _, tc := range []struct {
		path string
		kind string
	}{{"f/i/cached_step", KindFlow}, {"f/i/cached_flow", KindFlow}, {"f/i/nonce_script", KindScript}} {
		run := func(ws string) nonce {
			var n nonce
			runJSON(t, rt, tc.path, `{}`, RunOptions{Kind: tc.kind, Identity: Identity{Workspace: ws}}, &n)
			return n
		}
		a1, a2, b, d1, d2 := run("ws-a"), run("ws-a"), run("ws-b"), run(""), run("windmill-lite")
		if a1.Workspace != "ws-a" || a2 != a1 {
			t.Errorf("%s: two runs in ws-a gave %+v and %+v; want one cached result", tc.path, a1, a2)
		}
		if b.Workspace != "ws-b" || b.Nonce == a1.Nonce {
			t.Errorf("%s: a run in ws-b gave %+v after ws-a's %+v; want its own", tc.path, b, a1)
		}
		if d1.Workspace != "windmill-lite" || d2 != d1 || d1.Nonce == a1.Nonce || d1.Nonce == b.Nonce {
			t.Errorf("%s: runs naming no workspace and windmill-lite gave %+v and %+v; want one result of their own", tc.path, d1, d2)
		}
	}
}

// DecodeIdentity takes exactly one JSON object of the six keys, spelled
// exactly, each a string, and nothing after it but white space.
func TestDecodeIdentity(t *testing.T) {
	got, err := DecodeIdentity([]byte(` {"workspace":"ws","username":"u","email":"e@example.test","permissioned_as":"g/x","base_url":"https://x.test","worker_group":"g"} ` + "\n"))
	want := Identity{Workspace: "ws", Username: "u", Email: "e@example.test", PermissionedAs: "g/x", BaseURL: "https://x.test", WorkerGroup: "g"}
	if err != nil || got != want {
		t.Fatalf("a full identity: %+v, %v", got, err)
	}
	if got, err := DecodeIdentity([]byte(`{}`)); err != nil || got != (Identity{}) {
		t.Fatalf("an empty object: %+v, %v", got, err)
	}
	for _, bad := range []string{
		`null`, `[]`, `"ws"`, `7`, ``, `{"workspace":null}`, `{"workspace":1}`, `{"workspace":{}}`,
		`{"Workspace":"ws"}`, `{"WORKSPACE":"ws"}`, `{"other":"x"}`,
		`{"workspace":"ws"}}`, `{"workspace":"ws"} {}`, `{"workspace":"ws"} x`, `{"workspace":"ws"`,
	} {
		if got, err := DecodeIdentity([]byte(bad)); err == nil {
			t.Errorf("%s decoded as %+v", bad, got)
		}
	}
}

// A dispatch tracked in the root of an ended run of another workspace
// (root_job) leaves that root its workspace's: the root lookup answers in
// A and not in B before the dispatch, in the dispatched run of B, and
// after it.
func TestTrackedDispatchOfAnEndedRoot(t *testing.T) {
	rt := identityRuntime(t, Options{})
	const rootA = "0b6d2a4e-5b5d-4f00-9c3e-00000000a001"
	var env identityEnv
	runJSON(t, rt, "f/i/env", `{}`, RunOptions{ID: rootA, Identity: Identity{Workspace: "ws-a"}}, &env)
	lookups := func(when string) {
		t.Helper()
		for ws, want := range map[string]string{"ws-a": `200 "` + rootA + `"`, "ws-b": noRows} {
			var got string
			runJSON(t, rt, "f/i/root", `{"id":"`+rootA+`"}`, RunOptions{Identity: Identity{Workspace: ws}}, &got)
			if got != want {
				t.Errorf("%s: the root lookup of A's ended run from %s: %s, want %s", when, ws, got, want)
			}
		}
	}
	lookups("before")
	var during string
	runJSON(t, rt, "f/i/spawn", `{"route":"p/f/i/root?root_job=`+rootA+`","body":{"id":"`+rootA+`"}}`, RunOptions{Identity: Identity{Workspace: "ws-b"}}, &during)
	if during != noRows {
		t.Errorf("the root lookup of A's ended run from B's run tracked in it: %s, want %s", during, noRows)
	}
	lookups("after")
}
