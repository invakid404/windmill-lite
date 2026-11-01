//go:build jsonlib_v2

package jsonlib

import (
	"encoding/json"

	v2 "github.com/invakid404/windmill-lite/internal/jsonlib/v2"
)

// Name is the library this build swaps in.
const Name = "encoding/json/v2"

func Valid(b []byte) bool                                    { return v2.Valid(b) }
func Unmarshal(b []byte, v any) error                        { return v2.Unmarshal(b, v) }
func Marshal(v any) ([]byte, error)                          { return v2.Marshal(v) }
func RawObject(b []byte) (map[string]json.RawMessage, error) { return v2.RawObject(b) }
func RawArray(b []byte) ([]json.RawMessage, error)           { return v2.RawArray(b) }
func Members(obj []byte, fn func(key string, ks, ke, vs, ve int)) bool {
	return v2.Members(obj, fn)
}
