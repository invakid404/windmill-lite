package openflow

import "encoding/json"

// Flow is a flow document: the OpenFlow object that `flow.yaml` holds
// (summary, description, value, schema) plus the flow metadata fields that
// windmill-lite reads (OF-16). Other metadata keys of `flow.yaml` (tag,
// dedicated_worker, labels, permissions and deploy controls) are placement or
// deploy settings and are not decoded.
type Flow struct {
	Summary     *string
	Description *string
	Value       *FlowValue
	// Schema is the flow's input JSON Schema, verbatim. It has no execution
	// semantics (OF-18). Nil when absent or null.
	Schema          json.RawMessage
	OnBehalfOfEmail *string
	// Timeout is the whole-flow timeout in seconds.
	Timeout *int32
}

// FlowValue is the `value` of a flow (OF-16).
type FlowValue struct {
	Modules            []Module
	FailureModule      *Module
	PreprocessorModule *Module

	SameWorker       bool
	PreserveStepTags bool

	ConcurrentLimit        *int32
	ConcurrencyKey         *string
	ConcurrencyTimeWindowS *int32
	// Debouncing holds the flat flow-level debouncing fields (EE).
	Debouncing Debouncing

	SkipExpr          *string
	CacheTTL          *uint32
	CacheIgnoreS3Path *bool
	// EarlyReturn is a module id, never JavaScript (OF-V3).
	EarlyReturn      *string
	Priority         *int16
	ChatInputEnabled *bool
	// FlowEnv maps names to verbatim JSON values, in input order.
	FlowEnv         []EnvVar
	DeleteAfterSecs *int32
}

// EnvVar is one `flow_env` entry. Value is the JSON text as written; it may
// hold `$var:` or `$res:` references, which are resolved at run time.
type EnvVar struct {
	Name  string
	Value json.RawMessage
}

// Module is a module envelope (`FlowModule`, (OF-5)).
type Module struct {
	// ID is the module id. Ids are never validated and need not be unique
	// (OF-2).
	ID string
	// IDDerived reports that the input had no `id`, so ID was derived from the
	// module's position (deviation D6; Windmill generates a random id).
	IDDerived bool
	Value     Value

	Summary             *string
	StopAfterIf         *StopAfterIf
	StopAfterAllItersIf *StopAfterIf
	SkipIf              *SkipIf
	Sleep               *InputTransform
	// Timeout is a restricted transform: a static value is an i32 or null
	// (OF-V2).
	Timeout           *InputTransform
	Retry             *Retry
	ContinueOnError   *bool
	Suspend           *Suspend
	Mock              *Mock
	CacheTTL          *uint32
	CacheIgnoreS3Path *bool
	Priority          *int16
	DeleteAfterSecs   *int32
	ApplyPreprocessor *bool
	// PassFlowInputDirectly on the envelope makes the step receive the
	// flow's args (OF-V4).
	PassFlowInputDirectly *bool
	Debouncing            *Debouncing

	// at and raw are the envelope and its undecoded value, held between
	// the outer decode and the module-value stage (OF-2).
	at, raw *node
}

// StopAfterIf is a `stop_after_if` or `stop_after_all_iters_if` condition.
type StopAfterIf struct {
	Expr               string
	SkipIfStopped      bool
	ErrorMessage       *string
	ErrorIncludeResult *bool
}

// SkipIf is a `skip_if` condition.
type SkipIf struct {
	Expr string
}

// Retry is a module's retry policy (OF-11). The zero value means no retry.
type Retry struct {
	Constant    *ConstantRetry
	Exponential *ExponentialRetry
	RetryIf     *RetryIf
}

// ConstantRetry retries Attempts times, Seconds apart.
type ConstantRetry struct {
	Attempts uint32
	Seconds  uint16
}

// ExponentialRetry retries Attempts times with exponential backoff.
// Multiplier defaults to 1; RandomFactor is nil when absent or null.
type ExponentialRetry struct {
	Attempts     uint32
	Multiplier   uint16
	Seconds      uint16
	RandomFactor *int8
}

// RetryIf limits retries to errors or results for which Expr is true.
type RetryIf struct {
	Expr string
}

// MaxRetries is the number of retries after the original execution:
// constant plus exponential attempts, saturating at the u32 maximum
// (OF-11).
func (r *Retry) MaxRetries() uint32 {
	var c, e uint32
	if r.Constant != nil {
		c = r.Constant.Attempts
	}
	if r.Exponential != nil {
		e = r.Exponential.Attempts
	}
	if s := c + e; s >= c {
		return s
	}
	return ^uint32(0)
}

// Suspend is an approval step's configuration. windmill-lite does not run
// approvals; the field is decoded so that acceptance matches Windmill.
type Suspend struct {
	RequiredEvents              *uint32
	Timeout                     *uint32
	ResumeForm                  json.RawMessage
	UserAuthRequired            *bool
	UserGroupsRequired          *InputTransform
	SelfApprovalDisabled        *bool
	HideCancel                  *bool
	ContinueOnDisapproveTimeout *bool
}

// Mock replaces a step's result. ReturnValue is verbatim JSON, nil when
// absent or null.
type Mock struct {
	Enabled     bool
	ReturnValue json.RawMessage
}

// Debouncing is the EE debouncing configuration, decoded and ignored.
type Debouncing struct {
	DebounceKey              *string
	DebounceDelayS           *int64
	MaxTotalDebouncingTime   *int64
	MaxTotalDebouncesAmount  *int64
	DebounceArgsToAccumulate []string
}

// TransformType is the `type` of an input transform.
type TransformType string

const (
	Static     TransformType = "static"
	JavaScript TransformType = "javascript"
	// AI transforms are filled by the AI runtime (out of scope).
	AI TransformType = "ai"
)

// InputTransform is a step input, an iterator, a sleep, a timeout or a
// parallelism (OF-10).
type InputTransform struct {
	Type TransformType
	// Value is a static value's JSON text, verbatim (JE-40); `null` when
	// the value is absent.
	Value json.RawMessage
	// Expr is a JavaScript expression as written; "" when absent.
	Expr string
}

// Transforms is a map of input transforms by name, in input order. A name
// that repeats keeps its first position and its last transform.
type Transforms []NamedTransform

// NamedTransform is one entry of Transforms.
type NamedTransform struct {
	Name      string
	Transform InputTransform
}

// Get returns the transform named name.
func (t Transforms) Get(name string) (InputTransform, bool) {
	for _, e := range t {
		if e.Name == name {
			return e.Transform, true
		}
	}
	return InputTransform{}, false
}

// Kind is a module value's `type` (OF-9).
type Kind string

const (
	KindRawScript Kind = "rawscript"
	KindScript    Kind = "script"
	KindFlow      Kind = "flow"
	KindForLoop   Kind = "forloopflow"
	KindWhileLoop Kind = "whileloopflow"
	KindBranchOne Kind = "branchone"
	KindBranchAll Kind = "branchall"
	KindIdentity  Kind = "identity"
	// KindAIAgent is decoded but out of scope; the loader rejects it.
	KindAIAgent Kind = "aiagent"
	// KindFlowScript is the lite form's internal reference to a stored
	// rawscript. The decoder accepts it as Windmill's does; it never appears
	// in a sync folder.
	KindFlowScript Kind = "flowscript"
)

// Value is a module value: one of *RawScript, *PathScript, *PathFlow,
// *ForLoop, *WhileLoop, *BranchOne, *BranchAll, *Identity, *AIAgent and
// *FlowScript.
type Value interface {
	Kind() Kind
}

// RawScript is an inline script.
type RawScript struct {
	Content         string
	Language        Language
	InputTransforms Transforms
	// Lock is the lock text as written; nil when absent or null.
	Lock      *string
	Path      *string
	Tag       *string
	IsTrigger *bool
	// Assets are kept verbatim; nil when absent or null.
	Assets                 []json.RawMessage
	CustomConcurrencyKey   *string
	ConcurrentLimit        *int32
	ConcurrencyTimeWindowS *int32
}

// PathScript runs a workspace script by path.
type PathScript struct {
	Path            string
	InputTransforms Transforms
	Hash            *ScriptHash
	TagOverride     *string
	IsTrigger       *bool
	// PassFlowInputDirectly inside the value is decoded but not used by the
	// flow executor (OF-V4).
	PassFlowInputDirectly *bool
}

// PathFlow runs a workspace flow by path as a subflow.
type PathFlow struct {
	Path                  string
	InputTransforms       Transforms
	PassFlowInputDirectly *bool
}

// ForLoop is a `forloopflow`.
type ForLoop struct {
	Iterator InputTransform
	Modules  []Module
	// SkipFailures is nil when absent or null; see EffectiveSkipFailures.
	SkipFailures *bool
	Parallel     bool
	// Parallelism is a restricted transform: a static value is a u16 or
	// null (OF-V2).
	Parallelism *InputTransform
	Squash      *bool
}

// EffectiveSkipFailures is the decoder's default: absent or null means true
// (OF-6). A preview run of Windmill reads the raw value instead (OF-7).
func (l *ForLoop) EffectiveSkipFailures() bool {
	return l.SkipFailures == nil || *l.SkipFailures
}

// WhileLoop is a `whileloopflow`. Its `parallel` and `parallelism` fields
// are type-checked and dropped (OF-8).
type WhileLoop struct {
	Modules      []Module
	SkipFailures bool
	Squash       *bool
}

// BranchOne runs the first branch whose expression is true, or Default.
type BranchOne struct {
	Branches []Branch
	Default  []Module
}

// BranchAll runs every branch.
type BranchAll struct {
	Branches []Branch
	Parallel bool
}

// Branch is one branch of a BranchOne or BranchAll (OF-6).
type Branch struct {
	Summary *string
	Expr    string
	Modules []Module
	// SkipFailure is nil when absent; null is rejected (OF-V1). See
	// EffectiveSkipFailure.
	SkipFailure *bool
	// Parallel is decoded with its default but not used by name in
	// execution.
	Parallel bool
}

// EffectiveSkipFailure is the decoder's default: absent means true
// (OF-6).
func (b *Branch) EffectiveSkipFailure() bool {
	return b.SkipFailure == nil || *b.SkipFailure
}

// Identity returns its input.
type Identity struct{}

// AIAgent is an `aiagent` module (out of scope; the loader rejects it).
type AIAgent struct {
	Tools                      []AgentTool
	InputTransforms            Transforms
	Tag                        *string
	OmitOutputFromConversation bool
}

// AgentTool is one tool of an AIAgent.
type AgentTool struct {
	ID          string
	Summary     *string
	Description *string
	Value       ToolValue
}

// ToolValue is one of *ModuleTool, *MCPTool and *WebsearchTool.
type ToolValue interface {
	toolType() string
}

// ModuleTool is a tool implemented by a module value.
type ModuleTool struct {
	Value Value
	raw   *node
}

// MCPTool references an MCP server's tools.
type MCPTool struct {
	ResourcePath string
	IncludeTools []string
	ExcludeTools []string
}

// WebsearchTool is the built-in web search tool.
type WebsearchTool struct{}

// FlowScript is the lite form's internal rawscript reference, kept
// verbatim.
type FlowScript struct {
	Raw json.RawMessage
}

func (*RawScript) Kind() Kind  { return KindRawScript }
func (*PathScript) Kind() Kind { return KindScript }
func (*PathFlow) Kind() Kind   { return KindFlow }
func (*ForLoop) Kind() Kind    { return KindForLoop }
func (*WhileLoop) Kind() Kind  { return KindWhileLoop }
func (*BranchOne) Kind() Kind  { return KindBranchOne }
func (*BranchAll) Kind() Kind  { return KindBranchAll }
func (*Identity) Kind() Kind   { return KindIdentity }
func (*AIAgent) Kind() Kind    { return KindAIAgent }
func (*FlowScript) Kind() Kind { return KindFlowScript }

func (*ModuleTool) toolType() string    { return "flowmodule" }
func (*MCPTool) toolType() string       { return "mcp" }
func (*WebsearchTool) toolType() string { return "websearch" }
