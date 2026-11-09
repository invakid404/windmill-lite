package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// A branch that moves windmill-lite to a new Windmill or Bun version may
// list the failures its pull requests knowingly leave in a retarget ledger:
// the ledger file and markers in test code. No test reads the ledger file,
// and a marker skips a test without anything failing. This file holds the
// guard that keeps both out of every tree CI checks, `wltest ledger`
// (cmd/wltest/README.md, "The ledger guard").
const (
	// ledgerPath is the ledger file's path from the repository root.
	ledgerPath = "cmd/wltest/testdata/flowsuite-pending.json"
	// markerPrefix starts a marker, the skip message of a Go test or the
	// name of a skipped bun test: markerPrefix, a space, a milestone id, a
	// space and the reason.
	markerPrefix = "retarget-pending:"
)

// milestoneID is the form of a milestone's id, the prefix of its pull
// request's title: a letter, a number and an optional lower-case letter.
var milestoneID = regexp.MustCompile(`^[WX][0-9]+[a-z]?$`)

const ledgerUsage = `usage: wltest ledger [--repo DIR]
`

// ledger fails while the ledger file exists or any marker remains in the
// repository's files, and prints each marker it finds.
func ledger(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ledger", flag.ContinueOnError)
	fs.SetOutput(stderr)
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
	if scan.exists {
		fmt.Fprintf(stderr, "wltest: %s exists: delete the retarget ledger\n", ledgerPath)
		failed = true
	}
	if len(scan.markers) > 0 {
		fmt.Fprintf(stderr, "wltest: %d retarget markers remain: remove every one\n", len(scan.markers))
		failed = true
	}
	if failed {
		return 1
	}
	return 0
}

// marker is a marker found in a file, or an occurrence of markerPrefix in
// a test source that is not one.
type marker struct {
	file              string // from the repository root, slash-separated
	line              int
	milestone, reason string
	err               error // set for a malformed marker
}

// ledgerScan is what `wltest ledger` found.
type ledgerScan struct {
	exists    bool // the ledger file exists
	markers   []marker
	malformed []marker
}

// scanLedger checks for the ledger file and searches every file Git tracks
// or would track (untracked files that no ignore rule excludes) for
// markers. A marker anywhere counts. An occurrence of the prefix that is
// not a marker is malformed in a test source, a Go test or a bun test
// file, where it could skip a test, and elsewhere is prose, such as
// documentation of the form.
func scanLedger(repo string) (*ledgerScan, error) {
	_, err := os.Stat(filepath.Join(repo, filepath.FromSlash(ledgerPath)))
	scan := &ledgerScan{exists: err == nil}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
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
			k := strings.Index(line[at:], markerPrefix)
			if k < 0 {
				break
			}
			k += at
			at = k + len(markerPrefix)
			text := line[k:]
			if k > 0 && strings.ContainsRune("\"'`", rune(line[k-1])) {
				text = untilQuote(text, line[k-1])
			}
			text = strings.TrimRight(text, " \t\r")
			m := marker{file: name, line: i + 1}
			m.milestone, m.reason, m.err = parseMarker(text)
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

// parseMarker reads a marker: markerPrefix, one space, a milestone id of
// milestoneID's form, one space and a reason, which runs to the end of s
// and is one non-empty line without surrounding space.
func parseMarker(s string) (milestone, reason string, err error) {
	rest, ok := strings.CutPrefix(s, markerPrefix+" ")
	if !ok {
		return "", "", fmt.Errorf("%q does not start with %q and a space", s, markerPrefix)
	}
	milestone, reason, _ = strings.Cut(rest, " ")
	switch {
	case !milestoneID.MatchString(milestone):
		return "", "", fmt.Errorf("%q: milestone %q is not a milestone id such as W4c or X1", s, milestone)
	case reason == "":
		return "", "", fmt.Errorf("%q: no reason", s)
	case strings.TrimSpace(reason) != reason || strings.ContainsAny(reason, "\r\n"):
		return "", "", fmt.Errorf("%q: reason %q is not one line without surrounding space", s, reason)
	}
	return milestone, reason, nil
}

// print says whether the ledger file exists and lists the markers.
func (s *ledgerScan) print(w io.Writer) {
	state := "absent"
	if s.exists {
		state = "present"
	}
	fmt.Fprintf(w, "retarget ledger %s: %s\n", ledgerPath, state)
	fmt.Fprintf(w, "markers: %d\n", len(s.markers))
	for _, m := range s.markers {
		fmt.Fprintf(w, "  %s:%d: %s %s %s\n", m.file, m.line, markerPrefix, m.milestone, m.reason)
	}
}
