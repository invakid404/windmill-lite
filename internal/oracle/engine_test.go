package oracle

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/openflow"
	"github.com/invakid404/windmill-lite/internal/syncfolder"
)

// fakeEngine is a Windmill API that executes the flows of a stub fixture's
// sync tree job by job, as the oracle's worker does, for the session
// tests of stub mode: flow_status entries go from WaitingForPriorSteps to
// InProgress to Success or Failure while the run is in progress, every
// leaf job is a stub that POSTs its call to the replay service and takes
// its outcome, failed steps are retried, a sequential loop with a one-step
// plain body collapses into leaf iterations, parallel iterations run at
// once, and named subflows are child flows. Its environment values follow
// OR-V6: a named subflow and a parallel iteration are new innermost root
// flows; WM_ROOT_JOB_ID is always the submitted root. It models only what
// the stub fixtures use: transforms that read flow_input or results by a
// dotted path, and static ones.
type fakeEngine struct {
	t      *testing.T
	folder *syncfolder.Folder
	docker *fakeDocker
	// skipCall names a step whose jobs complete without calling the
	// replay service, like a script that was not stubbed.
	skipCall string

	mu      sync.Mutex
	jobs    map[string]*fjob
	n       int
	deleted []string
	wg      sync.WaitGroup
}

type fjob struct {
	kind, path string
	created    time.Time
	finished   time.Time
	completed  bool
	success    bool
	args       json.RawMessage
	result     json.RawMessage
	fs         *fflowStatus
}

type fflowStatus struct {
	Step    int     `json:"step"`
	Modules []*fmod `json:"modules"`
	Failure *fmod   `json:"failure_module"`
}

type fmod struct {
	Type          string    `json:"type"`
	ID            string    `json:"id"`
	Job           *string   `json:"job,omitempty"`
	FlowJobs      *[]string `json:"flow_jobs,omitempty"`
	Iterator      *struct{} `json:"iterator,omitempty"`
	FailedRetries []string  `json:"failed_retries,omitempty"`
}

func newFakeEngine(t *testing.T, tree string, d *fakeDocker) *fakeEngine {
	f, err := syncfolder.Open(tree)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return &fakeEngine{t: t, folder: f, docker: d, jobs: map[string]*fjob{}}
}

func (e *fakeEngine) newJob(kind, path string, args any, fs *fflowStatus) string {
	data, _ := json.Marshal(args)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.n++
	id := fmt.Sprintf("30000000-0000-4000-8000-%012d", e.n)
	e.jobs[id] = &fjob{kind: kind, path: path, args: data, fs: fs}
	return id
}

func (e *fakeEngine) complete(id string, ok bool, result any) {
	data, _ := json.Marshal(result)
	e.mu.Lock()
	defer e.mu.Unlock()
	j := e.jobs[id]
	j.completed, j.success, j.result, j.finished = true, ok, data, time.Now()
}

// set changes a module entry under the lock, which the API reads.
func (e *fakeEngine) set(f func()) {
	e.mu.Lock()
	defer e.mu.Unlock()
	f()
}

func waitingStatus(modules []openflow.Module) *fflowStatus {
	fs := &fflowStatus{Failure: &fmod{Type: "WaitingForPriorSteps", ID: "failure"}}
	for _, m := range modules {
		fs.Modules = append(fs.Modules, &fmod{Type: "WaitingForPriorSteps", ID: m.ID})
	}
	return fs
}

func (e *fakeEngine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/api")
	write := func(code int, body string) {
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	}
	switch p {
	case "/version":
		write(200, OracleVersion)
		return
	case "/health/status":
		write(200, `{"status":"healthy","database_healthy":true,"workers_alive":1}`)
		return
	case "/auth/login":
		write(200, "session-token")
		return
	case "/workspaces/create":
		write(201, "created")
		return
	}
	if strings.HasPrefix(p, "/workspaces/delete/") && r.Method == http.MethodDelete {
		e.mu.Lock()
		e.deleted = append(e.deleted, strings.TrimPrefix(p, "/workspaces/delete/"))
		e.mu.Unlock()
		write(200, "deleted")
		return
	}
	rest := p[strings.Index(p[3:], "/")+4:] // after /w/{ws}/
	switch {
	case rest == "jobs/queue/count":
		write(200, `{"database_length":0}`)
	case rest == "scripts/list":
		var list []map[string]string
		for _, s := range e.folder.Paths(syncfolder.KindScript) {
			list = append(list, map[string]string{"path": s, "hash": "h" + strings.ReplaceAll(s, "/", "_")})
		}
		data, _ := json.Marshal(list)
		write(200, string(data))
	case strings.HasPrefix(rest, "scripts/deployment_status/h/"):
		write(200, `{"lock":"","lock_error_logs":null,"job_id":null}`)
	case rest == "flows/list":
		var list []map[string]string
		for _, f := range e.folder.Paths(syncfolder.KindFlow) {
			list = append(list, map[string]string{"path": f})
		}
		data, _ := json.Marshal(list)
		write(200, string(data))
	case strings.HasPrefix(rest, "flows/deployment_status/p/"):
		write(200, `{"lock_error_logs":null,"job_id":null}`)
	case strings.HasPrefix(rest, "flows/get/"):
		fl, err := e.folder.Flow(strings.TrimPrefix(rest, "flows/get/"))
		if err != nil {
			write(404, err.Error())
			return
		}
		var mods []map[string]string
		for _, m := range fl.Document.Value.Modules {
			mods = append(mods, map[string]string{"id": m.ID})
		}
		data, _ := json.Marshal(map[string]any{"value": map[string]any{"modules": mods}})
		write(200, string(data))
	case strings.HasPrefix(rest, "jobs/run/f/"):
		e.run(w, r, strings.TrimPrefix(rest, "jobs/run/f/"))
	case strings.HasPrefix(rest, "jobs_u/"):
		id := rest[strings.LastIndex(rest, "/")+1:]
		e.mu.Lock()
		defer e.mu.Unlock()
		j, ok := e.jobs[id]
		if !ok {
			write(404, "no job")
			return
		}
		switch {
		case strings.HasPrefix(rest, "jobs_u/get/"):
			typ := "QueuedJob"
			if j.completed {
				typ = "CompletedJob"
			}
			job := map[string]any{"type": typ, "id": id, "job_kind": j.kind, "script_path": j.path, "canceled": false, "is_skipped": false}
			if !j.created.IsZero() {
				job["created_at"] = j.created.UTC().Format(time.RFC3339Nano)
			}
			if j.completed {
				job["success"] = j.success
				job["completed_at"] = j.finished.UTC().Format(time.RFC3339Nano)
			}
			if j.fs != nil {
				job["flow_status"] = j.fs
			}
			data, err := json.Marshal(job)
			if err != nil {
				e.t.Error(err)
			}
			write(200, string(data))
		case strings.HasPrefix(rest, "jobs_u/get_args/"):
			write(200, string(j.args))
		default:
			if !j.completed {
				write(200, `{"completed":false,"result":null}`)
				return
			}
			write(200, fmt.Sprintf(`{"completed":true,"started":true,"success":%v,"result":%s}`, j.success, j.result))
		}
	default:
		e.t.Errorf("fake engine: unexpected %s %s", r.Method, r.URL)
		write(404, "unexpected")
	}
}

// scope is what a module's job sees: the outer root, the innermost root
// flow, the job's parent flow, the flow input and the results so far.
type scope struct {
	root, rootFlow, flow string
	input                map[string]any
	results              map[string]any
}

func (e *fakeEngine) run(w http.ResponseWriter, r *http.Request, path string) {
	root := r.URL.Query().Get("job_id")
	body, _ := io.ReadAll(r.Body)
	var input map[string]any
	_ = json.Unmarshal(body, &input)
	fl, err := e.folder.Flow(path)
	if err != nil {
		w.WriteHeader(404)
		return
	}
	fs := waitingStatus(fl.Document.Value.Modules)
	e.mu.Lock()
	e.jobs[root] = &fjob{kind: "flow", path: path, args: body, fs: fs, created: time.Now()}
	e.mu.Unlock()
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		ok, res := e.flow(scope{root: root, rootFlow: root, flow: root, input: input}, fs, fl.Document.Value.Modules)
		e.complete(root, ok, res)
	}()
	w.WriteHeader(201)
	_, _ = io.WriteString(w, root)
}

// flow runs a module list whose status entries are fs.Modules.
func (e *fakeEngine) flow(sc scope, fs *fflowStatus, modules []openflow.Module) (bool, any) {
	sc.results = map[string]any{}
	var last any = sc.input
	for i := range modules {
		m := &modules[i]
		entry := fs.Modules[i]
		ok, res := e.module(sc, entry, m)
		sc.results[m.ID] = res
		last = res
		if !ok {
			return false, res
		}
	}
	return true, last
}

func (e *fakeEngine) module(sc scope, entry *fmod, m *openflow.Module) (bool, any) {
	switch v := m.Value.(type) {
	case *openflow.RawScript:
		return e.leaf(sc, entry, m.ID, m.ID, "flowscript", v.Content, e.transforms(sc, v.InputTransforms), m.Retry)
	case *openflow.PathScript:
		s, err := e.folder.Script(v.Path)
		if err != nil {
			e.t.Fatal(err)
		}
		return e.leaf(sc, entry, m.ID, m.ID, "script", s.Content, e.transforms(sc, v.InputTransforms), m.Retry)
	case *openflow.PathFlow:
		sub, err := e.folder.Flow(v.Path)
		if err != nil {
			e.t.Fatal(err)
		}
		fs := waitingStatus(sub.Document.Value.Modules)
		input := e.transforms(sc, v.InputTransforms)
		id := e.newJob("flow", v.Path, input, fs)
		e.set(func() { entry.Type, entry.Job = "InProgress", &id })
		ok, res := e.flow(scope{root: sc.root, rootFlow: id, flow: id, input: input}, fs, sub.Document.Value.Modules)
		e.complete(id, ok, res)
		e.set(func() { entry.Type = status(ok) })
		return ok, res
	case *openflow.ForLoop:
		return e.loop(sc, entry, m, v)
	}
	e.t.Fatalf("fake engine: module %s of kind %s", m.ID, m.Value.Kind())
	return false, nil
}

func status(ok bool) string {
	if ok {
		return "Success"
	}
	return "Failure"
}

func (e *fakeEngine) loop(sc scope, entry *fmod, m *openflow.Module, v *openflow.ForLoop) (bool, any) {
	items, _ := e.eval(sc, v.Iterator).([]any)
	jobs := []string{}
	e.set(func() { entry.Type, entry.Iterator, entry.FlowJobs = "InProgress", &struct{}{}, &jobs })
	body := v.Modules
	collapsed := !v.Parallel && len(body) == 1 && body[0].Retry == nil
	if _, raw := body[0].Value.(*openflow.RawScript); !raw {
		collapsed = false
	}
	results := make([]any, len(items))
	oks := make([]bool, len(items))
	iterate := func(i int) {
		in := map[string]any{}
		for k, x := range sc.input {
			in[k] = x
		}
		if old, ok := in["iter"]; ok {
			in["iter_parent"] = old
		}
		in["iter"] = map[string]any{"index": i, "value": items[i]}
		if collapsed {
			rs := body[0].Value.(*openflow.RawScript)
			isc := sc
			isc.input = in
			oks[i], results[i] = e.leaf(sc, nil, m.ID, body[0].ID, "flowscript", rs.Content, e.transforms(isc, rs.InputTransforms), nil, func(id string) {
				e.set(func() { jobs = append(jobs, id); entry.FlowJobs = &jobs })
			})
			return
		}
		kind, child := "flownode", scope{root: sc.root, rootFlow: sc.rootFlow, input: in}
		fs := waitingStatus(body)
		id := e.newJob(kind, "", in, fs)
		child.flow = id
		if v.Parallel {
			e.set(func() { e.jobs[id].kind = "flowpreview" })
			child.rootFlow = id
		}
		e.set(func() { jobs[i] = id })
		oks[i], results[i] = e.flow(child, fs, body)
		e.complete(id, oks[i], results[i])
	}
	if v.Parallel {
		e.set(func() {
			jobs = make([]string, len(items))
			entry.FlowJobs = &jobs
		})
		var wg sync.WaitGroup
		for i := range items {
			wg.Add(1)
			go func() { defer wg.Done(); iterate(i) }()
		}
		wg.Wait()
	} else {
		for i := range items {
			if !collapsed {
				e.set(func() { jobs = append(jobs, ""); entry.FlowJobs = &jobs })
			}
			iterate(i)
			if !oks[i] && !v.EffectiveSkipFailures() {
				results = results[:i+1]
				e.set(func() { entry.Type = "Failure" })
				return false, results
			}
		}
	}
	ok := true
	for _, o := range oks {
		ok = ok && (o || v.EffectiveSkipFailures())
	}
	e.set(func() { entry.Type = status(ok) })
	return ok, results
}

var (
	paramName = regexp.MustCompile(`^\s*([A-Za-z_$][\w$]*)`)
	dateAlias = regexp.MustCompile(`^(?:export\s+)?type\s+([A-Za-z_$][\w$]*)\s*=\s*Date\s*;?$`)
)

// leaf runs a stub's job and its retries. The job's step id is step (the
// loop's id for a collapsed body). With entry nil it is a collapsed
// iteration, announced through onJob.
func (e *fakeEngine) leaf(sc scope, entry *fmod, step, module, kind, content string, args map[string]any, retry *openflow.Retry, onJob ...func(string)) (bool, any) {
	m, err := scanTS(content)
	if err != nil {
		e.t.Fatal(err)
	}
	// The wrapper hydrates a parameter whose annotation is Date or a local
	// alias of it (WR-8, WR-V3), and binds by name in parameter order.
	dates := map[string]bool{"Date": true}
	for _, d := range m.keep {
		if a := dateAlias.FindStringSubmatch(d); a != nil {
			dates[a[1]] = true
		}
	}
	var positional []any
	for _, p := range strings.Split(m.params, ",") {
		n := paramName.FindStringSubmatch(p)
		if n == nil {
			continue
		}
		v := args[n[1]]
		if _, typ, _ := strings.Cut(p, ":"); dates[strings.TrimSpace(typ)] {
			if s, ok := v.(string); ok {
				if at, err := time.Parse(time.RFC3339, s); err == nil {
					v = at.UTC().Format("2006-01-02T15:04:05.000Z")
				}
			}
		}
		positional = append(positional, v)
	}
	retries := 0
	if retry != nil {
		retries = int(retry.MaxRetries())
	}
	var failed []string
	for attempt := 0; ; attempt++ {
		id := e.newJob(kind, "", args, nil)
		if entry != nil {
			e.set(func() {
				entry.Type, entry.Job, entry.FailedRetries = "InProgress", &id, append([]string(nil), failed...)
			})
		}
		for _, f := range onJob {
			f(id)
		}
		ok, res := e.call(sc, id, step, positional)
		e.complete(id, ok, res)
		if ok || attempt >= retries {
			if entry != nil {
				e.set(func() { entry.Type = status(ok) })
			}
			return ok, res
		}
		failed = append(failed, id)
	}
}

// call is the stub: it POSTs to the replay service as the generated stub
// does, and turns the answer into the job's outcome as the Bun wrapper
// stores it (OR-12).
func (e *fakeEngine) call(sc scope, job, step string, args []any) (bool, any) {
	if step == e.skipCall {
		return true, "not stubbed"
	}
	e.docker.mu.Lock()
	u, err := url.Parse(e.docker.sinkURL)
	e.docker.mu.Unlock()
	if err != nil {
		e.t.Fatal(err)
	}
	req, _ := json.Marshal(map[string]any{"root": sc.root, "root_flow": sc.rootFlow, "flow": sc.flow, "job": job, "step": step, "args": args})
	resp, err := http.Post("http://127.0.0.1:"+u.Port()+"/replay", "application/json", bytes.NewReader(req))
	if err != nil {
		e.t.Error(err)
		return false, nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	fail := func(name, message string, extra map[string]any) (bool, any) {
		obj := map[string]any{"name": name, "message": message, "stack": name + ": " + message + "\n    at main (/tmp/windmill/cache/bun/x.ts:1:1)", "step_id": step}
		if len(extra) > 0 {
			obj["extra"] = extra
		}
		return false, map[string]any{"error": obj}
	}
	if resp.StatusCode != 200 {
		return fail("Error", "wltest replay refused the call (HTTP "+fmt.Sprint(resp.StatusCode)+"): "+string(body), nil)
	}
	var out struct {
		Result any            `json:"result"`
		Error  map[string]any `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		e.t.Error(err)
	}
	if out.Error == nil {
		return true, out.Result
	}
	name, _ := out.Error["name"].(string)
	if name == "" {
		name = "Error"
	}
	msg, _ := out.Error["message"].(string)
	extra := map[string]any{}
	for k, v := range out.Error {
		if k != "name" && k != "message" {
			extra[k] = v
		}
	}
	return fail(name, msg, extra)
}

func (e *fakeEngine) transforms(sc scope, ts openflow.Transforms) map[string]any {
	out := map[string]any{}
	for _, t := range ts {
		out[t.Name] = e.eval(sc, t.Transform)
	}
	return out
}

// eval evaluates a static transform, or a javascript one that is a dotted
// path into flow_input or results.
func (e *fakeEngine) eval(sc scope, t openflow.InputTransform) any {
	if t.Type == openflow.Static {
		var v any
		_ = json.Unmarshal(t.Value, &v)
		return v
	}
	parts := strings.Split(t.Expr, ".")
	var cur any
	switch parts[0] {
	case "flow_input":
		cur = sc.input
	case "results":
		cur = sc.results
	default:
		e.t.Fatalf("fake engine: expression %q", t.Expr)
	}
	for _, p := range parts[1:] {
		m, _ := cur.(map[string]any)
		cur = m[p]
	}
	return cur
}

// jobKinds lists the kinds of the jobs the engine ran, for checks.
func (e *fakeEngine) jobKinds() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, j := range e.jobs {
		out = append(out, j.kind)
	}
	sort.Strings(out)
	return out
}
