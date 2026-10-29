package conformance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"slices"
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

// trailingLoc is one location suffix of an error layer at the end of a
// message (FX-50): " @<file>.rs:<line>:<col>", or the marker " <loc>" where
// the expected text writes the catalogue's <loc> for one. The parenthesised
// " (<file>.rs:<line>)" of a not-found message is not a layer's suffix and
// is never matched by its text (see StripLocations).
var trailingLoc = regexp.MustCompile(`(?: @[^ @]+\.rs:[0-9]+:[0-9]+| ` + MarkerLoc + `)$`)

// layerPrefixes are the prefixes of FX-50's error layers whose display
// ends in a location suffix: Anyhow's "Error: ", the located internal
// error's "Internal: ", and the wrapped library errors'.
var layerPrefixes = []string{"Error: ", "Internal: ", "SqlErr: ", "IoErr: ", "Utf8Err: ", "UuidErr: ", "SerdeJson: ", "HexErr: "}

// executionErrPrefix begins the message of the worker's execution error
// (WR-37, WR-43).
const executionErrPrefix = "execution error:\n"

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
// user's). Where the oracle's normalised observation o (an expect or a
// recording) holds there an error whose message matches a catalogue
// template that carries <loc>, the generated location suffixes are
// removed, leaving the message windmill-lite writes without a Rust
// location. The rule is FX-50's:
//
//  1. A message that is Windmill's error display begins with k layer
//     prefixes, one after another (layerPrefixes: "Error: ", "Internal: ",
//     "SqlErr: " and the other located kinds); it loses at most k trailing
//     " @<file>.rs:<line>:<col>" suffixes from the end, or <loc> markers
//     the expected text writes for them. A message with no layer prefix
//     loses none, and a suffix beyond the k-th is the message's own.
//  2. A parenthesised " (<file>.rs:<line>)", which a not-found message
//     ends in, is not searched for: no text tells a generated one from a
//     user's (FX-50/d). It goes only where the expected text writes <loc>
//     for it (CF-19/c): every <loc> marker left in a generated value's
//     message after step 1 is removed, wherever it stands.
//
// The displays step 1 reads, each the error at a pointer:
//
//   - the storage failure of OR-27 (InternalErr, "Internal: Could not add
//     completed job …"), whose location the normaliser masked as <loc>;
//   - a chaining error of FX-15 and FX-50, named InternalError (a later
//     transition) or Anyhow (a pickup);
//   - the string error of a throwing stop_after_if (FX-21);
//   - the cause of the error stop of a stop_after_all_iters_if that fails
//     to evaluate (CF-13, JE-27), an EarlyStopError whose message quotes
//     the expression first;
//   - the worker's execution error (ExecutionErr), after its
//     "execution error:\n" lead-in (WR-37, WR-43, FX-51 rule 5).
//
// The value at a pointer is the error object, or an object whose error
// member is it (or, for FX-21, is the string), as a stored failure value
// is. Nothing else is stripped: the normaliser never masks a location in a
// user's message and escapes a user's literal "<loc>" ("<<loc>"), and a
// pointer that does not resolve, or names another value, is skipped.
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
			setString(v, unmarked(strippedDisplay(v.str())))
			continue
		}
		if v == nil || v.kind != kindObject {
			continue
		}
		name, msg := v.member("name"), v.member("message")
		if name == nil || name.kind != kindString || msg == nil || msg.kind != kindString || msg.marker {
			continue
		}
		s := msg.str()
		switch name.str() {
		case "InternalErr", "InternalError", "Anyhow":
			s = strippedDisplay(s)
		case "EarlyStopError":
			if strings.HasPrefix(s, allItersPrefix) {
				if end := strings.Index(s[len(allItersPrefix):], "`: "); end >= 0 {
					head := len(allItersPrefix) + end + len("`: ")
					s = s[:head] + strippedDisplay(s[head:])
				}
			}
		case "ExecutionErr":
			if rest, ok := strings.CutPrefix(s, executionErrPrefix); ok {
				s = executionErrPrefix + strippedDisplay(rest)
			}
		}
		setString(msg, unmarked(s))
	}
	var out Observation
	if err := json.Unmarshal(tree.raw(), &out); err != nil {
		return o, err
	}
	return out, nil
}

// notFoundLoc is the parenthesised location that ends the text of a
// not-found error from Windmill's lookup helper, "<kind> not found at name
// <name> (<file>.rs:<line>)", wherever a message quotes it (FX-50).
var notFoundLoc = regexp.MustCompile(` not found at name [^ ()]+( \([^ ()]+\.rs:[0-9]+\))`)

// StripNotFoundLocations applies rule 3 of FX-50's masking to the
// comparison of windmill-lite with the oracle (ADR 0011, Location
// suffixes): at each pointer, a run declares that the message there quotes
// a not-found error of the lookup helper whose parenthesised location
// Windmill generated (Run.NotFoundLocs), as the case's expected text
// writes <loc> for it (CF-19/c's failed fetch of a step's result). The
// value at a pointer is an error object, an object whose error member is
// one, or the message itself; the location of the first not-found text in
// the message goes, with its leading space. Nothing is stripped where no
// pointer says so, since a user's own message can hold the same phrase
// (FX-50/d), and a pointer that does not resolve to such a message is
// skipped, so its difference shows.
func StripNotFoundLocations(o Observation, at []string) (Observation, error) {
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
	for _, p := range at {
		toks, err := SplitPointer(p)
		if err != nil {
			return o, err
		}
		v := tree.at(toks)
		if v != nil && v.kind == kindObject {
			if e := v.member("error"); e != nil && e.kind == kindObject {
				v = e
			}
			v = v.member("message")
		}
		if v == nil || v.kind != kindString || v.marker {
			continue
		}
		s := v.str()
		if m := notFoundLoc.FindStringSubmatchIndex(s); m != nil {
			*v = jnode{kind: kindString, text: quote(s[:m[2]] + s[m[3]:])}
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

// setString replaces a string node's value with s, when it differs.
func setString(t *jnode, s string) {
	if s != t.str() {
		*t = jnode{kind: kindString, text: quote(s)}
	}
}

// strippedDisplay is s, a message that may be Windmill's error display
// (FX-50), without its generated trailing suffixes: one that begins with k
// layer prefixes loses at most k trailing suffixes (trailingLoc), from the
// end; any other is returned as it is.
func strippedDisplay(s string) string {
	k := 0
	for rest := s; ; k++ {
		i := slices.IndexFunc(layerPrefixes, func(p string) bool { return strings.HasPrefix(rest, p) })
		if i < 0 {
			break
		}
		rest = rest[len(layerPrefixes[i]):]
	}
	for range k {
		loc := trailingLoc.FindStringIndex(s)
		if loc == nil {
			break
		}
		s = s[:loc[0]]
	}
	return s
}

// unmarked is s without the <loc> markers left in it, each with the space
// before it: the parenthesised locations the expected text writes as <loc>
// (FX-50, masking step 3). In normalised text " <loc>" is always a marker:
// a user's literal "<loc>" reads "<<loc>".
func unmarked(s string) string { return strings.ReplaceAll(s, " "+MarkerLoc, "") }

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
