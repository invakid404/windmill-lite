package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/expr"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// member is one member of a JSON object: its key and its value's text.
type member struct {
	key   string
	value []byte
}

// objectOf writes the members as a JSON object, in order.
func objectOf(ms ...member) jsonval.Value {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range ms {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(jsonString(m.key))
		b.WriteByte(':')
		b.Write(m.value)
	}
	b.WriteByte('}')
	return jsonval.FromProducer(b.Bytes())
}

// jsonString encodes s as a JSON string, without HTML escaping, as
// Windmill's server writes strings.
func jsonString(s string) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		panic(err)
	}
	return bytes.TrimSpace(b.Bytes())
}

// orNull is raw, or null for an absent value (JE-V4).
func orNull(raw []byte) []byte {
	if len(bytes.TrimSpace(raw)) == 0 {
		return []byte("null")
	}
	return raw
}

func isObject(v jsonval.Value) bool {
	t := bytes.TrimSpace(v.Producer())
	return len(t) > 0 && t[0] == '{' && json.Valid(t)
}

// members splits a JSON object's text into its top-level members, each
// key and value as written. ok is false for text that is not one JSON
// object.
func members(obj []byte) (ms []rawMember, ok bool) {
	dec := json.NewDecoder(bytes.NewReader(obj))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, false
	}
	for dec.More() {
		start := dec.InputOffset()
		var key string
		if t, err := dec.Token(); err != nil {
			return nil, false
		} else if key, ok = t.(string); !ok {
			return nil, false
		}
		keyEnd := dec.InputOffset()
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, false
		}
		ms = append(ms, rawMember{key: key, rawKey: bytes.TrimLeft(bytes.TrimSpace(obj[start:keyEnd]), ","), value: v})
	}
	if t, err := dec.Token(); err != nil || t != json.Delim('}') {
		return nil, false
	}
	if _, err := dec.Token(); err == nil {
		return nil, false
	}
	return ms, true
}

// rawMember is a member as written: rawKey is the key's JSON text.
type rawMember struct {
	key    string
	rawKey []byte
	value  []byte
}

// writeMembers writes members as an object, keys as written.
func writeMembers(ms []rawMember) []byte {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range ms {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(bytes.TrimSpace(m.rawKey))
		b.WriteByte(':')
		b.Write(m.value)
	}
	b.WriteByte('}')
	return b.Bytes()
}

// flowInputText is the text of a flow input as the worker holds it after
// reading the stored arguments (JE-44): a compact object whose top-level
// keys come in an order rng draws, each member's value in jsonb's printed
// form.
func flowInputText(stored []byte, rng *rand.Rand) []byte {
	ms, ok := members(stored)
	if !ok {
		return stored
	}
	for i := range ms {
		ms[i].value = expr.DatabaseText(ms[i].value)
	}
	rng.Shuffle(len(ms), func(i, j int) { ms[i], ms[j] = ms[j], ms[i] })
	return writeMembers(ms)
}

// storeMembers is the storage boundary of a job's arguments (ADR 0005
// step 3): each top-level argument becomes its stored text, in the order
// they come. An argument jsonb refuses fails the push of the job, whose
// error is returned with PostgreSQL's message as Windmill's database layer
// displays it (FX-48 case 2; the exact text is open, Q-FX-8).
func storeMembers(args jsonval.Value) (jsonval.Value, error) {
	ms, ok := members(args.Producer())
	if !ok {
		return jsonval.Value{}, errors.New("error returned from database: invalid input syntax for type json")
	}
	for i, m := range ms {
		s, err := jsonval.FromProducer(m.value).Stored()
		if err != nil {
			var se *jsonval.StoreError
			if errors.As(err, &se) {
				return jsonval.Value{}, errors.New("error returned from database: " + se.DatabaseMessage())
			}
			return jsonval.Value{}, err
		}
		ms[i].value = s
	}
	return jsonval.FromProducer(writeMembers(ms)), nil
}

// identityResult is what an identity job returns: its stored arguments'
// previous_result, or {} when they have none (FX-16). The job reads its
// arguments from storage, so the value is in stored form.
func identityResult(stored jsonval.Value) jsonval.Value {
	ms, _ := members(stored.Producer())
	for _, m := range ms {
		if m.key == "previous_result" {
			return jsonval.FromProducer(m.value)
		}
	}
	return jsonval.FromProducer([]byte("{}"))
}

// errorName is the name of the error a failure value carries, as H7
// matches it: E's name in {"error": E}, or "" when there is none.
func errorName(v jsonval.Value) string {
	var top struct {
		Error struct {
			Name string `json:"name"`
		} `json:"error"`
	}
	if v.IsZero() || json.Unmarshal(v.Producer(), &top) != nil {
		return ""
	}
	return top.Error.Name
}

// engineError is a pre-run or pickup failure: {"error": {"message",
// "name"}} with no step_id (FX-12, FX-14, FX-15).
func engineError(name, message string) jsonval.Value {
	return wrapped(struct {
		Message string `json:"message"`
		Name    string `json:"name"`
	}{message, name})
}

// transformFailure is an input transform's failed evaluation.
type transformFailure struct {
	text string // the expression as written (JE-5)
	err  error
}

func (e *transformFailure) Error() string { return e.err.Error() }
func (e *transformFailure) Unwrap() error { return e.err }

// transformError is the failure value of a step whose input transform
// failed (FX-14, JE-23): ExecutionErr, quoting the expression as written
// and the evaluator's text.
func transformError(err error) jsonval.Value {
	text := ""
	var tf *transformFailure
	if errors.As(err, &tf) {
		text = tf.text
	}
	return engineError("ExecutionErr", "Error during isolated evaluation of expression `"+text+"`:\n"+evalText(err))
}

// evalText is an evaluation's error text: the evaluator's message, with
// Windmill's texts (B2b).
func evalText(err error) string {
	var ee *expr.EvalError
	if errors.As(err, &ee) {
		return ee.Message
	}
	return err.Error()
}

// notBoolean is a predicate whose value is not JSON true or false (JE-24).
type notBoolean struct{ text string }

func (e *notBoolean) Error() string {
	return "Expected a boolean value, found: " + rustDebug(e.text)
}

// predicateText is the display text of a predicate's failure, as Windmill
// writes it before a site routes it (JE-24): the strict check's own text,
// or an evaluation error as the generic error "Error: <evaluator text>".
// Windmill ends the generic one with a Rust source location, which
// windmill-lite does not write (D14).
func predicateText(err error) string {
	var nb *notBoolean
	if errors.As(err, &nb) {
		return nb.Error()
	}
	return "Error: " + evalText(err)
}

// rustDebug quotes s as Rust's Debug formatting of a string does: in
// double quotes, with the quote, the backslash, \t, \r, \n and \0
// escaped, and other control characters as \u{…}.
func rustDebug(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, c := range s {
		switch c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\t':
			b.WriteString(`\t`)
		case '\r':
			b.WriteString(`\r`)
		case '\n':
			b.WriteString(`\n`)
		case 0:
			b.WriteString(`\0`)
		default:
			if c < 0x20 || c == 0x7f {
				fmt.Fprintf(&b, `\u{%x}`, c)
			} else {
				b.WriteRune(c)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// FatalError is the error a run latches when a module fails with an error
// whose name the host listed as fatal (H7).
type FatalError struct {
	// Site is the module's site; Name the error's name; Value the failure
	// value the module carried.
	Site  conformance.SitePath
	Name  string
	Value jsonval.Value
}

func (e *FatalError) Error() string {
	return fmt.Sprintf("engine: module %s failed with %s, which the host lists as fatal", e.Site, e.Name)
}
