package platform

import (
	"context"
	"errors"
	"sync"
)

// ReadCache is one run's read cache, deviation D21 of [ADR 0001], which a
// host turns on: it answers each resource and each variable from its
// providers once per run, at the first read of its path, and every later
// read of that path in the run from that answer. Windmill reads them again
// at each job's pickup and at each API call, so a value that changes
// during a run reaches the run's later steps there, and here only the next
// run.
//
// It holds the providers' answers, the values as stored: the walker still
// interpolates them at each read, so a $WM_* string resolves for the job
// that reads it, and a nested $res: or $var: reference is read through the
// cache too. An answer that is an error is not kept, so the next read asks
// again; reads of one path that overlap share one call. A variable that a
// step updates through the cache (UpdateVariable) is read again at its
// next read. ReadCache is safe for concurrent use.
//
// [ADR 0001]: https://github.com/invakid404/windmill-lite/blob/main/docs/design/0001-equivalence-contract.md#5-named-deviations
type ReadCache struct {
	res  Resources
	vars Variables

	mu        sync.Mutex
	resources map[string]*cacheEntry[Resource]
	variables map[string]*cacheEntry[Variable]
}

type cacheEntry[T any] struct {
	done  chan struct{}
	value T
	found bool
	err   error
}

// NewReadCache returns a read cache over the resource and variable
// providers res and vars, either of which may be nil (none).
func NewReadCache(res Resources, vars Variables) *ReadCache {
	return &ReadCache{res: res, vars: vars, resources: map[string]*cacheEntry[Resource]{}, variables: map[string]*cacheEntry[Variable]{}}
}

// Resource answers the resource at path from the run's first read of it.
func (c *ReadCache) Resource(ctx context.Context, path string) (Resource, bool, error) {
	if c.res == nil {
		return Resource{}, false, nil
	}
	return cachedRead(ctx, &c.mu, c.resources, path, c.res.Resource)
}

// Variable answers the variable at path from the run's first read of it.
func (c *ReadCache) Variable(ctx context.Context, path string) (Variable, bool, error) {
	if c.vars == nil {
		return Variable{}, false, nil
	}
	return cachedRead(ctx, &c.mu, c.variables, path, c.vars.Variable)
}

// UpdateVariable updates the variable at path through the variable
// provider, which must take writes (VariableUpdater), and drops the
// cache's answer for path, so that the run's next read asks again.
func (c *ReadCache) UpdateVariable(ctx context.Context, path string, value *string, secret *bool) (bool, error) {
	u, ok := c.vars.(VariableUpdater)
	if !ok {
		return false, ErrReadOnlyVariables
	}
	found, err := u.UpdateVariable(ctx, path, value, secret)
	c.mu.Lock()
	delete(c.variables, path)
	c.mu.Unlock()
	return found, err
}

// errPanicked is what the readers that waited for a read get when the
// provider panicked instead of answering.
var errPanicked = errors.New("platform: the provider's read did not complete")

// cachedRead answers path from m, or loads it once: the first reader
// loads it, and readers that come while it loads wait for its answer, or
// for their own ctx. An error is answered to those readers and not kept.
func cachedRead[T any](ctx context.Context, mu *sync.Mutex, m map[string]*cacheEntry[T], path string, load func(context.Context, string) (T, bool, error)) (T, bool, error) {
	mu.Lock()
	e := m[path]
	if e != nil {
		mu.Unlock()
		select {
		case <-e.done:
			return e.value, e.found, e.err
		case <-ctx.Done():
			var zero T
			return zero, false, context.Cause(ctx)
		}
	}
	e = &cacheEntry[T]{done: make(chan struct{})}
	m[path] = e
	mu.Unlock()
	defer close(e.done)
	failed := true
	defer func() {
		if failed {
			// An error, or a panic in the provider: the readers that waited
			// get an error, and the next read asks again.
			if e.err == nil {
				e.err = errPanicked
			}
			mu.Lock()
			if m[path] == e {
				delete(m, path)
			}
			mu.Unlock()
		}
	}()
	e.value, e.found, e.err = load(ctx, path)
	failed = e.err != nil
	return e.value, e.found, e.err
}
