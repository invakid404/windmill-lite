//go:build jsonlib_j1

package jsonlib

import (
	"encoding/json"

	"github.com/invakid404/windmill-lite/internal/jsonlib/std"
	v2 "github.com/invakid404/windmill-lite/internal/jsonlib/v2"
)

// Name is the library this build swaps in: lever J1 as recommended,
// encoding/json/v2's jsontext where a site keeps a value as raw text (an
// object's members by key or in order, an array's elements), and
// encoding/json everywhere else: decoding into Go values (struct fields,
// strings) and marshalling keep v1's semantics, which v2's options do not
// reproduce in full (fix round 1, P1).
const Name = "J1: jsontext at the raw-text decode sites"

// V1Compatible reports whether this build keeps encoding/json's behaviour
// at every site it routes: the equivalence tests are strict for it.
const V1Compatible = true

func Valid(b []byte) bool             { return std.Valid(b) }
func Unmarshal(b []byte, v any) error { return std.Unmarshal(b, v) }
func Marshal(v any) ([]byte, error)   { return std.Marshal(v) }

// RawObject and RawArray decode with jsontext and, when that fails, again
// with encoding/json, whose result they return. encoding/json/v2 words a
// type mismatch at random, once per process ("cannot" or "unable to"), and
// its other errors differ from v1's; a failed decode is rare (malformed
// input from the harness or the host), so the second decode costs nothing
// in practice and keeps today's error, text included.
func RawObject(b []byte) (map[string]json.RawMessage, error) {
	if m, err := v2.RawObject(b); err == nil {
		return m, nil
	}
	return std.RawObject(b)
}

func RawArray(b []byte) ([]json.RawMessage, error) {
	if a, err := v2.RawArray(b); err == nil {
		return a, nil
	}
	return std.RawArray(b)
}

func Members(obj []byte, fn func(key string, ks, ke, vs, ve int)) bool {
	return v2.Members(obj, fn)
}
