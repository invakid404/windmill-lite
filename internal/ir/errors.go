package ir

import (
	"fmt"
	"strings"
)

// ErrorKind classifies an Error.
type ErrorKind string

// The error kinds. A compile error names the flow and step, or the script,
// it is about; see the package README for when each one is raised.
const (
	// ErrLoad: the sync folder could not supply an item the closure
	// reaches, or the item failed to read (SY-15 and every other
	// syncfolder error). The wrapped *syncfolder.Error says which.
	ErrLoad ErrorKind = "load"
	// ErrUnsupported: a reachable feature that windmill-lite rejects at
	// load (0001 §3, §5a). The message is `unsupported: <feature>`.
	ErrUnsupported ErrorKind = "unsupported_feature"
	// ErrLanguage: a step's language has no harness class in this
	// release (0002, Language and harness class). The message is
	// `unsupported_language: <lang>`.
	ErrLanguage ErrorKind = "unsupported_language"
	// ErrPinnedHash: a script step pins a version with `hash`, which a
	// folder cannot supply (OF-14, SY-15).
	ErrPinnedHash ErrorKind = "pinned_hash"
	// ErrAbsentSkipFlag: with the Strict option, a for-loop's
	// `skip_failures` or a branchall branch's `skip_failure` is absent
	// (Q-CF-13).
	ErrAbsentSkipFlag ErrorKind = "absent_skip_flag"
	// ErrAmbiguousSelector: a selector names a module id that occurs more
	// than once in one module list without giving the occurrence, or a
	// mandatory provided-site selector matches more than one site (0008,
	// 0001 §5a).
	ErrAmbiguousSelector ErrorKind = "ambiguous_selector"
	// ErrContract: the host contract is malformed, or names something the
	// program does not have.
	ErrContract ErrorKind = "invalid_contract"
)

// Error is a compile error.
type Error struct {
	Kind ErrorKind
	// Flow is the path of the flow the error is about, and Step the id of
	// the module, when the error is about one.
	Flow, Step string
	// Script is the path of the script the error is about, for a root
	// script or a step's target.
	Script string
	// Selector is the index of the host contract's provided site the
	// error is about, or -1.
	Selector int
	Msg      string
	// Err is the underlying error: a *syncfolder.Error for ErrLoad.
	Err error
}

// Error returns "<where>: <message>", where <where> names the flow step as
// "<flow path> step '<id>'", or the flow, the script or the provided site.
func (e *Error) Error() string {
	var b strings.Builder
	switch {
	case e.Flow != "" && e.Step != "":
		fmt.Fprintf(&b, "%s step '%s'", e.Flow, e.Step)
	case e.Flow != "":
		fmt.Fprintf(&b, "flow %s", e.Flow)
	case e.Script != "":
		fmt.Fprintf(&b, "script %s", e.Script)
	case e.Selector >= 0:
		fmt.Fprintf(&b, "provided site %d", e.Selector)
	}
	if b.Len() > 0 {
		b.WriteString(": ")
	}
	b.WriteString(e.Msg)
	if e.Err != nil {
		if e.Msg != "" {
			b.WriteString(": ")
		}
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

// Unwrap returns the underlying error.
func (e *Error) Unwrap() error { return e.Err }

func unsupported(flow, step, feature string) *Error {
	return &Error{Kind: ErrUnsupported, Flow: flow, Step: step, Selector: -1, Msg: "unsupported: " + feature}
}
