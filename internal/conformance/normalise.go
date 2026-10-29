package conformance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
)

// Normaliser turns the raw JSON values of one run into normalised values
// (OR-14). It replaces only walked job ids, masks a stack only with
// runtime-error provenance (OR-V3), masks only the location of the one
// recognised internal error (OR-27), and escapes user text so that the
// result stays injective (EncodeText). Everything else is kept: unknown
// UUIDs, user strings, member order, number text and duplicate array
// elements.
type Normaliser struct {
	// IDs maps every walked job id of the run to its site (OR-11).
	IDs map[string]SitePath
	// RuntimeErrors holds clause (a) of OR-V3: the stored result of each
	// failed leaf job that executed a script, of the shape {"error":{…}}.
	RuntimeErrors []json.RawMessage

	runtime []any // RuntimeErrors decoded, for deep equality
	inner   []any // their "error" members
}

// internalErrPrefix is the message prefix of the storage failure of a NUL
// result, the one internal error whose location is masked (OR-27).
const internalErrPrefix = "Internal: Could not add completed job "

// locSuffix is a trailing Rust source location, " @<file>:<line>:<col>".
var locSuffix = regexp.MustCompile(` @[^ @]+:[0-9]+:[0-9]+$`)

// trailingLoc is one location suffix of FX-50 at the end of a message, the
// catalogue's <loc>: " @<file>.rs:<line>:<col>", or the parenthesised form
// " (<file>.rs:<line>)" that some not-found messages end in.
var trailingLoc = regexp.MustCompile(`(?: @[^ @]+\.rs:[0-9]+:[0-9]+| \([^ ()]+\.rs:[0-9]+\))$`)

// resolveErrPrefix begins the message of a job whose argument resolution
// failed with an internal error (JE-42, WR-5): the execution envelope
// around Windmill's located internal error.
const resolveErrPrefix = "execution error:\nInternal: "

// anyhowPrefix is the prefix each conversion into Windmill's Anyhow error
// kind puts before a message, with a location suffix after it (FX-50).
const anyhowPrefix = "Error: "

// Value normalises one raw JSON value.
func (n *Normaliser) Value(raw json.RawMessage) (json.RawMessage, error) {
	if raw == nil {
		return nil, nil
	}
	if err := n.prepare(); err != nil {
		return nil, err
	}
	tree, err := parseJSON(raw)
	if err != nil {
		return nil, err
	}
	n.maskStacks(tree)
	n.rewrite(tree)
	return json.RawMessage(tree.raw()), nil
}

// Text normalises a text kept as written rather than as a JSON value, such
// as an HTTP response body (Response). Walked job ids become placeholders
// anywhere in it. When the text is a JSON value, a stack is masked exactly
// where Value would mask it (OR-V3 clauses a and b): in an object deep-equal
// to a runtime error, its error member's stack; in an object deep-equal to
// such an error's inner object, its own stack. Only that string's span
// becomes the stack marker, so the text keeps its spelling, spaces and
// member order, and the same string elsewhere, a handler's own data among
// it, stays as it is. A text that is not JSON gets no stack masking.
func (n *Normaliser) Text(raw string) (string, error) {
	if err := n.prepare(); err != nil {
		return "", err
	}
	var spans [][2]int
	if len(n.runtime) > 0 && json.Valid([]byte(raw)) {
		sc := spanScanner{text: raw}
		n.stackSpans(raw, sc.value(), &spans)
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i][0] < spans[j][0] })
	var parts []TextPart
	pos := 0
	for _, sp := range spans {
		parts = append(parts, SplitJobIDs(raw[pos:sp[0]], n.IDs)...)
		parts = append(parts, Literal(`"`), MarkerPart(MarkerStack), Literal(`"`))
		pos = sp[1]
	}
	parts = append(parts, SplitJobIDs(raw[pos:], n.IDs)...)
	return EncodeText(parts), nil
}

// stackSpans collects, below t, the spans of the stack strings that
// maskStacks masks in the same value.
func (n *Normaliser) stackSpans(raw string, t *spanNode, out *[][2]int) {
	switch t.kind {
	case kindObject:
		if v, err := decodeAny([]byte(raw[t.start:t.end])); err == nil {
			for i, r := range n.runtime {
				if reflect.DeepEqual(v, r) {
					if e := t.member("error"); e != nil {
						if st := e.member("stack"); st != nil && st.kind == kindString {
							*out = append(*out, [2]int{st.start, st.end})
						}
					}
					return
				}
				if n.inner[i] != nil && reflect.DeepEqual(v, n.inner[i]) {
					if st := t.member("stack"); st != nil && st.kind == kindString {
						*out = append(*out, [2]int{st.start, st.end})
					}
					return
				}
			}
		}
		for _, m := range t.members {
			n.stackSpans(raw, m.value, out)
		}
	case kindArray:
		for _, e := range t.elems {
			n.stackSpans(raw, e, out)
		}
	}
}

// spanNode is a JSON value of a text with its byte span, so that one
// member can be replaced without respelling the rest.
type spanNode struct {
	kind       jkind
	start, end int
	members    []spanMember
	elems      []*spanNode
}

type spanMember struct {
	key   string
	value *spanNode
}

// member returns the value of object t's member k, the last one when the
// key repeats, as decoding takes it; nil when there is none.
func (t *spanNode) member(k string) *spanNode {
	if t.kind != kindObject {
		return nil
	}
	var v *spanNode
	for _, m := range t.members {
		if m.key == k {
			v = m.value
		}
	}
	return v
}

// spanScanner reads a text already checked with json.Valid into spanNodes.
type spanScanner struct {
	text string
	pos  int
}

func (sc *spanScanner) space() {
	for sc.pos < len(sc.text) && strings.IndexByte(" \t\n\r", sc.text[sc.pos]) >= 0 {
		sc.pos++
	}
}

func (sc *spanScanner) value() *spanNode {
	sc.space()
	t := &spanNode{start: sc.pos}
	switch c := sc.text[sc.pos]; c {
	case '{':
		t.kind = kindObject
		sc.pos++
		for sc.space(); sc.text[sc.pos] != '}'; sc.space() {
			k := sc.value()
			var key string
			_ = json.Unmarshal([]byte(sc.text[k.start:k.end]), &key)
			sc.space()
			sc.pos++ // the colon
			t.members = append(t.members, spanMember{key: key, value: sc.value()})
			sc.space()
			if sc.text[sc.pos] == ',' {
				sc.pos++
			}
		}
		sc.pos++
	case '[':
		t.kind = kindArray
		sc.pos++
		for sc.space(); sc.text[sc.pos] != ']'; sc.space() {
			t.elems = append(t.elems, sc.value())
			sc.space()
			if sc.text[sc.pos] == ',' {
				sc.pos++
			}
		}
		sc.pos++
	case '"':
		t.kind = kindString
		for sc.pos++; sc.text[sc.pos] != '"'; sc.pos++ {
			if sc.text[sc.pos] == '\\' {
				sc.pos++
			}
		}
		sc.pos++
	default: // a number, true, false or null: only the span matters
		t.kind = kindNumber
		for sc.pos < len(sc.text) && strings.IndexByte(",]} \t\n\r", sc.text[sc.pos]) < 0 {
			sc.pos++
		}
	}
	t.end = sc.pos
	return t
}

func (n *Normaliser) prepare() error {
	if n.runtime != nil || len(n.RuntimeErrors) == 0 {
		return nil
	}
	for _, r := range n.RuntimeErrors {
		v, err := decodeAny(r)
		if err != nil {
			return fmt.Errorf("conformance: runtime error value: %w", err)
		}
		n.runtime = append(n.runtime, v)
		var inner any // stays aligned with runtime; nil matches nothing
		if m, ok := v.(map[string]any); ok {
			inner = m["error"]
		}
		n.inner = append(n.inner, inner)
	}
	return nil
}

func decodeAny(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	err := dec.Decode(&v)
	return v, err
}

// maskStacks applies clauses (a) and (b) of OR-V3: a value deep-equal,
// before normalisation, to a runtime error has its error.stack masked, and
// a value deep-equal to such an error's inner object (a handler that
// returns the error it received) has its stack masked. Equality ignores
// member order and compares numbers by their text.
func (n *Normaliser) maskStacks(t *jnode) {
	if len(n.runtime) == 0 || (t.kind != kindObject && t.kind != kindArray) {
		return
	}
	if t.kind == kindObject {
		v, err := decodeAny(t.raw())
		if err == nil {
			for i, r := range n.runtime {
				if reflect.DeepEqual(v, r) {
					if e := t.member("error"); e != nil {
						maskString(e, "stack")
					}
					return
				}
				if n.inner[i] != nil && reflect.DeepEqual(v, n.inner[i]) {
					maskString(t, "stack")
					return
				}
			}
		}
		for _, m := range t.members {
			n.maskStacks(m.value)
		}
		return
	}
	for _, e := range t.elems {
		n.maskStacks(e)
	}
}

// maskString replaces the string member k of object t with MarkerStack.
func maskString(t *jnode, k string) {
	if s := t.member(k); s != nil && s.kind == kindString {
		*s = jnode{kind: kindString, text: quote(MarkerStack), marker: true}
	}
}

// rewrite replaces walked job ids in every string and member name, escapes
// marker-like user text, and masks the location of the recognised internal
// error.
func (n *Normaliser) rewrite(t *jnode) {
	switch t.kind {
	case kindString:
		if t.marker {
			return
		}
		t.text = quote(EncodeText(SplitJobIDs(t.str(), n.IDs)))
	case kindArray:
		for _, e := range t.elems {
			n.rewrite(e)
		}
	case kindObject:
		if msg := t.member("message"); msg != nil && msg.kind == kindString && !msg.marker {
			if name := t.member("name"); name != nil && name.kind == kindString && name.str() == "InternalErr" {
				s := msg.str()
				if loc := locSuffix.FindStringIndex(s); strings.HasPrefix(s, internalErrPrefix) && loc != nil {
					parts := append(SplitJobIDs(s[:loc[0]], n.IDs), Literal(" "), MarkerPart(MarkerLoc))
					*msg = jnode{kind: kindString, text: quote(EncodeText(parts)), marker: true}
				}
			}
		}
		for i := range t.members {
			t.members[i].key = EncodeText(SplitJobIDs(t.members[i].key, n.IDs))
			n.rewrite(t.members[i].value)
		}
	}
}

// MaskPaths replaces, in an encoded observation, each string at a stack
// pointer with MarkerStack (OR-V3 clause c) and each value at a mask
// pointer with MarkerMasked (OR-14 item 8). A pointer that does not resolve
// is skipped: a fixture's runs need not all reach every masked path.
func MaskPaths(o Observation, stack, mask []string) (Observation, error) {
	if len(stack) == 0 && len(mask) == 0 {
		return o, nil
	}
	data, err := json.Marshal(o)
	if err != nil {
		return o, err
	}
	tree, err := parseJSON(data)
	if err != nil {
		return o, err
	}
	for _, group := range []struct {
		ptrs   []string
		marker string
	}{{stack, MarkerStack}, {mask, MarkerMasked}} {
		for _, p := range group.ptrs {
			toks, err := SplitPointer(p)
			if err != nil {
				return o, err
			}
			if v := tree.at(toks); v != nil && (group.marker == MarkerMasked || v.kind == kindString) {
				*v = jnode{kind: kindString, text: quote(group.marker), marker: true}
			}
		}
	}
	var out Observation
	if err := json.Unmarshal(tree.raw(), &out); err != nil {
		return o, err
	}
	return out, nil
}

// StripLocations applies the one rule that comparing windmill-lite with
// the oracle adds to OR-14 (ADR 0011, Location suffixes; ADR 0001 §2, D14).
// Each pointer names a value of windmill-lite's observation that
// windmill-lite generated (an engine, harness or facade error, never a
// user's). Where the oracle's normalised observation o holds there an
// error whose message matches a catalogue template that carries <loc>,
// the location suffixes are removed, leaving the message windmill-lite
// writes without a Rust location. The templates are:
//
//   - the recognised internal error of OR-27 (InternalErr, "Internal:
//     Could not add completed job …"), whose location the normaliser
//     masked: the marker and the space before it go;
//   - a job that failed before its script ran because its arguments did
//     not resolve (JE-42, WR-5, FX-51/c), ExecutionErr, whose message is
//     the execution envelope around an internal error with its location:
//     "execution error:\nInternal: …<loc>" loses its one suffix;
//   - a chaining error of FX-15 and FX-50, named InternalError (a later
//     transition) or Anyhow (a pickup), whose message is Windmill's
//     generic error display: one "Error: " prefix per conversion, and one
//     location suffix per conversion at the end;
//   - the string error of a throwing stop_after_if (FX-21), the same
//     display;
//   - the error stop of a stop_after_all_iters_if that fails to evaluate
//     (CF-13, JE-27), an EarlyStopError whose message quotes the
//     expression and then gives its cause in the same display.
//
// For the last three, a message (or the cause) that begins with n
// "Error: " prefixes loses its last n suffixes, and only when it ends in n
// of them. The value at a
// pointer is the error object, or an object whose error member is it (or,
// for FX-21, is the string), as a stored failure value is. Nothing else is
// stripped: the normaliser never masks a location in a user's message, and
// a pointer that does not resolve, or names another value, is skipped.
func StripLocations(o Observation, at []string) (Observation, error) {
	if len(at) == 0 {
		return o, nil
	}
	data, err := json.Marshal(o)
	if err != nil {
		return o, err
	}
	tree, err := parseJSON(data)
	if err != nil {
		return o, err
	}
	// In normalised text, " <loc>" can only end in the marker: a user's
	// literal "<loc>" is written "<<loc>".
	const suffix = " " + MarkerLoc
	for _, p := range at {
		toks, err := SplitPointer(p)
		if err != nil {
			return o, err
		}
		v := tree.at(toks)
		if v != nil && v.kind == kindObject {
			if e := v.member("error"); e != nil && (e.kind == kindObject || e.kind == kindString) {
				v = e
			}
		}
		if v != nil && v.kind == kindString && !v.marker {
			// FX-21: the untyped error's display, as a string.
			stripDisplay(v)
			continue
		}
		if v == nil || v.kind != kindObject {
			continue
		}
		name, msg := v.member("name"), v.member("message")
		if name == nil || name.kind != kindString || msg == nil || msg.kind != kindString || msg.marker {
			continue
		}
		switch name.str() {
		case "InternalErr":
			if s := msg.str(); strings.HasPrefix(s, internalErrPrefix) && strings.HasSuffix(s, suffix) {
				*msg = jnode{kind: kindString, text: quote(strings.TrimSuffix(s, suffix))}
			}
		case "InternalError", "Anyhow":
			stripDisplay(msg)
		case "EarlyStopError":
			if s := msg.str(); strings.HasPrefix(s, allItersPrefix) {
				if end := strings.Index(s[len(allItersPrefix):], "`: "); end >= 0 {
					head := len(allItersPrefix) + end + len("`: ")
					if cause, ok := strippedDisplay(s[head:]); ok {
						*msg = jnode{kind: kindString, text: quote(s[:head] + cause)}
					}
				}
			}
		case "ExecutionErr":
			if s := msg.str(); strings.HasPrefix(s, resolveErrPrefix) {
				if loc := trailingLoc.FindStringIndex(s); loc != nil {
					*msg = jnode{kind: kindString, text: quote(s[:loc[0]])}
				}
			}
		}
	}
	var out Observation
	if err := json.Unmarshal(tree.raw(), &out); err != nil {
		return o, err
	}
	return out, nil
}

// allItersPrefix begins the message of a stop_after_all_iters_if that
// fails to evaluate (CF-13).
const allItersPrefix = "Error evaluating stop_after_all_iters_if expression `"

// stripDisplay removes, from a string node holding Windmill's generic error
// display (FX-50), one trailing location suffix per leading "Error: "
// prefix, when the text ends in that many; otherwise it leaves the node,
// which then shows its difference.
func stripDisplay(t *jnode) {
	if out, ok := strippedDisplay(t.str()); ok {
		*t = jnode{kind: kindString, text: quote(out)}
	}
}

// strippedDisplay is s, Windmill's generic error display, without one
// trailing location suffix per leading "Error: " prefix, and true; or
// false when s has no such prefix or does not end in that many suffixes.
func strippedDisplay(s string) (string, bool) {
	n := 0
	for rest := s; strings.HasPrefix(rest, anyhowPrefix); rest = rest[len(anyhowPrefix):] {
		n++
	}
	if n == 0 {
		return "", false
	}
	out := s
	for range n {
		loc := trailingLoc.FindStringIndex(out)
		if loc == nil {
			return "", false
		}
		out = out[:loc[0]]
	}
	return out, true
}

// at resolves reference tokens below t, or returns nil.
func (t *jnode) at(toks []string) *jnode {
	for _, k := range toks {
		switch t.kind {
		case kindObject:
			t = t.member(k)
		case kindArray:
			var i int
			if _, err := fmt.Sscan(k, &i); err != nil || i < 0 || i >= len(t.elems) || fmt.Sprint(i) != k {
				return nil
			}
			t = t.elems[i]
		default:
			return nil
		}
		if t == nil {
			return nil
		}
	}
	return t
}

// str returns the value of a string node.
func (t *jnode) str() string {
	var s string
	_ = json.Unmarshal([]byte(t.text), &s) // text is always a valid JSON string
	return s
}
