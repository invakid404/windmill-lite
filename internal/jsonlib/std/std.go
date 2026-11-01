// Package std is the JSON-library study's encoding/json backend: the v1 API
// as windmill-lite calls it today. Which engine runs it is a build setting:
// Go 1.27's default runs the v1 API on the v2 implementation, and
// GOEXPERIMENT=nojsonv2 restores the classic v1 implementation.
//
// Study branch only (study/json-libraries); never merged.
package std

import (
	"bytes"
	"encoding/json"
	"io"
)

// Valid reports whether b is one JSON value.
func Valid(b []byte) bool { return json.Valid(b) }

// Compact appends b without insignificant white space to dst.
func Compact(dst, b []byte) ([]byte, error) {
	buf := bytes.NewBuffer(dst)
	if err := json.Compact(buf, b); err != nil {
		return dst, err
	}
	return buf.Bytes(), nil
}

// Unmarshal is json.Unmarshal.
func Unmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// Marshal is json.Marshal.
func Marshal(v any) ([]byte, error) { return json.Marshal(v) }

// RawObject decodes a JSON object into its members' texts, the last of
// duplicate keys kept, as harness.Decode and engine.Entrypoint do.
func RawObject(b []byte) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	err := json.Unmarshal(b, &m)
	return m, err
}

// RawArray decodes a JSON array into its elements' texts, as
// engine.elements does.
func RawArray(b []byte) ([]json.RawMessage, error) {
	var a []json.RawMessage
	err := json.Unmarshal(b, &a)
	return a, err
}

// Members calls fn for each top-level member of the JSON object obj, in
// order and duplicates included, with the key decoded and where the key's
// text (quotes included) and the value's text lie in obj: obj[ks:ke] and
// obj[vs:ve]. ok is false when obj is not exactly one JSON object; fn may
// have been called by then. It is engine.members' walk, with a
// json.Decoder.
func Members(obj []byte, fn func(key string, ks, ke, vs, ve int)) (ok bool) {
	dec := json.NewDecoder(bytes.NewReader(obj))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return false
	}
	for dec.More() {
		start := dec.InputOffset()
		t, err := dec.Token()
		key, isKey := t.(string)
		if err != nil || !isKey {
			return false
		}
		keyEnd := dec.InputOffset()
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return false
		}
		raw := bytes.TrimSpace(bytes.TrimLeft(bytes.TrimSpace(obj[start:keyEnd]), ","))
		ke := int(keyEnd)
		ve := int(dec.InputOffset())
		fn(key, ke-len(raw), ke, ve-len(v), ve)
	}
	if t, err := dec.Token(); err != nil || t != json.Delim('}') {
		return false
	}
	if _, err := dec.Token(); err != io.EOF {
		return false
	}
	return true
}

// Canonical decodes b, numbers kept as written, and encodes it again with
// object keys sorted bytewise and no HTML escaping: the decode, sort and
// encode that G1's baseline sets against jsonval's canonicaliser. Its key
// order is not jsonb's.
func Canonical(b []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.Grow(len(b))
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}
