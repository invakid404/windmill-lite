package ir

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/syncfolder"
)

// The compile cases live in conformance/ir as data files, one per finding.
// Each case cites its catalogue case (docs/semantics/conformance.md), or
// only the finding for a case that extends the catalogue, and gives a sync
// folder as a map of files, optionally a host contract and build options.
// The runner writes the folder to a temporary directory, compiles it, and
// checks the expectations: the errors by kind and message substrings, or
// parts of the compiled program addressed by JSON Pointer, by flow, by a
// step's site, by a module list's sites, and by inventory id.

const (
	casesDir       = "../../conformance/ir"
	catalogueFile  = "../../docs/semantics/conformance.md"
	openflowDir    = "../../conformance/openflow"
	syncfolderDir  = "../../conformance/syncfolder"
	irExpectedName = "conformance/ir"
)

type caseFile struct {
	Finding string        `json:"finding"`
	Note    string        `json:"note"`
	Cases   []compileCase `json:"cases"`
}

type compileCase struct {
	Spec       string `json:"spec"`
	Variant    string `json:"variant"`
	Provenance string `json:"provenance"`
	Scope      string `json:"scope"`
	Part       string `json:"part"`
	Policy     string `json:"policy"`
	Deviation  string `json:"deviation"`
	Note       string `json:"note"`
	Title      string `json:"title"`
	Given      struct {
		Files    map[string]string `json:"files"`
		HexFiles map[string]string `json:"hex_files"`
		Contract string            `json:"contract"`
		Options  Options           `json:"options"`
	} `json:"given"`
	Expect struct {
		Errors  []errExpect             `json:"errors"`
		At      map[string]any          `json:"at"`
		Absent  []string                `json:"absent"`
		Flows   map[string]pointerCheck `json:"flows"`
		NoFlows []string                `json:"no_flows"`
		Steps   map[string]stepCheck    `json:"steps"`
		Lists   map[string][]string     `json:"lists"`
		Scripts map[string]pointerCheck `json:"scripts"`
	} `json:"expect"`
}

type errExpect struct {
	Kind     ErrorKind `json:"kind"`
	Contains []string  `json:"contains"`
}

type pointerCheck struct {
	At     map[string]any `json:"at"`
	Absent []string       `json:"absent"`
}

type stepCheck struct {
	pointerCheck
	// Script checks the inventory entry of the step's leaf.
	Script *pointerCheck `json:"script"`
}

type deferredFile struct {
	Note     string `json:"note"`
	Deferred []struct {
		Spec       string `json:"spec"`
		Reason     string `json:"reason"`
		ExpectedIn string `json:"expected_in"`
	} `json:"deferred"`
}

var specPattern = regexp.MustCompile(`^((?:OF|SY|FX|CF|JE|WR|WA)-[0-9V]+)(/[a-z])?$`)

func readStrict(t *testing.T, path string, out any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func loadCaseFiles(t *testing.T) []caseFile {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(casesDir, "*-*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no case files in %s (%v)", casesDir, err)
	}
	var files []caseFile
	for _, p := range paths {
		var f caseFile
		readStrict(t, p, &f)
		if want := strings.ToLower(f.Finding) + ".json"; filepath.Base(p) != want {
			t.Fatalf("%s holds finding %s; want the file %s", p, f.Finding, want)
		}
		for _, c := range f.Cases {
			m := specPattern.FindStringSubmatch(c.Spec)
			if m == nil || m[1] != f.Finding {
				t.Fatalf("%s: case %s does not belong to finding %s", p, c.Spec, f.Finding)
			}
			switch c.Provenance {
			case "source-derived", "oracle-observed", "policy", "disputed":
			default:
				t.Fatalf("%s: case %s has provenance %q", p, c.Spec, c.Provenance)
			}
			if c.Part != "" && c.Part != "compile" {
				t.Fatalf("%s: case %s has part %q", p, c.Spec, c.Part)
			}
			if c.Part == "compile" && c.Note == "" {
				t.Fatalf("%s: case %s checks only its compile half and must say where the rest is checked", p, c.Spec)
			}
			if c.Provenance == "disputed" && c.Note == "" {
				t.Fatalf("%s: disputed case %s must say which reading it encodes and why", p, c.Spec)
			}
		}
		files = append(files, f)
	}
	return files
}

func caseName(c compileCase, i int) string {
	name := c.Spec
	if c.Variant != "" {
		name += "/" + c.Variant
	}
	if !strings.Contains(c.Spec, "/") && c.Variant == "" {
		name += "/" + strconv.Itoa(i)
	}
	return strings.ReplaceAll(name, "/", "_")
}

func TestConformanceIR(t *testing.T) {
	n := 0
	for _, f := range loadCaseFiles(t) {
		names := map[string]bool{}
		for i, c := range f.Cases {
			name := caseName(c, i)
			if names[name] {
				t.Fatalf("finding %s: two cases are named %s; give a variant", f.Finding, name)
			}
			names[name] = true
			t.Run(name, func(t *testing.T) { runCase(t, c) })
			n++
		}
	}
	t.Logf("%d compile cases", n)
}

// openFolder writes files to a temporary directory and opens it as a sync
// folder. hexFiles gives files by their bytes in hex, for text that is not
// UTF-8. A `.git` directory bounds the search for wmill.yaml.
func openFolder(t *testing.T, files map[string]string, hexFiles ...map[string]string) *syncfolder.Folder {
	t.Helper()
	dir := t.TempDir()
	write := func(name string, data []byte) {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, text := range files {
		write(name, []byte(text))
	}
	for _, hf := range hexFiles {
		for name, h := range hf {
			data, err := hex.DecodeString(h)
			if err != nil {
				t.Fatal(err)
			}
			write(name, data)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := syncfolder.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func runCase(t *testing.T, c compileCase) {
	f := openFolder(t, c.Given.Files, c.Given.HexFiles)
	var contract *Contract
	if c.Given.Contract != "" {
		var err error
		if contract, err = ParseContract([]byte(c.Given.Contract)); err != nil {
			checkErrors(t, err, c.Expect.Errors)
			return
		}
	}
	prog, err := Compile(f, contract, c.Given.Options)
	x := c.Expect
	if len(x.Errors) > 0 {
		if err == nil {
			t.Fatalf("%s: compiled; want errors %+v", c.Title, x.Errors)
		}
		checkErrors(t, err, x.Errors)
		return
	}
	if err != nil {
		t.Fatalf("%s: %v", c.Title, err)
	}
	data, err := prog.MarshalIndent()
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeAny(t, data)
	checkPointers(t, "program", doc, pointerCheck{At: x.At, Absent: x.Absent})
	for p, want := range x.Flows {
		fl, ok := resolvePointer(doc, "/flows/"+escape(p))
		if !ok {
			t.Errorf("flow %s is not in the program", p)
			continue
		}
		checkPointers(t, "flow "+p, fl, want)
	}
	for _, p := range x.NoFlows {
		if _, ok := prog.Flows[p]; ok {
			t.Errorf("flow %s is in the program; want it opaque or unreached", p)
		}
	}
	for key, want := range x.Steps {
		m := findStep(t, prog, key)
		if m == nil {
			continue
		}
		checkPointers(t, "step "+key, toDoc(t, m), want.pointerCheck)
		if want.Script != nil {
			if m.Leaf == nil {
				t.Errorf("step %s has no leaf", key)
				continue
			}
			s, ok := prog.Scripts[m.Leaf.Script]
			if !ok {
				t.Errorf("step %s: script %s is not in the inventory", key, m.Leaf.Script)
				continue
			}
			checkPointers(t, "script of "+key, toDoc(t, s), *want.Script)
		}
	}
	for key, want := range x.Lists {
		list := findList(t, prog, key)
		var got []string
		for _, m := range list {
			got = append(got, segmentOf(m).String())
		}
		if !slices.Equal(got, want) {
			t.Errorf("list %s = %q, want %q", key, got, want)
		}
	}
	for id, want := range x.Scripts {
		s, ok := prog.Scripts[id]
		if !ok {
			t.Errorf("script %s is not in the inventory", id)
			continue
		}
		checkPointers(t, "script "+id, toDoc(t, s), want)
	}
	// The JSON form decodes back to the same program.
	var back Program
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("decoding the program: %v", err)
	}
	again, err := back.MarshalIndent()
	if err != nil || !bytes.Equal(again, data) {
		t.Errorf("the program's JSON form does not round-trip (%v)", err)
	}
}

func segmentOf(m *Module) conformance.Segment {
	if m.Occurrence != nil {
		return conformance.RepeatedModule(m.ID, *m.Occurrence)
	}
	return conformance.Module(m.ID)
}

// findList resolves "<flow path>" to the flow's module list, or
// "<flow path> <site>" to the body of the loop at that site, or with a
// final branch segment to that branch's body.
func findList(t *testing.T, p *Program, key string) []*Module {
	t.Helper()
	flow, site, _ := strings.Cut(key, " ")
	fl, ok := p.Flows[flow]
	if !ok {
		t.Fatalf("list %s: flow %s is not in the program", key, flow)
	}
	if site == "" {
		return fl.Modules
	}
	path, err := conformance.ParseSitePath(site)
	if err != nil {
		t.Fatalf("list %s: %v", key, err)
	}
	segs := path.Segments()
	last := segs[len(segs)-1]
	if last.Kind() == conformance.KindBranch {
		m := walk(t, fl, segs[:len(segs)-1], key)
		return branchBody(t, m, last, key)
	}
	m := walk(t, fl, segs, key)
	switch {
	case m.ForLoop != nil:
		return m.ForLoop.Modules
	case m.WhileLoop != nil:
		return m.WhileLoop.Modules
	}
	t.Fatalf("list %s: %s is not a loop", key, site)
	return nil
}

func findStep(t *testing.T, p *Program, key string) *Module {
	t.Helper()
	flow, site, ok := strings.Cut(key, " ")
	fl, found := p.Flows[flow]
	if !ok || !found {
		t.Errorf("step %s: flow %s is not in the program", key, flow)
		return nil
	}
	path, err := conformance.ParseSitePath(site)
	if err != nil {
		t.Fatalf("step %s: %v", key, err)
	}
	return walk(t, fl, path.Segments(), key)
}

// walk follows a static site path (iteration segments left out) from a
// flow to a module.
func walk(t *testing.T, fl *Flow, segs []conformance.Segment, key string) *Module {
	t.Helper()
	var list []*Module
	var cur *Module
	for i := 0; i < len(segs); i++ {
		s := segs[i]
		switch s.Kind() {
		case conformance.KindSlot:
			if i != 0 {
				t.Fatalf("%s: a slot segment must come first", key)
			}
			if s.Value() == "failure" {
				cur = fl.FailureModule
			} else {
				cur = fl.PreprocessorModule
			}
			if cur == nil {
				t.Fatalf("%s: flow %s has no %s module", key, fl.Path, s.Value())
			}
			continue
		case conformance.KindBranch:
			list = branchBody(t, cur, s, key)
			continue
		case conformance.KindModule:
		default:
			t.Fatalf("%s: unexpected segment %s", key, s)
		}
		switch {
		case i == 0:
			list = fl.Modules
		case list == nil && cur.ForLoop != nil:
			list = cur.ForLoop.Modules
		case list == nil && cur.WhileLoop != nil:
			list = cur.WhileLoop.Modules
		case list == nil:
			t.Fatalf("%s: %s has no body without a branch segment", key, cur.ID)
		}
		cur = nil
		for _, m := range list {
			if segmentOf(m) == s {
				cur = m
				break
			}
		}
		if cur == nil {
			t.Fatalf("%s: no module %s", key, s)
		}
		list = nil
	}
	if cur == nil {
		t.Fatalf("%s: empty site", key)
	}
	return cur
}

func branchBody(t *testing.T, m *Module, s conformance.Segment, key string) []*Module {
	t.Helper()
	if m == nil {
		t.Fatalf("%s: a branch segment must follow a module", key)
	}
	n, indexed := s.Index()
	switch {
	case m.BranchOne != nil && !indexed:
		return m.BranchOne.Default
	case m.BranchOne != nil && n < len(m.BranchOne.Branches):
		return m.BranchOne.Branches[n].Modules
	case m.BranchAll != nil && indexed && n < len(m.BranchAll.Branches):
		return m.BranchAll.Branches[n].Modules
	}
	t.Fatalf("%s: %s has no branch %s", key, m.ID, s)
	return nil
}

// checkErrors checks that each expected error is among the errors err
// joins: an *Error of that kind whose message holds every substring.
func checkErrors(t *testing.T, err error, want []errExpect) {
	t.Helper()
	if err == nil {
		t.Fatalf("no error; want %+v", want)
	}
	var all []error
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		all = j.Unwrap()
	} else {
		all = []error{err}
	}
	for _, e := range all {
		var ie *Error
		if !errors.As(e, &ie) {
			t.Errorf("error %T is not an *Error: %v", e, e)
		}
	}
	for _, w := range want {
		found := slices.ContainsFunc(all, func(e error) bool {
			var ie *Error
			if !errors.As(e, &ie) || ie.Kind != w.Kind {
				return false
			}
			for _, s := range w.Contains {
				if !strings.Contains(e.Error(), s) {
					return false
				}
			}
			return true
		})
		if !found {
			t.Errorf("no %s error containing %q in:\n%v", w.Kind, w.Contains, err)
		}
	}
}

func checkPointers(t *testing.T, what string, doc any, want pointerCheck) {
	t.Helper()
	for ptr, w := range want.At {
		v, ok := resolvePointer(doc, ptr)
		if !ok {
			t.Errorf("%s: %s absent in %s", what, ptr, mustJSON(doc))
			continue
		}
		if !reflect.DeepEqual(v, normalise(t, w)) {
			t.Errorf("%s: %s = %s, want %s", what, ptr, mustJSON(v), mustJSON(w))
		}
	}
	for _, ptr := range want.Absent {
		if v, ok := resolvePointer(doc, ptr); ok {
			t.Errorf("%s: %s = %s, want absent", what, ptr, mustJSON(v))
		}
	}
}

func toDoc(t *testing.T, v any) any {
	t.Helper()
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return decodeAny(t, b.Bytes())
}

// normalise re-decodes an expected value so that numbers compare as the
// program's do.
func normalise(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return decodeAny(t, b)
}

func decodeAny(t *testing.T, data []byte) any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("%s: %v", data, err)
	}
	return v
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func escape(s string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(s)
}

func resolvePointer(doc any, ptr string) (any, bool) {
	if ptr == "" {
		return doc, true
	}
	unescape := strings.NewReplacer("~1", "/", "~0", "~")
	for _, part := range strings.Split(strings.TrimPrefix(ptr, "/"), "/") {
		part = unescape.Replace(part)
		switch v := doc.(type) {
		case map[string]any:
			next, ok := v[part]
			if !ok {
				return nil, false
			}
			doc = next
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(v) {
				return nil, false
			}
			doc = v[i]
		default:
			return nil, false
		}
	}
	return doc, true
}

// TestConformanceIRCatalogue accounts for the catalogue cases this level
// touches. Every cited catalogue case exists with the provenance the
// catalogue gives it. For every finding with a file here, each of its
// catalogue cases is a case at this level, at the decode or load level,
// or listed in one of their deferred.json files with a reason. And every
// case that the decode level defers to this level is a case here.
func TestConformanceIRCatalogue(t *testing.T) {
	data, err := os.ReadFile(catalogueFile)
	if err != nil {
		t.Fatal(err)
	}
	catalogue := map[string]string{}
	byFinding := map[string][]string{}
	re := regexp.MustCompile(`(?m)^- \*\*([A-Z]+-[0-9V]+)(/[a-z])\*\* · ([a-z-]+)`)
	for _, m := range re.FindAllSubmatch(data, -1) {
		id := string(m[1]) + string(m[2])
		catalogue[id] = string(m[3])
		byFinding[string(m[1])] = append(byFinding[string(m[1])], id)
	}
	// Findings are the headings of the reference's area files.
	known := map[string]bool{}
	areas, _ := filepath.Glob(filepath.Join(filepath.Dir(catalogueFile), "*.md"))
	heading := regexp.MustCompile(`(?m)^### ([A-Z]+-[0-9V]+) · `)
	for _, a := range areas {
		text, err := os.ReadFile(a)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range heading.FindAllSubmatch(text, -1) {
			known[string(m[1])] = true
		}
	}

	accounted := map[string]bool{}
	here := map[string]bool{}
	for _, f := range loadCaseFiles(t) {
		for _, c := range f.Cases {
			if !strings.Contains(c.Spec, "/") {
				if !known[c.Spec] {
					t.Errorf("finding %s is not in the reference", c.Spec)
				}
				continue
			}
			prov, ok := catalogue[c.Spec]
			if !ok {
				t.Errorf("case %s is not in the catalogue", c.Spec)
				continue
			}
			if prov != c.Provenance {
				t.Errorf("case %s has provenance %s; the catalogue says %s", c.Spec, c.Provenance, prov)
			}
			accounted[c.Spec], here[c.Spec] = true, true
		}
	}
	var deferred deferredFile
	readStrict(t, filepath.Join(casesDir, "deferred.json"), &deferred)
	for _, d := range deferred.Deferred {
		if d.Reason == "" || d.ExpectedIn == "" {
			t.Errorf("deferred %s needs a reason and where it is expected", d.Spec)
		}
		if _, ok := catalogue[d.Spec]; !ok {
			t.Errorf("deferred %s is not in the catalogue", d.Spec)
		}
		if here[d.Spec] {
			t.Errorf("%s is both a case and deferred", d.Spec)
		}
		accounted[d.Spec] = true
	}
	// The decode and load levels' cases and deferrals.
	for _, dir := range []string{openflowDir, syncfolderDir} {
		paths, _ := filepath.Glob(filepath.Join(dir, "*-*.json"))
		for _, p := range paths {
			var f struct {
				Cases []struct {
					Spec string `json:"spec"`
				} `json:"cases"`
			}
			raw, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(raw, &f); err != nil {
				t.Fatalf("%s: %v", p, err)
			}
			for _, c := range f.Cases {
				accounted[c.Spec] = true
			}
		}
		var d deferredFile
		readStrict(t, filepath.Join(dir, "deferred.json"), &d)
		for _, e := range d.Deferred {
			if strings.Contains(e.ExpectedIn, irExpectedName) {
				if !here[e.Spec] {
					t.Errorf("%s defers %s to %s, which has no such case", dir, e.Spec, irExpectedName)
				}
				continue
			}
			accounted[e.Spec] = true
		}
	}
	for _, f := range loadCaseFiles(t) {
		for _, id := range byFinding[f.Finding] {
			if !accounted[id] {
				t.Errorf("catalogue case %s of %s is neither a case nor deferred", id, f.Finding)
			}
		}
	}
}
