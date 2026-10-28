package quickjs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/expr"
)

// The engine-level cases of testdata/engine: each encodes a catalogue case
// (docs/semantics/conformance.md) or a rule of its finding, as the outcome
// the engine itself produces. Messages are not compared: Windmill's texts
// wrap these outcomes in the pipeline (B2b).

type caseFile struct {
	Finding string       `json:"finding"`
	Cases   []engineCase `json:"cases"`
}

type engineCase struct {
	Spec       string `json:"spec"`
	Variant    string `json:"variant"`
	Provenance string `json:"provenance"`
	Deviation  string `json:"deviation"`
	Slow       bool   `json:"slow"`
	Title      string `json:"title"`
	Note       string `json:"note"`
	Given      struct {
		Expr      string     `json:"expr"`
		Source    string     `json:"source"`
		SourceGen *textGen   `json:"source_gen"`
		Strings   []caseText `json:"strings"`
		Limits    *struct {
			MemoryMB   int `json:"memory_mb"`
			AttemptMS  int `json:"attempt_ms"`
			StackBytes int `json:"stack_bytes"`
		} `json:"limits"`
	} `json:"given"`
	Expect struct {
		Value            *string          `json:"value"`
		Error            string           `json:"error"`
		Exception        *exceptionExpect `json:"exception"`
		CompileException *exceptionExpect `json:"compile_exception"`
	} `json:"expect"`
}

type caseText struct {
	Name string   `json:"name"`
	Text *string  `json:"text"`
	Gen  *textGen `json:"gen"`
}

// textGen generates a large text: Repeat × Count, or Open × Count + Inner +
// Close × Count.
type textGen struct {
	Repeat string `json:"repeat"`
	Open   string `json:"open"`
	Close  string `json:"close"`
	Inner  string `json:"inner"`
	Count  int    `json:"count"`
}

func (g *textGen) text() string {
	if g.Repeat != "" {
		return strings.Repeat(g.Repeat, g.Count)
	}
	return strings.Repeat(g.Open, g.Count) + g.Inner + strings.Repeat(g.Close, g.Count)
}

type exceptionExpect struct {
	Thrown      string `json:"thrown"`
	Name        string `json:"name"`
	Message     string `json:"message"`
	OutOfMemory *bool  `json:"out_of_memory"`
}

// caseWrapper turns a case's expr into a program: a stand-in for the
// pipeline's wrapper (B2b) that awaits the value and serialises it as JE-15
// says.
func caseWrapper(e string) string {
	return "(async () => { const v = await (async () => (" + e + "))(); return JSON.stringify(v ?? null); })()"
}

// outOfMemory is JE-3's reading of a thrown value: an InternalError whose
// text is exactly "out of memory", or a bare null or undefined throw.
func outOfMemory(ex *expr.Exception) bool {
	switch ex.Thrown {
	case expr.ThrownNull, expr.ThrownUndefined:
		return true
	case expr.ThrownError:
		return ex.Name == "InternalError" && ex.Message == "out of memory"
	}
	return false
}

var thrownNames = map[string]expr.Thrown{
	"error":     expr.ThrownError,
	"null":      expr.ThrownNull,
	"undefined": expr.ThrownUndefined,
	"other":     expr.ThrownOther,
}

func loadCaseFiles(t *testing.T) map[string]caseFile {
	t.Helper()
	paths, err := filepath.Glob("testdata/engine/*.json")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no case files: %v", err)
	}
	files := make(map[string]caseFile)
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		var f caseFile
		if err := dec.Decode(&f); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		files[filepath.Base(p)] = f
	}
	return files
}

func TestEngineCases(t *testing.T) {
	for name, f := range loadCaseFiles(t) {
		for _, c := range f.Cases {
			id := c.Spec
			if c.Variant != "" {
				id += "/" + c.Variant
			}
			t.Run(name+"/"+id, func(t *testing.T) {
				if c.Slow {
					if testing.Short() {
						t.Skip("slow case")
					}
					t.Parallel()
				}
				runEngineCase(t, c)
			})
		}
	}
}

func runEngineCase(t *testing.T, c engineCase) {
	limits := expr.DefaultLimits()
	if l := c.Given.Limits; l != nil {
		if l.MemoryMB != 0 {
			limits.MemoryMB = l.MemoryMB
		}
		if l.AttemptMS != 0 {
			limits.Attempt = time.Duration(l.AttemptMS) * time.Millisecond
		}
		if l.StackBytes != 0 {
			limits.StackBytes = l.StackBytes
		}
	}
	e := newEngine(t, Options{Limits: limits, Slots: 1})

	var source string
	switch g := c.Given; {
	case g.Expr != "":
		source = caseWrapper(g.Expr)
	case g.Source != "":
		source = g.Source
	case g.SourceGen != nil:
		source = g.SourceGen.text()
	default:
		t.Fatal("case gives no program")
	}
	p, err := e.Compile(source)
	if want := c.Expect.CompileException; want != nil {
		var ex *expr.Exception
		if !errors.As(err, &ex) {
			t.Fatalf("Compile: got %v, want an exception", err)
		}
		checkException(t, ex, want)
		return
	}
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	var in expr.Input
	for _, s := range c.Given.Strings {
		var text string
		switch {
		case s.Text != nil:
			text = *s.Text
		case s.Gen != nil:
			text = s.Gen.text()
		}
		in.Strings = append(in.Strings, expr.HostString{Name: s.Name, Text: []byte(text)})
	}

	start := time.Now()
	out, err := e.Eval(context.Background(), p, in)
	elapsed := time.Since(start)

	switch want := c.Expect; {
	case want.Value != nil:
		if err != nil {
			t.Fatalf("Eval: %v, want %s", err, *want.Value)
		}
		if string(out) != *want.Value {
			t.Fatalf("Eval = %s, want %s", out, *want.Value)
		}
	case want.Error == "attempt_timeout":
		if !errors.Is(err, expr.ErrAttemptTimeout) {
			t.Fatalf("Eval: got (%s, %v), want ErrAttemptTimeout", out, err)
		}
		// The interrupt fires at the limit; the slack allows for a loaded
		// runner.
		if elapsed < limits.Attempt || elapsed > limits.Attempt+5*time.Second {
			t.Fatalf("timed out after %v, want about %v", elapsed, limits.Attempt)
		}
	case want.Error == "not_string":
		if !errors.Is(err, expr.ErrNotString) {
			t.Fatalf("Eval: got (%s, %v), want ErrNotString", out, err)
		}
	case want.Exception != nil:
		var ex *expr.Exception
		if !errors.As(err, &ex) {
			t.Fatalf("Eval: got (%s, %v), want an exception", out, err)
		}
		checkException(t, ex, want.Exception)
	default:
		t.Fatal("case has no expectation")
	}
}

func checkException(t *testing.T, ex *expr.Exception, want *exceptionExpect) {
	t.Helper()
	if want.Thrown != "" && ex.Thrown != thrownNames[want.Thrown] {
		t.Errorf("thrown kind %d, want %s (%+v)", ex.Thrown, want.Thrown, ex)
	}
	if want.Name != "" && ex.Name != want.Name {
		t.Errorf("name %q, want %q", ex.Name, want.Name)
	}
	if want.Message != "" && ex.Message != want.Message {
		t.Errorf("message %q, want %q", ex.Message, want.Message)
	}
	if want.OutOfMemory != nil && outOfMemory(ex) != *want.OutOfMemory {
		t.Errorf("out of memory = %v, want %v (%+v)", outOfMemory(ex), *want.OutOfMemory, ex)
	}
}

// The catalogue cases of the findings whose limits B1a checks at the engine
// level must all be encoded, with the catalogue's provenance.
var engineFindings = regexp.MustCompile(`^- \*\*(JE-(?:1|2|3|48)/[a-z]+)\*\* · ([a-z-]+) ·`)

func TestEngineCasesCoverTheCatalogue(t *testing.T) {
	catalogue, err := os.ReadFile("../../../docs/semantics/conformance.md")
	if err != nil {
		t.Fatal(err)
	}
	want := make(map[string]string)
	for _, line := range strings.Split(string(catalogue), "\n") {
		if m := engineFindings.FindStringSubmatch(line); m != nil {
			want[m[1]] = m[2]
		}
	}
	if len(want) == 0 {
		t.Fatal("found no JE-1, JE-2, JE-3 or JE-48 cases in the catalogue")
	}
	deviations, err := os.ReadFile("../../../docs/design/0001-equivalence-contract.md")
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]bool)
	for name, f := range loadCaseFiles(t) {
		for _, c := range f.Cases {
			if !strings.HasPrefix(c.Spec, f.Finding) {
				t.Errorf("%s: case %s outside finding %s", name, c.Spec, f.Finding)
			}
			if c.Deviation != "" && !bytes.Contains(deviations, []byte("| "+c.Deviation+" |")) {
				t.Errorf("%s: case %s names unknown deviation %s", name, c.Spec, c.Deviation)
			}
			if prov, ok := want[c.Spec]; ok {
				got[c.Spec] = true
				if c.Provenance != prov {
					t.Errorf("%s: case %s has provenance %s, the catalogue says %s", name, c.Spec, c.Provenance, prov)
				}
			} else if strings.Contains(c.Spec, "/") {
				t.Errorf("%s: case %s is not in the catalogue", name, c.Spec)
			}
		}
	}
	for id := range want {
		if !got[id] {
			t.Errorf("catalogue case %s has no engine-level case", id)
		}
	}
}
