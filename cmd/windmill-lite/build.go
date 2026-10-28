package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/invakid404/windmill-lite/internal/bundle"
	"github.com/invakid404/windmill-lite/internal/engine"
)

// runBuild is `windmill-lite build <folder> -o <bundle>` (ADR 0010). It
// prints the bundle digest on stdout, and warnings on stderr.
func runBuild(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("build", stderr)
	out := fs.String("o", "", "the bundle to write: a directory, or a `file` ending in .wlb")
	contract := fs.String("contract", "", "the host contract `file` (YAML or JSON); without one, every flow and script is a root")
	bun := fs.String("bun", envOr("WL_BUN", "bun"), "the Bun `executable`, which must be Bun 1.3.10 (default $WL_BUN, or bun)")
	helper := fs.String("helper", os.Getenv("WL_BUILD_HELPER"), "the Bun build helper, harnesses/bun/build/main.ts with its dependencies installed (default $WL_BUILD_HELPER)")
	runtime := fs.String("harness-runtime", os.Getenv("WL_HARNESS_RUNTIME"), "the Bun harness runtime `directory` the bundle is built for (default $WL_HARNESS_RUNTIME, or runtime/ beside the helper's directory)")
	mismatch := fs.Bool("bun-mismatch", false, "build on a Bun other than 1.3.10 and mark the bundle nonconformant_runtime")
	mode := fs.String("module-state", string(engine.ModuleStateWarm), "the module-state `mode`: warm or fresh")
	strict := fs.Bool("strict", false, "refuse absent for-loop skip_failures and branchall skip_failure instead of defaulting them")
	work := fs.String("work", "", "a scratch `directory` for dependency installs (default: a temporary one)")
	verbose := fs.Bool("verbose", false, "copy the build helper's output to stderr")
	pos, ok := parse(fs, args)
	if !ok {
		return 2
	}
	if len(pos) != 1 || *out == "" {
		fmt.Fprintln(stderr, "usage: windmill-lite build <folder> -o <bundle> [flags]")
		return 2
	}
	if *helper == "" {
		fmt.Fprintln(stderr, "windmill-lite build: the build helper is not set: pass -helper <harnesses/bun/build/main.ts> or set WL_BUILD_HELPER")
		return 2
	}
	if *runtime == "" {
		*runtime = filepath.Join(filepath.Dir(filepath.Dir(*helper)), "runtime")
	}
	cfg := bundle.Config{
		Folder:           pos[0],
		Strict:           *strict,
		ModuleState:      engine.ModuleState(*mode),
		EngineVersion:    resolveVersion(),
		Bun:              *bun,
		Helper:           *helper,
		HarnessRuntime:   *runtime,
		AllowBunMismatch: *mismatch,
		Work:             *work,
	}
	if *verbose {
		cfg.Diagnostics = stderr
	}
	if *contract != "" {
		data, err := os.ReadFile(*contract)
		if err != nil {
			fmt.Fprintf(stderr, "windmill-lite build: %v\n", err)
			return 1
		}
		cfg.Contract = data
	}
	archive := strings.HasSuffix(*out, bundle.ArchiveExt)
	if archive {
		if _, err := os.Lstat(*out); err == nil {
			fmt.Fprintf(stderr, "windmill-lite build: %s exists\n", *out)
			return 1
		}
		tmp, err := os.MkdirTemp("", "windmill-lite-bundle-")
		if err != nil {
			fmt.Fprintf(stderr, "windmill-lite build: %v\n", err)
			return 1
		}
		defer os.RemoveAll(tmp)
		cfg.Out = filepath.Join(tmp, "bundle")
	} else {
		cfg.Out = *out
	}

	// The helper runs in a process group of its own, so a signal to this
	// process does not reach it: cancelling the build ends it.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	res, err := bundle.Build(ctx, cfg)
	if res != nil {
		for _, w := range res.Warnings {
			fmt.Fprintf(stderr, "windmill-lite build: warning: %s\n", w)
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "windmill-lite build: %v\n", err)
		return 1
	}
	if archive {
		if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
			fmt.Fprintf(stderr, "windmill-lite build: %v\n", err)
			return 1
		}
		if err := bundle.WriteArchiveFile(cfg.Out, *out); err != nil {
			fmt.Fprintf(stderr, "windmill-lite build: %v\n", err)
			return 1
		}
	}
	if res.Manifest.NonconformantRuntime {
		fmt.Fprintf(stderr, "windmill-lite build: warning: built on Bun %s, not the pinned 1.3.10; the bundle is marked nonconformant_runtime\n", res.Manifest.Bundlers.Bun)
	}
	if cfg.EngineVersion == "(devel)" {
		fmt.Fprintln(stderr, "windmill-lite build: warning: this engine has no version, so the bundle records (devel)")
	}
	fmt.Fprintln(stdout, res.Digest)
	return 0
}

// runVerify is `windmill-lite verify <bundle>` (ADR 0010, drift guard 3).
// It prints the bundle digest on stdout when every digest checks out, and
// exits with status 1 otherwise.
func runVerify(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("verify", stderr)
	release := fs.String("manifest", "", "a release manifest `file` that the bundle's files and manifest must match")
	digest := fs.String("digest", "", "the bundle `digest` the bundle must have")
	pos, ok := parse(fs, args)
	if !ok {
		return 2
	}
	if len(pos) != 1 {
		fmt.Fprintln(stderr, "usage: windmill-lite verify <bundle> [-manifest <release manifest>] [-digest <digest>]")
		return 2
	}
	opts := bundle.VerifyOptions{Digest: *digest}
	if *release != "" {
		data, err := os.ReadFile(*release)
		if err != nil {
			fmt.Fprintf(stderr, "windmill-lite verify: %v\n", err)
			return 1
		}
		opts.Release = data
	}
	v, err := bundle.Verify(pos[0], opts)
	if err != nil {
		var me *bundle.MismatchError
		if errors.As(err, &me) {
			fmt.Fprintf(stderr, "windmill-lite verify: %s does not match:\n", pos[0])
			for _, p := range me.Problems {
				fmt.Fprintf(stderr, "  %s\n", p)
			}
			return 1
		}
		fmt.Fprintf(stderr, "windmill-lite verify: %v\n", err)
		return 1
	}
	if engineVersion := resolveVersion(); v.Manifest.Engine.Version != engineVersion {
		fmt.Fprintf(stderr, "windmill-lite verify: note: engine %s built the bundle; this engine is %s and would not open it\n",
			v.Manifest.Engine.Version, engineVersion)
	}
	fmt.Fprintln(stdout, v.Digest)
	return 0
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}
