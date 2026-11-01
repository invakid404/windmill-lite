package bench

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Op is one operation a step's path does with JSON, as a candidate does it.
// Prepare turns the payload into the operation's input outside the timed
// part; Do runs it once and returns how many bytes it produced, or an
// error.
type Op struct {
	Name    string
	Has     func(Cand) bool
	Prepare func(c Cand, payload []byte) (any, error)
	Do      func(c Cand, in any) (int, error)
}

// Ops are the operations, each named after what it stands for:
//
//   - validate: json.Valid (wl's resolver and hook outputs, engine.isObject,
//     engine.FailureValue, engine.elements, runcmd's arguments);
//   - compact: json.Compact, which encoding/json also runs on every
//     json.RawMessage it marshals;
//   - unmarshal-any: decoding into interface{} values;
//   - marshal-any: encoding those values again;
//   - unmarshal-frame: a result frame decoded into its members' raw texts,
//     as harness.Decode does (map[string]json.RawMessage);
//   - members: an object split into its members in order, duplicates kept,
//     as engine.members (storeMembers, ManualFailure, flowInputText),
//     harness.scriptArgs and platform's interpolation walk it;
//   - rawmessage: a host_call payload, a map holding the arguments as a
//     json.RawMessage, marshalled as wl's plugin does for rewrite_args and
//     intercept (on_run_end's trace holds every step's texts the same way);
//   - canonical: decode, sort keys and encode, against jsonval's streaming
//     canonicaliser (whose order is jsonb's, not bytewise) and RFC 8785.
var Ops = []Op{
	{
		Name:    "validate",
		Has:     func(c Cand) bool { return c.Valid != nil },
		Prepare: same,
		Do: func(c Cand, in any) (int, error) {
			if !c.Valid(in.([]byte)) {
				return 0, errors.New("not valid")
			}
			return 0, nil
		},
	},
	{
		Name:    "compact",
		Has:     func(c Cand) bool { return c.Compact != nil },
		Prepare: same,
		Do: func(c Cand, in any) (int, error) {
			out, err := c.Compact(nil, in.([]byte))
			return len(out), err
		},
	},
	{
		Name:    "unmarshal-any",
		Has:     func(c Cand) bool { return c.Unmarshal != nil },
		Prepare: same,
		Do: func(c Cand, in any) (int, error) {
			var v any
			return 0, c.Unmarshal(in.([]byte), &v)
		},
	},
	{
		Name: "marshal-any",
		Has:  func(c Cand) bool { return c.Unmarshal != nil && c.Marshal != nil },
		Prepare: func(c Cand, payload []byte) (any, error) {
			var v any
			err := c.Unmarshal(payload, &v)
			return v, err
		},
		Do: func(c Cand, in any) (int, error) {
			out, err := c.Marshal(in)
			return len(out), err
		},
	},
	{
		Name:    "unmarshal-frame",
		Has:     func(c Cand) bool { return c.RawObject != nil },
		Prepare: func(_ Cand, payload []byte) (any, error) { return Frame(payload), nil },
		Do: func(c Cand, in any) (int, error) {
			frame := in.([]byte)
			m, err := c.RawObject(frame)
			if err != nil {
				return 0, err
			}
			body := m["body"]
			if len(body) != len(frame)-len(frameHead)-len(frameTail) {
				return 0, fmt.Errorf("body of %d bytes", len(body))
			}
			return len(body), nil
		},
	},
	{
		Name:    "members",
		Has:     func(c Cand) bool { return c.Members != nil },
		Prepare: same,
		Do: func(c Cand, in any) (int, error) {
			n := 0
			if !c.Members(in.([]byte), func(_ []byte, _ string, v []byte) { n += len(v) }) {
				return 0, errors.New("not one object")
			}
			return n, nil
		},
	},
	{
		Name:    "rawmessage",
		Has:     func(c Cand) bool { return c.Marshal != nil },
		Prepare: func(_ Cand, payload []byte) (any, error) { return HostCall(payload), nil },
		Do: func(c Cand, in any) (int, error) {
			out, err := c.Marshal(in)
			return len(out), err
		},
	},
	{
		Name:    "canonical",
		Has:     func(c Cand) bool { return c.Canonical != nil },
		Prepare: same,
		Do: func(c Cand, in any) (int, error) {
			out, err := c.Canonical(in.([]byte))
			return len(out), err
		},
	},
}

func same(_ Cand, payload []byte) (any, error) { return payload, nil }

const (
	frameHead = `{"t":"result","id":"inv-000001","outcome":"ok","body":`
	frameTail = `,"settled":true}`
)

// Frame is a result frame's payload carrying body, as the harness writes it
// (ADR 0004): the envelope's fields, the body spliced in raw.
func Frame(body []byte) []byte {
	b := make([]byte, 0, len(frameHead)+len(body)+len(frameTail))
	b = append(b, frameHead...)
	b = append(b, body...)
	return append(b, frameTail...)
}

// HostCall is a rewrite_args host_call payload as wl's plugin builds it
// before json.Marshal: the wrapper's index, the arguments as raw text and
// the site.
func HostCall(args []byte) map[string]any {
	return map[string]any{
		"index": 0,
		"args":  json.RawMessage(args),
		"site":  map[string]any{"path": "f/bench/flow/a", "flow": "f/bench/flow", "module": "a", "script": "f/bench/step"},
	}
}

// OpNamed is the operation called name.
func OpNamed(name string) (Op, bool) {
	for _, op := range Ops {
		if op.Name == name {
			return op, true
		}
	}
	return Op{}, false
}

// CandNamed is the candidate called name.
func CandNamed(name string) (Cand, bool) {
	for _, c := range Cands {
		if c.Name == name {
			return c, true
		}
	}
	return Cand{}, false
}
