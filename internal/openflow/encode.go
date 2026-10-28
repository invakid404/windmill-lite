package openflow

import (
	"encoding/json"
	"strconv"
	"unicode/utf8"
)

// The decoded form is windmill-lite's JSON rendering of a decoded value: one
// object per type with the decoder's defaults written out, optional fields
// omitted when absent, static values and other verbatim JSON copied as
// written, maps in input order, and dropped fields (delete_after_use, the
// while-loop parallel fields) left out. It is not Windmill's stored full
// form, and it decodes back to an equal value. Conformance expectations
// address it with JSON Pointers.

// MarshalJSON renders the flow value in the decoded form.
func (v *FlowValue) MarshalJSON() ([]byte, error) {
	var e encoder
	e.flowValue(v)
	return e.buf, nil
}

// MarshalJSON renders the flow document in the decoded form.
func (f *Flow) MarshalJSON() ([]byte, error) {
	var e encoder
	e.object(func(o *object) {
		o.optString("summary", f.Summary)
		o.optString("description", f.Description)
		if f.Value != nil {
			o.key("value")
			e.flowValue(f.Value)
		}
		o.optRaw("schema", f.Schema)
		o.optString("on_behalf_of_email", f.OnBehalfOfEmail)
		optInt(o, "timeout", f.Timeout)
	})
	return e.buf, nil
}

type encoder struct {
	buf []byte
}

type object struct {
	e     *encoder
	count int
}

func (e *encoder) object(fill func(*object)) {
	e.buf = append(e.buf, '{')
	fill(&object{e: e})
	e.buf = append(e.buf, '}')
}

func (o *object) key(name string) {
	if o.count > 0 {
		o.e.buf = append(o.e.buf, ',')
	}
	o.count++
	o.e.string(name)
	o.e.buf = append(o.e.buf, ':')
}

func (o *object) string(name, s string) {
	o.key(name)
	o.e.string(s)
}

func (o *object) optString(name string, s *string) {
	if s != nil {
		o.string(name, *s)
	}
}

func (o *object) bool(name string, b bool) {
	o.key(name)
	o.e.buf = strconv.AppendBool(o.e.buf, b)
}

func (o *object) optBool(name string, b *bool) {
	if b != nil {
		o.bool(name, *b)
	}
}

func (o *object) optRaw(name string, r json.RawMessage) {
	if r != nil {
		o.key(name)
		o.e.buf = append(o.e.buf, r...)
	}
}

func (o *object) strings(name string, s []string) {
	if s == nil {
		return
	}
	o.key(name)
	o.e.list(len(s), func(i int) { o.e.string(s[i]) })
}

func optInt[I integer](o *object, name string, v *I) {
	if v != nil {
		o.key(name)
		o.e.buf = strconv.AppendInt(o.e.buf, int64(*v), 10)
	}
}

func intValue[I integer](o *object, name string, v I) {
	optInt(o, name, &v)
}

func (e *encoder) list(n int, item func(int)) {
	e.buf = append(e.buf, '[')
	for i := range n {
		if i > 0 {
			e.buf = append(e.buf, ',')
		}
		item(i)
	}
	e.buf = append(e.buf, ']')
}

// string writes s as a JSON string, escaping only what JSON requires.
func (e *encoder) string(s string) {
	const hex = "0123456789abcdef"
	e.buf = append(e.buf, '"')
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '"' || c == '\\':
			e.buf = append(e.buf, '\\', c)
		case c == '\n':
			e.buf = append(e.buf, '\\', 'n')
		case c == '\r':
			e.buf = append(e.buf, '\\', 'r')
		case c == '\t':
			e.buf = append(e.buf, '\\', 't')
		case c < 0x20:
			e.buf = append(e.buf, '\\', 'u', '0', '0', hex[c>>4], hex[c&0xf])
		case c < utf8.RuneSelf:
			e.buf = append(e.buf, c)
		default:
			_, size := utf8.DecodeRuneInString(s[i:])
			e.buf = append(e.buf, s[i:i+size]...)
			i += size
			continue
		}
		i++
	}
	e.buf = append(e.buf, '"')
}

func (e *encoder) flowValue(v *FlowValue) {
	e.object(func(o *object) {
		o.key("modules")
		e.modules(v.Modules)
		if v.FailureModule != nil {
			o.key("failure_module")
			e.module(v.FailureModule)
		}
		if v.PreprocessorModule != nil {
			o.key("preprocessor_module")
			e.module(v.PreprocessorModule)
		}
		o.bool("same_worker", v.SameWorker)
		o.bool("preserve_step_tags", v.PreserveStepTags)
		optInt(o, "concurrent_limit", v.ConcurrentLimit)
		o.optString("concurrency_key", v.ConcurrencyKey)
		optInt(o, "concurrency_time_window_s", v.ConcurrencyTimeWindowS)
		e.debouncingFields(o, &v.Debouncing)
		o.optString("skip_expr", v.SkipExpr)
		optInt(o, "cache_ttl", v.CacheTTL)
		o.optBool("cache_ignore_s3_path", v.CacheIgnoreS3Path)
		o.optString("early_return", v.EarlyReturn)
		optInt(o, "priority", v.Priority)
		o.optBool("chat_input_enabled", v.ChatInputEnabled)
		if v.FlowEnv != nil {
			o.key("flow_env")
			e.object(func(env *object) {
				for _, ev := range v.FlowEnv {
					env.optRaw(ev.Name, ev.Value)
				}
			})
		}
		optInt(o, "delete_after_secs", v.DeleteAfterSecs)
	})
}

func (e *encoder) debouncingFields(o *object, b *Debouncing) {
	o.optString("debounce_key", b.DebounceKey)
	optInt(o, "debounce_delay_s", b.DebounceDelayS)
	optInt(o, "max_total_debouncing_time", b.MaxTotalDebouncingTime)
	optInt(o, "max_total_debounces_amount", b.MaxTotalDebouncesAmount)
	o.strings("debounce_args_to_accumulate", b.DebounceArgsToAccumulate)
}

func (e *encoder) modules(ms []Module) {
	e.list(len(ms), func(i int) { e.module(&ms[i]) })
}

func (e *encoder) module(m *Module) {
	e.object(func(o *object) {
		o.string("id", m.ID)
		o.key("value")
		e.value(m.Value)
		o.optString("summary", m.Summary)
		e.stopAfterIf(o, "stop_after_if", m.StopAfterIf)
		e.stopAfterIf(o, "stop_after_all_iters_if", m.StopAfterAllItersIf)
		if m.SkipIf != nil {
			o.key("skip_if")
			e.object(func(s *object) { s.string("expr", m.SkipIf.Expr) })
		}
		e.optTransform(o, "sleep", m.Sleep)
		e.optTransform(o, "timeout", m.Timeout)
		if m.Retry != nil {
			o.key("retry")
			e.retry(m.Retry)
		}
		o.optBool("continue_on_error", m.ContinueOnError)
		if s := m.Suspend; s != nil {
			o.key("suspend")
			e.object(func(so *object) {
				optInt(so, "required_events", s.RequiredEvents)
				optInt(so, "timeout", s.Timeout)
				so.optRaw("resume_form", s.ResumeForm)
				so.optBool("user_auth_required", s.UserAuthRequired)
				e.optTransform(so, "user_groups_required", s.UserGroupsRequired)
				so.optBool("self_approval_disabled", s.SelfApprovalDisabled)
				so.optBool("hide_cancel", s.HideCancel)
				so.optBool("continue_on_disapprove_timeout", s.ContinueOnDisapproveTimeout)
			})
		}
		if m.Mock != nil {
			o.key("mock")
			e.object(func(mo *object) {
				mo.bool("enabled", m.Mock.Enabled)
				mo.optRaw("return_value", m.Mock.ReturnValue)
			})
		}
		optInt(o, "cache_ttl", m.CacheTTL)
		o.optBool("cache_ignore_s3_path", m.CacheIgnoreS3Path)
		optInt(o, "priority", m.Priority)
		optInt(o, "delete_after_secs", m.DeleteAfterSecs)
		o.optBool("apply_preprocessor", m.ApplyPreprocessor)
		o.optBool("pass_flow_input_directly", m.PassFlowInputDirectly)
		if m.Debouncing != nil {
			o.key("debouncing")
			e.object(func(do *object) { e.debouncingFields(do, m.Debouncing) })
		}
	})
}

func (e *encoder) stopAfterIf(o *object, name string, s *StopAfterIf) {
	if s == nil {
		return
	}
	o.key(name)
	e.object(func(so *object) {
		so.string("expr", s.Expr)
		so.bool("skip_if_stopped", s.SkipIfStopped)
		so.optString("error_message", s.ErrorMessage)
		so.optBool("error_include_result", s.ErrorIncludeResult)
	})
}

func (e *encoder) retry(r *Retry) {
	e.object(func(o *object) {
		if c := r.Constant; c != nil {
			o.key("constant")
			e.object(func(co *object) {
				intValue(co, "attempts", c.Attempts)
				intValue(co, "seconds", c.Seconds)
			})
		}
		if x := r.Exponential; x != nil {
			o.key("exponential")
			e.object(func(xo *object) {
				intValue(xo, "attempts", x.Attempts)
				intValue(xo, "multiplier", x.Multiplier)
				intValue(xo, "seconds", x.Seconds)
				optInt(xo, "random_factor", x.RandomFactor)
			})
		}
		if r.RetryIf != nil {
			o.key("retry_if")
			e.object(func(ro *object) { ro.string("expr", r.RetryIf.Expr) })
		}
	})
}

func (e *encoder) transform(t *InputTransform) {
	e.object(func(o *object) {
		o.string("type", string(t.Type))
		switch t.Type {
		case Static:
			o.optRaw("value", t.Value)
		case JavaScript:
			o.string("expr", t.Expr)
		}
	})
}

func (e *encoder) optTransform(o *object, name string, t *InputTransform) {
	if t != nil {
		o.key(name)
		e.transform(t)
	}
}

func (e *encoder) transforms(o *object, ts Transforms) {
	o.key("input_transforms")
	e.object(func(to *object) {
		for i := range ts {
			to.key(ts[i].Name)
			e.transform(&ts[i].Transform)
		}
	})
}

func (e *encoder) branches(bs []Branch) {
	e.list(len(bs), func(i int) {
		b := &bs[i]
		e.object(func(o *object) {
			o.optString("summary", b.Summary)
			o.string("expr", b.Expr)
			o.key("modules")
			e.modules(b.Modules)
			o.bool("skip_failure", b.EffectiveSkipFailure())
			o.bool("parallel", b.Parallel)
		})
	})
}

func (e *encoder) value(v Value) {
	if fs, ok := v.(*FlowScript); ok {
		e.buf = append(e.buf, fs.Raw...)
		return
	}
	e.object(func(o *object) {
		e.valueFields(o, v)
	})
}

// valueFields writes a module value's members, `type` first.
func (e *encoder) valueFields(o *object, v Value) {
	o.string("type", string(v.Kind()))
	switch v := v.(type) {
	case *RawScript:
		o.string("content", v.Content)
		o.string("language", string(v.Language))
		e.transforms(o, v.InputTransforms)
		o.optString("lock", v.Lock)
		o.optString("path", v.Path)
		o.optString("tag", v.Tag)
		o.optBool("is_trigger", v.IsTrigger)
		if v.Assets != nil {
			o.key("assets")
			e.list(len(v.Assets), func(i int) { e.buf = append(e.buf, v.Assets[i]...) })
		}
		o.optString("custom_concurrency_key", v.CustomConcurrencyKey)
		optInt(o, "concurrent_limit", v.ConcurrentLimit)
		optInt(o, "concurrency_time_window_s", v.ConcurrencyTimeWindowS)
	case *PathScript:
		o.string("path", v.Path)
		e.transforms(o, v.InputTransforms)
		if v.Hash != nil {
			o.string("hash", v.Hash.String())
		}
		o.optString("tag_override", v.TagOverride)
		o.optBool("is_trigger", v.IsTrigger)
		o.optBool("pass_flow_input_directly", v.PassFlowInputDirectly)
	case *PathFlow:
		o.string("path", v.Path)
		e.transforms(o, v.InputTransforms)
		o.optBool("pass_flow_input_directly", v.PassFlowInputDirectly)
	case *ForLoop:
		o.key("modules")
		e.modules(v.Modules)
		o.key("iterator")
		e.transform(&v.Iterator)
		o.bool("skip_failures", v.EffectiveSkipFailures())
		o.bool("parallel", v.Parallel)
		e.optTransform(o, "parallelism", v.Parallelism)
		o.optBool("squash", v.Squash)
	case *WhileLoop:
		o.key("modules")
		e.modules(v.Modules)
		o.bool("skip_failures", v.SkipFailures)
		o.optBool("squash", v.Squash)
	case *BranchOne:
		o.key("branches")
		e.branches(v.Branches)
		o.key("default")
		e.modules(v.Default)
	case *BranchAll:
		o.key("branches")
		e.branches(v.Branches)
		o.bool("parallel", v.Parallel)
	case *AIAgent:
		o.key("tools")
		e.list(len(v.Tools), func(i int) { e.tool(&v.Tools[i]) })
		e.transforms(o, v.InputTransforms)
		o.optString("tag", v.Tag)
		o.bool("omit_output_from_conversation", v.OmitOutputFromConversation)
	}
}

func (e *encoder) tool(t *AgentTool) {
	e.object(func(o *object) {
		o.string("id", t.ID)
		o.optString("summary", t.Summary)
		o.optString("description", t.Description)
		o.key("value")
		if mt, ok := t.Value.(*ModuleTool); ok {
			if fs, ok := mt.Value.(*FlowScript); ok {
				e.buf = append(e.buf, fs.Raw...)
				return
			}
		}
		e.object(func(vo *object) {
			vo.string("tool_type", t.Value.toolType())
			switch tv := t.Value.(type) {
			case *ModuleTool:
				e.valueFields(vo, tv.Value)
			case *MCPTool:
				vo.string("resource_path", tv.ResourcePath)
				vo.strings("include_tools", tv.IncludeTools)
				vo.strings("exclude_tools", tv.ExcludeTools)
			}
		})
	})
}
