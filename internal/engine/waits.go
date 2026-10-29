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
// returns such text when the user's code replaces JSON.stringify, JE-15)
// fails with serde_json's error, which serde.go writes: the end of the
// text, a character no value starts with or a broken true, false or null
// (serdeSyntax), a leading zero (serdeNumber), then more than white space
// after the value (serdeTrailing). Reading: serde_json's other syntax
// errors, for a value that starts well and breaks later, are not
// reproduced: they read "expected value" where the decoder stopped, or
// "EOF while parsing a value" at the end.
func decodeJSON(text []byte) (any, error) {
	if msg, ok := serdeSyntax(text); ok {
		return nil, notJSON(msg)
	}
	t := bytes.TrimLeft(text, " \t\r\n")
	start := len(text) - len(t)
	if t[0] == '-' || t[0] >= '0' && t[0] <= '9' {
		if msg, bad := serdeNumber(text, start, numberToken(t)); bad {
			return nil, notJSON(msg)
		}
	}
	dec := json.NewDecoder(bytes.NewReader(text))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		var se *json.SyntaxError
		if errors.As(err, &se) {
			return nil, notJSON("expected value" + serdePosition(text, int(se.Offset)))
		}
		return nil, notJSON("EOF while parsing a value" + serdePosition(text, len(text)))
	}
	if msg, trailing := serdeTrailing(text, int(dec.InputOffset())); trailing {
		return nil, notJSON(msg)
	}
	return v, nil
}

// notJSON is serde_json's error for a text that is not one JSON value.
type notJSON string

func (e notJSON) Error() string { return string(e) }

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
// with its error text, on the serde helpers of serde.go that parseU16
// uses too: a syntax error first (serdeSyntax, serdeNumber); then an
// integer out of the i32 range ("invalid value: integer `n`, expected
// i32"), a number beyond f64's range ("number out of range") or a value
// of another type (serdeError's "invalid type: …"), each at the position
// after the bytes read, "at line L column C"; then more than white space
// after an i32 (serdeTrailing). serde_json checks a value's type and range
// before what follows it.
func parseI32(text []byte) (int32, error) {
	if msg, ok := serdeSyntax(text); ok {
		return 0, errors.New(msg)
	}
	t := bytes.TrimLeft(text, " \t\r\n")
	start := len(text) - len(t)
	if t[0] != '-' && (t[0] < '0' || t[0] > '9') {
		return 0, errors.New(serdeError(text, "i32"))
	}
	tok := numberToken(t)
	pos := start + len(tok)
	if msg, bad := serdeNumber(text, start, tok); bad {
		return 0, errors.New(msg)
	}
	var n int32
	switch k, u, s, f := classifyNumber(string(tok)); {
	case k == numberUnsigned && u <= math.MaxInt32:
		n = int32(u)
	case k == numberUnsigned:
		return 0, fmt.Errorf("invalid value: integer `%d`, expected i32%s", u, serdePosition(text, pos))
	case k == numberSigned && s >= math.MinInt32:
		n = int32(s)
	case k == numberSigned:
		return 0, fmt.Errorf("invalid value: integer `%d`, expected i32%s", s, serdePosition(text, pos))
	case math.IsInf(f, 0):
		// serde_json refuses a number beyond f64's range as it reads it.
		return 0, errors.New("number out of range" + serdePosition(text, pos))
	default:
		return 0, errors.New(serdeError(text, "i32"))
	}
	if msg, trailing := serdeTrailing(text, pos); trailing {
		return 0, errors.New(msg)
	}
	return n, nil
}

// rustValueDebug writes a JSON value as the Debug form of serde_json's
// Value does, its objects in insertion order (the backend builds
// serde_json with preserve_order, JE-44): Null, Bool(true), Number(1.5),
// String("…"), Array [Number(1)], Object {"a": Null}, a float in
// rustFloat's spelling (Number(1e+21)).
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
			b.WriteString(rustFloat(f))
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

// orderedObject is a JSON object with its members in order.
type orderedObject []orderedMember

type orderedMember struct {
	key   string
	value any
}
