package conformance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

type compareCase struct {
	caseMeta
	Options struct {
		Subset         bool     `json:"subset"`
		StrictKeyOrder bool     `json:"strict_key_order"`
		Unordered      []string `json:"unordered"`
		WithRecording  bool     `json:"with_recording"`
	} `json:"options"`
	Got         Observation `json:"got"`
	Want        Observation `json:"want"`
	Differences []string    `json:"differences"`
}

// TestCompareCases runs testdata/compare/cases.json.
func TestCompareCases(t *testing.T) {
	data, err := os.ReadFile("testdata/compare/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		About string        `json:"about"`
		Cases []compareCase `json:"cases"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		t.Fatal(err)
	}
	for _, c := range file.Cases {
		t.Run(c.Name, func(t *testing.T) {
			if len(c.Spec) == 0 {
				t.Fatal("case cites no spec ID")
			}
			if !validProvenances[c.Provenance] {
				t.Fatalf("provenance %q", c.Provenance)
			}
			diffs, err := Compare(c.Got, c.Want, CompareOptions{
				Subset:         c.Options.Subset,
				StrictKeyOrder: c.Options.StrictKeyOrder,
				Unordered:      c.Options.Unordered,
				WithRecording:  c.Options.WithRecording,
			})
			if err != nil {
				t.Fatal(err)
			}
			got := make([]string, 0, len(diffs))
			for _, d := range diffs {
				got = append(got, d.String())
			}
			if !reflect.DeepEqual(got, c.Differences) {
				t.Fatalf("differences:\n got  %q\n want %q", got, c.Differences)
			}
		})
	}
}

func TestPointers(t *testing.T) {
	for _, p := range []string{"", "/a", "/steps/m:l~1i:0~1m:x/result", "/a~0b/0", "/"} {
		toks, err := SplitPointer(p)
		if err != nil {
			t.Fatalf("%q: %v", p, err)
		}
		if JoinPointer(toks) != p {
			t.Errorf("%q round-trips to %q", p, JoinPointer(toks))
		}
	}
	toks, _ := SplitPointer("/steps/m:l~1i:0~1m:x")
	if !reflect.DeepEqual(toks, []string{"steps", "m:l/i:0/m:x"}) {
		t.Errorf("tokens %q", toks)
	}
	for _, p := range []string{"a", "/a~2", "/a~"} {
		if _, err := SplitPointer(p); err == nil {
			t.Errorf("%q accepted", p)
		}
	}
}

func TestIndentJSONKeepsOrderAndText(t *testing.T) {
	in := `{"z":1.0,"a":["<stack>",{"y":null,"b":true}],"e":{},"f":[]}`
	out, err := IndentJSON([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"z\": 1.0,\n  \"a\": [\n    \"<stack>\",\n    {\n      \"y\": null,\n      \"b\": true\n    }\n  ],\n  \"e\": {},\n  \"f\": []\n}\n"
	if string(out) != want {
		t.Fatalf("got\n%s\nwant\n%s", out, want)
	}
}

// An exact comparison of an unordered array counts canonical texts; the
// complete matching must give the same answer on every input, under either
// key-order policy. The inputs are permutations of few distinct values,
// with object members reshuffled and sometimes one element replaced, so
// both answers are common.
func TestUnorderedCountingAgreesWithMatching(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for _, strict := range []bool{false, true} {
		c := comparer{opt: CompareOptions{StrictKeyOrder: strict}, unordered: map[string]bool{"/result": true}}
		var equal, different int
		for range 20000 {
			want := make([]*jnode, r.IntN(7))
			for j := range want {
				want[j] = randomNode(t, r, 3)
			}
			got := make([]*jnode, len(want))
			for i, w := range want {
				got[i] = reshuffled(r, w)
			}
			r.Shuffle(len(got), func(i, j int) { got[i], got[j] = got[j], got[i] })
			if len(got) > 0 && r.IntN(2) == 0 {
				got[r.IntN(len(got))] = randomNode(t, r, 3)
			}
			counted, matched := c.sameCanonical(got, want), c.match([]string{"result"}, got, want)
			if counted != matched {
				t.Fatalf("strict %v: counting says %v, matching %v\n got  %s\n want %s", strict, counted, matched, elemsText(got), elemsText(want))
			}
			if counted {
				equal++
			} else {
				different++
			}
		}
		if equal < 2000 || different < 2000 {
			t.Fatalf("strict %v: %d equal and %d different inputs; the generator lost its mix", strict, equal, different)
		}
		t.Logf("strict %v: %d equal and %d different inputs agree", strict, equal, different)
	}
}

// A subset comparison of an unordered array matches groups of
// interchangeable elements; the complete matching of the elements must give
// the same answer on every input. The actual elements are the expected ones
// with members added, reshuffled and sometimes one replaced, so an expected
// element often equals several actual ones, and a greedy pairing would
// often fail where a complete matching exists.
func TestUnorderedSubsetGroupsAgreeWithMatching(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	c := comparer{opt: CompareOptions{Subset: true}, unordered: map[string]bool{"/result": true}}
	var equal, different int
	for range 20000 {
		want := make([]*jnode, r.IntN(7))
		for j := range want {
			want[j] = randomNode(t, r, 3)
		}
		got := make([]*jnode, len(want))
		for i, w := range want {
			got[i] = reshuffled(r, widened(t, r, w))
		}
		r.Shuffle(len(got), func(i, j int) { got[i], got[j] = got[j], got[i] })
		if len(got) > 0 && r.IntN(3) == 0 {
			got[r.IntN(len(got))] = randomNode(t, r, 3)
		}
		grouped, matched := c.matchGroups([]string{"result"}, got, want), c.match([]string{"result"}, got, want)
		if grouped != matched {
			t.Fatalf("grouping says %v, matching %v\n got  %s\n want %s", grouped, matched, elemsText(got), elemsText(want))
		}
		if grouped {
			equal++
		} else {
			different++
		}
	}
	if equal < 2000 || different < 2000 {
		t.Fatalf("%d equal and %d different inputs; the generator lost its mix", equal, different)
	}
	t.Logf("%d equal and %d different inputs agree", equal, different)
}

// widened copies n with a random member added to some of its objects, at
// any depth, so that n is a subset of the copy.
func widened(t *testing.T, r *rand.Rand, n *jnode) *jnode {
	c := *n
	c.members = slices.Clone(n.members)
	for i := range c.members {
		c.members[i].value = widened(t, r, c.members[i].value)
	}
	if n.kind == kindObject && r.IntN(2) == 0 {
		key := []string{"a", "b", "c", "d"}[r.IntN(4)]
		if c.member(key) == nil {
			c.members = append(c.members, jmember{key: key, value: randomNode(t, r, 1)})
		}
	}
	c.elems = slices.Clone(n.elems)
	for i := range c.elems {
		c.elems[i] = widened(t, r, c.elems[i])
	}
	return &c
}

// randomNode parses a small random JSON value, built from few enough
// choices that equal values are common.
func randomNode(t *testing.T, r *rand.Rand, depth int) *jnode {
	t.Helper()
	n, err := parseJSON([]byte(randomJSON(r, depth)))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func randomJSON(r *rand.Rand, depth int) string {
	scalars := []string{`0`, `1`, `1.0`, `"1"`, `"a"`, `true`, `null`}
	switch k := r.IntN(10); {
	case depth == 0 || k < 6:
		return scalars[r.IntN(len(scalars))]
	case k < 8:
		elems := make([]string, r.IntN(3))
		for i := range elems {
			elems[i] = randomJSON(r, depth-1)
		}
		return "[" + strings.Join(elems, ",") + "]"
	default:
		var members []string
		for _, key := range []string{"a", "b", "c"} {
			if r.IntN(3) > 0 {
				members = append(members, fmt.Sprintf("%q:%s", key, randomJSON(r, depth-1)))
			}
		}
		return "{" + strings.Join(members, ",") + "}"
	}
}

// reshuffled copies n with the members of every object in a random order.
func reshuffled(r *rand.Rand, n *jnode) *jnode {
	c := *n
	c.members = slices.Clone(n.members)
	for i := range c.members {
		c.members[i].value = reshuffled(r, c.members[i].value)
	}
	r.Shuffle(len(c.members), func(i, j int) { c.members[i], c.members[j] = c.members[j], c.members[i] })
	c.elems = slices.Clone(n.elems)
	for i := range c.elems {
		c.elems[i] = reshuffled(r, c.elems[i])
	}
	return &c
}

func elemsText(elems []*jnode) string {
	return string((&jnode{kind: kindArray, elems: elems}).raw())
}

// BenchmarkCompareUnordered compares arrays at an unordered pointer
// (OR-23): repeated scalars, the worst case of a complete matching of the
// elements (E2 review item P3-7), and distinct objects in reverse order.
// Exact comparisons count the elements' canonical texts; subset
// comparisons match groups of equal elements, and the actual objects carry
// a member the expected ones lack.
func BenchmarkCompareUnordered(b *testing.B) {
	repeated := func(n int) string { return "[" + strings.Repeat("0,", n-1) + "0]" }
	repeatedObjects := func(n int, extra string) string {
		return "[" + strings.Repeat(`{"id":1`+extra+`},`, n-1) + `{"id":1` + extra + `}]`
	}
	objects := func(n int, reverse bool, extra string) string {
		var s strings.Builder
		s.WriteByte('[')
		for k := range n {
			i := k
			if reverse {
				i = n - 1 - k
			}
			if k > 0 {
				s.WriteByte(',')
			}
			fmt.Fprintf(&s, `{"id":%d,"tags":["x","y"],"meta":{"z":true,"a":null}%s}`, i, extra)
		}
		s.WriteByte(']')
		return s.String()
	}
	for _, bc := range []struct {
		name      string
		got, want string
		subset    bool
	}{
		{"exact/repeated-scalars/500", repeated(500), repeated(500), false},
		{"exact/repeated-scalars/2000", repeated(2000), repeated(2000), false},
		{"exact/distinct-objects/500", objects(500, true, ""), objects(500, false, ""), false},
		{"subset/repeated-scalars/500", repeated(500), repeated(500), true},
		{"subset/repeated-scalars/2000", repeated(2000), repeated(2000), true},
		{"subset/repeated-objects/500", repeatedObjects(500, `,"x":2`), repeatedObjects(500, ""), true},
		{"subset/distinct-objects/500", objects(500, true, `,"x":2`), objects(500, false, ""), true},
	} {
		got := Observation{Status: RunSuccess, Result: json.RawMessage(bc.got)}
		want := Observation{Status: RunSuccess, Result: json.RawMessage(bc.want)}
		opt := CompareOptions{Subset: bc.subset, Unordered: []string{"/result"}}
		b.Run(bc.name, func(b *testing.B) {
			for b.Loop() {
				if diffs, err := Compare(got, want, opt); err != nil || len(diffs) != 0 {
					b.Fatal(diffs, err)
				}
			}
		})
	}
}
