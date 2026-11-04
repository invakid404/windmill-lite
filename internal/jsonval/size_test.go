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

// sizeInputs are JSON texts that StoredBound is checked on: the inputs of
// the other tests that jsonb stores (Stored accepts them; a value it
// refuses has no stored size), and texts that take jsonb's most bytes per
// byte of text (one-digit numbers, empty strings and containers, keys),
// long numbers, escapes, deep nesting, and the large strings of the
// job-reads-large fixture's runs.
func sizeInputs(t *testing.T) (inputs []string, named map[string]int) {
	for _, in := range postgresInputs(t) {
		if _, err := FromProducer([]byte(in)).Stored(); json.Valid([]byte(in)) && err == nil {
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
		// Every input is one that jsonb stores, as the PostgreSQL check
		// needs.
		if _, err := FromProducer([]byte(in)).Stored(); err != nil {
			t.Errorf("%.40s…: jsonb does not store it: %v", in, err)
		}
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
	command := psqlCommand(t)
	inputs, named := sizeInputs(t)
	computed, stored := postgresSizes(t, command, inputs)
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

func psqlCommand(t *testing.T) []string {
	t.Helper()
	command := strings.Fields(os.Getenv("WL_JSONB_PSQL"))
	if len(command) == 0 {
		t.Skip("WL_JSONB_PSQL is not set")
	}
	return command
}

// postgresSizes stores each input as jsonb in a table of PostgreSQL and
// returns its pg_column_size as computed (the datum, uncompressed) and as
// the table stores it, after TOAST.
func postgresSizes(t *testing.T, command, inputs []string) (computed, stored map[int]int) {
	t.Helper()
	var script strings.Builder
	script.WriteString("SET client_encoding = 'UTF8';\nCREATE TEMP TABLE wl_size (n int, v jsonb);\n")
	for n, in := range inputs {
		fmt.Fprintf(&script, "INSERT INTO wl_size VALUES (%d, '%s'::jsonb);\n", n, strings.ReplaceAll(in, "'", "''"))
	}
	script.WriteString("SELECT n || chr(9) || pg_column_size(v::text::jsonb) || chr(9) || pg_column_size(v) FROM wl_size ORDER BY n;\n")
	cmd := exec.Command(command[0], append(command[1:], "-X", "-q", "-A", "-t", "-v", "ON_ERROR_STOP=1")...)
	cmd.Stdin = strings.NewReader(script.String())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s: %v\n%s", strings.Join(command, " "), err, stderr.Bytes())
	}
	computed, stored = map[int]int{}, map[int]int{}
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
	return computed, stored
}

// compressionInputs are JSON texts that exercise pglz: arrays of objects
// with repeated keys, sentences over a small vocabulary (ASCII and
// multi-byte, whose bytes above 0x7f the hash reads as signed), numbers, a
// random block repeated at distances inside and outside pglz's 4 KiB
// window, and random text with repeats mixed in near the 25 % pglz
// requires, at sizes from 3 KB to 300 KB.
func compressionInputs() []string {
	rng := rand.New(rand.NewPCG(3, 4))
	letters := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	random := func(n int) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = letters[rng.IntN(64)]
		}
		return string(b)
	}
	quote := func(s string) string {
		b, _ := json.Marshal(s)
		return string(b)
	}
	var out []string
	for _, n := range []int{50, 300, 1000, 3000} {
		var b strings.Builder
		b.WriteByte('[')
		for i := range n {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `{"id":%d,"name":"row-%d","tags":["a","%c"],"score":%d.%02d,"ok":%t,"nil":null}`, i, (i*7919)%100003, 'b'+byte(i%3), i%97, i%100, i%2 == 0)
		}
		b.WriteByte(']')
		out = append(out, b.String())
	}
	for _, words := range [][]string{
		{"alpha", "beta", "gamma", "delta", "flow", "step", "job", "result", "windmill", "lite"},
		{"Привет", "мир", "задача", "поток", "шаг", "результат", "día", "naïve", "日本語", "テスト", "😀", "ünïcødé"},
	} {
		for _, n := range []int{500, 5000, 30000} {
			ws := make([]string, n)
			for i := range ws {
				ws[i] = words[rng.IntN(len(words))]
			}
			out = append(out, quote(strings.Join(ws, " ")))
		}
	}
	nums := make([]string, 20000)
	for i := range nums {
		nums[i] = strconv.FormatFloat(rng.Float64()*1e6-5e5, 'f', rng.IntN(6), 64)
	}
	out = append(out, "["+strings.Join(nums, ",")+"]")
	for _, block := range []int{1000, 2000, 4000, 4094, 4095, 4096, 4200, 8192} {
		b := random(block)
		out = append(out, quote(strings.Repeat(b, 120000/block+1)[:120000]))
	}
	for _, p := range []float64{0.15, 0.25, 0.3, 0.35, 0.5} {
		for _, n := range []int{3000, 95000, 300000} {
			var b strings.Builder
			for b.Len() < n {
				if s := b.String(); len(s) > 64 && rng.Float64() < p {
					at := max(0, len(s)-4000) + rng.IntN(min(len(s), 4000)-32)
					b.WriteString(s[at : at+3+rng.IntN(29)])
				} else {
					b.WriteString(random(16))
				}
			}
			out = append(out, quote(b.String()[:n]))
		}
	}
	return out
}

// ToastedSize equals what PostgreSQL stores, checked as
// TestStoredMatchesPostgreSQL is, when WL_JSONB_PSQL holds a psql command
// line: for every input of the other tests that jsonb stores and every
// compression input, the datum's length is pg_column_size of the value as
// computed, and for a value large enough to be toasted, pg_column_size as
// the table stores it is ToastedSize when TOAST moves it out of line, or
// the compressed length and its 8-byte header when it stays inline.
func TestToastedSizeMatchesPostgreSQL(t *testing.T) {
	command := psqlCommand(t)
	inputs, _ := sizeInputs(t)
	inputs = append(inputs, compressionInputs()...)
	computed, stored := postgresSizes(t, command, inputs)
	toasted, compressed := 0, 0
	for n, in := range inputs {
		d, err := jsonbDatum([]byte(in))
		if err != nil {
			t.Errorf("%.60s…: %v", in, err)
			continue
		}
		if len(d) != computed[n] {
			t.Errorf("%.60s… (%d bytes of text): datum %d bytes, pg_column_size %d computed", in, len(in), len(d), computed[n])
			continue
		}
		if len(d) < 2500 {
			continue
		}
		toasted++
		size, _ := ToastedSize([]byte(in))
		c := pglzCompressedLen(d[4:])
		if c >= 0 && c+8 < len(d)-4-2 {
			compressed++
		}
		if stored[n] != size && !(size == c+4 && stored[n] == c+8) {
			t.Errorf("%.60s… (%d bytes of text): ToastedSize %d (pglz %d of %d), pg_column_size %d stored", in, len(in), size, c, len(d)-4, stored[n])
		}
	}
	t.Logf("%d inputs, %d toasted, %d of them compressed", len(inputs), toasted, compressed)
}
