package expr

import (
	"bytes"
	"encoding/json"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// refMember is member's test implementation: a decoded map of raw member
// texts, which keeps the last of duplicate keys and each text verbatim.
func refMember(obj []byte, key string) ([]byte, bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(obj, &m) != nil || m == nil {
		return nil, false
	}
	v, ok := m[key]
	return v, ok
}

// refWalk is walk's test implementation: it decodes the text and follows
// the path through Go values, reading an array index as #> does.
func refWalk(text []byte, path []string) (any, bool) {
	d := json.NewDecoder(bytes.NewReader(text))
	d.UseNumber()
	var v any
	if d.Decode(&v) != nil {
		return nil, false
	}
	for _, p := range path {
		switch c := v.(type) {
		case map[string]any:
			x, ok := c[p]
			if !ok {
				return nil, false
			}
			v = x
		case []any:
			n, err := strconv.ParseInt(p, 10, 32)
			if err != nil || n < 0 || n >= int64(len(c)) {
				return nil, false
			}
			v = c[n]
		default:
			return nil, false
		}
	}
	return v, true
}

func stored(t *testing.T, text []byte) string {
	t.Helper()
	s, err := jsonval.FromProducer(text).Stored()
	if err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	return string(s)
}

func TestMember(t *testing.T) {
	for _, tc := range []struct {
		obj, key, want string
		ok             bool
	}{
		{`{"a":1}`, "a", "1", true},
		{` { "a" : [1, {"x": "}"}] , "b":2}`, "a", `[1, {"x": "}"}]`, true},
		{`{"a":1,"a":2}`, "a", "2", true},
		{`{"a.b":"esc"}`, "a.b", `"esc"`, true},
		{`{"a.b":"lit","a":{"b":1}}`, "a.b", `"lit"`, true},
		{`{"":1}`, "", "1", true},
		{`{"a":1}`, "b", "", false},
		{`[1]`, "0", "", false},
		{`"a"`, "a", "", false},
		{`{}`, "a", "", false},
		{`{"a":}`, "a", "", false},
		{`{"a":1`, "a", "", false},
	} {
		got, ok := member([]byte(tc.obj), tc.key)
		if ok != tc.ok || string(got) != tc.want {
			t.Errorf("member(%s, %q) = %s, %v; want %s, %v", tc.obj, tc.key, got, ok, tc.want, tc.ok)
		}
	}
}

func TestDatabaseText(t *testing.T) {
	for in, want := range map[string]string{
		`{"a":1,"b":[1,2]}`:         `{"a": 1, "b": [1, 2]}`,
		`[{},[],{"c":{}}]`:          `[{}, [], {"c": {}}]`,
		`"a, b: c"`:                 `"a, b: c"`,
		`{"k\"e:y,":"v\\",",":":"}`: `{"k\"e:y,": "v\\", ",": ":"}`,
		`12.50`:                     `12.50`,
		`null`:                      `null`,
	} {
		if got := string(databaseText([]byte(in))); got != want {
			t.Errorf("databaseText(%s) = %s, want %s", in, got, want)
		}
	}
}

// TestWalkAgainstReference compares member, walk and databaseText with
// their test implementations on generated values and paths.
func TestWalkAgainstReference(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	keys := []string{"a", "b", "0", "1", "length", "x_1", "01", "2147483648"}
	for i := range 3000 {
		var b strings.Builder
		genJSON(r, &b, 0)
		text := []byte(b.String())
		st := []byte(stored(t, text))
		path := make([]string, r.IntN(4))
		for j := range path {
			path[j] = keys[r.IntN(len(keys))]
		}
		got, ok := walk(st, path)
		want, wok := refWalk(st, path)
		if ok != wok {
			t.Fatalf("case %d: walk(%s, %q) found %v, the reference %v", i, st, path, ok, wok)
		}
		if ok {
			wt, err := json.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			if stored(t, got) != stored(t, wt) {
				t.Fatalf("case %d: walk(%s, %q) = %s, the reference %s", i, st, path, got, wt)
			}
			// jsonb's text output parses to the same value.
			if stored(t, databaseText(got)) != stored(t, got) {
				t.Fatalf("case %d: databaseText(%s) = %s parses differently", i, got, databaseText(got))
			}
		}
		if len(path) > 0 {
			gm, gok := member(text, path[0])
			wm, wmok := refMember(text, path[0])
			if gok != wmok || !bytes.Equal(gm, wm) {
				t.Fatalf("case %d: member(%s, %q) = %s, %v; the reference %s, %v", i, text, path[0], gm, gok, wm, wmok)
			}
		}
	}
}

// genJSON writes a random JSON value with random spacing and duplicate
// keys.
func genJSON(r *rand.Rand, b *strings.Builder, depth int) {
	sp := func() {
		if r.IntN(3) == 0 {
			b.WriteString([]string{" ", "\n", "\t ", "  "}[r.IntN(4)])
		}
	}
	keys := []string{"a", "b", "0", "1", "length", "x_1", "01", "a.b", `q\"`, `a`}
	n := r.IntN(10)
	if depth > 3 {
		n = 6 + r.IntN(4)
	}
	switch n {
	case 0, 1:
		b.WriteString("{")
		for i := range r.IntN(4) {
			if i > 0 {
				b.WriteString(",")
			}
			sp()
			b.WriteString(`"` + keys[r.IntN(len(keys))] + `"`)
			sp()
			b.WriteString(":")
			sp()
			genJSON(r, b, depth+1)
			sp()
		}
		b.WriteString("}")
	case 2, 3:
		b.WriteString("[")
		for i := range r.IntN(4) {
			if i > 0 {
				b.WriteString(",")
			}
			sp()
			genJSON(r, b, depth+1)
			sp()
		}
		b.WriteString("]")
	case 6:
		b.WriteString([]string{"true", "false", "null"}[r.IntN(3)])
	case 7:
		b.WriteString([]string{`"x"`, `"a, b: c"`, `"}]"`, `"\"\\"`, `""`}[r.IntN(5)])
	default:
		b.WriteString([]string{"0", "-1", "1.50", "1e3", "12345678901234567890", "-0.0"}[r.IntN(6)])
	}
}

// FuzzJSONText checks that the scanners never panic and that walk agrees
// with the reference on any stored text.
func FuzzJSONText(f *testing.F) {
	for _, s := range []string{`{"a":[1,{"b":"x"}]}`, `[1,2]`, `"s"`, `{"a":`, `[`, `{"a.":1}`} {
		f.Add(s, "a", "1")
	}
	f.Fuzz(func(t *testing.T, text, p0, p1 string) {
		member([]byte(text), p0)
		element([]byte(text), len(p1))
		databaseText([]byte(text))
		st, err := jsonval.FromProducer([]byte(text)).Stored()
		if err != nil {
			return
		}
		path := []string{p0, p1}
		got, ok := walk(st, path)
		want, wok := refWalk(st, path)
		if ok != wok {
			t.Fatalf("walk(%s, %q) found %v, the reference %v", st, path, ok, wok)
		}
		if ok {
			wt, _ := json.Marshal(want)
			if stored(t, got) != stored(t, wt) {
				t.Fatalf("walk(%s, %q) = %s, the reference %s", st, path, got, wt)
			}
		}
	})
}
