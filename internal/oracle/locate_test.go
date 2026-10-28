package oracle

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/openflow"
	"github.com/invakid404/windmill-lite/internal/yamlx"
)

// jobAPI serves GET jobs_u/get/{id} from a map of job JSON texts that a
// test changes while a Locate runs, and counts the requests per job.
type jobAPI struct {
	mu   sync.Mutex
	jobs map[string]string
	gets map[string]int
}

func (a *jobAPI) set(id, job string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.jobs[id] = job
}

func (a *jobAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	a.mu.Lock()
	defer a.mu.Unlock()
	a.gets[id]++
	j, ok := a.jobs[id]
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	_, _ = w.Write([]byte(j))
}

func treeOf(t *testing.T, flows map[string]string) *SyncTree {
	t.Helper()
	tree := &SyncTree{Flows: map[string]*openflow.Flow{}}
	for p, y := range flows {
		j, err := yamlx.ToJSON([]byte(y))
		if err != nil {
			t.Fatal(err)
		}
		f, err := openflow.DecodeFlow(j)
		if err != nil {
			t.Fatal(err)
		}
		tree.Flows[p] = f
	}
	return tree
}

func newLocator(t *testing.T, tree *SyncTree, jobs map[string]string) (*Locator, *jobAPI) {
	api := &jobAPI{jobs: jobs, gets: map[string]int{}}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	return &Locator{Client: &Client{BaseURL: srv.URL}, Workspace: "ws", Tree: tree, Interval: time.Millisecond}, api
}

func flowJob(id, path, typ, status string) string {
	return `{"type":"` + typ + `","id":"` + id + `","job_kind":"flow","script_path":"` + path + `","flow_status":` + status + `}`
}

const twoScripts = "summary: s\nvalue:\n  modules:\n    - {id: a, value: {type: rawscript, content: x, language: bun, input_transforms: {}}}\n    - {id: a, value: {type: rawscript, content: x, language: bun, input_transforms: {}}}\n  failure_module: {id: recover, value: {type: rawscript, content: x, language: bun, input_transforms: {}}}\n"

// TestLocatorFindsALateJob looks for a job that is not in its parent's
// status yet: the locator walks again until the job appears.
func TestLocatorFindsALateJob(t *testing.T) {
	tree := treeOf(t, map[string]string{"f/x/w": twoScripts})
	waiting := `{"modules":[{"type":"InProgress","id":"a","job":"A0"},{"type":"WaitingForPriorSteps","id":"a"}],"failure_module":{"type":"WaitingForPriorSteps","id":"failure"}}`
	l, api := newLocator(t, tree, map[string]string{"R": flowJob("R", "f/x/w", "QueuedJob", waiting)})
	done := make(chan conformance.SitePath)
	go func() {
		site, err := l.Locate(context.Background(), "R", "A1")
		if err != nil {
			t.Error(err)
		}
		done <- site
	}()
	time.Sleep(20 * time.Millisecond)
	api.set("R", flowJob("R", "f/x/w", "QueuedJob", `{"modules":[{"type":"Success","id":"a","job":"A0"},{"type":"InProgress","id":"a","job":"A1"}]}`))
	// The second of two modules with one id is m:a~1 (ADR 0011).
	if site := <-done; site.String() != "m:a~1" {
		t.Fatalf("site %q", site)
	}
	if api.gets["R"] < 2 {
		t.Fatalf("the root was read %d times; a running root is read again", api.gets["R"])
	}
	site, err := l.Locate(context.Background(), "R", "A0")
	if err != nil || site.String() != "m:a~0" {
		t.Fatalf("A0: %q %v", site, err)
	}
}

// TestLocatorGivesUp ends with the context when the job never appears.
func TestLocatorGivesUp(t *testing.T) {
	l, _ := newLocator(t, treeOf(t, map[string]string{"f/x/w": twoScripts}), map[string]string{
		"R": flowJob("R", "f/x/w", "QueuedJob", `{"modules":[{"type":"WaitingForPriorSteps","id":"a"},{"type":"WaitingForPriorSteps","id":"a"}]}`),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := l.Locate(ctx, "R", "nowhere"); err == nil || !strings.Contains(err.Error(), "is not in the job tree of run R") {
		t.Fatalf("got %v", err)
	}
	// A root the API does not know yet is looked for again too.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel2()
	if _, err := l.Locate(ctx2, "unknown-root", "j"); err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("got %v", err)
	}
}

// TestLocatorSites places jobs in each shape the walker knows (OR-11).
func TestLocatorSites(t *testing.T) {
	tree := treeOf(t, map[string]string{
		"f/x/w": twoScripts,
		"f/x/b": "summary: s\nvalue:\n  modules:\n    - id: pick\n      value:\n        type: branchone\n        branches:\n          - {expr: 'false', modules: [{id: x, value: {type: rawscript, content: c, language: bun, input_transforms: {}}}]}\n          - {expr: 'true', modules: [{id: y, value: {type: rawscript, content: c, language: bun, input_transforms: {}}}]}\n        default: []\n",
		"f/x/l": "summary: s\nvalue:\n  modules:\n    - id: loop\n      value:\n        type: forloopflow\n        iterator: {type: javascript, expr: '[1]'}\n        modules:\n          - {id: p, value: {type: rawscript, content: c, language: bun, input_transforms: {}}}\n          - {id: q, value: {type: rawscript, content: c, language: bun, input_transforms: {}}}\n",
	})
	jobs := map[string]string{
		// A failure handler is at s:failure; a failed attempt at the
		// module's own site.
		"F": flowJob("F", "f/x/w", "QueuedJob", `{"modules":[{"type":"Failure","id":"a","job":"A1","failed_retries":["A0"]},{"type":"WaitingForPriorSteps","id":"a"}],"failure_module":{"type":"InProgress","id":"recover","job":"H"}}`),
		// A branchone body is walked under b:<n>.
		"B":  flowJob("B", "f/x/b", "QueuedJob", `{"modules":[{"type":"InProgress","id":"pick","job":"B1","branch_chosen":{"type":"branch","branch":1}}]}`),
		"B1": flowJob("B1", "", "QueuedJob", `{"modules":[{"type":"InProgress","id":"y","job":"Y"}]}`),
		// A loop that has not started an iteration names its parent as its
		// job; a child not known yet is skipped.
		"L":  flowJob("L", "f/x/l", "QueuedJob", `{"modules":[{"type":"InProgress","id":"loop","job":"L","flow_jobs":["L0","L1"]}]}`),
		"L0": flowJob("L0", "", "CompletedJob", `{"modules":[{"type":"Success","id":"p","job":"P0"},{"type":"Success","id":"q","job":"Q0"}]}`),
		"N":  flowJob("N", "f/x/l", "QueuedJob", `{"modules":[{"type":"InProgress","id":"loop","job":"N"}]}`),
	}
	l, api := newLocator(t, tree, jobs)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, c := range []struct{ root, job, site string }{
		{"F", "H", "s:failure"},
		{"F", "A0", "m:a~0"},
		{"F", "A1", "m:a~0"},
		{"B", "Y", "m:pick/b:1/m:y"},
		{"L", "Q0", "m:loop/i:0/m:q"},
	} {
		site, err := l.Locate(ctx, c.root, c.job)
		if err != nil || site.String() != c.site {
			t.Errorf("%s in %s: %q %v, want %q", c.job, c.root, site, err, c.site)
		}
	}
	// The completed iteration is read once and then cached.
	if _, err := l.Locate(ctx, "L", "P0"); err != nil {
		t.Fatal(err)
	}
	if api.gets["L0"] != 1 {
		t.Errorf("the completed iteration was read %d times", api.gets["L0"])
	}
	// A loop whose job is its parent is not walked again (no recursion).
	short, cancel2 := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel2()
	if _, err := l.Locate(short, "N", "x"); err == nil {
		t.Fatal("found a job in an empty loop")
	}
	// A leaf iteration needs a one-module body.
	api.set("L", flowJob("L", "f/x/l", "QueuedJob", `{"modules":[{"type":"InProgress","id":"loop","flow_jobs":["LEAF"]}]}`))
	if _, err := l.Locate(ctx, "L", "LEAF"); err == nil || !strings.Contains(err.Error(), "one-module body") {
		t.Fatalf("got %v", err)
	}
	// A script run's root is the stub itself.
	if site, err := l.Locate(ctx, "S", "S"); err != nil || !site.IsRoot() {
		t.Fatalf("script run: %q %v", site, err)
	}
}

// TestCheckReplay holds answered calls against the walk.
func TestCheckReplay(t *testing.T) {
	site := func(s string) conformance.SitePath {
		p, err := conformance.ParseSitePath(s)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	w := &Walk{
		IDs: map[string]conformance.SitePath{
			"R": {}, "it": site("m:loop/i:0"), "j0": site("m:loop/i:0/m:a/r:0"), "j1": site("m:loop/i:0/m:a"), "k": site("m:b"),
		},
		Timings: map[string]JobTiming{
			"": {Kind: "flow"}, "m:loop/i:0": {Kind: "flownode"}, "m:loop/i:0/m:a/r:0": {Kind: "flowscript"}, "m:loop/i:0/m:a": {Kind: "flowscript"}, "m:b": {Kind: "script"},
		},
	}
	call := func(job, s string, attempt int) conformance.ReplayCall {
		return conformance.ReplayCall{Job: job, Site: site(s), Attempt: attempt}
	}
	good := []conformance.ReplayCall{call("j0", "m:loop/i:0/m:a", 0), call("j1", "m:loop/i:0/m:a", 1), call("k", "m:b", 0)}
	if err := checkReplay(w, good); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name  string
		calls []conformance.ReplayCall
		want  string
	}{
		{"a refused call", append(good[:2:2], conformance.ReplayCall{Job: "k", Err: `{"error":"replay_miss"}`}), "replay_miss"},
		{"the last job counted as a later attempt", []conformance.ReplayCall{good[0], call("j1", "m:loop/i:0/m:a", 2), good[2]}, "replay_site_mismatch"},
		{"a failed attempt counted wrong", []conformance.ReplayCall{call("j0", "m:loop/i:0/m:a", 1), good[1], good[2]}, "replay_site_mismatch"},
		{"another site", []conformance.ReplayCall{good[0], good[1], call("k", "m:c", 0)}, `"walked":"m:b"`},
		{"a job the walk did not see", append(good[:3:3], call("zz", "m:b", 1)), `"walked":"(not walked)"`},
		{"a script job that never called", good[:2], `{"error":"stub_not_called","sites":["m:b"]}`},
	} {
		if err := checkReplay(w, c.calls); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", c.name, err, c.want)
		}
	}
}
