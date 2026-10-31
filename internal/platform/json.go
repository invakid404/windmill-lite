package platform

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// This file holds JSON values as Windmill's backend JSON library holds
// them (FX-42 rules 2 and 9): what the API's interpolation and the
// worker's argument resolution parse and write out again, and the results
// of a loop's or branchall's children, which the worker reads into values
// and writes out as the container's result (Reprint). An object keeps its keys
// in insertion order, and a repeated key keeps its first position with its
// last value. A number without a fraction or an exponent is an integer
// when a signed or an unsigned 64-bit integer holds it exactly (except
// -0); every other number is a double. Written out, the value is compact,
// a string's escapes are minimal, an integer is its digits, and a double
// is the shortest text that reads back as it, in the library's notation:
// plain decimals, with ".0" for a whole number, for a decimal exponent
// from -5 to 15, and otherwise one digit, the rest after a point, and
// "e+N" or "e-N" (1000.0, 0.00001, 1e+21, 1.2345678901234568e+29, 5e-7).

// kind is a JSON value's type.
type kind uint8

const (
	kNull kind = iota
	kBool
	kNumber
	kString
	kArray
	kObject
)

// node is one JSON value. A literal or a number keeps its written-out
// text in text, a string its decoded value.
type node struct {
	kind  kind
	text  string
	items []*node
	keys  []string
	vals  []*node
}

// set adds a member to an object, or replaces the value of the key it
// already has in place.
func (n *node) set(k string, v *node) {
	for i, kk := range n.keys {
		if kk == k {
			n.vals[i] = v
			return
		}
	}
	n.keys = append(n.keys, k)
	n.vals = append(n.vals, v)
}

func stringNode(s string) *node { return &node{kind: kString, text: s} }

// write appends the value's text as the library writes it.
func (n *node) write(b []byte) []byte {
	switch n.kind {
	case kString:
		return appendString(b, n.text)
	case kArray:
		b = append(b, '[')
		for i, it := range n.items {
			if i > 0 {
				b = append(b, ',')
			}
			b = it.write(b)
		}
		return append(b, ']')
	case kObject:
		b = append(b, '{')
		for i, k := range n.keys {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendString(b, k)
			b = append(b, ':')
			b = n.vals[i].write(b)
		}
		return append(b, '}')
	default:
		return append(b, n.text...)
	}
}

// appendString writes s as a JSON string with the library's escapes: the
// quote, the backslash, and the control characters below U+0020, as \b,
// \t, \n, \f, \r or \u00XX; everything else as it is, solidus included.
func appendString(b []byte, s string) []byte {
	const hex = "0123456789abcdef"
	b = append(b, '"')
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		var esc string
		switch {
		case c == '"':
			esc = `\"`
		case c == '\\':
			esc = `\\`
		case c == '\b':
			esc = `\b`
		case c == '\t':
			esc = `\t`
		case c == '\n':
			esc = `\n`
		case c == '\f':
			esc = `\f`
		case c == '\r':
			esc = `\r`
		case c < 0x20:
			esc = `\u00` + string(hex[c>>4]) + string(hex[c&0xf])
		default:
			continue
		}
		b = append(b, s[start:i]...)
		b = append(b, esc...)
		start = i + 1
	}
	b = append(b, s[start:]...)
	return append(b, '"')
}

// formatDouble writes a finite double in the library's notation (see the
// top of this file).
func formatDouble(f float64) string {
	sci := strconv.FormatFloat(f, 'e', -1, 64) // [-]d[.ddd]e±XX
	neg := strings.HasPrefix(sci, "-")
	sci = strings.TrimPrefix(sci, "-")
	mant, expText, _ := strings.Cut(sci, "e")
	digits := strings.Replace(mant, ".", "", 1)
	exp, _ := strconv.Atoi(expText)
	var out string
	switch n := len(digits); {
	case exp >= -5 && exp <= 15 && n-1 <= exp:
		out = digits + strings.Repeat("0", exp-(n-1)) + ".0"
	case exp >= 0 && exp <= 15:
		out = digits[:exp+1] + "." + digits[exp+1:]
	case exp >= -5 && exp < 0:
		out = "0." + strings.Repeat("0", -exp-1) + digits
	default:
		out = digits[:1]
		if n > 1 {
			out += "." + digits[1:]
		}
		if exp >= 0 {
			out += "e+" + strconv.Itoa(exp)
		} else {
			out += "e-" + strconv.Itoa(-exp)
		}
	}
	if neg {
		out = "-" + out
	}
	return out
}

// parseError is a parse failure in the library's wording: "<message> at
// line <l> column <c>", the column counting the bytes of the line up to
// where the reader stood.
type parseError struct {
	msg          string
	line, column int
}

func (e *parseError) Error() string {
	return fmt.Sprintf("%s at line %d column %d", e.msg, e.line, e.column)
}

// recursionLimit is the library's nesting budget when it parses: each array
// or object opened takes one, and the one that would use the last fails,
// so a value nests at most 127 deep and the array or object opened at
// depth 128 fails ([FX-42] rule 9, [JE-49]).
//
// [FX-42]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/execution.md#fx-42
// [JE-49]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/expressions.md#je-49
const recursionLimit = 128

// parser reads one JSON text with the library's rules.
type parser struct {
	src   []byte
	pos   int
	depth int
}

// parseJSON parses text as the library does: one value, optionally
// surrounded by whitespace, anything else after it refused.
func parseJSON(text []byte) (*node, error) {
	p := &parser{src: text}
	v, err := p.value()
	if err != nil {
		return nil, err
	}
	p.space()
	if p.pos < len(p.src) {
		return nil, p.fail("trailing characters", p.pos+1)
	}
	return v, nil
}

// fail builds the error for a reader that stood at offset at.
func (p *parser) fail(msg string, at int) error {
	if at > len(p.src) {
		at = len(p.src)
	}
	line, start := 1, 0
	for i := 0; i < at; i++ {
		if p.src[i] == '\n' {
			line, start = line+1, i+1
		}
	}
	return &parseError{msg: msg, line: line, column: at - start}
}

func (p *parser) space() {
	for p.pos < len(p.src) {
		switch p.src[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

func (p *parser) value() (*node, error) {
	p.space()
	if p.pos >= len(p.src) {
		return nil, p.fail("EOF while parsing a value", p.pos)
	}
	switch c := p.src[p.pos]; {
	case c == 'n':
		return p.literal("null", kNull)
	case c == 't':
		return p.literal("true", kBool)
	case c == 'f':
		return p.literal("false", kBool)
	case c == '"':
		p.pos++
		s, err := p.str()
		if err != nil {
			return nil, err
		}
		return stringNode(s), nil
	case c == '-' || c >= '0' && c <= '9':
		return p.number()
	case c == '[':
		return p.array()
	case c == '{':
		return p.object()
	default:
		return nil, p.fail("expected value", p.pos+1)
	}
}

func (p *parser) literal(word string, k kind) (*node, error) {
	for i := 0; i < len(word); i++ {
		if p.pos >= len(p.src) {
			return nil, p.fail("EOF while parsing a value", p.pos)
		}
		if p.src[p.pos] != word[i] {
			return nil, p.fail("expected ident", p.pos+1)
		}
		p.pos++
	}
	return &node{kind: k, text: word}, nil
}

func (p *parser) nest() error {
	if p.depth++; p.depth >= recursionLimit {
		return p.fail("recursion limit exceeded", p.pos+1)
	}
	return nil
}

func (p *parser) array() (*node, error) {
	if err := p.nest(); err != nil {
		return nil, err
	}
	p.pos++
	n := &node{kind: kArray, items: []*node{}}
	p.space()
	switch {
	case p.pos >= len(p.src):
		// The library reports the unclosed list here, and "a value" only
		// where a value was due, after a comma (`[` and `[1,`).
		return nil, p.fail("EOF while parsing a list", p.pos)
	case p.src[p.pos] == ']':
		p.pos++
		p.depth--
		return n, nil
	}
	for {
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		n.items = append(n.items, v)
		p.space()
		if p.pos >= len(p.src) {
			return nil, p.fail("EOF while parsing a list", p.pos)
		}
		switch p.src[p.pos] {
		case ',':
			p.pos++
			p.space()
			if p.pos < len(p.src) && p.src[p.pos] == ']' {
				return nil, p.fail("trailing comma", p.pos+1)
			}
		case ']':
			p.pos++
			p.depth--
			return n, nil
		default:
			return nil, p.fail("expected `,` or `]`", p.pos+1)
		}
	}
}

func (p *parser) object() (*node, error) {
	if err := p.nest(); err != nil {
		return nil, err
	}
	p.pos++
	n := &node{kind: kObject, keys: []string{}, vals: []*node{}}
	p.space()
	if p.pos < len(p.src) && p.src[p.pos] == '}' {
		p.pos++
		p.depth--
		return n, nil
	}
	for {
		p.space()
		switch {
		case p.pos >= len(p.src) && len(n.keys) > 0:
			// After a comma a key is due, and the library reports the end
			// of the text there as a missing value, not as an unclosed
			// object.
			return nil, p.fail("EOF while parsing a value", p.pos)
		case p.pos >= len(p.src):
			return nil, p.fail("EOF while parsing an object", p.pos)
		case p.src[p.pos] != '"':
			return nil, p.fail("key must be a string", p.pos+1)
		}
		p.pos++
		k, err := p.str()
		if err != nil {
			return nil, err
		}
		p.space()
		switch {
		case p.pos >= len(p.src):
			return nil, p.fail("EOF while parsing an object", p.pos)
		case p.src[p.pos] != ':':
			return nil, p.fail("expected `:`", p.pos+1)
		}
		p.pos++
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		n.set(k, v)
		p.space()
		if p.pos >= len(p.src) {
			return nil, p.fail("EOF while parsing an object", p.pos)
		}
		switch p.src[p.pos] {
		case ',':
			p.pos++
			p.space()
			if p.pos < len(p.src) && p.src[p.pos] == '}' {
				return nil, p.fail("trailing comma", p.pos+1)
			}
		case '}':
			p.pos++
			p.depth--
			return n, nil
		default:
			return nil, p.fail("expected `,` or `}`", p.pos+1)
		}
	}
}

// str reads a string's body after its opening quote. Escapes must be
// valid, a surrogate escape must be one of a pair, and a control character
// must be escaped.
func (p *parser) str() (string, error) {
	var b strings.Builder
	for {
		if p.pos >= len(p.src) {
			return "", p.fail("EOF while parsing a string", p.pos)
		}
		c := p.src[p.pos]
		switch {
		case c == '"':
			p.pos++
			return b.String(), nil
		case c < 0x20:
			p.pos++
			return "", p.fail("control character (\\u0000-\\u001F) found while parsing a string", p.pos)
		case c != '\\':
			r, size := utf8.DecodeRune(p.src[p.pos:])
			if r == utf8.RuneError && size == 1 {
				p.pos++
				return "", p.fail("invalid unicode code point", p.pos)
			}
			b.Write(p.src[p.pos : p.pos+size])
			p.pos += size
			continue
		}
		p.pos++
		if p.pos >= len(p.src) {
			return "", p.fail("EOF while parsing a string", p.pos)
		}
		e := p.src[p.pos]
		p.pos++
		switch e {
		case '"', '\\', '/':
			b.WriteByte(e)
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'u':
			r, err := p.unicode()
			if err != nil {
				return "", err
			}
			b.WriteRune(r)
		default:
			return "", p.fail("invalid escape", p.pos)
		}
	}
}

// unicode reads a \u escape's code point, after its "\u".
func (p *parser) unicode() (rune, error) {
	n, err := p.hex4()
	if err != nil {
		return 0, err
	}
	switch {
	case n >= 0xDC00 && n <= 0xDFFF:
		return 0, p.fail("lone leading surrogate in hex escape", p.pos)
	case n < 0xD800 || n > 0xDBFF:
		return rune(n), nil
	}
	for _, want := range []byte{'\\', 'u'} {
		if p.pos >= len(p.src) {
			return 0, p.fail("EOF while parsing a string", p.pos)
		}
		if p.src[p.pos] != want {
			p.pos++
			return 0, p.fail("unexpected end of hex escape", p.pos)
		}
		p.pos++
	}
	n2, err := p.hex4()
	if err != nil {
		return 0, err
	}
	if n2 < 0xDC00 || n2 > 0xDFFF {
		return 0, p.fail("lone leading surrogate in hex escape", p.pos)
	}
	return rune((n-0xD800)<<10|(n2-0xDC00)) + 0x10000, nil
}

func (p *parser) hex4() (uint32, error) {
	var n uint32
	for range 4 {
		if p.pos >= len(p.src) {
			return 0, p.fail("EOF while parsing a string", p.pos)
		}
		c := p.src[p.pos]
		p.pos++
		var d byte
		switch {
		case c >= '0' && c <= '9':
			d = c - '0'
		case c >= 'a' && c <= 'f':
			d = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			d = c - 'A' + 10
		default:
			return 0, p.fail("invalid escape", p.pos)
		}
		n = n<<4 | uint32(d)
	}
	return n, nil
}

// number reads a number with JSON's grammar, and classifies it as the
// library does: an integer that a signed or unsigned 64-bit integer holds
// exactly stays one, -0 and every other number becomes a double, and one
// beyond a double's range is refused.
func (p *parser) number() (*node, error) {
	start := p.pos
	neg := p.src[p.pos] == '-'
	if neg {
		p.pos++
	}
	digits := func() int {
		n := 0
		for p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
			p.pos++
			n++
		}
		return n
	}
	intStart := p.pos
	switch {
	case p.pos >= len(p.src):
		return nil, p.fail("EOF while parsing a value", p.pos)
	case p.src[p.pos] == '0':
		p.pos++
		if p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
			return nil, p.fail("invalid number", p.pos+1)
		}
	case p.src[p.pos] >= '1' && p.src[p.pos] <= '9':
		digits()
	default:
		return nil, p.fail("invalid number", p.pos+1)
	}
	intEnd, float := p.pos, false
	if p.pos < len(p.src) && p.src[p.pos] == '.' {
		float = true
		p.pos++
		if digits() == 0 {
			if p.pos >= len(p.src) {
				return nil, p.fail("EOF while parsing a value", p.pos)
			}
			return nil, p.fail("invalid number", p.pos+1)
		}
	}
	if p.pos < len(p.src) && (p.src[p.pos] == 'e' || p.src[p.pos] == 'E') {
		float = true
		p.pos++
		if p.pos < len(p.src) && (p.src[p.pos] == '+' || p.src[p.pos] == '-') {
			p.pos++
		}
		if digits() == 0 {
			if p.pos >= len(p.src) {
				return nil, p.fail("EOF while parsing a value", p.pos)
			}
			return nil, p.fail("invalid number", p.pos+1)
		}
	}
	tok := string(p.src[start:p.pos])
	if !float {
		if u, err := strconv.ParseUint(string(p.src[intStart:intEnd]), 10, 64); err == nil {
			switch {
			case !neg:
				return &node{kind: kNumber, text: strconv.FormatUint(u, 10)}, nil
			case u != 0 && u <= 1<<63:
				return &node{kind: kNumber, text: tok}, nil
			}
		}
	}
	f, err := strconv.ParseFloat(tok, 64)
	if math.IsInf(f, 0) {
		return nil, p.fail("number out of range", p.pos)
	}
	if err != nil && !isRangeError(err) {
		return nil, p.fail("invalid number", p.pos)
	}
	return &node{kind: kNumber, text: formatDouble(f)}, nil
}

// isRangeError reports a ParseFloat error that is only about range: an
// underflow to zero, which the library reads as zero.
func isRangeError(err error) bool {
	ne, ok := err.(*strconv.NumError)
	return ok && ne.Err == strconv.ErrRange
}

// JSONString writes s as a JSON string as the backend's JSON library does:
// the body of a variable's value (WA-11).
func JSONString(s string) []byte { return appendString(nil, s) }

// Reprint parses text, one JSON value, and writes it out again as the
// backend's JSON library does (FX-42 rule 9): compact, keys in their order,
// a string's escapes minimal, and numbers in the library's notation (see
// the top of this file), -0 as -0.0. It fails where the library's parse
// does, for a number beyond a double's range among others. The engine
// writes a loop's or branchall's result this way (JE-44).
func Reprint(text []byte) ([]byte, error) {
	n, err := parseJSON(text)
	if err != nil {
		return nil, err
	}
	return n.write(nil), nil
}

// FormatDouble writes a finite double in the backend's JSON library's
// notation (see the top of this file).
func FormatDouble(f float64) string { return formatDouble(f) }
