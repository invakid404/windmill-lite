package main

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/wl"
)

// TestPluginTraceDetailHost runs a run-end hook's trace detail on the real
// topology: a compiled host whose plugin's onRunEnd declares the trace it
// takes (testdata/benchhost, WL_BENCH_TRACE) and answers the keys of what
// it got as a warning (WL_BENCH_TRACE_KEYS=1). The default and full give
// the whole trace, summary the run's own fields, none the run's id and
// status.
func TestPluginTraceDetailHost(t *testing.T) {
	bun := benchSetup(t)
	t.Setenv("WL_BENCH_TRACE_KEYS", "1")
	full := []string{"bundle", "cached", "children", "deviations", "may_have_mutated", "module_state", "platform_calls", "result", "run", "shared_code", "spans", "status", "steps", "warnings"}
	for _, tc := range []struct {
		detail string
		keys   []string
	}{
		{"", full},
		{"full", full},
		{"summary", []string{"bundle", "cached", "deviations", "may_have_mutated", "module_state", "run", "shared_code", "status", "warnings"}},
		{"none", []string{"run", "status"}},
	} {
		t.Run("detail="+tc.detail, func(t *testing.T) {
			t.Setenv("WL_BENCH_TRACE", tc.detail)
			rt := benchRuntimeWith(t, bun, "host-plugin", func(*wl.Options) {})
			res, err := rt.Run(context.Background(), "f/bench/cachecheck", json.RawMessage(`{}`), wl.RunOptions{})
			if err != nil || res.Status != wl.StatusSuccess {
				t.Fatalf("%+v, %v", res, err)
			}
			i := slices.IndexFunc(res.Warnings, func(w wl.Warning) bool { return w.Code == "bench_trace" })
			if i < 0 {
				t.Fatalf("onRunEnd answered no keys: %+v", res.Warnings)
			}
			if got := strings.Split(res.Warnings[i].Message, ","); !slices.Equal(got, tc.keys) {
				t.Fatalf("onRunEnd got %v, want %v", got, tc.keys)
			}
		})
	}
}
