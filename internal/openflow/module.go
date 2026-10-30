package openflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Module envelopes (OF-5) and the structs they hold.

var moduleFields = []field[Module]{
	{name: "id", decode: func(_ *decoder, n *node, m *Module) (err error) {
		m.ID, err = decodeString(n)
		m.IDDerived = false
		return err
	}},
	// The value is decoded later, in the module-value stage (OF-2).
	{name: "value", required: true, decode: func(_ *decoder, n *node, m *Module) error {
		m.raw = n
		return nil
	}},
	optStringField("summary", func(m *Module) **string { return &m.Summary }),
	{name: "stop_after_if", decode: func(d *decoder, n *node, m *Module) (err error) {
		m.StopAfterIf, err = optionalStruct(d, n, "struct StopAfterIf", stopAfterIfFields, nil)
		return err
	}},
	{name: "stop_after_all_iters_if", decode: func(d *decoder, n *node, m *Module) (err error) {
		m.StopAfterAllItersIf, err = optionalStruct(d, n, "struct StopAfterIf", stopAfterIfFields, nil)
		return err
	}},
	{name: "skip_if", decode: func(d *decoder, n *node, m *Module) (err error) {
		m.SkipIf, err = optionalStruct(d, n, "struct SkipIf", skipIfFields, nil)
		return err
	}},
	{name: "sleep", decode: func(_ *decoder, n *node, m *Module) (err error) {
		m.Sleep, err = optional(n, decodeTransform)
		return err
	}},
	{name: "timeout", decode: func(_ *decoder, n *node, m *Module) (err error) {
		m.Timeout, err = decodeRestricted[int32](n, false)
		return err
	}},
	{name: "retry", decode: func(d *decoder, n *node, m *Module) (err error) {
		m.Retry, err = optionalStruct(d, n, "struct Retry", retryFields, nil)
		return err
	}},
	optBoolField("continue_on_error", func(m *Module) **bool { return &m.ContinueOnError }),
	{name: "suspend", decode: func(d *decoder, n *node, m *Module) (err error) {
		m.Suspend, err = optionalStruct(d, n, "struct Suspend", suspendFields, nil)
		return err
	}},
	{name: "mock", decode: func(d *decoder, n *node, m *Module) (err error) {
		m.Mock, err = optionalStruct(d, n, "struct Mock", mockFields, nil)
		return err
	}},
	optIntField("cache_ttl", func(m *Module) **uint32 { return &m.CacheTTL }),
	optBoolField("cache_ignore_s3_path", func(m *Module) **bool { return &m.CacheIgnoreS3Path }),
	optIntField("priority", func(m *Module) **int16 { return &m.Priority }),
	// delete_after_use is decoded and dropped (OF-12).
	{name: "delete_after_use", decode: func(_ *decoder, n *node, _ *Module) error {
		_, err := optional(n, decodeBool)
		return err
	}},
	optIntField("delete_after_secs", func(m *Module) **int32 { return &m.DeleteAfterSecs }),
	optBoolField("apply_preprocessor", func(m *Module) **bool { return &m.ApplyPreprocessor }),
	optBoolField("pass_flow_input_directly", func(m *Module) **bool { return &m.PassFlowInputDirectly }),
	{name: "debouncing", decode: func(d *decoder, n *node, m *Module) (err error) {
		m.Debouncing, err = optionalStruct(d, n, "struct DebouncingSettings", debouncingFields, nil)
		return err
	}},
}

// optionalStruct decodes an optional struct-valued field; null is absent.
// init, when set, presets the defaults.
func optionalStruct[T any](d *decoder, n *node, expected string, fields []field[T], init func(*T)) (*T, error) {
	if n.kind == kindNull {
		return nil, nil
	}
	var v T
	if init != nil {
		init(&v)
	}
	if err := decodeStruct(d, n, expected, fields, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

var stopAfterIfFields = []field[StopAfterIf]{
	stringField("expr", true, func(s *StopAfterIf) *string { return &s.Expr }),
	boolField("skip_if_stopped", true, func(s *StopAfterIf) *bool { return &s.SkipIfStopped }),
	optStringField("error_message", func(s *StopAfterIf) **string { return &s.ErrorMessage }),
	optBoolField("error_include_result", func(s *StopAfterIf) **bool { return &s.ErrorIncludeResult }),
}

var skipIfFields = []field[SkipIf]{
	stringField("expr", true, func(s *SkipIf) *string { return &s.Expr }),
}

var retryFields = []field[Retry]{
	{name: "constant", decode: func(d *decoder, n *node, r *Retry) (err error) {
		r.Constant, err = optionalStruct(d, n, "struct ConstantDelay", constantRetryFields, nil)
		return err
	}},
	{name: "exponential", decode: func(d *decoder, n *node, r *Retry) (err error) {
		r.Exponential, err = optionalStruct(d, n, "struct ExponentialDelay", exponentialRetryFields,
			func(e *ExponentialRetry) { e.Multiplier = 1 })
		return err
	}},
	{name: "retry_if", decode: func(d *decoder, n *node, r *Retry) (err error) {
		r.RetryIf, err = optionalStruct(d, n, "struct RetryIf", retryIfFields, nil)
		return err
	}},
}

var constantRetryFields = []field[ConstantRetry]{
	intField("attempts", func(c *ConstantRetry) *uint32 { return &c.Attempts }),
	intField("seconds", func(c *ConstantRetry) *uint16 { return &c.Seconds }),
}

var exponentialRetryFields = []field[ExponentialRetry]{
	intField("attempts", func(e *ExponentialRetry) *uint32 { return &e.Attempts }),
	intField("multiplier", func(e *ExponentialRetry) *uint16 { return &e.Multiplier }),
	intField("seconds", func(e *ExponentialRetry) *uint16 { return &e.Seconds }),
	optIntField("random_factor", func(e *ExponentialRetry) **int8 { return &e.RandomFactor }),
}

var retryIfFields = []field[RetryIf]{
	stringField("expr", true, func(r *RetryIf) *string { return &r.Expr }),
}

var suspendFields = []field[Suspend]{
	optIntField("required_events", func(s *Suspend) **uint32 { return &s.RequiredEvents }),
	optIntField("timeout", func(s *Suspend) **uint32 { return &s.Timeout }),
	optRawField("resume_form", func(s *Suspend) *json.RawMessage { return &s.ResumeForm }),
	optBoolField("user_auth_required", func(s *Suspend) **bool { return &s.UserAuthRequired }),
	{name: "user_groups_required", decode: func(_ *decoder, n *node, s *Suspend) (err error) {
		s.UserGroupsRequired, err = optional(n, decodeTransform)
		return err
	}},
	optBoolField("self_approval_disabled", func(s *Suspend) **bool { return &s.SelfApprovalDisabled }),
	optBoolField("hide_cancel", func(s *Suspend) **bool { return &s.HideCancel }),
	optBoolField("continue_on_disapprove_timeout", func(s *Suspend) **bool { return &s.ContinueOnDisapproveTimeout }),
}

var mockFields = []field[Mock]{
	boolField("enabled", true, func(m *Mock) *bool { return &m.Enabled }),
	optRawField("return_value", func(m *Mock) *json.RawMessage { return &m.ReturnValue }),
}

// debouncingFields serve the envelope's `debouncing` object. The integer
// widths are not in the reference; they are decoded as i64.
var debouncingFields = []field[Debouncing]{
	{name: "debounce_key", alias: "custom_debounce_key", decode: func(_ *decoder, n *node, b *Debouncing) (err error) {
		b.DebounceKey, err = optional(n, decodeString)
		return err
	}},
	optIntField("debounce_delay_s", func(b *Debouncing) **int64 { return &b.DebounceDelayS }),
	optIntField("max_total_debouncing_time", func(b *Debouncing) **int64 { return &b.MaxTotalDebouncingTime }),
	optIntField("max_total_debounces_amount", func(b *Debouncing) **int64 { return &b.MaxTotalDebouncesAmount }),
	optStringsField("debounce_args_to_accumulate", func(b *Debouncing) *[]string { return &b.DebounceArgsToAccumulate }),
}

// decodeEnvelope decodes one module envelope; its value stays raw.
func decodeEnvelope(d *decoder, n *node) (Module, error) {
	m := Module{IDDerived: true, at: n}
	if err := decodeStruct(d, n, "struct FlowModule", moduleFields, &m); err != nil {
		return Module{}, err
	}
	if m.IDDerived {
		m.ID = derivedID(pointer(d.value, n))
	}
	return m, nil
}

func decodeEnvelopes(d *decoder, n *node) ([]Module, error) {
	if n.kind != kindArray {
		return nil, invalidType(n, "a sequence")
	}
	out := make([]Module, 0, len(n.elems))
	for _, e := range n.elems {
		m, err := decodeEnvelope(d, e)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// derivedID is the id of a module that has none (deviation D6): `wl` and
// the first 8 hex digits of the SHA-256 of the module's position, its JSON
// Pointer within the flow value. Windmill generates 6 random alphanumerics
// instead (OF-5).
func derivedID(position string) string {
	sum := sha256.Sum256([]byte(position))
	return "wl" + hex.EncodeToString(sum[:4])
}
