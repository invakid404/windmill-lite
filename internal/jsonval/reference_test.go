package jsonval

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// referenceStored is the independent implementation that ADR 0005 checks the
// canonicaliser against, used only in tests. It decodes with encoding/json,
// sorts, and re-encodes, and shares no code with canonical.go: tokens are
// found with regular expressions, numbers are rewritten with math/big, and
// strings are re-escaped from their decoded form.
//
// It returns the stored text, or the sentinel of the first token jsonb
// rejects (ErrNUL, ErrUnpairedSurrogate, ErrNumericOverflow). It returns
// ErrSyntax for any text that is not one JSON value in valid UTF-8, without
// ordering that against the other rejections.
func referenceStored(text []byte) ([]byte, error) {
	if !utf8.Valid(text) || !json.Valid(text) {
		return nil, ErrSyntax
	}
	if err := referenceRejection(text); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(text))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("reference decode: %w", err)
	}
	var b strings.Builder
	if err := referenceEncode(&b, v); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

var (
	// referenceToken matches the string and number tokens of valid JSON
	// text, in text order. Digits inside strings are part of the string
	// match, so a number match is always a number token.
	referenceToken = regexp.MustCompile(`"(?:[^"\\]|\\.)*"|-?(?:0|[1-9][0-9]*)(?:\.([0-9]+))?(?:[eE]([+-]?[0-9]+))?`)
	// referenceEscape matches each escape inside a string token.
	referenceEscape = regexp.MustCompile(`\\(?:u([0-9a-fA-F]{4})|.)`)
)

// referenceRejection returns the sentinel of the first string or number
// token in valid JSON text that jsonb input rejects.
func referenceRejection(text []byte) error {
	for _, m := range referenceToken.FindAllSubmatchIndex(text, -1) {
		tok := text[m[0]:m[1]]
		var err error
		if tok[0] == '"' {
			err = referenceStringRejection(tok)
		} else {
			err = referenceNumberRejection(tok)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// referenceStringRejection applies jsonb's rules for \u escapes: a high
// surrogate must be directly followed by a low surrogate escape, and
// nothing may decode to U+0000. The pairing rule is checked first, so a
// high surrogate followed by \u0000 is an unpaired surrogate.
func referenceStringRejection(tok []byte) error {
	pendingHigh, pendingEnd := false, 0
	for _, m := range referenceEscape.FindAllSubmatchIndex(tok, -1) {
		code := -1
		if m[2] >= 0 {
			n, _ := strconv.ParseUint(string(tok[m[2]:m[3]]), 16, 32)
			code = int(n)
		}
		isLow := code >= 0xDC00 && code <= 0xDFFF
		if pendingHigh {
			if m[0] != pendingEnd || !isLow {
				return ErrUnpairedSurrogate
			}
			pendingHigh = false
			continue
		}
		switch {
		case code >= 0xD800 && code <= 0xDBFF:
			pendingHigh, pendingEnd = true, m[1]
		case isLow:
			return ErrUnpairedSurrogate
		case code == 0:
			return ErrNUL
		}
	}
	if pendingHigh {
		return ErrUnpairedSurrogate
	}
	return nil
}

// referenceNumberRejection applies numeric's range: an exponent of at most
// 2^30-1 in magnitude, at most 16383 digits after the point, and at most
// 131072 digits before it, counted from the first nonzero digit.
func referenceNumberRejection(tok []byte) error {
	mantissa, exponent, _ := strings.Cut(strings.ToLower(string(tok)), "e")
	exp := int64(0)
	if exponent != "" {
		n, err := strconv.ParseInt(strings.TrimLeft(exponent, "+"), 10, 64)
		if err != nil || n > 1<<30-1 || n < -(1<<30-1) {
			return ErrNumericOverflow
		}
		exp = n
	}
	intPart, fracPart, _ := strings.Cut(strings.TrimPrefix(mantissa, "-"), ".")
	if int64(len(fracPart))-exp > 16383 {
		return ErrNumericOverflow
	}
	digits := intPart + fracPart
	if first := strings.IndexFunc(digits, func(r rune) bool { return r != '0' }); first >= 0 {
		if int64(len(intPart)-first)+exp > 131072 {
			return ErrNumericOverflow
		}
	}
	return nil
}

func referenceEncode(b *strings.Builder, v any) error {
	switch v := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		slices.SortFunc(keys, func(x, y string) int {
			if n := cmp.Compare(len(x), len(y)); n != 0 {
				return n
			}
			return strings.Compare(x, y)
		})
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			referenceString(b, k)
			b.WriteByte(':')
			if err := referenceEncode(b, v[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	case []any:
		b.WriteByte('[')
		for i, e := range v {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := referenceEncode(b, e); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case string:
		referenceString(b, v)
	case json.Number:
		s, err := referenceNumber(string(v))
		if err != nil {
			return err
		}
		b.WriteString(s)
	case bool:
		b.WriteString(strconv.FormatBool(v))
	case nil:
		b.WriteString("null")
	default:
		return fmt.Errorf("reference encode: unexpected %T", v)
	}
	return nil
}

// referenceNumber writes a JSON number as PostgreSQL numeric's output does:
// the exact value in plain decimal with max(0, fraction digits - exponent)
// digits after the point.
func referenceNumber(s string) (string, error) {
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return "", fmt.Errorf("reference number %q", s)
	}
	mantissa, exponent, _ := strings.Cut(strings.ToLower(s), "e")
	exp := 0
	if exponent != "" {
		n, err := strconv.Atoi(exponent)
		if err != nil {
			return "", err
		}
		exp = n
	}
	_, frac, _ := strings.Cut(mantissa, ".")
	return r.FloatString(max(len(frac)-exp, 0)), nil
}

// referenceString writes s with PostgreSQL's JSON string escaping.
func referenceString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\f':
			b.WriteString(`\f`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20:
			fmt.Fprintf(b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
}
