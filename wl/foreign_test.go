package wl

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/invakid404/windmill-lite/internal/bundle"
	"github.com/invakid404/windmill-lite/internal/harness"
)

// TestForeignWindmillOrigins checks Options.ForeignWindmillOrigins as the
// welcome carries it, through host mode's handshake: each origin in one
// form (scheme and host in lower case, no default port, no trailing slash),
// each once, in the order given; none without the option, so that the
// welcome is today's. Open refuses every value that is not an origin which
// the harness's URL parser accepts and reads the same way, such as an IPv4
// address it would refuse, and refuses a host with engine_refused for it.
// No refusal holds the value, which can carry a secret: the marker in the
// refused values appears in no error and no handshake refusal.
func TestForeignWindmillOrigins(t *testing.T) {
	dir := writeTestBundle(t, "test-engine", nil, nil)
	work := t.TempDir()
	// The origins in the forms of the cases' also_listed: duplicates, case,
	// default ports, trailing slashes and colons, an underscore, a leading
	// hyphen, IPv6.
	_, err, _, w := openHost(t, dir, Options{Host: &Host{Workdir: work}, ForeignWindmillOrigins: readForeignCases(t).AlsoListed}, work)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"https://windmill.example.com", "http://127.0.0.1:8000", "http://[::1]", "http://[::1]:8080",
		"https://a_b.example", "https://-a.example:8443", "http://1.2.3.4", "https://[::ffff:1.2.3.4]", "https://localhost:0",
	}
	if w.Config == nil || !reflect.DeepEqual(w.Config.ForeignWindmillOrigins, want) {
		t.Fatalf("welcome %+v, want the foreign origins %q", w.Config, want)
	}
	_, err, _, w = openHost(t, dir, Options{Host: &Host{Workdir: work}}, work)
	if err != nil {
		t.Fatal(err)
	}
	if w.Config == nil || w.Config.ForeignWindmillOrigins != nil {
		t.Fatalf("welcome without the option %+v, want no foreign origins", w.Config)
	}
	const marker = "s3cr3t-marker"
	for _, bad := range []string{
		"", "windmill.example.com", "ftp://windmill.example.com", "https://", "https://:443",
		"https://windmill.example.com/api", "https://windmill.example.com/?x=1", "https://windmill.example.com/#top",
		"https://user@windmill.example.com", "https://windmill.example.com:65536", "https://windmill.example.com/?",
		"https://windmill.example.com/#", "https://windmill.example.com#",
		// The URL parser refuses these hosts, or reads them as another.
		"http://999.999.999.999", "https://1.2.3", "https://01.2.3.4", "https://0x7f.0.0.1", "https://a.1", "https://a.0x1",
		"https://xn--a.example", "https://xn--bcher-kva.example", "https://Bücher.example", "https://[fe80::1%25eth0]",
		"https://a..b", "https://example.com.", "https://.example", "https://a!b.example", "https://a%2eb.example", "https://a b.example",
		// Secrets in a refused value.
		"https://user:" + marker + "@windmill.example.com", "https://windmill.example.com/?token=" + marker,
		"https://windmill.example.com/#" + marker, "https://windmill.example.com/" + marker, "https://" + marker + ".1",
	} {
		_, err := Open(dir, Options{EngineVersion: "test-engine", ForeignWindmillOrigins: []string{"https://ok.example.com", bad}})
		if err == nil || !strings.Contains(err.Error(), "ForeignWindmillOrigins[1]") {
			t.Errorf("%q: Open returned %v, want a refusal that names the option and the value's position", bad, err)
		} else if strings.Contains(err.Error(), marker) {
			t.Errorf("%q: the refusal %q holds the value", bad, err)
		}
	}
	_, err, _, w = openHost(t, dir, Options{Host: &Host{Workdir: work}, ForeignWindmillOrigins: []string{"https://user:" + marker + "@windmill.example.com"}}, work)
	if err == nil || w.Reject == nil || w.Reject.Code != harness.RejectEngine || !strings.Contains(w.Reject.Message, "ForeignWindmillOrigins[0]") {
		t.Fatalf("a host with user information in an origin: %v, welcome %+v; want engine_refused naming the option", err, w)
	}
	if strings.Contains(w.Reject.Message, marker) || strings.Contains(err.Error(), marker) {
		t.Fatalf("the host's refusal %q or Open's error holds the value", w.Reject.Message)
	}
}

// foreignCases is testdata/foreign/cases.json, whose note says what each
// field means.
type foreignCases struct {
	DispatchID  string   `json:"dispatch_id"`
	FlowResult  string   `json:"flow_result"`
	OtherParent string   `json:"other_parent"`
	AlsoListed  []string `json:"also_listed"`
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

func readForeignCases(t *testing.T) foreignCases {
	t.Helper()
	var c foreignCases
	b, err := os.ReadFile(filepath.Join("testdata", "foreign", "cases.json"))
	if err == nil {
		err = json.Unmarshal(b, &c)
	}
	if err != nil || len(c.Cases) == 0 {
		t.Fatalf("testdata/foreign/cases.json: %v", err)
	}
	return c
}

// standIn is a loopback HTTP server that stands in for a real Windmill, as
// cases.json's note says, and records every request it serves.
type standIn struct {
	*httptest.Server
	mu   sync.Mutex
	seen []seenRequest
}

// seenRequest is a request as the stand-in received it.
type seenRequest struct {
	Method, Path, Body string
	Query              url.Values
}

func newStandIn(t *testing.T, c foreignCases) *standIn {
	s := &standIn{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.seen = append(s.seen, seenRequest{Method: r.Method, Path: r.URL.Path, Body: string(body), Query: r.URL.Query()})
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
func (s *standIn) take() []seenRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := s.seen
	s.seen = nil
	return seen
}

// windmillClientOrSkip returns WL_WINDMILL_CLIENT, a directory whose
// node_modules holds the stock windmill-client@1.862.0; without it the test
// is skipped, unless WL_REQUIRE_BUN is set, as CI sets it.
func windmillClientOrSkip(t *testing.T) string {
	t.Helper()
	client := os.Getenv("WL_WINDMILL_CLIENT")
	if client == "" {
		if os.Getenv("WL_REQUIRE_BUN") != "" {
			t.Fatal("WL_REQUIRE_BUN is set but WL_WINDMILL_CLIENT is not; it names a directory whose node_modules holds windmill-client@1.862.0")
		}
		t.Skip("WL_WINDMILL_CLIENT is not set")
	}
	var pkg struct{ Version string }
	b, err := os.ReadFile(filepath.Join(client, "node_modules", "windmill-client", "package.json"))
	if err == nil {
		err = json.Unmarshal(b, &pkg)
	}
	if err != nil || pkg.Version != "1.862.0" {
		t.Fatalf("WL_WINDMILL_CLIENT must hold windmill-client 1.862.0, not %q: %v", pkg.Version, err)
	}
	return client
}

// buildForeignBundle builds testdata/foreign/windmill: the flow
// f/foreign/client, whose step runs f/foreign/cb/call on its base and call
// inputs, and the flow f/foreign/native, whose step runs the native
// f/foreign/fetch on its base input. f/foreign/cb is a codebase, so the
// build inlines the stock client from its node_modules, a copy of
// WL_WINDMILL_CLIENT's; it is not the folder's, from which the native
// step's embedded client would resolve too.
func buildForeignBundle(t *testing.T, bun, client string) (out, runtimeDir string) {
	t.Helper()
	repo, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(t.TempDir(), "windmill")
	if err := os.CopyFS(folder, os.DirFS(filepath.Join("testdata", "foreign", "windmill"))); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(filepath.Join(folder, "f", "foreign", "cb", "node_modules"), os.DirFS(filepath.Join(client, "node_modules"))); err != nil {
		t.Fatal(err)
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

// TestForeignWindmillLinks is the acceptance test of
// Options.ForeignWindmillOrigins, on the pinned Bun 1.4.3 with the stock
// windmill-client@1.862.0, in warm and fresh mode; host mode's is
// TestHostForeignWindmillLinks in cmd/windmill-lite. Two loopback servers
// stand in for real Windmill instances, and the option lists one of them,
// in a form that the runtime normalises: in upper case, with a trailing
// slash. Each case of testdata/foreign/cases.json runs a flow whose step
// calls one of them, and checks the request that the server saw:
//
//   - With the option, a dispatch to the listed origin under
//     /api/w/{workspace}/jobs/run/ carries neither of the step's own job
//     ids, from the stock client (runScriptAsync, runFlow) or from a native
//     step's fetch. runFlowAsync, which sends none by default, is
//     unchanged. A parent that the step names itself stays, while its own
//     root goes, and so does a parent that is the top of its run in a
//     subflow, its WM_ROOT_JOB_ID. A request to the origin that is not
//     listed, and one to the listed origin outside jobs/run/, keep both.
//   - Without the option, the control, every request keeps what the step
//     sent.
//
// The rest of each request is the step's: its method, path, body and other
// query parameters, and the step reads the server's answer.
func TestForeignWindmillLinks(t *testing.T) {
	bun := bunOrSkip(t)
	client := windmillClientOrSkip(t)
	cases := readForeignCases(t)
	out, runtimeDir := buildForeignBundle(t, bun, client)
	listed, other := newStandIn(t, cases), newStandIn(t, cases)
	for _, mode := range []string{ModuleStateWarm, ModuleStateFresh} {
		for _, on := range []bool{true, false} {
			name := mode + "/without the option"
			opts := Options{Bun: bun, HarnessRuntime: runtimeDir, ModuleState: mode}
			if on {
				name = mode + "/with the option"
				// Each origin that Open accepts must be one that the
				// harness's URL parser accepts: one it refused would stop
				// every harness process at its welcome.
				opts.ForeignWindmillOrigins = append([]string{strings.ToUpper(listed.URL) + "/"}, cases.AlsoListed...)
			}
			t.Run(name, func(t *testing.T) {
				rt, err := Open(out, opts)
				if err != nil {
					t.Fatal(err)
				}
				defer rt.Close()
				for i, c := range cases.Cases {
					t.Run(c.Name, func(t *testing.T) {
						server := other
						if c.Listed {
							server = listed
						}
						listed.take()
						other.take()
						args, _ := json.Marshal(map[string]string{"base": server.URL, "call": c.Call})
						res, err := rt.Run(context.Background(), c.Flow, args, RunOptions{})
						if err != nil {
							t.Fatal(err)
						}
						if res.Status != StatusSuccess {
							t.Fatalf("the run ended %s: %s", res.Status, res.Result)
						}
						checkForeignCase(t, cases, i, on, res.Result, server.take())
					})
				}
			})
		}
	}
}

// checkForeignCase checks the result of case i's step, and the requests
// that its server saw, against the case.
func checkForeignCase(t *testing.T, cases foreignCases, i int, on bool, result json.RawMessage, seen []seenRequest) {
	t.Helper()
	c := cases.Cases[i]
	var step struct {
		Job, Root, Top string
		Out            json.RawMessage
	}
	if err := json.Unmarshal(result, &step); err != nil || step.Job == "" || step.Root == "" {
		t.Fatalf("the step's result %s: %v", result, err)
	}
	if ok, err := jsonEqual(step.Out, c.Out); err != nil || !ok {
		t.Errorf("the step read %s, want %s", step.Out, c.Out)
	}
	if len(seen) != c.Requests {
		t.Fatalf("the server saw %d requests, want %d: %+v", len(seen), c.Requests, seen)
	}
	r := seen[0]
	if r.Method != c.Request.Method || r.Path != c.Request.Path || r.Body != c.Request.Body {
		t.Errorf("the request %s %s %q, want %+v", r.Method, r.Path, r.Body, c.Request)
	}
	for k, v := range c.Params {
		if got := r.Query.Get(k); got != v {
			t.Errorf("%s %q, want %q: the other parameters stay", k, got, v)
		}
	}
	want := c.Sent
	if on {
		want = c.Got
	}
	value := map[string]string{"job": step.Job, "top": step.Top, "root": step.Root, "other": cases.OtherParent, "": ""}
	if slices.Contains(c.Sent[:], "top") && (step.Top == "" || step.Top == step.Root || step.Top == step.Job) {
		t.Fatalf("the top of the step's run %q is empty, its root or its job: the case shows nothing", step.Top)
	}
	for j, name := range []string{"parent_job", "root_job"} {
		got, present := r.Query.Get(name), r.Query.Has(name)
		if w := value[want[j]]; got != w || present != (w != "") {
			t.Errorf("%s %q (present %t), want %q (%q)", name, got, present, w, want[j])
		}
	}
}
