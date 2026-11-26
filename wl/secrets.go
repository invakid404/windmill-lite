package wl

import (
	"bytes"
	"errors"

	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/harness"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// The masking of a runtime's secret harness entries (EnvValue.Secret) in
// what leaves the engine besides a run's requested result: the copies of a
// step's arguments, results and late output in a trace and in the records
// hooks get, the run's warnings and the errors it latched. The harness
// processes' own output is masked in internal/harness (harness.Secrets).
// Every function here returns its input unchanged when secrets is nil.

// maskTrace returns a copy of t whose values, warnings, platform calls and
// latched error have each secret masked, or t itself when secrets is nil.
// The engine's trace stays as it was: Run's requested result comes from it.
func maskTrace(secrets *harness.Secrets, t *Trace) *Trace {
	if secrets == nil || t == nil {
		return t
	}
	c := *t
	c.Result = maskValue(secrets, t.Result)
	c.Latched = maskError(secrets, t.Latched)
	if t.Steps != nil {
		c.Steps = make([]StepRecord, len(t.Steps))
		for i, s := range t.Steps {
			c.Steps[i] = maskRecord(secrets, s)
		}
	}
	c.Warnings = maskWarnings(secrets, t.Warnings)
	if t.PlatformCalls != nil {
		c.PlatformCalls = make([]PlatformCall, len(t.PlatformCalls))
		for i, p := range t.PlatformCalls {
			p.Path, p.Query, p.Message = secrets.Mask(p.Path), secrets.Mask(p.Query), secrets.Mask(p.Message)
			c.PlatformCalls[i] = p
		}
	}
	return &c
}

// maskRecord returns rec with each secret masked in its arguments, its
// result, its carried value and its late output.
func maskRecord(secrets *harness.Secrets, rec StepRecord) StepRecord {
	if secrets == nil {
		return rec
	}
	rec.ArgsBeforeRewrite = maskValue(secrets, rec.ArgsBeforeRewrite)
	rec.RecordedArgs = maskValue(secrets, rec.RecordedArgs)
	rec.ResolvedArgs = maskValue(secrets, rec.ResolvedArgs)
	rec.Result = maskValue(secrets, rec.Result)
	rec.Carried = maskValue(secrets, rec.Carried)
	if rec.Late != nil {
		late := make([]engine.Late, len(rec.Late))
		for i, l := range rec.Late {
			l.Body = maskValue(secrets, l.Body)
			late[i] = l
		}
		rec.Late = late
	}
	return rec
}

// maskValue returns v with each secret masked in its strings, or v itself
// when none holds one (harness.Secrets.MaskJSON).
func maskValue(secrets *harness.Secrets, v jsonval.Value) jsonval.Value {
	if secrets == nil || v.IsZero() {
		return v
	}
	text := v.Producer()
	masked := secrets.MaskJSON(text)
	if bytes.Equal(masked, text) {
		return v
	}
	return jsonval.FromProducer(masked)
}

// maskWarnings returns ws with each secret masked in their codes and
// messages, in a new slice when secrets is not nil.
func maskWarnings(secrets *harness.Secrets, ws []Warning) []Warning {
	if secrets == nil || ws == nil {
		return ws
	}
	out := make([]Warning, len(ws))
	for i, w := range ws {
		w.Code, w.Message = secrets.Mask(w.Code), secrets.Mask(w.Message)
		out[i] = w
	}
	return out
}

// maskError returns err, or, when its text holds a secret, an error whose
// text has it masked. A latch of step code or of a host plugin keeps its
// type, with its message masked; any other error is wrapped in one that
// errors.Is still matches as err, and that does not unwrap to err, whose
// text holds the secret.
func maskError(secrets *harness.Secrets, err error) error {
	if secrets == nil || err == nil {
		return err
	}
	text := err.Error()
	masked := secrets.Mask(text)
	if masked == text {
		return err
	}
	switch e := err.(type) {
	case *engine.StepLatch:
		return &engine.StepLatch{Message: secrets.Mask(e.Message)}
	case *engine.PluginError:
		return &engine.PluginError{Message: secrets.Mask(e.Message)}
	}
	return &maskedError{text: masked, err: err}
}

// maskedError is an error whose text has the runtime's secrets masked.
type maskedError struct {
	text string
	err  error
}

func (e *maskedError) Error() string { return e.text }

// Is reports whether the error it masks is target, or wraps it.
func (e *maskedError) Is(target error) bool { return errors.Is(e.err, target) }
