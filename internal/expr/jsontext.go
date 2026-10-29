package expr

import (
	"bytes"
	"encoding/json"
	"strconv"
)

// The shortcuts read JSON text without decoding it: JE-7 returns a member
// of the flow input byte for byte, and JE-9 walks a stored result the way
// PostgreSQL's #> operator walks jsonb. Both read text that a Value holds,
// which is one JSON value; on text that is not, they report a miss rather
// than fail.

// member returns the text of the member named key of the JSON object text
// obj, verbatim, and false when obj is not an object or has no such member.
// Of duplicate keys the last is taken, as jsonb and a decoded map keep it.
func member(obj []byte, key string) ([]byte, bool) {
	s := scanner{b: obj}
	s.space()
	if !s.peek('{') {
		return nil, false
	}
	s.i++
	var found []byte
	ok := false
	for {
		s.space()
		if s.peek('}') {
			return found, ok
		}
		ks, ke, good := s.string()
		if !good {
			return nil, false
		}
		s.space()
		if !s.peek(':') {
			return nil, false
		}
		s.i++
		s.space()
		vs, ve, good := s.value()
		if !good {
			return nil, false
		}
		if keyIs(obj[ks:ke], key) {
			found, ok = obj[vs:ve:ve], true
		}
		s.space()
		switch {
		case s.peek(','):
			s.i++
		case s.peek('}'):
			return found, ok
		default:
			return nil, false
		}
	}
}

// element returns the text of the n-th element of the JSON array text arr,
// and false when arr is not an array or is shorter.
func element(arr []byte, n int) ([]byte, bool) {
	s := scanner{b: arr}
	s.space()
	if !s.peek('[') {
		return nil, false
	}
	s.i++
	s.space()
	if s.peek(']') {
		return nil, false
	}
	for k := 0; ; k++ {
		s.space()
		vs, ve, good := s.value()
		if !good {
			return nil, false
		}
		if k == n {
			return arr[vs:ve:ve], true
		}
		s.space()
		if !s.peek(',') {
			return nil, false
		}
		s.i++
	}
}

// walk applies a path to JSON text as PostgreSQL's #> operator applies a
// text[] path to jsonb (JE-9): an object takes the element as a key, an
// array takes it as an index, which must be a whole decimal int4, and
// anything else, or a miss, ends the walk with no value.
func walk(text []byte, path []string) ([]byte, bool) {
	cur := text
	for _, p := range path {
		s := scanner{b: cur}
		s.space()
		switch {
		case s.peek('{'):
			v, ok := member(cur, p)
			if !ok {
				return nil, false
			}
			cur = v
		case s.peek('['):
			// The path's elements hold only [A-Za-z0-9_], so no sign or
			// space reaches the parse; leading zeros are allowed.
			n, err := strconv.ParseInt(p, 10, 32)
			if err != nil || n < 0 {
				return nil, false
			}
			v, ok := element(cur, int(n))
			if !ok {
				return nil, false
			}
			cur = v
		default:
			return nil, false
		}
	}
	return cur, true
}

// databaseText returns jsonb's text output for stored text: the stored
// text with a space after every : and , outside strings. The JE-9 lookup
// returns this text (JE-44), and a site that quotes its value shows the
// spaces, as the timeout site's parse error does.
func databaseText(stored []byte) []byte {
	var out []byte
	inString, escaped := false, false
	for i, c := range stored {
		if out != nil {
			out = append(out, c)
		}
		switch {
		case inString:
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
		case c == '"':
			inString = true
		case c == ':' || c == ',':
			if out == nil {
				out = append(make([]byte, 0, len(stored)+len(stored)/4), stored[:i+1]...)
			}
			out = append(out, ' ')
		}
	}
	if out == nil {
		return stored
	}
	return out
}

// keyIs reports whether the JSON string text raw, quotes included, decodes
// to key.
func keyIs(raw []byte, key string) bool {
	inner := raw[1 : len(raw)-1]
	if bytes.IndexByte(inner, '\\') < 0 {
		return string(inner) == key
	}
	var k string
	return json.Unmarshal(raw, &k) == nil && k == key
}

// scanner walks JSON text by byte offsets.
type scanner struct {
	b []byte
	i int
}

func (s *scanner) peek(c byte) bool { return s.i < len(s.b) && s.b[s.i] == c }

func (s *scanner) space() {
	for s.i < len(s.b) {
		switch s.b[s.i] {
		case ' ', '\t', '\n', '\r':
			s.i++
		default:
			return
		}
	}
}

// string skips a string at the offset and returns its span, quotes
// included.
func (s *scanner) string() (int, int, bool) {
	if !s.peek('"') {
		return 0, 0, false
	}
	start := s.i
	for s.i++; s.i < len(s.b); s.i++ {
		switch s.b[s.i] {
		case '\\':
			s.i++
		case '"':
			s.i++
			return start, s.i, true
		}
	}
	return 0, 0, false
}

// value skips one value at the offset and returns its span.
func (s *scanner) value() (int, int, bool) {
	if s.i >= len(s.b) {
		return 0, 0, false
	}
	start := s.i
	switch s.b[s.i] {
	case '"':
		return s.string()
	case '{', '[':
		depth := 0
		for s.i < len(s.b) {
			switch s.b[s.i] {
			case '"':
				if _, _, ok := s.string(); !ok {
					return 0, 0, false
				}
				continue
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					s.i++
					return start, s.i, true
				}
			}
			s.i++
		}
		return 0, 0, false
	}
	for s.i < len(s.b) {
		switch s.b[s.i] {
		case ',', '}', ']', ' ', '\t', '\n', '\r':
			return start, s.i, s.i > start
		}
		s.i++
	}
	return start, s.i, s.i > start
}
