package bundle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The build cases live in conformance/build as data files, one per finding
// (or per deviation, for windmill-lite's own mechanisms). Each case cites
// its catalogue case (docs/semantics/conformance.md), or only its finding
// for a case that extends the catalogue, and gives a sync folder, the
// script to build and what the build must record: signatures, exports,
// annotations, the bundler target, the workflow-as-code markers, the
// workspace imports, the problem it reports. A case may also run the built
// modules on the pinned Bun and check what their exports return, which is
// how the import transforms, the env rewrite and the shims are checked
// against real behaviour.

const (
	casesDir      = "../../conformance/build"
	catalogueFile = "../../docs/semantics/conformance.md"
)

type caseFile struct {
	Finding string      `json:"finding"`
	Scope   string      `json:"scope"`
	Note    string      `json:"note"`
	Cases   []buildCase `json:"cases"`
}

type entryGiven struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Path   string `json:"path"`
	Inline bool   `json:"inline"`
	Flow   *struct {
		Path         string `json:"path"`
		Module       string `json:"module"`
		ExplicitPath string `json:"explicit_path"`
	} `json:"flow"`
	Dependencies map[string]string `json:"dependencies"`
}

type buildCase struct {
	Spec       string `json:"spec"`
	Variant    string `json:"variant"`
	Provenance string `json:"provenance"`
	Scope      string `json:"scope"`
	Title      string `json:"title"`
	Part       string `json:"part"`
	RestIn     string `json:"rest_in"`
	Policy     string `json:"policy"`
	Deviation  string `json:"deviation"`
	Note       string `json:"note"`
	Given      struct {
		entryGiven
		Entries     []entryGiven                 `json:"entries"`
		Files       map[string]string            `json:"files"`
		Packages    map[string]map[string]string `json:"packages"`
		HostModules []struct {
			ID           string            `json:"id"`
			Key          string            `json:"key"`
			File         string            `json:"file"`
			Package      string            `json:"package"`
			Dependencies map[string]string `json:"dependencies"`
		} `json:"host_modules"`
	} `json:"given"`
	Expect struct {
		entryExpect
		Entries  map[string]entryExpect `json:"entries"`
		Provided map[string]struct {
			Exports []string     `json:"exports"`
			Imports []string     `json:"host_modules"`
			Problem *problemWant `json:"problem"`
		} `json:"provided"`
		HostModuleOrder []string     `json:"host_module_order"`
		OrderProblem    *problemWant `json:"order_problem"`
		Warnings        []string     `json:"warnings"`
		Run             *struct {
			Env      map[string]string `json:"env"`
			Register []string          `json:"register"`
			Calls    []struct {
				Entry  string          `json:"entry"`
				Export string          `json:"export"`
				Args   []any           `json:"args"`
				Result json.RawMessage `json:"result"`
				Error  string          `json:"error"`
			} `json:"calls"`
		} `json:"run"`
	} `json:"expect"`
}

type entryExpect struct {
	Problem          *problemWant             `json:"problem"`
	Signatures       map[string]signatureWant `json:"signatures"`
	Exports          []string                 `json:"exports"`
	Annotations      []string                 `json:"annotations"`
	Target           string                   `json:"target"`
	WorkflowAsCode   *WorkflowAsCode          `json:"workflow_as_code"`
	Imports          []string                 `json:"imports"`
	HostModules      []string                 `json:"host_modules"`
	BundleContains   []string                 `json:"bundle_contains"`
	BundleNotContain []string                 `json:"bundle_not_contains"`
	FirstLine        string                   `json:"first_line"`
}

type problemWant struct {
	Kind     string   `json:"kind"`
	Contains []string `json:"contains"`
}

type signatureWant struct {
	Params        []Param `json:"params"`
	ErrorContains string  `json:"error_contains"`
}

type deferredFile struct {
	Note     string `json:"note"`
	Deferred []struct {
		Spec       string `json:"spec"`
		Reason     string `json:"reason"`
		ExpectedIn string `json:"expected_in"`
	} `json:"deferred"`
}

var (
	specPattern    = regexp.MustCompile(`^((?:WR|WA)-[0-9V]+|D[0-9]+)(/[a-z])?$`)
	catalogueEntry = regexp.MustCompile(`(?m)^- \*\*((?:WR|WA)-[0-9V]+/[a-z])\*\* · ([a-z-]+)`)
)

func readStrict(t *testing.T, path string, out any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func loadCaseFiles(t *testing.T) []caseFile {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(casesDir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	var files []caseFile
	for _, p := range paths {
		if base := filepath.Base(p); base == "deferred.json" || base == "annotations.json" {
			continue
		}
		var f caseFile
		readStrict(t, p, &f)
		if want := strings.ToLower(f.Finding) + ".json"; filepath.Base(p) != want {
			t.Fatalf("%s holds finding %s; want the file %s", p, f.Finding, want)
		}
		for _, c := range f.Cases {
			m := specPattern.FindStringSubmatch(c.Spec)
			if m == nil || m[1] != f.Finding {
				t.Fatalf("%s: case %s does not belong to %s", p, c.Spec, f.Finding)
			}
			switch c.Provenance {
			case "source-derived", "disputed", "policy":
			default:
				t.Fatalf("%s: case %s has provenance %q", p, c.Spec, c.Provenance)
			}
			if c.Title == "" {
				t.Fatalf("%s: case %s has no title", p, c.Spec)
			}
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		t.Fatalf("no case files in %s", casesDir)
	}
	return files
}

func TestConformanceBuild(t *testing.T) {
	bun := bunPath(t)
	helper := repoPath(t, "harnesses/bun/build/main.ts")
	n := 0
	for _, f := range loadCaseFiles(t) {
		for _, c := range f.Cases {
			name := c.Spec
			if c.Variant != "" {
				name += "_" + c.Variant
			}
			t.Run(strings.NewReplacer("/", "_", " ", "_").Replace(name), func(t *testing.T) {
				runBuildCase(t, bun, helper, c)
			})
			n++
		}
	}
	t.Logf("build cases: %d", n)
}

func runBuildCase(t *testing.T, bun, helper string, c buildCase) {
	entries := c.Given.Entries
	if c.Given.Source != "" || c.Given.Flow != nil {
		main := c.Given.entryGiven
		if main.ID == "" {
			main.ID = "main"
		}
		entries = append([]entryGiven{main}, entries...)
	}
	files := map[string]string{}
	for p, text := range c.Given.Files {
		files[p] = text
	}
	var reg *registry
	if len(c.Given.Packages) > 0 {
		reg = newRegistry(t, c.Given.Packages)
	}
	lockOf := func(deps map[string]string) string {
		if len(deps) == 0 {
			return ""
		}
		if reg == nil {
			t.Fatal("a case with dependencies needs packages")
		}
		return reg.lock(t, bun, deps)
	}
	var scripts []BunScript
	for _, e := range entries {
		s := BunScript{ID: e.ID, Path: e.Path, Lock: lockOf(e.Dependencies)}
		switch {
		case e.Flow != nil:
			s.Path = ImportBase(e.Flow.Path, e.Flow.Module, e.Flow.ExplicitPath)
			s.Content = e.Source
		case e.Inline:
			s.Content = e.Source
		default:
			if s.Path == "" {
				s.Path = "f/case/" + e.ID
			}
			s.File = s.Path + ".ts"
			files[s.File] = e.Source
		}
		scripts = append(scripts, s)
	}
	var hosts []HostModule
	for _, h := range c.Given.HostModules {
		m := HostModule{ID: h.ID, Key: h.Key, Package: h.Package, Lock: lockOf(h.Dependencies)}
		if h.File != "" {
			m.File, m.Path = h.File, strings.TrimSuffix(h.File, ".ts")
		}
		hosts = append(hosts, m)
	}
	out := t.TempDir()
	cfg := BunConfig{
		Bun: bun, Helper: helper,
		Folder: writeFolder(t, files), Out: out, Work: t.TempDir(),
		Scripts: scriptsOf(files), Entries: scripts, HostModules: hosts,
	}
	if reg != nil {
		cfg.Env = reg.env
	}
	res, err := BuildBun(context.Background(), cfg)
	var be *BuildError
	if err != nil && !errors.As(err, &be) {
		t.Fatalf("BuildBun: %v", err)
	}
	problems := map[string]Problem{}
	if be != nil {
		for _, p := range be.Problems {
			problems[p.Item] = p.Problem
		}
	}

	want := c.Expect.Entries
	if want == nil {
		want = map[string]entryExpect{}
	}
	if !reflect.ValueOf(c.Expect.entryExpect).IsZero() {
		want["main"] = c.Expect.entryExpect
	}
	for id, w := range want {
		checkEntry(t, id, w, res, problems["script "+id], out)
	}
	for _, s := range scripts {
		if _, ok := want[s.ID]; !ok {
			if p, bad := problems["script "+s.ID]; bad {
				t.Errorf("script %s: unexpected problem %s: %s", s.ID, p.Kind, p.Message)
			}
		}
	}
	for id, w := range c.Expect.Provided {
		p, bad := problems["host module "+id]
		if w.Problem != nil {
			checkProblem(t, "host module "+id, w.Problem, p, bad)
			continue
		}
		if bad {
			t.Errorf("host module %s: unexpected problem %s: %s", id, p.Kind, p.Message)
			continue
		}
		hm := res.HostModules[id]
		if hm == nil {
			t.Errorf("host module %s: not scanned", id)
			continue
		}
		if w.Exports != nil && !slices.Equal(hm.Exports, w.Exports) {
			t.Errorf("host module %s: exports %q, want %q", id, hm.Exports, w.Exports)
		}
		if w.Imports != nil && !slices.Equal(nonNil(hm.HostModules), w.Imports) {
			t.Errorf("host module %s: imports host modules %q, want %q", id, hm.HostModules, w.Imports)
		}
	}
	for item, p := range problems {
		if id, ok := strings.CutPrefix(item, "host module "); ok {
			if w, ok := c.Expect.Provided[id]; !ok || w.Problem == nil {
				t.Errorf("%s: unexpected problem %s: %s", item, p.Kind, p.Message)
			}
		}
	}
	if p, bad := problems["host modules"]; c.Expect.OrderProblem != nil {
		checkProblem(t, "host modules", c.Expect.OrderProblem, p, bad)
	} else if bad {
		t.Errorf("host modules: unexpected problem %s: %s", p.Kind, p.Message)
	}
	if c.Expect.HostModuleOrder != nil && be == nil {
		if !slices.Equal(res.HostModuleOrder, c.Expect.HostModuleOrder) {
			t.Errorf("host module order %q, want %q", res.HostModuleOrder, c.Expect.HostModuleOrder)
		}
	}
	if res != nil {
		var codes []string
		for _, w := range res.Warnings {
			codes = append(codes, w.Code)
		}
		if !slices.Equal(codes, c.Expect.Warnings) {
			t.Errorf("warnings %q, want %q (%v)", codes, c.Expect.Warnings, res.Warnings)
		}
	}
	if r := c.Expect.Run; r != nil {
		if be != nil {
			t.Fatalf("a case that runs needs a clean build: %v", be)
		}
		var register [][2]string
		if r.Register != nil {
			register = [][2]string{}
			for _, id := range r.Register {
				register = append(register, [2]string{res.HostModules[id].Key, filepath.Join(out, "scripts", "bun", "provided", id+".mjs")})
			}
		}
		var calls []call
		for _, cl := range r.Calls {
			entry := cl.Entry
			if entry == "" {
				entry = "main"
			}
			exp := cl.Export
			if exp == "" {
				exp = "main"
			}
			calls = append(calls, call{Module: filepath.Join(out, "scripts", "bun", entry+".mjs"), Export: exp, Args: cl.Args})
		}
		got := runModules(t, bun, r.Env, register, calls)
		for i, cl := range r.Calls {
			g := got[i]
			switch {
			case cl.Error != "":
				if g.Error == nil || !strings.Contains(*g.Error, cl.Error) {
					t.Errorf("call %d: got %s / %v, want an error containing %q", i, g.Result, g.Error, cl.Error)
				}
			case g.Error != nil:
				t.Errorf("call %d: threw %s, want %s", i, *g.Error, cl.Result)
			case !jsonEqual(t, g.Result, cl.Result):
				t.Errorf("call %d: returned %s, want %s", i, g.Result, cl.Result)
			}
		}
	}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func checkProblem(t *testing.T, item string, w *problemWant, p Problem, bad bool) {
	t.Helper()
	if !bad {
		t.Errorf("%s: no problem, want %s", item, w.Kind)
		return
	}
	if p.Kind != w.Kind {
		t.Errorf("%s: problem %s (%s), want %s", item, p.Kind, p.Message, w.Kind)
	}
	for _, s := range w.Contains {
		if !strings.Contains(p.Message, s) {
			t.Errorf("%s: problem message %q lacks %q", item, p.Message, s)
		}
	}
}

func checkEntry(t *testing.T, id string, w entryExpect, res *BunResult, p Problem, out string) {
	t.Helper()
	item := "script " + id
	bad := p.Kind != ""
	if w.Problem != nil {
		checkProblem(t, item, w.Problem, p, bad)
	} else if bad {
		t.Errorf("%s: unexpected problem %s: %s", item, p.Kind, p.Message)
		return
	}
	if res == nil || res.Scripts[id] == nil {
		t.Fatalf("%s: no metadata", item)
	}
	m := res.Scripts[id]
	for name, sw := range w.Signatures {
		got, ok := m.Signatures[name]
		switch {
		case !ok:
			t.Errorf("%s: no signature for %s (exports %q)", item, name, m.Exports)
		case sw.ErrorContains != "":
			if !strings.Contains(got.Error, sw.ErrorContains) {
				t.Errorf("%s: signature of %s is %+v, want an error containing %q", item, name, got, sw.ErrorContains)
			}
		case got.Error != "":
			t.Errorf("%s: signature of %s failed: %s", item, name, got.Error)
		case !slices.Equal(got.Params, nonNilParams(sw.Params)):
			t.Errorf("%s: signature of %s is %+v, want %+v", item, name, got.Params, sw.Params)
		}
	}
	if w.Exports != nil && !slices.Equal(m.Exports, w.Exports) {
		t.Errorf("%s: exports %q, want %q", item, m.Exports, w.Exports)
	}
	if w.Annotations != nil {
		var on []string
		a := m.Annotations
		for _, f := range []struct {
			name string
			on   bool
		}{{"npm", a.Npm}, {"nodejs", a.Nodejs}, {"native", a.Native}, {"nobundling", a.Nobundling}, {"sandbox", a.Sandbox}} {
			if f.on {
				on = append(on, f.name)
			}
		}
		if !slices.Equal(nonNil(on), w.Annotations) {
			t.Errorf("%s: annotations %q, want %q", item, on, w.Annotations)
		}
	}
	if w.Target != "" && m.Target != w.Target {
		t.Errorf("%s: target %s, want %s", item, m.Target, w.Target)
	}
	if w.WorkflowAsCode != nil && m.WorkflowAsCode != *w.WorkflowAsCode {
		t.Errorf("%s: workflow-as-code %+v, want %+v", item, m.WorkflowAsCode, *w.WorkflowAsCode)
	}
	if w.Imports != nil && !slices.Equal(m.Imports, w.Imports) {
		t.Errorf("%s: imports %q, want %q", item, m.Imports, w.Imports)
	}
	if w.HostModules != nil && !slices.Equal(m.HostModules, w.HostModules) {
		t.Errorf("%s: host modules %q, want %q", item, m.HostModules, w.HostModules)
	}
	if len(w.BundleContains)+len(w.BundleNotContain) > 0 || w.FirstLine != "" {
		b, err := os.ReadFile(filepath.Join(out, "scripts", "bun", id+".mjs"))
		if err != nil {
			t.Fatalf("%s: %v", item, err)
		}
		for _, s := range w.BundleContains {
			if !bytes.Contains(b, []byte(s)) {
				t.Errorf("%s: the bundle lacks %q:\n%s", item, s, b)
			}
		}
		for _, s := range w.BundleNotContain {
			if bytes.Contains(b, []byte(s)) {
				t.Errorf("%s: the bundle contains %q:\n%s", item, s, b)
			}
		}
		if first, _, _ := strings.Cut(string(b), "\n"); w.FirstLine != "" && first != w.FirstLine {
			t.Errorf("%s: the bundle starts with %q, want %q", item, first, w.FirstLine)
		}
	}
}

func nonNilParams(p []Param) []Param {
	if p == nil {
		return []Param{}
	}
	return p
}

// TestConformanceBuildCatalogue accounts for every catalogue case of the
// findings this level covers: each is a case here or is listed in
// deferred.json with the reason and where it is checked, and every cited
// case exists with the provenance the catalogue gives it.
func TestConformanceBuildCatalogue(t *testing.T) {
	data, err := os.ReadFile(catalogueFile)
	if err != nil {
		t.Fatal(err)
	}
	catalogue := map[string]string{}
	for _, m := range catalogueEntry.FindAllSubmatch(data, -1) {
		catalogue[string(m[1])] = string(m[2])
	}
	files := loadCaseFiles(t)
	findings := map[string]bool{}
	covered := map[string]bool{}
	for _, f := range files {
		findings[f.Finding] = true
		for _, c := range f.Cases {
			if !strings.Contains(c.Spec, "/") {
				continue // a case that extends the catalogue
			}
			prov, ok := catalogue[c.Spec]
			if !ok {
				t.Errorf("%s is not a catalogue case", c.Spec)
				continue
			}
			if c.Provenance != prov {
				t.Errorf("%s: provenance %q, catalogue says %q", c.Spec, c.Provenance, prov)
			}
			covered[c.Spec] = true
		}
	}
	var d deferredFile
	readStrict(t, filepath.Join(casesDir, "deferred.json"), &d)
	deferred := map[string]bool{}
	for _, e := range d.Deferred {
		switch {
		case catalogue[e.Spec] == "":
			t.Errorf("deferred %s is not a catalogue case", e.Spec)
		case covered[e.Spec]:
			t.Errorf("%s is both covered and deferred", e.Spec)
		case deferred[e.Spec]:
			t.Errorf("%s is deferred twice", e.Spec)
		case e.Reason == "" || e.ExpectedIn == "":
			t.Errorf("deferred %s needs a reason and a place", e.Spec)
		}
		deferred[e.Spec] = true
	}
	n := 0
	for spec := range catalogue {
		finding, _, _ := strings.Cut(spec, "/")
		if !findings[finding] {
			continue
		}
		n++
		if !covered[spec] && !deferred[spec] {
			t.Errorf("catalogue case %s is neither covered nor deferred", spec)
		}
	}
	t.Logf("catalogue cases of the covered findings: %d; covered here: %d; deferred: %d", n, len(covered), len(deferred))
}
