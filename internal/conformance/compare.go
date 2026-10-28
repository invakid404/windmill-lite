package conformance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// CompareOptions selects how two observations are compared (OR-14 item 8).
// The zero value is the default core gate: exact, objects without regard to
// key order (OR-13), arrays in order, numbers by their JSON text.
type CompareOptions struct {
	// Subset accepts extra object members and extra steps in the actual
	// observation. Subset assertions alone cannot establish an exact error
	// contract (OR-15); fixtures use them sparingly.
	Subset bool
	// StrictKeyOrder compares object members in order, inside values and
	// inside the elements of unordered arrays. A subset has no member
	// order, so Subset ignores it; a fixture may not ask for both.
	StrictKeyOrder bool
	// Unordered lists JSON Pointers, into the expected observation, of
	// arrays that compare as multisets (OR-23): ["a","a","b"] differs from
	// ["a","b"] (OR-23/b).
	Unordered []string
	// WithRecording leaves carried and resolved_args out, because the
	// oracle exposes neither, so a recording holds neither (ADR 0011).
	WithRecording bool
}

// Difference is one place where two observations differ.
type Difference struct {
	// Pointer is the RFC 6901 JSON Pointer of the differing value.
	Pointer string
	// Got and Want are compact JSON, or "(absent)".
	Got, Want string
}

// String writes the difference as "<pointer>: <got> != <want>" (OR-15/b).
func (d Difference) String() string { return d.Pointer + ": " + d.Got + " != " + d.Want }

const absent = "(absent)"

// Compare compares the actual observation got with the expected want and
// returns their differences, ordered by pointer. A step field that one side
// leaves to its canonical absence (ran, module, skipped, carried,
// resolved_args) and the other spells out is first expanded to the value
// the absence stands for, so an absence and its explicit spelling compare
// equal under any key-order policy (ADR 0011). A field both sides leave out
// is not compared twice: its source (status, result, args) already is.
func Compare(got, want Observation, opt CompareOptions) ([]Difference, error) {
	g, gs, err := observationTree(got, opt)
	if err != nil {
		return nil, fmt.Errorf("conformance: actual observation: %w", err)
	}
	w, ws, err := observationTree(want, opt)
	if err != nil {
		return nil, fmt.Errorf("conformance: expected observation: %w", err)
	}
	for site, wstep := range ws {
		if gstep, ok := gs[site]; ok {
			expandPair(gstep, wstep)
		}
	}
	c := comparer{opt: opt, unordered: make(map[string]bool)}
	for _, p := range opt.Unordered {
		toks, err := SplitPointer(p)
		if err != nil {
			return nil, err
		}
		c.unordered[JoinPointer(toks)] = true
	}
	c.compare(nil, g, w)
	sort.SliceStable(c.diffs, func(i, j int) bool { return c.diffs[i].Pointer < c.diffs[j].Pointer })
	return c.diffs, nil
}

// stepTree is one step's node and the defaults its absent fields stand for.
type stepTree struct {
	node     *jnode
	defaults map[string][]byte
}

// observationTree encodes o and returns its tree and, by site, its steps.
func observationTree(o Observation, opt CompareOptions) (*jnode, map[string]stepTree, error) {
	data, err := json.Marshal(o)
	if err != nil {
		return nil, nil, err
	}
	root, err := parseJSON(data)
	if err != nil {
		return nil, nil, err
	}
	steps := map[string]stepTree{}
	sm := root.member("steps")
	if sm == nil {
		return root, steps, nil
	}
	for _, m := range sm.members {
		var s StepObservation
		if err := json.Unmarshal(m.value.raw(), &s); err != nil {
			return nil, nil, err
		}
		d := map[string][]byte{
			"ran":     []byte(fmt.Sprint(s.Ran == nil || *s.Ran)),
			"module":  mustJSON(s.ModuleStatus()),
			"skipped": []byte(fmt.Sprint(s.Skipped != nil && *s.Skipped)),
		}
		if s.Args != nil {
			d["resolved_args"] = s.Args
		}
		if s.Result != nil {
			d["carried"] = s.Result
		}
		if opt.WithRecording {
			m.value.remove("carried")
			m.value.remove("resolved_args")
			delete(d, "carried")
			delete(d, "resolved_args")
		}
		steps[m.key] = stepTree{node: m.value, defaults: d}
	}
	return root, steps, nil
}

// expandPair writes out, on each side, the defaults of the fields that the
// other side spells and this side leaves absent.
func expandPair(a, b stepTree) {
	for _, k := range []string{"ran", "module", "skipped", "resolved_args", "carried"} {
		an, bn := a.node.member(k), b.node.member(k)
		switch {
		case an == nil && bn != nil:
			a.node.setDefault(k, a.defaults[k])
		case bn == nil && an != nil:
			b.node.setDefault(k, b.defaults[k])
		}
	}
}

// setDefault adds member k with the JSON text v, when v is known.
func (n *jnode) setDefault(k string, v []byte) {
	if v == nil {
		return
	}
	if d, err := parseJSON(v); err == nil {
		n.members = append(n.members, jmember{key: k, value: d})
	}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

type comparer struct {
	opt       CompareOptions
	unordered map[string]bool
	diffs     []Difference
}

func (c *comparer) add(path []string, got, want *jnode) {
	d := Difference{Pointer: JoinPointer(path), Got: absent, Want: absent}
	if got != nil {
		d.Got = string(got.raw())
	}
	if want != nil {
		d.Want = string(want.raw())
	}
	c.diffs = append(c.diffs, d)
}

func (c *comparer) compare(path []string, got, want *jnode) {
	if got == nil || want == nil || got.kind != want.kind {
		c.add(path, got, want)
		return
	}
	switch want.kind {
	case kindObject:
		c.compareObjects(path, got, want)
	case kindArray:
		if c.unordered[JoinPointer(path)] {
			if !multisetEqual(path, got.elems, want.elems, c.opt) {
				c.add(path, got, want)
			}
			return
		}
		if len(got.elems) != len(want.elems) {
			c.add(path, got, want)
			return
		}
		for i := range want.elems {
			c.compare(append(append([]string(nil), path...), fmt.Sprint(i)), got.elems[i], want.elems[i])
		}
	default:
		if got.text != want.text {
			c.add(path, got, want)
		}
	}
}

// structural reports whether path names part of the observation's own
// shape (the root, the steps map, a step record) rather than a value inside
// it. Strict key order applies only to values: the order of an
// observation's own fields means nothing.
func structural(path []string) bool {
	return len(path) == 0 || (path[0] == "steps" && len(path) <= 2)
}

func (c *comparer) compareObjects(path []string, got, want *jnode) {
	if c.opt.StrictKeyOrder && !c.opt.Subset && !structural(path) && !sameKeyOrder(got, want) {
		c.add(path, got, want)
		return
	}
	keys := make(map[string]bool)
	for _, m := range want.members {
		keys[m.key] = true
	}
	if !c.opt.Subset {
		for _, m := range got.members {
			keys[m.key] = true
		}
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	for _, k := range sorted {
		c.compare(append(append([]string(nil), path...), k), got.member(k), want.member(k))
	}
}

func sameKeyOrder(a, b *jnode) bool {
	if len(a.members) != len(b.members) {
		return false
	}
	for i := range a.members {
		if a.members[i].key != b.members[i].key {
			return false
		}
	}
	return true
}

// multisetEqual reports whether got and want, the elements of the array at
// path, hold the same elements with the same multiplicity. Element equality
// is an equivalence, so a greedy matching decides it. Elements are compared
// as values below path, with the same options.
func multisetEqual(path []string, got, want []*jnode, opt CompareOptions) bool {
	if len(got) != len(want) {
		return false
	}
	used := make([]bool, len(got))
	for _, w := range want {
		found := false
		for i, g := range got {
			if !used[i] && nodesEqual(path, g, w, opt) {
				used[i] = true
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// nodesEqual compares two elements of the array at path. The element's
// path carries the placeholder token "*", which no index has, so the
// element is a value (strict key order applies to it) and no unordered
// pointer matches below it.
func nodesEqual(path []string, a, b *jnode, opt CompareOptions) bool {
	c := comparer{opt: opt, unordered: map[string]bool{}}
	c.compare(append(append([]string(nil), path...), "*"), a, b)
	return len(c.diffs) == 0
}

// SplitPointer splits an RFC 6901 JSON Pointer into its reference tokens.
// The empty pointer is the whole document.
func SplitPointer(p string) ([]string, error) {
	if p == "" {
		return nil, nil
	}
	if p[0] != '/' {
		return nil, fmt.Errorf("conformance: JSON Pointer %q does not start with /", p)
	}
	parts := strings.Split(p[1:], "/")
	for i, t := range parts {
		for j := 0; j < len(t); j++ {
			if t[j] == '~' && (j+1 == len(t) || (t[j+1] != '0' && t[j+1] != '1')) {
				return nil, fmt.Errorf("conformance: JSON Pointer %q has a bad ~ escape", p)
			}
		}
		parts[i] = strings.ReplaceAll(strings.ReplaceAll(t, "~1", "/"), "~0", "~")
	}
	return parts, nil
}

// JoinPointer writes reference tokens as an RFC 6901 JSON Pointer.
func JoinPointer(tokens []string) string {
	var b strings.Builder
	for _, t := range tokens {
		b.WriteByte('/')
		b.WriteString(strings.ReplaceAll(strings.ReplaceAll(t, "~", "~0"), "/", "~1"))
	}
	return b.String()
}

// jnode is a parsed JSON value that keeps member order and number text.
type jnode struct {
	kind    jkind
	text    string // the JSON text of a scalar
	members []jmember
	elems   []*jnode
	// marker is set on a string the normaliser wrote as a marker, so a
	// later pass leaves it as it is.
	marker bool
}

type jmember struct {
	key   string
	value *jnode
}

type jkind int

const (
	kindNull jkind = iota
	kindBool
	kindNumber
	kindString
	kindArray
	kindObject
)

func (n *jnode) member(k string) *jnode {
	if n == nil || n.kind != kindObject {
		return nil
	}
	for _, m := range n.members {
		if m.key == k {
			return m.value
		}
	}
	return nil
}

func (n *jnode) remove(k string) {
	out := n.members[:0]
	for _, m := range n.members {
		if m.key != k {
			out = append(out, m)
		}
	}
	n.members = out
}

// raw returns n as compact JSON in its member order.
func (n *jnode) raw() []byte {
	var b bytes.Buffer
	n.write(&b)
	return b.Bytes()
}

func (n *jnode) write(b *bytes.Buffer) {
	switch n.kind {
	case kindObject:
		b.WriteByte('{')
		for i, m := range n.members {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(quote(m.key))
			b.WriteByte(':')
			m.value.write(b)
		}
		b.WriteByte('}')
	case kindArray:
		b.WriteByte('[')
		for i, e := range n.elems {
			if i > 0 {
				b.WriteByte(',')
			}
			e.write(b)
		}
		b.WriteByte(']')
	default:
		b.WriteString(n.text)
	}
}

// parseJSON parses one JSON value, keeping member order and number text.
// A key given twice keeps its last value, as jsonb does (OR-13).
func parseJSON(data []byte) (*jnode, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	n, err := parseValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("conformance: trailing data after JSON value")
	}
	return n, nil
}

func parseValue(dec *json.Decoder) (*jnode, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			n := &jnode{kind: kindObject}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				v, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				k := kt.(string)
				replaced := false
				for i := range n.members {
					if n.members[i].key == k {
						n.members[i].value = v
						replaced = true
					}
				}
				if !replaced {
					n.members = append(n.members, jmember{key: k, value: v})
				}
			}
			_, err := dec.Token()
			return n, err
		case '[':
			n := &jnode{kind: kindArray, elems: []*jnode{}}
			for dec.More() {
				v, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				n.elems = append(n.elems, v)
			}
			_, err := dec.Token()
			return n, err
		}
	case nil:
		return &jnode{kind: kindNull, text: "null"}, nil
	case bool:
		return &jnode{kind: kindBool, text: fmt.Sprint(t)}, nil
	case json.Number:
		return &jnode{kind: kindNumber, text: t.String()}, nil
	case string:
		return &jnode{kind: kindString, text: quote(t)}, nil
	}
	return nil, fmt.Errorf("conformance: unexpected JSON token %v", tok)
}

// IndentJSON re-encodes JSON text with two-space indentation and without
// HTML escaping, keeping member order and number text, so a recording reads
// as its values were written ("<stack>", not "<stack>").
func IndentJSON(data []byte) ([]byte, error) {
	n, err := parseJSON(data)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	n.writeIndent(&b, "")
	b.WriteByte('\n')
	return b.Bytes(), nil
}

func (n *jnode) writeIndent(b *bytes.Buffer, prefix string) {
	inner := prefix + "  "
	switch {
	case n.kind == kindObject && len(n.members) > 0:
		b.WriteString("{\n")
		for i, m := range n.members {
			b.WriteString(inner)
			b.WriteString(quote(m.key))
			b.WriteString(": ")
			m.value.writeIndent(b, inner)
			if i+1 < len(n.members) {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(prefix)
		b.WriteByte('}')
	case n.kind == kindArray && len(n.elems) > 0:
		b.WriteString("[\n")
		for i, e := range n.elems {
			b.WriteString(inner)
			e.writeIndent(b, inner)
			if i+1 < len(n.elems) {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(prefix)
		b.WriteByte(']')
	default:
		n.write(b)
	}
}

// quote writes s as a JSON string without HTML escaping, so markers such as
// "<stack>" read as written in a difference.
func quote(s string) string {
	var b bytes.Buffer
	writeJSONString(&b, s)
	return b.String()
}
