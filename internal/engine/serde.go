package engine

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// The texts Windmill's backend writes where it decodes a JSON text with its
// JSON library (serde_json): the error of a value that is not what the
// site expects (CF-2's iterator check, JE-V1's u16), and the numbers of a
// value it decodes and writes out again (JE-44, FX-42 rule 2).

// elements splits a JSON array's text into its elements' texts, each as
// written. ok is false for text that is not one JSON array.
func elements(text []byte) (items [][]byte, ok bool) {
	t := bytes.TrimSpace(text)
	if len(t) == 0 || t[0] != '[' || !json.Valid(t) {
		return nil, false
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(t, &raw); err != nil {
		return nil, false
	}
	items = make([][]byte, len(raw))
	for i, r := range raw {
		items[i] = r
	}
	return items, true
}

// serdeError is the error serde_json gives when it decodes text, one JSON
// value that is not of the expected type, into a type whose expectation
// reads expected ("a sequence", "u16"): what it met, in serde's words, and
// where its reader stood, which is after the value it had to read to name
// it (a scalar) or before it (an object or an array):
//
//	invalid type: map, expected a sequence at line 1 column 0
//	invalid type: floating point `1.5`, expected u16 at line 1 column 3
func serdeError(text []byte, expected string) string {
	if msg, ok := serdeSyntax(text); ok {
		return msg
	}
	if t := bytes.TrimLeft(text, " \t\r\n"); len(t) > 0 && (t[0] == '-' || t[0] >= '0' && t[0] <= '9') {
		if msg, bad := serdeNumber(text, len(text)-len(t), numberToken(t)); bad {
			return msg
		}
	}
	unexp, pos := serdeUnexpected(text)
	return "invalid type: " + unexp + ", expected " + expected + serdePosition(text, pos)
}

// serdeSyntax is serde_json's error for a text that does not start with a
// JSON value, which an evaluation returns when the user's code replaces
// JSON.stringify (JE-15, Q-JE-14): the end of the text, a character no
// value starts with, or a broken true, false or null, where the reader
// stops after the first character that differs. Reading: serde_json's
// other syntax errors, for a value that starts well and breaks later, are
// not reproduced.
func serdeSyntax(text []byte) (string, bool) {
	t := bytes.TrimLeft(text, " \t\r\n")
	p := len(text) - len(t)
	if len(t) == 0 {
		return "EOF while parsing a value" + serdePosition(text, p), true
	}
	ident := map[byte]string{'t': "true", 'f': "false", 'n': "null"}[t[0]]
	if ident == "" && strings.IndexByte(`{["-0123456789`, t[0]) < 0 {
		return "expected value" + serdePosition(text, p+1), true
	}
	for j := 1; j < len(ident); j++ {
		if j == len(t) {
			return "EOF while parsing a value" + serdePosition(text, p+j), true
		}
		if t[j] != ident[j] {
			return "expected ident" + serdePosition(text, p+j+1), true
		}
	}
	return "", false
}

// parseU16 decodes text as serde_json decodes a u16 (JE-V1): one JSON
// integer from 0 to 65535, with only white space after it, or the error's
// text. As serde_json does, it checks the value's type and range before
// what follows it.
func parseU16(text []byte) (int, string) {
	t := bytes.TrimLeft(text, " \t\r\n")
	start := len(text) - len(t)
	if len(t) > 0 && (t[0] == '-' || t[0] >= '0' && t[0] <= '9') {
		tok := numberToken(t)
		pos := start + len(tok)
		if msg, bad := serdeNumber(text, start, tok); bad {
			return 0, msg
		}
		if isInteger(tok) {
			if u, err := strconv.ParseUint(string(tok), 10, 64); err == nil {
				if u > math.MaxUint16 {
					return 0, "invalid value: integer `" + string(tok) + "`, expected u16" + serdePosition(text, pos)
				}
				if msg, trailing := serdeTrailing(text, pos); trailing {
					return 0, msg
				}
				return int(u), ""
			}
			if _, err := strconv.ParseInt(string(tok), 10, 64); err == nil {
				return 0, "invalid value: integer `" + string(tok) + "`, expected u16" + serdePosition(text, pos)
			}
		}
	}
	return 0, serdeError(text, "u16")
}

// serdeNumber is serde_json's error for a number token tok at byte start
// of text that is not a JSON number's start: a leading zero followed by a
// digit, where the reader stops on that digit.
func serdeNumber(text []byte, start int, tok []byte) (string, bool) {
	d := 0
	if len(tok) > 0 && tok[0] == '-' {
		d = 1
	}
	if len(tok) > d+1 && tok[d] == '0' && tok[d+1] >= '0' && tok[d+1] <= '9' {
		return "invalid number" + serdePosition(text, start+d+2), true
	}
	return "", false
}

// serdeTrailing is serde_json's error for text that holds more than white
// space after its value, which ends at byte end: the reader stops on the
// first character after it.
func serdeTrailing(text []byte, end int) (string, bool) {
	rest := text[end:]
	t := bytes.TrimLeft(rest, " \t\r\n")
	if len(t) == 0 {
		return "", false
	}
	return "trailing characters" + serdePosition(text, end+len(rest)-len(t)+1), true
}

// serdeSequenceError is serde_json's error for text that is not one JSON
// array, when decoded as a sequence (CF-2): an array followed by more than
// white space has trailing characters; any other value is serdeError's.
func serdeSequenceError(text []byte) string {
	t := bytes.TrimLeft(text, " \t\r\n")
	start := len(text) - len(t)
	if len(t) > 0 && t[0] == '[' {
		dec := json.NewDecoder(bytes.NewReader(t))
		var v json.RawMessage
		if err := dec.Decode(&v); err == nil {
			if msg, trailing := serdeTrailing(text, start+int(dec.InputOffset())); trailing {
				return msg
			}
		}
	}
	return serdeError(text, "a sequence")
}

// serdeUnexpected names the first JSON value of text as serde's
// Unexpected does, with serde_json's spelling of null and of a float
// (its JsonUnexpected: "null", and the float as ryu writes it), and
// returns the reader's position once it has named it.
func serdeUnexpected(text []byte) (string, int) {
	t := bytes.TrimLeft(text, " \t\r\n")
	p := len(text) - len(t)
	if len(t) == 0 {
		return "unit value", p
	}
	switch c := t[0]; {
	case c == '{':
		return "map", p
	case c == '[':
		return "sequence", p
	case c == '"':
		dec := json.NewDecoder(bytes.NewReader(t))
		var s string
		if err := dec.Decode(&s); err == nil {
			return "string " + rustDebug(s), p + int(dec.InputOffset())
		}
	case c == 't':
		return "boolean `true`", p + 4
	case c == 'f':
		return "boolean `false`", p + 5
	case c == 'n':
		return "null", p + 4
	case c == '-' || c >= '0' && c <= '9':
		// An integer of 64 bits, but negative zero, which serde_json reads
		// as a float (classifyNumber); a float written with ryu
		// (ryuFormat): 1e21, not the re-printed 1e+21 of rustFloat.
		tok := numberToken(t)
		if k, _, _, _ := classifyNumber(string(tok)); k != numberFloat {
			return "integer `" + string(tok) + "`", p + len(tok)
		}
		if f, err := strconv.ParseFloat(string(tok), 64); err == nil {
			return "floating point `" + ryuFormat(f) + "`", p + len(tok)
		}
	}
	return "unit value", p
}

// serdePosition is serde_json's " at line L column C" for the reader at
// byte pos of text: C counts the bytes read on the line.
func serdePosition(text []byte, pos int) string {
	line, col := 1, pos
	if nl := bytes.LastIndexByte(text[:pos], '\n'); nl >= 0 {
		line, col = 1+bytes.Count(text[:pos], []byte{'\n'}), pos-nl-1
	}
	return " at line " + strconv.Itoa(line) + " column " + strconv.Itoa(col)
}

// numberToken is the JSON number at the start of t.
func numberToken(t []byte) []byte {
	n := 0
	for n < len(t) && strings.IndexByte("+-0123456789.eE", t[n]) >= 0 {
		n++
	}
	return t[:n]
}

func isInteger(tok []byte) bool { return !bytes.ContainsAny(tok, ".eE") }

// reprintNumbers rewrites the numbers of a compact JSON text as the
// backend's JSON library writes them after decoding it (FX-42 rule 2): an
// integer that fits a signed or unsigned 64-bit integer stays as it is,
// and every other number becomes the nearest double, in rustFloat's
// spelling. Strings and the rest of the text are kept.
func reprintNumbers(text []byte) []byte {
	var b []byte
	for i := 0; i < len(text); {
		c := text[i]
		switch {
		case c == '"':
			j := i + 1
			for j < len(text) && text[j] != '"' {
				if text[j] == '\\' {
					j++
				}
				j++
			}
			b = append(b, text[i:min(j+1, len(text))]...)
			i = j + 1
		case c == '-' || c >= '0' && c <= '9':
			tok := numberToken(text[i:])
			b = append(b, reprintNumber(tok)...)
			i += len(tok)
		default:
			b = append(b, c)
			i++
		}
	}
	return b
}

func reprintNumber(tok []byte) []byte {
	if isInteger(tok) {
		if _, err := strconv.ParseInt(string(tok), 10, 64); err == nil {
			return tok
		}
		if _, err := strconv.ParseUint(string(tok), 10, 64); err == nil {
			return tok
		}
	}
	f, err := strconv.ParseFloat(string(tok), 64)
	if err != nil || math.IsInf(f, 0) {
		return tok
	}
	return []byte(rustFloat(f))
}

// rustFloat spells a double as the backend's JSON library does (FX-42 rule
// 2): the shortest digits that round-trip, as a decimal with ".0" for an
// integral value below 10^16 (1000.0), plain decimals down to 10^-5
// (0.0001), and an exponent otherwise, written as FX-42's examples write
// it (1e+21, 1.2345678901234568e+29, 1e-7).
func rustFloat(f float64) string {
	if f == 0 {
		if math.Signbit(f) {
			return "-0.0"
		}
		return "0.0"
	}
	s := strconv.FormatFloat(f, 'e', -1, 64)
	neg := s[0] == '-'
	if neg {
		s = s[1:]
	}
	mant, exp, _ := strings.Cut(s, "e")
	digits := strings.Replace(mant, ".", "", 1)
	e, _ := strconv.Atoi(exp)
	n, kk := len(digits), e+1 // 10^(kk-1) <= |f| < 10^kk
	var out string
	switch {
	case kk >= n && kk <= 16:
		out = digits + strings.Repeat("0", kk-n) + ".0"
	case kk > 0 && kk <= 16:
		out = digits[:kk] + "." + digits[kk:]
	case kk > -5 && kk <= 0:
		out = "0." + strings.Repeat("0", -kk) + digits
	default:
		out = digits[:1]
		if n > 1 {
			out += "." + digits[1:]
		}
		if kk-1 < 0 {
			out += "e-" + strconv.Itoa(1-kk)
		} else {
			out += "e+" + strconv.Itoa(kk-1)
		}
	}
	if neg {
		out = "-" + out
	}
	return out
}
