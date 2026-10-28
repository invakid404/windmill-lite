package openflow

import (
	"encoding/json"
	"errors"
	"iter"
	"strings"
)

// DecodeFlowValue decodes a flow `value` the way Windmill validates one when
// a flow is created or updated (OF-2):
//
//  1. The outer decode reads the FlowValue fields and the envelopes of the
//     top-level, failure and preprocessor modules. Failures are ErrFlowValue
//     (`Invalid flow value: …`).
//  2. A traversal visits every module: the main list, then the failure
//     module, then the preprocessor module, each module before its body,
//     bodies in order (branches before a branchone's default), and the
//     module values of AI-agent tools. For each module it decodes the value
//     (ErrModule, `Module '<id>': …`; the envelopes of a container's body
//     modules are decoded with the container's value, so their failures
//     name the container), then rejects an exponential retry with attempts
//     and a zero base (ErrRetryBase), then a script or flow path outside
//     the workspace prefixes (ErrStepPath).
//
// The first failure is returned. Module ids are not checked for uniqueness
// or charset, and referenced scripts and flows need not exist (OF-2).
func DecodeFlowValue(data []byte) (*FlowValue, error) {
	root, err := parseJSON(data)
	if err != nil {
		return nil, err
	}
	return decodeFlowValue(root)
}

func decodeFlowValue(n *node) (*FlowValue, error) {
	d := &decoder{value: n}
	v := &FlowValue{}
	if err := decodeStruct(d, n, "struct FlowValue", flowValueFields, v); err != nil {
		return nil, stageError(err, ErrFlowValue, "", nil)
	}
	w := walker{module: d.checkModule, tool: d.checkTool}
	if err := w.flow(v); err != nil {
		return nil, err
	}
	return v, nil
}

var flowValueFields = []field[FlowValue]{
	{name: "modules", required: true, decode: func(d *decoder, n *node, v *FlowValue) (err error) {
		v.Modules, err = decodeEnvelopes(d, n)
		return err
	}},
	{name: "failure_module", decode: func(d *decoder, n *node, v *FlowValue) (err error) {
		v.FailureModule, err = optionalEnvelope(d, n)
		return err
	}},
	{name: "preprocessor_module", decode: func(d *decoder, n *node, v *FlowValue) (err error) {
		v.PreprocessorModule, err = optionalEnvelope(d, n)
		return err
	}},
	boolField("same_worker", false, func(v *FlowValue) *bool { return &v.SameWorker }),
	boolField("preserve_step_tags", false, func(v *FlowValue) *bool { return &v.PreserveStepTags }),
	optIntField("concurrent_limit", func(v *FlowValue) **int32 { return &v.ConcurrentLimit }),
	optStringField("concurrency_key", func(v *FlowValue) **string { return &v.ConcurrencyKey }),
	optIntField("concurrency_time_window_s", func(v *FlowValue) **int32 { return &v.ConcurrencyTimeWindowS }),
	{name: "debounce_key", alias: "custom_debounce_key", decode: func(_ *decoder, n *node, v *FlowValue) (err error) {
		v.Debouncing.DebounceKey, err = optional(n, decodeString)
		return err
	}},
	optIntField("debounce_delay_s", func(v *FlowValue) **int64 { return &v.Debouncing.DebounceDelayS }),
	optIntField("max_total_debouncing_time", func(v *FlowValue) **int64 { return &v.Debouncing.MaxTotalDebouncingTime }),
	optIntField("max_total_debounces_amount", func(v *FlowValue) **int64 { return &v.Debouncing.MaxTotalDebouncesAmount }),
	optStringsField("debounce_args_to_accumulate", func(v *FlowValue) *[]string { return &v.Debouncing.DebounceArgsToAccumulate }),
	optStringField("skip_expr", func(v *FlowValue) **string { return &v.SkipExpr }),
	optIntField("cache_ttl", func(v *FlowValue) **uint32 { return &v.CacheTTL }),
	optBoolField("cache_ignore_s3_path", func(v *FlowValue) **bool { return &v.CacheIgnoreS3Path }),
	optStringField("early_return", func(v *FlowValue) **string { return &v.EarlyReturn }),
	optIntField("priority", func(v *FlowValue) **int16 { return &v.Priority }),
	optBoolField("chat_input_enabled", func(v *FlowValue) **bool { return &v.ChatInputEnabled }),
	{name: "flow_env", decode: func(_ *decoder, n *node, v *FlowValue) (err error) {
		v.FlowEnv, err = decodeFlowEnv(n)
		return err
	}},
	// delete_after_use is decoded and dropped (OF-12).
	{name: "delete_after_use", decode: func(_ *decoder, n *node, _ *FlowValue) error {
		_, err := optional(n, decodeBool)
		return err
	}},
	optIntField("delete_after_secs", func(v *FlowValue) **int32 { return &v.DeleteAfterSecs }),
}

func optionalEnvelope(d *decoder, n *node) (*Module, error) {
	if n.kind == kindNull {
		return nil, nil
	}
	m, err := decodeEnvelope(d, n)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// decodeFlowEnv keeps each value verbatim. A repeated name keeps its first
// position and takes its last value.
func decodeFlowEnv(n *node) ([]EnvVar, error) {
	switch n.kind {
	case kindNull:
		return nil, nil
	case kindObject:
	default:
		return nil, invalidType(n, "a map")
	}
	out := []EnvVar{}
next:
	for _, m := range n.members {
		for i := range out {
			if out[i].Name == m.key {
				out[i].Value = raw(m.val)
				continue next
			}
		}
		out = append(out, EnvVar{m.key, raw(m.val)})
	}
	return out, nil
}

// The module-value stage and the two server checks.

func (d *decoder) checkModule(m *Module) error {
	at := pointer(nil, m.at)
	v, err := decodeValue(d, m.raw)
	if err != nil {
		return stageError(err, ErrModule, m.ID, nil)
	}
	m.Value, m.at, m.raw = v, nil, nil
	if r := m.Retry; r != nil && r.Exponential != nil && r.Exponential.Attempts > 0 && r.Exponential.Seconds == 0 {
		return &Error{Kind: ErrRetryBase, Module: m.ID, Pointer: at + "/retry/exponential", Detail: retryBaseDetail}
	}
	return checkStepPath(m.ID, m.Value, at+"/value/path")
}

func (d *decoder) checkTool(t *AgentTool, mt *ModuleTool) error {
	at := pointer(nil, mt.raw)
	v, err := decodeValue(d, mt.raw)
	if err != nil {
		return stageError(err, ErrModule, t.ID, nil)
	}
	mt.Value, mt.raw = v, nil
	return checkStepPath(t.ID, mt.Value, at+"/path")
}

// checkStepPath rejects a non-empty script or flow path that does not start
// with `u/`, `f/`, `g/` or `hub/` (OF-2).
func checkStepPath(id string, v Value, at string) error {
	var path string
	switch v := v.(type) {
	case *PathScript:
		path = v.Path
	case *PathFlow:
		path = v.Path
	default:
		return nil
	}
	if path == "" {
		return nil
	}
	for _, prefix := range []string{"u/", "f/", "g/", "hub/"} {
		if strings.HasPrefix(path, prefix) {
			return nil
		}
	}
	return &Error{Kind: ErrStepPath, Module: id, Pointer: at, Detail: stepPathDetail(id, path)}
}

// walker is the one traversal order (OF-2, SY-7): the main list, then the
// failure module, then the preprocessor module; each module before its
// body; for-loop and while-loop bodies, every branch in order, a branchone's
// default after its branches, and the module values of AI-agent tools.
type walker struct {
	module func(*Module) error
	tool   func(*AgentTool, *ModuleTool) error
}

func (w walker) flow(v *FlowValue) error {
	if err := w.list(v.Modules); err != nil {
		return err
	}
	for _, m := range []*Module{v.FailureModule, v.PreprocessorModule} {
		if m != nil {
			if err := w.visit(m); err != nil {
				return err
			}
		}
	}
	return nil
}

func (w walker) list(ms []Module) error {
	for i := range ms {
		if err := w.visit(&ms[i]); err != nil {
			return err
		}
	}
	return nil
}

func (w walker) visit(m *Module) error {
	if err := w.module(m); err != nil {
		return err
	}
	return w.body(m.Value)
}

func (w walker) body(v Value) error {
	switch v := v.(type) {
	case *ForLoop:
		return w.list(v.Modules)
	case *WhileLoop:
		return w.list(v.Modules)
	case *BranchOne:
		if err := w.branches(v.Branches); err != nil {
			return err
		}
		return w.list(v.Default)
	case *BranchAll:
		return w.branches(v.Branches)
	case *AIAgent:
		for i := range v.Tools {
			mt, ok := v.Tools[i].Value.(*ModuleTool)
			if !ok {
				continue
			}
			if w.tool != nil {
				if err := w.tool(&v.Tools[i], mt); err != nil {
					return err
				}
			}
			if err := w.body(mt.Value); err != nil {
				return err
			}
		}
	}
	return nil
}

func (w walker) branches(bs []Branch) error {
	for i := range bs {
		if err := w.list(bs[i].Modules); err != nil {
			return err
		}
	}
	return nil
}

var errStopWalk = errors.New("stop")

// AllModules yields every module envelope of the flow value in traversal
// order (see DecodeFlowValue). AI-agent tools are not modules and are not
// yielded, but modules inside a tool's container value are.
func (v *FlowValue) AllModules() iter.Seq[*Module] {
	return func(yield func(*Module) bool) {
		w := walker{module: func(m *Module) error {
			if !yield(m) {
				return errStopWalk
			}
			return nil
		}}
		_ = w.flow(v)
	}
}

// DecodeFlow decodes a flow document: the OpenFlow object of `flow.yaml`
// with its metadata. `value` is decoded as by DecodeFlowValue. Failures of
// other fields are ErrFlow. Windmill CE rejects `ws_error_handler_muted:
// true`, an EE setting (OF-2); that failure is ErrEEOnly. Metadata that
// windmill-lite does not use is ignored.
func DecodeFlow(data []byte) (*Flow, error) {
	root, err := parseJSON(data)
	if err != nil {
		return nil, err
	}
	var doc flowDocument
	if err := decodeStruct(nil, root, "struct OpenFlow", flowFields, &doc); err != nil {
		return nil, stageError(err, ErrFlow, "", nil)
	}
	if doc.wsErrorHandlerMuted != nil && *doc.wsErrorHandlerMuted {
		return nil, &Error{Kind: ErrEEOnly, Pointer: "/ws_error_handler_muted", Detail: wsErrorHandlerMutedDetail}
	}
	return &doc.Flow, nil
}

// The reference records the CE rejection but not its text; this message is
// windmill-lite's.
const wsErrorHandlerMutedDetail = "ws_error_handler_muted: true is an EE setting, which Windmill CE rejects"

type flowDocument struct {
	Flow
	wsErrorHandlerMuted *bool
}

var flowFields = []field[flowDocument]{
	optStringField("summary", func(f *flowDocument) **string { return &f.Summary }),
	optStringField("description", func(f *flowDocument) **string { return &f.Description }),
	{name: "value", required: true, decode: func(_ *decoder, n *node, f *flowDocument) (err error) {
		f.Value, err = decodeFlowValue(n)
		return err
	}},
	optRawField("schema", func(f *flowDocument) *json.RawMessage { return &f.Schema }),
	optStringField("on_behalf_of_email", func(f *flowDocument) **string { return &f.OnBehalfOfEmail }),
	optIntField("timeout", func(f *flowDocument) **int32 { return &f.Timeout }),
	optBoolField("ws_error_handler_muted", func(f *flowDocument) **bool { return &f.wsErrorHandlerMuted }),
}
