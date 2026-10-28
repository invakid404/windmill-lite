package jsonval

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

// An edgeCase is a canonicaliser input the conformance cases cannot hold
// conveniently (long numbers, raw control and separator characters,
// invalid text), with its expected stored text or rejection. The PostgreSQL
// test runs every one of them through jsonb too.
type edgeCase struct {
	name   string
	given  string
	stored string
	kind   error
}

func edgeCases() []edgeCase {
	zeros := func(n int) string { return strings.Repeat("0", n) }
	return []edgeCase{
		// Whitespace, including line breaks, between every token.
		{name: "space", given: " \t\r\n{ \"a\" :\n[ 1 ,\t2 ] ,\r\n\"b\" : { } }\r\n", stored: `{"a":[1,2],"b":{}}`},
		{name: "space-scalar", given: "\n\"x\"\n", stored: `"x"`},

		// Characters jsonb writes raw: separators, DEL, noncharacters.
		{name: "separators-escaped", given: `"\u2028\u2029\u0085"`, stored: "\"\u2028\u2029\u0085\""},
		{name: "separators-raw", given: "\"\u2028\u2029\u0085\"", stored: "\"\u2028\u2029\u0085\""},
		{name: "del-escaped", given: `"\u007f"`, stored: "\"\x7f\""},
		{name: "del-raw", given: "\"\x7f\"", stored: "\"\x7f\""},
		{name: "noncharacters", given: `"\uffff\ufffe\ufdd0"`, stored: "\"\uffff\ufffe\ufdd0\""},
		{name: "latin1-range", given: `"\u0080\u00a0\u00ff"`, stored: "\"\u0080\u00a0\u00ff\""},
		{name: "astral-pairs", given: `"\ud800\udc00\uDBFF\uDFFF"`, stored: "\"\U00010000\U0010FFFF\""},
		{name: "astral-noncharacters", given: `"\ud83f\udffe\udbff\udfff"`, stored: "\"\U0001FFFE\U0010FFFF\""},
		{name: "astral-noncharacters-raw", given: "\"\U0001FFFE\U0010FFFF\"", stored: "\"\U0001FFFE\U0010FFFF\""},
		{name: "control-in-key", given: `{"\t":2,"\u0001":1}`, stored: `{"\u0001":1,"\t":2}`},

		// Numbers at the edges of numeric's range and of a double's.
		{name: "max-integer-digits", given: "1e131071", stored: "1" + zeros(131071)},
		{name: "max-integer-digits-plain", given: "1" + zeros(131071), stored: "1" + zeros(131071)},
		{name: "max-integer-digits-negative", given: "-9.99e131071", stored: "-999" + zeros(131069)},
		{name: "max-integer-digits-from-fraction", given: "0.1e131072", stored: "1" + zeros(131071)},
		{name: "too-many-integer-digits", given: "1e131072", kind: ErrNumericOverflow},
		{name: "too-many-integer-digits-mantissa", given: "10e131071", kind: ErrNumericOverflow},
		{name: "too-many-integer-digits-plain", given: "1" + zeros(131072), kind: ErrNumericOverflow},
		{name: "max-scale", given: "1e-16383", stored: "0." + zeros(16382) + "1"},
		{name: "max-scale-plain", given: "0." + zeros(16383), stored: "0." + zeros(16383)},
		{name: "too-large-scale", given: "1e-16384", kind: ErrNumericOverflow},
		{name: "too-large-scale-zero", given: "0e-16384", kind: ErrNumericOverflow},
		{name: "too-large-scale-plain", given: "0." + zeros(16384), kind: ErrNumericOverflow},
		{name: "zero-large-exponent", given: "0e131073", stored: "0"},
		{name: "zero-max-exponent", given: "0e1073741823", stored: "0"},
		{name: "exponent-out-of-range", given: "0e1073741824", kind: ErrNumericOverflow},
		{name: "exponent-max-nonzero", given: "1e1073741823", kind: ErrNumericOverflow},
		{name: "exponent-min-nonzero", given: "1e-1073741823", kind: ErrNumericOverflow},
		{name: "smallest-double", given: "5e-324", stored: "0." + zeros(323) + "5"},
		{name: "largest-double", given: "1.7976931348623157e+308", stored: "17976931348623157" + zeros(292)},
		{name: "largest-double-negative", given: "-1.7976931348623157e308", stored: "-17976931348623157" + zeros(292)},
		{name: "long-mantissa", given: "123456789012345678901234567890e-30", stored: "0.123456789012345678901234567890"},

		// Rejections are reported for the first rejected token.
		{name: "nul-after-surrogate", given: `["\ud800","\u0000"]`, kind: ErrUnpairedSurrogate},
		{name: "surrogate-after-nul", given: `["\u0000","\ud800"]`, kind: ErrNUL},
		{name: "overflow-before-nul", given: `[1e-99999,"\u0000"]`, kind: ErrNumericOverflow},
		{name: "nul-before-overflow", given: `["\u0000",1e-99999]`, kind: ErrNUL},
		{name: "high-surrogate-then-nul", given: `"\ud800\u0000"`, kind: ErrUnpairedSurrogate},

		// In text that is not one JSON value, PostgreSQL reads one token ahead
		// and can report a different kind; these pin jsonval's, and the
		// PostgreSQL test checks only that both reject (see canonicalise).
		{name: "lookahead-nul-after-number", given: `[1 "\u0000"]`, kind: ErrSyntax},
		{name: "lookahead-nul-after-key", given: `{"a" "\u0000"}`, kind: ErrSyntax},
		{name: "lookahead-nul-after-string", given: `"a" "\u0000"`, kind: ErrSyntax},
		{name: "lookahead-nul-after-literal", given: `[true "\u0000"]`, kind: ErrSyntax},
		{name: "lookahead-overflow-then-bad-token", given: `[1e99999999999 x]`, kind: ErrNumericOverflow},
		{name: "lookahead-overflow-then-surrogate", given: `[1e-99999 "\ud800"]`, kind: ErrNumericOverflow},
		{name: "lookahead-overflow-then-nul", given: `{"a":1e-99999 "\u0000"}`, kind: ErrNumericOverflow},

		// Text that is not one JSON value.
		{name: "empty", given: ``, kind: ErrSyntax},
		{name: "only-space", given: " \n", kind: ErrSyntax},
		{name: "unclosed-object", given: `{`, kind: ErrSyntax},
		{name: "unclosed-array", given: `[1,2`, kind: ErrSyntax},
		{name: "unclosed-member", given: `{"a":1`, kind: ErrSyntax},
		{name: "stray-close", given: `}`, kind: ErrSyntax},
		{name: "mismatched-close", given: `{"a":[}`, kind: ErrSyntax},
		{name: "trailing-comma-array", given: `[1,]`, kind: ErrSyntax},
		{name: "trailing-comma-object", given: `{"a":1,}`, kind: ErrSyntax},
		{name: "key-without-value", given: `{"a"}`, kind: ErrSyntax},
		{name: "missing-colon", given: `{"a" 1}`, kind: ErrSyntax},
		{name: "number-key", given: `{1:2}`, kind: ErrSyntax},
		{name: "leading-zero", given: `01`, kind: ErrSyntax},
		{name: "negative-leading-zero", given: `-01`, kind: ErrSyntax},
		{name: "bare-point", given: `1.`, kind: ErrSyntax},
		{name: "leading-point", given: `.5`, kind: ErrSyntax},
		{name: "plus-sign", given: `+1`, kind: ErrSyntax},
		{name: "bare-minus", given: `-`, kind: ErrSyntax},
		{name: "bare-exponent", given: `1e`, kind: ErrSyntax},
		{name: "bare-exponent-sign", given: `1e+`, kind: ErrSyntax},
		{name: "two-points", given: `1.5.3`, kind: ErrSyntax},
		{name: "number-then-letter", given: `1x`, kind: ErrSyntax},
		{name: "number-then-underscore", given: `[1_]`, kind: ErrSyntax},
		{name: "literal-prefix", given: `tru`, kind: ErrSyntax},
		{name: "literal-suffix", given: `[nulls]`, kind: ErrSyntax},
		{name: "literal-case", given: `True`, kind: ErrSyntax},
		{name: "nan", given: `NaN`, kind: ErrSyntax},
		{name: "infinity", given: `-Infinity`, kind: ErrSyntax},
		{name: "unterminated-string", given: `"abc`, kind: ErrSyntax},
		{name: "unterminated-escape", given: `"\`, kind: ErrSyntax},
		{name: "invalid-escape", given: `"\x41"`, kind: ErrSyntax},
		{name: "upper-u-escape", given: `"\U0041"`, kind: ErrSyntax},
		{name: "short-u-escape", given: `"\u12"`, kind: ErrSyntax},
		{name: "non-hex-u-escape", given: `"\u12G4"`, kind: ErrSyntax},
		{name: "raw-tab", given: "\"a\tb\"", kind: ErrSyntax},
		{name: "raw-newline", given: "\"a\nb\"", kind: ErrSyntax},
		{name: "two-values", given: `1 2`, kind: ErrSyntax},
		{name: "two-values-array", given: `[1 2]`, kind: ErrSyntax},
		{name: "two-objects", given: `{} {}`, kind: ErrSyntax},
		{name: "byte-order-mark", given: "\ufeff{}", kind: ErrSyntax},
		{name: "single-quotes", given: `'a'`, kind: ErrSyntax},
	}
}

// invalidUTF8 are inputs PostgreSQL never sees as jsonb input (text with
// invalid UTF-8 fails before the cast), so only the Go tests run them.
var invalidUTF8 = []string{"\"\xff\"", "\"\xc3\"", "\"\xed\xa0\x80\"", "{\"\xc0\xaf\":1}", "[\"a\",\"\x80\"]"}

func TestStoredEdgeCases(t *testing.T) {
	for _, c := range edgeCases() {
		t.Run(c.name, func(t *testing.T) {
			checkStored(t, c.given, c.stored, c.kind)
		})
	}
	for _, text := range invalidUTF8 {
		checkStored(t, text, "", ErrSyntax)
	}
}

// checkStored checks Stored against an expected text or rejection, and the
// test implementation against Stored.
func checkStored(t *testing.T, given, want string, kind error) {
	t.Helper()
	got, err := FromProducer([]byte(given)).Stored()
	if kind != nil {
		var se *StoreError
		if !errors.As(err, &se) || !errors.Is(err, kind) {
			t.Fatalf("Stored(%.60q) = %.60q, %v; want %v", given, got, err, kind)
		}
	} else if err != nil || string(got) != want {
		t.Fatalf("Stored(%.60q) = %.60q, %v; want %.60q", given, got, err, want)
	}
	checkAgainstReference(t, []byte(given))
}

// checkAgainstReference checks that Stored and the test implementation
// agree on text, that the stored text is a fixed point, and that HasNUL
// agrees with a NUL rejection.
func checkAgainstReference(t *testing.T, text []byte) {
	t.Helper()
	v := FromProducer(text)
	got, err := v.Stored()
	want, wantErr := referenceStored(text)
	switch {
	case errors.Is(wantErr, ErrSyntax):
		// The test implementation does not order a syntax error against
		// the other rejections, so any rejection agrees with it.
		if err == nil {
			t.Fatalf("Stored(%.80q) = %.80q; the test implementation rejects it as %v", text, got, wantErr)
		}
		return
	case wantErr != nil:
		if !errors.Is(err, wantErr) {
			t.Fatalf("Stored(%.80q) = %.80q, %v; the test implementation rejects it as %v", text, got, err, wantErr)
		}
		if errors.Is(wantErr, ErrNUL) && !v.HasNUL() {
			t.Fatalf("Stored(%.80q) rejects a NUL that HasNUL does not see", text)
		}
		return
	case err != nil:
		t.Fatalf("Stored(%.80q) = %v; the test implementation gives %.80q", text, err, want)
	case !bytes.Equal(got, want):
		t.Fatalf("Stored(%.80q)\n = %.200q\nthe test implementation gives\n   %.200q", text, got, want)
	case v.HasNUL():
		t.Fatalf("HasNUL(%.80q) is true for a value with a stored form", text)
	}
	again, err := FromProducer(bytes.Clone(got)).Stored()
	if err != nil || !bytes.Equal(again, got) {
		t.Fatalf("stored text %.80q is not a fixed point: %.80q, %v", got, again, err)
	}
}

func TestStoredAgreesWithReferenceOnGeneratedText(t *testing.T) {
	n := 5000
	if testing.Short() {
		n = 500
	}
	for i, text := range generatedTexts(n, 1) {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			checkAgainstReference(t, text)
		})
	}
	// Mutations are mostly not JSON, which must be rejected; those that
	// still are must agree like any other text.
	for i, text := range brokenTexts(n, 4) {
		t.Run(fmt.Sprint("broken-", i), func(t *testing.T) {
			checkAgainstReference(t, text)
		})
	}
}

func FuzzStored(f *testing.F) {
	for _, c := range edgeCases() {
		if len(c.given) < 1000 {
			f.Add([]byte(c.given))
		}
	}
	for _, text := range generatedTexts(64, 2) {
		f.Add(text)
	}
	f.Fuzz(func(t *testing.T, text []byte) {
		// encoding/json refuses nesting deeper than 10000, which only a
		// longer text can reach.
		if len(text) > 10000 {
			t.Skip()
		}
		checkAgainstReference(t, text)
	})
}

func TestStoredErrorOffsets(t *testing.T) {
	tests := []struct {
		given  string
		kind   error
		offset int
	}{
		{`["a","\u0000"]`, ErrNUL, 6},
		{`{"k":1e99999999999}`, ErrNumericOverflow, 5},
		{`"x\ud800"`, ErrUnpairedSurrogate, 2},
		{`"x\ude00"`, ErrUnpairedSurrogate, 2},
		{`[1,]`, ErrSyntax, 3},
		{`"abc`, ErrSyntax, 0},
	}
	for _, tt := range tests {
		_, err := FromProducer([]byte(tt.given)).Stored()
		var se *StoreError
		if !errors.As(err, &se) || se.Kind != tt.kind || se.Offset != tt.offset {
			t.Errorf("Stored(%s) = %v; want %v at byte %d", tt.given, err, tt.kind, tt.offset)
		}
	}
}

func TestStoreErrorDatabaseMessage(t *testing.T) {
	// spec: OR-27/a — the message recorded on the oracle for NUL. The
	// others are PostgreSQL's, checked by TestStoredMatchesPostgreSQL.
	for kind, want := range map[error]string{
		ErrNUL:               "unsupported Unicode escape sequence",
		ErrUnpairedSurrogate: "invalid input syntax for type json",
		ErrNumericOverflow:   "value overflows numeric format",
		ErrSyntax:            "invalid input syntax for type json",
	} {
		if got := (&StoreError{Kind: kind}).DatabaseMessage(); got != want {
			t.Errorf("DatabaseMessage(%v) = %q, want %q", kind, got, want)
		}
	}
}

func TestStoredDeepNesting(t *testing.T) {
	// The canonicaliser keeps its own stack, so nesting far deeper than a
	// recursive parser could follow is canonicalised, not a crash.
	const depth = 1_000_000
	text := strings.Repeat(`[{"k":`, depth) + `{"b":1,"a":2}` + strings.Repeat("}]", depth)
	got, err := FromProducer([]byte(text)).Stored()
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Repeat(`[{"k":`, depth) + `{"a":2,"b":1}` + strings.Repeat("}]", depth)
	if string(got) != want {
		t.Fatalf("deeply nested text: got %d bytes, want %d", len(got), len(want))
	}
	// Objects reordered at every level, which the reorders copy once per
	// level (see closeObject).
	checkAgainstReference(t, []byte(strings.Repeat(`{"b":0,"a":`, 1000)+"1"+strings.Repeat("}", 1000)))
	checkAgainstReference(t, []byte(strings.Repeat("[", 1000)+strings.Repeat("]", 1000)))
}

// generatedTexts returns n JSON texts from a seeded generator, so a failure
// names a reproducible input. One text in eight may hold an escape or a
// number that jsonb rejects.
func generatedTexts(n int, seed uint64) [][]byte {
	g := textGen{r: rand.New(rand.NewPCG(seed, 0x6a736f6e62))}
	texts := make([][]byte, n)
	for i := range texts {
		g.bad = i%8 == 7
		var b bytes.Buffer
		g.space(&b)
		g.value(&b, 0)
		g.space(&b)
		texts[i] = b.Bytes()
	}
	return texts
}

// brokenTexts returns n generated texts with one random edit each: a byte
// deleted, a byte inserted, or a token spliced in, among them tokens jsonb
// rejects, so rejections meet syntax errors in every order.
func brokenTexts(n int, seed uint64) [][]byte {
	r := rand.New(rand.NewPCG(seed, 0x62726f6b656e))
	splices := []string{`"\u0000"`, `"\ud800"`, `1e-99999`, `1e99999999999`, `x`, `,`, `:`, `{`, `]`, `"`, `\`, ` 1`, `""`, `0`}
	texts := generatedTexts(n, seed)
	for i, text := range texts {
		at := r.IntN(len(text) + 1)
		var edited []byte
		switch k := r.IntN(3); {
		case k == 0 && at < len(text):
			edited = append(append(edited, text[:at]...), text[at+1:]...)
		case k == 1:
			const inserts = `{}[],:"0e-\ x`
			edited = append(append(append(edited, text[:at]...), inserts[r.IntN(len(inserts))]), text[at:]...)
		default:
			edited = append(append(append(edited, text[:at]...), splices[r.IntN(len(splices))]...), text[at:]...)
		}
		texts[i] = edited
	}
	return texts
}

// textGen writes random JSON text aimed at the canonicaliser's corners:
// keys of mixed byte lengths and scripts that often repeat, every way of
// writing a character, exponent numbers, and whitespace between tokens.
type textGen struct {
	r   *rand.Rand
	bad bool
}

var (
	genKeys  = []string{"", "a", "b", "B", "_", "1", "10", "aa", "ab", "zz", "ccc", "é", "€", "ｚ", "😀", "a\"", "k\n", "\\", "/"}
	genChars = []rune{'a', 'Z', '0', ' ', '"', '\\', '/', '\b', '\f', '\n', '\r', '\t', 0x01, 0x1f, 0x7f, 'é', '€', 'ｚ', '😀', 0x2028, 0xffff}
)

func (g *textGen) value(b *bytes.Buffer, depth int) {
	switch k := g.r.IntN(10); {
	case k < 2 && depth < 4:
		b.WriteByte('{')
		for i := range g.r.IntN(6) {
			if i > 0 {
				b.WriteByte(',')
			}
			g.space(b)
			g.str(b, genKeys[g.r.IntN(len(genKeys))])
			g.space(b)
			b.WriteByte(':')
			g.space(b)
			g.value(b, depth+1)
			g.space(b)
		}
		b.WriteByte('}')
	case k < 4 && depth < 4:
		b.WriteByte('[')
		for i := range g.r.IntN(5) {
			if i > 0 {
				b.WriteByte(',')
			}
			g.space(b)
			g.value(b, depth+1)
			g.space(b)
		}
		b.WriteByte(']')
	case k < 6:
		var s strings.Builder
		for range g.r.IntN(8) {
			s.WriteRune(genChars[g.r.IntN(len(genChars))])
		}
		g.str(b, s.String())
	case k < 9:
		g.number(b)
	default:
		b.WriteString([]string{"true", "false", "null"}[g.r.IntN(3)])
	}
}

// str writes s as a JSON string, choosing for each character between its
// raw form and the escapes that mean it.
func (g *textGen) str(b *bytes.Buffer, s string) {
	b.WriteByte('"')
	for _, r := range s {
		if g.bad && g.r.IntN(12) == 0 {
			b.WriteString([]string{`\u0000`, `\ud83d`, `\ude00`, `\ud83d\u0041`}[g.r.IntN(4)])
		}
		short := map[rune]string{'"': `\"`, '\\': `\\`, '/': `\/`, '\b': `\b`, '\f': `\f`, '\n': `\n`, '\r': `\r`, '\t': `\t`}[r]
		mustEscape := r == '"' || r == '\\' || r < 0x20
		switch k := g.r.IntN(4); {
		case short != "" && (k == 0 || mustEscape && k < 3):
			b.WriteString(short)
		case k == 1 || mustEscape:
			g.uEscape(b, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
}

func (g *textGen) uEscape(b *bytes.Buffer, r rune) {
	format := `\u%04x`
	if g.r.IntN(2) == 0 {
		format = `\u%04X`
	}
	if r > 0xffff {
		r -= 0x10000
		fmt.Fprintf(b, format, 0xd800+(r>>10))
		fmt.Fprintf(b, format, 0xdc00+(r&0x3ff))
		return
	}
	fmt.Fprintf(b, format, r)
}

func (g *textGen) number(b *bytes.Buffer) {
	if g.r.IntN(6) == 0 {
		b.WriteByte('-')
	}
	if g.r.IntN(4) == 0 {
		b.WriteByte('0')
	} else {
		b.WriteByte(byte('1' + g.r.IntN(9)))
		g.digits(b, g.r.IntN(20))
	}
	if g.r.IntN(2) == 0 {
		b.WriteByte('.')
		g.digits(b, 1+g.r.IntN(8))
		if g.r.IntN(3) == 0 {
			b.WriteString("00")
		}
	}
	if g.r.IntN(2) == 0 {
		b.WriteByte("eE"[g.r.IntN(2)])
		b.WriteString([]string{"", "+", "-"}[g.r.IntN(3)])
		if g.r.IntN(5) == 0 {
			b.WriteByte('0')
		}
		exp := g.r.IntN(40)
		switch {
		case g.bad && g.r.IntN(4) == 0:
			exp = 200000
		case g.r.IntN(10) == 0:
			exp = g.r.IntN(400)
		}
		fmt.Fprint(b, exp)
	}
}

func (g *textGen) digits(b *bytes.Buffer, n int) {
	for range n {
		b.WriteByte(byte('0' + g.r.IntN(10)))
	}
}

func (g *textGen) space(b *bytes.Buffer) {
	if g.r.IntN(3) == 0 {
		for range 1 + g.r.IntN(2) {
			b.WriteByte(" \t\n\r"[g.r.IntN(4)])
		}
	}
}

// The benchmarks compare the canonicaliser with the test implementation on
// a value shaped like a step result. Milestone G1 records the baseline.
func benchmarkText(n int) []byte {
	var b strings.Builder
	b.WriteByte('[')
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"id":%d,"name":"item %d","tags":["x","y\n"],"price":%d.50,"meta":{"zz":true,"a":null,"created":"2024-01-01T00:00:00Z"}}`, i, i, i)
	}
	b.WriteByte(']')
	return []byte(b.String())
}

func BenchmarkStored(b *testing.B) {
	for _, n := range []int{1, 32, 1024} {
		text := benchmarkText(n)
		b.Run(fmt.Sprintf("canonicaliser/%dB", len(text)), func(b *testing.B) {
			b.SetBytes(int64(len(text)))
			for b.Loop() {
				if _, err := canonicalise(text); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("decode-sort-encode/%dB", len(text)), func(b *testing.B) {
			b.SetBytes(int64(len(text)))
			for b.Loop() {
				if _, err := referenceStored(text); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
