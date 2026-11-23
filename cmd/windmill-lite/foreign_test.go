package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/invakid404/windmill-lite/internal/bundle"
)

// foreignDir is the synthetic fixture of the foreign Windmill origins, which
// wl's TestForeignWindmillLinks runs in warm and fresh mode: its sync
// folder, windmill/, and its cases, cases.json, whose note says what each
// field means.
const foreignDir = "../../wl/testdata/foreign"

type foreignCases struct {
	DispatchID  string `json:"dispatch_id"`
	FlowResult  string `json:"flow_result"`
	OtherParent string `json:"other_parent"`
	Cases       []struct {
		Name, Flow, Call string
		Listed           bool
		Sent, Got        [2]string
		Request          struct{ Method, Path, Body string }
		Requests         int
		Params           map[string]string
		Out              json.RawMessage
	}
}

// foreignStandIn is a loopback HTTP server that stands in for a real
// Windmill, as cases.json's note says, and records every request it
// serves.
type foreignStandIn struct {
	*httptest.Server
	mu   sync.Mutex
	seen []foreignRequest
}

type foreignRequest struct {
	Method, Path, Body string
	Query              url.Values
}

func newForeignStandIn(t *testing.T, c foreignCases) *foreignStandIn {
	s := &foreignStandIn{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.seen = append(s.seen, foreignRequest{Method: r.Method, Path: r.URL.Path, Body: string(body), Query: r.URL.Query()})
		s.mu.Unlock()
		switch {
		case strings.Contains(r.URL.Path, "/jobs/run/"):
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, c.DispatchID)
		case strings.Contains(r.URL.Path, "/jobs_u/completed/get_result_maybe/"):
			res, _ := json.Marshal(c.FlowResult)
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"started":true,"completed":true,"success":true,"result":`+string(res)+`}`)
		default:
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `[]`)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

// take returns the requests seen since the last take, and forgets them.
func (s *foreignStandIn) take() []foreignRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := s.seen
	s.seen = nil
	return seen
}

// TestHostForeignWindmillLinks is the host-mode acceptance test of the
// foreign Windmill origins, on the real topology: the compiled test host
// connects with the SDK's foreignWindmillOrigins, which reach the engine as
// -foreign-windmill flags and the host's runtime in the welcome, and its
// steps, which run in the host, call two loopback servers that stand in for
// real Windmill instances. The option lists one of them, in upper case
// and with a trailing slash. Each case of the fixture's cases.json runs
// with the option, where a dispatch to the listed origin under
// /api/w/{workspace}/jobs/run/ loses the step's own job ids, and without
// it, the control, where every request keeps what the step sent; the rest
// of each request is the step's. It needs WL_BUN and WL_WINDMILL_CLIENT,
// as wl's TestForeignWindmillLinks does.
func TestHostForeignWindmillLinks(t *testing.T) {
	host := compiledHost(t)
	client := os.Getenv("WL_WINDMILL_CLIENT")
	if client == "" {
		if os.Getenv("WL_REQUIRE_BUN") != "" {
			t.Fatal("WL_REQUIRE_BUN is set but WL_WINDMILL_CLIENT is not; it names a directory whose node_modules holds windmill-client@1.862.0")
		}
		t.Skip("WL_WINDMILL_CLIENT is not set")
	}
	var cases foreignCases
	b, err := os.ReadFile(filepath.Join(foreignDir, "cases.json"))
	if err == nil {
		err = json.Unmarshal(b, &cases)
	}
	if err != nil || len(cases.Cases) == 0 {
		t.Fatalf("%s/cases.json: %v", foreignDir, err)
	}
	bundlePath := buildForeignHostBundle(t, os.Getenv("WL_BUN"), client)
	listed, other := newForeignStandIn(t, cases), newForeignStandIn(t, cases)
	for _, on := range []bool{true, false} {
		name, env := "without the option", "WL_FOREIGN_WINDMILL="
		if on {
			name, env = "with the option", "WL_FOREIGN_WINDMILL="+strings.ToUpper(listed.URL)+"/"
		}
		t.Run(name, func(t *testing.T) {
			s := superviseHost(t, []string{host}, bundlePath, 30000, env)
			for _, c := range cases.Cases {
				t.Run(c.Name, func(t *testing.T) {
					server := other
					if c.Listed {
						server = listed
					}
					listed.take()
					other.take()
					r, err := s.run(c.Flow, "", map[string]string{"base": server.URL, "call": c.Call}, nil)
					if err != nil || r.Status != "success" {
						t.Fatalf("%+v, %v\n%s", r, err, s.gen(0).log())
					}
					var step struct {
						Job, Root, Top string
						Out            json.RawMessage
					}
					if err := json.Unmarshal(r.Result, &step); err != nil || step.Job == "" || step.Root == "" {
						t.Fatalf("the step's result %s: %v", r.Result, err)
					}
					var out, want any
					if json.Unmarshal(step.Out, &out) != nil || json.Unmarshal(c.Out, &want) != nil || !jsonValuesEqual(out, want) {
						t.Errorf("the step read %s, want %s", step.Out, c.Out)
					}
					seen := server.take()
					if len(seen) != c.Requests {
						t.Fatalf("the server saw %d requests, want %d: %+v", len(seen), c.Requests, seen)
					}
					q := seen[0]
					if q.Method != c.Request.Method || q.Path != c.Request.Path || q.Body != c.Request.Body {
						t.Errorf("the request %s %s %q, want %+v", q.Method, q.Path, q.Body, c.Request)
					}
					for k, v := range c.Params {
						if got := q.Query.Get(k); got != v {
							t.Errorf("%s %q, want %q: the other parameters stay", k, got, v)
						}
					}
					links := c.Sent
					if on {
						links = c.Got
					}
					value := map[string]string{"job": step.Job, "top": step.Top, "root": step.Root, "other": cases.OtherParent, "": ""}
					if slices.Contains(c.Sent[:], "top") && (step.Top == "" || step.Top == step.Root || step.Top == step.Job) {
						t.Fatalf("the top of the step's run %q is empty, its root or its job: the case shows nothing", step.Top)
					}
					for j, name := range []string{"parent_job", "root_job"} {
						got, present := q.Query.Get(name), q.Query.Has(name)
						if w := value[links[j]]; got != w || present != (w != "") {
							t.Errorf("%s %q (present %t), want %q (%q)", name, got, present, w, links[j])
						}
					}
				})
			}
		})
	}
}

// jsonValuesEqual compares two decoded JSON values.
func jsonValuesEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// buildForeignHostBundle builds the fixture's sync folder for this binary's
// engine version, with the stock client copied into its codebase's
// node_modules, from which the build inlines it.
func buildForeignHostBundle(t *testing.T, bun, client string) string {
	t.Helper()
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(t.TempDir(), "windmill")
	if err := os.CopyFS(folder, os.DirFS(filepath.Join(foreignDir, "windmill"))); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(filepath.Join(folder, "f", "foreign", "cb", "node_modules"), os.DirFS(filepath.Join(client, "node_modules"))); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "bundle")
	if _, err := bundle.Build(context.Background(), bundle.Config{
		Folder:         folder,
		EngineVersion:  resolveVersion(),
		Bun:            bun,
		Helper:         filepath.Join(repo, "harnesses", "bun", "build", "main.ts"),
		HarnessRuntime: filepath.Join(repo, "harnesses", "bun", "runtime"),
		Out:            out,
		Work:           filepath.Join(t.TempDir(), "work"),
	}); err != nil {
		t.Fatal(err)
	}
	return out
}
