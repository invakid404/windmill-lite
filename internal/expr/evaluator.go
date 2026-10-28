package expr

import (
	"context"

	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// Evaluator evaluates compiled expressions for the flow interpreter: the
// whole pipeline, from the shortcuts to the engine and the error texts
// (milestones B2a and B2b). It is safe for concurrent use.
//
// Eval returns the expression's value as producer text: a shortcut's stored
// text verbatim (JE-6 to JE-9), or the engine's JSON.stringify output
// (JE-15). An error is the evaluation's failure, with Windmill's message;
// each site routes it as its finding says (JE-25 to JE-35). Eval checks ctx
// before it starts, as every expression evaluation is a cancellation point
// (ADR 0004).
type Evaluator interface {
	Eval(ctx context.Context, e *Expr, s *Scope) (jsonval.Value, error)
}

// Scope is what one evaluation can see at its site (JE-22). Which names a
// site binds, and to what, is the flow interpreter's to decide from JE-22
// and JE-36; the evaluator binds only those the text mentions (JE-10).
type Scope struct {
	// PreviousID is the id of the module before this one in its flow, or ""
	// at the first module of a flow or body and wherever JE-22 says the
	// previous id is empty (JE-8).
	PreviousID string
	// Context holds the context values by name: previous_result, result,
	// params, resume, resumes, approvers, all_iters, started_at,
	// flow_job_id, and WM_SCHEDULED_FOR for skip_expr (JE-6, JE-22).
	Context []Named
	// FlowInput is the current (sub)flow's input (JE-37), or the zero Value
	// at a site with no flow input, where flow_input is bound to null
	// (JE-10).
	//
	// The shortcuts answer these texts verbatim: JE-6 a context value's,
	// JE-8 previous_result's, JE-7 a member of FlowInput or FlowEnv. A site
	// can quote the answer (the timeout site's parse error does), so the
	// form of each text is observable. ADR 0005's routing table says which
	// text each value holds; whether a value read from the database (the
	// flow input's members, previous_result after a loop) is in jsonb's
	// spaced text output there is open (docs/design/README.md request 8).
	FlowInput jsonval.Value
	// FlowEnv is the resolved flow env (JE-38), or the zero Value when there
	// is none, where flow_env is bound to {} (JE-10).
	FlowEnv jsonval.Value
	// Results is the site's results context, or nil at a site that has none
	// (JE-22).
	Results Results
	// Helpers answers the variable(), resource() and flow_user_state()
	// helpers (JE-20), or is nil when the site has no authenticated client,
	// where the helpers reject with JE-20's "not available" texts.
	// flow_user_state() also needs Results.
	Helpers Helpers
}

// Named is one context value.
type Named struct {
	Name  string
	Value jsonval.Value
}

// Results answers results lookups for one site, from the run's state. The
// previous-step case of JE-18 (step 1) is the evaluator's own: it returns
// previous_result from the scope without asking.
type Results interface {
	// Lookup resolves results.<id> on the engine path, steps 2 and 3 of
	// JE-18: a module of the current flow that has a job yields its result
	// (an array of child results for a loop or branchall) or an error when
	// fetching it fails, whose message is the read's whole text, Failed to
	// fetch result for step '<id>': <cause> (results for step, for a list);
	// anything else goes through the node-id lookup, and any failure there
	// yields JSON null. Either way it is a database read: the evaluator
	// binds the value's stored text.
	Lookup(ctx context.Context, id string) (jsonval.Value, error)
	// Stored returns the stored result found for id by the result-by-node-id
	// lookup of JE-19, through the scope chain, and false when there is
	// none. JE-9 applies its path to this value's stored text and answers
	// in jsonb's text output.
	Stored(ctx context.Context, id string) (jsonval.Value, bool)
}

// Helpers resolves the expression helpers of JE-20 for one run, through the
// platform services the run uses (ADR 0007). A returned error rejects the
// helper's Promise with an Error whose message is err.Error(), which is the
// API error text Windmill reports.
type Helpers interface {
	// Variable returns the variable's value as a string, secrets decrypted.
	Variable(ctx context.Context, path string) (string, error)
	// Resource returns the resource's value with its nested $var: and $res:
	// references interpolated.
	Resource(ctx context.Context, path string) (jsonval.Value, error)
	// FlowUserState returns the value stored under key for the run's root
	// flow job (later scope).
	FlowUserState(ctx context.Context, key string) (jsonval.Value, error)
}
