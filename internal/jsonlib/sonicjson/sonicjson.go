// Package sonicjson is the JSON-library study's github.com/bytedance/sonic
// backend, in sonic's two named configurations:
//
//   - Std, sonic.ConfigStd: encoding/json's behaviour (HTML escaping, sorted
//     map keys, compacted and validated Marshaler output, validated
//     strings); raw members are copies.
//   - Fastest, sonic.ConfigFastest: Marshaler output (json.RawMessage
//     included) is neither validated nor compacted, values skipped into a
//     raw member are not validated, and raw members alias the input
//     (sonic.NoCopyRawMessage).
//
// Members walks an object with sonic/ast's lazy parser, which validates the
// text once and skips each value without decoding it.
//
// Study branch only (study/json-libraries); never merged.
package sonicjson

import (
	"bytes"
	v1 "encoding/json"
	"errors"
	"unsafe"

	"github.com/bytedance/sonic"
	"github.com/bytedance/sonic/ast"
)

// Lib is sonic in one configuration.
type Lib struct {
	Name    string
	api     sonic.API
	compact sonic.API
	canonIn sonic.API
	canonEn sonic.API
	noCopy  bool
}

var (
	// Std is sonic.ConfigStd.
	Std = Lib{
		Name:    "sonicstd",
		api:     sonic.ConfigStd,
		compact: sonic.Config{CompactMarshaler: true}.Froze(),
		canonIn: sonic.Config{UseNumber: true, ValidateString: true}.Froze(),
		canonEn: sonic.Config{SortMapKeys: true}.Froze(),
	}
	// Fastest is sonic.ConfigFastest.
	Fastest = Lib{
		Name:    "sonicfast",
		api:     sonic.ConfigFastest,
		compact: sonic.Config{CompactMarshaler: true}.Froze(),
		canonIn: sonic.Config{UseNumber: true}.Froze(),
		canonEn: sonic.Config{SortMapKeys: true}.Froze(),
		noCopy:  true,
	}
)

// Valid reports whether b is one JSON value.
func (l Lib) Valid(b []byte) bool { return l.api.Valid(b) }

// Compact appends b without insignificant white space to dst: sonic has no
// Compact, so it marshals b as a json.RawMessage with CompactMarshaler,
// which validates and compacts it.
func (l Lib) Compact(dst, b []byte) ([]byte, error) {
	out, err := l.compact.Marshal(v1.RawMessage(b))
	if err != nil {
		return dst, err
	}
	return append(dst, out...), nil
}

// Unmarshal is the configuration's Unmarshal.
func (l Lib) Unmarshal(b []byte, v any) error { return l.api.Unmarshal(b, v) }

// Marshal is the configuration's Marshal.
func (l Lib) Marshal(v any) ([]byte, error) { return l.api.Marshal(v) }

// RawObject decodes a JSON object into its members' texts, the last of
// duplicate keys kept. Fastest's texts alias b.
func (l Lib) RawObject(b []byte) (map[string]v1.RawMessage, error) {
	if !l.noCopy {
		var m map[string]v1.RawMessage
		err := l.api.Unmarshal(b, &m)
		return m, err
	}
	var m map[string]sonic.NoCopyRawMessage
	if err := l.api.Unmarshal(b, &m); err != nil || m == nil {
		return nil, err
	}
	out := make(map[string]v1.RawMessage, len(m))
	for k, v := range m {
		out[k] = v1.RawMessage(v)
	}
	return out, nil
}

// RawArray decodes a JSON array into its elements' texts.
func (l Lib) RawArray(b []byte) ([]v1.RawMessage, error) {
	if !l.noCopy {
		var a []v1.RawMessage
		err := l.api.Unmarshal(b, &a)
		return a, err
	}
	var a []sonic.NoCopyRawMessage
	if err := l.api.Unmarshal(b, &a); err != nil || a == nil {
		return nil, err
	}
	out := make([]v1.RawMessage, len(a))
	for i, v := range a {
		out[i] = v1.RawMessage(v)
	}
	return out, nil
}

// Canonical decodes b, numbers kept as written (UseNumber), and encodes it
// again with map keys sorted and no HTML escaping.
func (l Lib) Canonical(b []byte) ([]byte, error) {
	var v any
	if err := l.canonIn.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	return l.canonEn.Marshal(v)
}

// Members walks obj with sonic/ast: ast.NewRaw validates the whole text,
// and the object's iterator skips each value, whose raw text is a slice of
// obj. The key's text as written is what lies between the previous value
// (or the opening brace) and the value, less the comma, the colon and white
// space. Both configurations walk alike.
func (l Lib) Members(obj []byte, fn func(key string, ks, ke, vs, ve int)) (ok bool) {
	return Members(obj, fn)
}

var errNotSlice = errors.New("sonicjson: a raw value is not a slice of the input")

// Members is Lib.Members.
func Members(obj []byte, fn func(key string, ks, ke, vs, ve int)) (ok bool) {
	if len(obj) == 0 {
		return false
	}
	base := unsafe.Pointer(unsafe.SliceData(obj))
	src := unsafe.String((*byte)(base), len(obj))
	root := ast.NewRaw(src)
	if root.Check() != nil || root.TypeSafe() != ast.V_OBJECT {
		return false
	}
	whole, err := root.Raw()
	if err != nil {
		return false
	}
	start, err := offset(base, len(obj), whole)
	if err != nil {
		return false
	}
	// Only white space may follow the object.
	if len(bytes.TrimLeft(obj[start+len(whole):], " \t\r\n")) != 0 {
		return false
	}
	it, err := root.Properties()
	if err != nil {
		return false
	}
	prev := start + 1
	var p ast.Pair
	for it.Next(&p) {
		raw, err := p.Value.Raw()
		if err != nil {
			return false
		}
		vs, err := offset(base, len(obj), raw)
		if err != nil {
			return false
		}
		// Between the previous value and this one: white space, a comma
		// (but before the first), the key, white space, a colon, white
		// space.
		ks, ke := prev, vs
		for ks < ke && (isSpace(obj[ks]) || obj[ks] == ',') {
			ks++
		}
		for ke > ks && (isSpace(obj[ke-1]) || obj[ke-1] == ':') {
			ke--
		}
		fn(p.Key, ks, ke, vs, vs+len(raw))
		prev = vs + len(raw)
	}
	return true
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' }

// offset is where s starts in the n bytes at base, or an error when s is
// not a slice of them.
func offset(base unsafe.Pointer, n int, s string) (int, error) {
	if len(s) == 0 {
		return 0, errNotSlice
	}
	d := uintptr(unsafe.Pointer(unsafe.StringData(s))) - uintptr(base)
	if d > uintptr(n) || int(d)+len(s) > n {
		return 0, errNotSlice
	}
	return int(d), nil
}
