package openflow

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrorKind classifies a decode failure. The kinds follow the stages of
// Windmill's deploy-time validation (OF-2).
type ErrorKind int

const (
	// ErrSyntax: the input is not JSON.
	ErrSyntax ErrorKind = iota + 1
	// ErrFlowValue: the outer decode failed, covering the FlowValue fields
	// and the envelopes of the top-level, failure and preprocessor modules.
	// Windmill prefixes these with `Invalid flow value:`.
	ErrFlowValue
	// ErrModule: a module value failed to decode, including the envelopes
	// of a container's body modules. Windmill prefixes these with
	// `Module '<id>':`.
	ErrModule
	// ErrRetryBase: an exponential retry with attempts and a zero base.
	ErrRetryBase
	// ErrStepPath: a script or flow step path outside the workspace
	// prefixes.
	ErrStepPath
	// ErrFlow: a field of the flow document outside its value.
	ErrFlow
	// ErrEEOnly: a flow setting that Windmill CE rejects as EE-only.
	ErrEEOnly
)

// Error is a decode or validation failure.
//
// Message comparison with Windmill is by substring, as in the reference
// (OF-2): Windmill appends a position suffix and the API wraps the message
// again. Error() reproduces Windmill's prefixes and texts where the
// reference gives them and follows serde's conventions elsewhere.
type Error struct {
	Kind ErrorKind
	// Module is the module id the message names (ErrModule, ErrRetryBase,
	// ErrStepPath).
	Module string
	// Pointer locates the offending value in the input (RFC 6901). It is a
	// diagnostic and is not part of the message.
	Pointer string
	// Detail is the message without Windmill's prefix.
	Detail string
}

func (e *Error) Error() string {
	switch e.Kind {
	case ErrFlowValue:
		return "Invalid flow value: " + e.Detail
	case ErrModule, ErrRetryBase:
		return "Module '" + e.Module + "': " + e.Detail
	case ErrSyntax:
		return "invalid JSON: " + e.Detail
	case ErrFlow:
		if e.Pointer == "" {
			return "invalid flow: " + e.Detail
		}
		return "invalid flow: " + e.Pointer + ": " + e.Detail
	default:
		return e.Detail
	}
}

const retryBaseDetail = "Exponential backoff base (seconds) must be greater than 0. A base of 0 would cause immediate retries."

func stepPathDetail(id, path string) string {
	return "step '" + id + "' references '" + path + "', which is not a workspace path (expected u/, f/, g/ or hub/). Absolute or local filesystem paths are not allowed in flow steps."
}

// decodeError is a failure below the stage prefixes; the stage that catches
// it turns it into an *Error.
type decodeError struct {
	at  *node
	msg string
}

func (e *decodeError) Error() string { return e.msg }

// The messages below follow serde's wording, which the reference quotes for
// missing fields, unknown variants and duplicate fields.

func invalidType(n *node, expected string) error {
	return &decodeError{n, "invalid type: " + unexpected(n) + ", expected " + expected}
}

func invalidValue(n *node, expected string) error {
	return &decodeError{n, "invalid value: " + unexpected(n) + ", expected " + expected}
}

func missingField(obj *node, name string) error {
	return &decodeError{obj, "missing field `" + name + "`"}
}

func duplicateField(n *node, name string) error {
	return &decodeError{n, "duplicate field `" + name + "`"}
}

func unknownVariant(n *node, got string, variants []string) error {
	return &decodeError{n, "unknown variant `" + got + "`, expected " + oneOf(variants)}
}

func oneOf(variants []string) string {
	quoted := make([]string, len(variants))
	for i, v := range variants {
		quoted[i] = "`" + v + "`"
	}
	switch len(quoted) {
	case 1:
		return quoted[0]
	case 2:
		return quoted[0] + " or " + quoted[1]
	}
	return "one of " + strings.Join(quoted, ", ")
}

// unexpected describes a value the way serde's "invalid type" messages do.
func unexpected(n *node) string {
	switch n.kind {
	case kindNull:
		return "null"
	case kindBool:
		return fmt.Sprintf("boolean `%t`", n.boolean)
	case kindNumber:
		if _, ok := parseInteger(n); ok {
			return "integer `" + string(n.raw) + "`"
		}
		return "floating point `" + string(n.raw) + "`"
	case kindString:
		return "string " + strconv.Quote(n.str)
	case kindArray:
		return "sequence"
	}
	return "map"
}

// stageError converts a failure caught by a stage into an *Error. Errors
// that already are an *Error (from a nested stage) pass through.
func stageError(err error, kind ErrorKind, module string, root *node) error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	var de *decodeError
	if !errors.As(err, &de) {
		return &Error{Kind: kind, Module: module, Detail: err.Error()}
	}
	return &Error{Kind: kind, Module: module, Pointer: pointer(root, de.at), Detail: de.msg}
}
