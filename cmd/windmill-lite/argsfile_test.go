package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/invakid404/windmill-lite/internal/bundle"
	"github.com/invakid404/windmill-lite/wl"
)

// TestHostArgsFileLeftOut checks host mode's args.json rule (ADR 0004 host
// mode item 7) on the real topology: f/bench/chain's step, f/bench/echo,
// returns its argument and reaches no file, so the build clears it
// (no_args_file), and a host-mode run of the chain writes no args.json
// into the host's working directory; its steps still get their
// arguments, which travel in the invoke frame. The shapes' step,
// f/bench/work, can fetch, so it is not cleared.
// TestHostArgsFileReaders runs scripts that do read the file.
func TestHostArgsFileLeftOut(t *testing.T) {
	bun := benchSetup(t)
	b, err := os.ReadFile(filepath.Join(benchBundle, bundle.ManifestFile))
	var m bundle.Manifest
	if err == nil {
		err = json.Unmarshal(b, &m)
	}
	if err != nil || !slices.Contains(m.NoArgsFile, "f/bench/echo") || !slices.Contains(m.NoArgsFile, "f/bench/plain") || slices.Contains(m.NoArgsFile, "f/bench/work") {
		t.Fatalf("the benchmarks' manifest: no_args_file %v, %v", m.NoArgsFile, err)
	}
	rt := benchRuntime(t, bun, "host")
	res, err := rt.Run(context.Background(), "f/bench/chain", json.RawMessage(`{"p":{"id":7}}`), wl.RunOptions{})
	if err != nil || res.Status != wl.StatusSuccess || string(res.Result) != `{"id":7}` {
		t.Fatalf("%+v, %v", res, err)
	}
	dir, _ := benchWorkdirs.Load(rt)
	if _, err := os.Stat(filepath.Join(dir.(string), bundle.ArgsFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("args.json in the host's working directory %s: %v", dir, err)
	}
}
