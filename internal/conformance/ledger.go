package conformance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
)

// The retarget ledger lists what a pull request into the retarget branch
// (rt/retarget) knowingly leaves failing while windmill-lite moves to a new
// Windmill and Bun, each item with the retarget milestone that clears it and
// why. It exists only on that branch: the pull request that merges the
// branch into main deletes the ledger file and every marker, and
// `wltest ledger --base main` refuses either (cmd/wltest/README.md, "The
// retarget ledger").
const (
	// LedgerPath is the ledger file's path from the repository root.
	LedgerPath = "cmd/wltest/testdata/flowsuite-pending.json"
	// MarkerPrefix starts a marker, the skip message of a Go test or the
	// name of a skipped bun test that a retarget milestone clears:
	// MarkerPrefix, a space, the milestone, a space and the reason.
	MarkerPrefix = "retarget-pending:"
)

// milestoneID is the form of a retarget milestone's id, the prefix of its
// pull request's title: a letter, a number and an optional lower-case
// letter.
var milestoneID = regexp.MustCompile(`^[WX][0-9]+[a-z]?$`)

// LedgerEntry is one pending item: the milestone that clears it, why it is
// pending, and, for a suite that runs in several modes, the modes it fails
// in (none means every mode).
type LedgerEntry struct {
	Milestone string   `json:"milestone"`
	Reason    string   `json:"reason"`
	Modes     []string `json:"modes,omitempty"`
}

// Marker is the entry in marker form, which logs print.
func (e LedgerEntry) Marker() string {
	return MarkerPrefix + " " + e.Milestone + " " + e.Reason
}

// Covers reports whether the entry holds in mode: in every mode when it
// names none.
func (e LedgerEntry) Covers(mode string) bool {
	return len(e.Modes) == 0 || slices.Contains(e.Modes, mode)
}

// Ledger is the ledger file. Each section maps an item, named from the
// repository root, to its entry:
//   - FlowSuite: required runs of wltest's flow suite (<fixture
//     directory>/<run>, or <expression file>/<case>) that may fail or be
//     unsupported in the modes the entry covers;
//   - StepSuite: step cases (<step file>/<case>) that may fail in the modes
//     the entry covers;
//   - Oracle: runs, named as for FlowSuite, whose recording is pending:
//     `wltest record --check` may report them new, changed,
//     expect_mismatch, failed or unstable, and the recording tests accept
//     them without a recording. Oracle entries name no modes.
type Ledger struct {
	About     string                 `json:"about"`
	FlowSuite map[string]LedgerEntry `json:"flow_suite"`
	StepSuite map[string]LedgerEntry `json:"step_suite"`
	Oracle    map[string]LedgerEntry `json:"oracle"`
}

// Len is the number of entries in all sections.
func (l *Ledger) Len() int {
	return len(l.FlowSuite) + len(l.StepSuite) + len(l.Oracle)
}

// LoadLedger reads the ledger file at p. A missing file is an empty ledger:
// nothing is pending.
func LoadLedger(p string) (*Ledger, error) {
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return &Ledger{}, nil
	}
	if err != nil {
		return nil, err
	}
	l, err := ParseLedger(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	return l, nil
}

// ParseLedger decodes a ledger file strictly and validates every entry:
// the item is a clean relative path with a directory, the milestone has a
// milestone's form, the reason is one non-empty line, the modes are
// distinct and non-empty, and an Oracle entry names none.
func ParseLedger(data []byte) (*Ledger, error) {
	var l Ledger
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&l); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("data after the ledger object")
	}
	var errs []error
	for _, s := range []struct {
		name    string
		entries map[string]LedgerEntry
	}{{"flow_suite", l.FlowSuite}, {"step_suite", l.StepSuite}, {"oracle", l.Oracle}} {
		for _, item := range slices.Sorted(maps.Keys(s.entries)) {
			e := s.entries[item]
			if err := checkLedgerItem(item); err != nil {
				errs = append(errs, fmt.Errorf("%s %q: %w", s.name, item, err))
			}
			if err := checkMarkerParts(e.Milestone, e.Reason); err != nil {
				errs = append(errs, fmt.Errorf("%s %s: %w", s.name, item, err))
			}
			if s.name == "oracle" && len(e.Modes) > 0 {
				errs = append(errs, fmt.Errorf("oracle %s: an oracle entry names no modes", item))
			}
			for i, m := range e.Modes {
				if m == "" || strings.TrimSpace(m) != m {
					errs = append(errs, fmt.Errorf("%s %s: mode %q is not a mode name", s.name, item, m))
				}
				if slices.Contains(e.Modes[:i], m) {
					errs = append(errs, fmt.Errorf("%s %s: mode %s is listed twice", s.name, item, m))
				}
			}
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return &l, nil
}

// checkLedgerItem requires an item name to be a clean slash-separated path
// from the repository root, with a directory part.
func checkLedgerItem(item string) error {
	switch {
	case item == "" || strings.TrimSpace(item) != item:
		return errors.New("not an item name")
	case strings.Contains(item, `\`):
		return errors.New("not slash-separated")
	case path.IsAbs(item) || item == ".." || strings.HasPrefix(item, "../") || path.Clean(item) != item:
		return errors.New("not a clean path from the repository root")
	case !strings.Contains(item, "/"):
		return errors.New("names no directory or file before the run or case")
	}
	return nil
}

// ParseMarker reads a marker: MarkerPrefix, one space, a milestone id, one
// space and a reason, which runs to the end of s and must not be empty.
func ParseMarker(s string) (milestone, reason string, err error) {
	rest, ok := strings.CutPrefix(s, MarkerPrefix+" ")
	if !ok {
		return "", "", fmt.Errorf("%q does not start with %q and a space", s, MarkerPrefix)
	}
	milestone, reason, _ = strings.Cut(rest, " ")
	if err := checkMarkerParts(milestone, reason); err != nil {
		return "", "", fmt.Errorf("%q: %w", s, err)
	}
	return milestone, reason, nil
}

// checkMarkerParts requires a milestone id of milestoneID's form and a
// reason of one non-empty line without surrounding space.
func checkMarkerParts(milestone, reason string) error {
	switch {
	case !milestoneID.MatchString(milestone):
		return fmt.Errorf("milestone %q is not a milestone id such as W4c or X1", milestone)
	case reason == "":
		return errors.New("no reason")
	case strings.TrimSpace(reason) != reason || strings.ContainsAny(reason, "\r\n"):
		return fmt.Errorf("reason %q is not one line without surrounding space", reason)
	}
	return nil
}
