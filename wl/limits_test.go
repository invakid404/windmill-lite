package wl

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

// A run's limits reach the engine: arguments over MaxArgsBytes refuse the
// run before it starts (a flow and a script alike), MaxInFlight lists D15
// in the trace, and MaxHeldBytes latches run_memory_limit, in a flow and
// in a script job. A negative limit is refused.
func TestRunLimits(t *testing.T) {
	bun := bunOrSkip(t)
	big := strings.Repeat("x", 100)
	out, runtimeDir := buildBunBundle(t, bun, map[string]string{
		"wmill.yaml": "defaultTs: bun\n",
		"f/t/big.ts": "export async function main() { return \"" + big + "\" }\n",
		"f/t/two.flow/flow.yaml": `{"summary":"t","value":{"modules":[` +
			`{"id":"a","value":{"type":"script","path":"f/t/big","input_transforms":{}}},` +
			`{"id":"b","value":{"type":"script","path":"f/t/big","input_transforms":{}}}]}}`,
	})
	rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	ctx := context.Background()
	started := 0
	hooks := &Hooks{OnModuleStart: func(context.Context, RunHandle, Site) error { started++; return nil }}
	args := json.RawMessage(`{"k":"` + big + `"}`)
	for _, opts := range []RunOptions{
		{Hooks: hooks, Limits: RunLimits{MaxArgsBytes: len(args) - 1}},
		{Kind: KindScript, Limits: RunLimits{MaxArgsBytes: len(args) - 1}},
	} {
		path := "f/t/two"
		if opts.Kind == KindScript {
			path = "f/t/big"
		}
		if res, err := rt.Run(ctx, path, args, opts); !errors.Is(err, ErrRunArgsTooLarge) {
			t.Fatalf("%s: %+v, %v; want ErrRunArgsTooLarge", path, res, err)
		}
	}
	if started != 0 {
		t.Fatalf("a refused run started %d modules", started)
	}
	// The size is the arguments' as given: ten spaces pass two bytes, though
	// they run as {}, and no arguments at all fit one byte.
	if res, err := rt.Run(ctx, "f/t/big", json.RawMessage("          "), RunOptions{Kind: KindScript, Limits: RunLimits{MaxArgsBytes: 2}}); !errors.Is(err, ErrRunArgsTooLarge) {
		t.Fatalf("ten spaces under two bytes: %+v, %v; want ErrRunArgsTooLarge", res, err)
	}
	if res, err := rt.Run(ctx, "f/t/big", nil, RunOptions{Kind: KindScript, Limits: RunLimits{MaxArgsBytes: 1}}); err != nil || res.Status != StatusSuccess {
		t.Fatalf("no arguments under one byte: %+v, %v", res, err)
	}
	res, err := rt.Run(ctx, "f/t/two", args, RunOptions{Limits: RunLimits{MaxArgsBytes: len(args), MaxInFlight: 1}})
	if err != nil || res.Status != StatusSuccess || !slices.Contains(res.Trace.Deviations, "D15") {
		t.Fatalf("arguments at the limit, MaxInFlight 1: %+v, %v", res, err)
	}
	res, err = rt.Run(ctx, "f/t/two", nil, RunOptions{Limits: RunLimits{MaxHeldBytes: 150}})
	if err != nil || res.Status != StatusLatched || !errors.Is(res.Latched, ErrRunMemoryLimit) {
		t.Fatalf("a flow over MaxHeldBytes: %+v, %v; want run_memory_limit", res, err)
	}
	res, err = rt.Run(ctx, "f/t/big", nil, RunOptions{Kind: KindScript, Limits: RunLimits{MaxHeldBytes: 50}})
	if err != nil || res.Status != StatusLatched || !errors.Is(res.Latched, ErrRunMemoryLimit) {
		t.Fatalf("a script job over MaxHeldBytes: %+v, %v; want run_memory_limit", res, err)
	}
	if res, err := rt.Run(ctx, "f/t/big", nil, RunOptions{Kind: KindScript, Limits: RunLimits{MaxHeldBytes: 1000}}); err != nil || res.Status != StatusSuccess {
		t.Fatalf("a script job under MaxHeldBytes: %+v, %v", res, err)
	}
	if _, err := rt.Run(ctx, "f/t/two", nil, RunOptions{Limits: RunLimits{MaxInFlight: -1}}); err == nil {
		t.Fatal("a negative limit was not refused")
	}
}
