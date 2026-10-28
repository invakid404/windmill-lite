package conformance

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Claim is one assertion about a run's observation, written in the
// catalogue's terms: a JSON Pointer into the observation and what the value
// there must be. Claims state what a catalogue case says (an exact value, or
// a message's prefix and substrings, as docs/semantics/conformance.md
// compares engine messages) without stating the whole observation, which
// the committed recording does (milestone E3a). A claim holds exactly one
// of Equals, Prefix, Contains or NotContains (which may be combined, for a
// string), Length or Absent.
//
// The observation is read in its canonical encoding, so a step field left
// to its canonical absence (ran, module, skipped, carried, resolved_args)
// is absent, not its default: claim `skipped` with Equals true, or with
// Absent for a module that was not skipped.
type Claim struct {
	// At is the RFC 6901 JSON Pointer of the value, for example
	// /steps/m:b/args/x or /result/error/message; a / inside a site path
	// is ~1.
	At string `json:"at"`
	// Equals is the JSON value the pointer must hold, compared as Compare
	// compares values: objects without regard to key order unless KeyOrder
	// is strict, arrays in order, numbers by their JSON text.
	Equals json.RawMessage `json:"equals,omitempty"`
	// KeyOrder is "loose" (the default) or "strict", for Equals.
	KeyOrder string `json:"key_order,omitempty"`
	// Prefix, Contains and NotContains match a string value.
	Prefix      *string  `json:"prefix,omitempty"`
	Contains    []string `json:"contains,omitempty"`
	NotContains []string `json:"not_contains,omitempty"`
	// Length is the number of elements an array must have.
	Length *int `json:"length,omitempty"`
	// Absent requires that the pointer resolve to nothing.
	Absent bool   `json:"absent,omitempty"`
	Note   string `json:"note,omitempty"`
}

func (c Claim) check() error {
	if _, err := SplitPointer(c.At); err != nil {
		return err
	}
	kinds := 0
	if c.Equals != nil {
		kinds++
	}
	if c.Prefix != nil || len(c.Contains) > 0 || len(c.NotContains) > 0 {
		kinds++
	}
	if c.Length != nil {
		kinds++
	}
	if c.Absent {
		kinds++
	}
	if kinds != 1 {
		return fmt.Errorf("claim at %q must hold exactly one of equals, a string match, length or absent", c.At)
	}
	switch c.KeyOrder {
	case "", "loose":
	case "strict":
		if c.Equals == nil {
			return fmt.Errorf("claim at %q: key_order applies only to equals", c.At)
		}
	default:
		return fmt.Errorf("claim at %q: key_order %q is not loose or strict", c.At, c.KeyOrder)
	}
	if c.Equals != nil {
		if _, err := parseJSON(c.Equals); err != nil {
			return fmt.Errorf("claim at %q: equals: %w", c.At, err)
		}
	}
	return nil
}

// CheckClaims checks each claim against o and returns one line per claim
// that does not hold, "<pointer>: <what was found> (<what the claim
// wants>)". An observation that cannot be encoded is an error.
func CheckClaims(o Observation, claims []Claim) ([]string, error) {
	data, err := json.Marshal(o)
	if err != nil {
		return nil, err
	}
	root, err := parseJSON(data)
	if err != nil {
		return nil, err
	}
	var failed []string
	for _, c := range claims {
		if err := c.check(); err != nil {
			return nil, err
		}
		if msg := c.holds(root); msg != "" {
			failed = append(failed, c.At+": "+msg)
		}
	}
	return failed, nil
}

// holds returns "" when the claim holds on root, otherwise what failed.
func (c Claim) holds(root *jnode) string {
	toks, _ := SplitPointer(c.At)
	n := root.at(toks)
	if c.Absent {
		if n != nil {
			return fmt.Sprintf("%s (want absent)", n.raw())
		}
		return ""
	}
	if n == nil {
		return "absent"
	}
	switch {
	case c.Equals != nil:
		want, _ := parseJSON(c.Equals)
		cmp := comparer{opt: CompareOptions{StrictKeyOrder: c.KeyOrder == "strict"}, unordered: map[string]bool{}}
		// The comparison starts below a non-structural path, so strict key
		// order applies from the claimed value down.
		cmp.compare([]string{"claim"}, n, want)
		if len(cmp.diffs) > 0 {
			return fmt.Sprintf("%s (want %s)", n.raw(), want.raw())
		}
	case c.Length != nil:
		if n.kind != kindArray {
			return fmt.Sprintf("%s (want an array of %d)", n.raw(), *c.Length)
		}
		if len(n.elems) != *c.Length {
			return fmt.Sprintf("%d elements (want %d)", len(n.elems), *c.Length)
		}
	default:
		if n.kind != kindString {
			return fmt.Sprintf("%s (want a string)", n.raw())
		}
		s := n.str()
		if c.Prefix != nil && !strings.HasPrefix(s, *c.Prefix) {
			return fmt.Sprintf("%s (want prefix %q)", n.text, *c.Prefix)
		}
		for _, sub := range c.Contains {
			if !strings.Contains(s, sub) {
				return fmt.Sprintf("%s (want it to contain %q)", n.text, sub)
			}
		}
		for _, sub := range c.NotContains {
			if strings.Contains(s, sub) {
				return fmt.Sprintf("%s (want it not to contain %q)", n.text, sub)
			}
		}
	}
	return ""
}

// ValueAt returns the value at pointer p of o's canonical encoding, as
// compact JSON in its member order, or nil when p does not resolve.
func ValueAt(o Observation, p string) (json.RawMessage, error) {
	toks, err := SplitPointer(p)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(o)
	if err != nil {
		return nil, err
	}
	root, err := parseJSON(data)
	if err != nil {
		return nil, err
	}
	n := root.at(toks)
	if n == nil {
		return nil, nil
	}
	return n.raw(), nil
}

// SameJSON reports whether a and b are the same JSON value: objects with
// their members in the same order when strict is set, otherwise without
// regard to it; arrays in order; numbers and strings by their JSON text,
// so 1000 and 1e3 differ.
func SameJSON(a, b []byte, strict bool) (bool, error) {
	x, err := parseJSON(a)
	if err != nil {
		return false, err
	}
	y, err := parseJSON(b)
	if err != nil {
		return false, err
	}
	cmp := comparer{opt: CompareOptions{StrictKeyOrder: strict}, unordered: map[string]bool{}}
	cmp.compare([]string{"value"}, x, y)
	return len(cmp.diffs) == 0, nil
}

// Delay is a lower bound on the time from one job's completion to another
// job's start, in a run's recording: a sleep delays the next job (JE-28).
// Timings are never compared between recordings (OR-17), so a delay is
// checked against the recording's provenance, which holds each walked
// job's times by site.
type Delay struct {
	// From and To are site paths; To starts AtLeastMS after From
	// completes.
	From      string `json:"from"`
	To        string `json:"to"`
	AtLeastMS int64  `json:"at_least_ms"`
}

func (d Delay) check() error {
	for _, s := range []string{d.From, d.To} {
		if _, err := ParseSitePath(s); err != nil {
			return fmt.Errorf("delay: %w", err)
		}
	}
	if d.AtLeastMS <= 0 {
		return fmt.Errorf("delay from %s to %s: at_least_ms must be positive", d.From, d.To)
	}
	return nil
}
