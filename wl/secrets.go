package wl

import (
	"bytes"
	"errors"
	"maps"
	"net/url"
	"slices"
	"strings"

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
			p.Method, p.Message = secrets.Mask(p.Method), secrets.Mask(p.Message)
			p.Path, p.Query = maskPath(secrets, p.Path), maskQuery(secrets, p.Query)
			c.PlatformCalls[i] = p
		}
	}
	return &c
}

// maskPath returns an escaped URL path with each secret masked in its
// segments as decoded, so that a secret that a client escaped is masked
// too. A segment that holds a secret is escaped again around the mask; the
// others keep their bytes.
func maskPath(secrets *harness.Secrets, path string) string {
	segs := strings.Split(path, "/")
	changed := false
	for i, seg := range segs {
		if m := maskEscaped(secrets, seg, url.PathUnescape, url.PathEscape); m != seg {
			segs[i], changed = m, true
		}
	}
	if !changed {
		return path
	}
	return strings.Join(segs, "/")
}

// maskQuery returns a raw query with each secret masked in its names and
// values as decoded, each that holds one escaped again around the mask;
// the others keep their bytes, and the pairs their order.
func maskQuery(secrets *harness.Secrets, query string) string {
	pairs := strings.Split(query, "&")
	changed := false
	for i, pair := range pairs {
		name, value, eq := strings.Cut(pair, "=")
		mn := maskEscaped(secrets, name, url.QueryUnescape, url.QueryEscape)
		mv := maskEscaped(secrets, value, url.QueryUnescape, url.QueryEscape)
		if mn != name || mv != value {
			changed = true
			pairs[i] = mn
			if eq {
				pairs[i] += "=" + mv
			}
		}
	}
	if !changed {
		return query
	}
	return strings.Join(pairs, "&")
}

// maskEscaped masks the secrets in s as it reads raw and decoded with
// unescape, and returns s unchanged when neither holds one. A part that
// does not decode is masked as it is.
func maskEscaped(secrets *harness.Secrets, s string, unescape func(string) (string, error), escape func(string) string) string {
	if m := secrets.Mask(s); m != s {
		s = m
	}
	dec, err := unescape(s)
	if err != nil {
		return s
	}
	if m := secrets.Mask(dec); m != dec {
		return escape(m)
	}
	return s
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

// maskWarnings returns ws with each secret masked in their codes,
// messages and the keys of their counts, in a new slice when secrets is not
// nil.
func maskWarnings(secrets *harness.Secrets, ws []Warning) []Warning {
	if secrets == nil || ws == nil {
		return ws
	}
	out := make([]Warning, len(ws))
	for i, w := range ws {
		w.Code, w.Message = secrets.Mask(w.Code), secrets.Mask(w.Message)
		w.Left, w.Untracked = maskCounts(secrets, w.Left), maskCounts(secrets, w.Untracked)
		out[i] = w
	}
	return out
}

// maskCounts returns counts with each secret masked in its keys, in a new
// map when a key holds one. Keys that mask to one key, such as two
// secrets, add their counts there.
func maskCounts(secrets *harness.Secrets, counts map[string]int) map[string]int {
	if !slices.ContainsFunc(slices.Collect(maps.Keys(counts)), func(k string) bool { return secrets.Mask(k) != k }) {
		return counts
	}
	out := make(map[string]int, len(counts))
	for k, n := range counts {
		out[secrets.Mask(k)] += n
	}
	return out
}

// maskError returns err, or, when its text holds a secret, an error whose
// text has it masked. A *engine.StepLatch or *engine.PluginError keeps its
// type, with its message masked (a *harness.HostCallError arrives masked
// already, so it is returned as it is). Any other error, such as a Go
// hook's own type latched through RunHandle.Latch, comes back wrapped in
// one that errors.Is still matches as err, and that errors.As does not see
// through, since the error it wraps holds the secret in its text.
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
