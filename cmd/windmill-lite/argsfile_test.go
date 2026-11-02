package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/bundle"
	"github.com/invakid404/windmill-lite/wl"
)

// manifestOf reads a built bundle's manifest.
func manifestOf(t *testing.T, dir string) bundle.Manifest {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, bundle.ManifestFile))
	var m bundle.Manifest
	if err == nil {
		err = json.Unmarshal(b, &m)
	}
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// TestHostArgsFileSharedReader checks that host mode's args.json, one
// file in the host's working directory, holds the latest invocation's
// arguments for a script that reads it while other invocations run (ADR
// 0004 host mode item 7), with the host's default, which writes every
// invocation's file. f/bench/argsreader marks that it has started, waits
// until the test releases it, and then reads the file. Meanwhile the
// eight-step chain runs, its steps' arguments {"p":{"id":7}}, so the
// reader sees those, not its own.
func TestHostArgsFileSharedReader(t *testing.T) {
	bun := benchSetup(t)
	rt := benchRuntime(t, bun, "host")
	wd, _ := benchWorkdirs.Load(rt)
	workdir := wd.(string)
	release := func() { os.WriteFile(filepath.Join(workdir, "reader-release"), []byte("go"), 0o644) }
	t.Cleanup(release)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan *wl.Result, 1)
	go func() {
		r, err := rt.Run(ctx, "f/bench/argsreader", json.RawMessage(`{"p":{"tag":"reader"}}`), wl.RunOptions{})
		if err != nil {
			t.Error(err)
		}
		done <- r
	}()
	for {
		if _, err := os.Stat(filepath.Join(workdir, "reader-ready")); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("the reader did not start")
		case <-time.After(time.Millisecond):
		}
	}
	r, err := rt.Run(ctx, "f/bench/chain", json.RawMessage(`{"p":{"id":7}}`), wl.RunOptions{})
	if err != nil || r.Status != wl.StatusSuccess {
		t.Fatalf("the chain: %+v, %v", r, err)
	}
	release()
	got := <-done
	if got == nil || got.Status != wl.StatusSuccess || string(got.Result) != `{"p":{"id":7}}` {
		t.Fatalf("the reader read %+v; want the latest invocation's arguments, {\"p\":{\"id\":7}}", got)
	}
}

// TestHostArgsFileHookReader checks that a host plugin's hook that reads
// args.json after a run finds the run's latest invocation's arguments,
// with the host's default: the host's onRunEnd answers the file's text, or
// MISSING, as a warning. The invocation has no around wrapper.
func TestHostArgsFileHookReader(t *testing.T) {
	bun := benchSetup(t)
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "host.ts")
	if err := os.WriteFile(src, []byte(`import { connect } from "`+filepath.Join(repo, "harnesses/bun/host/index.ts")+`";
const env = process.env;
await connect({
  engine: env.WL_ENGINE!,
  bundle: env.WL_BUNDLE!,
  workdir: process.cwd(),
  watchdog: { killAfter: 0 },
  onEngineExit: () => process.exit(0),
  plugin: {
    onRunEnd: async () => {
      try {
        return [{ code: "args_file_read", message: await Bun.file("args.json").text() }];
      } catch {
        return [{ code: "args_file_read", message: "MISSING" }];
      }
    },
  },
});
console.log("ready " + process.pid);
`), 0o644); err != nil {
		t.Fatal(err)
	}
	host := filepath.Join(dir, "host")
	if err := compileHost(bun, src, host); err != nil {
		t.Fatal(err)
	}
	prev := benchHost
	benchHost = host
	defer func() { benchHost = prev }()
	rt := benchRuntime(t, bun, "host")
	r, err := rt.Run(context.Background(), "f/bench/chain", json.RawMessage(`{"p":{"id":7}}`), wl.RunOptions{})
	if err != nil || r.Status != wl.StatusSuccess {
		t.Fatalf("%+v, %v", r, err)
	}
	for _, w := range r.Warnings {
		if w.Code == "args_file_read" {
			if w.Message != `{"p":{"id":7}}` {
				t.Fatalf("onRunEnd read %q; want the last invocation's arguments", w.Message)
			}
			return
		}
	}
	t.Fatalf("onRunEnd answered no reading: %+v", r.Warnings)
}

// TestHostSkipArgsFile checks lever 2's rule (ADR 0004 host mode item 7):
// host mode writes no args.json only for a bundle none of whose scripts
// can read it (the manifest's no_args_file_reader) whose host opted in
// (wl.Host.SkipArgsFile). The plain bundle has no reader, the benchmarks'
// bundle has readers; the plain one runs with the option on and off, the
// benchmarks' with it on.
func TestHostSkipArgsFile(t *testing.T) {
	bun := benchSetup(t)
	if !manifestOf(t, benchPlainBundle).NoArgsFileReader || manifestOf(t, benchBundle).NoArgsFileReader {
		t.Fatal("no_args_file_reader: want it set for the plain bundle alone")
	}
	for _, tc := range []struct {
		name, bundle, flow string
		option, file       bool
	}{
		{"no reader, option on", benchPlainBundle, "f/bench/plain_one", true, false},
		{"no reader, option off", benchPlainBundle, "f/bench/plain_one", false, true},
		{"readers, option on", benchBundle, "f/bench/chain", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := benchRuntimeOn(t, bun, "host", tc.bundle, func(o *wl.Options) { o.Host.SkipArgsFile = tc.option })
			r, err := rt.Run(context.Background(), tc.flow, json.RawMessage(`{"p":{"id":7}}`), wl.RunOptions{})
			if err != nil || r.Status != wl.StatusSuccess {
				t.Fatalf("%+v, %v", r, err)
			}
			dir, _ := benchWorkdirs.Load(rt)
			_, err = os.Stat(filepath.Join(dir.(string), bundle.ArgsFileName))
			if got := err == nil; got != tc.file || err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("args.json in the host's working directory: %t (%v), want %t", got, err, tc.file)
			}
		})
	}
}

// TestHostSkipArgsFileOption checks that the SDK's skipArgsFile reaches
// the engine (windmill-lite host -skip-args-file): the test host, on the
// plain bundle, leaves args.json out with WL_SKIP_ARGS_FILE=1, and writes
// it without.
func TestHostSkipArgsFileOption(t *testing.T) {
	benchSetup(t)
	for _, on := range []bool{true, false} {
		var env []string
		if on {
			env = append(env, "WL_SKIP_ARGS_FILE=1")
		}
		s := superviseHost(t, []string{compiledHost(t)}, benchPlainBundle, 30000, env...)
		r, err := s.run("f/bench/plain_one", "tenant-a", map[string]any{"p": map[string]any{"id": 7}}, nil)
		if err != nil || r.Status != "success" {
			t.Fatalf("skipArgsFile %t: %+v, %v", on, r, err)
		}
		_, err = os.Stat(filepath.Join(s.dir, bundle.ArgsFileName))
		if written := err == nil; written == on {
			t.Fatalf("skipArgsFile %t: args.json written %t (%v)", on, written, err)
		}
	}
}
