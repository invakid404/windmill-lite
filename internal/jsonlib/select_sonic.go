//go:build jsonlib_sonic

package jsonlib

import (
	"encoding/json"

	"github.com/invakid404/windmill-lite/internal/jsonlib/sonicjson"
)

// Name is the library this build swaps in.
const Name = "sonic Std"

func Valid(b []byte) bool                                    { return sonicjson.Std.Valid(b) }
func Unmarshal(b []byte, v any) error                        { return sonicjson.Std.Unmarshal(b, v) }
func Marshal(v any) ([]byte, error)                          { return sonicjson.Std.Marshal(v) }
func RawObject(b []byte) (map[string]json.RawMessage, error) { return sonicjson.Std.RawObject(b) }
func RawArray(b []byte) ([]json.RawMessage, error)           { return sonicjson.Std.RawArray(b) }
func Members(obj []byte, fn func(key string, ks, ke, vs, ve int)) bool {
	return sonicjson.Members(obj, fn)
}
