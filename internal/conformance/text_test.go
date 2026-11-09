package conformance

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

type textCases struct {
	About string `json:"about"`
	IDs   map[string]struct {
		ID   string `json:"id"`
		Path string `json:"path"`
	} `json:"ids"`
	Encode []struct {
		caseMeta
		Raw  string `json:"raw"`
		Want string `json:"want"`
	} `json:"encode"`
	Distinct []struct {
		caseMeta
		A string `json:"a"`
		B string `json:"b"`
	} `json:"distinct"`
}

func loadTextCases(t *testing.T) (textCases, map[string]SitePath, *strings.Replacer) {
	t.Helper()
	data, err := os.ReadFile("testdata/text/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var c textCases
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]SitePath)
	var pairs []string
	for name, v := range c.IDs {
		p, err := ParseSitePath(v.Path)
		if err != nil {
			t.Fatal(err)
		}
		ids[v.ID] = p
		pairs = append(pairs, "${"+name+"}", v.ID)
	}
	return c, ids, strings.NewReplacer(pairs...)
}

func normaliseText(raw string, ids map[string]SitePath) string {
	return EncodeText(SplitJobIDs(raw, ids))
}

// TestTextCases checks the escape of normalised text (OR-14, Q-OR-17): the
// expected encodings, and pairs of raw texts that must stay distinct.
func TestTextCases(t *testing.T) {
	c, ids, r := loadTextCases(t)
	for _, tc := range c.Encode {
		t.Run(tc.Name, func(t *testing.T) {
			tc.check(t, tc.Name)
			got := normaliseText(r.Replace(tc.Raw), ids)
			if got != tc.Want {
				t.Fatalf("got %q, want %q", got, tc.Want)
			}
			parts, err := ParseText(got)
			if err != nil {
				t.Fatal(err)
			}
			if again := EncodeText(parts); again != got {
				t.Fatalf("re-encoding the parsed parts gives %q, want %q", again, got)
			}
		})
	}
	for _, tc := range c.Distinct {
		t.Run(tc.Name, func(t *testing.T) {
			tc.check(t, tc.Name)
			a, b := normaliseText(r.Replace(tc.A), ids), normaliseText(r.Replace(tc.B), ids)
			if a == b {
				t.Fatalf("both normalise to %q", a)
			}
		})
	}
}

// TestTextMarkersDistinct checks that a marker written by the normaliser
// differs from user text spelling the same marker (OR-V3: a masked stack
// is not the user string "<stack>").
func TestTextMarkersDistinct(t *testing.T) {
	for _, m := range []string{MarkerStack, MarkerLoc, MarkerMasked} {
		marker := EncodeText([]TextPart{MarkerPart(m)})
		literal := EncodeText([]TextPart{Literal(m)})
		if marker != m || literal == marker {
			t.Errorf("marker %q encodes to %q, literal to %q", m, marker, literal)
		}
		parts, err := ParseText(marker)
		if err != nil || len(parts) != 1 || parts[0].Marker != m {
			t.Errorf("ParseText(%q) = %+v, %v", marker, parts, err)
		}
	}
	got := EncodeText([]TextPart{Literal("a <"), MarkerPart(MarkerLoc)})
	if got != "a <<<loc>" {
		t.Errorf("a literal < before a marker: got %q", got)
	}
}

// TestTextUnwalkedPlaceholder checks the placeholder of an unwalked job,
// <job:?N> (OR-14 item 1): written and read back like a walked job's, and
// distinct from user text that spells it.
func TestTextUnwalkedPlaceholder(t *testing.T) {
	// spec: OR-14/c
	const id = "01a1b47f-0fbb-67de-aaf0-dfc021fceabe"
	for _, c := range []struct{ raw, want string }{
		{id, "<job:?2>"},
		{"<job:?2>", "<<job:?2>"},
		{"<" + id, "<<<job:?2>"},
		{"job " + id + " of " + fuzzA, "job <job:?2> of <job:m:a>"},
	} {
		got := EncodeText(splitJobIDs(c.raw, fuzzIDs, map[string]int{id: 2}))
		if got != c.want {
			t.Errorf("%q: got %q, want %q", c.raw, got, c.want)
		}
		parts, err := ParseText(got)
		if err != nil {
			t.Fatalf("ParseText(%q): %v", got, err)
		}
		if again := EncodeText(parts); again != got {
			t.Errorf("re-encoding %q gives %q", got, again)
		}
	}
	if parts, err := ParseText("<job:?12>"); err != nil || len(parts) != 1 || parts[0].Unwalked != 12 {
		t.Errorf("ParseText(<job:?12>) = %+v, %v", parts, err)
	}
}

func TestParseTextRejects(t *testing.T) {
	for _, s := range []string{"<job:m:a", "<job:a>", "<job:m:a/>", "x<<<job:zz>", "<job:?>", "<job:?0>", "<job:?01>", "<job:?-1>", "<job:?+1>", "<job:?a>", "<job:?1/m:a>", "<job:??1>"} {
		if _, err := ParseText(s); err == nil {
			t.Errorf("ParseText(%q) accepted", s)
		}
	}
}

// fuzzIDs are the walked ids of the fuzz targets, and fuzzUnwalked the
// unwalked ones with their numbers. The fuzzer's bytes 0x01 and 0x02 stand
// for the walked ids and 0x03 for the unwalked one, so random inputs mix
// ids and text freely.
var fuzzIDs = map[string]SitePath{
	fuzzA:                                  SitePath{}.Child(Module("a")),
	"22222222-bbbb-4bbb-8bbb-bbbbbbbbbbbb": SitePath{}.Child(Module("a>b"), Iteration(0)),
}

const fuzzA = "11111111-aaaa-4aaa-8aaa-aaaaaaaaaaaa"

var fuzzUnwalked = map[string]int{"44444444-dddd-4ddd-8ddd-dddddddddddd": 1}

var fuzzReplacer = strings.NewReplacer("\x01", fuzzA, "\x02", "22222222-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "\x03", "44444444-dddd-4ddd-8ddd-dddddddddddd")

// fuzzNormalise normalises raw text with the fuzz targets' walked and
// unwalked ids.
func fuzzNormalise(raw string) string { return EncodeText(splitJobIDs(raw, fuzzIDs, fuzzUnwalked)) }

func fuzzSeeds(f *testing.F) {
	for _, s := range []string{"", "<", "<\x01", "<job:m:a>", "<<job:", "\x01\x02", "<stack>", "<<<loc>", "a<b>c", "<masked>\x01<", "<\x03", "<job:?1>\x03"} {
		f.Add(s)
	}
}

// FuzzTextRoundTrip: the normalised text parses back to the split parts.
func FuzzTextRoundTrip(f *testing.F) {
	fuzzSeeds(f)
	f.Fuzz(func(t *testing.T, s string) {
		parts := mergeLiterals(splitJobIDs(fuzzReplacer.Replace(s), fuzzIDs, fuzzUnwalked))
		enc := EncodeText(parts)
		got, err := ParseText(enc)
		if err != nil {
			t.Fatalf("ParseText(%q): %v", enc, err)
		}
		if len(parts) == 0 {
			parts = []TextPart{Literal("")}
		}
		if !reflect.DeepEqual(got, parts) {
			t.Fatalf("%q: parts %+v, parsed back %+v", enc, parts, got)
		}
	})
}

// FuzzTextInjective: two raw texts normalise alike only when they are the
// same text.
func FuzzTextInjective(f *testing.F) {
	f.Add("<\x01", "<job:m:a>")
	f.Add("\x02", "\x01b>")
	f.Add("<<\x01", "<<job:m:a>")
	f.Add("<\x03", "<job:?1>")
	f.Fuzz(func(t *testing.T, a, b string) {
		ra, rb := fuzzReplacer.Replace(a), fuzzReplacer.Replace(b)
		if ra != rb && fuzzNormalise(ra) == fuzzNormalise(rb) {
			t.Fatalf("%q and %q both normalise to %q", ra, rb, fuzzNormalise(ra))
		}
	})
}

// FuzzParseText: every accepted text re-encodes to itself.
func FuzzParseText(f *testing.F) {
	fuzzSeeds(f)
	f.Fuzz(func(t *testing.T, s string) {
		parts, err := ParseText(s)
		if err != nil {
			return
		}
		if enc := EncodeText(parts); enc != s {
			t.Fatalf("ParseText(%q) re-encodes to %q", s, enc)
		}
	})
}
