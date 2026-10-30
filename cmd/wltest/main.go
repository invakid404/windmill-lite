// Command wltest is windmill-lite's conformance and oracle runner (ADR
// 0011). It has three verbs so far:
//
//	wltest record [flags] <fixture, expression file or directory>...
//	wltest stub --out <dir> <sync tree>
//	wltest check [flags] <step file, fixture or directory>...
//
// record runs flow fixtures, and the expression cases of conformance/exprs
// lifted into generated one-step flows, on the pinned Windmill CE oracle
// and writes their recordings with provenance; fixtures in stub mode run
// stubbed, with the replay service answering from each run's table. stub
// writes a stubbed copy of a sync tree. check runs the step cases of
// conformance/steps on windmill-lite's Bun harness (milestone C3a) and the
// flow fixtures of conformance/flows on its engine (milestone E1). diff,
// which would run the oracle and windmill-lite live and compare them, is
// not implemented: no milestone of ADR 0012 has it yet.
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
	"sort"
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

const usage = `usage: wltest record [flags] <fixture, expression file or directory>...
       wltest stub --out <dir> <sync tree>
       wltest check [flags] <step file, fixture or directory>...

record runs flow fixtures, and expression-case files lifted into generated
one-step flows, on the Windmill CE v1.767.0 oracle and writes their
recordings with provenance. Run wltest record -h for its flags.
stub copies a sync tree to <dir> with every Bun script body replaced by a
replay stub (ADR 0011).
check runs step-case files (conformance/steps) on windmill-lite's Bun
harness, in warm mode or with --mode fresh in one process per run, and
compares each step's stored value with the case's, and runs flow fixtures
(conformance/flows) on windmill-lite, in warm mode, and compares each
run's observation with its expect or recording. Run wltest check -h for
its flags. diff is not implemented yet.
`

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "record":
		return record(ctx, args[1:], stdout, stderr)
	case "stub":
		return stub(args[1:], stdout, stderr)
	case "check":
		return check(ctx, args[1:], stdout, stderr)
	case "diff":
		fmt.Fprintf(stderr, "wltest %s is not implemented yet (ADR 0011; no milestone of ADR 0012 has it): wltest check compares windmill-lite with the committed recordings\n", args[0])
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
	skipUnmet := fs.Bool("skip-unmet-workers", false, "skip fixtures that need more workers than --workers, reporting their runs as skipped, instead of refusing them")
	listen := fs.Int("listen-interval", 0, "the oracle's LISTEN_NEW_EVENTS_INTERVAL_SEC (default 1), for the knob comparison of OR-7/a")
	session := fs.String("session", "", "the oracle configuration to run (native, result_size, flow_script_off, long; default the reference configuration); only the fixtures that need it are recorded")
	skipSession := fs.Bool("skip-unmet-session", false, "skip fixtures that need another oracle configuration than --session, reporting their runs as skipped, instead of refusing them")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 || *workers < 1 || *workers > 2 || *repeat < 1 || *listen < 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	// An explicit file must be an expression file: anything else would be
	// silently skipped.
	for _, a := range fs.Args() {
		if info, err := os.Stat(a); err == nil && !info.IsDir() && !conformance.IsExprFile(a) {
			fmt.Fprintf(stderr, "wltest: %s is neither a fixture directory nor an expression file (exprs/<finding>.yaml)\n", a)
			return 2
		}
	}
	found, err := conformance.FindFixtures(fs.Args()...)
	if err != nil {
		fmt.Fprintln(stderr, "wltest:", err)
		return 1
	}
	// Overlapping arguments reach a fixture once.
	var dirs []string
	seen := map[string]bool{}
	for _, d := range found {
		if !seen[filepath.Clean(d)] {
			seen[filepath.Clean(d)] = true
			dirs = append(dirs, d)
		}
	}
	exprFiles, err := conformance.FindExprFiles(fs.Args()...)
	if err != nil {
		fmt.Fprintln(stderr, "wltest:", err)
		return 1
	}
	if len(dirs)+len(exprFiles) == 0 {
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
	// Expression cases are lifted into generated one-step flow fixtures
	// (ADR 0011), whose trees live only for the session.
	if len(exprFiles) > 0 {
		gen, err := os.MkdirTemp("", "wltest-exprs-")
		if err != nil {
			fmt.Fprintln(stderr, "wltest:", err)
			return 1
		}
		defer os.RemoveAll(gen)
		var files []*conformance.ExprFile
		for _, p := range exprFiles {
			ef, err := conformance.LoadExprFile(p)
			if err != nil {
				fmt.Fprintln(stderr, "wltest:", err)
				return 1
			}
			files = append(files, ef)
		}
		// Slow cases go last, so that whatever they leave running on the
		// oracle (JE-2) cannot slow the others.
		sort.SliceStable(files, func(i, j int) bool { return !files[i].Slow && files[j].Slow })
		for i, ef := range files {
			f, err := conformance.LiftExprFile(ef, filepath.Join(gen, fmt.Sprint(i)))
			if err != nil {
				fmt.Fprintln(stderr, "wltest:", err)
				return 1
			}
			fixtures = append(fixtures, f)
		}
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
		SkipUnmetWorkers:   *skipUnmet,
		ListenInterval:     *listen,
		Config:             *session,
		SkipUnmetSession:   *skipSession,
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

// stub writes the stubbed copy of a sync tree (ADR 0011 stub mode). Its
// metadata still describes the original: run the CLI's generate-metadata
// in the copied tree before pushing it, as record does for stub fixtures.
// When the tree's codebase entries name paths outside it, the copied tree
// is below <dir>, with those paths beside it (oracle.Layout).
func stub(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("stub", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "", "directory to write the stubbed tree to; it must not exist")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 || *out == "" {
		fmt.Fprint(stderr, usage)
		return 2
	}
	rep, err := oracle.StubTree(fs.Arg(0), *out)
	if err != nil {
		var r *oracle.Refusal
		if errors.As(err, &r) {
			fmt.Fprintln(stdout, string(r.JSON()))
		}
		fmt.Fprintln(stderr, "wltest:", err)
		return 1
	}
	for _, f := range rep.Files {
		fmt.Fprintln(stdout, "stubbed", f)
	}
	for _, o := range rep.Outside {
		fmt.Fprintln(stdout, "carried", o)
	}
	for _, d := range rep.Dirs {
		fmt.Fprintln(stdout, "made", d)
	}
	tree := *out
	if rep.Root != "." {
		fmt.Fprintln(stdout, "tree", rep.Root)
		tree = filepath.Join(*out, filepath.FromSlash(rep.Root))
	}
	fmt.Fprintf(stderr, "wltest: run %s generate-metadata in %s before pushing it\n", oracle.CLIPackage, tree)
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
