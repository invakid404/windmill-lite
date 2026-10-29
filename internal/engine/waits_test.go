package engine

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/ir"
)

// Milestone N2's arithmetic and texts: the values of sleep (FX-23, JE-28)
// and timeout (JE-29), and the retry schedule (FX-26), as pure functions.
// The engine-level tests are in retry_test.go; the flow fixtures
// conformance/flows/execution/fx-23 to fx-29 run them on the real Bun
// harness and on the oracle.

// FX-23, JE-28: a non-negative integer is seconds, any other number value
// × 1000 milliseconds truncated, a negative one 0; a negative integer and
// any other JSON value fail with Windmill's texts.
func TestSleepDelay(t *testing.T) {
	for _, c := range []struct {
		text string
		want time.Duration
		err  string
	}{
		{"2", 2 * time.Second, ""},
		{"0", 0, ""},
		{"0.25", 250 * time.Millisecond, ""},
		{"1.5", 1500 * time.Millisecond, ""},
		// A static 2.0 is a float: 2000 ms, the same delay (JE-28).
		{"2.0", 2 * time.Second, ""},
		// An exponent makes a float too: 1e3 × 1000 ms.
		{"1e3", 1000 * time.Second, ""},
		{"0.0001", 0, ""},
		{"0.1", 100 * time.Millisecond, ""},
		{"-0.5", 0, ""},
		// Negative zero is a float in serde_json, so 0, not an error.
		{"-0", 0, ""},
		// Beyond 64 bits an integer is a float, and saturates.
		{"18446744073709551616", maxDelay, ""},
		{"18446744073709551615", maxDelay, ""},
		{"-1", 0, "Expected an integer, found: -1"},
		{"-9223372036854775808", 0, "Expected an integer, found: -9223372036854775808"},
		{`"1"`, 0, `Expected an integer, found: String("1")`},
		{`"a\"b\n"`, 0, `Expected an integer, found: String("a\"b\n")`},
		{"null", 0, "Expected an integer, found: Null"},
		{"true", 0, "Expected an integer, found: Bool(true)"},
		{"[1, 1.5, -2, null]", 0, "Expected an integer, found: Array [Number(1), Number(1.5), Number(-2), Null]"},
		{`{"b":1,"a":{"c":[]}}`, 0, `Expected an integer, found: Object {"b": Number(1), "a": Object {"c": Array []}}`},
		// serde_json's syntax errors, for text a replaced JSON.stringify
		// returns (JE-15): a broken literal, a truncated one, a leading
		// zero, a character no value starts with on a later line, more
		// than white space after the value.
		{"1 2", 0, "trailing characters at line 1 column 3"},
		{"nope", 0, "expected ident at line 1 column 2"},
		{"tru", 0, "EOF while parsing a value at line 1 column 3"},
		{"01", 0, "invalid number at line 1 column 2"},
		{"-01", 0, "invalid number at line 1 column 3"},
		{"\n  x", 0, "expected value at line 2 column 3"},
		{"", 0, "EOF while parsing a value at line 1 column 0"},
		{"null x", 0, "trailing characters at line 1 column 6"},
	} {
		got, err := sleepDelay([]byte(c.text))
		switch {
		case c.err != "" && (err == nil || err.Error() != c.err):
			t.Errorf("%s: error %v, want %q", c.text, err, c.err)
		case c.err == "" && (err != nil || got != c.want):
			t.Errorf("%s: %v, %v, want %v", c.text, got, err, c.want)
		}
	}
}

// JE-29: a javascript timeout's value deserialises as an i32, with
// serde_json's error and the evaluator's text as it is; a negative one
// fails with its own text.
func TestTimeoutResult(t *testing.T) {
	const p = "Error parsing result as i32: "
	for _, c := range []struct {
		text string
		want int32
		err  string
	}{
		{"5", 5, ""},
		{"0", 0, ""},
		{"2147483647", math.MaxInt32, ""},
		{"-1", 0, "Timeout value cannot be negative"},
		{"-2147483648", 0, "Timeout value cannot be negative"},
		// The recorded JE-29/a.
		{"1.5", 0, p + "invalid type: floating point `1.5`, expected i32 at line 1 column 3. Value was: 1.5"},
		{"2e0", 0, p + "invalid type: floating point `2.0`, expected i32 at line 1 column 3. Value was: 2e0"},
		// serde_json writes the float as rustFloat does (fx-24/js_exponent).
		{"1e21", 0, p + "invalid type: floating point `1e+21`, expected i32 at line 1 column 4. Value was: 1e21"},
		{"1e+21", 0, p + "invalid type: floating point `1e+21`, expected i32 at line 1 column 5. Value was: 1e+21"},
		{"-0", 0, p + "invalid type: floating point `-0.0`, expected i32 at line 1 column 2. Value was: -0"},
		{"2147483648", 0, p + "invalid value: integer `2147483648`, expected i32 at line 1 column 10. Value was: 2147483648"},
		{"-3000000000", 0, p + "invalid value: integer `-3000000000`, expected i32 at line 1 column 11. Value was: -3000000000"},
		{`"5"`, 0, p + `invalid type: string "5", expected i32 at line 1 column 3. Value was: "5"`},
		{"null", 0, p + "invalid type: null, expected i32 at line 1 column 4. Value was: null"},
		{"true", 0, p + "invalid type: boolean `true`, expected i32 at line 1 column 4. Value was: true"},
		{"false", 0, p + "invalid type: boolean `false`, expected i32 at line 1 column 5. Value was: false"},
		{"[1]", 0, p + "invalid type: sequence, expected i32 at line 1 column 0. Value was: [1]"},
		// A JE-9 lookup's text is quoted with its spacing (JE-29).
		{`{"a": 2, "b": 1}`, 0, p + `invalid type: map, expected i32 at line 1 column 0. Value was: {"a": 2, "b": 1}`},
		{"", 0, p + "EOF while parsing a value at line 1 column 0. Value was: "},
		{"1 2", 0, p + "trailing characters at line 1 column 3. Value was: 1 2"},
		{"1e400", 0, p + "number out of range at line 1 column 5. Value was: 1e400"},
		// serde_json's syntax errors come before the type: a broken
		// literal, a leading zero, a value on a later line.
		{"nope", 0, p + "expected ident at line 1 column 2. Value was: nope"},
		{"01", 0, p + "invalid number at line 1 column 2. Value was: 01"},
		{"\n \"5\"", 0, p + "invalid type: string \"5\", expected i32 at line 2 column 4. Value was: \n \"5\""},
		{"5 x", 0, p + "trailing characters at line 1 column 3. Value was: 5 x"},
	} {
		got, err := timeoutResult([]byte(c.text))
		switch {
		case c.err != "" && (err == nil || err.Error() != c.err):
			t.Errorf("%q: error %v, want %q", c.text, err, c.err)
		case c.err == "" && (err != nil || got != c.want):
			t.Errorf("%q: %d, %v, want %d", c.text, got, err, c.want)
		}
	}
	if _, err := timeoutResult([]byte("-1")); !errors.Is(err, errNegativeTimeout) {
		t.Fatalf("a negative timeout's error is %v", err)
	}
}

// fixedJitter draws p and up every time, and counts its draws.
type fixedJitter struct {
	p, n  int
	up    bool
	draws []int
}

func (j *fixedJitter) Draw(n int) (int, bool) {
	j.draws = append(j.draws, n)
	return min(j.p, n-1), j.up
}

func exponential(attempts uint32, multiplier, seconds uint16, factor *int8) *ir.Retry {
	return &ir.Retry{Exponential: &ir.ExponentialRetry{Attempts: attempts, Multiplier: multiplier, Seconds: seconds, RandomFactor: factor}}
}

func int8p(v int8) *int8 { return &v }

// FX-26: the constant phase, then the exponential phase with an exponent
// that counts every failure, in unsigned 16-bit arithmetic that saturates
// the power and wraps the product, and the 6-hour cap.
func TestRetryDelay(t *testing.T) {
	schedule := func(r *ir.Retry, j Jitter) []time.Duration {
		var out []time.Duration
		for n := uint32(0); ; n++ {
			d, ok := retryDelay(r, n, j)
			if ok != attemptsLeft(r, n) {
				t.Fatalf("retryDelay and attemptsLeft disagree at %d", n)
			}
			if !ok {
				return out
			}
			out = append(out, d)
		}
	}
	none := &fixedJitter{}
	for _, c := range []struct {
		name string
		r    *ir.Retry
		want []time.Duration
	}{
		// FX-26/a: 1, 1, 2^3 and 2^4.
		{"constant then exponential", &ir.Retry{
			Constant:    &ir.ConstantRetry{Attempts: 2, Seconds: 1},
			Exponential: &ir.ExponentialRetry{Attempts: 2, Multiplier: 1, Seconds: 2},
		}, []time.Duration{time.Second, time.Second, 8 * time.Second, 16 * time.Second}},
		// FX-26/b: 3 × 2^1.
		{"multiplier", exponential(1, 3, 2, nil), []time.Duration{6 * time.Second}},
		// FX-26/c: (250 × 300) mod 65536.
		{"16-bit wrap", exponential(1, 250, 300, nil), []time.Duration{9464 * time.Second}},
		// FX-26/d: the cap.
		{"6-hour cap", exponential(1, 1, 30000, nil), []time.Duration{21600 * time.Second}},
		// FX-26/e: 300^2 saturates at 65535, then the cap.
		{"saturation", exponential(2, 1, 300, nil), []time.Duration{300 * time.Second, 21600 * time.Second}},
		// The saturated power times 2 wraps: 65535 × 2 mod 65536.
		{"saturated power wraps", exponential(1, 2, 65535, nil), []time.Duration{21600 * time.Second}},
		{"wrap below the cap", exponential(1, 3, 21846, nil), []time.Duration{2 * time.Second}},
		{"constant over the cap", &ir.Retry{Constant: &ir.ConstantRetry{Attempts: 1, Seconds: 30000}}, []time.Duration{21600 * time.Second}},
		{"zero attempts", &ir.Retry{Constant: &ir.ConstantRetry{}}, nil},
		{"no phases", &ir.Retry{}, nil},
	} {
		got := schedule(c.r, none)
		if len(got) != len(c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: %v, want %v", c.name, got, c.want)
			}
		}
	}
	if len(none.draws) != 0 {
		t.Fatalf("drew jitter %v without a random_factor", none.draws)
	}

	// The jitter applies to the exponential phase only, as p percent of
	// the delay, up or down, with p drawn from [0, min(random_factor,
	// 100)).
	jittered := &ir.Retry{
		Constant:    &ir.ConstantRetry{Attempts: 1, Seconds: 10},
		Exponential: &ir.ExponentialRetry{Attempts: 1, Multiplier: 1, Seconds: 10, RandomFactor: int8p(50)},
	}
	up := &fixedJitter{p: 20, up: true}
	if got := schedule(jittered, up); len(got) != 2 || got[0] != 10*time.Second || got[1] != 120*time.Second {
		t.Fatalf("up: %v", got)
	}
	if len(up.draws) != 1 || up.draws[0] != 50 {
		t.Fatalf("draws %v, want one from [0, 50)", up.draws)
	}
	down := &fixedJitter{p: 20}
	if got := schedule(jittered, down); got[1] != 80*time.Second {
		t.Fatalf("down: %v", got)
	}
	big := &fixedJitter{p: 99}
	schedule(exponential(1, 1, 10, int8p(120)), big)
	if big.draws[0] != 100 {
		t.Fatalf("a random_factor above 100 draws from %d", big.draws[0])
	}
	for _, f := range []int8{0, -5} {
		j := &fixedJitter{p: 50, up: true}
		if got := schedule(exponential(1, 1, 10, int8p(f)), j); got[0] != 10*time.Second || len(j.draws) != 0 {
			t.Fatalf("random_factor %d: %v, draws %v", f, got, j.draws)
		}
	}
}

// FX-26's jitter in 16 bits: the product wraps before the division, and
// the sum saturates.
func TestExponentialJitterArithmetic(t *testing.T) {
	e := &ir.ExponentialRetry{Attempts: 1, Multiplier: 60000, Seconds: 1, RandomFactor: int8p(100)}
	// delay 60000, p 99: (60000 × 99) mod 65536 = 41760, / 100 = 417.
	if got := exponentialDelay(e, 0, &fixedJitter{p: 99, up: true}); got != 60417 {
		t.Fatalf("up: %d", got)
	}
	if got := exponentialDelay(e, 0, &fixedJitter{p: 99}); got != 59583 {
		t.Fatalf("down: %d", got)
	}
	e.Multiplier = 65000
	// delay 65000, p 1: 650 / 100 = 6, and 65006 does not saturate; p 2:
	// 130000 mod 65536 = 64464, / 100 = 644, which saturates at 65535.
	if got := exponentialDelay(e, 0, &fixedJitter{p: 2, up: true}); got != math.MaxUint16 {
		t.Fatalf("saturated: %d", got)
	}
	e.Multiplier, e.Seconds = 1, 0
	if got := exponentialDelay(e, 3, &fixedJitter{p: 50}); got != 0 {
		t.Fatalf("a base of 0: %d", got)
	}
}

// H10: a seeded jitter repeats its draws, which stay in range.
func TestSeededJitter(t *testing.T) {
	a, b := SeededJitter(7), SeededJitter(7)
	ups := 0
	for i := range 200 {
		n := 1 + i%100
		pa, ua := a.Draw(n)
		pb, ub := b.Draw(n)
		if pa != pb || ua != ub {
			t.Fatalf("draw %d differs: %d %t, %d %t", i, pa, ua, pb, ub)
		}
		if pa < 0 || pa >= n {
			t.Fatalf("draw %d: %d out of [0, %d)", i, pa, n)
		}
		if ua {
			ups++
		}
	}
	if ups == 0 || ups == 200 {
		t.Fatalf("%d of 200 draws went up", ups)
	}
}

// The debug form of serde_json's Value, which a sleep's error quotes: its
// floats in rustFloat's spelling, its objects in insertion order.
func TestRustValueDebug(t *testing.T) {
	for text, want := range map[string]string{
		`[1e21, 1.5, 2.0, -0, 1e-7]`:  "Array [Number(1e+21), Number(1.5), Number(2.0), Number(-0.0), Number(1e-7)]",
		`{"z": 18446744073709551615}`: `Object {"z": Number(18446744073709551615)}`,
		`"\u0001"`:                    `String("\u{1}")`,
	} {
		v, err := decodeJSON([]byte(text))
		if err != nil {
			t.Fatal(err)
		}
		if got := rustValueDebug(v); got != want {
			t.Errorf("%s: %s, want %s", text, got, want)
		}
	}
}

// H10's record mode: a wait is recorded and returns at once, and the
// clock runs ahead by it; an ended context ends the wait with its cause.
func TestRecordClock(t *testing.T) {
	var c RecordClock
	before := c.Now()
	start := time.Now()
	if err := c.Wait(context.Background(), time.Hour); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("the recording clock slept")
	}
	if got := c.Now().Sub(before); got < time.Hour {
		t.Fatalf("the clock moved %v", got)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("stop")
	cancel(cause)
	if err := c.Wait(ctx, time.Minute); err != cause {
		t.Fatalf("an ended context: %v", err)
	}
	if got := c.Waits(); len(got) != 1 || got[0] != time.Hour {
		t.Fatalf("waits %v", got)
	}
}
