package openflow

import (
	"encoding/json"
	"strconv"
)

// transformFieldNames are the members an input transform object may use.
var transformFieldNames = []string{"type", "value", "expr"}

var nullJSON = json.RawMessage("null")

func gotTransformType(n *node, got string) error {
	return &decodeError{n, "got value: " + got + " for field `type`, expected value: `static` or `javascript`"}
}

// decodeTransform decodes an ordinary input transform (OF-10): step inputs,
// `iterator`, `sleep` and `suspend.user_groups_required`. The `type` selects
// which member is read: `static` reads `value` (absent means null) and
// `javascript` reads `expr` (absent means ""); the other member is ignored.
func decodeTransform(n *node) (InputTransform, error) {
	if n.kind != kindObject {
		return InputTransform{}, invalidType(n, "an input transform object")
	}
	typ, err := soleMember(n, "type")
	if err != nil {
		return InputTransform{}, err
	}
	if typ == nil {
		return InputTransform{}, missingField(n, "type")
	}
	s, err := decodeString(typ)
	if err != nil {
		return InputTransform{}, err
	}
	switch s {
	case "static":
		t := InputTransform{Type: Static, Value: nullJSON}
		v, err := soleMember(n, "value")
		if v != nil {
			t.Value = raw(v)
		}
		return t, err
	case "javascript":
		t := InputTransform{Type: JavaScript}
		e, err := soleMember(n, "expr")
		if err != nil || e == nil || e.kind == kindNull {
			return t, err
		}
		t.Expr, err = decodeString(e)
		return t, err
	case "ai":
		return InputTransform{Type: AI}, nil
	}
	return InputTransform{}, gotTransformType(typ, s)
}

// soleMember returns the member named name, nil when absent, and an error
// when the name repeats.
func soleMember(n *node, name string) (*node, error) {
	var found *node
	for _, m := range n.members {
		if m.key != name {
			continue
		}
		if found != nil {
			return nil, duplicateField(m.val, name)
		}
		found = m.val
	}
	return found, nil
}

// restricted holds the members of a restricted transform object.
type restricted[I integer] struct {
	typ   string
	value *I
	expr  *string
}

func restrictedFields[I integer]() []field[restricted[I]] {
	return []field[restricted[I]]{
		stringField("type", true, func(r *restricted[I]) *string { return &r.typ }),
		optIntField("value", func(r *restricted[I]) **I { return &r.value }),
		optStringField("expr", func(r *restricted[I]) **string { return &r.expr }),
	}
}

// decodeRestricted decodes the two restricted slots, module `timeout` (i32)
// and loop `parallelism` (u16) (OF-V2). A bare integer is a static value. In
// object form every supplied `value` must be an integer of the slot's width
// or null, and every supplied `expr` a string or null, whatever the `type`.
// Null means the slot is absent.
func decodeRestricted[I integer](n *node) (*InputTransform, error) {
	switch n.kind {
	case kindNull:
		return nil, nil
	case kindNumber:
		v, err := decodeInt[I](n)
		if err != nil {
			return nil, err
		}
		return &InputTransform{Type: Static, Value: intJSON(v)}, nil
	case kindObject:
		var r restricted[I]
		if err := decodeStruct(nil, n, "an input transform object", restrictedFields[I](), &r); err != nil {
			return nil, err
		}
		switch r.typ {
		case "static":
			t := &InputTransform{Type: Static, Value: nullJSON}
			if r.value != nil {
				t.Value = intJSON(*r.value)
			}
			return t, nil
		case "javascript":
			t := &InputTransform{Type: JavaScript}
			if r.expr != nil {
				t.Expr = *r.expr
			}
			return t, nil
		}
		typ, _ := soleMember(n, "type")
		return nil, gotTransformType(typ, r.typ)
	}
	return nil, invalidType(n, widthOf[I]().name+" or an input transform object")
}

func intJSON[I integer](v I) json.RawMessage {
	return json.RawMessage(strconv.FormatInt(int64(v), 10))
}

// decodeTransforms decodes an `input_transforms` map. A repeated name keeps
// its first position and takes its last transform, as a map insert does.
func decodeTransforms(n *node) (Transforms, error) {
	if n.kind != kindObject {
		return nil, invalidType(n, "a map")
	}
	out := Transforms{}
	for _, m := range n.members {
		t, err := decodeTransform(m.val)
		if err != nil {
			return nil, err
		}
		if i := out.index(m.key); i >= 0 {
			out[i].Transform = t
		} else {
			out = append(out, NamedTransform{m.key, t})
		}
	}
	return out, nil
}

func (t Transforms) index(name string) int {
	for i, e := range t {
		if e.Name == name {
			return i
		}
	}
	return -1
}
