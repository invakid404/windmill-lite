package syncfolder

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/invakid404/windmill-lite/internal/yamlx"
)

// fields reads typed values from a parsed YAML mapping. A null value counts
// as absent, as JavaScript's `??` reads it. A value of another type fails
// with kind, naming the file, the key and its position.
type fields struct {
	file string
	kind ErrorKind
	m    *yamlx.Node
}

func (f fields) wrong(n *yamlx.Node, key, want string) *Error {
	return &Error{Kind: f.kind, File: f.file, Line: n.Line, Column: n.Column,
		Msg: fmt.Sprintf("%s must be %s, not %s", key, want, describe(n))}
}

func describe(n *yamlx.Node) string {
	switch n.Kind {
	case yamlx.Null:
		return "null"
	case yamlx.Bool:
		return "the boolean " + n.Text
	case yamlx.Number:
		return "the number " + n.Text
	case yamlx.String:
		return fmt.Sprintf("the string %q", truncate(n.Text))
	case yamlx.Sequence:
		return "a list"
	}
	return "a mapping"
}

func truncate(s string) string {
	if len(s) > 40 {
		return s[:37] + "..."
	}
	return s
}

func (f fields) get(key string) *yamlx.Node {
	n := f.m.Get(key)
	if n == nil || n.Kind == yamlx.Null {
		return nil
	}
	return n
}

func (f fields) has(key string) bool { return f.get(key) != nil }

func (f fields) str(key string) (*string, error) {
	n := f.get(key)
	if n == nil {
		return nil, nil
	}
	if n.Kind != yamlx.String {
		return nil, f.wrong(n, key, "a string")
	}
	s := n.Text
	return &s, nil
}

func (f fields) boolean(key string) (bool, error) {
	n := f.get(key)
	if n == nil {
		return false, nil
	}
	if n.Kind != yamlx.Bool {
		return false, f.wrong(n, key, "a boolean")
	}
	return n.Text == "true", nil
}

func (f fields) optBool(key string) (*bool, error) {
	if !f.has(key) {
		return nil, nil
	}
	b, err := f.boolean(key)
	return &b, err
}

// strings reads a list of strings. It returns nil for an absent key and an
// empty, non-nil slice for an empty list, because filters tell the two
// apart (SY-V2).
func (f fields) strings(key string) ([]string, error) {
	n := f.get(key)
	if n == nil {
		return nil, nil
	}
	if n.Kind != yamlx.Sequence {
		return nil, f.wrong(n, key, "a list of strings")
	}
	out := make([]string, 0, len(n.Items))
	for _, it := range n.Items {
		if it.Kind != yamlx.String {
			return nil, f.wrong(it, key+" entry", "a string")
		}
		out = append(out, it.Text)
	}
	return out, nil
}

// int32 reads an integer within int32, as the JSON number the CLI sends:
// `30.0` is 30, `30.5` is not an integer.
func (f fields) int32(key string) (*int32, error) {
	n := f.get(key)
	if n == nil {
		return nil, nil
	}
	v, ok := n.Float64()
	if !ok || v != math.Trunc(v) || v < math.MinInt32 || v > math.MaxInt32 {
		return nil, f.wrong(n, key, "an integer")
	}
	i := int32(v)
	return &i, nil
}

func (f fields) raw(key string) json.RawMessage {
	n := f.get(key)
	if n == nil {
		return nil
	}
	return n.AppendJSON(nil)
}

// mapping parses a YAML file that must hold a mapping (or nothing, which
// counts as an empty mapping).
func parseMapping(file string, kind ErrorKind, src []byte) (fields, error) {
	n, err := yamlx.Parse(src)
	if err != nil {
		return fields{}, yamlError(file, err)
	}
	switch n.Kind {
	case yamlx.Mapping:
	case yamlx.Null:
		n = &yamlx.Node{Kind: yamlx.Mapping}
	default:
		return fields{}, &Error{Kind: kind, File: file, Line: n.Line, Column: n.Column,
			Msg: "the file must hold a mapping, not " + describe(n)}
	}
	return fields{file: file, kind: kind, m: n}, nil
}

func yamlError(file string, err error) *Error {
	e := &Error{Kind: ErrYAML, File: file, Msg: err.Error(), Err: err}
	if ye, ok := err.(*yamlx.Error); ok {
		e.Line, e.Column = ye.Line, ye.Column
		e.Msg = string(ye.Kind) + ": " + ye.Msg
	}
	return e
}

// jsWhitespace reports the characters JavaScript's trim and trimStart
// remove: WhiteSpace and LineTerminator, which unlike Go's unicode.IsSpace
// include U+FEFF and exclude U+0085.
func jsWhitespace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}

func trimStartJS(s string) string { return strings.TrimLeftFunc(s, jsWhitespace) }
