package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/invakid404/windmill-lite/internal/bundle"
	"github.com/invakid404/windmill-lite/wl"
)

// TestHostArgsFileLeftOut checks host mode's args.json rule (ADR 0004 host
// mode item 7) on the real topology: the benchmarks' bundle has no script
// that names args.json, so its manifest leaves args_file unset, and a
// host-mode run writes no args.json into the host's working directory;
// its steps still get their arguments, which travel in the invoke frame.
// TestHostCodebaseAsset runs a bundle whose script reads the file, which
// the engine still writes.
func TestHostArgsFileLeftOut(t *testing.T) {
	bun := benchSetup(t)
	b, err := os.ReadFile(filepath.Join(benchBundle, bundle.ManifestFile))
	var m bundle.Manifest
	if err == nil {
		err = json.Unmarshal(b, &m)
	}
	if err != nil || m.ArgsFile {
		t.Fatalf("the benchmarks' manifest: args_file %t, %v", m.ArgsFile, err)
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
