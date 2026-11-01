package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/expr"
	"github.com/invakid404/windmill-lite/internal/jsonlib"
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
	return len(t) > 0 && t[0] == '{' && jsonlib.Valid(t)
}

// members splits a JSON object's text into its top-level members, each
// key and value as written. ok is false for text that is not one JSON
// object.
func members(obj []byte) (ms []rawMember, ok bool) {
	if !jsonlib.Members(obj, func(key string, ks, ke, vs, ve int) {
		ms = append(ms, rawMember{key: key, rawKey: obj[ks:ke:ke], value: obj[vs:ve:ve]})
	}) {
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
// step 3): each top-level argument, its key and its value, becomes its
// stored text, in the order they come; of two keys that store alike, the
// last one's value is kept, as jsonb keeps it. A key or value that jsonb
// refuses (a NUL escape, FX-48; an unpaired surrogate escape or a number
// beyond numeric's range, FX-51), or arguments whose text is not JSON (a
// replaced JSON.stringify, JE-15), fail the push of the job, whose error
// is the push's database error (pushError, FX-48 case 2, FX-50).
func storeMembers(args jsonval.Value) (jsonval.Value, error) {
	ms, ok := members(args.Producer())
	if !ok {
		return jsonval.Value{}, errors.New(sqlErr + "error returned from database: invalid input syntax for type json")
	}
	var out []rawMember
	at := map[string]int{}
	for _, m := range ms {
		k, err := jsonval.FromProducer(m.rawKey).Stored()
		if err != nil {
			return jsonval.Value{}, pushError(err)
		}
		v, err := jsonval.FromProducer(m.value).Stored()
		if err != nil {
			return jsonval.Value{}, pushError(err)
		}
		if j, dup := at[string(k)]; dup {
			out[j].value = v
			continue
		}
		at[string(k)] = len(out)
		out = append(out, rawMember{key: m.key, rawKey: k, value: v})
	}
	return jsonval.FromProducer(writeMembers(out)), nil
}

// databaseError is the error Windmill's database layer reports for a text
// jsonb refuses, as the completion insert of a job's result quotes it
// after its own Internal text (FX-48 case 1).
func databaseError(err error) error {
	var se *jsonval.StoreError
	if errors.As(err, &se) {
		return errors.New("error returned from database: " + se.DatabaseMessage())
	}
	return err
}

// sqlErr is the display prefix of Windmill's database error kind (FX-50).
const sqlErr = "SqlErr: "

// pushError is the error of a statement that stores a text jsonb refuses
// and returns the database error as it is: the push of a job, whose
// arguments or a child flow's input it stores (FX-48 and FX-51 case 2), or
// the insert of a dispatched run's job (case 3). It displays as
// SqlErr: error returned from database: <PostgreSQL's message> (FX-50),
// without the location suffix Windmill adds (D14).
func pushError(err error) error {
	return errors.New(sqlErr + databaseError(err).Error())
}

// identityResult is what an identity job returns: its stored arguments'
// previous_result, or {} when they have none (FX-16). The job reads its
// arguments from storage, decoded member by member from the jsonb column,
// as the worker reads a flow input's members (JE-44), so the value is in
// jsonb's printed form: a space after each : and , outside strings
// (expr.DatabaseText).
func identityResult(stored jsonval.Value) jsonval.Value {
	ms, _ := members(stored.Producer())
	for _, m := range ms {
		if m.key == "previous_result" {
			return jsonval.FromProducer(expr.DatabaseText(m.value))
		}
	}
	return jsonval.FromProducer([]byte("{}"))
}

// canceledError is the result of a canceled flow (FX-38), whatever its
// modules reported.
func canceledError(reason, canceler string) jsonval.Value {
	return wrapped(struct {
		Message  string `json:"message"`
		Name     string `json:"name"`
		Reason   string `json:"reason"`
		Canceler string `json:"canceler"`
	}{"Job canceled: " + reason + " by " + canceler, "Canceled", reason, canceler})
}

// killedError is the failure value of a step whose job a cancellation
// killed while it ran (FX-38): the worker's text, ExecutionErr, step_id.
func killedError(stepID, reason, canceler string) jsonval.Value {
	return workerError(stepID, "execution error:\njob process terminated due to cancelled by "+canceler+" (reason: "+reason+")")
}

// recovers reports whether a failure module's successful result makes the
// flow succeed (FX-33): it decodes as the one-field recovery record with
// recover equal to JSON true. The decoder is serde's derived struct
// decoder: an object whose recover member is a boolean, or null or absent
// for none, unknown members ignored and a repeated recover refused; or an
// array of exactly one element, the field by position, a boolean or null.
// Anything else is a decode error, which does not recover.
func recovers(result jsonval.Value) bool {
	t := bytes.TrimSpace(result.Producer())
	var field []byte
	switch {
	case len(t) > 0 && t[0] == '{':
		ms, ok := members(t)
		if !ok {
			return false
		}
		for _, m := range ms {
			if m.key == "recover" {
				if field != nil {
					return false
				}
				field = m.value
			}
		}
	case len(t) > 0 && t[0] == '[':
		var elems []json.RawMessage
		if jsonlib.Unmarshal(t, &elems) != nil || len(elems) != 1 {
			return false
		}
		field = elems[0]
	default:
		return false
	}
	return string(bytes.TrimSpace(field)) == "true"
}

// rfc3339 writes t as a UTC RFC 3339 timestamp whose fraction has 0, 3, 6
// or 9 digits, the fewest that keep its precision, as Windmill's server
// writes a start time.
func rfc3339(t time.Time) string {
	t = t.UTC()
	layout := "2006-01-02T15:04:05Z"
	switch ns := t.Nanosecond(); {
	case ns == 0:
	case ns%1e6 == 0:
		layout = "2006-01-02T15:04:05.000Z"
	case ns%1e3 == 0:
		layout = "2006-01-02T15:04:05.000000Z"
	default:
		layout = "2006-01-02T15:04:05.000000000Z"
	}
	return t.Format(layout)
}

// errorName is the name of the error a failure value carries, as H7
// matches it: E's name in {"error": E}, or "" when there is none.
func errorName(v jsonval.Value) string {
	var top struct {
		Error struct {
			Name string `json:"name"`
		} `json:"error"`
	}
	if v.IsZero() || jsonlib.Unmarshal(v.Producer(), &top) != nil {
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
// and the evaluator's text; the body step of a collapsed loop prefixes it
// with "could not transform input using an expr: " (JE-23, JE-39).
func transformError(err error, collapsed bool) jsonval.Value {
	text := ""
	var tf *transformFailure
	if errors.As(err, &tf) {
		text = tf.text
	}
	msg := "Error during isolated evaluation of expression `" + text + "`:\n" + evalText(err)
	if collapsed {
		msg = "could not transform input using an expr: " + msg
	}
	return engineError("ExecutionErr", msg)
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
