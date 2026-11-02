package jsonval

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"io"
	"unicode/utf8"
)

// The raw-text reads: an object's members and an array's elements as the
// texts they are written as, for the sites that keep a value as text
// (ADR 0005). They read with encoding/json/jsontext, which reads the text in
// place, where encoding/json copies every value it passes and, in its
// member walk, each member again. They accept what encoding/json accepts:
// duplicate keys and invalid UTF-8 in strings.
//
// When a read fails, the text is read again with encoding/json and its
// error is returned, so error texts stay encoding/json's. jsontext's own
// errors differ from them, and encoding/json/v2 words a type mismatch at
// random once per process ("cannot" or "unable to"). A failed read is rare
// (malformed text from a harness or a host), so the second read costs
// nothing in practice.

// rawOptions accept duplicate names and invalid UTF-8, as encoding/json
// does.
var rawOptions = jsonv2.JoinOptions(jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true))

// RawObject is json.Unmarshal of text into a map[string]json.RawMessage:
// each member's text by its decoded key, the last of duplicate keys kept,
// and a nil map for null. Its error is encoding/json's.
func RawObject(text []byte) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if jsonv2.Unmarshal(text, &m, rawOptions) == nil {
		return m, nil
	}
	m = nil
	err := json.Unmarshal(text, &m)
	return m, err
}

// RawArray is json.Unmarshal of text into a []json.RawMessage: each
// element's text, and a nil slice for null. Its error is encoding/json's.
func RawArray(text []byte) ([]json.RawMessage, error) {
	var a []json.RawMessage
	if jsonv2.Unmarshal(text, &a, rawOptions) == nil {
		return a, nil
	}
	a = nil
	err := json.Unmarshal(text, &a)
	return a, err
}

// Rest is what follows the object that Members walked.
type Rest int

const (
	// RestNone: only whitespace.
	RestNone Rest = iota
	// RestValue: a JSON value or the start of one.
	RestValue
	// RestInvalid: text that cannot start a JSON value.
	RestInvalid
)

// Members calls fn for each top-level member of the JSON object that text
// starts with, in order and duplicates included, with its key decoded as
// encoding/json decodes it (invalid UTF-8 and unpaired surrogates become
// U+FFFD) and the offsets of its key's text, quotes included, and of its
// value's text: text[ks:ke] and text[vs:ve]. ok is false when text does
// not start with one JSON object, and fn may have been called by then.
// rest says what follows the object, as encoding/json's Decoder.Token
// reads it: RestValue for a token, RestInvalid for an error other than the
// end of the text, a number beyond float64's range included. Members reads
// text in place and copies nothing but the keys that hold escapes.
func Members(text []byte, fn func(key string, ks, ke, vs, ve int)) (rest Rest, ok bool) {
	dec := jsontext.NewDecoder(bytes.NewBuffer(text), rawOptions)
	if t, err := dec.ReadToken(); err != nil || t.Kind() != '{' {
		return 0, false
	}
	for dec.PeekKind() == '"' {
		k, err := dec.ReadValue()
		if err != nil {
			return 0, false
		}
		ke := int(dec.InputOffset())
		key := unquote(k)
		v, err := dec.ReadValue()
		if err != nil {
			return 0, false
		}
		ve := int(dec.InputOffset())
		fn(key, ke-len(k), ke, ve-len(v), ve)
	}
	if t, err := dec.ReadToken(); err != nil || t.Kind() != '}' {
		return 0, false
	}
	switch t, err := dec.ReadToken(); {
	case err == nil && t.Kind() == '0':
		// Decoder.Token gives a number as a float64, and fails on one
		// beyond its range.
		if _, err := t.Float(); err != nil {
			return RestInvalid, true
		}
		return RestValue, true
	case err == nil:
		return RestValue, true
	case errors.Is(err, io.EOF):
		return RestNone, true
	}
	return RestInvalid, true
}

// unquote decodes a JSON string's text, quotes included, that the decoder
// has read. Its only possible errors are invalid UTF-8 and unpaired
// surrogates, which it replaces with U+FFFD, as encoding/json decodes them.
func unquote(k []byte) string {
	if len(k) >= 2 && bytes.IndexByte(k, '\\') < 0 && utf8.Valid(k) {
		return string(k[1 : len(k)-1])
	}
	s, _ := jsontext.AppendUnquote(nil, k)
	return string(s)
}
