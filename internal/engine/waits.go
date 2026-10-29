package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/invakid404/windmill-lite/internal/ir"
)

// The values of a module's sleep and timeout and the retry delay
// (milestone N2): the texts Windmill's worker reads them with, and the
// arithmetic it applies.

// maxDelay is the longest wait windmill-lite schedules: a sleep beyond it
// waits this long (about 292 years), where Windmill's scheduled time would
// overflow.
const maxDelay = time.Duration(math.MaxInt64)

// sleepDelay reads a sleep's value, the text a static value is written in
// or an evaluation returned (FX-23, JE-28):
//
//   - a JSON integer that fits in 64 unsigned bits is that many seconds;
//   - any other JSON number (a fraction or an exponent, a negative zero, an
//     integer beyond 64 bits) is value × 1000 milliseconds, truncated, a
//     negative one 0;
//   - a negative integer fails with "Expected an integer, found: <n>";
//   - any other JSON value fails with "Expected an integer, found: <its
//     debug form>" (rustValueDebug), such as String("1") or Null.
//
// The error's text is a chaining error's (FX-15), with no prefix.
func sleepDelay(text []byte) (time.Duration, error) {
	v, err := decodeJSON(text)
	if err != nil {
		return 0, err
	}
	n, ok := v.(json.Number)
	if !ok {
		return 0, fmt.Errorf("Expected an integer, found: %s", rustValueDebug(v))
	}
	switch k, u, i, f := classifyNumber(string(n)); k {
	case numberUnsigned:
		return seconds(u), nil
	case numberSigned:
		return 0, fmt.Errorf("Expected an integer, found: %d", i)
	default:
		// Rust's float-to-integer cast saturates: a negative or NaN value
		// is 0, one beyond the range the maximum.
		ms := f * 1000
		switch {
		case !(ms > 0):
			return 0, nil
		case ms >= float64(maxDelay/time.Millisecond):
			return maxDelay, nil
		}
		return time.Duration(ms) * time.Millisecond, nil
	}
}

// seconds is n seconds, at most maxDelay.
func seconds(n uint64) time.Duration {
	if n > uint64(maxDelay/time.Second) {
		return maxDelay
	}
	return time.Duration(n) * time.Second
}

// timeoutResult reads a javascript timeout's value, the evaluator's text,
// as Windmill's worker deserialises it into a 32-bit signed integer of
// seconds (JE-29). A value that does not deserialise fails with "Error
// parsing result as i32: <serde_json's error>. Value was: <the text as it
// is>"; a negative one with "Timeout value cannot be negative".
func timeoutResult(text []byte) (int32, error) {
	n, err := parseI32(text)
	if err != nil {
		return 0, fmt.Errorf("Error parsing result as i32: %s. Value was: %s", err, text)
	}
	if n < 0 {
		return 0, errNegativeTimeout
	}
	return n, nil
}

// errNegativeTimeout is the chaining error of a negative timeout (FX-24).
var errNegativeTimeout = errors.New("Timeout value cannot be negative")

// retryDelay is FX-26's schedule for a module that has failed n times in
// the current retry sequence (the flow-wide counter of FX-29): the delay
// of the next attempt, or false when no attempt is left. The constant phase
// comes first; the exponential phase's exponent counts every failure, the
// constant phase's included, in the released build's unsigned 16-bit
// arithmetic, where an overflow wraps. Only the exponential phase is
// jittered, with j's draws. The delay is at most 21600 s.
func retryDelay(r *ir.Retry, n uint32, j Jitter) (time.Duration, bool) {
	var secs uint16
	var constant uint32
	if r.Constant != nil {
		constant = r.Constant.Attempts
	}
	switch e := r.Exponential; {
	case n < constant:
		secs = r.Constant.Seconds
	case e != nil && n-constant < e.Attempts:
		secs = exponentialDelay(e, n, j)
	default:
		return 0, false
	}
	return time.Duration(min(secs, 21600)) * time.Second, true
}

// exponentialDelay is the exponential phase's delay for the (n+1)th
// failure (FX-26): seconds^(n+1) saturating at 65535, times the multiplier
// modulo 65536, then the jitter when random_factor is positive.
func exponentialDelay(e *ir.ExponentialRetry, n uint32, j Jitter) uint16 {
	pow := saturatingPow(e.Seconds, uint64(n)+1)
	delay := e.Multiplier * pow // uint16: wraps, as an unchecked release build does
	if e.RandomFactor == nil || *e.RandomFactor <= 0 {
		return delay
	}
	p, up := j.Draw(min(int(*e.RandomFactor), 100))
	delta := delay * uint16(p) / 100
	if up {
		if delay > math.MaxUint16-delta {
			return math.MaxUint16
		}
		return delay + delta
	}
	if delta > delay {
		return 0
	}
	return delay - delta
}

// saturatingPow is base^exp in unsigned 16 bits, saturating at 65535.
func saturatingPow(base uint16, exp uint64) uint16 {
	r := uint64(1)
	for ; exp > 0; exp-- {
		if r *= uint64(base); r >= math.MaxUint16 {
			return math.MaxUint16
		}
		if r == 0 {
			return 0
		}
	}
	return uint16(r)
}

// Jitter is the retry-jitter source (H10): the random draws of FX-26's
// exponential phase. A run that sets none draws from its own seed
// (RunRequest.Seed), so a fixed seed fixes the jitter. Implementations
// must be safe for concurrent use.
type Jitter interface {
	// Draw returns p, drawn uniformly from [0, n) for 1 <= n <= 100, and
	// whether the delay goes up by p percent (probability ½) or down.
	Draw(n int) (p int, up bool)
}

// SeededJitter returns a Jitter whose draws are fixed by seed.
func SeededJitter(seed uint64) Jitter {
	return &seededJitter{rng: rand.New(rand.NewPCG(seed, seed^0x5851f42d4c957f2d))}
}

type seededJitter struct {
	mu  sync.Mutex
	rng *rand.Rand
}

func (s *seededJitter) Draw(n int) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rng.IntN(n), s.rng.IntN(2) == 1
}

// RecordClock is H10's record mode: it records every requested wait and
// returns at once, and its time runs ahead of the wall clock by the waits
// it recorded, so that the times a run reads stay in order. A run on it
// records H10 among its trace's deviations, since its waits were not
// slept (ADR 0008). The zero RecordClock is ready to use, is safe for
// concurrent use and must not be copied after first use.
type RecordClock struct {
	mu    sync.Mutex
	ahead time.Duration
	waits []time.Duration
}

// Now returns the wall time plus the waits recorded so far.
func (c *RecordClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.ahead)
}

// Wait records d and returns at once, with context.Cause(ctx) if ctx has
// already ended.
func (c *RecordClock) Wait(ctx context.Context, d time.Duration) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.waits = append(c.waits, d)
	if d > 0 {
		c.ahead += d
	}
	return nil
}

// Waits returns the waits recorded so far, in order.
func (c *RecordClock) Waits() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.waits...)
}

// decodeJSON decodes one JSON value: its numbers as json.Number, its
// objects as orderedObject. Text that is not one JSON value (an evaluation
// can return such text, B2b's readback) fails with serde_json's message
// for the commonest cases.
func decodeJSON(text []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(text))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return nil, fmt.Errorf("expected value at line 1 column %d", leadingSpace(text)+1)
	}
	if rest := text[dec.InputOffset():]; len(bytes.TrimSpace(rest)) > 0 {
		return nil, fmt.Errorf("trailing characters at line 1 column %d", int(dec.InputOffset())+leadingSpace(rest)+1)
	}
	return v, nil
}

// decodeValue decodes the next value of dec, keeping object members in
// order.
func decodeValue(dec *json.Decoder) (any, error) {
	t, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t {
	case json.Delim('['):
		arr := []any{}
		for dec.More() {
			v, err := decodeValue(dec)
			if err != nil {
				return nil, err
			}
			arr = append(arr, v)
		}
		_, err := dec.Token()
		return arr, err
	case json.Delim('{'):
		obj := orderedObject{}
		for dec.More() {
			k, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, ok := k.(string)
			if !ok {
				return nil, errors.New("engine: an object key that is not a string")
			}
			v, err := decodeValue(dec)
			if err != nil {
				return nil, err
			}
			obj = append(obj, orderedMember{key, v})
		}
		_, err := dec.Token()
		return obj, err
	case json.Delim(']'), json.Delim('}'):
		return nil, io.ErrUnexpectedEOF
	}
	return t, nil
}

func leadingSpace(text []byte) int {
	return len(text) - len(bytes.TrimLeft(text, " \t\r\n"))
}

// numberKind is how serde_json holds a JSON number: an unsigned or a
// negative integer of 64 bits, or a float.
type numberKind uint8

const (
	numberUnsigned numberKind = iota
	numberSigned
	numberFloat
)

// classifyNumber reads a JSON number's text as serde_json does without
// arbitrary precision: an integer (no fraction or exponent) that fits in
// u64, or is negative and fits in i64, is an integer; anything else,
// negative zero included, is the nearest float.
func classifyNumber(s string) (numberKind, uint64, int64, float64) {
	if !strings.ContainsAny(s, ".eE") {
		if u, err := strconv.ParseUint(s, 10, 64); err == nil {
			return numberUnsigned, u, 0, 0
		}
		if i, err := strconv.ParseInt(s, 10, 64); err == nil && i < 0 {
			return numberSigned, 0, i, 0
		}
	}
	f, _ := strconv.ParseFloat(s, 64)
	return numberFloat, 0, 0, f
}

// parseI32 deserialises text into an i32 as serde_json::from_str does,
// with its error text: "invalid type: …" or "invalid value: …", "expected
// i32", then the position after the bytes read, "at line L column C".
func parseI32(text []byte) (int32, error) {
	i := leadingSpace(text)
	at := func(pos int, msg string) error {
		line, col := 1, pos
		if nl := bytes.LastIndexByte(text[:pos], '\n'); nl >= 0 {
			line, col = 1+bytes.Count(text[:nl+1], []byte{'\n'}), pos-nl-1
		}
		return fmt.Errorf("%s at line %d column %d", msg, line, col)
	}
	invalid := func(pos int, unexpected string) error {
		return at(pos, "invalid type: "+unexpected+", expected i32")
	}
	if i == len(text) {
		return 0, at(i, "EOF while parsing a value")
	}
	dec := json.NewDecoder(bytes.NewReader(text[i:]))
	dec.UseNumber()
	var v any
	switch text[i] {
	case '[':
		return 0, invalid(i, "sequence")
	case '{':
		return 0, invalid(i, "map")
	}
	if err := dec.Decode(&v); err != nil {
		return 0, at(i+1, "expected value")
	}
	end := i + int(dec.InputOffset())
	var n int32
	var err error
	switch v := v.(type) {
	case nil:
		err = invalid(end, "null")
	case bool:
		err = invalid(end, fmt.Sprintf("boolean `%t`", v))
	case string:
		err = invalid(end, "string "+rustDebug(v))
	case json.Number:
		switch k, u, s, f := classifyNumber(string(v)); {
		case k == numberUnsigned && u <= math.MaxInt32:
			n = int32(u)
		case k == numberUnsigned:
			err = at(end, fmt.Sprintf("invalid value: integer `%d`, expected i32", u))
		case k == numberSigned && s >= math.MinInt32:
			n = int32(s)
		case k == numberSigned:
			err = at(end, fmt.Sprintf("invalid value: integer `%d`, expected i32", s))
		case math.IsInf(f, 0):
			// serde_json refuses a number beyond f64's range as it reads it.
			err = at(end, "number out of range")
		default:
			err = invalid(end, "floating point `"+floatDisplay(f)+"`")
		}
	}
	if err != nil {
		return 0, err
	}
	if rest := text[end:]; len(bytes.TrimSpace(rest)) > 0 {
		return 0, at(end+leadingSpace(rest)+1, "trailing characters")
	}
	return n, nil
}

// floatDisplay writes f as serde's Unexpected::Float does: Rust's Display
// of an f64 (the shortest digits that read back, never an exponent), with
// ".0" added when it has no decimal point.
func floatDisplay(f float64) string {
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return map[bool]string{true: "inf", false: "-inf"}[f > 0]
	}
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

// rustValueDebug writes a JSON value as the Debug form of serde_json's
// Value does, its objects in insertion order (the backend builds
// serde_json with preserve_order, JE-44): Null, Bool(true), Number(1.5),
// String("…"), Array [Number(1)], Object {"a": Null}.
func rustValueDebug(v any) string {
	var b strings.Builder
	writeValueDebug(&b, v)
	return b.String()
}

func writeValueDebug(b *strings.Builder, v any) {
	switch v := v.(type) {
	case nil:
		b.WriteString("Null")
	case bool:
		fmt.Fprintf(b, "Bool(%t)", v)
	case json.Number:
		b.WriteString("Number(")
		switch k, u, i, f := classifyNumber(string(v)); k {
		case numberUnsigned:
			b.WriteString(strconv.FormatUint(u, 10))
		case numberSigned:
			b.WriteString(strconv.FormatInt(i, 10))
		default:
			b.WriteString(ryuFormat(f))
		}
		b.WriteByte(')')
	case string:
		b.WriteString("String(" + rustDebug(v) + ")")
	case []any:
		b.WriteString("Array [")
		for i, e := range v {
			if i > 0 {
				b.WriteString(", ")
			}
			writeValueDebug(b, e)
		}
		b.WriteByte(']')
	case orderedObject:
		b.WriteString("Object {")
		for i, m := range v {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(rustDebug(m.key) + ": ")
			writeValueDebug(b, m.value)
		}
		b.WriteByte('}')
	}
}

// ryuFormat writes f as the ryu crate's format_finite does, which
// serde_json's Number displays floats with: the shortest digits, in
// positional form when the decimal point falls within 16 digits of them
// and in scientific form (1e30, 1.234e-7) otherwise, with ".0" on an
// integral value.
func ryuFormat(f float64) string {
	if f == 0 {
		if math.Signbit(f) {
			return "-0.0"
		}
		return "0.0"
	}
	sign := ""
	if f < 0 {
		sign, f = "-", -f
	}
	// strconv's shortest form: d.ddde±x.
	e := strconv.FormatFloat(f, 'e', -1, 64)
	mant, expText, _ := strings.Cut(e, "e")
	digits := strings.Replace(mant, ".", "", 1)
	exp, _ := strconv.Atoi(expText)
	length := len(digits)
	k := exp - (length - 1) // f = digits × 10^k
	kk := length + k        // where the decimal point falls
	switch {
	case k >= 0 && kk <= 16:
		return sign + digits + strings.Repeat("0", k) + ".0"
	case kk > 0 && kk <= 16:
		return sign + digits[:kk] + "." + digits[kk:]
	case kk > -5 && kk <= 0:
		return sign + "0." + strings.Repeat("0", -kk) + digits
	case length == 1:
		return sign + digits + "e" + strconv.Itoa(kk-1)
	}
	return sign + digits[:1] + "." + digits[1:] + "e" + strconv.Itoa(kk-1)
}

// orderedObject is a JSON object with its members in order.
type orderedObject []orderedMember

type orderedMember struct {
	key   string
	value any
}
