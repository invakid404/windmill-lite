package harness

import (
	"encoding/base64"
	"slices"
	"strconv"
	"unicode/utf8"
)

// envelope writes one frame payload in canonical form: a JSON object whose
// keys come in the order of ADR 0004's message table, `t` first, strings
// escaped exactly as JavaScript's JSON.stringify escapes them, and raw JSON
// values spliced in byte for byte. Go and every harness write the same bytes
// for the same message, which is what makes golden frames comparable across
// languages (conformance/protocol/frames.json).
type envelope struct {
	b     []byte
	first bool
	err   error
}

func newEnvelope(b []byte, t string) *envelope {
	e := &envelope{b: append(b, '{'), first: true}
	e.str("t", t)
	return e
}

func (e *envelope) key(k string) {
	if !e.first {
		e.b = append(e.b, ',')
	}
	e.first = false
	e.b = append(e.b, '"')
	e.b = append(e.b, k...)
	e.b = append(e.b, '"', ':')
}

func (e *envelope) str(k, v string) {
	e.key(k)
	var err error
	if e.b, err = appendJSONString(e.b, v); err != nil && e.err == nil {
		e.err = err
	}
}

// dataKey writes the key of a member whose key is data rather than a
// protocol field name, escaped as a string.
func (e *envelope) dataKey(k string) {
	if !e.first {
		e.b = append(e.b, ',')
	}
	e.first = false
	var err error
	if e.b, err = appendJSONString(e.b, k); err != nil && e.err == nil {
		e.err = err
	}
	e.b = append(e.b, ':')
}

// pair writes a member whose key is data, with a string value.
func (e *envelope) pair(k, v string) {
	e.dataKey(k)
	var err error
	if e.b, err = appendJSONString(e.b, v); err != nil && e.err == nil {
		e.err = err
	}
}

// strMap writes an object of strings whose keys are data (env, headers):
// keys are escaped like values and sorted bytewise, because a Go map has no
// order.
func (e *envelope) strMap(k string, m map[string]string) {
	keys := make([]string, 0, len(m))
	for name := range m {
		keys = append(keys, name)
	}
	slices.Sort(keys)
	e.object(k, func(e *envelope) {
		for _, name := range keys {
			e.pair(name, m[name])
		}
	})
}

// intMap writes an object of integers whose keys are data, sorted
// bytewise as strMap sorts them.
func (e *envelope) intMap(k string, m map[string]int) {
	keys := make([]string, 0, len(m))
	for name := range m {
		keys = append(keys, name)
	}
	slices.Sort(keys)
	e.object(k, func(e *envelope) {
		for _, name := range keys {
			e.dataKey(name)
			e.b = strconv.AppendInt(e.b, int64(m[name]), 10)
		}
	})
}

// bytes writes b as padded standard base64, or nothing when b is nil.
func (e *envelope) bytes(k string, b []byte) {
	if b == nil {
		return
	}
	e.key(k)
	e.b = append(e.b, '"')
	e.b = base64.StdEncoding.AppendEncode(e.b, b)
	e.b = append(e.b, '"')
}

func (e *envelope) int(k string, v int64) {
	e.key(k)
	e.b = strconv.AppendInt(e.b, v, 10)
}

func (e *envelope) bool(k string, v bool) {
	e.key(k)
	e.b = strconv.AppendBool(e.b, v)
}

// raw splices v, which must be one JSON value, without re-encoding it.
func (e *envelope) raw(k string, v []byte) {
	e.key(k)
	e.b = append(e.b, v...)
}

// object writes a nested object whose members fill writes.
func (e *envelope) object(k string, fill func(*envelope)) {
	e.key(k)
	inner := &envelope{b: append(e.b, '{'), first: true}
	fill(inner)
	e.b = append(inner.b, '}')
	if inner.err != nil && e.err == nil {
		e.err = inner.err
	}
}

func (e *envelope) strings(k string, vs []string) {
	e.key(k)
	var err error
	if e.b, err = appendJSONStrings(e.b, vs); err != nil && e.err == nil {
		e.err = err
	}
}

// appendJSONStrings appends vs as a JSON array of strings.
func appendJSONStrings(b []byte, vs []string) ([]byte, error) {
	b = append(b, '[')
	var first error
	for i, v := range vs {
		if i > 0 {
			b = append(b, ',')
		}
		var err error
		if b, err = appendJSONString(b, v); err != nil && first == nil {
			first = err
		}
	}
	return append(b, ']'), first
}

func (e *envelope) end() ([]byte, error) {
	e.b = append(e.b, '}')
	return e.b, e.err
}

// appendJSONString appends s as a JSON string with JSON.stringify's
// escaping: the two-character escapes for ", \, backspace, form feed,
// newline, carriage return and tab, \u00xx with lowercase hex for the other
// control characters, and every other character as its UTF-8 bytes. A
// string that is not valid UTF-8 has no such form and is refused.
func appendJSONString(b []byte, s string) ([]byte, error) {
	if !utf8.ValidString(s) {
		return b, protoErr(CodeInvalidUTF8, "string is not valid UTF-8")
	}
	const hex = "0123456789abcdef"
	b = append(b, '"')
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		var esc string
		switch c {
		case '"':
			esc = `\"`
		case '\\':
			esc = `\\`
		case '\b':
			esc = `\b`
		case '\f':
			esc = `\f`
		case '\n':
			esc = `\n`
		case '\r':
			esc = `\r`
		case '\t':
			esc = `\t`
		default:
			if c >= 0x20 {
				continue
			}
		}
		b = append(b, s[start:i]...)
		if esc != "" {
			b = append(b, esc...)
		} else {
			b = append(b, '\\', 'u', '0', '0', hex[c>>4], hex[c&0xf])
		}
		start = i + 1
	}
	b = append(b, s[start:]...)
	return append(b, '"'), nil
}
