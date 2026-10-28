package openflow

import "encoding/json"

// Module values (OF-9) are decoded in two steps, as Windmill's shared value
// decoder does (OF-V1): every recognised field of every kind is decoded and
// type-checked first, then `type` selects the kind and the fields it keeps.
// A field the kind does not use can therefore still reject the value. Every
// field of this table is optional, so null counts as absent; each kind
// checks its required fields afterwards.

type valueFields struct {
	typ                    *string
	content                *string
	language               *Language
	inputTransforms        Transforms
	lock                   *string
	path                   *string
	tag                    *string
	isTrigger              *bool
	assets                 []json.RawMessage
	customConcurrencyKey   *string
	concurrentLimit        *int32
	concurrencyTimeWindowS *int32
	hash                   *ScriptHash
	tagOverride            *string
	passFlowInputDirectly  *bool
	iterator               *InputTransform
	modules                []Module
	skipFailures           *bool
	parallel               *bool
	parallelism            *InputTransform
	squash                 *bool
	branches               []Branch
	defaults               []Module
	tools                  []AgentTool
	omitOutput             *bool

	hasModules, hasBranches, hasDefault, hasTools bool
}

// valueKinds are the kinds Windmill's error message lists. The internal
// `flowscript` is accepted too but not listed.
var valueKinds = []string{"script", "flow", "forloopflow", "whileloopflow", "branchone", "branchall", "rawscript", "identity", "aiagent"}

var valueFieldTable = []field[valueFields]{
	optStringField("type", func(v *valueFields) **string { return &v.typ }),
	optStringField("content", func(v *valueFields) **string { return &v.content }),
	{name: "language", decode: func(_ *decoder, n *node, v *valueFields) (err error) {
		v.language, err = optional(n, decodeLanguage)
		return err
	}},
	{name: "input_transforms", alias: "input_transform", decode: func(_ *decoder, n *node, v *valueFields) (err error) {
		if n.kind != kindNull {
			v.inputTransforms, err = decodeTransforms(n)
		}
		return err
	}},
	optStringField("lock", func(v *valueFields) **string { return &v.lock }),
	optStringField("path", func(v *valueFields) **string { return &v.path }),
	optStringField("tag", func(v *valueFields) **string { return &v.tag }),
	optBoolField("is_trigger", func(v *valueFields) **bool { return &v.isTrigger }),
	// Assets are not executed; they are type-checked as a list and kept
	// verbatim.
	{name: "assets", decode: func(_ *decoder, n *node, v *valueFields) error {
		switch n.kind {
		case kindNull:
			return nil
		case kindArray:
			v.assets = make([]json.RawMessage, 0, len(n.elems))
			for _, e := range n.elems {
				v.assets = append(v.assets, raw(e))
			}
			return nil
		}
		return invalidType(n, "a sequence")
	}},
	optStringField("custom_concurrency_key", func(v *valueFields) **string { return &v.customConcurrencyKey }),
	optIntField("concurrent_limit", func(v *valueFields) **int32 { return &v.concurrentLimit }),
	optIntField("concurrency_time_window_s", func(v *valueFields) **int32 { return &v.concurrencyTimeWindowS }),
	{name: "hash", decode: func(_ *decoder, n *node, v *valueFields) (err error) {
		v.hash, err = optional(n, decodeScriptHash)
		return err
	}},
	optStringField("tag_override", func(v *valueFields) **string { return &v.tagOverride }),
	optBoolField("pass_flow_input_directly", func(v *valueFields) **bool { return &v.passFlowInputDirectly }),
	{name: "iterator", decode: func(_ *decoder, n *node, v *valueFields) (err error) {
		v.iterator, err = optional(n, decodeTransform)
		return err
	}},
	{name: "modules", decode: func(d *decoder, n *node, v *valueFields) (err error) {
		if n.kind != kindNull {
			v.modules, err = decodeEnvelopes(d, n)
			v.hasModules = true
		}
		return err
	}},
	optBoolField("skip_failures", func(v *valueFields) **bool { return &v.skipFailures }),
	optBoolField("parallel", func(v *valueFields) **bool { return &v.parallel }),
	{name: "parallelism", decode: func(_ *decoder, n *node, v *valueFields) (err error) {
		v.parallelism, err = decodeRestricted[uint16](n)
		return err
	}},
	optBoolField("squash", func(v *valueFields) **bool { return &v.squash }),
	{name: "branches", decode: func(d *decoder, n *node, v *valueFields) (err error) {
		if n.kind != kindNull {
			v.branches, err = decodeBranches(d, n)
			v.hasBranches = true
		}
		return err
	}},
	{name: "default", decode: func(d *decoder, n *node, v *valueFields) (err error) {
		if n.kind != kindNull {
			v.defaults, err = decodeEnvelopes(d, n)
			v.hasDefault = true
		}
		return err
	}},
	{name: "tools", decode: func(d *decoder, n *node, v *valueFields) (err error) {
		if n.kind != kindNull {
			v.tools, err = decodeTools(d, n)
			v.hasTools = true
		}
		return err
	}},
	optBoolField("omit_output_from_conversation", func(v *valueFields) **bool { return &v.omitOutput }),
}

func decodeLanguage(n *node) (Language, error) {
	s, err := decodeString(n)
	if err != nil {
		return "", err
	}
	l, ok := parseLanguage(s)
	if !ok {
		names := make([]string, len(Languages))
		for i, l := range Languages {
			names[i] = string(l)
		}
		return "", unknownVariant(n, s, names)
	}
	return l, nil
}

func decodeScriptHash(n *node) (ScriptHash, error) {
	s, err := decodeString(n)
	if err != nil {
		return 0, err
	}
	h, err := ParseScriptHash(s)
	if err != nil {
		return 0, &decodeError{n, err.Error()}
	}
	return h, nil
}

// decodeValue decodes a module value. The envelopes of body modules are
// decoded with it; their values are left for the traversal.
func decodeValue(d *decoder, n *node) (Value, error) {
	var f valueFields
	if err := decodeStruct(d, n, "a flow module value", valueFieldTable, &f); err != nil {
		return nil, err
	}
	if f.typ == nil {
		return nil, missingField(n, "type")
	}
	transforms := f.inputTransforms
	if transforms == nil {
		transforms = Transforms{}
	}
	switch *f.typ {
	case "rawscript":
		if f.content == nil {
			return nil, missingField(n, "content")
		}
		if f.language == nil {
			return nil, missingField(n, "language")
		}
		return &RawScript{
			Content: *f.content, Language: *f.language, InputTransforms: transforms,
			Lock: f.lock, Path: f.path, Tag: f.tag, IsTrigger: f.isTrigger, Assets: f.assets,
			CustomConcurrencyKey: f.customConcurrencyKey, ConcurrentLimit: f.concurrentLimit,
			ConcurrencyTimeWindowS: f.concurrencyTimeWindowS,
		}, nil
	case "script":
		if f.path == nil {
			return nil, missingField(n, "path")
		}
		return &PathScript{
			Path: *f.path, InputTransforms: transforms, Hash: f.hash, TagOverride: f.tagOverride,
			IsTrigger: f.isTrigger, PassFlowInputDirectly: f.passFlowInputDirectly,
		}, nil
	case "flow":
		if f.path == nil {
			return nil, missingField(n, "path")
		}
		return &PathFlow{Path: *f.path, InputTransforms: transforms, PassFlowInputDirectly: f.passFlowInputDirectly}, nil
	case "forloopflow":
		if f.iterator == nil {
			return nil, missingField(n, "iterator")
		}
		if !f.hasModules {
			return nil, missingField(n, "modules")
		}
		return &ForLoop{
			Iterator: *f.iterator, Modules: f.modules, SkipFailures: f.skipFailures,
			Parallel: f.parallel != nil && *f.parallel, Parallelism: f.parallelism, Squash: f.squash,
		}, nil
	case "whileloopflow":
		if !f.hasModules {
			return nil, missingField(n, "modules")
		}
		return &WhileLoop{Modules: f.modules, SkipFailures: f.skipFailures != nil && *f.skipFailures, Squash: f.squash}, nil
	case "branchone":
		if !f.hasBranches {
			return nil, missingField(n, "branches")
		}
		if !f.hasDefault {
			return nil, missingField(n, "default")
		}
		return &BranchOne{Branches: f.branches, Default: f.defaults}, nil
	case "branchall":
		if !f.hasBranches {
			return nil, missingField(n, "branches")
		}
		return &BranchAll{Branches: f.branches, Parallel: f.parallel == nil || *f.parallel}, nil
	case "identity":
		return &Identity{}, nil
	case "aiagent":
		if !f.hasTools {
			return nil, missingField(n, "tools")
		}
		return &AIAgent{
			Tools: f.tools, InputTransforms: transforms, Tag: f.tag,
			OmitOutputFromConversation: f.omitOutput != nil && *f.omitOutput,
		}, nil
	case "flowscript":
		return &FlowScript{Raw: raw(n)}, nil
	}
	typ, _ := soleMember(n, "type")
	return nil, unknownVariant(typ, *f.typ, valueKinds)
}

// Branches are typed structs with plain defaults: `expr` "", `skip_failure`
// true and `parallel` true. Null is rejected for all three (OF-6, OF-V1).

var branchFields = []field[Branch]{
	optStringField("summary", func(b *Branch) **string { return &b.Summary }),
	stringField("expr", false, func(b *Branch) *string { return &b.Expr }),
	{name: "modules", required: true, decode: func(d *decoder, n *node, b *Branch) (err error) {
		b.Modules, err = decodeEnvelopes(d, n)
		return err
	}},
	{name: "skip_failure", decode: func(_ *decoder, n *node, b *Branch) error {
		v, err := decodeBool(n)
		b.SkipFailure = &v
		return err
	}},
	boolField("parallel", false, func(b *Branch) *bool { return &b.Parallel }),
}

func decodeBranches(d *decoder, n *node) ([]Branch, error) {
	if n.kind != kindArray {
		return nil, invalidType(n, "a sequence")
	}
	out := make([]Branch, 0, len(n.elems))
	for _, e := range n.elems {
		b := Branch{Parallel: true}
		if err := decodeStruct(d, e, "struct Branch", branchFields, &b); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

// AI-agent tools (out of scope; decoded so that acceptance matches). A tool
// value is tagged by `tool_type`; an untagged value is a legacy module
// value. Module values of tools are decoded in the traversal, like body
// modules.

var agentToolFields = []field[AgentTool]{
	stringField("id", true, func(t *AgentTool) *string { return &t.ID }),
	optStringField("summary", func(t *AgentTool) **string { return &t.Summary }),
	optStringField("description", func(t *AgentTool) **string { return &t.Description }),
	{name: "value", required: true, decode: func(_ *decoder, n *node, t *AgentTool) (err error) {
		t.Value, err = decodeToolValue(n)
		return err
	}},
}

var toolTypes = []string{"flowmodule", "mcp", "websearch"}

var mcpToolFields = []field[MCPTool]{
	{name: "tool_type", decode: func(_ *decoder, n *node, _ *MCPTool) error {
		_, err := decodeString(n)
		return err
	}},
	stringField("resource_path", true, func(t *MCPTool) *string { return &t.ResourcePath }),
	optStringsField("include_tools", func(t *MCPTool) *[]string { return &t.IncludeTools }),
	optStringsField("exclude_tools", func(t *MCPTool) *[]string { return &t.ExcludeTools }),
}

func decodeTools(d *decoder, n *node) ([]AgentTool, error) {
	if n.kind != kindArray {
		return nil, invalidType(n, "a sequence")
	}
	out := make([]AgentTool, 0, len(n.elems))
	for _, e := range n.elems {
		var t AgentTool
		if err := decodeStruct(d, e, "struct AgentTool", agentToolFields, &t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func decodeToolValue(n *node) (ToolValue, error) {
	if n.kind != kindObject {
		return nil, invalidType(n, "a tool value object")
	}
	tt, err := soleMember(n, "tool_type")
	if err != nil {
		return nil, err
	}
	if tt == nil || tt.kind == kindNull {
		return &ModuleTool{raw: n}, nil
	}
	s, err := decodeString(tt)
	if err != nil {
		return nil, err
	}
	switch s {
	case "flowmodule":
		return &ModuleTool{raw: n}, nil
	case "mcp":
		var t MCPTool
		if err := decodeStruct(nil, n, "struct McpToolValue", mcpToolFields, &t); err != nil {
			return nil, err
		}
		return &t, nil
	case "websearch":
		return &WebsearchTool{}, nil
	}
	return nil, unknownVariant(tt, s, toolTypes)
}
