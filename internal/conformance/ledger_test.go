package conformance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This file builds every marker from MarkerPrefix: `wltest ledger` reads a
// marker in any test source, so a literal one here would be a real marker.

// A ledger with an entry in each section parses, and its entries keep
// their milestone, reason and modes.
func TestParseLedger(t *testing.T) {
	l, err := ParseLedger([]byte(`{
  "about": "a",
  "flow_suite": {"conformance/flows/x/y/run": {"milestone": "W8c", "reason": "the image's Bun", "modes": ["warm", "host"]}},
  "step_suite": {"conformance/steps/bun/d11.yaml/d11-case": {"milestone": "W4d", "reason": "Bun's new text"}},
  "oracle": {"conformance/exprs/je-1.yaml/je-1-a": {"milestone": "W5f", "reason": "the wrapper's columns moved"}}
}`))
	if err != nil {
		t.Fatal(err)
	}
	if l.Len() != 3 {
		t.Fatalf("%d entries, want 3", l.Len())
	}
	e := l.FlowSuite["conformance/flows/x/y/run"]
	if e.Milestone != "W8c" || e.Reason != "the image's Bun" || !e.Covers("warm") || !e.Covers("host") || e.Covers("fresh") {
		t.Errorf("flow_suite entry %+v", e)
	}
	if s := l.StepSuite["conformance/steps/bun/d11.yaml/d11-case"]; !s.Covers("warm") || !s.Covers("fresh") {
		t.Errorf("an entry without modes covers every mode: %+v", s)
	}
	if got, want := l.Oracle["conformance/exprs/je-1.yaml/je-1-a"].Marker(), MarkerPrefix+" W5f the wrapper's columns moved"; got != want {
		t.Errorf("marker %q, want %q", got, want)
	}

	empty, err := ParseLedger([]byte(`{"about": "nothing pending", "flow_suite": {}, "step_suite": {}, "oracle": {}}`))
	if err != nil || empty.Len() != 0 {
		t.Errorf("empty ledger: %v, %d entries", err, empty.Len())
	}
}

// ParseLedger refuses a malformed file and every malformed entry, naming
// the section and the item.
func TestParseLedgerRefuses(t *testing.T) {
	entry := func(item, body string) string {
		return `{"about": "a", "flow_suite": {"` + item + `": ` + body + `}}`
	}
	ok := `{"milestone": "W4c", "reason": "r"}`
	for _, tc := range []struct{ name, data, want string }{
		{"not JSON", `{`, "unexpected EOF"},
		{"unknown section", `{"about": "a", "go_tests": {}}`, "unknown field"},
		{"unknown entry field", entry("a/b", `{"milestone": "W4c", "reason": "r", "until": "x"}`), "unknown field"},
		{"data after the object", `{"about": "a"} {}`, "data after the ledger object"},
		{"lower-case milestone", entry("a/b", `{"milestone": "w4c", "reason": "r"}`), `milestone "w4c" is not a milestone id`},
		{"milestone without a number", entry("a/b", `{"milestone": "W", "reason": "r"}`), "is not a milestone id"},
		{"no milestone", entry("a/b", `{"reason": "r"}`), `milestone "" is not a milestone id`},
		{"no reason", entry("a/b", `{"milestone": "W4c"}`), "flow_suite a/b: no reason"},
		{"blank reason", entry("a/b", `{"milestone": "W4c", "reason": " r"}`), "not one line without surrounding space"},
		{"two-line reason", entry("a/b", `{"milestone": "W4c", "reason": "r\ns"}`), "not one line"},
		{"empty mode", entry("a/b", `{"milestone": "W4c", "reason": "r", "modes": [""]}`), "is not a mode name"},
		{"mode twice", entry("a/b", `{"milestone": "W4c", "reason": "r", "modes": ["warm", "warm"]}`), "mode warm is listed twice"},
		{"oracle entry with modes", `{"about": "a", "oracle": {"a/b": {"milestone": "W5a", "reason": "r", "modes": ["warm"]}}}`, "an oracle entry names no modes"},
		{"item without a directory", entry("run", ok), "names no directory"},
		{"absolute item", entry("/a/b", ok), "not a clean path"},
		{"item above the root", entry("../a/b", ok), "not a clean path"},
		{"unclean item", entry("a//b", ok), "not a clean path"},
		{"backslashes", entry(`a\\b`, ok), "not slash-separated"},
		{"empty item", entry("", ok), "not an item name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseLedger([]byte(tc.data))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// A missing ledger file is an empty ledger; a malformed one is an error
// that names the file.
func TestLoadLedger(t *testing.T) {
	dir := t.TempDir()
	l, err := LoadLedger(filepath.Join(dir, "missing.json"))
	if err != nil || l.Len() != 0 {
		t.Fatalf("missing file: %v, %+v", err, l)
	}
	p := filepath.Join(dir, "ledger.json")
	if err := os.WriteFile(p, []byte(`{"about": "a", "oracle": {"x": {"milestone": "W5a", "reason": "r"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLedger(p); err == nil || !strings.Contains(err.Error(), p) {
		t.Errorf("error %v, want one naming %s", err, p)
	}
}

// ParseMarker reads the milestone and the reason of a marker, and refuses
// any other form.
func TestParseMarker(t *testing.T) {
	for _, tc := range []struct{ text, milestone, reason string }{
		{MarkerPrefix + " W4c Bun 1.4.3's process.env refuses accessors", "W4c", "Bun 1.4.3's process.env refuses accessors"},
		{MarkerPrefix + " W10a r", "W10a", "r"},
		{MarkerPrefix + " X1 the bound fails under load", "X1", "the bound fails under load"},
		{MarkerPrefix + " W9 r: with a colon", "W9", "r: with a colon"},
	} {
		m, r, err := ParseMarker(tc.text)
		if err != nil || m != tc.milestone || r != tc.reason {
			t.Errorf("ParseMarker(%q) = %q, %q, %v", tc.text, m, r, err)
		}
	}
	for _, text := range []string{
		MarkerPrefix + "W4c r",
		MarkerPrefix + "  W4c r",
		MarkerPrefix + " w4c r",
		MarkerPrefix + " W4c",
		MarkerPrefix + " W4c ",
		MarkerPrefix + " W4c  r",
		MarkerPrefix + " <milestone> <reason>",
		MarkerPrefix + " W4cc r",
		"pending: W4c r",
		" " + MarkerPrefix + " W4c r",
	} {
		if m, r, err := ParseMarker(text); err == nil {
			t.Errorf("ParseMarker(%q) = %q, %q, want an error", text, m, r)
		}
	}
	e := LedgerEntry{Milestone: "W6a", Reason: "step errors gain job_id"}
	if m, r, err := ParseMarker(e.Marker()); err != nil || m != e.Milestone || r != e.Reason {
		t.Errorf("an entry's marker reads back as %q, %q, %v", m, r, err)
	}
}
