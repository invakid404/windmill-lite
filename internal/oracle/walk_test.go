package oracle

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/openflow"
)

// A walk case gives a run's jobs as the API reports them and the
// normalised observation the walker and normaliser must make of it. Jobs
// are named; ${name} anywhere in the case stands for that job's id.
type walkCase struct {
	Name       string                     `json:"name"`
	Spec       []string                   `json:"spec"`
	Provenance string                     `json:"provenance"`
	Target     string                     `json:"target"`
	Script     bool                       `json:"script"`
	Flow       json.RawMessage            `json:"flow"`
	Subflows   map[string]json.RawMessage `json:"subflows"`
	Root       string                     `json:"root"`
	Jobs       map[string]fakeJob         `json:"jobs"`
	Compare    conformance.CompareSpec    `json:"compare"`
	Expect     json.RawMessage            `json:"expect"`
	Error      string                     `json:"error"`
}

// fakeJob is a job of the fake API.
type fakeJob struct {
	Kind       string          `json:"kind"`
	Success    bool            `json:"success"`
	Skipped    bool            `json:"skipped"`
	Canceled   bool            `json:"canceled"`
	FlowStatus json.RawMessage `json:"flow_status"`
	Args       json.RawMessage `json:"args"`
	Result     json.RawMessage `json:"result"`
}

func jobID(i int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1) }

// fakeAPI serves jobs_u/get, get_args and get_result_maybe for the jobs.
func fakeAPI(t *testing.T, ws string, jobs map[string]fakeJob) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prefix := "/api/w/" + ws + "/"
		p := strings.TrimPrefix(r.URL.Path, prefix)
		for _, route := range []string{"jobs_u/get/", "jobs_u/get_args/", "jobs_u/completed/get_result_maybe/"} {
			if !strings.HasPrefix(p, route) {
				continue
			}
			id := strings.TrimPrefix(p, route)
			j, ok := jobs[id]
			if !ok {
				http.Error(w, "no job "+id, http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			switch route {
			case "jobs_u/get/":
				job := map[string]any{"type": "CompletedJob", "id": id, "job_kind": j.Kind, "success": j.Success, "is_skipped": j.Skipped, "canceled": j.Canceled}
				if j.FlowStatus != nil {
					job["flow_status"] = j.FlowStatus
				}
				_ = json.NewEncoder(w).Encode(job)
			case "jobs_u/get_args/":
				args := j.Args
				if args == nil {
					args = json.RawMessage("{}")
				}
				_, _ = w.Write(args)
			default:
				res := j.Result
				if res == nil {
					res = json.RawMessage("null")
				}
				fmt.Fprintf(w, `{"completed":true,"started":true,"success":%v,"result":%s}`, j.Success, res)
			}
			return
		}
		http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
	}))
}

func decodeFlowValue(t *testing.T, raw json.RawMessage) *openflow.Flow {
	t.Helper()
	f, err := openflow.DecodeFlow([]byte(`{"value":` + string(raw) + `}`))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// TestWalkCases runs testdata/walk/cases.json.
func TestWalkCases(t *testing.T) {
	data, err := os.ReadFile("testdata/walk/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		About string            `json:"about"`
		Cases []json.RawMessage `json:"cases"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	for _, raw := range file.Cases {
		var names struct {
			Name string                     `json:"name"`
			Jobs map[string]json.RawMessage `json:"jobs"`
		}
		if err := json.Unmarshal(raw, &names); err != nil {
			t.Fatal(err)
		}
		keys := make([]string, 0, len(names.Jobs))
		for k := range names.Jobs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var pairs []string
		for i, k := range keys {
			pairs = append(pairs, "${"+k+"}", jobID(i))
		}
		text := strings.NewReplacer(pairs...).Replace(string(raw))
		var c walkCase
		dec := json.NewDecoder(strings.NewReader(text))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&c); err != nil {
			t.Fatalf("%s: %v", names.Name, err)
		}
		jobs := map[string]fakeJob{}
		for i, k := range keys {
			jobs[jobID(i)] = c.Jobs[k]
		}
		t.Run(c.Name, func(t *testing.T) {
			if len(c.Spec) == 0 || c.Provenance == "" {
				t.Fatal("case needs spec IDs and a provenance")
			}
			const ws = "wl-test-1"
			srv := fakeAPI(t, ws, jobs)
			defer srv.Close()
			tree := &SyncTree{Flows: map[string]*openflow.Flow{}}
			if c.Flow != nil {
				tree.Flows[c.Target] = decodeFlowValue(t, c.Flow)
			}
			for p, v := range c.Subflows {
				tree.Flows[p] = decodeFlowValue(t, v)
			}
			wk := &Walker{Client: &Client{BaseURL: srv.URL}, Workspace: ws, Tree: tree}
			w, err := wk.Walk(context.Background(), c.Root, c.Target, c.Script)
			if c.Error != "" {
				if err == nil || !strings.Contains(err.Error(), c.Error) {
					t.Fatalf("error %v, want one containing %q", err, c.Error)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			obs, err := Observe(w, nil, nil, c.Compare)
			if err != nil {
				t.Fatal(err)
			}
			var want conformance.Observation
			if err := json.Unmarshal(c.Expect, &want); err != nil {
				t.Fatal(err)
			}
			diffs, err := conformance.Compare(obs, want, conformance.CompareOptions{StrictKeyOrder: true})
			if err != nil {
				t.Fatal(err)
			}
			if len(diffs) > 0 {
				got, _ := json.MarshalIndent(obs, "", "  ")
				t.Fatalf("differences %v\nobservation:\n%s", diffs, got)
			}
			// Every walked job of the case is mapped to a site.
			for id := range jobs {
				if _, ok := w.IDs[id]; !ok {
					t.Errorf("job %s was not walked", id)
				}
			}
		})
	}
}
