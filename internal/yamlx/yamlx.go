// Package yamlx parses YAML into the values the wmill CLI would send to
// Windmill, so that a sync folder's files mean what they mean to Windmill
// (SY-9).
//
// The CLI parses YAML with the npm yaml package v2 (YAML 1.2, core schema)
// plus the scalar tags !inline and !inline_fileset, and sends the resulting
// JavaScript values as JSON. yamlx parses with go.yaml.in/yaml/v3 into
// nodes, never uses that library's scalar resolution, and resolves every
// scalar itself. A mapping becomes the JavaScript object the CLI builds:
// keys are property names, and pairs are in that object's property order.
// Numbers keep their source text; their value is the JavaScript double the
// CLI computes.
//
// YAML that the CLI accepts but turns into JavaScript-specific values, or
// that a YAML 1.1 parser reads differently, is refused with an
// [Unsupported] error instead of being guessed at. The package README
// lists the rules and the refusals.
package yamlx

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Kind is the JSON type of a node.
type Kind uint8

// The node kinds.
const (
	Null Kind = iota
	Bool
	Number
	String
	Sequence
	Mapping
)

var kindNames = [...]string{"null", "bool", "number", "string", "sequence", "mapping"}

func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "Kind(" + strconv.Itoa(int(k)) + ")"
}

// Node is a parsed YAML value. Nodes are shared where the source uses an
// alias, so treat them as read-only.
type Node struct {
	Kind Kind
	// Text is the scalar text: the string for String, "true" or "false"
	// for Bool, and the number exactly as written (017, 0x1F, 1e3, .inf)
	// for Number. It is empty for Null, Sequence and Mapping.
	Text string
	// Items are the elements of a Sequence.
	Items []*Node
	// Pairs are the properties of a Mapping, in JavaScript property order:
	// array-index keys ("0" to "4294967294") in ascending numeric order,
	// then the other keys in the order they first appear.
	Pairs []Pair
	// Line and Column are the 1-based source position of the node, or of
	// its anchored original when it was reached through an alias. Both are
	// 0 for the null value of an empty document.
	Line, Column int

	num float64 // the value of a Number
}

// Pair is one property of a Mapping.
type Pair struct {
	// Key is the property name: a string key as written, or a scalar key
	// converted as JavaScript's String() converts its value (null is "",
	// 0x1F is "31", .inf is "Infinity").
	Key   string
	Value *Node
}

// Float64 returns the JavaScript number of a Number node: the double
// nearest to its text, so integers above 2^53 lose precision as they do in
// the CLI. It returns false for any other kind.
func (n *Node) Float64() (float64, bool) {
	if n == nil || n.Kind != Number {
		return 0, false
	}
	return n.num, true
}

// Get returns the value of a Mapping's property, or nil when n is not a
// Mapping or has no such property.
func (n *Node) Get(key string) *Node {
	if n == nil || n.Kind != Mapping {
		return nil
	}
	for _, p := range n.Pairs {
		if p.Key == key {
			return p.Value
		}
	}
	return nil
}

// MarshalJSON renders the node as JavaScript's JSON.stringify renders the
// CLI's value. A nil node renders as null.
func (n *Node) MarshalJSON() ([]byte, error) {
	return n.AppendJSON(nil), nil
}

// AppendJSON appends the JSON text of the node to b.
func (n *Node) AppendJSON(b []byte) []byte {
	return appendJSON(b, n)
}

// ErrorKind classifies an [Error].
type ErrorKind string

const (
	// Syntax is YAML the CLI rejects too: malformed input, a duplicate
	// key, more than one document, an alias that is undefined or contains
	// itself.
	Syntax ErrorKind = "yaml_syntax"
	// Unsupported is YAML the CLI accepts that yamlx refuses, because the
	// CLI's value is JavaScript-specific or a YAML 1.1 parser cannot read
	// the input the way the CLI does.
	Unsupported ErrorKind = "unsupported_yaml"
)

// Error is a parse failure. Line and Column are 1-based, or 0 when the
// position is unknown.
type Error struct {
	Kind         ErrorKind
	Line, Column int
	Msg          string
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(string(e.Kind))
	b.WriteString(": ")
	if e.Line > 0 {
		fmt.Fprintf(&b, "line %d", e.Line)
		if e.Column > 0 {
			fmt.Fprintf(&b, " column %d", e.Column)
		}
		b.WriteString(": ")
	}
	b.WriteString(e.Msg)
	return b.String()
}

func errorAt(kind ErrorKind, n *yaml.Node, format string, args ...any) *Error {
	e := &Error{Kind: kind, Msg: fmt.Sprintf(format, args...)}
	if n != nil {
		e.Line, e.Column = n.Line, n.Column
	}
	return e
}

// ToJSON parses src and returns the JSON text of its value.
func ToJSON(src []byte) ([]byte, error) {
	n, err := Parse(src)
	if err != nil {
		return nil, err
	}
	return n.AppendJSON(nil), nil
}

// Parse parses a YAML stream holding at most one document. An empty stream,
// or one holding only comments, is null, as in the CLI.
func Parse(src []byte) (*Node, error) {
	src, restore, err := prepare(src)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(src))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return &Node{Kind: Null}, nil
		}
		return nil, libraryError(err)
	}
	var next yaml.Node
	switch err := dec.Decode(&next); {
	case errors.Is(err, io.EOF):
	case err != nil:
		return nil, libraryError(err)
	default:
		return nil, errorAt(Syntax, &next, "the stream holds more than one document")
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return nil, errorAt(Syntax, &doc, "expected one document node")
	}
	p := &parser{
		src:     src,
		restore: restore,
		lines:   lineStarts(src),
		done:    map[*yaml.Node]*Node{},
		active:  map[*yaml.Node]bool{},
		weight:  map[*Node]int64{},
	}
	return p.node(doc.Content[0])
}

// libraryError converts a parse error of the library, "yaml: line N:
// message" or "yaml: message", into a Syntax error.
func libraryError(err error) *Error {
	e := &Error{Kind: Syntax, Msg: strings.TrimPrefix(err.Error(), "yaml: ")}
	if rest, ok := strings.CutPrefix(e.Msg, "line "); ok {
		if num, msg, ok := strings.Cut(rest, ": "); ok {
			if line, err := strconv.Atoi(num); err == nil {
				e.Line, e.Msg = line, msg
			}
		}
	}
	return e
}
