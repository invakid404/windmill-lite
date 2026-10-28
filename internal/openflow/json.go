package openflow

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// The decoder works on a parsed tree rather than on encoding/json, because
// Windmill's decode rules need what encoding/json discards: object members
// in input order with duplicates kept (duplicate fields are errors),
// case-sensitive names, the literal text of numbers (integer fields reject
// `1.0`), and the exact input bytes of every value (static values are kept
// verbatim).

type nodeKind uint8

const (
	kindNull nodeKind = iota
	kindBool
	kindNumber
	kindString
	kindArray
	kindObject
)

// node is one parsed JSON value.
type node struct {
	kind nodeKind
	// raw is the exact input text of the value.
	raw     []byte
	boolean bool
	str     string
	elems   []*node
	members []member

	parent *node
	// key is the member name in the parent object; index is the position
	// in the parent array, or -1.
	key   string
	index int
}

type member struct {
	key string
	val *node
}

// maxDepth bounds nesting so that hostile input cannot exhaust the stack.
const maxDepth = 10000

type parser struct {
	data  []byte
	pos   int
	depth int
}

// parseJSON parses one JSON document (RFC 8259, no extensions).
func parseJSON(data []byte) (*node, error) {
	p := &parser{data: data}
	p.space()
	n, err := p.value()
	if err != nil {
		return nil, err
	}
	n.index = -1
	p.space()
	if p.pos < len(p.data) {
		return nil, p.fail("trailing characters")
	}
	return n, nil
}

func (p *parser) fail(format string, args ...any) error {
	line, col := 1, 1
	for _, c := range p.data[:min(p.pos, len(p.data))] {
		if c == '\n' {
			line, col = line+1, 1
		} else {
			col++
		}
	}
	return &Error{Kind: ErrSyntax, Detail: fmt.Sprintf(format, args...) + fmt.Sprintf(" at line %d column %d", line, col)}
}

func (p *parser) space() {
	for p.pos < len(p.data) {
		switch p.data[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

func (p *parser) value() (*node, error) {
	if p.pos >= len(p.data) {
		return nil, p.fail("EOF while parsing a value")
	}
	start := p.pos
	n := &node{index: -1}
	var err error
	switch c := p.data[p.pos]; {
	case c == '{':
		n.kind = kindObject
		err = p.object(n)
	case c == '[':
		n.kind = kindArray
		err = p.array(n)
	case c == '"':
		n.kind = kindString
		n.str, err = p.string()
	case c == 't':
		n.kind, n.boolean = kindBool, true
		err = p.literal("true")
	case c == 'f':
		n.kind = kindBool
		err = p.literal("false")
	case c == 'n':
		n.kind = kindNull
		err = p.literal("null")
	case c == '-' || '0' <= c && c <= '9':
		n.kind = kindNumber
		err = p.number()
	default:
		err = p.fail("expected value")
	}
	if err != nil {
		return nil, err
	}
	n.raw = p.data[start:p.pos]
	return n, nil
}

func (p *parser) enter() error {
	p.depth++
	if p.depth > maxDepth {
		return p.fail("recursion limit exceeded")
	}
	return nil
}

func (p *parser) object(n *node) error {
	if err := p.enter(); err != nil {
		return err
	}
	p.pos++
	p.space()
	if p.pos < len(p.data) && p.data[p.pos] == '}' {
		p.pos++
		p.depth--
		return nil
	}
	for {
		if p.pos >= len(p.data) || p.data[p.pos] != '"' {
			return p.fail("key must be a string")
		}
		key, err := p.string()
		if err != nil {
			return err
		}
		p.space()
		if p.pos >= len(p.data) || p.data[p.pos] != ':' {
			return p.fail("expected `:`")
		}
		p.pos++
		p.space()
		v, err := p.value()
		if err != nil {
			return err
		}
		v.parent, v.key = n, key
		n.members = append(n.members, member{key, v})
		p.space()
		if p.pos >= len(p.data) {
			return p.fail("EOF while parsing an object")
		}
		switch p.data[p.pos] {
		case ',':
			p.pos++
			p.space()
		case '}':
			p.pos++
			p.depth--
			return nil
		default:
			return p.fail("expected `,` or `}`")
		}
	}
}

func (p *parser) array(n *node) error {
	if err := p.enter(); err != nil {
		return err
	}
	p.pos++
	p.space()
	if p.pos < len(p.data) && p.data[p.pos] == ']' {
		p.pos++
		p.depth--
		return nil
	}
	for {
		v, err := p.value()
		if err != nil {
			return err
		}
		v.parent, v.index = n, len(n.elems)
		n.elems = append(n.elems, v)
		p.space()
		if p.pos >= len(p.data) {
			return p.fail("EOF while parsing a list")
		}
		switch p.data[p.pos] {
		case ',':
			p.pos++
			p.space()
		case ']':
			p.pos++
			p.depth--
			return nil
		default:
			return p.fail("expected `,` or `]`")
		}
	}
}

func (p *parser) literal(word string) error {
	if !bytes.HasPrefix(p.data[p.pos:], []byte(word)) {
		return p.fail("expected value")
	}
	p.pos += len(word)
	return nil
}

func (p *parser) number() error {
	digits := func() int {
		start := p.pos
		for p.pos < len(p.data) && '0' <= p.data[p.pos] && p.data[p.pos] <= '9' {
			p.pos++
		}
		return p.pos - start
	}
	if p.data[p.pos] == '-' {
		p.pos++
	}
	if p.pos < len(p.data) && p.data[p.pos] == '0' {
		p.pos++
		if p.pos < len(p.data) && '0' <= p.data[p.pos] && p.data[p.pos] <= '9' {
			return p.fail("invalid number")
		}
	} else if digits() == 0 {
		return p.fail("invalid number")
	}
	if p.pos < len(p.data) && p.data[p.pos] == '.' {
		p.pos++
		if digits() == 0 {
			return p.fail("invalid number")
		}
	}
	if p.pos < len(p.data) && (p.data[p.pos] == 'e' || p.data[p.pos] == 'E') {
		p.pos++
		if p.pos < len(p.data) && (p.data[p.pos] == '+' || p.data[p.pos] == '-') {
			p.pos++
		}
		if digits() == 0 {
			return p.fail("invalid number")
		}
	}
	return nil
}

func (p *parser) string() (string, error) {
	p.pos++
	start := p.pos
	var sb *strings.Builder
	for {
		if p.pos >= len(p.data) {
			return "", p.fail("EOF while parsing a string")
		}
		c := p.data[p.pos]
		switch {
		case c == '"':
			s := string(p.data[start:p.pos])
			if sb != nil {
				sb.WriteString(s)
				s = sb.String()
			}
			p.pos++
			return s, nil
		case c == '\\':
			if sb == nil {
				sb = &strings.Builder{}
			}
			sb.Write(p.data[start:p.pos])
			if err := p.escape(sb); err != nil {
				return "", err
			}
			start = p.pos
		case c < 0x20:
			return "", p.fail("control character (\\u0000-\\u001F) found while parsing a string")
		case c < utf8.RuneSelf:
			p.pos++
		default:
			r, size := utf8.DecodeRune(p.data[p.pos:])
			if r == utf8.RuneError && size <= 1 {
				return "", p.fail("invalid UTF-8")
			}
			p.pos += size
		}
	}
}

func (p *parser) escape(sb *strings.Builder) error {
	p.pos++
	if p.pos >= len(p.data) {
		return p.fail("EOF while parsing a string")
	}
	c := p.data[p.pos]
	p.pos++
	switch c {
	case '"', '\\', '/':
		sb.WriteByte(c)
	case 'b':
		sb.WriteByte('\b')
	case 'f':
		sb.WriteByte('\f')
	case 'n':
		sb.WriteByte('\n')
	case 'r':
		sb.WriteByte('\r')
	case 't':
		sb.WriteByte('\t')
	case 'u':
		r, err := p.hex4()
		if err != nil {
			return err
		}
		switch {
		case utf16.IsSurrogate(r) && r < 0xdc00:
			if !bytes.HasPrefix(p.data[p.pos:], []byte(`\u`)) {
				return p.fail("lone leading surrogate in hex escape")
			}
			p.pos += 2
			low, err := p.hex4()
			if err != nil {
				return err
			}
			if r = utf16.DecodeRune(r, low); r == utf8.RuneError {
				return p.fail("lone leading surrogate in hex escape")
			}
		case utf16.IsSurrogate(r):
			return p.fail("unexpected end of hex escape")
		}
		sb.WriteRune(r)
	default:
		p.pos--
		return p.fail("invalid escape")
	}
	return nil
}

func (p *parser) hex4() (rune, error) {
	if p.pos+4 > len(p.data) {
		return 0, p.fail("EOF while parsing a string")
	}
	v, err := strconv.ParseUint(string(p.data[p.pos:p.pos+4]), 16, 32)
	if err != nil {
		return 0, p.fail("invalid escape")
	}
	p.pos += 4
	return rune(v), nil
}

// pointer is n's JSON Pointer (RFC 6901) relative to root.
func pointer(root, n *node) string {
	var parts []string
	for ; n != nil && n != root && n.parent != nil; n = n.parent {
		if n.index >= 0 {
			parts = append(parts, strconv.Itoa(n.index))
		} else {
			parts = append(parts, strings.NewReplacer("~", "~0", "/", "~1").Replace(n.key))
		}
	}
	var sb strings.Builder
	for i := len(parts) - 1; i >= 0; i-- {
		sb.WriteByte('/')
		sb.WriteString(parts[i])
	}
	return sb.String()
}
