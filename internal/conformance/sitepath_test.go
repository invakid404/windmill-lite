package conformance

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// caseMeta is the part every case file entry shares: what it cites and its
// provenance (ADR 0003).
type caseMeta struct {
	Name       string   `json:"name"`
	Spec       []string `json:"spec"`
	Provenance string   `json:"provenance"`
}

func (m caseMeta) check(t *testing.T, label string) {
	t.Helper()
	if len(m.Spec) == 0 {
		t.Errorf("%s: case cites no spec ID", label)
	}
	if m.Provenance != "policy" {
		t.Errorf("%s: provenance %q, want policy (ADR 0011 decides the encoding)", label, m.Provenance)
	}
}

func loadCases(t *testing.T, name string, v any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "sitepath", name))
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// segmentCase is one segment in case-file notation: exactly one of module,
// iteration, branch, slot or attempt.
type segmentCase struct {
	Module     *string         `json:"module"`
	Occurrence *int            `json:"occurrence"`
	Iteration  *int            `json:"iteration"`
	Branch     json.RawMessage `json:"branch"`
	Slot       *string         `json:"slot"`
	Attempt    *int            `json:"attempt"`
}

func (c segmentCase) segment(t *testing.T) Segment {
	t.Helper()
	var segs []Segment
	if c.Module != nil {
		if c.Occurrence != nil {
			segs = append(segs, RepeatedModule(*c.Module, *c.Occurrence))
		} else {
			segs = append(segs, Module(*c.Module))
		}
	} else if c.Occurrence != nil {
		t.Fatal("occurrence without module")
	}
	if c.Iteration != nil {
		segs = append(segs, Iteration(*c.Iteration))
	}
	if c.Branch != nil {
		segs = append(segs, branchSegment(t, c.Branch))
	}
	if c.Slot != nil {
		switch *c.Slot {
		case "failure":
			segs = append(segs, FailureSlot())
		case "preprocessor":
			segs = append(segs, PreprocessorSlot())
		default:
			t.Fatalf("unknown slot %q", *c.Slot)
		}
	}
	if c.Attempt != nil {
		segs = append(segs, Attempt(*c.Attempt))
	}
	if len(segs) != 1 {
		t.Fatalf("segment case %+v sets %d kinds, want 1", c, len(segs))
	}
	return segs[0]
}

func branchSegment(t *testing.T, raw json.RawMessage) Segment {
	t.Helper()
	var name string
	if err := json.Unmarshal(raw, &name); err == nil {
		if name != "default" {
			t.Fatalf("unknown branch name %q", name)
		}
		return DefaultBranch()
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		t.Fatalf("branch %s: %v", raw, err)
	}
	return Branch(n)
}

func TestSitePathEncodeCases(t *testing.T) {
	var file struct {
		About string `json:"about"`
		Cases []struct {
			caseMeta
			Segments    []segmentCase `json:"segments"`
			Text        string        `json:"text"`
			Placeholder *string       `json:"placeholder"`
		} `json:"cases"`
	}
	loadCases(t, "encode.json", &file)
	if len(file.Cases) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range file.Cases {
		t.Run(c.Name, func(t *testing.T) {
			c.check(t, c.Name)
			segs := make([]Segment, len(c.Segments))
			for i, sc := range c.Segments {
				segs[i] = sc.segment(t)
			}
			p := SitePath{}.Child(segs...)
			if got := p.String(); got != c.Text {
				t.Fatalf("text = %q, want %q", got, c.Text)
			}
			if c.Placeholder != nil && p.Placeholder() != *c.Placeholder {
				t.Errorf("placeholder = %q, want %q", p.Placeholder(), *c.Placeholder)
			}
			if p.IsRoot() != (len(segs) == 0) {
				t.Errorf("IsRoot = %v with %d segments", p.IsRoot(), len(segs))
			}

			q, err := ParseSitePath(c.Text)
			if err != nil {
				t.Fatalf("ParseSitePath(%q): %v", c.Text, err)
			}
			if q != p {
				t.Errorf("parsed path %q != built path %q", q, p)
			}
			if got := q.Segments(); !reflect.DeepEqual(got, nilIfEmpty(segs)) {
				t.Errorf("Segments() = %v, want %v", got, segs)
			}

			// Building the path one segment at a time gives the same path.
			var stepwise SitePath
			for _, s := range segs {
				stepwise = stepwise.Child(s)
			}
			if stepwise != p {
				t.Errorf("stepwise path %q != %q", stepwise, p)
			}

			text, err := p.MarshalText()
			if err != nil {
				t.Fatal(err)
			}
			var r SitePath
			if err := r.UnmarshalText(text); err != nil || r != p {
				t.Errorf("text round trip: %q, %v", r, err)
			}
		})
	}
}

func nilIfEmpty(segs []Segment) []Segment {
	if len(segs) == 0 {
		return nil
	}
	return segs
}

func TestSitePathRejectCases(t *testing.T) {
	var file struct {
		About string `json:"about"`
		Cases []struct {
			Text       string   `json:"text"`
			Spec       []string `json:"spec"`
			Provenance string   `json:"provenance"`
			Why        string   `json:"why"`
		} `json:"cases"`
	}
	loadCases(t, "reject.json", &file)
	if len(file.Cases) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range file.Cases {
		t.Run(c.Text, func(t *testing.T) {
			caseMeta{Spec: c.Spec, Provenance: c.Provenance}.check(t, c.Text)
			if _, err := ParseSitePath(c.Text); !errors.Is(err, ErrInvalidSitePath) {
				t.Errorf("ParseSitePath(%q) = %v, want ErrInvalidSitePath (%s)", c.Text, err, c.Why)
			}
			var p SitePath
			if err := p.UnmarshalText([]byte(c.Text)); err == nil {
				t.Errorf("UnmarshalText(%q) accepted it (%s)", c.Text, c.Why)
			}
		})
	}
}

type flowCase struct {
	Modules      []nodeCase `json:"modules"`
	Failure      *nodeCase  `json:"failure_module"`
	Preprocessor *nodeCase  `json:"preprocessor_module"`
}

type nodeCase struct {
	ID             string       `json:"id"`
	Label          string       `json:"label"`
	FailedAttempts []nodeCase   `json:"failed_attempts"`
	Iterations     [][]nodeCase `json:"iterations"`
	Branches       []branchCase `json:"branches"`
	Flow           *flowCase    `json:"flow"`
}

type branchCase struct {
	Branch  json.RawMessage `json:"branch"`
	Modules []nodeCase      `json:"modules"`
}

// siteWalker assigns site paths to the labelled sites of a synthetic run the
// way the engine and the oracle walker must: occurrences per module list,
// slots by role, iterations, branches and attempts as typed segments.
type siteWalker struct {
	t     *testing.T
	sites map[string]SitePath
}

func (w *siteWalker) record(label string, p SitePath) {
	w.t.Helper()
	if label == "" {
		w.t.Fatalf("site %q has no label", p)
	}
	if _, dup := w.sites[label]; dup {
		w.t.Fatalf("label %q used twice", label)
	}
	w.sites[label] = p
}

func (w *siteWalker) flow(at SitePath, f *flowCase) {
	w.list(at, f.Modules)
	if f.Failure != nil {
		w.module(at.Child(FailureSlot()), *f.Failure)
	}
	if f.Preprocessor != nil {
		w.module(at.Child(PreprocessorSlot()), *f.Preprocessor)
	}
}

func (w *siteWalker) list(at SitePath, modules []nodeCase) {
	ids := make([]string, len(modules))
	for i, m := range modules {
		ids[i] = m.ID
	}
	for i, seg := range ModuleSegments(ids) {
		w.module(at.Child(seg), modules[i])
	}
}

func (w *siteWalker) module(at SitePath, m nodeCase) {
	w.record(m.Label, at)
	for k, attempt := range m.FailedAttempts {
		if len(attempt.FailedAttempts) > 0 || attempt.ID != "" {
			w.t.Fatalf("attempt %q may not have an id or attempts of its own", attempt.Label)
		}
		w.module(at.Child(Attempt(k)), attempt)
	}
	w.children(at, m)
}

func (w *siteWalker) children(at SitePath, m nodeCase) {
	for n, body := range m.Iterations {
		w.list(at.Child(Iteration(n)), body)
	}
	for _, br := range m.Branches {
		w.list(at.Child(branchSegment(w.t, br.Branch)), br.Modules)
	}
	if m.Flow != nil {
		w.flow(at, m.Flow)
	}
}

func TestSitePathFlowCases(t *testing.T) {
	var file struct {
		About string `json:"about"`
		Cases []struct {
			caseMeta
			Flow   flowCase          `json:"flow"`
			Expect map[string]string `json:"expect"`
			Swap   []string          `json:"swap"`
		} `json:"cases"`
	}
	loadCases(t, "flows.json", &file)
	if len(file.Cases) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range file.Cases {
		t.Run(c.Name, func(t *testing.T) {
			c.check(t, c.Name)
			w := &siteWalker{t: t, sites: map[string]SitePath{}}
			w.flow(SitePath{}, &c.Flow)

			got := make(map[string]string, len(w.sites))
			for label, p := range w.sites {
				got[label] = p.String()
			}
			if !reflect.DeepEqual(got, c.Expect) {
				t.Fatalf("site paths:\n got  %v\n want %v", got, c.Expect)
			}

			// Distinct sites get distinct paths and distinct placeholders,
			// and every path parses back to itself.
			byPath := map[SitePath]string{}
			byPlaceholder := map[string]string{}
			for label, p := range w.sites {
				if other, dup := byPath[p]; dup {
					t.Errorf("sites %q and %q share path %q", label, other, p)
				}
				byPath[p] = label
				ph := p.Placeholder()
				if other, dup := byPlaceholder[ph]; dup {
					t.Errorf("sites %q and %q share placeholder %q", label, other, ph)
				}
				byPlaceholder[ph] = label
				if q, err := ParseSitePath(p.String()); err != nil || q != p {
					t.Errorf("ParseSitePath(%q) = %q, %v", p, q, err)
				}
			}

			// Every site survives the projection as its own record.
			obs := observationOf(w.sites, func(label string) string { return label })
			if len(obs.Steps) != len(w.sites) {
				t.Fatalf("projection kept %d of %d sites", len(obs.Steps), len(w.sites))
			}
			encoded := mustMarshal(t, obs)
			var decoded Observation
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(decoded, obs) {
				t.Errorf("observation JSON round trip changed it:\n%s", encoded)
			}

			if len(c.Swap) == 0 {
				return
			}
			if len(c.Swap) != 2 {
				t.Fatalf("swap names %d sites, want 2", len(c.Swap))
			}
			x, y := c.Swap[0], c.Swap[1]
			// Swapping the two sites' results changes the observation.
			swapped := observationOf(w.sites, func(label string) string {
				switch label {
				case x:
					return y
				case y:
					return x
				}
				return label
			})
			if bytes.Equal(mustMarshal(t, swapped), encoded) {
				t.Errorf("swapping the results of %q and %q left the observation unchanged", x, y)
			}
			// A result that references one site's job differs from one that
			// references the other's.
			refX := observationOf(w.sites, func(string) string { return "ran " + w.sites[x].Placeholder() })
			refY := observationOf(w.sites, func(string) string { return "ran " + w.sites[y].Placeholder() })
			if bytes.Equal(mustMarshal(t, refX), mustMarshal(t, refY)) {
				t.Errorf("references to %q and %q normalise to the same observation", x, y)
			}
		})
	}
}

// observationOf builds an observation with one successful step per site,
// whose result is the JSON string result(label).
func observationOf(sites map[string]SitePath, result func(label string) string) Observation {
	obs := Observation{Status: RunSuccess, Result: json.RawMessage(`null`), Steps: map[SitePath]StepObservation{}}
	for label, p := range sites {
		text, _ := json.Marshal(result(label))
		obs.Steps[p] = StepObservation{Status: StepSuccess, Result: text}
	}
	return obs
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestPlaceholderStaysDelimitedInsideStrings(t *testing.T) {
	// A string holding a's job id followed by "b>" must not normalise to
	// the same text as a string holding the job id of a module named "a>b".
	a := SitePath{}.Child(Module("a"))
	ab := SitePath{}.Child(Module("a>b"))
	if a.Placeholder()+"b>" == ab.Placeholder() {
		t.Fatalf("placeholders collide: %q", ab.Placeholder())
	}
	if strings.Count(ab.Placeholder(), ">") != 1 {
		t.Errorf("placeholder %q has a > before its end", ab.Placeholder())
	}
}

func TestModuleSegments(t *testing.T) {
	got := ModuleSegments([]string{"a", "b", "a", "c", "a", "b"})
	want := []Segment{
		RepeatedModule("a", 0), RepeatedModule("b", 0), RepeatedModule("a", 1),
		Module("c"), RepeatedModule("a", 2), RepeatedModule("b", 1),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ModuleSegments = %v, want %v", got, want)
	}
	if got := ModuleSegments(nil); len(got) != 0 {
		t.Errorf("ModuleSegments(nil) = %v", got)
	}
}

func TestSegmentAccessors(t *testing.T) {
	tests := []struct {
		seg    Segment
		kind   SegmentKind
		value  string
		occ    int
		hasOcc bool
		index  int
		hasIdx bool
	}{
		{Module("a/b"), KindModule, "a/b", 0, false, 0, false},
		{RepeatedModule("a", 3), KindModule, "a", 3, true, 0, false},
		{Iteration(7), KindIteration, "7", 0, false, 7, true},
		{Branch(2), KindBranch, "2", 0, false, 2, true},
		{DefaultBranch(), KindBranch, "default", 0, false, 0, false},
		{FailureSlot(), KindSlot, "failure", 0, false, 0, false},
		{PreprocessorSlot(), KindSlot, "preprocessor", 0, false, 0, false},
		{Attempt(0), KindAttempt, "0", 0, false, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.seg.String(), func(t *testing.T) {
			if tt.seg.Kind() != tt.kind || tt.seg.Value() != tt.value {
				t.Errorf("kind, value = %c, %q", tt.seg.Kind(), tt.seg.Value())
			}
			if occ, ok := tt.seg.Occurrence(); occ != tt.occ || ok != tt.hasOcc {
				t.Errorf("Occurrence() = %d, %v", occ, ok)
			}
			if n, ok := tt.seg.Index(); n != tt.index || ok != tt.hasIdx {
				t.Errorf("Index() = %d, %v", n, ok)
			}
		})
	}
}

func TestConstructorsRejectNegativeNumbers(t *testing.T) {
	for name, f := range map[string]func(){
		"RepeatedModule": func() { RepeatedModule("a", -1) },
		"Iteration":      func() { Iteration(-1) },
		"Branch":         func() { Branch(-1) },
		"Attempt":        func() { Attempt(-1) },
		"zero Segment":   func() { SitePath{}.Child(Segment{}) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("did not panic")
				}
			}()
			f()
		})
	}
}

func TestMarshalTextRejectsInvalidUTF8(t *testing.T) {
	p := SitePath{}.Child(Module("a\xffb"))
	if _, err := p.MarshalText(); err == nil {
		t.Fatal("MarshalText accepted invalid UTF-8")
	}
	// The text itself still round-trips through the parser.
	if q, err := ParseSitePath(p.String()); err != nil || q != p {
		t.Errorf("ParseSitePath = %q, %v", q, err)
	}
}

// FuzzParseSitePath checks that the parser accepts only canonical text:
// whatever it accepts re-encodes to the same text.
func FuzzParseSitePath(f *testing.F) {
	for _, seed := range []string{
		"", "m:a", "m:a~0", "m:a%2Fb", "m:a%252Fb", "m:outer/i:1/m:inner/i:0/m:s",
		"s:failure", "b:default", "m:a/r:0", "m:a%2fb", "i:01", "m:a~1~2", "m:%25%2F%3A%7E",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) {
		p, err := ParseSitePath(text)
		if err != nil {
			return
		}
		if p.String() != text {
			t.Fatalf("ParseSitePath(%q) kept %q", text, p.String())
		}
		if re := (SitePath{}).Child(p.Segments()...); re != p {
			t.Fatalf("re-encoding %q gave %q", text, re)
		}
	})
}

// FuzzModuleSegmentInjective checks that distinct module segments always get
// distinct texts and placeholders, whatever the ids hold, and that each text
// parses back to its segment.
func FuzzModuleSegmentInjective(f *testing.F) {
	for _, seed := range [][2]string{
		{"a/b", "a%2Fb"}, {"failure", "failure"}, {"a~0", "a"}, {"a:b", "a%3Ab"},
		{"%", "%25"}, {"a>b", "a"}, {"", "~"},
	} {
		f.Add(seed[0], seed[1], uint8(0), uint8(1))
	}
	f.Fuzz(func(t *testing.T, id1, id2 string, k1, k2 uint8) {
		segs := []Segment{
			Module(id1), Module(id2),
			RepeatedModule(id1, int(k1)), RepeatedModule(id2, int(k2)),
		}
		for i, a := range segs {
			pa := SitePath{}.Child(a)
			if got := pa.Segments(); len(got) != 1 || got[0] != a {
				t.Fatalf("%q parsed back as %v, want %v", pa, got, a)
			}
			for _, b := range segs[i+1:] {
				pb := SitePath{}.Child(b)
				if (a == b) != (pa == pb) {
					t.Fatalf("segments %#v and %#v: equal=%v but texts %q and %q", a, b, a == b, pa, pb)
				}
				if (a == b) != (pa.Placeholder() == pb.Placeholder()) {
					t.Fatalf("segments %#v and %#v: placeholders %q and %q", a, b, pa.Placeholder(), pb.Placeholder())
				}
			}
		}
	})
}

// segmentsFromBytes decodes fuzz input into a segment sequence, so the fuzzer
// explores multi-segment paths of every kind with arbitrary module ids.
func segmentsFromBytes(data []byte) []Segment {
	var segs []Segment
	next := func() (byte, bool) {
		if len(data) == 0 {
			return 0, false
		}
		b := data[0]
		data = data[1:]
		return b, true
	}
	for {
		op, ok := next()
		if !ok {
			return segs
		}
		n, _ := next()
		switch op % 8 {
		case 0, 1, 2: // modules are the most common segment
			size := int(n % 8)
			if size > len(data) {
				size = len(data)
			}
			id := string(data[:size])
			data = data[size:]
			if op%8 == 2 {
				segs = append(segs, RepeatedModule(id, int(n/8)))
			} else {
				segs = append(segs, Module(id))
			}
		case 3:
			segs = append(segs, Iteration(int(n)))
		case 4:
			if n%4 == 0 {
				segs = append(segs, DefaultBranch())
			} else {
				segs = append(segs, Branch(int(n)))
			}
		case 5:
			segs = append(segs, FailureSlot())
		case 6:
			segs = append(segs, PreprocessorSlot())
		case 7:
			segs = append(segs, Attempt(int(n)))
		}
	}
}

// FuzzSitePathInjective checks whole paths: every sequence of segments
// parses back to itself, and two sequences share a text or a placeholder
// only when they are equal.
func FuzzSitePathInjective(f *testing.F) {
	for _, seed := range [][2]string{
		{"\x00\x01a\x03\x01", "\x00\x01a\x03\x02"},
		{"\x00\x03a/b", "\x00\x05a%2Fb"},
		{"\x05\x00", "\x00\x07failure"},
		{"\x06\x00", "\x00\x0cpreprocessor"},
		{"\x02\x01a", "\x00\x03a~0"},
		{"\x00\x01a\x00\x01b", "\x00\x03a/b"},
		{"\x04\x00\x04\x01", "\x04\x04"},
	} {
		f.Add([]byte(seed[0]), []byte(seed[1]))
	}
	f.Fuzz(func(t *testing.T, a, b []byte) {
		sa, sb := segmentsFromBytes(a), segmentsFromBytes(b)
		pa, pb := SitePath{}.Child(sa...), SitePath{}.Child(sb...)
		for _, c := range []struct {
			segs []Segment
			p    SitePath
		}{{sa, pa}, {sb, pb}} {
			if got := c.p.Segments(); !reflect.DeepEqual(got, nilIfEmpty(c.segs)) {
				t.Fatalf("%q parsed back as %v, want %v", c.p, got, c.segs)
			}
			if q, err := ParseSitePath(c.p.String()); err != nil || q != c.p {
				t.Fatalf("ParseSitePath(%q) = %q, %v", c.p, q, err)
			}
		}
		same := reflect.DeepEqual(nilIfEmpty(sa), nilIfEmpty(sb))
		if same != (pa == pb) {
			t.Fatalf("segments %v and %v: equal=%v, texts %q and %q", sa, sb, same, pa, pb)
		}
		if same != (pa.Placeholder() == pb.Placeholder()) {
			t.Fatalf("segments %v and %v: equal=%v, placeholders %q and %q", sa, sb, same, pa.Placeholder(), pb.Placeholder())
		}
	})
}
