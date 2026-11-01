package bench

import (
	"sync"
	"sync/atomic"
	"time"
)

// Concurrency is the concurrency a Workers call reached.
type Concurrency struct {
	// Wall is the time from the start of the first operation to the end
	// of the last.
	Wall time.Duration
	// Peak is the largest number of operations in flight at once.
	Peak int
	// Mean is the mean number of operations in flight over Wall: the sum
	// of the operations' durations divided by Wall.
	Mean float64
	// Ops are the durations of the operations that completed, worker by
	// worker, in order.
	Ops [][]time.Duration
}

// Workers runs conc workers, each of which calls op ops times in turn with
// its index, and returns the concurrency they reached. The workers start
// together: none calls op before every worker is ready, so even ops = 1
// puts conc operations in flight at once, and each worker does the same
// number of operations, so the end has no tail of fewer workers than
// conc, beyond operations of unequal length. A worker stops at its first
// error, and Workers returns the first error any worker met, after every
// worker has stopped.
func Workers(conc, ops int, op func(worker int) error) (Concurrency, error) {
	var (
		ready, done sync.WaitGroup
		start       = make(chan struct{})
		inFlight    atomic.Int64
		peak        atomic.Int64
		errOnce     sync.Once
		firstErr    error
		c           = Concurrency{Ops: make([][]time.Duration, conc)}
	)
	ready.Add(conc)
	for w := range conc {
		done.Go(func() {
			ready.Done()
			<-start
			for range ops {
				n := inFlight.Add(1)
				for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
				}
				t0 := time.Now()
				err := op(w)
				d := time.Since(t0)
				inFlight.Add(-1)
				if err != nil {
					errOnce.Do(func() { firstErr = err })
					return
				}
				c.Ops[w] = append(c.Ops[w], d)
			}
		})
	}
	ready.Wait()
	t0 := time.Now()
	close(start)
	done.Wait()
	c.Wall = time.Since(t0)
	c.Peak = int(peak.Load())
	var busy time.Duration
	for _, w := range c.Ops {
		for _, d := range w {
			busy += d
		}
	}
	if c.Wall > 0 {
		c.Mean = float64(busy) / float64(c.Wall)
	}
	return c, firstErr
}
