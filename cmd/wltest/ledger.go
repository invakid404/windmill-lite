package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/oracle"
)

// The retarget ledger (conformance.LedgerPath) and the markers exist only
// on the retarget branch, rt/retarget. This file holds the guard that
// keeps them there, `wltest ledger`, and the decisions the ledger's
// readers share. The pull request that merges the branch into main deletes
// the ledger file, every marker and their readers; the guard runs on that
// pull request, and so is the last part to go.

const ledgerUsage = `usage: wltest ledger [--base BRANCH] [--repo DIR]
`

// ledger validates and prints the retarget ledger and every marker in the
// repository's files, and with --base main refuses both.
func ledger(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ledger", flag.ContinueOnError)
	fs.SetOutput(stderr)
	base := fs.String("base", "", "the branch the change goes into; main refuses the ledger file and every marker")
	repoDir := fs.String("repo", ".", "the repository's root directory")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprint(stderr, ledgerUsage)
		return 2
	}
	scan, err := scanLedger(*repoDir)
	if err != nil {
		fmt.Fprintln(stderr, "wltest:", err)
		return 1
	}
	scan.print(stdout)
	failed := false
	for _, m := range scan.malformed {
		fmt.Fprintf(stderr, "wltest: %s:%d: %v\n", m.file, m.line, m.err)
		failed = true
	}
	if *base == "main" {
		if scan.exists {
			fmt.Fprintf(stderr, "wltest: %s exists: a change into main deletes the retarget ledger\n", conformance.LedgerPath)
			failed = true
		}
		if len(scan.markers) > 0 {
			fmt.Fprintf(stderr, "wltest: %d retarget markers remain: a change into main removes every one\n", len(scan.markers))
			failed = true
		}
	}
	if failed {
		return 1
	}
	return 0
}

// marker is a marker found in a file, or an occurrence of
// conformance.MarkerPrefix in a test source that is not one.
type marker struct {
	file              string // from the repository root, slash-separated
	line              int
	milestone, reason string
	err               error // set for a malformed marker
}

// ledgerScan is what `wltest ledger` found.
type ledgerScan struct {
	exists    bool // the ledger file exists
	ledger    *conformance.Ledger
	markers   []marker
	malformed []marker
}

// scanLedger reads the ledger file and searches every file Git tracks or
// would track (untracked files that no ignore rule excludes) for markers.
// A marker anywhere counts. An occurrence of the prefix that is not a
// marker is malformed in a test source, a Go test or a bun test file,
// where it would skip a test without the guard seeing it, and elsewhere is
// prose, such as documentation of the form.
func scanLedger(repo string) (*ledgerScan, error) {
	p := filepath.Join(repo, filepath.FromSlash(conformance.LedgerPath))
	_, err := os.Stat(p)
	scan := &ledgerScan{exists: err == nil}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if scan.ledger, err = conformance.LoadLedger(p); err != nil {
		return nil, err
	}
	out, err := exec.Command("git", "-C", repo, "ls-files", "-z", "--cached", "--others", "--exclude-standard").Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			err = fmt.Errorf("%w: %s", err, bytes.TrimSpace(ee.Stderr))
		}
		return nil, fmt.Errorf("listing the files of %s: %w", repo, err)
	}
	names := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	slices.Sort(names)
	for _, name := range slices.Compact(names) {
		if name == "" {
			continue
		}
		f := filepath.Join(repo, filepath.FromSlash(name))
		// A file deleted from the work tree, a symbolic link or a
		// submodule holds no marker of its own.
		if info, err := os.Lstat(f); err != nil || !info.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		for _, m := range findMarkers(name, data) {
			if m.err != nil {
				scan.malformed = append(scan.malformed, m)
			} else {
				scan.markers = append(scan.markers, m)
			}
		}
	}
	return scan, nil
}

// testSource matches the files a marker skips tests in: Go test files and
// bun test files.
var testSource = regexp.MustCompile(`(_test\.go|\.test\.[cm]?[jt]sx?)$`)

// findMarkers returns the markers in one file's text, and the malformed
// ones of a test source. A marker that follows a quote, as the skip
// message of a Go test or a bun test's name does, ends at the closing
// quote; any other runs to the end of its line.
func findMarkers(name string, data []byte) []marker {
	if bytes.IndexByte(data, 0) >= 0 {
		return nil // not text
	}
	var found []marker
	for i, line := range strings.Split(string(data), "\n") {
		for at := 0; ; {
			k := strings.Index(line[at:], conformance.MarkerPrefix)
			if k < 0 {
				break
			}
			k += at
			at = k + len(conformance.MarkerPrefix)
			text := line[k:]
			if k > 0 && strings.ContainsRune("\"'`", rune(line[k-1])) {
				text = untilQuote(text, line[k-1])
			}
			text = strings.TrimRight(text, " \t\r")
			m := marker{file: name, line: i + 1}
			m.milestone, m.reason, m.err = conformance.ParseMarker(text)
			if m.err == nil || testSource.MatchString(name) {
				found = append(found, m)
			}
		}
	}
	return found
}

// untilQuote returns s up to its first quote q that no backslash escapes.
func untilQuote(s string, q byte) string {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case q:
			return s[:i]
		}
	}
	return s
}

// print lists the ledger's entries and the markers, then a count per
// milestone.
func (s *ledgerScan) print(w io.Writer) {
	perMilestone := map[string]int{}
	if !s.exists {
		fmt.Fprintf(w, "retarget ledger %s: absent\n", conformance.LedgerPath)
	} else {
		l := s.ledger
		fmt.Fprintf(w, "retarget ledger %s: %d flow-suite runs, %d step cases, %d oracle runs\n", conformance.LedgerPath, len(l.FlowSuite), len(l.StepSuite), len(l.Oracle))
		for _, sec := range []struct {
			name    string
			entries map[string]conformance.LedgerEntry
		}{{"flow_suite", l.FlowSuite}, {"step_suite", l.StepSuite}, {"oracle", l.Oracle}} {
			for _, item := range slices.Sorted(maps.Keys(sec.entries)) {
				e := sec.entries[item]
				modes := ""
				if len(e.Modes) > 0 {
					modes = " (" + strings.Join(e.Modes, ", ") + ")"
				}
				fmt.Fprintf(w, "  %s %s%s: %s\n", sec.name, item, modes, e.Marker())
				perMilestone[e.Milestone]++
			}
		}
	}
	fmt.Fprintf(w, "markers: %d\n", len(s.markers))
	for _, m := range s.markers {
		fmt.Fprintf(w, "  %s:%d: %s\n", m.file, m.line, conformance.LedgerEntry{Milestone: m.milestone, Reason: m.reason}.Marker())
		perMilestone[m.milestone]++
	}
	if len(perMilestone) > 0 {
		var counts []string
		for _, m := range slices.Sorted(maps.Keys(perMilestone)) {
			counts = append(counts, fmt.Sprintf("%s %d", m, perMilestone[m]))
		}
		fmt.Fprintf(w, "pending by milestone: %s\n", strings.Join(counts, ", "))
	}
}

// recordVerdict reports whether run r fails a record session, and a note
// to print below its line when the ledger's oracle section lists it
// (oracle.RunReport.PendingVerdict).
func recordVerdict(r oracle.RunReport, check bool, pending map[string]conformance.LedgerEntry) (fails bool, note string) {
	e, listed := pending[r.Fixture+"/"+r.Run]
	if !listed {
		return r.Failed(check), ""
	}
	return r.PendingVerdict(check, e)
}

// A suite verdict is how a run of the flow suite or a step case went in one
// mode, given its ledger entry.
type suiteVerdict int

const (
	verdictPass    suiteVerdict = iota // passed, and no entry covers the mode
	verdictFail                        // failed, and no entry covers the mode: an error
	verdictPending                     // failed in a mode its entry covers: logged with the marker
	verdictStale                       // passed in a mode its entry covers: an error, since the entry hides nothing
)

// judgeSuite decides a suite verdict; listed reports whether the ledger
// has an entry e for the run or case.
func judgeSuite(failed bool, e conformance.LedgerEntry, listed bool, mode string) suiteVerdict {
	covered := listed && e.Covers(mode)
	switch {
	case failed && covered:
		return verdictPending
	case failed:
		return verdictFail
	case covered:
		return verdictStale
	}
	return verdictPass
}
