// Package gje is the JSON-library study's github.com/go-json-experiment/json
// backend: the standalone module the standard library's v2 API came from,
// for embedders who build windmill-lite's wl package as a library. It is
// package v2's code with the module's packages: where a call site relies
// on what encoding/json v1 accepts, it sets the two v1 allowances
// explicitly (duplicate names, invalid UTF-8); every other option is v2's
// default, except Marshal, which sorts map keys as v1 does.
//
// Study branch only (study/json-libraries); never merged.
package gje

import (
	"bytes"
	v1 "encoding/json"
	"io"
	"unicode/utf8"

	json "github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
)

// compat accepts what encoding/json v1 accepts at windmill-lite's call
// sites: duplicate names and invalid UTF-8 in strings.
var compat = json.JoinOptions(jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true))

// Valid reports whether b is one JSON value.
func Valid(b []byte) bool { return jsontext.Value(b).IsValid(compat) }

// Compact appends b without insignificant white space to dst.
func Compact(dst, b []byte) ([]byte, error) {
	out := append(dst[:len(dst):len(dst)], b...)
	v := jsontext.Value(out[len(dst):])
	if err := v.Compact(compat); err != nil {
		return dst, err
	}
	return append(out[:len(dst)], v...), nil
}

// Unmarshal is json.Unmarshal with v2's defaults and the two v1 allowances.
func Unmarshal(b []byte, v any) error { return json.Unmarshal(b, v, compat) }

// Marshal is json.Marshal with map keys sorted (Deterministic), as v1
// sorts them; v2 does not escape <, > and & as v1 does.
func Marshal(v any) ([]byte, error) { return json.Marshal(v, json.Deterministic(true)) }

// RawObject decodes a JSON object into its members' texts, the last of
// duplicate keys kept.
func RawObject(b []byte) (map[string]v1.RawMessage, error) {
	var m map[string]jsontext.Value
	if err := json.Unmarshal(b, &m, compat); err != nil || m == nil {
		return nil, err
	}
	out := make(map[string]v1.RawMessage, len(m))
	for k, v := range m {
		out[k] = v1.RawMessage(v)
	}
	return out, nil
}

// RawArray decodes a JSON array into its elements' texts.
func RawArray(b []byte) ([]v1.RawMessage, error) {
	var a []jsontext.Value
	if err := json.Unmarshal(b, &a, compat); err != nil || a == nil {
		return nil, err
	}
	out := make([]v1.RawMessage, len(a))
	for i, v := range a {
		out[i] = v1.RawMessage(v)
	}
	return out, nil
}

// Members is std.Members with a jsontext.Decoder, which reads obj in place
// (a bytes.Buffer is not copied), so rawKey and value alias obj.
func Members(obj []byte, fn func(rawKey []byte, key string, value []byte)) (ok bool) {
	dec := jsontext.NewDecoder(bytes.NewBuffer(obj), compat)
	if t, err := dec.ReadToken(); err != nil || t.Kind() != '{' {
		return false
	}
	for dec.PeekKind() == '"' {
		k, err := dec.ReadValue()
		if err != nil {
			return false
		}
		rawKey := k
		key, ok := unquote(k)
		if !ok {
			return false
		}
		v, err := dec.ReadValue()
		if err != nil {
			return false
		}
		fn(rawKey, key, v)
	}
	if t, err := dec.ReadToken(); err != nil || t.Kind() != '}' {
		return false
	}
	if _, err := dec.ReadToken(); err != io.EOF {
		return false
	}
	return true
}

// unquote decodes a JSON string's text, quotes included, that the decoder
// has read. Invalid UTF-8 is replaced with U+FFFD, as v1 decodes it; that
// is the only error AppendUnquote can report for such a text.
func unquote(k []byte) (string, bool) {
	if len(k) >= 2 && bytes.IndexByte(k, '\\') < 0 && utf8.Valid(k) {
		return string(k[1 : len(k)-1]), true
	}
	s, _ := jsontext.AppendUnquote(nil, k)
	return string(s), true
}

// Canonical decodes b into Go values and encodes it again with object keys
// sorted (Deterministic) and no HTML escaping. v2 has no UseNumber: numbers
// become float64, so integers beyond 2^53 lose precision.
func Canonical(b []byte) ([]byte, error) {
	var v any
	if err := json.Unmarshal(b, &v, compat); err != nil {
		return nil, err
	}
	return json.Marshal(v, json.Deterministic(true))
}

// JCS is jsontext's own canonical form, RFC 8785: keys sorted by UTF-16
// code units, numbers as ECMAScript prints doubles. It is neither jsonb's
// key order nor its numbers.
func JCS(b []byte) ([]byte, error) {
	v := jsontext.Value(bytes.Clone(b))
	if err := v.Canonicalize(); err != nil {
		return nil, err
	}
	return v, nil
}
