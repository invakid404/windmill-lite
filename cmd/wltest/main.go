// Command wltest is windmill-lite's conformance and oracle runner (ADR
// 0011). It has one verb so far:
//
//	wltest record [flags] <fixture or directory>...
//
// record runs flow fixtures on the pinned Windmill CE oracle and writes
// their recordings with provenance. check, diff and stub are later
// milestones (E1, E4a).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/oracle"
)

// version is set at build time with -ldflags "-X main.version=…".
var version = ""

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

const usage = `usage: wltest record [flags] <fixture or directory>...

record runs flow fixtures on the Windmill CE v1.767.0 oracle and writes
recorded/<run>.json with provenance. Run wltest record -h for its flags.
check, diff and stub are not implemented yet.
`

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "record":
		return record(ctx, args[1:], stdout, stderr)
	case "check", "diff", "stub":
		fmt.Fprintf(stderr, "wltest %s is not implemented yet (ADR 0012: E1, E4a)\n", args[0])
		return 2
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	}
	fmt.Fprint(stderr, usage)
	return 2
}

func record(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("record", flag.ContinueOnError)
	fs.SetOutput(stderr)
	check := fs.Bool("check", false, "fail when a run has no committed recording or its observation differs from it; never write into fixtures")
	out := fs.String("out", "", "write new and changed recordings under this directory instead of in place")
	raw := fs.String("raw", "", "write the raw API captures of every run under this directory")
	report := fs.String("report", "", "write the session report (JSON) to this file")
	repeat := fs.Int("repeat", 1, "run every run this many times and require equal observations")
	regen := fs.Bool("regenerate-metadata", false, "run folder add-missing and generate-metadata in each fixture's sync tree before pushing (the drift gate)")
	workers := fs.Int("workers", 1, "worker containers: 1, or 2 for fixtures with blocking dispatch")
	cli := fs.String("cli", strings.Join(oracle.DefaultCLICommand, " "), "command that runs "+oracle.CLIPackage)
	dataRoot := fs.String("docker-data-root", "", "directory whose filesystem the disk preflight measures (default: docker's data root)")
	sinkListen := fs.String("sink-listen", "0.0.0.0:0", "listen address of the effect sink")
	runTimeout := fs.Duration("run-timeout", 5*time.Minute, "timeout of one run; less than 60s is raised to 60s, and a timed-out run is canceled")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 || *workers < 1 || *workers > 2 || *repeat < 1 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	dirs, err := conformance.FindFixtures(fs.Args()...)
	if err != nil {
		fmt.Fprintln(stderr, "wltest:", err)
		return 1
	}
	if len(dirs) == 0 {
		fmt.Fprintln(stderr, "wltest:", oracle.ErrNoFixtures)
		return 1
	}
	var fixtures []*conformance.Fixture
	for _, d := range dirs {
		f, err := conformance.LoadFixture(d)
		if err != nil {
			fmt.Fprintln(stderr, "wltest:", err)
			return 1
		}
		fixtures = append(fixtures, f)
	}
	root, _ := os.Getwd()
	s := &oracle.Session{
		Docker:             oracle.ExecDocker{},
		CLI:                oracle.CLI{Command: strings.Fields(*cli), Stderr: stderr},
		Workers:            *workers,
		Log:                stderr,
		DataRoot:           *dataRoot,
		SinkListen:         *sinkListen,
		RunTimeout:         *runTimeout,
		Repeat:             *repeat,
		RegenerateMetadata: *regen,
		Check:              *check,
		OutDir:             *out,
		RawDir:             *raw,
		RepoRoot:           root,
		Harness:            harnessInfo(),
	}
	rep, runErr := s.Run(ctx, fixtures)
	if *report != "" {
		if err := writeJSON(*report, rep); err != nil {
			fmt.Fprintln(stderr, "wltest:", err)
			return 1
		}
	}
	failed := runErr != nil
	if runErr != nil {
		var r *oracle.Refusal
		if errors.As(runErr, &r) {
			fmt.Fprintln(stdout, string(r.JSON()))
		}
		fmt.Fprintln(stderr, "wltest:", runErr)
	}
	for _, r := range rep.Runs {
		line := fmt.Sprintf("%-10s %s %s", r.Outcome, r.Fixture, r.Run)
		if r.Wrote != "" {
			line += " -> " + r.Wrote
		}
		fmt.Fprintln(stdout, line)
		if r.Error != "" {
			fmt.Fprintln(stdout, "  error:", r.Error)
		}
		for _, d := range r.Differences {
			fmt.Fprintln(stdout, "  "+d)
		}
		if r.Failed(*check) {
			failed = true
		}
	}
	if failed {
		return 1
	}
	return 0
}

func writeJSON(p string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, append(data, '\n'), 0o644)
}

// harnessInfo identifies this build for provenance: the version from
// ldflags or the module, and the VCS revision Go stamped into it.
func harnessInfo() oracle.HarnessInfo {
	h := oracle.HarnessInfo{Name: "wltest", Version: version}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return h
	}
	h.Go = bi.GoVersion
	if h.Version == "" {
		h.Version = bi.Main.Version
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			h.Revision = s.Value
		case "vcs.modified":
			h.Modified = s.Value == "true"
		}
	}
	return h
}
