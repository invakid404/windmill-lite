// Package jsonval is the engine's JSON value type (ADR 0005).
//
// Go holds every value that crosses a step, expression or storage boundary
// as JSON text, never as decoded Go values, because the text itself is
// observable: key order, number spelling and duplicate keys differ between
// the form a producer wrote and the form Windmill stored in PostgreSQL
// jsonb (FX-42, JE-44). A Value therefore carries:
//
//   - the producer text, byte for byte as the harness, QuickJS-ng, a static
//     transform or the host wrote it;
//   - the stored text, its jsonb-canonical form, computed the first time a
//     storage route asks for it;
//   - a NUL flag, computed when the Value is made, because Windmill stores
//     every result and PostgreSQL rejects the escape \u0000 (OR-27, FX-48).
//
// Which consumer sees which text is the routing table of ADR 0005, applied
// by the engine. This package does not decode what it holds: the
// canonicaliser that computes the stored text streams over the bytes.
package jsonval

import (
	"bytes"
	"errors"
	"sync"
)

// Value is one JSON value held as text. The zero Value holds no value at
// all, which is not JSON null: it marks an absent value (a site that bound
// nothing, a step that produced nothing).
//
// A Value is immutable and safe for concurrent use; copying it is cheap and
// shares the text.
type Value struct {
	r *rep
}

type rep struct {
	producer []byte
	nul      bool

	storedOnce sync.Once
	stored     []byte
	storedErr  error
}

// FromProducer returns the Value whose producer text is text: exactly one
// JSON value, as its producer wrote it. FromProducer does not validate or
// copy text; the caller hands it over and must not modify it afterwards.
func FromProducer(text []byte) Value {
	return Value{r: &rep{producer: text, nul: hasNULEscape(text)}}
}

// IsZero reports whether v holds no value. It lets struct fields tagged
// `json:",omitzero"` drop absent values.
func (v Value) IsZero() bool { return v.r == nil }

// Producer returns the producer text. The caller must not modify it. It is
// nil for the zero Value.
func (v Value) Producer() []byte {
	if v.r == nil {
		return nil
	}
	return v.r.producer
}

// HasNUL reports whether the value's text holds the escape \u0000 in a
// string or a key. Such a value fails at every storage boundary (OR-27).
// It is the one rejection known without canonicalising (ADR 0005); Stored
// reports the others.
func (v Value) HasNUL() bool { return v.r != nil && v.r.nul }

// Stored returns the stored text of v: the text PostgreSQL jsonb gives back
// for the producer text, without jsonb's output spaces. Keys are in jsonb's
// order with the last duplicate kept, numbers as numeric writes them, and
// strings in jsonb's escaping (ADR 0005). It is computed on first use, once.
// The caller must not modify it; it shares the producer's bytes when they are
// already in stored form, and the stored text of a Value made from stored
// text is that text itself.
//
// Stored fails with a *StoreError for the first token jsonb rejects, in text
// order: errors.Is(err, ErrNUL) for \u0000, and ErrUnpairedSurrogate,
// ErrNumericOverflow or ErrSyntax otherwise. It fails with another error for
// the zero Value.
func (v Value) Stored() ([]byte, error) {
	if v.r == nil {
		return nil, errZero
	}
	v.r.storedOnce.Do(func() {
		v.r.stored, v.r.storedErr = canonicalise(v.r.producer)
	})
	return v.r.stored, v.r.storedErr
}

var errZero = errors.New("jsonval: zero Value holds no value")

// String returns the producer text, for diagnostics.
func (v Value) String() string { return string(v.Producer()) }

// MarshalJSON returns the producer text. It fails for the zero Value, so an
// absent value is never silently written as null; tag such fields
// `json:",omitzero"`.
//
// encoding/json does not embed that text byte for byte: it compacts it and,
// unless the Encoder's SetEscapeHTML(false) is set, escapes <, > and &. That
// keeps the parsed value but not the spelling, so a writer that must keep
// the producer's bytes, such as harness frames (ADR 0004) or recordings,
// writes Producer() itself.
func (v Value) MarshalJSON() ([]byte, error) {
	if v.r == nil {
		return nil, errZero
	}
	return v.r.producer, nil
}

// UnmarshalJSON keeps a copy of the JSON text it is given as the producer
// text, so a value read from a frame or a file keeps its spelling.
func (v *Value) UnmarshalJSON(data []byte) error {
	*v = FromProducer(bytes.Clone(data))
	return nil
}

// hasNULEscape reports whether JSON text holds the escape \u0000. In valid
// JSON a backslash occurs only inside a string or a key and always starts an
// escape, so the scan needs no string state: it steps over each escape
// whole, which keeps an escaped backslash followed by "u0000" from counting.
func hasNULEscape(text []byte) bool {
	for i := bytes.IndexByte(text, '\\'); i >= 0 && i+1 < len(text); {
		if text[i+1] == 'u' && i+6 <= len(text) && string(text[i+2:i+6]) == "0000" {
			return true
		}
		next := bytes.IndexByte(text[i+2:], '\\')
		if next < 0 {
			return false
		}
		i += 2 + next
	}
	return false
}
