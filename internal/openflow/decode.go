package openflow

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// Struct decoding follows serde's derive rules, which the effective decoder
// uses (OF-1, OF-4, OF-V1):
//
//   - members are decoded in input order and the first failure wins;
//   - names are case-sensitive, and a name no field claims is ignored;
//   - a field given twice (also through its alias) is a duplicate-field
//     error;
//   - required fields are checked after the last member, in table order;
//   - an optional field treats null as absent, while a field with a plain
//     default rejects null.
//
// Each field table is also the decoder's list of mapped schema properties,
// which the schema-coverage test reads (LIC-3).

// decoder carries what the decode functions need beyond the node itself.
type decoder struct {
	// value is the flow value's node; derived module ids (D6) use
	// positions relative to it.
	value *node
}

type field[T any] struct {
	name     string
	alias    string
	required bool
	decode   func(d *decoder, n *node, out *T) error
}

func decodeStruct[T any](d *decoder, n *node, expected string, fields []field[T], out *T) error {
	if n.kind != kindObject {
		return invalidType(n, expected)
	}
	seen := make([]bool, len(fields))
	for _, m := range n.members {
		i := fieldIndex(fields, m.key)
		if i < 0 {
			continue
		}
		if seen[i] {
			return duplicateField(m.val, fields[i].name)
		}
		seen[i] = true
		if err := fields[i].decode(d, m.val, out); err != nil {
			return err
		}
	}
	for i, f := range fields {
		if f.required && !seen[i] {
			return missingField(n, f.name)
		}
	}
	return nil
}

func fieldIndex[T any](fields []field[T], name string) int {
	for i, f := range fields {
		if f.name == name || f.alias != "" && f.alias == name {
			return i
		}
	}
	return -1
}

func fieldNames[T any](fields []field[T]) []string {
	var names []string
	for _, f := range fields {
		names = append(names, f.name)
		if f.alias != "" {
			names = append(names, f.alias)
		}
	}
	return names
}

// Scalars.

func decodeString(n *node) (string, error) {
	if n.kind != kindString {
		return "", invalidType(n, "a string")
	}
	return n.str, nil
}

func decodeBool(n *node) (bool, error) {
	if n.kind != kindBool {
		return false, invalidType(n, "a boolean")
	}
	return n.boolean, nil
}

func optional[V any](n *node, decode func(*node) (V, error)) (*V, error) {
	if n.kind == kindNull {
		return nil, nil
	}
	v, err := decode(n)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// raw returns a value's JSON text verbatim.
func raw(n *node) json.RawMessage {
	return json.RawMessage(n.raw)
}

func decodeStrings(n *node) ([]string, error) {
	if n.kind != kindArray {
		return nil, invalidType(n, "a sequence")
	}
	out := make([]string, 0, len(n.elems))
	for _, e := range n.elems {
		s, err := decodeString(e)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// Integers. JSON numbers are classified as serde_json does: a literal
// without fraction or exponent is an integer when it fits 64 bits; `-0`,
// fractions, exponents and wider literals are floating point, which every
// integer field rejects.

type integer interface {
	int8 | int16 | int32 | int64 | uint16 | uint32
}

type width struct {
	name string
	min  int64
	max  uint64
}

var (
	widthI8  = width{"i8", math.MinInt8, math.MaxInt8}
	widthI16 = width{"i16", math.MinInt16, math.MaxInt16}
	widthI32 = width{"i32", math.MinInt32, math.MaxInt32}
	widthI64 = width{"i64", math.MinInt64, math.MaxInt64}
	widthU16 = width{"u16", 0, math.MaxUint16}
	widthU32 = width{"u32", 0, math.MaxUint32}
)

func widthOf[I integer]() width {
	switch any(I(0)).(type) {
	case int8:
		return widthI8
	case int16:
		return widthI16
	case int32:
		return widthI32
	case int64:
		return widthI64
	case uint16:
		return widthU16
	}
	return widthU32
}

type intLiteral struct {
	neg bool
	mag uint64
}

func parseInteger(n *node) (intLiteral, bool) {
	s := string(n.raw)
	if strings.ContainsAny(s, ".eE") {
		return intLiteral{}, false
	}
	neg := strings.HasPrefix(s, "-")
	mag, err := strconv.ParseUint(strings.TrimPrefix(s, "-"), 10, 64)
	if err != nil || neg && (mag == 0 || mag > 1<<63) {
		return intLiteral{}, false
	}
	return intLiteral{neg, mag}, true
}

func decodeInt[I integer](n *node) (I, error) {
	w := widthOf[I]()
	if n.kind != kindNumber {
		return 0, invalidType(n, w.name)
	}
	lit, ok := parseInteger(n)
	if !ok {
		return 0, invalidType(n, w.name)
	}
	if lit.neg {
		v := -int64(lit.mag)
		if v < w.min {
			return 0, invalidValue(n, w.name)
		}
		return I(v), nil
	}
	if lit.mag > w.max {
		return 0, invalidValue(n, w.name)
	}
	return I(lit.mag), nil
}

// Field constructors. `at` selects the destination inside the struct.

func optStringField[T any](name string, at func(*T) **string) field[T] {
	return field[T]{name: name, decode: func(_ *decoder, n *node, out *T) (err error) {
		*at(out), err = optional(n, decodeString)
		return err
	}}
}

// stringField rejects null; with required false the caller presets the
// default.
func stringField[T any](name string, required bool, at func(*T) *string) field[T] {
	return field[T]{name: name, required: required, decode: func(_ *decoder, n *node, out *T) (err error) {
		*at(out), err = decodeString(n)
		return err
	}}
}

func optBoolField[T any](name string, at func(*T) **bool) field[T] {
	return field[T]{name: name, decode: func(_ *decoder, n *node, out *T) (err error) {
		*at(out), err = optional(n, decodeBool)
		return err
	}}
}

// boolField rejects null; with required false the caller presets the
// default.
func boolField[T any](name string, required bool, at func(*T) *bool) field[T] {
	return field[T]{name: name, required: required, decode: func(_ *decoder, n *node, out *T) (err error) {
		*at(out), err = decodeBool(n)
		return err
	}}
}

func optIntField[T any, I integer](name string, at func(*T) **I) field[T] {
	return field[T]{name: name, decode: func(_ *decoder, n *node, out *T) (err error) {
		*at(out), err = optional(n, decodeInt[I])
		return err
	}}
}

// intField rejects null; the caller presets the default.
func intField[T any, I integer](name string, at func(*T) *I) field[T] {
	return field[T]{name: name, decode: func(_ *decoder, n *node, out *T) (err error) {
		*at(out), err = decodeInt[I](n)
		return err
	}}
}

// optRawField keeps any JSON value verbatim; null counts as absent.
func optRawField[T any](name string, at func(*T) *json.RawMessage) field[T] {
	return field[T]{name: name, decode: func(_ *decoder, n *node, out *T) error {
		if n.kind != kindNull {
			*at(out) = raw(n)
		}
		return nil
	}}
}

func optStringsField[T any](name string, at func(*T) *[]string) field[T] {
	return field[T]{name: name, decode: func(_ *decoder, n *node, out *T) error {
		if n.kind == kindNull {
			return nil
		}
		s, err := decodeStrings(n)
		*at(out) = s
		return err
	}}
}
