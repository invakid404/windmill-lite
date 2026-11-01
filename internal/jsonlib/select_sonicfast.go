//go:build jsonlib_sonicfast

package jsonlib

import (
	"encoding/json"

	"github.com/invakid404/windmill-lite/internal/jsonlib/sonicjson"
)

// Name is the library this build swaps in.
const Name = "sonic Fastest"

// V1Compatible reports whether this build keeps encoding/json\'s behaviour
// at every site it routes: the equivalence tests are strict for it.
const V1Compatible = false

func Valid(b []byte) bool                                    { return sonicjson.Fastest.Valid(b) }
func Unmarshal(b []byte, v any) error                        { return sonicjson.Fastest.Unmarshal(b, v) }
func Marshal(v any) ([]byte, error)                          { return sonicjson.Fastest.Marshal(v) }
func RawObject(b []byte) (map[string]json.RawMessage, error) { return sonicjson.Fastest.RawObject(b) }
func RawArray(b []byte) ([]json.RawMessage, error)           { return sonicjson.Fastest.RawArray(b) }
func Members(obj []byte, fn func(key string, ks, ke, vs, ve int)) bool {
	return sonicjson.Members(obj, fn)
}
