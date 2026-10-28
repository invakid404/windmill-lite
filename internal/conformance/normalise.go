package conformance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
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
// user's). Where the oracle's normalised observation o holds there the
// recognised internal error of OR-27, whose location the normaliser
// masked, the marker and the space before it are removed, leaving the
// message windmill-lite writes without a Rust location. The value at a
// pointer is the error object, or an object whose error member is it, as
// a stored failure value is. Nothing else is stripped: the normaliser
// never masks a location in a user's message, and a pointer that does not
// resolve, or names another value, is skipped.
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
			if e := v.member("error"); e != nil && e.kind == kindObject {
				v = e
			}
		}
		if v == nil || v.kind != kindObject {
			continue
		}
		name, msg := v.member("name"), v.member("message")
		if name == nil || name.kind != kindString || name.str() != "InternalErr" || msg == nil || msg.kind != kindString {
			continue
		}
		if s := msg.str(); strings.HasPrefix(s, internalErrPrefix) && strings.HasSuffix(s, suffix) {
			*msg = jnode{kind: kindString, text: quote(strings.TrimSuffix(s, suffix))}
		}
	}
	var out Observation
	if err := json.Unmarshal(tree.raw(), &out); err != nil {
		return o, err
	}
	return out, nil
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
