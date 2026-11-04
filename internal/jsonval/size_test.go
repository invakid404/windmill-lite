package jsonval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// sizeInputs are JSON texts that StoredBound is checked on: the valid
// inputs of the other tests, and texts that take jsonb's most bytes per
// byte of text (one-digit numbers, empty strings and containers, keys),
// long numbers, escapes, deep nesting, and the large strings of the
// job-reads-large fixture's runs.
func sizeInputs(t *testing.T) (inputs []string, named map[string]int) {
	for _, in := range postgresInputs(t) {
		if json.Valid([]byte(in)) {
			inputs = append(inputs, in)
		}
	}
	repeat := func(open, item, close string, n int) string {
		var b strings.Builder
		b.WriteString(open)
		for i := range n {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(strings.ReplaceAll(item, "%d", strconv.Itoa(i)))
		}
		b.WriteString(close)
		return b.String()
	}
	inputs = append(inputs,
		repeat("[", "1", "]", 10000),
		repeat("[", "0", "]", 3000),
		repeat("[", `""`, "]", 10000),
		repeat("[", "[]", "]", 10000),
		repeat("[", "{}", "]", 10000),
		repeat("[", "null", "]", 10000),
		repeat("{", `"k%d":1`, "}", 5000),
		repeat("{", `"%d":[]`, "}", 5000),
		repeat("[", "-1.000000000000000000000000000001e-120", "]", 2000),
		repeat("[", "123456789012345678901234567890.5", "]", 2000),
		repeat("[", `"é\n\"\\😀x"`, "]", 3000),
		strings.Repeat("[", 500)+strings.Repeat("]", 500),
		`"`+strings.Repeat("ab", 4000)+`"`,
	)
	rng := rand.New(rand.NewPCG(1, 2))
	letters := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	random := func(n int) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = letters[rng.IntN(64)]
		}
		return string(b)
	}
	named = map[string]int{}
	add := func(name, text string) {
		named[name] = len(inputs)
		inputs = append(inputs, text)
	}
	add("random 89000", `"`+random(89000)+`"`)
	add("args of random 89000", `{"x":"`+random(89000)+`"}`)
	add("random 92000", `"`+random(92000)+`"`)
	add("x 92000", `"`+strings.Repeat("x", 92000)+`"`)
	return inputs, named
}

// The walk's bound is never above the shortcut's, 12 bytes per byte of
// text and 32 more, so the shortcut stays a bound above the walk's for
// short texts; TestStoredBoundAbovePostgreSQL checks the walk itself.
func TestStoredBound(t *testing.T) {
	inputs, _ := sizeInputs(t)
	for _, in := range append(inputs, "1", "[1]", "[1,1]", `{"":1}`, `[[],[]]`, `""`, "true", "-0.5e-3") {
		if w := storedWalk([]byte(in)); w > 12*len(in)+32 {
			t.Errorf("%.40s… (%d bytes): walked %d, above the shortcut's %d", in, len(in), w, 12*len(in)+32)
		}
	}
}

// TestStoredBoundAbovePostgreSQL is the bound's check against PostgreSQL
// itself, run as TestStoredMatchesPostgreSQL is, when WL_JSONB_PSQL holds a
// psql command line: every input cast to jsonb takes at most StoredBound
// bytes, by pg_column_size of the value as computed and as a table stores
// it, after the server's compression. It also checks what the
// job-reads-large fixture's runs rest on: 89,000 random characters, and an
// object holding them, are stored below the 90,000 bytes from which
// Windmill replaces a job's result or arguments, and bounded below too; 92,000
// random characters are stored above, and 92,000 times "x" below, while
// both are bounded above, where windmill-lite cannot tell them apart.
func TestStoredBoundAbovePostgreSQL(t *testing.T) {
	command := strings.Fields(os.Getenv("WL_JSONB_PSQL"))
	if len(command) == 0 {
		t.Skip("WL_JSONB_PSQL is not set")
	}
	inputs, named := sizeInputs(t)
	var script strings.Builder
	script.WriteString("SET client_encoding = 'UTF8';\nCREATE TEMP TABLE wl_size (n int, v jsonb);\nINSERT INTO wl_size VALUES\n")
	for n, in := range inputs {
		if n > 0 {
			script.WriteString(",\n")
		}
		fmt.Fprintf(&script, "(%d, '%s'::jsonb)", n, strings.ReplaceAll(in, "'", "''"))
	}
	script.WriteString(";\nSELECT n || chr(9) || pg_column_size(v::text::jsonb) || chr(9) || pg_column_size(v) FROM wl_size ORDER BY n;\n")
	cmd := exec.Command(command[0], append(command[1:], "-X", "-q", "-A", "-t", "-v", "ON_ERROR_STOP=1")...)
	cmd.Stdin = strings.NewReader(script.String())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s: %v\n%s", strings.Join(command, " "), err, stderr.Bytes())
	}
	computed, stored := map[int]int{}, map[int]int{}
	for line := range strings.Lines(string(out)) {
		var n, c, s int
		if _, err := fmt.Sscanf(strings.TrimSpace(line), "%d\t%d\t%d", &n, &c, &s); err != nil {
			t.Fatalf("unexpected output line %q: %v", line, err)
		}
		computed[n], stored[n] = c, s
	}
	if len(computed) != len(inputs) {
		t.Fatalf("PostgreSQL answered %d of %d inputs", len(computed), len(inputs))
	}
	for n, in := range inputs {
		if b := StoredBound([]byte(in)); b < computed[n] || b < stored[n] {
			t.Errorf("%.60s… (%d bytes of text): bound %d, pg_column_size %d computed, %d stored", in, len(in), b, computed[n], stored[n])
		}
	}
	for name, n := range named {
		t.Logf("%s: bound %d, pg_column_size %d computed, %d stored", name, StoredBound([]byte(inputs[n])), computed[n], stored[n])
	}
	const from = 90000
	for _, name := range []string{"random 89000", "args of random 89000"} {
		if n := named[name]; stored[n] >= from || StoredBound([]byte(inputs[n])) >= from {
			t.Errorf("%s: stored %d, bound %d, want both below %d", name, stored[n], StoredBound([]byte(inputs[n])), from)
		}
	}
	if n := named["random 92000"]; stored[n] < from {
		t.Errorf("random 92000: stored %d, want %d or more", stored[n], from)
	}
	if n := named["x 92000"]; stored[n] >= from {
		t.Errorf("x 92000: stored %d, want below %d", stored[n], from)
	}
	for _, name := range []string{"random 92000", "x 92000"} {
		if n := named[name]; StoredBound([]byte(inputs[n])) < from {
			t.Errorf("%s: bound %d, want %d or more", name, StoredBound([]byte(inputs[n])), from)
		}
	}
}
