package facade

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/platform"
)

// The steps of the services' tests on the stock client.
func init() {
	// Flow user state through the stock helpers (WA-14): op "set" writes
	// and reads back, "get" reads what another step of the run wrote, and
	// "standalone" is a script job's, whose write the helper swallows and
	// logs, and whose strict write rejects.
	bunSteps["userstate"] = `import * as wmill from "windmill-client";

const show = (v) => (v === undefined ? "undefined" : v);

export async function main(op) {
  if (op === "set") {
    await wmill.setFlowUserState("k", { n: 1 });
    await wmill.setFlowUserState("u", undefined);
    return { k: await wmill.getFlowUserState("k"), u: await wmill.getFlowUserState("u"), missing: await wmill.getFlowUserState("missing") };
  }
  if (op === "get") {
    return { k: await wmill.getFlowUserState("k"), root: (await wmill.getRootJobId()) === process.env.WM_ROOT_JOB_ID };
  }
  const logged = [];
  const error = console.error;
  console.error = (...args) => logged.push(args.join(" "));
  let set, get;
  try {
    set = await wmill.setFlowUserState("k", 1);
    get = await wmill.getFlowUserState("k");
  } finally {
    console.error = error;
  }
  let strict;
  try {
    await wmill.setFlowUserState("k", 1, true);
  } catch (err) {
    strict = err.message;
  }
  return { set: show(set), get: show(get), logged, strict };
}
`
	// The object store through the stock helpers (WA-16): an explicit key,
	// another storage, binary content, a bare string refused before any
	// request, and a missing object.
	bunSteps["objects"] = `import * as wmill from "windmill-client";

const attempt = async (f) => {
  try {
    return { value: await f() };
  } catch (err) {
    return { thrown: String(err.message ?? err) };
  }
};
const text = (bytes) => new TextDecoder().decode(bytes);

export async function main() {
  const written = await wmill.writeS3File({ s3: "t/a.txt" }, "hi");
  const read = text(await wmill.loadS3File(written));
  const other = await wmill.writeS3File("s3://other/t/a.txt", "there");
  const otherRead = text(await wmill.loadS3File("s3://other/t/a.txt"));
  const again = text(await wmill.loadS3File({ s3: "t/a.txt" }));
  await wmill.writeS3File({ s3: "bin" }, new Blob([new Uint8Array([0, 255, 128, 10])]));
  const bytes = Array.from(await wmill.loadS3File({ s3: "bin" }));
  const blob = await wmill.loadS3FileStream({ s3: "t/a.txt" });
  const bare = await attempt(() => wmill.writeS3File("t/a.txt", "x"));
  const missing = await attempt(() => wmill.loadS3File({ s3: "none" }));
  return { written, read, other, otherRead, again, bytes, type: blob.type, bare, missing };
}
`
	// An upload with no object store (WA-16/d): the stock client throws
	// the generated client's ApiError, whose message is the status's text.
	bunSteps["nostorage"] = `import * as wmill from "windmill-client";

export async function main() {
  return await wmill.writeS3File({ s3: "t/a.txt" }, "hi").then((o) => ({ ok: o }), (e) => ({ error: e.message, status: e.status }));
}
`
	// setVariable on an existing variable (WA-11/e): the client asks
	// variables/exists, then updates, and resolves to undefined.
	bunSteps["setvariable"] = `import * as wmill from "windmill-client";

export async function main() {
  const set = await wmill.setVariable("u/x/w", "c");
  return { set: set === undefined ? "undefined" : set, read: await wmill.getVariable("u/x/w") };
}
`
	// Dispatches to targets that are not roots of the bundle (ADR 0001
	// §5a): the async helpers take the refusal's body for a job id
	// (WA-V5/a), and the synchronous one, whose result poll that id fails,
	// rejects at once rather than polling forever.
	bunSteps["dispatch"] = `import * as wmill from "windmill-client";

export async function main() {
  const async = await wmill.runScriptByPathAsync("u/t/x", { a: 1 });
  let sync;
  try {
    await wmill.runScriptByPath("u/t/x", {});
  } catch (err) {
    sync = { name: err.name, status: err.status };
  }
  const flow = await wmill.runFlowAsync("f/t/y", {});
  return { async, sync, flow };
}
`
}

// WA-14/a on the stock client, in warm and fresh mode: two steps of one
// flow run share its map, a missing key and an undefined value read null,
// and a script job's write is refused with the 404, which the helper logs
// and swallows, or rejects with when strict. No call is unsupported, so
// the steps' results stand in the default fail mode.
func TestFlowUserStateOnTheClient(t *testing.T) {
	r := newBunRig(t)
	for _, mode := range []engine.ModuleState{engine.ModuleStateWarm, engine.ModuleStateFresh} {
		t.Run(string(mode), func(t *testing.T) {
			f := newFacade(t, Config{})
			x := f.Executor(r.pool(t, f, mode))
			a := r.identity("f/t/us", "a")
			end := f.platform.JobRegistry().Begin(platform.Job{ID: a.RootJobID, Kind: platform.JobFlow, Root: a.RootJobID, Path: "f/t/us"})
			defer end()
			b := a
			b.JobID, b.StepID, b.JobPath, b.Token = newID(), "b", "f/t/us/b", platform.NewToken()
			job := newID()
			script := platform.Identity{JobID: job, RootJobID: job, JobPath: "f/t/s", Token: platform.NewToken()}
			for _, c := range []struct {
				id         platform.Identity
				args, want string
			}{
				{a, `{"op":"set"}`, `{"k":{"n":1},"u":null,"missing":null}`},
				{b, `{"op":"get"}`, `{"k":{"n":1},"root":true}`},
				{script, `{"op":"standalone"}`, `{"set":"undefined","get":null,"logged":["Error setting flow user state at k: Not found: Flow job not found"],"strict":"Error setting flow user state at k: Not found: Flow job not found"}`},
			} {
				out, err := x.Execute(r.ctx(t), r.invocation(c.id, "attr/userstate", c.args, mode))
				if err != nil || out.Kind != engine.OutcomeOK || string(out.Body.Producer()) != c.want {
					t.Fatalf("%s: %+v %s %v, want %s\n%s", c.args, out, out.Body.Producer(), err, c.want, r.diag)
				}
			}
		})
	}
}

// WA-16/a–c on the stock client, in warm and fresh mode: writeS3File with
// an explicit key posts the raw body with file_key and resolves to
// {s3: <the answer's file_key>}, plus the storage of an s3://<storage>/
// URI; loadS3File reads it back, binary content intact on both
// transports; a bare string is refused before any request (WA-16/b); a
// missing object rejects with the client's text around the 404.
func TestObjectsOnTheClient(t *testing.T) {
	r := newBunRig(t)
	const want = `{"written":{"s3":"t/a.txt"},"read":"hi","other":{"s3":"t/a.txt","storage":"other"},"otherRead":"there","again":"hi","bytes":[0,255,128,10],"type":"application/octet-stream",` +
		`"bare":{"thrown":"Invalid s3 object \"t/a.txt\": expected an s3://<storage>/<key> URI (e.g. \"s3:///t/a.txt\" for key \"t/a.txt\" in the default storage) or { s3: <key> }"},` +
		`"missing":{"thrown":"Failed to load S3 file: 404 Not Found - Not found: windmill-lite: no object \"none\" in storage \"_default_\""}}`
	for _, mode := range []engine.ModuleState{engine.ModuleStateWarm, engine.ModuleStateFresh} {
		t.Run(string(mode), func(t *testing.T) {
			objects, err := platform.NewDirObjects(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			f := newFacade(t, Config{Platform: &platform.Services{Objects: objects}})
			x := f.Executor(r.pool(t, f, mode))
			id := r.identity("f/t/objects", "a")
			out, err := x.Execute(r.ctx(t), r.invocation(id, "attr/objects", `{}`, mode))
			if err != nil || out.Kind != engine.OutcomeOK || string(out.Body.Producer()) != want {
				t.Fatalf("%+v\n got %s\nwant %s\n%v\n%s", out, out.Body.Producer(), want, err, r.diag)
			}
			var routes []string
			for _, c := range r.recorded(id.JobID) {
				routes = append(routes, c.Route)
			}
			uploads := strings.Count(strings.Join(routes, "|"), "upload_s3_file")
			if uploads != 3 || len(routes) != 9 {
				t.Errorf("calls %q: want 3 uploads and 6 downloads, the bare string making none", routes)
			}
			first := r.recorded(id.JobID)[0]
			if first.Method != "POST" || first.Path != "/api/w/wl_pf1/job_helpers/upload_s3_file" || first.Query != "file_key=t%2Fa.txt" || string(first.Body) != "hi" || first.Status != 200 {
				t.Errorf("WA-16/a: the upload was %+v", first)
			}
		})
	}
}

// WA-16/d on the stock client, in warm and fresh mode: with no object
// store, an upload answers 500, as Windmill CE answers it with no
// workspace storage, so writeS3File rejects with the generated client's
// "Internal Server Error". The call is not unsupported, so the step's own
// result stands in the default fail mode.
func TestNoObjectStoreOnTheClient(t *testing.T) {
	r := newBunRig(t)
	const want = `{"error":"Internal Server Error","status":500}`
	for _, mode := range []engine.ModuleState{engine.ModuleStateWarm, engine.ModuleStateFresh} {
		t.Run(string(mode), func(t *testing.T) {
			f := newFacade(t, Config{})
			x := f.Executor(r.pool(t, f, mode))
			id := r.identity("f/t/nostorage", "a")
			out, err := x.Execute(r.ctx(t), r.invocation(id, "attr/nostorage", `{}`, mode))
			if err != nil || out.Kind != engine.OutcomeOK || string(out.Body.Producer()) != want {
				t.Fatalf("%+v\n got %s\nwant %s\n%v\n%s", out, out.Body.Producer(), want, err, r.diag)
			}
			if calls := r.recorded(id.JobID); len(calls) != 1 || calls[0].Status != 500 || calls[0].Unsupported {
				t.Errorf("calls %+v", calls)
			}
		})
	}
}

// WA-11/e on the stock client, in warm and fresh mode: setVariable on an
// existing variable asks variables/exists, then sends the update, whose
// answer it does not return: it resolves to undefined, and the variable
// then reads the new value.
func TestSetVariableOnTheClient(t *testing.T) {
	r := newBunRig(t)
	const want = `{"set":"undefined","read":"c"}`
	for _, mode := range []engine.ModuleState{engine.ModuleStateWarm, engine.ModuleStateFresh} {
		t.Run(string(mode), func(t *testing.T) {
			store := platform.NewStore()
			store.SetVariable("u/x/w", platform.Variable{Value: "a"})
			f := newFacade(t, Config{Platform: &platform.Services{Variables: store}})
			x := f.Executor(r.pool(t, f, mode))
			id := r.identity("f/t/setvariable", "a")
			out, err := x.Execute(r.ctx(t), r.invocation(id, "attr/setvariable", `{}`, mode))
			if err != nil || out.Kind != engine.OutcomeOK || string(out.Body.Producer()) != want {
				t.Fatalf("%+v\n got %s\nwant %s\n%v\n%s", out, out.Body.Producer(), want, err, r.diag)
			}
			var routes []string
			for _, c := range r.recorded(id.JobID) {
				routes = append(routes, fmt.Sprint(c.Status, " ", c.Route))
			}
			if want := []string{"200 GET /w/{ws}/variables/exists/{path}", "200 POST /w/{ws}/variables/update/{path}", "200 GET /w/{ws}/variables/get_value/{path}"}; !slices.Equal(routes, want) {
				t.Errorf("calls %q, want %q", routes, want)
			}
		})
	}
}

// The dispatch-target restriction on the stock client (ADR 0001 §5a), in
// warm and fresh mode: with no target in the bundle, runScriptByPathAsync
// and runFlowAsync resolve to the refusal's body as a job id (WA-V5/a),
// and runScriptByPath rejects with the 400 of a result poll whose id is
// not a UUID (WA-V2/c) instead of polling forever. In fail mode the step fails with
// UnsupportedPlatformCall, "dispatch target not in bundle: <path>",
// listing the three dispatches.
func TestDispatchRefusedOnTheClient(t *testing.T) {
	r := newBunRig(t)
	const observed = `{"async":"dispatch target not in bundle: u/t/x","sync":{"name":"ApiError","status":400},"flow":"dispatch target not in bundle: f/t/y"}`
	const failed = `{"name":"UnsupportedPlatformCall","message":"dispatch target not in bundle: u/t/x","calls":["POST /w/{ws}/jobs/run/p/{path}","POST /w/{ws}/jobs/run/p/{path}","POST /w/{ws}/jobs/run/f/{path}"]}`
	for _, mode := range []engine.ModuleState{engine.ModuleStateWarm, engine.ModuleStateFresh} {
		for _, c := range []struct {
			policy Policy
			kind   engine.OutcomeKind
			want   string
		}{{PolicyObserve, engine.OutcomeOK, observed}, {PolicyFail, engine.OutcomeError, failed}} {
			t.Run(string(mode)+" "+string(c.policy), func(t *testing.T) {
				f := newFacade(t, Config{Policy: c.policy})
				x := f.Executor(r.pool(t, f, mode))
				id := r.identity("f/t/dispatch", "a")
				out, err := x.Execute(r.ctx(t), r.invocation(id, "attr/dispatch", `{}`, mode))
				if err != nil || out.Kind != c.kind || string(out.Body.Producer()) != c.want {
					t.Fatalf("%+v\n got %s\nwant %s\n%v\n%s", out, out.Body.Producer(), c.want, err, r.diag)
				}
			})
		}
	}
}
