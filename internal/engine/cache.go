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

// jobTTL is the cache_ttl a job has, in seconds. Any job that has one
// looks the cache up, a TTL of zero or less included, since a lookup tests
// only the stored entry's own expiry: such a job hits an entry that a job
// with a positive TTL stored under the same key. Only a positive TTL
// stores, because an entry stored with zero has already expired ([FX-40]).
//
// [FX-40]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/execution.md#fx-40
type jobTTL struct {
	set  bool
	secs int64
}

// ttlOf is the jobTTL of a cache_ttl field, unset when the field is
// absent.
func ttlOf[T uint32 | int32](t *T) jobTTL {
	if t == nil {
		return jobTTL{}
	}
	return jobTTL{set: true, secs: int64(*t)}
}

// stores reports whether a job with the TTL stores its result.
func (t jobTTL) stores() bool { return t.secs > 0 }

// duration is the TTL as a duration.
func (t jobTTL) duration() time.Duration { return time.Duration(t.secs) * time.Second }

// isTrue reports whether an optional flag is set to true.
func isTrue(b *bool) bool { return b != nil && *b }

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
//
// An argument whose whole value is an S3 object record, and whose object
// the services' object store holds, is hashed with that object's ETag
// after its name (objectETag), and its stored text follows unless
// ignoreS3Path, the job's `cache_ignore_s3_path`, is set. So new content
// under the same object key misses, and with the flag two object keys
// that hold the same content share an entry. Without an ETag (no object
// store, no such object, a failed read) the argument is hashed as its text
// alone and ignoreS3Path has no effect, as in a Windmill workspace without
// object storage ([FX-40]).
//
// [FX-40]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/execution.md#fx-40
func (e *Engine) cacheKey(ctx context.Context, kind, id string, args jsonval.Value, ignoreS3Path bool) (string, bool) {
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
		etag, ok := e.objectETag(ctx, a.value)
		if !ok {
			field(a.value)
			continue
		}
		// The marker cannot start a JSON text, so an ETag's field never
		// reads as an argument's text. The runnable, which the key holds,
		// decides ignoreS3Path, so jobs that share a key agree on which
		// fields follow.
		field([]byte("\x00etag:" + etag))
		if !ignoreS3Path {
			field(a.value)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), true
}

// objectETag returns the ETag of the object that stored, an argument's
// stored text, names, when its whole value is an S3 object record: a JSON
// object with a string `s3` member, the object's key, and optional
// `storage`, `filename` and `presigned` members, each a string or null.
// Other members are ignored, and a value nested inside the argument is not
// examined. The object is looked up in the services' object store, in the
// storage that `storage` names, else the default one. Its ETag is the
// SHA-256 of its content, which changes with the content, as an S3 ETag
// does. false when the value is no such record, there is no object store,
// or the object is missing or its read fails: none of them gives an ETag
// ([FX-40]).
//
// Each call reads the whole object, since platform.ObjectStore has no
// metadata read, so a job whose arguments name large objects pays a read
// of each at its lookup, and a flow job a second one at its store. The
// reads are not shared: Windmill fetches a flow's ETags again at its
// completion, and an object may have new content by then.
//
// [FX-40]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/execution.md#fx-40
func (e *Engine) objectETag(ctx context.Context, stored []byte) (string, bool) {
	if e.platform == nil || e.platform.Objects == nil {
		return "", false
	}
	ms, ok := members(stored)
	if !ok {
		return "", false
	}
	var key *string
	storage := platform.DefaultStorage
	for _, m := range ms {
		switch m.key {
		case "s3", "storage", "filename", "presigned":
		default:
			continue
		}
		var s *string
		if err := json.Unmarshal(m.value, &s); err != nil {
			return "", false
		}
		switch {
		case m.key == "s3":
			key = s
		case m.key == "storage" && s != nil:
			storage = *s
		}
	}
	if key == nil {
		return "", false
	}
	o, found, err := e.platform.Objects.Get(ctx, storage, *key)
	if err != nil || !found {
		return "", false
	}
	sum := sha256.Sum256(o.Content)
	return hex.EncodeToString(sum[:]), true
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
// Expiry is in whole seconds: an entry is valid while now, rounded down to
// the second, is before its expiry (cachePut, [FX-40]).
//
// [FX-40]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/execution.md#fx-40
func (e *Engine) cacheGet(ctx context.Context, key string, now time.Time) (cacheEntry, bool, error) {
	b, ok, err := e.results.Get(ctx, key, now.Truncate(time.Second))
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

// cachePut stores v, of origin and nested, under key until now, rounded
// down to the second, plus ttl, as Windmill writes an entry's expiry in
// whole Unix seconds ([FX-40]). A value jsonb refuses is never stored,
// since no job completes with it (FX-48).
//
// [FX-40]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/execution.md#fx-40
func (e *Engine) cachePut(ctx context.Context, key string, now time.Time, ttl time.Duration, v jsonval.Value, origin Origin, nested []string) error {
	stored, err := v.Stored()
	if err != nil {
		return nil
	}
	b, err := json.Marshal(cacheEntry{Result: stored, Origin: origin, Nested: nested})
	if err != nil {
		return err
	}
	return e.results.Put(ctx, key, b, now.Truncate(time.Second).Add(ttl))
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
// args, is looked up in the result cache, and its TTL; the key is "" when
// the job has no cache_ttl ([FX-40]'s matrix). An inline step takes its
// module's cache_ttl and cache_ignore_s3_path, and in a collapsed
// iteration its loop's, which stands in for the module. A path script
// takes its own cache_ttl and drops its module's, collapsed or not, and
// its module's cache_ignore_s3_path with it: a script's metadata in a sync
// folder carries no such flag, so its S3 objects are hashed with their
// text.
//
// [FX-40]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/execution.md#fx-40
func (r *run) leafCache(ctx context.Context, fr *frame, m *ir.Module, args jsonval.Value) (string, jobTTL) {
	s := r.e.prog.Scripts[m.Leaf.Script]
	if s == nil {
		return "", jobTTL{}
	}
	kind, ttl, ignoreS3Path := cacheKindFlowScript, jobTTL{}, false
	switch {
	case s.Kind == ir.PathScript:
		kind = cacheKindScript
		if s.Metadata != nil {
			ttl = ttlOf(s.Metadata.CacheTTL)
		}
	case fr.collapsed != nil:
		ttl, ignoreS3Path = ttlOf(fr.collapsed.CacheTTL), isTrue(fr.collapsed.CacheIgnoreS3Path)
	default:
		ttl, ignoreS3Path = ttlOf(m.CacheTTL), isTrue(m.CacheIgnoreS3Path)
	}
	if !ttl.set {
		return "", ttl
	}
	key, ok := r.e.cacheKey(ctx, kind, s.ID, args, ignoreS3Path)
	if !ok {
		return "", jobTTL{}
	}
	return key, ttl
}

// cachedJob completes module i of fr, a leaf whose job the result cache
// answers, at site (FX-40): the job completes successfully at once with
// the cached result, logging that it was skipped, without running, so
// nothing is invoked and no argument is resolved; the module's own
// stop_after_if then applies as after any success. The job has an id and
// an identity of its own, and the job registry knows it as it knows every
// job of the run's tree while its root lives (ADR 0007, PF3), so a root
// lookup by its id answers as for a job that ran.
func (r *run) cachedJob(ctx context.Context, fr *frame, i int, site conformance.SitePath, o outcome, c cacheEntry) outcome {
	v := c.value()
	o.rec.Ran, o.rec.Job, o.rec.Cached = true, newUUID(), true
	r.e.registerHit(r.jobEnv(fr, i, o.rec.Job))
	r.ended(fr, &o)
	r.recordJob(fr, i, v)
	o.rec.Status, o.rec.Module, o.rec.Result, o.rec.Carried = conformance.StepSuccess, conformance.ModuleSuccess, v, v
	o.rec.ResultOrigin, o.rec.ResultNested, o.rec.CarriedNested = c.Origin, c.Nested, c.Nested
	o.carried, o.origin, o.nested = v, c.Origin, c.Nested
	return r.stop(ctx, fr, i, site, o)
}

// registerHit registers the script job of identity env, which the result
// cache completed without running, in the job registry, and ends it at
// once: a job that ran is registered by the facade's executor for as long
// as it runs (facade.Register), with the same fields, and is then known
// while its root lives (ADR 0007, PF3).
func (e *Engine) registerHit(env map[string]string) {
	end := e.platform.JobRegistry().Begin(platform.Job{ID: env["WM_JOB_ID"], Kind: platform.JobScript, Root: env["WM_ROOT_JOB_ID"], Path: env["WM_JOB_PATH"], Env: env})
	end()
}

// flowTTL is the TTL of a flow job of f, its flow-level cache_ttl: the
// root's, or a subflow's by path, whose step's own is dropped (FX-40);
// unset for none.
func flowTTL(f *ir.Flow) jobTTL { return ttlOf(f.CacheTTL) }

// resultCache is the result cache an engine with services p keeps its
// results in: the services', or one of its own when it has none (D4).
func resultCache(p *platform.Services) platform.ResultCache {
	if c := p.ResultCache(); c != nil {
		return c
	}
	return platform.NewMemoryResults()
}
