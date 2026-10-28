package oracle

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestStubSourceCases scans the sources of testdata/stub/sources.json: each
// keeps main's parameter list and the type declarations verbatim (WR-6,
// WR-7, WR-8, WR-V3), or is refused with the reason.
func TestStubSourceCases(t *testing.T) {
	data, err := os.ReadFile("testdata/stub/sources.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name   string   `json:"name"`
		Source string   `json:"source"`
		Params string   `json:"params"`
		Keep   []string `json:"keep"`
		Refuse string   `json:"refuse"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 25 {
		t.Fatalf("only %d cases", len(cases))
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			stub, err := StubSource(c.Source)
			if c.Refuse != "" {
				if err == nil || !strings.Contains(err.Error(), c.Refuse) {
					t.Fatalf("got %v, want a refusal containing %q", err, c.Refuse)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			m, err := scanTS(c.Source)
			if err != nil {
				t.Fatal(err)
			}
			if m.params != c.Params || strings.Join(m.keep, "\x00") != strings.Join(c.Keep, "\x00") {
				t.Fatalf("params %q keep %q, want %q %q", m.params, m.keep, c.Params, c.Keep)
			}
			// The stub is itself a script the scanner reads the same way,
			// so stubbing is idempotent: what Windmill's parser reads is
			// kept through any number of rewrites.
			again, err := scanTS(stub)
			if err != nil {
				t.Fatalf("the stub does not scan: %v\n%s", err, stub)
			}
			if again.params != c.Params || strings.Join(again.keep, "\x00") != strings.Join(c.Keep, "\x00") {
				t.Fatalf("the stub keeps params %q keep %q", again.params, again.keep)
			}
			if again2, _ := StubSource(stub); again2 != stub {
				t.Fatal("stubbing a stub changed it")
			}
			for _, gone := range []string{"@private/db", "conn.get", "return x * 2", "return at"} {
				if strings.Contains(c.Source, gone) && strings.Contains(stub, gone) {
					t.Fatalf("the stub kept %q", gone)
				}
			}
		})
	}
}

// TestStubSourceGolden pins the whole stub of one script.
func TestStubSourceGolden(t *testing.T) {
	src, err := os.ReadFile("../../conformance/flows/stub/collapsed/windmill/f/wl_stub/collapsed.flow/book.ts")
	if err != nil {
		t.Fatal(err)
	}
	got, err := StubSource(string(src))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/stub/book.stub.ts")
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("stub of book.ts:\n%s\nwant testdata/stub/book.stub.ts:\n%s", got, want)
	}
}
