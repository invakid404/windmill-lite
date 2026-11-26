package wl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"time"

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
			// The path and query as sent: masked as decoded, each span that
			// holds a secret escaped again around the mask.
			p.Path, p.Query = secrets.MaskURL(p.Path, false), secrets.MaskURL(p.Query, true)
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

// maskValue returns v with each secret masked (harness.Secrets.MaskJSON),
// or v itself when none holds one. A value reads two ways: as its producer
// wrote it, and as stored, the text that payloads and Stored give, which
// spells numbers in plain decimal, writes strings its own way and orders
// keys. When the stored text of the masked value still shows a secret, such
// as a secret of digits that the producer wrote with an exponent, the copy
// is that stored text, masked.
func maskValue(secrets *harness.Secrets, v jsonval.Value) jsonval.Value {
	if secrets == nil || v.IsZero() {
		return v
	}
	text := v.Producer()
	out := v
	if masked := secrets.MaskJSON(text); !bytes.Equal(masked, text) {
		out = jsonval.FromProducer(masked)
	}
	if stored, err := out.Stored(); err == nil {
		if masked := secrets.MaskJSON(stored); !bytes.Equal(masked, stored) {
			return jsonval.FromProducer(masked)
		}
	}
	return out
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

// secretResults is a host's result cache seen through a runtime with
// secret harness entries: it stores no value that holds a secret, raw or
// in the forms that Secrets masks, so no runtime that shares the cache,
// whose own secrets may differ, reads one from it. A result that holds a
// secret is therefore not cached, and its step runs again next time.
type secretResults struct {
	ResultCache
	secrets *harness.Secrets
}

// Put judges the result itself, decoded from the engine's entry, whatever
// its spelling there: the entry's own fields, such as the key "result",
// do not count. An entry it cannot decode is not stored.
func (c secretResults) Put(ctx context.Context, key string, value []byte, expires time.Time) error {
	var entry struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(value, &entry); err != nil || len(entry.Result) == 0 {
		return nil
	}
	if !bytes.Equal(c.secrets.MaskJSON(entry.Result), entry.Result) {
		return nil
	}
	return c.ResultCache.Put(ctx, key, value, expires)
}

// Get reads an entry as the store holds it, but misses one that does not
// list every error in its result (an earlier engine's, without
// "all_errors") when that result holds a secret: the run could not tell
// its errors, which it masks, from the host's data, which it keeps. The
// step then runs again. An entry whose result holds no secret is read as
// before.
func (c secretResults) Get(ctx context.Context, key string, now time.Time) ([]byte, bool, error) {
	value, ok, err := c.ResultCache.Get(ctx, key, now)
	if err != nil || !ok {
		return value, ok, err
	}
	var entry struct {
		Result    json.RawMessage `json:"result"`
		AllErrors bool            `json:"all_errors"`
	}
	if json.Unmarshal(value, &entry) == nil && !entry.AllErrors && !bytes.Equal(c.secrets.MaskJSON(entry.Result), entry.Result) {
		return nil, false, nil
	}
	return value, ok, err
}

// maskErrorParts returns a run's requested result with each secret masked
// in the parts of it that are errors, as the trace records them: the whole
// result when its origin says it is one, a cached result's included, or
// else each error inside it (Trace.ResultErrors), such as a tolerated
// branch's error inside a branchall's array. The other parts, the host's
// data, stay as they are, a copy of an error that a step returned among
// them.
func maskErrorParts(secrets *harness.Secrets, t *Trace, result jsonval.Value) jsonval.Value {
	if secrets == nil || result.IsZero() {
		return result
	}
	pointers := t.ResultErrors
	if t.ResultOrigin != engine.OriginNone {
		pointers = []string{""}
	}
	// The result is in stored form already (Trace.Result), the text the host
	// gets: its errors are masked as it reads.
	text := result.Producer()
	masked := secrets.MaskJSONAt(text, pointers)
	if bytes.Equal(masked, text) {
		return result
	}
	return jsonval.FromProducer(masked)
}

// maskedError is an error whose text has the runtime's secrets masked.
type maskedError struct {
	text string
	err  error
}

func (e *maskedError) Error() string { return e.text }

// Is reports whether the error it masks is target, or wraps it.
func (e *maskedError) Is(target error) bool { return errors.Is(e.err, target) }
