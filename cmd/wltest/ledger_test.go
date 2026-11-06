package main

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/oracle"
)

// This file builds every marker from conformance.MarkerPrefix: the guard
// reads a marker in any test source, so a literal one here would be a real
// marker, which a change into main must not carry.

// gitTree writes files into a new Git repository and returns its root. The
// files stay untracked: the guard reads untracked files that no ignore rule
// excludes, as it reads tracked ones.
func gitTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	writeTree(t, dir, files)
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	return dir
}

// TestLedgerGuard is the guard's exit gate: `wltest ledger --base main`
// fails while the ledger file exists, with an entry or empty, or while a
// marker remains in a Go test, a bun test or any other file, and passes
// without them; with another base it lists the entries and markers and
// passes. A malformed marker in a test source, and a malformed ledger,
// fail with any base.
func TestLedgerGuard(t *testing.T) {
	m := func(milestone, reason string) string {
		return conformance.LedgerEntry{Milestone: milestone, Reason: reason}.Marker()
	}
	ledgerFile := `{"about": "a", "flow_suite": {"conformance/flows/oracle/probes/env": {"milestone": "W8c", "reason": "the image's Bun", "modes": ["warm"]}}, "step_suite": {}, "oracle": {}}`
	for _, tc := range []struct {
		name  string
		files map[string]string
		// Exit codes with --base main and with --base rt/retarget.
		main, other int
		out, errOut []string
	}{
		{
			name: "nothing pending",
			files: map[string]string{
				"README.md": "A marker is `" + conformance.MarkerPrefix + " <milestone> <reason>`.\n",
				"x/ok.go":   "package x\n\nconst p = \"" + conformance.MarkerPrefix + "\"\n",
			},
			out: []string{"retarget ledger " + conformance.LedgerPath + ": absent", "markers: 0"},
		},
		{
			name:   "a ledger entry",
			files:  map[string]string{conformance.LedgerPath: ledgerFile},
			main:   1,
			out:    []string{"1 flow-suite runs, 0 step cases, 0 oracle runs", "  flow_suite conformance/flows/oracle/probes/env (warm): " + m("W8c", "the image's Bun"), "pending by milestone: W8c 1"},
			errOut: []string{conformance.LedgerPath + " exists: a change into main deletes the retarget ledger"},
		},
		{
			name:   "an empty ledger",
			files:  map[string]string{conformance.LedgerPath: `{"about": "a", "flow_suite": {}, "step_suite": {}, "oracle": {}}`},
			main:   1,
			out:    []string{"0 flow-suite runs, 0 step cases, 0 oracle runs", "markers: 0"},
			errOut: []string{"exists: a change into main deletes the retarget ledger"},
		},
		{
			name: "a Go test's marker",
			files: map[string]string{"internal/x/x_test.go": "package x\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {\n\tt.Skip(\"" +
				m("W4c", `Bun 1.4.3's process.env refuses accessors, so the \"view\" breaks`) + "\")\n}\n"},
			main:   1,
			out:    []string{"markers: 1", "  internal/x/x_test.go:6: " + m("W4c", `Bun 1.4.3's process.env refuses accessors, so the \"view\" breaks`), "pending by milestone: W4c 1"},
			errOut: []string{"1 retarget markers remain: a change into main removes every one"},
		},
		{
			name:   "a bun test's marker",
			files:  map[string]string{"harnesses/bun/test/native.test.ts": "test.skip('" + m("W4f", "a BYOB request now refuses respond(0)") + ": events reach the realm', () => {});\n"},
			main:   1,
			out:    []string{"  harnesses/bun/test/native.test.ts:1: " + m("W4f", "a BYOB request now refuses respond(0): events reach the realm")},
			errOut: []string{"1 retarget markers remain"},
		},
		{
			name:   "a marker in another file",
			files:  map[string]string{"conformance/steps/bun/x.yaml": "note: " + m("W4d", "the text moved") + "\n"},
			main:   1,
			out:    []string{"  conformance/steps/bun/x.yaml:1: " + m("W4d", "the text moved")},
			errOut: []string{"1 retarget markers remain"},
		},
		{
			name:   "a malformed marker in a test",
			files:  map[string]string{"x/x_test.go": "package x\n// " + conformance.MarkerPrefix + " w4c lower case\n"},
			main:   1,
			other:  1,
			errOut: []string{"x/x_test.go:2: ", `milestone "w4c" is not a milestone id`},
		},
		{
			name:   "a malformed ledger",
			files:  map[string]string{conformance.LedgerPath: `{"about": "a", "oracle": {"x/y": {"milestone": "W5a"}}}`},
			main:   1,
			other:  1,
			errOut: []string{"oracle x/y: no reason"},
		},
		{
			name:  "an ignored file",
			files: map[string]string{".gitignore": "/out/\n", "out/x_test.go": "// " + m("W4c", "r") + "\n"},
			out:   []string{"markers: 0"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := gitTree(t, tc.files)
			for _, b := range []struct {
				base string
				code int
			}{{"main", tc.main}, {"rt/retarget", tc.other}} {
				var stdout, stderr bytes.Buffer
				code := run(context.Background(), []string{"ledger", "--base", b.base, "--repo", dir}, &stdout, &stderr)
				if code != b.code {
					t.Errorf("--base %s: exit %d, want %d\nstdout:\n%s\nstderr:\n%s", b.base, code, b.code, stdout.String(), stderr.String())
				}
				for _, want := range tc.out {
					if !strings.Contains(stdout.String(), want) {
						t.Errorf("--base %s: stdout lacks %q:\n%s", b.base, want, stdout.String())
					}
				}
				if b.code == 1 {
					for _, want := range tc.errOut {
						if !strings.Contains(stderr.String(), want) {
							t.Errorf("--base %s: stderr lacks %q:\n%s", b.base, want, stderr.String())
						}
					}
				} else if stderr.Len() > 0 {
					t.Errorf("--base %s: stderr %q", b.base, stderr.String())
				}
			}
		})
	}

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"ledger", "x"}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "usage: wltest ledger") {
		t.Errorf("an argument: exit %d, %q", code, stderr.String())
	}
	if code := run(context.Background(), []string{"ledger", "--repo", t.TempDir()}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "listing the files of") {
		t.Errorf("not a repository: exit %d, %q", code, stderr.String())
	}
}

// TestRetargetLedger holds the committed ledger to the suites and fixtures
// it names: a flow_suite entry names a run the flow suite requires and
// modes of TestFlowSuite, a step_suite entry names a step case and the
// modes warm and fresh, and an oracle entry names a run of a fixture under
// conformance/flows or a case of an expression file. It needs no Bun, so
// it holds wherever the suites skip.
func TestRetargetLedger(t *testing.T) {
	l := retargetLedger(t)
	root := repo(t)
	rel := func(p string) string {
		r, err := filepath.Rel(root, mustAbs(t, p))
		if err != nil {
			t.Fatal(err)
		}
		return filepath.ToSlash(r)
	}
	checkModes := func(section, item string, modes, valid []string) {
		for _, mode := range modes {
			if !slices.Contains(valid, mode) {
				t.Errorf("%s %s: mode %s is not one of %v", section, item, mode, valid)
			}
		}
	}

	req := requiredRuns(t)
	var flowModes []string
	for _, m := range flowSuiteModes {
		flowModes = append(flowModes, m.name())
	}
	for item, e := range l.FlowSuite {
		if _, ok := req[item]; !ok {
			t.Errorf("flow_suite %s is not a run the flow suite requires", item)
		}
		checkModes("flow_suite", item, e.Modes, flowModes)
	}

	files, err := conformance.FindStepFiles(stepsDir)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]bool{}
	for _, p := range files {
		f, err := conformance.LoadStepFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for i := range f.Cases {
			cases[rel(p)+"/"+f.Cases[i].Name()] = true
		}
	}
	for item, e := range l.StepSuite {
		if !cases[item] {
			t.Errorf("step_suite %s is not a step case", item)
		}
		checkModes("step_suite", item, e.Modes, []string{conformance.ModeWarm, conformance.ModeFresh})
	}

	dirs, err := conformance.FindFixtures(flowsDir)
	if err != nil {
		t.Fatal(err)
	}
	runs := map[string]bool{}
	for _, d := range dirs {
		f, err := conformance.LoadFixture(d)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range f.Runs {
			runs[rel(d)+"/"+r.Name] = true
		}
	}
	exprs, err := conformance.FindExprFiles(exprsDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range exprs {
		ef, err := conformance.LoadExprFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for i := range ef.Cases {
			runs[rel(p)+"/"+ef.Cases[i].Name()] = true
		}
	}
	for item := range l.Oracle {
		if !runs[item] {
			t.Errorf("oracle %s is not a run of a fixture under %s or a case of an expression file under %s", item, flowsDir, exprsDir)
		}
	}
	t.Logf("retarget ledger: %d flow-suite runs, %d step cases, %d oracle runs", len(l.FlowSuite), len(l.StepSuite), len(l.Oracle))
}

// A run the ledger's oracle section lists may be new, changed,
// expect_mismatch, failed or unstable without failing the session, and its
// line says so; unchanged, its entry is stale and fails a check session; a
// refusal still fails; a run the ledger does not list is judged as always.
func TestRecordVerdict(t *testing.T) {
	e := conformance.LedgerEntry{Milestone: "W5b", Reason: "step errors gain job_id"}
	pending := map[string]conformance.LedgerEntry{"conformance/flows/x/f/r": e}
	for _, tc := range []struct {
		outcome, fixture string
		check, fails     bool
		note             string
	}{
		{oracle.OutcomeExpectMismatch, "conformance/flows/x/f", true, false, "pending: " + e.Marker()},
		{oracle.OutcomeNew, "conformance/flows/x/f", true, false, "pending: "},
		{oracle.OutcomeChanged, "conformance/flows/x/f", true, false, "pending: "},
		{oracle.OutcomeFailed, "conformance/flows/x/f", true, false, "pending: "},
		{oracle.OutcomeUnstable, "conformance/flows/x/f", true, false, "pending: "},
		{oracle.OutcomeExpectMismatch, "conformance/flows/x/f", false, false, "pending: "},
		{oracle.OutcomeUnchanged, "conformance/flows/x/f", true, true, "stale: the run is unchanged; remove its entry from the retarget ledger (" + e.Marker() + ")"},
		{oracle.OutcomeUnchanged, "conformance/flows/x/f", false, false, "stale: "},
		{oracle.OutcomeRefused, "conformance/flows/x/f", true, true, ""},
		{oracle.OutcomeSkipped, "conformance/flows/x/f", true, false, ""},
		{oracle.OutcomeExpectMismatch, "conformance/flows/x/g", true, true, ""},
		{oracle.OutcomeNew, "conformance/flows/x/g", true, true, ""},
		{oracle.OutcomeNew, "conformance/flows/x/g", false, false, ""},
	} {
		r := oracle.RunReport{Fixture: tc.fixture, Run: "r", Outcome: tc.outcome}
		fails, note := recordVerdict(r, tc.check, pending)
		if fails != tc.fails || (tc.note == "") != (note == "") || !strings.HasPrefix(note, tc.note) {
			t.Errorf("%s %s, check %v: fails %v, note %q; want %v, %q", tc.outcome, tc.fixture, tc.check, fails, note, tc.fails, tc.note)
		}
	}

	// printRuns prints the note below the run's line and its differences,
	// and fails the session only for the run outside the ledger.
	var out bytes.Buffer
	failed := printRuns(&out, []oracle.RunReport{
		{Fixture: "conformance/flows/x/f", Run: "r", Outcome: oracle.OutcomeExpectMismatch, Differences: []string{`/status: "failure" != "success"`}, Wrote: ""},
		{Fixture: "conformance/flows/x/g", Run: "r", Outcome: oracle.OutcomeUnchanged},
	}, true, pending)
	want := "expect_mismatch conformance/flows/x/f r\n  /status: \"failure\" != \"success\"\n  pending: " + e.Marker() + "\nunchanged  conformance/flows/x/g r\n"
	if failed || out.String() != want {
		t.Errorf("failed %v, output:\n%s\nwant:\n%s", failed, out.String(), want)
	}
	if !printRuns(&out, []oracle.RunReport{{Fixture: "conformance/flows/x/g", Run: "r", Outcome: oracle.OutcomeExpectMismatch}}, true, pending) {
		t.Error("an unlisted expect_mismatch did not fail the session")
	}
}

// A suite run or case fails unless an entry covers the mode, and passing
// in a mode its entry covers fails as a stale entry.
func TestJudgeSuite(t *testing.T) {
	all := conformance.LedgerEntry{Milestone: "W4d", Reason: "r"}
	warm := conformance.LedgerEntry{Milestone: "W4e", Reason: "r", Modes: []string{"warm"}}
	for _, tc := range []struct {
		failed bool
		e      conformance.LedgerEntry
		listed bool
		mode   string
		want   suiteVerdict
	}{
		{false, conformance.LedgerEntry{}, false, "warm", verdictPass},
		{true, conformance.LedgerEntry{}, false, "warm", verdictFail},
		{true, all, true, "fresh", verdictPending},
		{false, all, true, "fresh", verdictStale},
		{true, warm, true, "warm", verdictPending},
		{true, warm, true, "fresh", verdictFail},
		{false, warm, true, "fresh", verdictPass},
		{false, warm, true, "warm", verdictStale},
	} {
		if got := judgeSuite(tc.failed, tc.e, tc.listed, tc.mode); got != tc.want {
			t.Errorf("failed %v, entry %+v (listed %v), mode %s: %d, want %d", tc.failed, tc.e, tc.listed, tc.mode, got, tc.want)
		}
	}
}
