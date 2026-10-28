package quickjs

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/invakid404/windmill-lite/internal/expr"
)

// Options configure an Engine. The zero value gives Windmill's limits and
// the default pool.
type Options struct {
	// Limits are the per-evaluation limits; the zero value means
	// expr.DefaultLimits().
	Limits expr.Limits
	// Slots is the number of evaluation slots; 0 means
	// min(GOMAXPROCS, 8) (ADR 0003).
	Slots int
	// CacheEntries bounds the compile cache, which maps a program source to
	// its compiled program or its compile error; 0 means 1024, and a
	// negative value turns the cache off.
	CacheEntries int
}

// ErrClosed is returned by an Engine that has been closed.
var ErrClosed = errors.New("expr/quickjs: engine closed")

// errForeign reports a program that this Engine did not compile.
var errForeign = errors.New("expr/quickjs: program was not compiled by this engine")

// errNilCall reports a HostFunc without a Call.
var errNilCall = errors.New("expr/quickjs: host function has no Call")

// Engine is the native QuickJS-ng engine with its pool of slots. It is safe
// for concurrent use. Close it to stop its threads.
type Engine struct {
	limits    expr.Limits
	slots     int
	jobs      chan *job
	closing   chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
	cache     *compileCache
	stats     counters
}

var _ expr.Engine = (*Engine)(nil)

// program is what Compile returns: the bytecode of one source, for the
// Engine that compiled it.
type program struct {
	engine *Engine
	bc     []byte
}

type job struct {
	ctx     context.Context
	compile bool
	source  string
	prog    *program
	in      expr.Input
	reply   chan reply
}

type reply struct {
	out []byte // the result text, or the bytecode of a compile job
	err error
}

// New starts an Engine. It returns once every slot has prepared its first
// runtime, so no evaluation ever waits for a slot to be created.
func New(opts Options) (*Engine, error) {
	limits := opts.Limits
	if limits == (expr.Limits{}) {
		limits = expr.DefaultLimits()
	}
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	n := opts.Slots
	switch {
	case n == 0:
		n = min(runtime.GOMAXPROCS(0), 8)
	case n < 0:
		return nil, fmt.Errorf("expr/quickjs: slot count %d is negative", n)
	}
	e := &Engine{
		limits:  limits,
		slots:   n,
		jobs:    make(chan *job),
		closing: make(chan struct{}),
	}
	switch entries := opts.CacheEntries; {
	case entries == 0:
		e.cache = newCompileCache(1024)
	case entries > 0:
		e.cache = newCompileCache(entries)
	}
	for range n {
		if err := e.startSlot(); err != nil {
			e.Close()
			return nil, err
		}
	}
	return e, nil
}

// Limits returns the per-evaluation limits the Engine applies.
func (e *Engine) Limits() expr.Limits { return e.limits }

// Compile compiles source as a global script named eval_script, the name
// Windmill's engine frames carry (JE-21), and returns its bytecode as the
// Program. A source that does not compile returns an *expr.Exception with
// the engine's error, typically a SyntaxError, or a RangeError for source
// nested too deeply for the stack limit (JE-48). Results are cached by
// source text.
func (e *Engine) Compile(source string) (expr.Program, error) {
	if p, err, ok := e.cache.get(source); ok {
		return p, err
	}
	r, err := e.submit(context.Background(), &job{compile: true, source: source})
	if err != nil {
		return nil, err
	}
	var ex *expr.Exception
	switch {
	case errors.As(r.err, &ex):
		e.cache.put(source, nil, ex)
		return nil, copyException(ex)
	case r.err != nil:
		return nil, r.err
	}
	p := &program{engine: e, bc: r.out}
	e.cache.put(source, p, nil)
	return p, nil
}

// Eval runs p on a fresh runtime and context, as expr.Engine describes. Each
// HostString becomes a writable, enumerable and configurable global property
// holding its text as a string, and each HostFunc a global function of that
// name. A host function converts its first argument with ToString (a missing
// one is "undefined"), returns a Promise, and calls Call on its own
// goroutine with a context that ends with the attempt; at most 64 calls of
// one evaluation run at once, and the rest wait in call order. The Promise
// settles only after the jobs queued so far have run: it resolves with
// Call's text as a string, or rejects with an Error whose message is
// err.Error(), built with no frames on the stack. A Call still running when
// the evaluation ends is abandoned, and its result is dropped. Every Call
// must be non-nil.
//
// The completion value is settled when the program's top level returns
// something other than a Promise, or when the Promise it returned has
// settled; jobs still queued then never run. A job that throws fails the
// evaluation with its exception. The result text is UTF-8, except that an
// unpaired surrogate comes back as its three-byte (WTF-8) form.
func (e *Engine) Eval(ctx context.Context, p expr.Program, in expr.Input) ([]byte, error) {
	prog, ok := p.(*program)
	if !ok || prog == nil || prog.engine != e {
		return nil, errForeign
	}
	for _, f := range in.Funcs {
		if f.Call == nil {
			return nil, fmt.Errorf("%w: %q", errNilCall, f.Name)
		}
	}
	if ctx.Err() != nil {
		return nil, context.Cause(ctx)
	}
	r, err := e.submit(ctx, &job{ctx: ctx, prog: prog, in: in})
	if err != nil {
		return nil, err
	}
	return r.out, r.err
}

// Close stops the Engine. Callers still waiting for a slot get ErrClosed;
// evaluations already running finish first. Close waits for every slot
// thread to free its runtime and exit.
func (e *Engine) Close() error {
	e.closeOnce.Do(func() { close(e.closing) })
	e.wg.Wait()
	return nil
}

// submit hands j to the next prepared slot, waiting in FIFO order when none
// is free, and returns its reply.
func (e *Engine) submit(ctx context.Context, j *job) (reply, error) {
	select {
	case <-e.closing:
		return reply{}, ErrClosed
	default:
	}
	j.reply = make(chan reply, 1)
	select {
	case e.jobs <- j:
	default:
		// No prepared slot: wait. A blocked sender on an unbuffered
		// channel is queued and served in arrival order.
		e.stats.waited.Add(1)
		start := time.Now()
		select {
		case e.jobs <- j:
		case <-ctx.Done():
			return reply{}, context.Cause(ctx)
		case <-e.closing:
			return reply{}, ErrClosed
		}
		e.stats.waitNanos.Add(int64(time.Since(start)))
	}
	return <-j.reply, nil
}

// Stats are an Engine's counters since New.
type Stats struct {
	// Slots is the pool size.
	Slots int
	// Evaluations and Compiles count the jobs that reached a slot.
	Evaluations uint64
	Compiles    uint64
	// Waited counts the jobs that found no prepared slot, and WaitTime is
	// the total wait of those that then got one.
	Waited   uint64
	WaitTime time.Duration
	// Timeouts counts attempts interrupted at the attempt limit, and
	// Canceled those interrupted because their context ended.
	Timeouts uint64
	Canceled uint64
	// LeakedBlocks counts heap blocks that were still allocated after a
	// runtime had been freed, which the engine then freed itself.
	// QuickJS-ng leaks for some programs (assigning an object to
	// Error.stackTraceLimit, for one); any other leak is a bug.
	LeakedBlocks uint64
}

type counters struct {
	evaluations, compiles, waited, timeouts, canceled, leaked atomic.Uint64
	waitNanos                                                 atomic.Int64
}

// Stats returns the Engine's counters.
func (e *Engine) Stats() Stats {
	return Stats{
		Slots:        e.slots,
		Evaluations:  e.stats.evaluations.Load(),
		Compiles:     e.stats.compiles.Load(),
		Waited:       e.stats.waited.Load(),
		WaitTime:     time.Duration(e.stats.waitNanos.Load()),
		Timeouts:     e.stats.timeouts.Load(),
		Canceled:     e.stats.canceled.Load(),
		LeakedBlocks: e.stats.leaked.Load(),
	}
}

func copyException(ex *expr.Exception) *expr.Exception {
	c := *ex
	return &c
}

// compileCache is a bounded LRU map from source text to a compiled program
// or a compile exception. A nil cache caches nothing.
type compileCache struct {
	mu      sync.Mutex
	max     int
	entries map[string]*list.Element
	order   list.List
}

type cacheEntry struct {
	source string
	prog   *program
	ex     *expr.Exception
}

func newCompileCache(max int) *compileCache {
	return &compileCache{max: max, entries: make(map[string]*list.Element)}
}

func (c *compileCache) get(source string) (expr.Program, error, bool) {
	if c == nil {
		return nil, nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[source]
	if !ok {
		return nil, nil, false
	}
	c.order.MoveToFront(el)
	ent := el.Value.(*cacheEntry)
	if ent.ex != nil {
		return nil, copyException(ent.ex), true
	}
	return ent.prog, nil, true
}

func (c *compileCache) put(source string, p *program, ex *expr.Exception) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[source]; ok {
		c.order.MoveToFront(el)
		return
	}
	c.entries[source] = c.order.PushFront(&cacheEntry{source: source, prog: p, ex: ex})
	for c.order.Len() > c.max {
		last := c.order.Back()
		c.order.Remove(last)
		delete(c.entries, last.Value.(*cacheEntry).source)
	}
}
