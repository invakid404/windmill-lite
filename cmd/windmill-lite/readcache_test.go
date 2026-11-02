package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/invakid404/windmill-lite/wl"
)

// TestReadCacheHostCalls runs deviation D21's read cache on the real
// topology: a compiled host whose plugin provides the resources and the
// variable (testdata/benchhost, WL_BENCH_PLUGIN=1), which counts its
// hooks' calls by run and path. f/bench/cachecheck has three steps that
// each take two resources as $res: arguments and read a resource and a
// variable through the job API.
//
// With the cache, each (run, path) is one call of the plugin's hook, and
// every step sees the first read's value: f/bench/counter, which answers
// how many times the run read it, reads 1 in all three. Without it, every
// read is a call, and the steps see 1, 2 and 3, as on Windmill. Either way
// f/bench/jobres's $WM_JOB_ID resolves for each step's own job, since the
// cache keeps the value as stored. Only the cached run's trace lists D21.
func TestReadCacheHostCalls(t *testing.T) {
	bun := benchSetup(t)
	for _, cached := range []bool{true, false} {
		t.Run(fmt.Sprintf("cache=%t", cached), func(t *testing.T) {
			rt := benchRuntimeWith(t, bun, "host-plugin", func(o *wl.Options) { o.ReadCache = cached })
			res, err := rt.Run(context.Background(), "f/bench/cachecheck", json.RawMessage(`{}`), wl.RunOptions{})
			if err != nil || res.Status != wl.StatusSuccess {
				t.Fatalf("%+v, %v", res, err)
			}
			if got := slices.Contains(res.Trace.Deviations, "D21"); got != cached {
				t.Fatalf("the trace's deviations %v; D21 listed: %t, want %t", res.Trace.Deviations, got, cached)
			}
			var seen []float64
			for _, st := range res.Trace.Steps {
				var out struct {
					Seen   float64 `json:"seen"`
					Job    string  `json:"job"`
					OwnJob string  `json:"own_job"`
					R3     string  `json:"r3"`
					V1     string  `json:"v1"`
				}
				if err := json.Unmarshal(st.Result.Producer(), &out); err != nil {
					t.Fatalf("step %s: %s: %v", st.Site, st.Result.Producer(), err)
				}
				if out.Job == "" || out.Job != out.OwnJob || out.R3 != "db-3.bench.example.invalid" || out.V1 != "synthetic-setting" {
					t.Fatalf("step %s saw %+v", st.Site, out)
				}
				seen = append(seen, out.Seen)
			}
			reads := 3 // per path in the run, without the cache
			want := []float64{1, 2, 3}
			if cached {
				reads, want = 1, []float64{1, 1, 1}
			}
			if !slices.Equal(seen, want) {
				t.Fatalf("the steps saw the counter at %v, want %v", seen, want)
			}
			calls := probe(t, rt, false).calls
			for _, path := range []string{"f/bench/counter", "f/bench/jobres", "f/bench/r3", "f/bench/v1"} {
				if n := calls[res.ID+" "+path]; n != reads {
					t.Errorf("%s: %d calls of the plugin's hook in the run, want %d (all: %v)", path, n, reads, calls)
				}
			}
		})
	}
}
