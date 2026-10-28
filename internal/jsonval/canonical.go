package jsonval

import (
	"bytes"
	"cmp"
	"slices"
	"unicode/utf16"
	"unicode/utf8"
)

// canonicalise turns producer text into stored text: the text PostgreSQL
// jsonb gives back for it (ADR 0005, FX-42, JE-44), without its output
// spaces. It makes one pass over the bytes and builds no Go values:
//
//   - object keys are ordered by UTF-8 byte length, then bytewise, comparing
//     the decoded keys; of duplicate keys only the last is kept;
//   - numbers are written as PostgreSQL numeric writes them: in plain
//     decimal with the exponent applied, keeping the scale (the fraction
//     digits less the exponent, at least zero), and without a sign on zero;
//   - strings have their escapes decoded and are written as jsonb writes
//     them: \" \\ \b \f \n \r \t, other control characters as \u00xx with
//     lowercase hex digits, and everything else as raw UTF-8;
//   - whitespace is dropped. jsonb's own output puts a space after ':' and
//     ','; no consumer can see that after parsing (ADR 0005).
//
// Stored text is a fixed point: canonicalising it again returns the same
// bytes, so canonicalise returns src itself when nothing changes.
//
// It fails with a *StoreError. For valid JSON text that is the first token,
// in text order, that jsonb input rejects, which is the one PostgreSQL
// reports. Text that is not one JSON value is always rejected, but the kind
// can differ from PostgreSQL's, whose parser reads one token ahead: it
// reports the NUL in `[1 "\u0000"]` where this reports the syntax error.
//
// PostgreSQL's nesting limit and jsonb's size limits are not reproduced
// (see the README).
func canonicalise(src []byte) ([]byte, error) {
	c := canonicaliser{src: src, out: make([]byte, 0, len(src))}
	if err := c.run(); err != nil {
		return nil, err
	}
	if bytes.Equal(c.out, src) {
		return src, nil
	}
	return c.out, nil
}

// PostgreSQL numeric's limits on what jsonb input accepts.
const (
	// maxIntegerDigits and maxScale bound the digits before and after the
	// decimal point ("up to 131072 digits before the decimal point; up to
	// 16383 digits after", PostgreSQL's numeric type documentation).
	maxIntegerDigits = 131072
	maxScale         = 16383
	// maxExponent bounds an exponent's magnitude before it is applied.
	maxExponent = 1<<30 - 1
)

type canonicaliser struct {
	src []byte
	pos int
	out []byte

	// stack holds the open containers. members holds the members of the
	// open objects and keys their decoded keys, both used as stacks.
	stack   []frame
	members []member
	keys    []byte
	scratch []byte
}

type frame struct {
	object bool
	// start is the offset in out just past the opening '{'; members and
	// keys are where the object's entries begin in those stacks.
	start   int
	members int
	keys    int
}

// member locates one object member: out[start:end] is its key, ':' and
// value; keys[key:keyEnd] is its decoded key.
type member struct {
	start, end  int
	key, keyEnd int
}

func (c *canonicaliser) run() error {
value:
	for {
		c.skipSpace()
		if c.pos == len(c.src) {
			return c.syntax(c.pos, "unexpected end of text")
		}
		switch b := c.src[c.pos]; {
		case b == '{':
			c.pos++
			c.out = append(c.out, '{')
			if c.skipSpace(); c.peek('}') {
				c.pos++
				c.out = append(c.out, '}')
				break
			}
			c.stack = append(c.stack, frame{object: true, start: len(c.out), members: len(c.members), keys: len(c.keys)})
			if err := c.key(); err != nil {
				return err
			}
			continue value
		case b == '[':
			c.pos++
			c.out = append(c.out, '[')
			if c.skipSpace(); c.peek(']') {
				c.pos++
				c.out = append(c.out, ']')
				break
			}
			c.stack = append(c.stack, frame{})
			continue value
		case b == '"':
			if err := c.string(false); err != nil {
				return err
			}
		case b == '-' || '0' <= b && b <= '9':
			if err := c.number(); err != nil {
				return err
			}
		default:
			if err := c.literal(); err != nil {
				return err
			}
		}

		// A value has ended. Close the containers it ends, until one
		// expects another value or the text is done.
		for {
			c.skipSpace()
			if len(c.stack) == 0 {
				if c.pos != len(c.src) {
					return c.syntax(c.pos, "text after the value")
				}
				return nil
			}
			if c.pos == len(c.src) {
				return c.syntax(c.pos, "unexpected end of text")
			}
			f := &c.stack[len(c.stack)-1]
			switch b := c.src[c.pos]; {
			case f.object && (b == ',' || b == '}'):
				c.pos++
				c.members[len(c.members)-1].end = len(c.out)
				if b == ',' {
					c.out = append(c.out, ',')
					if err := c.key(); err != nil {
						return err
					}
					continue value
				}
				c.closeObject(f)
			case !f.object && b == ',':
				c.pos++
				c.out = append(c.out, ',')
				continue value
			case !f.object && b == ']':
				c.pos++
				c.out = append(c.out, ']')
			case f.object:
				return c.syntax(c.pos, "expected ',' or '}' after an object member")
			default:
				return c.syntax(c.pos, "expected ',' or ']' after an array element")
			}
			c.stack = c.stack[:len(c.stack)-1]
		}
	}
}

// key reads an object member's key and the ':' after it.
func (c *canonicaliser) key() error {
	if c.skipSpace(); !c.peek('"') {
		return c.syntax(c.pos, "expected a string key")
	}
	m := member{start: len(c.out), key: len(c.keys)}
	if err := c.string(true); err != nil {
		return err
	}
	m.keyEnd = len(c.keys)
	if c.skipSpace(); !c.peek(':') {
		return c.syntax(c.pos, "expected ':' after an object key")
	}
	c.pos++
	c.out = append(c.out, ':')
	c.members = append(c.members, m)
	return nil
}

// closeObject puts the members of the object f into key order, keeps the
// last of duplicate keys, and writes the closing brace.
//
// An object already in order costs nothing more. Reordering copies the
// object's bytes, which an enclosing object that is reordered too copies
// again, so the cost is the text's size times the number of reordered
// objects nested on one path. Windmill fails context values nested deeper
// than 127 levels (JE-49), which bounds that factor for working flows.
func (c *canonicaliser) closeObject(f *frame) {
	ms := c.members[f.members:]
	if !c.inOrder(ms) {
		// A stable sort keeps duplicates in text order, so the last
		// member of each run of equal keys is the one jsonb keeps.
		slices.SortStableFunc(ms, c.compareKeys)
		buf := c.scratch[:0]
		for i, m := range ms {
			if i+1 < len(ms) && c.compareKeys(m, ms[i+1]) == 0 {
				continue
			}
			if len(buf) > 0 {
				buf = append(buf, ',')
			}
			buf = append(buf, c.out[m.start:m.end]...)
		}
		c.out = append(c.out[:f.start], buf...)
		c.scratch = buf
	}
	c.out = append(c.out, '}')
	c.members = c.members[:f.members]
	c.keys = c.keys[:f.keys]
}

// inOrder reports whether ms is already in strictly ascending key order:
// sorted, with no duplicate keys.
func (c *canonicaliser) inOrder(ms []member) bool {
	for i := 1; i < len(ms); i++ {
		if c.compareKeys(ms[i-1], ms[i]) >= 0 {
			return false
		}
	}
	return true
}

// compareKeys orders members as jsonb orders keys: by the decoded key's
// byte length, then bytewise.
func (c *canonicaliser) compareKeys(a, b member) int {
	ka, kb := c.keys[a.key:a.keyEnd], c.keys[b.key:b.keyEnd]
	if n := cmp.Compare(len(ka), len(kb)); n != 0 {
		return n
	}
	return bytes.Compare(ka, kb)
}

// string reads the string at c.pos and writes it in jsonb's escaping. For a
// key it also appends the decoded bytes to c.keys.
func (c *canonicaliser) string(key bool) error {
	open := c.pos
	c.pos++
	c.out = append(c.out, '"')
	for {
		// Copy the run of bytes that stays as it is.
		run := c.pos
		for c.pos < len(c.src) {
			b := c.src[c.pos]
			if b == '"' || b == '\\' || b < 0x20 {
				break
			}
			if b < utf8.RuneSelf {
				c.pos++
				continue
			}
			r, n := utf8.DecodeRune(c.src[c.pos:])
			if r == utf8.RuneError && n == 1 {
				return c.syntax(c.pos, "invalid UTF-8")
			}
			c.pos += n
		}
		c.out = append(c.out, c.src[run:c.pos]...)
		if key {
			c.keys = append(c.keys, c.src[run:c.pos]...)
		}
		if c.pos == len(c.src) {
			return c.syntax(open, "unterminated string")
		}
		switch c.src[c.pos] {
		case '"':
			c.pos++
			c.out = append(c.out, '"')
			return nil
		case '\\':
			r, err := c.escape()
			if err != nil {
				return err
			}
			c.out = appendStoredRune(c.out, r)
			if key {
				c.keys = utf8.AppendRune(c.keys, r)
			}
		default:
			return c.syntax(c.pos, "unescaped control character in a string")
		}
	}
}

// escape decodes the escape at c.pos and returns the character it stands
// for, joining a surrogate pair into one character.
func (c *canonicaliser) escape() (rune, error) {
	at := c.pos
	if c.pos+1 == len(c.src) {
		return 0, c.syntax(at, "unterminated escape")
	}
	b := c.src[c.pos+1]
	c.pos += 2
	switch b {
	case '"', '\\', '/':
		return rune(b), nil
	case 'b':
		return '\b', nil
	case 'f':
		return '\f', nil
	case 'n':
		return '\n', nil
	case 'r':
		return '\r', nil
	case 't':
		return '\t', nil
	case 'u':
	default:
		return 0, c.syntax(at, "invalid escape")
	}
	r, ok := c.hex4()
	if !ok {
		return 0, c.syntax(at, `\u not followed by four hexadecimal digits`)
	}
	switch {
	case r == 0:
		return 0, c.reject(ErrNUL, at)
	case 0xD800 <= r && r < 0xDC00:
		// A high surrogate must be followed at once by the escape of a
		// low one. Anything else, \u0000 included, is an unpaired
		// surrogate, which is what PostgreSQL reports first.
		if c.pos+1 < len(c.src) && c.src[c.pos] == '\\' && c.src[c.pos+1] == 'u' {
			second := c.pos
			c.pos += 2
			lo, ok := c.hex4()
			if !ok {
				return 0, c.syntax(second, `\u not followed by four hexadecimal digits`)
			}
			if 0xDC00 <= lo && lo < 0xE000 {
				return utf16.DecodeRune(r, lo), nil
			}
		}
		return 0, c.reject(ErrUnpairedSurrogate, at)
	case 0xDC00 <= r && r < 0xE000:
		return 0, c.reject(ErrUnpairedSurrogate, at)
	}
	return r, nil
}

// hex4 reads four hexadecimal digits at c.pos.
func (c *canonicaliser) hex4() (rune, bool) {
	if len(c.src)-c.pos < 4 {
		return 0, false
	}
	var r rune
	for _, b := range c.src[c.pos : c.pos+4] {
		switch {
		case '0' <= b && b <= '9':
			r = r<<4 | rune(b-'0')
		case 'a' <= b && b <= 'f':
			r = r<<4 | rune(b-'a'+10)
		case 'A' <= b && b <= 'F':
			r = r<<4 | rune(b-'A'+10)
		default:
			return 0, false
		}
	}
	c.pos += 4
	return r, true
}

// appendStoredRune appends r as jsonb writes it inside a string.
func appendStoredRune(out []byte, r rune) []byte {
	switch r {
	case '"':
		return append(out, '\\', '"')
	case '\\':
		return append(out, '\\', '\\')
	case '\b':
		return append(out, '\\', 'b')
	case '\f':
		return append(out, '\\', 'f')
	case '\n':
		return append(out, '\\', 'n')
	case '\r':
		return append(out, '\\', 'r')
	case '\t':
		return append(out, '\\', 't')
	}
	if r < 0x20 {
		const hex = "0123456789abcdef"
		return append(out, '\\', 'u', '0', '0', hex[r>>4], hex[r&0xF])
	}
	return utf8.AppendRune(out, r)
}

// number reads the number at c.pos and writes it as PostgreSQL numeric
// writes it.
func (c *canonicaliser) number() error {
	start := c.pos
	neg := c.src[c.pos] == '-'
	if neg {
		c.pos++
	}
	intStart := c.pos
	switch {
	case c.peek('0'):
		c.pos++
	case c.pos < len(c.src) && '1' <= c.src[c.pos] && c.src[c.pos] <= '9':
		c.digits()
	default:
		return c.syntax(start, "invalid number")
	}
	intDigits := c.src[intStart:c.pos]
	var frac []byte
	if c.peek('.') {
		c.pos++
		f := c.pos
		if c.digits() == 0 {
			return c.syntax(start, "invalid number")
		}
		frac = c.src[f:c.pos]
	}
	exp, hasExp, expTooLarge := 0, false, false
	if c.peek('e') || c.peek('E') {
		hasExp = true
		c.pos++
		expNeg := c.peek('-')
		if expNeg || c.peek('+') {
			c.pos++
		}
		e := c.pos
		if c.digits() == 0 {
			return c.syntax(start, "invalid number")
		}
		for _, d := range c.src[e:c.pos] {
			if exp = exp*10 + int(d-'0'); exp > maxExponent {
				expTooLarge = true
				break
			}
		}
		if expNeg {
			exp = -exp
		}
	}
	// PostgreSQL lexes a number and the letters, digits and underscores
	// right after it as one token, so "1x" is a syntax error before the
	// number is ever converted.
	if c.pos < len(c.src) && isWordByte(c.src[c.pos]) {
		return c.syntax(start, "invalid number")
	}
	if expTooLarge {
		return c.reject(ErrNumericOverflow, start)
	}
	return c.writeNumber(start, neg, intDigits, frac, exp, hasExp)
}

// writeNumber writes the number whose digits are intDigits, then frac after
// the decimal point, times ten to the power exp.
func (c *canonicaliser) writeNumber(start int, neg bool, intDigits, frac []byte, exp int, hasExp bool) error {
	nInt, nFrac := len(intDigits), len(frac)
	scale := max(nFrac-exp, 0)
	if scale > maxScale {
		return c.reject(ErrNumericOverflow, start)
	}
	// digit returns the digit at index i of intDigits followed by frac,
	// and '0' outside them.
	digit := func(i int) byte {
		switch {
		case i < 0 || i >= nInt+nFrac:
			return '0'
		case i < nInt:
			return intDigits[i]
		default:
			return frac[i-nInt]
		}
	}
	lead := -1 // the index of the first nonzero digit
	for i := range nInt + nFrac {
		if digit(i) != '0' {
			lead = i
			break
		}
	}
	if lead < 0 {
		// numeric has no negative zero.
		c.out = append(c.out, '0')
		if scale > 0 {
			c.out = append(c.out, '.')
			c.out = append(c.out, bytes.Repeat([]byte{'0'}, scale)...)
		}
		return nil
	}
	point := nInt + exp // the index the decimal point moves to
	if point-lead > maxIntegerDigits {
		return c.reject(ErrNumericOverflow, start)
	}
	if !hasExp {
		c.out = append(c.out, c.src[start:c.pos]...)
		return nil
	}
	if neg {
		c.out = append(c.out, '-')
	}
	if point <= lead {
		c.out = append(c.out, '0')
	}
	for i := lead; i < point; i++ {
		c.out = append(c.out, digit(i))
	}
	if scale > 0 {
		c.out = append(c.out, '.')
		for i := point; i < point+scale; i++ {
			c.out = append(c.out, digit(i))
		}
	}
	return nil
}

// digits steps over a run of decimal digits and returns its length.
func (c *canonicaliser) digits() int {
	start := c.pos
	for c.pos < len(c.src) && '0' <= c.src[c.pos] && c.src[c.pos] <= '9' {
		c.pos++
	}
	return c.pos - start
}

func (c *canonicaliser) literal() error {
	for _, lit := range [...]string{"true", "false", "null"} {
		end := c.pos + len(lit)
		if bytes.HasPrefix(c.src[c.pos:], []byte(lit)) && (end == len(c.src) || !isWordByte(c.src[end])) {
			c.out = append(c.out, lit...)
			c.pos = end
			return nil
		}
	}
	return c.syntax(c.pos, "invalid token")
}

// isWordByte reports whether b continues a token for PostgreSQL's JSON
// lexer: a letter, a digit, '_' or a byte of a multibyte character.
func isWordByte(b byte) bool {
	return 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z' || '0' <= b && b <= '9' || b == '_' || b >= utf8.RuneSelf
}

func (c *canonicaliser) skipSpace() {
	for c.pos < len(c.src) {
		switch c.src[c.pos] {
		case ' ', '\t', '\n', '\r':
			c.pos++
		default:
			return
		}
	}
}

func (c *canonicaliser) peek(b byte) bool { return c.pos < len(c.src) && c.src[c.pos] == b }

func (c *canonicaliser) reject(kind error, at int) error {
	return &StoreError{Kind: kind, Offset: at}
}

func (c *canonicaliser) syntax(at int, detail string) error {
	return &StoreError{Kind: ErrSyntax, Offset: at, detail: detail}
}
