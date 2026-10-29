package ir

import (
	"bytes"
	"encoding/json"

	"github.com/invakid404/windmill-lite/internal/expr"
)

// Version is the version of the IR's JSON form. ir.json is a cache for one
// engine version, not an exchange format (0010); the number changes when
// the form does. Version 2 carries every expression compiled (expr.Expr's
// textual stages, milestone N1a); version 1 held only its site and text.
const Version = 2

// Program is a compiled bundle: every flow and script the roots reach, the
// host contract it was built against, and the deviations its content puts
// in effect (0002).
type Program struct {
	Version int `json:"version"`
	// Roots are the contract's roots, flows and scripts apart, in contract
	// order.
	Roots struct {
		Flows   []string `json:"flows"`
		Scripts []string `json:"scripts"`
	} `json:"roots"`
	// Contract is the host contract the program was compiled against.
	Contract Contract `json:"contract"`
	// Flows holds every flow the roots reach, by path. A flow reached only
	// through provided sites is opaque and absent.
	Flows map[string]*Flow `json:"flows"`
	// Scripts is the script inventory: every path script and inline script
	// the roots reach, by ScriptID.
	Scripts map[string]*Script `json:"scripts"`
	// Deviations lists the named deviations of 0001 that the folder's
	// content puts in effect, sorted: D5 (a native boundary step), D6 (a
	// derived module id), D7 (a nobundling script), D12 (host-provided
	// modules) and D13 (a CommonJS codebase script). Deviations that hold
	// for every bundle, or depend on run-time settings, are the manifest's
	// and the trace's.
	Deviations []string `json:"deviations"`
}

// Flow is a compiled flow (OF-16), normalised to its successfully deployed
// lite form.
type Flow struct {
	Path        string  `json:"path"`
	Summary     *string `json:"summary,omitempty"`
	Description *string `json:"description,omitempty"`
	// Schema is the flow's input JSON Schema, verbatim. It has no execution
	// semantics (OF-18); it is kept for client generation.
	Schema  json.RawMessage `json:"schema,omitempty"`
	Modules []*Module       `json:"modules"`
	// FailureModule and PreprocessorModule sit in their own slots
	// (`s:failure`, `s:preprocessor`), whatever their ids.
	FailureModule      *Module `json:"failure_module,omitempty"`
	PreprocessorModule *Module `json:"preprocessor_module,omitempty"`
	// SkipExpr is the flow's skip_expr (JE-33).
	SkipExpr *expr.Expr `json:"skip_expr,omitempty"`
	// FlowEnv maps names to verbatim JSON values, in input order.
	FlowEnv []EnvVar `json:"flow_env,omitempty"`
	// EarlyReturn is a module id, never JavaScript (OF-V3).
	EarlyReturn       *string `json:"early_return,omitempty"`
	CacheTTL          *uint32 `json:"cache_ttl,omitempty"`
	CacheIgnoreS3Path *bool   `json:"cache_ignore_s3_path,omitempty"`
	SameWorker        bool    `json:"same_worker,omitempty"`
	// Timeout is the whole-flow timeout in seconds, from the flow's
	// metadata.
	Timeout *int32 `json:"timeout,omitempty"`
}

// EnvVar is one flow_env entry; Value is the JSON text as written.
type EnvVar struct {
	Name  string          `json:"name"`
	Value json.RawMessage `json:"value"`
}

// Kind is a module's kind in the IR.
type Kind string

// The module kinds: the in-scope OpenFlow kinds, and Provided for a
// mandatory host-provided call site.
const (
	KindScript    Kind = "script"
	KindRawScript Kind = "rawscript"
	KindIdentity  Kind = "identity"
	KindSubflow   Kind = "flow"
	KindForLoop   Kind = "forloopflow"
	KindWhileLoop Kind = "whileloopflow"
	KindBranchOne Kind = "branchone"
	KindBranchAll Kind = "branchall"
	KindProvided  Kind = "provided"
)

// Module is a module of a module list, with its envelope already decoded.
// Exactly one of the kind fields is set, or none for an identity.
type Module struct {
	ID string `json:"id"`
	// Occurrence is the module's occurrence k, from 0 in list order, when
	// its id occurs more than once in its module list; nil when the id is
	// unique there. Site paths (0011) carry it as `m:<id>~<k>`.
	Occurrence *int `json:"occurrence,omitempty"`
	Kind       Kind `json:"kind"`
	// DerivedID reports an id derived from the module's position because
	// the folder gave none (deviation D6).
	DerivedID bool `json:"derived_id,omitempty"`
	// Virtual marks the `<id>-v` identity appended after a final sleep
	// (OF-17).
	Virtual bool    `json:"virtual,omitempty"`
	Summary *string `json:"summary,omitempty"`

	// The envelope (OF-5).
	SkipIf              *expr.Expr `json:"skip_if,omitempty"`
	StopAfterIf         *Stop      `json:"stop_after_if,omitempty"`
	StopAfterAllItersIf *Stop      `json:"stop_after_all_iters_if,omitempty"`
	Sleep               *Transform `json:"sleep,omitempty"`
	// Timeout is a restricted transform: a static value is an i32 or null
	// (OF-V2).
	Timeout           *Transform `json:"timeout,omitempty"`
	Retry             *Retry     `json:"retry,omitempty"`
	ContinueOnError   bool       `json:"continue_on_error,omitempty"`
	Mock              *Mock      `json:"mock,omitempty"`
	CacheTTL          *uint32    `json:"cache_ttl,omitempty"`
	CacheIgnoreS3Path *bool      `json:"cache_ignore_s3_path,omitempty"`
	ApplyPreprocessor *bool      `json:"apply_preprocessor,omitempty"`
	// PassFlowInputDirectly on the envelope makes the step receive the
	// flow's args instead of its input transforms (OF-V4).
	PassFlowInputDirectly bool `json:"pass_flow_input_directly,omitempty"`

	// The kinds.
	Leaf      *Leaf      `json:"leaf,omitempty"`
	Subflow   *Subflow   `json:"subflow,omitempty"`
	ForLoop   *ForLoop   `json:"forloop,omitempty"`
	WhileLoop *WhileLoop `json:"whileloop,omitempty"`
	BranchOne *BranchOne `json:"branchone,omitempty"`
	BranchAll *BranchAll `json:"branchall,omitempty"`
	Provided  *Provided  `json:"provided,omitempty"`
}

// Stop is a stop_after_if or stop_after_all_iters_if condition.
type Stop struct {
	Expr               *expr.Expr `json:"expr"`
	SkipIfStopped      bool       `json:"skip_if_stopped"`
	ErrorMessage       *string    `json:"error_message,omitempty"`
	ErrorIncludeResult *bool      `json:"error_include_result,omitempty"`
}

// Retry is a module's retry policy (OF-11), with every sub-field defaulted.
type Retry struct {
	Constant    *ConstantRetry    `json:"constant,omitempty"`
	Exponential *ExponentialRetry `json:"exponential,omitempty"`
	RetryIf     *expr.Expr        `json:"retry_if,omitempty"`
}

// ConstantRetry retries Attempts times, Seconds apart.
type ConstantRetry struct {
	Attempts uint32 `json:"attempts"`
	Seconds  uint16 `json:"seconds"`
}

// ExponentialRetry retries Attempts times with exponential backoff.
type ExponentialRetry struct {
	Attempts     uint32 `json:"attempts"`
	Multiplier   uint16 `json:"multiplier"`
	Seconds      uint16 `json:"seconds"`
	RandomFactor *int8  `json:"random_factor,omitempty"`
}

// Mock replaces a step's result; ReturnValue is verbatim JSON.
type Mock struct {
	Enabled     bool            `json:"enabled"`
	ReturnValue json.RawMessage `json:"return_value,omitempty"`
}

// TransformKind is the kind of a Transform.
type TransformKind string

const (
	// Static is a static value, kept as its raw JSON text (JE-40).
	Static TransformKind = "static"
	// Javascript is an expression.
	Javascript TransformKind = "javascript"
	// AI is filled by the AI runtime, which windmill-lite does not have: a
	// step input of this kind is left out of the arguments (FX-7), and
	// the other sites fail at run time as Windmill's do.
	AI TransformKind = "ai"
)

// Transform is an input transform, an iterator, a sleep, a timeout or a
// parallelism (OF-10).
type Transform struct {
	Type TransformKind `json:"type"`
	// Value is a static value's JSON text, verbatim; `null` when absent.
	Value json.RawMessage `json:"value,omitempty"`
	Expr  *expr.Expr      `json:"expr,omitempty"`
}

// Input is one named input transform, in input order.
type Input struct {
	Name      string    `json:"name"`
	Transform Transform `json:"transform"`
}

// Leaf is a script or rawscript step: a reference into the script inventory
// and the step's harness class, which is all the engine needs to place it
// (0002, 0004).
type Leaf struct {
	Script   string   `json:"script"`
	Harness  Class    `json:"harness"`
	Boundary Boundary `json:"boundary"`
	Inputs   []Input  `json:"inputs"`
}

// Subflow runs the flow at Path, which Program.Flows holds.
type Subflow struct {
	Path   string  `json:"path"`
	Inputs []Input `json:"inputs"`
}

// ForLoop is a forloopflow with its deployed defaults (OF-6).
type ForLoop struct {
	Iterator     Transform `json:"iterator"`
	Modules      []*Module `json:"modules"`
	SkipFailures bool      `json:"skip_failures"`
	Parallel     bool      `json:"parallel"`
	// Parallelism is a restricted transform: a static value is a u16 or
	// null (OF-V2).
	Parallelism *Transform `json:"parallelism,omitempty"`
	// Squash fails the loop on CE when it starts runners (CF-25).
	Squash bool `json:"squash,omitempty"`
	// Collapse reports that the loop runs each iteration as a job of the
	// enclosing flow, with no child flow (FX-46, CF-8).
	Collapse bool `json:"collapse"`
}

// WhileLoop is a whileloopflow (OF-6); its `parallel` fields are dropped
// (OF-8).
type WhileLoop struct {
	Modules      []*Module `json:"modules"`
	SkipFailures bool      `json:"skip_failures"`
	Squash       bool      `json:"squash,omitempty"`
	Collapse     bool      `json:"collapse"`
}

// BranchOne runs the first branch whose predicate is true, or Default.
type BranchOne struct {
	Branches []OneBranch `json:"branches"`
	Default  []*Module   `json:"default"`
}

// OneBranch is a branch of a BranchOne.
type OneBranch struct {
	Summary *string    `json:"summary,omitempty"`
	Expr    *expr.Expr `json:"expr"`
	Modules []*Module  `json:"modules"`
}

// BranchAll runs every branch.
type BranchAll struct {
	Branches []AllBranch `json:"branches"`
	Parallel bool        `json:"parallel"`
}

// AllBranch is a branch of a BranchAll, with its deployed skip_failure.
type AllBranch struct {
	Summary     *string   `json:"summary,omitempty"`
	Modules     []*Module `json:"modules"`
	SkipFailure bool      `json:"skip_failure"`
}

// Provided is a mandatory host-provided call site (0002, 0008). The module
// keeps its original envelope, so the orchestration around the site is the
// real one; the site's target is not part of the program.
type Provided struct {
	// Site is the index of the contract's provided site that names it.
	Site int `json:"site"`
	// Static is the static provider's value, verbatim; Provider the name of
	// the provider the host registers. Exactly one is set.
	Static   json.RawMessage `json:"static,omitempty"`
	Provider string          `json:"provider,omitempty"`
	// Target is what the site would have run: its kind (script, flow or
	// rawscript) and, for a by-path target, its path.
	Target Target `json:"target"`
	// Inputs are the site's own input transforms, evaluated as for the
	// real step; the provider receives their values.
	Inputs []Input `json:"inputs"`
}

// Target is the original target of a provided site.
type Target struct {
	Kind Kind   `json:"kind"`
	Path string `json:"path,omitempty"`
}

// MarshalIndent renders the program as indented JSON without escaping
// HTML characters, so verbatim JSON values keep their characters. Like any
// re-encoding of JSON, it drops insignificant white space inside them.
func (p *Program) MarshalIndent() ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(p); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
