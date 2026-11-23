package wl

import (
	"context"
	"encoding/json"
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
//     other run's in u/i/<other>, and looks up both runs' jobs through its
//     own workspace;
//   - f/i/root returns the root lookup of a job id through the step's own
//     workspace;
//   - f/i/spawn dispatches f/s/write with its value and returns its result;
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
		"f/i/spawn.ts": `export async function main(value: unknown) {
  const api = ` + "`${process.env.BASE_URL}/api/w/${process.env.WM_WORKSPACE}`" + `;
  const headers = { Authorization: ` + "`Bearer ${process.env.WM_TOKEN}`" + `, "Content-Type": "application/json" };
  const d = await fetch(api + "/jobs/run/p/f/s/write", { method: "POST", headers, body: JSON.stringify({ value }) });
  const id = await d.text();
  if (d.status !== 201) throw new Error("dispatch: " + d.status + " " + id);
  for (let i = 0; i < 500; i++) {
    const r = await (await fetch(api + "/jobs_u/completed/get_result_maybe/" + id, { headers })).json();
    if (r.completed) return r.result;
    await Bun.sleep(10);
  }
  throw new Error("the dispatched write never completed");
}
`,
		"f/i/spawn.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/i/spawn",` +
			`"input_transforms":{"value":{"type":"javascript","expr":"flow_input.value"}}}}]}}`,
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
  return {
    ids: mine,
    own: { job: await read(mine.job), sibling: await read(sibling), root: await rootOf(mine.job), child: await poll(child), state: await state(root) },
    other: {
      job: await read(theirs.job), sibling: await read(theirs.sibling), root: await rootOf(theirs.job), child: await poll(theirs.child),
      state: await state(theirs.root), setState: (await post(` + "`/jobs/flow/user_states/${theirs.root}/k`" + `, "x")).status,
    },
  };
}
`,
		"f/i/probe.flow/flow.yaml": `{"summary":"t","value":{"modules":[` +
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
	res, err := rt.Run(context.Background(), path, json.RawMessage(args), opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusSuccess {
		t.Fatalf("%s: %s %s", path, res.Status, res.Result)
	}
	if err := json.Unmarshal(res.Result, v); err != nil {
		t.Fatalf("%s: result %s: %v", path, res.Result, err)
	}
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
// writes in the step's run's workspace.
func TestIdentityStoreKey(t *testing.T) {
	store := NewMemoryResourceStore()
	one := identityRuntime(t, Options{Identity: Identity{Workspace: "ws-one"}, ResourceStore: store})
	two := identityRuntime(t, Options{Identity: Identity{Workspace: "ws-two"}, ResourceStore: store})
	var s string
	runJSON(t, one, "f/s/write", `{"value":1}`, RunOptions{}, &s)
	runJSON(t, two, "f/s/write", `{"value":2}`, RunOptions{}, &s)
	runJSON(t, one, "f/s/write", `{"value":3}`, RunOptions{Identity: Identity{Workspace: "ws-three"}}, &s)
	// A run that a step of ws-four dispatches writes in ws-four.
	runJSON(t, one, "f/i/spawn", `{"value":4}`, RunOptions{Identity: Identity{Workspace: "ws-four"}}, &s)
	for _, tc := range []struct {
		rt   *Runtime
		id   Identity
		want float64
	}{{one, Identity{}, 1}, {two, Identity{}, 2}, {one, Identity{Workspace: "ws-three"}, 3}, {two, Identity{Workspace: "ws-one"}, 1}, {two, Identity{Workspace: "ws-four"}, 4}} {
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
	Other struct {
		Job, Sibling int
		Root, Child  string
		State        string
		SetState     int `json:"setState"`
	} `json:"other"`
}

// Two concurrent runs of one runtime in two workspaces (RunOptions.Identity)
// each find their own run's jobs through their workspace's paths, and the
// run that they dispatched, which runs in their workspace; they find the
// other run's jobs as unknown ids: the job read answers 404, the root lookup 400
// SqlErr, the result poll of the run the other dispatched nulls, and the
// other's flow user state is null to read and not found to write. Once both
// runs have ended, the root lookup of an ended job still answers in its
// workspace only.
func TestTwoWorkspacesOneRuntime(t *testing.T) {
	rt := identityRuntime(t, Options{Variables: map[string]Variable{"u/i/a": {Value: ""}, "u/i/b": {Value: ""}}})
	runs := map[string]probeResult{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, tc := range []struct{ me, other, ws string }{{"a", "b", "ws-a"}, {"b", "a", "ws-b"}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var p probeResult
			runJSON(t, rt, "f/i/probe", `{"me":"`+tc.me+`","other":"`+tc.other+`"}`, RunOptions{Identity: Identity{Workspace: tc.ws}}, &p)
			mu.Lock()
			runs[tc.ws] = p
			mu.Unlock()
		}()
	}
	wg.Wait()
	if t.Failed() {
		return
	}
	noRows := `400 SqlErr: no rows returned by a query that expected to return at least one row`
	unknownPoll := `{"started":null,"success":null,"completed":false,"result":null}`
	for ws, p := range runs {
		own, other := p.Own, p.Other
		if own.Job != 200 || own.Sibling != 200 || own.Root != `200 "`+p.IDs.Root+`"` || own.State != `"`+map[string]string{"ws-a": "a", "ws-b": "b"}[ws]+`"` {
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
	}
	// The ended jobs keep their workspace.
	for _, tc := range []struct{ id, ws, want string }{
		{runs["ws-a"].IDs.Job, "ws-a", `200 "` + runs["ws-a"].IDs.Root + `"`},
		{runs["ws-a"].IDs.Job, "ws-b", noRows},
		{runs["ws-b"].IDs.Root, "ws-b", `200 "` + runs["ws-b"].IDs.Root + `"`},
		{runs["ws-b"].IDs.Root, "ws-a", noRows},
	} {
		var got string
		runJSON(t, rt, "f/i/root", `{"id":"`+tc.id+`"}`, RunOptions{Identity: Identity{Workspace: tc.ws}}, &got)
		if got != tc.want {
			t.Errorf("root lookup of the ended job %s from %s: %s, want %s", tc.id, tc.ws, got, tc.want)
		}
	}
}
