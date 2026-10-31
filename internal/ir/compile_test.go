package ir

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/expr"
	"github.com/invakid404/windmill-lite/internal/openflow"
	"github.com/invakid404/windmill-lite/internal/syncfolder"
)

const wmill = "defaultTs: bun\n"

func compileFiles(t *testing.T, files map[string]string, contract string, opts Options) (*Program, error) {
	t.Helper()
	f := openFolder(t, files)
	var c *Contract
	if contract != "" {
		var err error
		if c, err = ParseContract([]byte(contract)); err != nil {
			t.Fatal(err)
		}
	}
	return Compile(f, c, opts)
}

func mustCompile(t *testing.T, files map[string]string, contract string) *Program {
	t.Helper()
	p, err := compileFiles(t, files, contract, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func joined(err error) []*Error {
	var out []*Error
	all := []error{err}
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		all = j.Unwrap()
	}
	for _, e := range all {
		var ie *Error
		if errors.As(e, &ie) {
			out = append(out, ie)
		}
	}
	return out
}

func TestErrorText(t *testing.T) {
	inner := errors.New("boom")
	for _, c := range []struct {
		e    *Error
		want string
	}{
		{&Error{Flow: "f/a", Step: "s", Selector: -1, Msg: "unsupported: suspend"}, "f/a step 's': unsupported: suspend"},
		{&Error{Flow: "f/a", Selector: -1, Err: inner}, "flow f/a: boom"},
		{&Error{Script: "f/s", Selector: -1, Msg: "m", Err: inner}, "script f/s: m: boom"},
		{&Error{Selector: 2, Msg: "m"}, "provided site 2: m"},
		{&Error{Selector: -1, Msg: "m"}, "m"},
	} {
		if got := c.e.Error(); got != c.want {
			t.Errorf("Error() = %q, want %q", got, c.want)
		}
	}
	if !errors.Is(&Error{Err: inner}, inner) {
		t.Error("Unwrap does not reach the cause")
	}
}

// TestLoadErrors checks that what the folder cannot supply fails the
// compile with an ErrLoad that names the step and wraps the folder's
// error (SY-15).
func TestLoadErrors(t *testing.T) {
	files := map[string]string{
		"wmill.yaml":    wmill + "excludes: ['f/x/hidden*']\n",
		"f/x/hidden.ts": RET1,
		"f/x/main.flow/flow.yaml": "value:\n  modules:\n" +
			"    - id: gone\n      value: {type: script, path: f/x/missing}\n" +
			"    - id: hub\n      value: {type: flow, path: hub/123/x}\n" +
			"    - id: hid\n      value: {type: script, path: f/x/hidden}\n",
	}
	_, err := compileFiles(t, files, "roots:\n  - flow: f/x/main\n  - flow: f/x/nope\n  - script: f/x/nada\n", Options{})
	es := joined(err)
	want := []struct {
		where string
		kind  syncfolder.ErrorKind
	}{
		{"f/x/main step 'gone'", syncfolder.ErrNotInFolder},
		{"f/x/main step 'hub'", syncfolder.ErrHubPath},
		{"f/x/main step 'hid'", syncfolder.ErrFiltered},
		{"flow f/x/nope", syncfolder.ErrNotInFolder},
		{"script f/x/nada", syncfolder.ErrNotInFolder},
	}
	if len(es) != len(want) {
		t.Fatalf("got %d errors, want %d:\n%v", len(es), len(want), err)
	}
	for _, w := range want {
		found := slices.ContainsFunc(es, func(e *Error) bool {
			var se *syncfolder.Error
			return e.Kind == ErrLoad && strings.HasPrefix(e.Error(), w.where) && errors.As(e, &se) && se.Kind == w.kind
		})
		if !found {
			t.Errorf("no load error at %s of kind %s in:\n%v", w.where, w.kind, err)
		}
	}
	// The root script error comes before the flows', which are walked
	// after the roots are read; within a flow, errors follow the walk.
	if es[0].Script != "f/x/nada" || es[1].Step != "gone" || es[2].Step != "hub" {
		t.Errorf("errors out of walk order:\n%v", err)
	}
}

const RET1 = "export async function main() { return 1 }\n"

// TestSubflows checks that each reachable flow is compiled once, whatever
// calls it and however often, and that a cycle terminates.
func TestSubflows(t *testing.T) {
	sub := func(id, target string) string {
		return "    - id: " + id + "\n      value: {type: flow, path: " + target + "}\n"
	}
	p := mustCompile(t, map[string]string{
		"wmill.yaml":           wmill,
		"f/x/a.flow/flow.yaml": "value:\n  modules:\n" + sub("b1", "f/x/b") + sub("b2", "f/x/b"),
		"f/x/b.flow/flow.yaml": "value:\n  modules:\n" + sub("a", "f/x/a") + sub("c", "f/x/c"),
		"f/x/c.flow/flow.yaml": "value:\n  modules: []\n",
		"f/x/u.flow/flow.yaml": "value:\n  modules: []\n",
	}, "roots:\n  - flow: f/x/a\n")
	var got []string
	for k := range p.Flows {
		got = append(got, k)
	}
	slices.Sort(got)
	if !slices.Equal(got, []string{"f/x/a", "f/x/b", "f/x/c"}) {
		t.Errorf("flows = %q; want the closure of f/x/a only", got)
	}
}

// TestNilContract compiles every item the folder lists.
func TestNilContract(t *testing.T) {
	p := mustCompile(t, map[string]string{
		"wmill.yaml":           wmill,
		"f/x/s.ts":             RET1,
		"f/x/a.flow/flow.yaml": "value:\n  modules: []\n",
	}, "")
	if !slices.Equal(p.Roots.Flows, []string{"f/x/a"}) || !slices.Equal(p.Roots.Scripts, []string{"f/x/s"}) {
		t.Errorf("roots = %+v", p.Roots)
	}
	_, err := compileFiles(t, map[string]string{"wmill.yaml": wmill}, "", Options{})
	if es := joined(err); len(es) != 1 || es[0].Kind != ErrContract {
		t.Errorf("empty folder: %v", err)
	}
}

// TestDeterministic checks that a compile is reproducible and that an
// inline step's inventory id does not move when another step is added
// before it, since it depends only on the flow and the step's site.
func TestDeterministic(t *testing.T) {
	flow := func(extra string) map[string]string {
		return map[string]string{
			"wmill.yaml": wmill,
			"f/x/a.flow/flow.yaml": "value:\n  modules:\n" + extra +
				"    - id: s\n      value: {type: rawscript, language: bun, content: 'export function main() {}', input_transforms: {}}\n",
		}
	}
	p1 := mustCompile(t, flow(""), "")
	p2 := mustCompile(t, flow(""), "")
	b1, _ := p1.MarshalIndent()
	b2, _ := p2.MarshalIndent()
	if !bytes.Equal(b1, b2) {
		t.Error("two compiles of one folder differ")
	}
	p3 := mustCompile(t, flow("    - id: before\n      value: {type: identity}\n"), "")
	if id1, id3 := p1.Flows["f/x/a"].Modules[0].Leaf.Script, p3.Flows["f/x/a"].Modules[1].Leaf.Script; id1 != id3 {
		t.Errorf("inline id moved from %s to %s", id1, id3)
	}
}

// TestProvidedDetails covers provided-site rules that the conformance
// cases do not: overlapping selectors, a script selector on an inline
// step, a provided body that keeps its loop collapsed, a provided failure
// module, and a provided site's own suspend.
func TestProvidedDetails(t *testing.T) {
	raw := "{type: rawscript, language: bun, content: 'export function main() {}', input_transforms: {e: {type: javascript, expr: error}}}"
	files := map[string]string{
		"wmill.yaml": wmill,
		"f/x/a.flow/flow.yaml": "value:\n  failure_module:\n    id: failure\n    value: " + raw + "\n  modules:\n" +
			"    - id: l\n      value:\n        type: whileloopflow\n        modules:\n" +
			"          - id: p\n            value: {type: script, path: f/x/gone}\n" +
			"    - id: i\n      value: " + raw + "\n",
	}
	p := mustCompile(t, files, `roots: [{flow: f/x/a}]
provided:
  - {selector: {flow: f/x/a, module: p}, provider: one, mandatory: true}
  - {selector: {script: f/x/a/i}, provider: two, mandatory: true}
  - {selector: {flow: f/x/a, module: failure}, provider: three, mandatory: true}
`)
	fl := p.Flows["f/x/a"]
	loop := fl.Modules[0]
	if body := loop.WhileLoop.Modules[0]; body.Kind != KindProvided || body.Provided.Provider != "one" {
		t.Errorf("loop body = %+v", body)
	}
	// A provided body keeps its original kind for the collapse rule, but
	// this flow has a failure module, so nothing collapses.
	if loop.WhileLoop.Collapse {
		t.Error("a loop collapsed in a flow with a failure module")
	}
	if m := fl.Modules[1]; m.Kind != KindProvided || m.Provided.Target.Kind != KindRawScript || m.Provided.Provider != "two" {
		t.Errorf("inline step = %+v", m)
	}
	fm := fl.FailureModule
	if fm.Kind != KindProvided || fm.Provided.Inputs[0].Transform.Expr.Site != expr.SiteFailureInputTransform {
		t.Errorf("failure module = %+v", fm)
	}
	if len(p.Scripts) != 0 {
		t.Errorf("provided sites added scripts to the inventory: %v", p.Scripts)
	}

	// Without the failure module, the provided script body collapses.
	files["f/x/a.flow/flow.yaml"] = strings.Replace(files["f/x/a.flow/flow.yaml"], "  failure_module:\n    id: failure\n    value: "+raw+"\n", "", 1)
	p = mustCompile(t, files, "roots: [{flow: f/x/a}]\nprovided:\n  - {selector: {flow: f/x/a, module: p}, provider: one, mandatory: true}\n  - {selector: {flow: f/x/a, module: i}, provider: two, mandatory: true}\n")
	if !p.Flows["f/x/a"].Modules[0].WhileLoop.Collapse {
		t.Error("a provided script body does not collapse its loop")
	}

	// Two provided sites that select one site.
	_, err := compileFiles(t, files, "roots: [{flow: f/x/a}]\nprovided:\n  - {selector: {flow: f/x/a, module: p}, provider: one, mandatory: true}\n  - {selector: {script: f/x/gone}, provider: two, mandatory: true}\n  - {selector: {flow: f/x/a, module: i}, provider: three, mandatory: true}\n", Options{})
	if es := joined(err); len(es) != 1 || es[0].Kind != ErrContract || !strings.Contains(es[0].Msg, "provided sites 0 and 1 select the same site") {
		t.Errorf("overlap: %v", err)
	}

	// A provided site's own suspend is the root flow's, so it is refused.
	files["f/x/a.flow/flow.yaml"] = "value:\n  modules:\n    - id: p\n      suspend: {required_events: 1}\n      value: {type: script, path: f/x/gone}\n"
	_, err = compileFiles(t, files, "roots: [{flow: f/x/a}]\nprovided:\n  - {selector: {flow: f/x/a, module: p}, provider: one, mandatory: true}\n", Options{})
	if es := joined(err); len(es) != 1 || es[0].Kind != ErrUnsupported || es[0].Step != "p" {
		t.Errorf("suspend on a provided site: %v", err)
	}
}

func TestHostModulesDeviation(t *testing.T) {
	p := mustCompile(t, map[string]string{"wmill.yaml": wmill, "f/x/s.ts": RET1},
		"roots: [{script: f/x/s}]\nhost_modules: [f/x/scope.ts]\n")
	if !slices.Equal(p.Deviations, []string{"D12", "D19"}) || !slices.Equal(p.Contract.HostModules, []string{"f/x/scope.ts"}) {
		t.Errorf("deviations %q, host modules %q", p.Deviations, p.Contract.HostModules)
	}
}

// TestHarnessTable checks every language of OF-13 against 0002's table.
func TestHarnessTable(t *testing.T) {
	want := map[openflow.Language]struct {
		class    Class
		boundary Boundary
	}{
		openflow.LangBun:       {ClassBun, BoundaryBun},
		openflow.LangBunNative: {ClassBun, BoundaryNative},
		openflow.LangNativeTS:  {ClassBun, BoundaryNative},
		openflow.LangPython3:   {ClassPython3, BoundaryOwn},
		openflow.LangDeno:      {ClassDeno, BoundaryOwn},
		openflow.LangGo:        {ClassGo, BoundaryOwn},
		openflow.LangBash:      {ClassBash, BoundaryOwn},
	}
	if len(openflow.Languages) != 24 {
		t.Fatalf("OF-13 lists 24 languages; openflow has %d", len(openflow.Languages))
	}
	for _, l := range openflow.Languages {
		c, b, ok := harnessOf(l)
		w, has := want[l]
		if ok != has || c != w.class || b != w.boundary {
			t.Errorf("harnessOf(%s) = %s, %s, %v; want %s, %s, %v", l, c, b, ok, w.class, w.boundary, has)
		}
		if available(c) != (c == ClassBun) {
			t.Errorf("%s: available = %v", l, available(c))
		}
	}
}

// TestMarshalKeepsCharacters checks that the program's JSON form does not
// escape HTML characters inside verbatim values.
func TestMarshalKeepsCharacters(t *testing.T) {
	p := mustCompile(t, map[string]string{
		"wmill.yaml": wmill,
		"f/x/a.flow/flow.yaml": "value:\n  modules:\n    - id: a\n      value:\n        type: script\n        path: f/x/s\n" +
			"        input_transforms: {x: {type: static, value: '<a&b>'}}\n",
		"f/x/s.ts": RET1,
	}, "roots: [{flow: f/x/a}]\n")
	b, err := p.MarshalIndent()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte(`"<a&b>"`)) {
		t.Errorf("verbatim value escaped:\n%s", b)
	}
}

// A deploy relabels a Bun-family script from its native header, in both
// directions; the language then decides the boundary (OF-15, SY-V3, WR-40).
func TestDeployedLanguage(t *testing.T) {
	for _, tc := range []struct {
		lang     openflow.Language
		content  string
		want     openflow.Language
		boundary Boundary
	}{
		{openflow.LangBun, "//native\nexport function main() {}", openflow.LangBunNative, BoundaryNative},
		{openflow.LangBunNative, "export function main() {}", openflow.LangBun, BoundaryBun},
		{openflow.LangBun, " //native\nexport function main() {}", openflow.LangBun, BoundaryBun},
		{openflow.LangNativeTS, "export function main() {}", openflow.LangNativeTS, BoundaryNative},
		{openflow.LangNativeTS, "//native\n", openflow.LangNativeTS, BoundaryNative},
	} {
		got := DeployedLanguage(tc.lang, tc.content)
		b, ok := BoundaryOf(got)
		if got != tc.want || !ok || b != tc.boundary {
			t.Errorf("DeployedLanguage(%s, %q) = %s (%s, %v), want %s (%s)", tc.lang, tc.content, got, b, ok, tc.want, tc.boundary)
		}
	}
	if _, ok := BoundaryOf(openflow.Language("cobol")); ok {
		t.Error("a language with no harness class has no boundary")
	}
}

// WR-48: customBundler counts as set when it is JavaScript-truthy, number
// spellings read as JavaScript reads them.
func TestTruthy(t *testing.T) {
	for raw, want := range map[string]bool{
		``: false, `null`: false, `false`: false, `0`: false, `-0`: false, `0.0`: false, `0e5`: false, `1e-400`: false, `""`: false,
		`true`: true, `1`: true, `-1`: true, `1e400`: true, `"0"`: true, `"false"`: true, `" "`: true, `[]`: true, `{}`: true, `"./bundle.sh"`: true,
	} {
		if got := truthy([]byte(raw)); got != want {
			t.Errorf("truthy(%s) = %v, want %v", raw, got, want)
		}
	}
}
