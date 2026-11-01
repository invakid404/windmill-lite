//go:build !jsonlib_v2 && !jsonlib_j1 && !jsonlib_sonic && !jsonlib_sonicfast

package jsonlib

import (
	"encoding/json"

	"github.com/invakid404/windmill-lite/internal/jsonlib/std"
)

// Name is the library this build swaps in.
const Name = "encoding/json"

// V1Compatible reports whether this build keeps encoding/json's behaviour
// at every site it routes: the equivalence tests are strict for it.
const V1Compatible = true

func Valid(b []byte) bool                                    { return std.Valid(b) }
func Unmarshal(b []byte, v any) error                        { return std.Unmarshal(b, v) }
func Marshal(v any) ([]byte, error)                          { return std.Marshal(v) }
func RawObject(b []byte) (map[string]json.RawMessage, error) { return std.RawObject(b) }
func RawArray(b []byte) ([]json.RawMessage, error)           { return std.RawArray(b) }
func Members(obj []byte, fn func(key string, ks, ke, vs, ve int)) bool {
	return std.Members(obj, fn)
}
