package jsonval

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestStoredMatchesPostgreSQL is ADR 0005's check against PostgreSQL itself.
// It casts every input of the other tests (the conformance cases, the edge
// cases and generated text) to jsonb and back, and checks that Stored
// agrees: the same text once jsonb's output spaces are removed, or a
// rejection whose DatabaseMessage is PostgreSQL's message.
//
// It needs a server, so it runs only when WL_JSONB_PSQL holds a psql command
// line (split on spaces) that reaches one. CI's jsonb job sets it to psql
// inside the oracle's pinned PostgreSQL image (docs/semantics/oracle.md).
func TestStoredMatchesPostgreSQL(t *testing.T) {
	command := strings.Fields(os.Getenv("WL_JSONB_PSQL"))
	if len(command) == 0 {
		t.Skip("WL_JSONB_PSQL is not set")
	}
	inputs := postgresInputs(t)
	cmd := exec.Command(command[0], append(command[1:], "-X", "-q", "-A", "-t", "-v", "ON_ERROR_STOP=1")...)
	cmd.Stdin = strings.NewReader(postgresScript(inputs))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s: %v\n%s", strings.Join(command, " "), err, stderr.Bytes())
	}

	rows := map[int][]string{}
	for line := range strings.Lines(string(out)) {
		f := strings.Split(strings.TrimSuffix(line, "\n"), "\t")
		switch f[0] {
		case "V":
			t.Logf("server: %s", f[1])
		case "R":
			n, err := strconv.Atoi(f[1])
			if err != nil || n < 0 || n >= len(inputs) || rows[n] != nil {
				t.Fatalf("unexpected row %q", line)
			}
			rows[n] = f[2:]
		default:
			t.Fatalf("unexpected output line %q", line)
		}
	}
	if len(rows) != len(inputs) {
		t.Fatalf("PostgreSQL answered %d of %d inputs", len(rows), len(inputs))
	}

	outcomes := map[string]int{}
	failures := 0
	for n, in := range inputs {
		row := rows[n]
		got, err := FromProducer([]byte(in)).Stored()
		var problem string
		switch {
		case row[0] == "ok":
			outcomes["stored"]++
			text, herr := hex.DecodeString(row[1])
			var want bytes.Buffer
			if herr == nil {
				herr = json.Compact(&want, text)
			}
			if herr != nil {
				t.Fatalf("PostgreSQL's text %q: %v", row[1], herr)
			}
			if err != nil || !bytes.Equal(got, want.Bytes()) {
				problem = fmt.Sprintf("PostgreSQL stores %.120q; Stored() = %.120q, %v", want.Bytes(), got, err)
			}
		case row[0] == "err" && len(row) == 3:
			outcomes[row[1]+" "+row[2]]++
			var se *StoreError
			if !errors.As(err, &se) {
				problem = fmt.Sprintf("PostgreSQL rejects it (%s %s); Stored() = %.120q, %v", row[1], row[2], got, err)
			} else if se.DatabaseMessage() != row[2] {
				problem = fmt.Sprintf("PostgreSQL rejects it with %s %q; Stored() rejects it with %q (%v)", row[1], row[2], se.DatabaseMessage(), err)
			}
		default:
			t.Fatalf("unexpected row %q", row)
		}
		if problem != "" {
			t.Errorf("input %d %.120q: %s", n, in, problem)
			if failures++; failures == 20 {
				t.Fatal("too many differences")
			}
		}
	}
	t.Logf("inputs %d; outcomes %v", len(inputs), outcomes)
}

// postgresInputs returns the distinct inputs of the other tests that can be
// sent as SQL text: valid UTF-8 without a NUL byte.
func postgresInputs(t *testing.T) []string {
	var all []string
	for _, f := range loadCaseFiles(t) {
		for _, c := range f.Cases {
			all = append(all, c.Given)
		}
	}
	for _, c := range edgeCases() {
		all = append(all, c.given)
	}
	for _, text := range generatedTexts(4000, 3) {
		all = append(all, string(text))
	}
	seen := map[string]bool{}
	var inputs []string
	for _, in := range all {
		if !seen[in] && utf8.ValidString(in) && !strings.Contains(in, "\x00") {
			seen[in] = true
			inputs = append(inputs, in)
		}
	}
	return inputs
}

// postgresScript returns a psql script that prints the server version, then
// one row per input: R, its index, and either "ok" and jsonb's text in
// hexadecimal or "err", the SQLSTATE and PostgreSQL's message. The text is
// hex-encoded because psql drops characters it cannot display, such as
// U+10FFFF, from what it prints. Inputs are standard string literals, in
// which only a quote needs escaping.
func postgresScript(inputs []string) string {
	var b strings.Builder
	b.WriteString(`SET client_encoding = 'UTF8';
SELECT 'V' || chr(9) || version();
CREATE FUNCTION pg_temp.wl_stored(t text) RETURNS text LANGUAGE plpgsql AS $wl$
BEGIN
  RETURN 'ok' || chr(9) || encode(convert_to((t::jsonb)::text, 'UTF8'), 'hex');
EXCEPTION WHEN OTHERS THEN
  RETURN 'err' || chr(9) || SQLSTATE || chr(9) || SQLERRM;
END
$wl$;
SELECT 'R' || chr(9) || n || chr(9) || pg_temp.wl_stored(t) FROM (VALUES
`)
	for n, in := range inputs {
		if n > 0 {
			b.WriteString(",\n")
		}
		fmt.Fprintf(&b, "(%d, '%s')", n, strings.ReplaceAll(in, "'", "''"))
	}
	b.WriteString("\n) AS v(n, t) ORDER BY n;\n")
	return b.String()
}
