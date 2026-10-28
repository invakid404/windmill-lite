package oracle

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/invakid404/windmill-lite/internal/conformance"
)

// apiRecorder answers the three create endpoints and records each request.
type apiRecorder struct {
	mu   sync.Mutex
	reqs []seenRequest
}

func (a *apiRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	a.mu.Lock()
	a.reqs = append(a.reqs, seenRequest{method: r.Method, path: r.URL.Path, body: body})
	a.mu.Unlock()
	switch {
	case strings.HasSuffix(r.URL.Path, "/scripts/create"):
		var s NewScript
		_ = json.Unmarshal(body, &s)
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, map[string]string{"f/t/s": "00000000000000a1", "f/t/u": "00000000000000b2"}[s.Path])
	default:
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, "ok")
	}
}

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// apiTree is a tree of two scripts and a flow whose loop leaves out
// skip_failures and parallel, whose inline step is an !inline file, and
// whose second loop body pins script s to its deployed hash.
var apiTree = map[string]string{
	"wmill.yaml":           "defaultTs: bun\n",
	"f/t/folder.meta.yaml": "summary: ''\n",
	"f/t/s.ts":             "export async function main(v: any) { return v; }\n",
	"f/t/s.script.yaml":    "summary: pinned\ndescription: ''\ncache_ttl: 60\n",
	"f/t/u.ts":             "export async function main() { return 1; }\n",
	"f/t/x.flow/b.ts":      "export async function main() { return 2; }\n",
	"f/t/x.flow/flow.yaml": `summary: x
value:
  modules:
    - id: l
      value:
        type: forloopflow
        iterator: {type: static, value: [1, 2]}
        modules:
          - id: b
            value: {type: rawscript, content: '!inline b.ts', input_transforms: {}, language: bun}
    - id: m
      value:
        type: forloopflow
        iterator: {type: static, value: [1]}
        modules:
          - id: c
            value: {type: script, path: f/t/s, hash: '0000000000000000', input_transforms: {v: {type: static, value: '!inline b.ts'}}}
schema: {type: object, properties: {}}
`,
}

// TestDeployAPI deploys a tree through the API: folders, then scripts
// (with their metadata), then flows, sent as written. An absent container
// field stays absent, an !inline file is read, a pinned hash is resolved,
// and a static value that reads "!inline" is left alone.
func TestDeployAPI(t *testing.T) {
	// spec: CF-6/a, FX-40/d, OR-6
	rec := &apiRecorder{}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	d, err := DeployAPI(context.Background(), &Client{BaseURL: srv.URL}, "ws", writeTree(t, apiTree))
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, r := range rec.reqs {
		paths = append(paths, r.method+" "+r.path)
	}
	want := []string{
		"POST /api/w/ws/folders/create",
		"POST /api/w/ws/scripts/create",
		"POST /api/w/ws/scripts/create",
		"POST /api/w/ws/flows/create",
	}
	if strings.Join(paths, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests:\n%s", strings.Join(paths, "\n"))
	}
	if string(rec.reqs[0].body) != `{"name":"t"}` {
		t.Errorf("folder body %s", rec.reqs[0].body)
	}
	var s NewScript
	if err := json.Unmarshal(rec.reqs[1].body, &s); err != nil {
		t.Fatal(err)
	}
	if s.Path != "f/t/s" || s.Language != "bun" || s.Summary != "pinned" || s.CacheTTL == nil || *s.CacheTTL != 60 || !strings.Contains(s.Content, "return v") {
		t.Errorf("script body %s", rec.reqs[1].body)
	}
	if !strings.Contains(string(rec.reqs[2].body), `"path":"f/t/u"`) || strings.Contains(string(rec.reqs[2].body), "cache_ttl") {
		t.Errorf("script without metadata %s", rec.reqs[2].body)
	}
	var flow struct {
		Path    string `json:"path"`
		Summary string `json:"summary"`
		Value   struct {
			Modules []struct {
				Value map[string]json.RawMessage `json:"value"`
			} `json:"modules"`
		} `json:"value"`
		Schema json.RawMessage `json:"schema"`
	}
	if err := json.Unmarshal(rec.reqs[3].body, &flow); err != nil {
		t.Fatal(err)
	}
	if flow.Path != "f/t/x" || flow.Summary != "x" || flow.Schema == nil || len(flow.Value.Modules) != 2 {
		t.Fatalf("flow body %s", rec.reqs[3].body)
	}
	loop := flow.Value.Modules[0].Value
	if _, ok := loop["skip_failures"]; ok {
		t.Errorf("skip_failures was filled in: %s", rec.reqs[3].body)
	}
	if _, ok := loop["parallel"]; ok {
		t.Errorf("parallel was filled in: %s", rec.reqs[3].body)
	}
	body := string(rec.reqs[3].body)
	for _, part := range []string{
		`"content":"export async function main() { return 2; }\n"`,
		`"hash":"00000000000000a1"`,
		`"value":"!inline b.ts"`,
	} {
		if !strings.Contains(body, part) {
			t.Errorf("flow body lacks %s: %s", part, body)
		}
	}
	if d.Scripts["f/t/s"] != "00000000000000a1" || len(d.Folders) != 1 || !strings.Contains(string(d.Flows["f/t/x"]), `"hash":"00000000000000a1"`) || strings.Contains(string(d.Flows["f/t/x"]), `"path":"f/t/x"`) {
		t.Errorf("deployment %+v", d)
	}
}

// TestDeployAPIRefusals: the tree is read narrowly, and a refusal comes
// before anything is created when the tree itself is unsupported.
func TestDeployAPIRefusals(t *testing.T) {
	for name, c := range map[string]struct {
		edit       map[string]string
		drop       string
		contains   string
		noRequests bool
	}{
		"a Python script":           {edit: map[string]string{"f/t/p.py": "def main(): pass\n"}, contains: "not a Bun script", noRequests: true},
		"a script outside f/":       {edit: map[string]string{"u/admin/s.ts": "x"}, contains: "not under f/", noRequests: true},
		"a nested directory":        {edit: map[string]string{"f/t/sub/s.ts": "x"}, contains: "neither a folder nor a flow", noRequests: true},
		"unknown script metadata":   {edit: map[string]string{"f/t/s.script.yaml": "summary: s\nlock: x\n"}, contains: "unknown field"},
		"a pin to an absent script": {edit: map[string]string{"f/t/s.ts": ""}, drop: "f/t/s.ts", contains: "which the tree does not deploy"},
		"a missing inline file":     {drop: "f/t/x.flow/b.ts", contains: "b.ts"},
		"an inline path":            {edit: map[string]string{"f/t/x.flow/flow.yaml": strings.Replace(apiTree["f/t/x.flow/flow.yaml"], "'!inline b.ts', input", "'!inline ../u.ts', input", 1)}, contains: "not a file of the flow's directory"},
	} {
		t.Run(name, func(t *testing.T) {
			files := map[string]string{}
			for k, v := range apiTree {
				files[k] = v
			}
			for k, v := range c.edit {
				files[k] = v
			}
			if c.drop != "" {
				delete(files, c.drop)
				delete(files, strings.TrimSuffix(c.drop, ".ts")+".script.yaml")
			}
			rec := &apiRecorder{}
			srv := httptest.NewServer(rec)
			defer srv.Close()
			_, err := DeployAPI(context.Background(), &Client{BaseURL: srv.URL}, "ws", writeTree(t, files))
			if err == nil || !strings.Contains(err.Error(), c.contains) {
				t.Fatalf("got %v, want an error containing %q", err, c.contains)
			}
			if c.noRequests && len(rec.reqs) != 0 {
				t.Errorf("%d requests before the refusal", len(rec.reqs))
			}
		})
	}
}

// TestSessionDeploysThroughTheAPI records the smoke fixture with oracle
// deploy api: the CLI pushes nothing, the tree's items are created through
// the API with a pinned hash resolved, the same gate runs, and each
// recording's provenance holds the target flow's submitted value, the value
// the API answers after the gate, and generate-metadata's output from a
// copy of the tree.
func TestSessionDeploysThroughTheAPI(t *testing.T) {
	// spec: CF-6/a, FX-40/d, OR-9, OR-15
	rig := newRig(t)
	tree := filepath.Join(rig.fixture, "windmill")
	for _, p := range []string{"wmill-lock.yaml", "f/wl_smoke/double.script.lock"} {
		if err := os.Remove(filepath.Join(tree, filepath.FromSlash(p))); err != nil {
			t.Fatal(err)
		}
	}
	edits := map[string][2]string{
		"fixture.yaml": {"oracle: {workers: 1}", "oracle: {workers: 1, deploy: api}"},
		"windmill/f/wl_smoke/two_step.flow/flow.yaml": {"        path: f/wl_smoke/double\n", "        path: f/wl_smoke/double\n        hash: '0000000000000000'\n"},
	}
	for name, e := range edits {
		p := filepath.Join(rig.fixture, filepath.FromSlash(name))
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), e[0]) {
			t.Fatalf("%s lacks %q", name, e[0])
		}
		if err := os.WriteFile(p, []byte(strings.Replace(string(data), e[0], e[1], 1)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(tree, "f/wl_smoke/double.script.yaml"), []byte("summary: double\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(tree, "f/wl_smoke/two_step.flow/flow.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	s := rig.session(20 * GiB)
	s.RegenerateMetadata = true // never applied to an API-deployed tree
	rep, err := s.Run(context.Background(), []*conformance.Fixture{rig.load(t)})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rep.Runs {
		if r.Outcome != OutcomeNew {
			t.Errorf("%s: %s %s %v", r.Run, r.Outcome, r.Error, r.Differences)
		}
	}
	log, _ := os.ReadFile(rig.cliLog)
	if strings.Contains(string(log), "sync push") || strings.Count(string(log), "generate-metadata") != 1 {
		t.Errorf("CLI calls:\n%s", log)
	}
	if after, _ := os.ReadFile(filepath.Join(tree, "f/wl_smoke/two_step.flow/flow.yaml")); string(after) != string(before) {
		t.Error("the committed tree changed")
	}
	created := rig.oracle.created
	flow := string(created["flows/create f/wl_smoke/two_step"])
	if created["folders/create wl_smoke"] == nil || created["scripts/create f/wl_smoke/double"] == nil ||
		!strings.Contains(flow, `"hash":"5a1f"`) || !strings.Contains(flow, "too big") || strings.Contains(flow, "!inline") {
		t.Fatalf("created %v", created)
	}

	f := rig.load(t)
	for _, r := range f.Runs {
		var p Provenance
		if r.Recorded == nil {
			t.Fatalf("run %s has no recording", r.Name)
		}
		if err := json.Unmarshal(r.Recorded.Provenance, &p); err != nil {
			t.Fatal(err)
		}
		d := p.Deployment
		if d == nil || d.Method != "api" {
			t.Fatalf("run %s deployment %+v", r.Name, d)
		}
		compact := func(v json.RawMessage) string {
			var b bytes.Buffer
			if err := json.Compact(&b, v); err != nil {
				t.Fatal(err)
			}
			return b.String()
		}
		sub, mat, cli := compact(d.Submitted), compact(d.Materialized), compact(d.CLIGenerated)
		if !strings.Contains(sub, `"hash":"5a1f"`) || !strings.Contains(sub, "too big") ||
			mat != `{"modules":[{"id":"a"},{"id":"b"}],"materialized":true}` ||
			!strings.Contains(cli, `"content":"!inline b.ts"`) || !strings.Contains(cli, `"hash":"0000000000000000"`) {
			t.Errorf("run %s deployment:\n submitted %s\n materialized %s\n cli %s", r.Name, sub, mat, cli)
		}
	}
}

// TestDeployAPIResolvesOnlyModules: inline files and pinned hashes are
// resolved in every position that holds a module (the module list, the
// failure and preprocessor modules, loop bodies, branches and defaults),
// and nowhere else. Data that looks like a module value stays exactly as
// written (FX-7), at any depth, in a static input, an iterator and a mock,
// even when the file or script it names does not exist.
func TestDeployAPIResolvesOnlyModules(t *testing.T) {
	// spec: FX-7, OR-15
	const (
		rawLike    = `{"type":"rawscript","content":"!inline b.ts","lock":"!inline missing.lock"}`
		scriptLike = `{"type":"script","path":"f/t/absent","hash":"0000000000000000"}`
	)
	nested := `{"deep":[` + rawLike + `,{"k":` + scriptLike + `}]}`
	flow := `summary: positions
value:
  modules:
    - id: echo
      value:
        type: rawscript
        content: '!inline b.ts'
        language: bun
        input_transforms:
          r: {type: static, value: ` + rawLike + `}
          s: {type: static, value: ` + scriptLike + `}
          n: {type: static, value: ` + nested + `}
      mock: {enabled: false, return_value: ` + rawLike + `}
      stop_after_if: {expr: "'!inline b.ts' == ''", skip_if_stopped: false}
    - id: w
      value:
        type: whileloopflow
        modules:
          - id: wb
            value: {type: rawscript, content: '!inline b.ts', input_transforms: {}, language: bun}
            stop_after_if: {expr: "true", skip_if_stopped: false}
    - id: f
      value:
        type: forloopflow
        iterator: {type: static, value: [` + rawLike + `, ` + scriptLike + `]}
        modules:
          - id: fb
            value: {type: script, path: f/t/s, hash: '0000000000000000', input_transforms: {x: {type: static, value: ` + scriptLike + `}}}
    - id: one
      value:
        type: branchone
        branches:
          - expr: "false"
            modules:
              - id: ob
                value: {type: rawscript, content: '!inline b.ts', input_transforms: {}, language: bun}
        default:
          - id: od
            value: {type: script, path: f/t/s, hash: '0000000000000000', input_transforms: {}}
    - id: all
      value:
        type: branchall
        branches:
          - modules:
              - id: ab
                value: {type: script, path: f/t/s, hash: '0000000000000000', input_transforms: {}}
  failure_module:
    id: failure
    value: {type: rawscript, content: '!inline b.ts', input_transforms: {}, language: bun}
  preprocessor_module:
    id: preprocessor
    value: {type: rawscript, content: '!inline b.ts', input_transforms: {}, language: bun}
schema: {type: object, properties: {}}
`
	dir := writeTree(t, map[string]string{
		"f/t/x.flow/flow.yaml": flow,
		"f/t/x.flow/b.ts":      "export async function main() { return 2; }\n",
	})
	_, value, err := readFlow(dir, "f/t/x", map[string]string{"f/t/s": "0123456789abcdef"})
	if err != nil {
		t.Fatal(err)
	}
	const absent = "(absent)"
	b := `"export async function main() { return 2; }\n"`
	for pointer, want := range map[string]string{
		// modules, in every position, are resolved
		"/modules/0/value/content":                            b,
		"/modules/1/value/modules/0/value/content":            b,
		"/modules/2/value/modules/0/value/hash":               `"0123456789abcdef"`,
		"/modules/3/value/branches/0/modules/0/value/content": b,
		"/modules/3/value/default/0/value/hash":               `"0123456789abcdef"`,
		"/modules/4/value/branches/0/modules/0/value/hash":    `"0123456789abcdef"`,
		"/failure_module/value/content":                       b,
		"/preprocessor_module/value/content":                  b,
		// data is sent as written
		"/modules/0/value/input_transforms/r/value":                   rawLike,
		"/modules/0/value/input_transforms/s/value":                   scriptLike,
		"/modules/0/value/input_transforms/n/value":                   nested,
		"/modules/0/mock/return_value":                                rawLike,
		"/modules/0/stop_after_if/expr":                               `"'!inline b.ts' == ''"`,
		"/modules/2/value/iterator/value/0":                           rawLike,
		"/modules/2/value/iterator/value/1":                           scriptLike,
		"/modules/2/value/modules/0/value/input_transforms/x/value":   scriptLike,
		"/modules/2/value/modules/0/value/input_transforms/x/missing": absent,
	} {
		if got := jsonAt(t, value, pointer, absent); got != canonicalJSON(t, want) {
			t.Errorf("%s is %s, want %s", pointer, got, want)
		}
	}
}

// canonicalJSON is v as jsonAt writes it.
func canonicalJSON(t *testing.T, v string) string {
	t.Helper()
	if v == "(absent)" {
		return v
	}
	var x any
	if err := json.Unmarshal([]byte(v), &x); err != nil {
		t.Fatalf("%s: %v", v, err)
	}
	b, err := json.Marshal(x)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
