package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/expr/quickjs"
	"github.com/invakid404/windmill-lite/internal/harness"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// runHost is `windmill-lite host`: the engine side of host mode (ADR 0004
// host mode, ADR 0009), started by the Bun host SDK with its socket as file
// descriptor 3. It serves the host's runs with the walking skeleton's
// interpreter (milestone W1) until the host's connection ends, and runs
// their steps in the host. F3a replaces this with host mode on the wl
// library.
func runHost(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("host", flag.ContinueOnError)
	fs.SetOutput(stderr)
	bundle := fs.String("bundle", "", "the bundle directory")
	killAfter := fs.Duration("kill-after", harness.DefaultKillAfter, "the watchdog's threshold of silence from the host; 0 disables it")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *bundle == "" || fs.NArg() != 0 || *killAfter < 0 {
		fmt.Fprintln(stderr, "usage: windmill-lite host --bundle <dir> [--kill-after <duration>]")
		return 2
	}
	if err := serveHost(*bundle, *killAfter, stderr); err != nil {
		fmt.Fprintf(stderr, "windmill-lite host: %v\n", err)
		return 1
	}
	return 0
}

func serveHost(bundle string, killAfter time.Duration, stderr io.Writer) error {
	manifest, err := os.ReadFile(filepath.Join(bundle, "manifest.json"))
	if err != nil {
		return fmt.Errorf("the bundle manifest: %w", err)
	}
	sum := sha256.Sum256(manifest)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	js, err := quickjs.New(quickjs.Options{})
	if err != nil {
		return err
	}
	defer js.Close()
	skel, err := engine.NewSkeleton(engine.SkeletonConfig{BundleDir: bundle, Bundle: digest, JS: js})
	if err != nil {
		return err
	}
	conn, err := harness.HostConn()
	if err != nil {
		return err
	}
	if killAfter == 0 {
		killAfter = harness.WatchdogOff
	}
	cfg := harness.HostConfig{
		Conn:          conn,
		Kind:          "bun",
		Runtime:       harness.PinnedBun,
		Bundle:        digest,
		EngineVersion: resolveVersion(),
		KillAfter:     killAfter,
		Diagnostics:   stderr,
		Run: func(ctx context.Context, p *harness.Process, r harness.RunRequest) harness.RunDone {
			return hostRun(ctx, skel, p, r)
		},
	}
	// The host SDK passes the side channel of its steps' child groups as
	// file descriptor 4 (WR-55); a host that passes none announces them on
	// its socket.
	if c := harness.HostChildren(); c != nil {
		defer c.Close()
		cfg.Children = c
	}
	p, err := harness.AttachHost(context.Background(), cfg)
	if err != nil {
		return err
	}
	<-p.Done()
	return nil
}

// hostRun runs one of the host's runs; its steps go back to the host.
func hostRun(ctx context.Context, skel *engine.Skeleton, p *harness.Process, r harness.RunRequest) harness.RunDone {
	if r.Kind != harness.RunFlow {
		return failedRun(errors.New("the walking skeleton runs flows only"))
	}
	var hooks engine.Hooks = engine.NoHooks{}
	if !r.Intercept.IsZero() {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(r.Intercept.Producer(), &raw); err != nil {
			return failedRun(fmt.Errorf("intercept: %w", err))
		}
		results := make(map[string]jsonval.Value, len(raw))
		for id, v := range raw {
			results[id] = jsonval.FromProducer(v)
		}
		hooks = engine.StaticIntercepts{Results: results}
	}
	t, err := skel.Run(ctx, engine.SkeletonRun{ID: r.ID, Path: r.Path, Args: r.Args, Hooks: hooks, Exec: p})
	if err != nil {
		return failedRun(err)
	}
	return harness.RunDone{Status: string(t.Status), Result: t.Result}
}

// failedRun is a run that could not start, failed with an error the
// engine generates.
func failedRun(err error) harness.RunDone {
	b, _ := json.Marshal(map[string]map[string]string{"error": {"message": err.Error(), "name": "InternalErr"}})
	return harness.RunDone{Status: string(conformance.RunFailure), Result: jsonval.FromProducer(b)}
}
