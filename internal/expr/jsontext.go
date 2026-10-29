package expr

import (
	"bytes"
	"encoding/json"
	"strconv"
	"unicode/utf8"
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

// DatabaseText returns jsonb's printed form of a stored text, with a space
// after every : and , outside strings: the text a value read from the
// database has (JE-44). The engine writes the flow input's members in it.
func DatabaseText(stored []byte) []byte { return databaseText(stored) }

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

// validJSON reports whether t is exactly one JSON value (RFC 8259) in valid
// UTF-8, with white space around it allowed. It checks syntax only, through
// the whole text: it has no depth limit, and none of jsonb's restrictions,
// so a \u0000 escape, an unpaired surrogate escape and a number beyond
// numeric's range are valid. Its stack of open containers is a slice, so a
// deep value costs memory, not stack.
func validJSON(t []byte) bool {
	if !utf8.Valid(t) {
		return false
	}
	s := scanner{b: t}
	var open []byte // the opening bracket of each container not yet closed
	for {
		// A value.
		s.space()
		if s.i >= len(t) {
			return false
		}
		switch c := t[s.i]; {
		case c == '{' || c == '[':
			s.i++
			s.space()
			if s.peek(c + 2) { // '{'+2 is '}', '['+2 is ']'
				s.i++
				break
			}
			open = append(open, c)
			if c == '{' && !s.validKey() {
				return false
			}
			continue
		case c == '"':
			if !s.validString() {
				return false
			}
		case c == '-' || c >= '0' && c <= '9':
			if !s.validNumber() {
				return false
			}
		default:
			if !s.validLiteral() {
				return false
			}
		}
		// After a value: close containers until a comma asks for the next
		// value, or the text ends at the top level.
		for next := false; !next; {
			s.space()
			if len(open) == 0 {
				return s.i == len(t)
			}
			top := open[len(open)-1]
			switch {
			case s.peek(','):
				s.i++
				if top == '{' && !s.validKey() {
					return false
				}
				next = true
			case s.peek(top + 2):
				s.i++
				open = open[:len(open)-1]
			default:
				return false
			}
		}
	}
}

// validKey checks an object member's key and its colon, with the white
// space before each.
func (s *scanner) validKey() bool {
	s.space()
	if !s.peek('"') || !s.validString() {
		return false
	}
	s.space()
	if !s.peek(':') {
		return false
	}
	s.i++
	return true
}

// validString checks a string at the offset: no unescaped control
// character, and only JSON's escapes.
func (s *scanner) validString() bool {
	for s.i++; s.i < len(s.b); s.i++ {
		switch c := s.b[s.i]; {
		case c == '"':
			s.i++
			return true
		case c < 0x20:
			return false
		case c == '\\':
			s.i++
			if s.i >= len(s.b) {
				return false
			}
			switch s.b[s.i] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
			case 'u':
				if s.i+4 >= len(s.b) {
					return false
				}
				for _, h := range s.b[s.i+1 : s.i+5] {
					if !isHex(h) {
						return false
					}
				}
				s.i += 4
			default:
				return false
			}
		}
	}
	return false
}

func isHex(c byte) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }

// validNumber checks a number at the offset:
// -?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?
func (s *scanner) validNumber() bool {
	digits := func() int {
		start := s.i
		for s.i < len(s.b) && s.b[s.i] >= '0' && s.b[s.i] <= '9' {
			s.i++
		}
		return s.i - start
	}
	if s.peek('-') {
		s.i++
	}
	switch {
	case s.peek('0'):
		s.i++
	case digits() == 0:
		return false
	}
	if s.peek('.') {
		s.i++
		if digits() == 0 {
			return false
		}
	}
	if s.peek('e') || s.peek('E') {
		s.i++
		if s.peek('+') || s.peek('-') {
			s.i++
		}
		if digits() == 0 {
			return false
		}
	}
	return true
}

// validLiteral checks true, false or null at the offset.
func (s *scanner) validLiteral() bool {
	for _, lit := range []string{"true", "false", "null"} {
		if bytes.HasPrefix(s.b[s.i:], []byte(lit)) {
			s.i += len(lit)
			return true
		}
	}
	return false
}
