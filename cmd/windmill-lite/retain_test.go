package main

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"
	"weak"

	"github.com/invakid404/windmill-lite/internal/bench"
	"github.com/invakid404/windmill-lite/wl"
)

// TestSettledRunLetsGoOfItsValuesOnHosts carries internal/engine's
// TestSettledRunLetsGoOfItsValues to the real host topologies, where the
// harness keeps its last settled invocations for their late output (D9):
// the plain host, and the host whose plugin provides resources and
// variables (host-plugin), whose runs read through their own platform
// services and the plugin's binding to the run. Once a run has settled,
// its step results are unreachable, while the runtime and its host go
// on. It needs the pinned Bun.
func TestSettledRunLetsGoOfItsValuesOnHosts(t *testing.T) {
	bun := benchSetup(t)
	for _, topology := range []string{"host", "host-plugin"} {
		t.Run(topology, func(t *testing.T) {
			rt := benchRuntime(t, bun, topology)
			args := fmt.Appendf(nil, `{"p":%s}`, bench.Synthetic(1<<20))
			var results []weak.Pointer[byte]
			for range 3 {
				res, err := rt.Run(context.Background(), "f/bench/pair", args, wl.RunOptions{})
				if err != nil || res.Status != wl.StatusSuccess || res.Trace == nil || len(res.Trace.Steps) != 2 {
					t.Fatalf("the run: %v, %v", res, err)
				}
				for _, s := range res.Trace.Steps {
					results = append(results, weak.Make(&s.Result.Producer()[0]))
				}
			}
			reachable := func() int {
				runtime.GC()
				runtime.GC()
				n := 0
				for _, w := range results {
					if w.Value() != nil {
						n++
					}
				}
				return n
			}
			// A run settles a moment after its result returns, once its
			// invocations have.
			n := reachable()
			for deadline := time.Now().Add(10 * time.Second); n > 0 && time.Now().Before(deadline); n = reachable() {
				time.Sleep(20 * time.Millisecond)
			}
			if n > 0 {
				t.Fatalf("%d of %d step results of settled runs are still reachable", n, len(results))
			}
			runtime.KeepAlive(args)
			runtime.KeepAlive(rt)
		})
	}
}
