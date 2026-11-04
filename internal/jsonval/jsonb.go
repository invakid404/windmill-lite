// This file writes the binary form in which PostgreSQL 16 stores a jsonb
// value, following convertToJsonb (src/backend/utils/adt/jsonb_util.c) and
// numeric's set_var_from_str and make_result
// (src/backend/utils/adt/numeric.c), which are under the PostgreSQL
// License:
//
// Portions Copyright (c) 1996-2026, PostgreSQL Global Development Group
// Portions Copyright (c) 1994, The Regents of the University of California
//
// The full notice is in LICENSE.PostgreSQL beside this file.

package jsonval

import (
	"encoding/binary"
	"errors"
	"slices"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// jsonb's container and entry flags (src/include/utils/jsonb.h).
const (
	jbFScalar        = 0x10000000
	jbFObject        = 0x20000000
	jbFArray         = 0x40000000
	jentryOffLenMask = 0x0FFFFFFF
	jentryTypeMask   = 0x70000000
	jentryHasOff     = 0x80000000
	jentryNumeric    = 0x10000000
	jentryFalse      = 0x20000000
	jentryTrue       = 0x30000000
	jentryNull       = 0x40000000
	jentryContainer  = 0x50000000
	jbOffsetStride   = 32
)

// errJSONB marks text that jsonbDatum cannot read as one JSON value.
var errJSONB = errors.New("jsonval: not one JSON value")

// jbValue is a parsed JSON value: kind is one of n (null), t, f, s
// (string, its unescaped bytes in str), d (number, its text in str), a
// (array) or o (object, keys and values in kids, key then value).
type jbValue struct {
	kind byte
	str  []byte
	kids []jbValue
}

// jsonbDatum is the jsonb datum PostgreSQL 16 builds for text, one JSON
// value: a 4-byte varlena header, then the root container (a scalar as a
// one-element raw-scalar array), little-endian as on x86-64. Its length is
// pg_column_size of the value uncompressed.
func jsonbDatum(text []byte) ([]byte, error) {
	p := jbParser{s: text}
	p.space()
	v, err := p.value()
	if err != nil {
		return nil, err
	}
	if p.space(); p.i != len(p.s) {
		return nil, errJSONB
	}
	e := jbEncoder{b: make([]byte, 4, len(text)+64)}
	if v.kind == 'a' || v.kind == 'o' {
		e.value(&v)
	} else {
		e.array(&jbValue{kind: 'a', kids: []jbValue{v}}, true)
	}
	binary.LittleEndian.PutUint32(e.b, uint32(len(e.b))<<2)
	return e.b, nil
}

type jbEncoder struct{ b []byte }

// pad aligns the buffer to 4 bytes, counted from the datum's start, and
// returns the padding's length (padBufferToInt).
func (e *jbEncoder) pad() int {
	n := (4 - len(e.b)%4) % 4
	e.b = append(e.b, make([]byte, n)...)
	return n
}

func (e *jbEncoder) u32(v uint32) { e.b = binary.LittleEndian.AppendUint32(e.b, v) }

// value appends v and returns its entry (convertJsonbValue).
func (e *jbEncoder) value(v *jbValue) uint32 {
	switch v.kind {
	case 'n':
		return jentryNull
	case 't':
		return jentryTrue
	case 'f':
		return jentryFalse
	case 's':
		e.b = append(e.b, v.str...)
		return uint32(len(v.str))
	case 'd':
		n := numericDatum(string(v.str))
		p := e.pad()
		e.b = append(e.b, n...)
		return jentryNumeric | uint32(p+len(n))
	case 'a':
		return e.array(v, false)
	}
	return e.object(v)
}

func (e *jbEncoder) array(v *jbValue, rawScalar bool) uint32 {
	base := len(e.b)
	e.pad()
	head := uint32(len(v.kids)) | jbFArray
	if rawScalar {
		head |= jbFScalar
	}
	e.u32(head)
	at := len(e.b)
	e.b = append(e.b, make([]byte, 4*len(v.kids))...)
	total := 0
	for i := range v.kids {
		meta := e.value(&v.kids[i])
		total += int(meta & jentryOffLenMask)
		if i%jbOffsetStride == 0 {
			meta = meta&jentryTypeMask | uint32(total) | jentryHasOff
		}
		binary.LittleEndian.PutUint32(e.b[at+4*i:], meta)
	}
	return jentryContainer | uint32(len(e.b)-base)
}

func (e *jbEncoder) object(v *jbValue) uint32 {
	base := len(e.b)
	e.pad()
	n := len(v.kids) / 2
	e.u32(uint32(n) | jbFObject)
	at := len(e.b)
	e.b = append(e.b, make([]byte, 8*n)...)
	total := 0
	for i := 0; i < 2*n; i++ {
		// The keys first, in order, then the values.
		k := 2*(i-n) + 1
		if i < n {
			k = 2 * i
		}
		meta := e.value(&v.kids[k])
		total += int(meta & jentryOffLenMask)
		if i%jbOffsetStride == 0 {
			meta = meta&jentryTypeMask | uint32(total) | jentryHasOff
		}
		binary.LittleEndian.PutUint32(e.b[at+4*i:], meta)
	}
	return jentryContainer | uint32(len(e.b)-base)
}

// numericDatum is the numeric datum numeric_in builds for a JSON number:
// a 4-byte varlena header, the short or the long header, and its
// base-10000 digits, leading and trailing zero digits stripped.
func numericDatum(t string) []byte {
	neg, i := false, 0
	if i < len(t) && (t[i] == '+' || t[i] == '-') {
		neg = t[i] == '-'
		i++
	}
	dec := []int{0, 0, 0, 0}
	dweight, dscale, havePoint := -1, 0, false
	for ; i < len(t); i++ {
		switch c := t[i]; {
		case c >= '0' && c <= '9':
			dec = append(dec, int(c-'0'))
			if havePoint {
				dscale++
			} else {
				dweight++
			}
		case c == '.':
			havePoint = true
			continue
		default:
			goto digitsDone
		}
	}
digitsDone:
	ddigits := len(dec) - 4
	dec = append(dec, 0, 0, 0)
	if i < len(t) && (t[i] == 'e' || t[i] == 'E') {
		exp, err := strconv.Atoi(t[i+1:])
		if err == nil {
			dweight += exp
			if dscale -= exp; dscale < 0 {
				dscale = 0
			}
		}
	}
	var weight int
	if dweight >= 0 {
		weight = (dweight+1+3)/4 - 1
	} else {
		weight = -((-dweight-1)/4 + 1)
	}
	offset := (weight+1)*4 - (dweight + 1)
	ndigits := (ddigits + offset + 3) / 4
	digits := make([]int, ndigits)
	for k := range digits {
		at := 4 - offset + 4*k
		digits[k] = ((dec[at]*10+dec[at+1])*10+dec[at+2])*10 + dec[at+3]
	}
	for len(digits) > 0 && digits[0] == 0 {
		digits, weight = digits[1:], weight-1
	}
	for len(digits) > 0 && digits[len(digits)-1] == 0 {
		digits = digits[:len(digits)-1]
	}
	if len(digits) == 0 {
		neg, weight = false, 0
	}
	var b []byte
	if dscale <= 63 && weight <= 63 && weight >= -64 {
		b = make([]byte, 6, 6+2*len(digits))
		h := 0x8000 | dscale<<7 | weight&0x3F
		if neg {
			h |= 0x2000
		}
		if weight < 0 {
			h |= 0x0040
		}
		binary.LittleEndian.PutUint16(b[4:], uint16(h))
	} else {
		b = make([]byte, 8, 8+2*len(digits))
		sd := dscale & 0x3FFF
		if neg {
			sd |= 0x4000
		}
		binary.LittleEndian.PutUint16(b[4:], uint16(sd))
		binary.LittleEndian.PutUint16(b[6:], uint16(int16(weight)))
	}
	for _, d := range digits {
		b = binary.LittleEndian.AppendUint16(b, uint16(d))
	}
	binary.LittleEndian.PutUint32(b, uint32(len(b))<<2)
	return b
}

// jbParser reads JSON text into jbValues, unescaping strings as jsonb
// stores them and keeping each object's keys as jsonb orders them: by
// length, then bytes, the last of equal keys kept.
type jbParser struct {
	s []byte
	i int
}

func (p *jbParser) space() {
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == '\t' || p.s[p.i] == '\n' || p.s[p.i] == '\r') {
		p.i++
	}
}

func (p *jbParser) value() (jbValue, error) {
	if p.i >= len(p.s) {
		return jbValue{}, errJSONB
	}
	switch c := p.s[p.i]; {
	case c == '{':
		return p.object()
	case c == '[':
		return p.array()
	case c == '"':
		s, err := p.str()
		return jbValue{kind: 's', str: s}, err
	case c == 't' || c == 'f' || c == 'n':
		for _, w := range []string{"true", "false", "null"} {
			if len(p.s)-p.i >= len(w) && string(p.s[p.i:p.i+len(w)]) == w {
				p.i += len(w)
				return jbValue{kind: w[0]}, nil
			}
		}
		return jbValue{}, errJSONB
	default:
		j := p.i
		for p.i < len(p.s) && (p.s[p.i] == '-' || p.s[p.i] == '+' || p.s[p.i] == '.' || p.s[p.i] == 'e' || p.s[p.i] == 'E' || p.s[p.i] >= '0' && p.s[p.i] <= '9') {
			p.i++
		}
		if j == p.i {
			return jbValue{}, errJSONB
		}
		return jbValue{kind: 'd', str: p.s[j:p.i]}, nil
	}
}

func (p *jbParser) array() (jbValue, error) {
	v := jbValue{kind: 'a'}
	p.i++
	p.space()
	if p.i < len(p.s) && p.s[p.i] == ']' {
		p.i++
		return v, nil
	}
	for {
		p.space()
		k, err := p.value()
		if err != nil {
			return v, err
		}
		v.kids = append(v.kids, k)
		p.space()
		if p.i >= len(p.s) {
			return v, errJSONB
		}
		c := p.s[p.i]
		p.i++
		if c == ']' {
			return v, nil
		}
		if c != ',' {
			return v, errJSONB
		}
	}
}

func (p *jbParser) object() (jbValue, error) {
	type pair struct {
		key   []byte
		val   jbValue
		order int
	}
	var pairs []pair
	p.i++
	p.space()
	if p.i < len(p.s) && p.s[p.i] == '}' {
		p.i++
		return jbValue{kind: 'o'}, nil
	}
	for {
		p.space()
		if p.i >= len(p.s) || p.s[p.i] != '"' {
			return jbValue{}, errJSONB
		}
		k, err := p.str()
		if err != nil {
			return jbValue{}, err
		}
		p.space()
		if p.i >= len(p.s) || p.s[p.i] != ':' {
			return jbValue{}, errJSONB
		}
		p.i++
		p.space()
		v, err := p.value()
		if err != nil {
			return jbValue{}, err
		}
		pairs = append(pairs, pair{k, v, len(pairs)})
		p.space()
		if p.i >= len(p.s) {
			return jbValue{}, errJSONB
		}
		c := p.s[p.i]
		p.i++
		if c == '}' {
			break
		}
		if c != ',' {
			return jbValue{}, errJSONB
		}
	}
	// uniqueifyJsonbObject: keys by length, then bytes; of equal keys the
	// last given comes first and is the one kept.
	slices.SortFunc(pairs, func(a, b pair) int {
		if len(a.key) != len(b.key) {
			return len(a.key) - len(b.key)
		}
		if c := string(a.key); c != string(b.key) {
			if c < string(b.key) {
				return -1
			}
			return 1
		}
		return b.order - a.order
	})
	v := jbValue{kind: 'o'}
	for i, pr := range pairs {
		if i > 0 && string(pr.key) == string(pairs[i-1].key) {
			continue
		}
		v.kids = append(v.kids, jbValue{kind: 's', str: pr.key}, pr.val)
	}
	return v, nil
}

// str reads a JSON string at p.i and returns its UTF-8 bytes.
func (p *jbParser) str() ([]byte, error) {
	p.i++
	var out []byte
	for p.i < len(p.s) {
		c := p.s[p.i]
		switch {
		case c == '"':
			p.i++
			return out, nil
		case c != '\\':
			out = append(out, c)
			p.i++
			continue
		}
		if p.i+1 >= len(p.s) {
			return nil, errJSONB
		}
		e := p.s[p.i+1]
		p.i += 2
		switch e {
		case '"', '\\', '/':
			out = append(out, e)
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'u':
			r, ok := p.hex4()
			if !ok {
				return nil, errJSONB
			}
			if utf16.IsSurrogate(r) && p.i+1 < len(p.s) && p.s[p.i] == '\\' && p.s[p.i+1] == 'u' {
				save := p.i
				p.i += 2
				if r2, ok := p.hex4(); ok {
					if d := utf16.DecodeRune(r, r2); d != utf8.RuneError {
						r = d
					} else {
						p.i = save
					}
				} else {
					p.i = save
				}
			}
			out = utf8.AppendRune(out, r)
		default:
			return nil, errJSONB
		}
	}
	return nil, errJSONB
}

func (p *jbParser) hex4() (rune, bool) {
	if p.i+4 > len(p.s) {
		return 0, false
	}
	n, err := strconv.ParseUint(string(p.s[p.i:p.i+4]), 16, 16)
	if err != nil {
		return 0, false
	}
	p.i += 4
	return rune(n), true
}
