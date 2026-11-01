package bench

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// j spells a probe's bytes: @u is a backslash-u escape's start, @L and @P
// are raw U+2028 and U+2029, @FF is the byte 0xff (invalid UTF-8), @01 the
// byte 0x01 and @BOM a UTF-8 byte order mark.
func j(s string) []byte {
	return []byte(strings.NewReplacer(
		"@u", "\\u", "@L", string(rune(0x2028)), "@P", string(rune(0x2029)),
		"@FF", "\xff", "@01", "\x01", "@BOM", "\xef\xbb\xbf",
	).Replace(s))
}

// probes are the inputs the exactness test feeds every operation.
var probes = []struct{ name, text string }{
	{"int-above-2^64", `{"n":12345678901234567890}`},
	{"int-2^53+1", `{"n":9007199254740993}`},
	{"neg-zero", `{"n":-0}`},
	{"neg-zero-float", `{"n":-0.0}`},
	{"exp-overflow", `{"n":1E400}`},
	{"exp-1e21", `{"n":1e21}`},
	{"exp-small", `{"n":1.5e-7}`},
	{"trailing-zero", `{"n":1.50}`},
	{"long-decimal", `{"n":123456789012345678901234567890.123456789}`},
	{"nan", `{"n":NaN}`},
	{"infinity", `{"n":Infinity}`},
	{"leading-zero", `{"n":01}`},
	{"html", `{"s":"<b>&amp;</b>"}`},
	{"u2028-raw", `{"s":"a@Lb@Pc"}`},
	{"u2028-escaped", `{"s":"a@u2028b@u2029c"}`},
	{"invalid-utf8-value", `{"s":"a@FFb"}`},
	{"invalid-utf8-key", `{"@FF":1}`},
	{"lone-surrogate", `{"s":"@ud800x"}`},
	{"escapes", `{"s":"@u00e9\/@u0041\t"}`},
	{"control-raw", `{"s":"a@01b"}`},
	{"duplicate", `{"a":1,"a":2}`},
	{"duplicate-apart", `{"a":1,"b":2,"a":3}`},
	{"key-order", `{"b":1,"a":2,"aa":3,"B":4}`},
	{"whitespace", ` { "a" : [ 1 , 2 ] , "b" : { } } `},
	{"trailing-garbage", `{"a":1} x`},
	{"two-values", `{"a":1}{"b":2}`},
	{"trailing-comma", `{"a":1,}`},
	{"empty", ``},
	{"null", `null`},
	{"string", `"str"`},
	{"array", `[1,2]`},
	{"bom", `@BOM{}`},
	{"depth-5000", strings.Repeat("[", 5000) + strings.Repeat("]", 5000)},
	{"depth-10001", strings.Repeat("[", 10001) + strings.Repeat("]", 10001)},
	{"case-fields", `{"ERROR":{"NAME":"Fatal"},"Decline":true}`},
	{"type-mismatch", `{"error":{"name":7}}`},
}

// marshalProbes are Go values the exactness test marshals.
var marshalProbes = []struct {
	name string
	v    any
}{
	{"map-order-html", map[string]any{"b": 1, "a": 2, "<": "&", "B": 3}},
	{"neg-zero", math.Copysign(0, -1)},
	{"float-1e21", 1e21},
	{"float-1e20", 1e20},
	{"float-1.5e-7", 1.5e-7},
	{"float-1e-6", 1e-6},
	{"float-0.1", 0.1},
	{"float-min", 5e-324},
	{"float-max", math.MaxFloat64},
	{"float-2^53+2", float64(1<<53 + 2)},
	{"float32", float32(0.1)},
	{"nan", math.NaN()},
	{"inf", math.Inf(1)},
	{"int64-big", int64(1) << 62},
	{"uint64-max", uint64(math.MaxUint64)},
	{"string-seps", "a" + string(rune(0x2028)) + "b" + string(rune(0x2029)) + "c"},
	{"string-invalid-utf8", "a\xffb"},
	{"string-html", "<script>&</script>"},
	{"string-control", "a\x01b\x7fc"},
	{"bytes", []byte("hi")},
	{"nil-slice", []int(nil)},
	{"nil-map", map[string]int(nil)},
	{"raw-spaced", json.RawMessage(` { "a" : [ 1 , 2 ] } `)},
	{"raw-html", json.RawMessage(`{"s":"<&>"}`)},
	{"raw-seps", json.RawMessage(j(`{"s":"a@Lb"}`))},
	{"raw-invalid", json.RawMessage(`{"a":}`)},
	{"raw-escapes", json.RawMessage(j(`{"s":"@u00e9\/"}`))},
	{"struct-nil-fields", struct {
		S []int          `json:"s"`
		M map[string]int `json:"m"`
		P *int           `json:"p,omitempty"`
	}{}},
}

// TestExactness records what every candidate does with the probes, one
// JSON line per probe, operation and candidate, to the file
// JSONLIB_EXACT_OUT (appended) or the test log. The study compares the
// lines of each candidate with those of encoding/json, today's engine and
// the classic one.
func TestExactness(t *testing.T) {
	var out *os.File
	if p := os.Getenv("JSONLIB_EXACT_OUT"); p != "" {
		f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		out = f
	}
	emit := func(probe, op, cand, result string) {
		line, _ := json.Marshal(map[string]string{"probe": probe, "op": op, "cand": cand, "out": result})
		if out != nil {
			fmt.Fprintf(out, "%s\n", line)
		} else {
			t.Logf("%s", line)
		}
	}
	for _, c := range Cands {
		for _, p := range probes {
			in := j(p.text)
			if c.Valid != nil {
				emit(p.name, "validate", c.Name, strconv.FormatBool(c.Valid(in)))
			}
			if c.Compact != nil {
				b, err := c.Compact(nil, in)
				emit(p.name, "compact", c.Name, show(b, err))
			}
			if c.Unmarshal != nil {
				var v any
				err := c.Unmarshal(in, &v)
				emit(p.name, "unmarshal-any", c.Name, showValue(v, err))
				var e struct {
					Error struct {
						Name string `json:"name"`
					} `json:"error"`
					Decline bool `json:"decline"`
				}
				err = c.Unmarshal(in, &e)
				emit(p.name, "unmarshal-struct", c.Name, fmt.Sprintf("name=%q decline=%v err=%s", e.Error.Name, e.Decline, errText(err)))
			}
			if c.RawObject != nil {
				m, err := c.RawObject(in)
				emit(p.name, "rawobject", c.Name, showRaw(m, err))
			}
			if c.Members != nil {
				var ms []string
				ok := c.Members(in, func(rk []byte, k string, v []byte) { ms = append(ms, fmt.Sprintf("%q|%q|%q", rk, k, v)) })
				emit(p.name, "members", c.Name, fmt.Sprintf("%v %s", ok, strings.Join(ms, " ")))
			}
			if c.Marshal != nil {
				b, err := c.Marshal(HostCall(in))
				emit(p.name, "rawmessage", c.Name, show(b, err))
			}
			if c.Canonical != nil {
				b, err := c.Canonical(in)
				emit(p.name, "canonical", c.Name, show(b, err))
			}
		}
		if c.Marshal != nil {
			for _, p := range marshalProbes {
				b, err := c.Marshal(p.v)
				emit(p.name, "marshal-go", c.Name, show(b, err))
			}
		}
	}
}

func show(b []byte, err error) string {
	if err != nil {
		return "error: " + err.Error()
	}
	return strconv.Quote(string(b))
}

// showValue writes a decoded value with its Go types, so that a number's
// type and precision show.
func showValue(v any, err error) string {
	if err != nil {
		return "error: " + err.Error()
	}
	var b strings.Builder
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			b.WriteString("{")
			for i, k := range keys {
				if i > 0 {
					b.WriteString(",")
				}
				fmt.Fprintf(&b, "%q:", k)
				walk(x[k])
			}
			b.WriteString("}")
		case []any:
			b.WriteString("[")
			for i, e := range x {
				if i > 0 {
					b.WriteString(",")
				}
				walk(e)
			}
			b.WriteString("]")
		case float64:
			fmt.Fprintf(&b, "float64(%s)", strconv.FormatFloat(x, 'g', -1, 64))
		case string:
			fmt.Fprintf(&b, "%q", x)
		case nil:
			b.WriteString("nil")
		default:
			fmt.Fprintf(&b, "%s(%v)", reflect.TypeOf(x), x)
		}
	}
	walk(v)
	return b.String()
}

func showRaw(m map[string]json.RawMessage, err error) string {
	if err != nil {
		return "error: " + err.Error()
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%q=%q ", k, m[k])
	}
	return b.String()
}
