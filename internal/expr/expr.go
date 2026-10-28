// Package expr is windmill-lite's expression pipeline (ADR 0003).
//
// Every flow expression is evaluated in Go: a textual pipeline (trimming,
// the shortcuts of JE-6 to JE-9, the rewrites of JE-11 and JE-12,
// statement-mode detection, binding analysis) and, when no shortcut answers,
// QuickJS-ng embedded through cgo. This package defines the contracts the
// two halves meet at:
//
//   - Expr, the compiled expression the IR carries (ADR 0002);
//   - Engine, the JavaScript engine behind a Go interface; package
//     internal/expr/quickjs implements it (milestone B1a);
//   - Evaluator and Scope, what the flow interpreter calls at each
//     expression site (milestones B2a and B2b implement it).
//
// The pipeline itself is not implemented yet.
package expr

import "fmt"

// SiteKind is where an expression sits in a flow, which fixes its scope and
// whether it is a predicate (JE-22).
type SiteKind uint8

// The expression sites of JE-22. The suspend site
// (suspend.user_groups_required) is absent: suspend is rejected at load
// (ADR 0001 §3).
const (
	// SiteInputTransform is a step's input transform of type javascript
	// (JE-23).
	SiteInputTransform SiteKind = iota + 1
	// SiteFailureInputTransform is an input transform of a flow's failure
	// module, whose scope differs from other input transforms (JE-35).
	SiteFailureInputTransform
	// SiteIterator is a for loop's iterator (JE-30).
	SiteIterator
	// SiteParallelism is a parallel for loop's parallelism (JE-V1; later).
	SiteParallelism
	// SiteBranchOne is one branch predicate of a branchone (JE-31).
	SiteBranchOne
	// SiteSkipIf is a module's skip_if (JE-25).
	SiteSkipIf
	// SiteStopAfterIf is a module's stop_after_if (JE-26).
	SiteStopAfterIf
	// SiteStopAfterAllItersIf is a loop's or branchall's
	// stop_after_all_iters_if (JE-27).
	SiteStopAfterAllItersIf
	// SiteSleep is a module's sleep of type javascript (JE-28).
	SiteSleep
	// SiteTimeout is a module's timeout of type javascript (JE-29).
	SiteTimeout
	// SiteRetryIf is a retry's retry_if (JE-32).
	SiteRetryIf
	// SiteSkipExpr is a flow's skip_expr (JE-33; later).
	SiteSkipExpr
)

var siteNames = [...]string{
	SiteInputTransform:        "input_transform",
	SiteFailureInputTransform: "failure_input_transform",
	SiteIterator:              "iterator",
	SiteParallelism:           "parallelism",
	SiteBranchOne:             "branchone",
	SiteSkipIf:                "skip_if",
	SiteStopAfterIf:           "stop_after_if",
	SiteStopAfterAllItersIf:   "stop_after_all_iters_if",
	SiteSleep:                 "sleep",
	SiteTimeout:               "timeout",
	SiteRetryIf:               "retry_if",
	SiteSkipExpr:              "skip_expr",
}

// Valid reports whether k is one of the sites.
func (k SiteKind) Valid() bool { return k > 0 && int(k) < len(siteNames) }

// String returns the site's name, the OpenFlow field it comes from.
func (k SiteKind) String() string {
	if !k.Valid() {
		return fmt.Sprintf("SiteKind(%d)", uint8(k))
	}
	return siteNames[k]
}

// Predicate reports whether k is a boolean site, whose text is wrapped as
// Boolean(<text>) before trimming and whose value must be JSON true or false
// (JE-24).
func (k SiteKind) Predicate() bool {
	switch k {
	case SiteBranchOne, SiteSkipIf, SiteStopAfterIf, SiteStopAfterAllItersIf, SiteRetryIf, SiteSkipExpr:
		return true
	}
	return false
}

// MarshalText returns the site's name, for the IR's JSON form.
func (k SiteKind) MarshalText() ([]byte, error) {
	if !k.Valid() {
		return nil, fmt.Errorf("expr: unknown site kind %d", uint8(k))
	}
	return []byte(siteNames[k]), nil
}

// UnmarshalText accepts a site's name.
func (k *SiteKind) UnmarshalText(text []byte) error {
	for i, name := range siteNames {
		if name != "" && name == string(text) {
			*k = SiteKind(i)
			return nil
		}
	}
	return fmt.Errorf("expr: unknown site kind %q", text)
}

// Mode is how the engine source runs the text (JE-13).
type Mode uint8

const (
	// ModeExpression runs the text as "return <text>;" in an async
	// function, so a leading "{" is an object literal.
	ModeExpression Mode = iota + 1
	// ModeStatement runs the text as the body of an async function; with no
	// return the value is undefined, which serialises as null.
	ModeStatement
)

// String returns "expression" or "statement".
func (m Mode) String() string {
	switch m {
	case ModeExpression:
		return "expression"
	case ModeStatement:
		return "statement"
	}
	return fmt.Sprintf("Mode(%d)", uint8(m))
}

// Expr is a compiled expression: the result of the pipeline's textual
// stages, computed once per bundle and never per run (ADR 0002, ADR 0003).
// The engine program compiled from Source is not part of it: it belongs to
// the Engine instance of a running bundle.
type Expr struct {
	// Site is where the expression sits.
	Site SiteKind
	// Text is the expression as written in the flow. Site error messages
	// quote it untrimmed (JE-5).
	Text string
	// Trimmed is what the shortcuts and the mode detection see: Text
	// trimmed, or for a predicate "Boolean(" + Text + ")" trimmed (JE-5,
	// JE-24).
	Trimmed string
	// Shortcut holds the shortcuts the text allows.
	Shortcut Shortcut
	// Mode is the mode of the engine source.
	Mode Mode
	// Binds lists the names the evaluation binds, from the text alone: the
	// context names, flow_input, flow_env, results and error it mentions
	// (JE-10, JE-17).
	Binds []string
	// Source is the engine source: Trimmed after the rewrites of JE-11 and
	// JE-12, in the wrapper that Mode selects (JE-13), serialising the value
	// as JSON.stringify(value ?? null) (JE-15).
	Source string
}

// Shortcut holds the fast paths of JE-6 to JE-9 that the text alone allows.
// Which one answers is decided at evaluation time, in the order JE-6, JE-7,
// JE-8, JE-9, because each also depends on the run: JE-6 on the site's
// context names, JE-7 on the flow input's keys, JE-8 on the previous id and
// JE-9 on the stored results. Predicates never qualify for JE-6 to JE-8,
// because their text starts with "Boolean(" (JE-24).
type Shortcut struct {
	// ContextName is the trimmed text when it is exactly one of the names
	// JE-6 lists (previous_result, result, params, ...).
	ContextName string
	// FlowKey is the key of JE-7's flow_input or flow_env forms.
	FlowKey *FlowKey
	// PreviousID is the id that the text names when it is exactly one of
	// JE-8's four spellings. It fires only if that id is the previous id,
	// which may be "" (the first module of a flow or body).
	PreviousID *string
	// ResultsPath is JE-9's first qualifying line, or nil when no line
	// qualifies.
	ResultsPath *ResultsPath
}

// FlowKey is the key of a JE-7 shortcut.
type FlowKey struct {
	// Env is true for flow_env, false for flow_input.
	Env bool
	// Key is the one top-level key the text names: everything after the
	// first dot, or the text between the quotes of the bracket form.
	Key string
}

// ResultsPath is the first line of the trimmed text that JE-9's pattern
// matches.
type ResultsPath struct {
	// Step is the step id after "results." or "results?.".
	Step string
	// Index is the digits of the optional [<digits>] right after the step
	// id, or "" when there is none.
	Index string
	// Segments are the .segment parts after the step id and index.
	Segments []string
	// Declines is true when the last segment is "length": the shortcut then
	// declines for the whole text, and later lines are not searched.
	Declines bool
}
