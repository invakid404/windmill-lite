package engine

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"slices"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/ir"
	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/internal/platform"
)

// The cache_ttl result cache (milestone N4, FX-40): before a job with a
// cache_ttl runs, the engine looks its key up in the result cache of the
// run's platform services (platform.ResultCache, ADR 0007, D4); a hit
// completes the job successfully at once with the cached result, and a job
// that succeeds stores its result until now plus the TTL, on the run's
// clock (H10). A step's job is cached as FX-40's matrix says (leafCache), a
// whole flow's by its flow-level cache_ttl, for the root flow and a subflow
// by path (frame.cacheTTL), and a script root's run as a script job by its
// own (RunScript).

// The job kinds a cache key starts with, as Windmill names the jobs: an
// inline step of a deployed flow runs as a flowscript job, whose runnable
// is the flow node holding its code; a path script as a script job; a flow
// as a flow job (FX-40).
const (
	cacheKindFlowScript = "flowscript"
	cacheKindScript     = "script"
	cacheKindFlow       = "flow"
)

// cacheKey returns the key of a job of kind whose runnable is id, with
// args, a JSON object: a SHA-256 over the bundle's digest, the kind, the
// runnable and each argument in sorted key order, as its name and its
// stored text (FX-40). The runnable is never a path that differs between
// the jobs that share an entry: an inline step's inventory id, which its
// collapsed iterations share (ir.Script.ID leaves iteration segments out),
// a path script's path or a flow's path, as Windmill keys by a runnable id
// and never by a path when there is one. The bundle's digest stands for
// the deployed version Windmill's runnable ids belong to, so an entry
// never crosses bundles in a store a host shares (D4). false when args is
// not an object or holds a value jsonb refuses, which no job could store.
func (e *Engine) cacheKey(kind, id string, args jsonval.Value) (string, bool) {
	ms, ok := members(args.Producer())
	if !ok {
		return "", false
	}
	type arg struct {
		name  string
		value []byte
	}
	byName := map[string][]byte{}
	for _, m := range ms {
		v, err := jsonval.FromProducer(m.value).Stored()
		if err != nil {
			return "", false
		}
		byName[m.key] = v
	}
	sorted := make([]arg, 0, len(byName))
	for name, v := range byName {
		sorted = append(sorted, arg{name, v})
	}
	slices.SortFunc(sorted, func(a, b arg) int {
		switch {
		case a.name < b.name:
			return -1
		case a.name > b.name:
			return 1
		}
		return 0
	})
	h := sha256.New()
	field := func(b []byte) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(b)))
		h.Write(n[:])
		h.Write(b)
	}
	field([]byte("windmill-lite result cache 1"))
	field([]byte(e.bundle))
	field([]byte(kind))
	field([]byte(id))
	for _, a := range sorted {
		field([]byte(a.name))
		field(a.value)
	}
	return hex.EncodeToString(h.Sum(nil)), true
}

// cacheEntry is a cached result as the engine encodes it for the store:
// its stored text, and the provenance the trace keeps for it (an origin
// for a value a flow ended with, such as an error stop's EarlyStopError,
// and the generated errors inside an aggregate).
type cacheEntry struct {
	Result json.RawMessage `json:"result"`
	Origin Origin          `json:"origin,omitempty"`
	Nested []string        `json:"nested,omitempty"`
}

// value is the entry's result.
func (c cacheEntry) value() jsonval.Value { return jsonval.FromProducer(c.Result) }

// cacheGet returns the entry stored under key that has not expired at now.
func (e *Engine) cacheGet(ctx context.Context, key string, now time.Time) (cacheEntry, bool, error) {
	b, ok, err := e.results.Get(ctx, key, now)
	if err != nil || !ok {
		return cacheEntry{}, false, err
	}
	var c cacheEntry
	if err := json.Unmarshal(b, &c); err != nil || len(c.Result) == 0 {
		// Not an entry the engine wrote: a miss, which the next success
		// replaces.
		return cacheEntry{}, false, nil
	}
	return c, true, nil
}

// cachePut stores v, of origin and nested, under key until now plus ttl.
// A value jsonb refuses is never stored, since no job completes with it
// (FX-48).
func (e *Engine) cachePut(ctx context.Context, key string, now time.Time, ttl time.Duration, v jsonval.Value, origin Origin, nested []string) error {
	stored, err := v.Stored()
	if err != nil {
		return nil
	}
	b, err := json.Marshal(cacheEntry{Result: stored, Origin: origin, Nested: nested})
	if err != nil {
		return err
	}
	return e.results.Put(ctx, key, b, now.Add(ttl))
}

// lookup is cacheGet for a run: on its clock, with a store's failure a
// miss and a warning in the trace.
func (r *run) lookup(ctx context.Context, key string) (cacheEntry, bool) {
	c, ok, err := r.e.cacheGet(ctx, key, r.clock.Now())
	if err != nil {
		r.cacheWarning("the result cache failed a lookup, which counts as a miss: " + err.Error())
	}
	return c, ok
}

// store is cachePut for a run: on its clock, with a store's failure a
// warning in the trace.
func (r *run) store(ctx context.Context, key string, ttl time.Duration, v jsonval.Value, origin Origin, nested []string) {
	if err := r.e.cachePut(ctx, key, r.clock.Now(), ttl, v, origin, nested); err != nil {
		r.cacheWarning("the result cache failed to store a result: " + err.Error())
	}
}

// cacheWarning records a failure of the run's result cache, which never
// fails the run (warning code result_cache_unavailable).
func (r *run) cacheWarning(msg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.trace.Warnings = append(r.trace.Warnings, Warning{Code: "result_cache_unavailable", Message: msg})
}

// leafCache returns the key under which module i's leaf job, of fr, with
// args, is cached and the TTL that applies, zero for none (FX-40's
// matrix): an inline step takes its module's cache_ttl, and in a
// collapsed iteration its loop's, which stands in for the module; a path
// script takes its own and drops its module's, collapsed or not. A
// cache_ttl of zero caches nothing.
func (r *run) leafCache(fr *frame, m *ir.Module, args jsonval.Value) (string, time.Duration) {
	s := r.e.prog.Scripts[m.Leaf.Script]
	if s == nil {
		return "", 0
	}
	kind, secs := cacheKindFlowScript, int64(0)
	switch {
	case s.Kind == ir.PathScript:
		kind = cacheKindScript
		if s.Metadata != nil && s.Metadata.CacheTTL != nil {
			secs = int64(*s.Metadata.CacheTTL)
		}
	case fr.collapsed != nil:
		if t := fr.collapsed.CacheTTL; t != nil {
			secs = int64(*t)
		}
	case m.CacheTTL != nil:
		secs = int64(*m.CacheTTL)
	}
	if secs <= 0 {
		return "", 0
	}
	key, ok := r.e.cacheKey(kind, s.ID, args)
	if !ok {
		return "", 0
	}
	return key, time.Duration(secs) * time.Second
}

// cachedJob completes module i of fr, a leaf whose job the result cache
// answers, at site (FX-40): the job completes successfully at once with
// the cached result, logging that it was skipped, without running, so
// nothing is invoked and no argument is resolved; the module's own
// stop_after_if then applies as after any success. The job has an id of
// its own, which nothing sees.
func (r *run) cachedJob(ctx context.Context, fr *frame, i int, site conformance.SitePath, o outcome, c cacheEntry) outcome {
	v := c.value()
	o.rec.Ran, o.rec.Job, o.rec.Cached = true, newUUID(), true
	r.ended(fr, &o)
	r.recordJob(fr, i, v)
	o.rec.Status, o.rec.Module, o.rec.Result, o.rec.Carried = conformance.StepSuccess, conformance.ModuleSuccess, v, v
	o.rec.ResultOrigin, o.rec.ResultNested, o.rec.CarriedNested = c.Origin, c.Nested, c.Nested
	o.carried, o.origin, o.nested = v, c.Origin, c.Nested
	return r.stop(ctx, fr, i, site, o)
}

// flowTTL is the TTL of a flow job of f, its flow-level cache_ttl: the
// root's, or a subflow's by path, whose step's own is dropped (FX-40);
// zero for none.
func flowTTL(f *ir.Flow) time.Duration {
	if f.CacheTTL == nil {
		return 0
	}
	return time.Duration(*f.CacheTTL) * time.Second
}

// resultCache is the result cache an engine with services p keeps its
// results in: the services', or one of its own when it has none (D4).
func resultCache(p *platform.Services) platform.ResultCache {
	if c := p.ResultCache(); c != nil {
		return c
	}
	return platform.NewMemoryResults()
}
