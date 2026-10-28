package yamlx

import (
	"math"
	"strconv"
)

// appendJSON renders n as JSON.stringify renders the CLI's value.
func appendJSON(b []byte, n *Node) []byte {
	if n == nil {
		return append(b, "null"...)
	}
	switch n.Kind {
	case Null:
		return append(b, "null"...)
	case Bool:
		return append(b, n.Text...)
	case Number:
		if math.IsInf(n.num, 0) || math.IsNaN(n.num) {
			return append(b, "null"...)
		}
		return append(b, jsNumberString(n.num)...)
	case String:
		return appendJSONString(b, n.Text)
	case Sequence:
		b = append(b, '[')
		for i, it := range n.Items {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendJSON(b, it)
		}
		return append(b, ']')
	case Mapping:
		b = append(b, '{')
		for i, p := range n.Pairs {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendJSONString(b, p.Key)
			b = append(b, ':')
			b = appendJSON(b, p.Value)
		}
		return append(b, '}')
	}
	panic("yamlx: unknown node kind " + n.Kind.String())
}

// jsNumberString formats f as JavaScript's Number.prototype.toString does:
// the shortest digits that round-trip, in fixed notation from 1e-7 up to
// 1e21 and in exponent notation outside it.
func jsNumberString(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	case f == 0:
		return "0" // including -0
	}
	if abs := math.Abs(f); abs < 1e-6 || abs >= 1e21 {
		s := strconv.FormatFloat(f, 'e', -1, 64)
		// Go writes at least two exponent digits (1e-07); JavaScript
		// writes as many as needed (1e-7).
		if n := len(s); n >= 4 && s[n-4] == 'e' && s[n-3] == '-' && s[n-2] == '0' {
			s = s[:n-2] + s[n-1:]
		}
		return s
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// appendJSONString quotes s as JSON.stringify does: it escapes the quote,
// the backslash and control characters below U+0020, and nothing else.
func appendJSONString(b []byte, s string) []byte {
	const hex = "0123456789abcdef"
	b = append(b, '"')
	for i := 0; i < len(s); i++ {
		// Bytes of multi-byte characters are all at least 0x80, so they
		// are copied as they are.
		switch c := s[i]; c {
		case '"':
			b = append(b, '\\', '"')
		case '\\':
			b = append(b, '\\', '\\')
		case '\b':
			b = append(b, '\\', 'b')
		case '\f':
			b = append(b, '\\', 'f')
		case '\n':
			b = append(b, '\\', 'n')
		case '\r':
			b = append(b, '\\', 'r')
		case '\t':
			b = append(b, '\\', 't')
		default:
			if c < 0x20 {
				b = append(b, '\\', 'u', '0', '0', hex[c>>4], hex[c&0xF])
			} else {
				b = append(b, c)
			}
		}
	}
	return append(b, '"')
}
