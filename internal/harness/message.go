package harness

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// ProtocolVersion is the protocol version this package speaks, sent as
// `proto` in hello and welcome.
const ProtocolVersion = 1

// Message types (the `t` field, ADR 0004). Milestone C1 implemented the
// frames of spawned harnesses; the walking skeleton (W1) added
// `platform`/`platform_reply`, with the fields the facade (PF1) builds on,
// and host mode's `run`/`run_done`/`run_cancel`; milestone C3a added
// `warning`. `host_call`/`host_reply` (F2c) are refused as CodeUnknownType
// until their milestone.
const (
	TypeHello         = "hello"
	TypeWelcome       = "welcome"
	TypeInvoke        = "invoke"
	TypeResult        = "result"
	TypeLate          = "late"
	TypeCancel        = "cancel"
	TypeLog           = "log"
	TypeWarning       = "warning"
	TypePlatform      = "platform"
	TypePlatformReply = "platform_reply"
	TypeRun           = "run"
	TypeRunDone       = "run_done"
	TypeRunCancel     = "run_cancel"
	TypePing          = "ping"
	TypePong          = "pong"
	TypePart          = "part"
	TypeDrain         = "drain"
	TypeBye           = "bye"
)

// Message is one protocol message. Encode writes its canonical payload.
type Message interface {
	Type() string
	appendPayload(b []byte) ([]byte, error)
}

// Encode returns the canonical payload of m: the bytes a frame carries,
// without the length prefix.
func Encode(m Message) ([]byte, error) { return m.appendPayload(nil) }

// Hello is the harness's first frame.
type Hello struct {
	Proto   int
	Harness HarnessInfo
	// Runtime is the runtime's own version (Bun.version for Bun).
	Runtime string
	Caps    Caps
	// Bundle is the digest of the bundle the harness loaded (ADR 0010).
	Bundle string
	// PID is the harness's process id. The engine checks it against the
	// process it spawned, so that the watchdog's SIGKILL reaches the
	// harness itself and not a wrapper that started it (ADR 0004 host mode
	// item 4).
	PID int
}

// HarnessInfo names the harness implementation.
type HarnessInfo struct {
	Kind    string
	Version string
}

// Caps are the optional features a harness offers (ADR 0004).
type Caps struct {
	Fetch  bool
	Native bool
	Fresh  bool
	Host   bool
}

// Welcome is the engine's answer to hello: either Config, accepting the
// harness, or Reject.
type Welcome struct {
	Proto  int
	Engine string
	Bundle string
	Config *WelcomeConfig
	Reject *Reject
}

// WelcomeConfig is what the engine tells an accepted harness.
type WelcomeConfig struct {
	// ModuleState is the process's module-state mode (ADR 0006).
	ModuleState string
	// HeartbeatMS is how often the harness sends a ping on its own, in
	// milliseconds. The engine's watchdog measures silence from the frames
	// it receives, so these pings keep a live harness from looking silent
	// while the engine's own writes are stuck behind a large message.
	HeartbeatMS int
	// MaxMessage is the largest message either side may send, in bytes.
	MaxMessage int64
	// Facade is the platform facade's origin ("http://127.0.0.1:<port>"),
	// or "" when the engine serves no platform calls. The harness turns
	// fetch requests to this origin into platform frames (ADR 0004,
	// per-invocation identity).
	Facade string
}

// Reject says why the engine refused a harness. Code is one of the
// Reject constants.
type Reject struct {
	Code    string
	Message string
}

// Reject codes.
const (
	RejectProto   = "proto_mismatch"
	RejectHarness = "harness_mismatch"
	RejectRuntime = "runtime_mismatch"
	RejectBundle  = "bundle_mismatch"
	RejectPID     = "pid_mismatch"
)

// Invoke asks the harness to run one step.
type Invoke struct {
	ID       string
	Run      string
	Site     string
	Script   string
	Entry    string
	Args     jsonval.Value
	Env      map[string]string
	Boundary string
}

// Result is an invocation's outcome.
type Result struct {
	ID string
	// Outcome is "ok", "error", "escaped" or "empty".
	Outcome string
	// Body is the serialised result (ok) or error object (error); zero
	// for escaped and empty.
	Body jsonval.Value
	// Tail and ExitCode describe an escaped outcome (WR-20); they are
	// written only for one.
	Tail     string
	ExitCode int
	// Settled is false when the outcome was fixed while the entrypoint call
	// goes on (D11, or an attributed process-level error).
	Settled bool
}

// Late is something an invocation produced after its outcome was fixed or
// it was abandoned (D9).
type Late struct {
	ID string
	// Kind is "result", "error" or "log".
	Kind string
	// Body is the late result, the error object, or for "log" an array of
	// lines; it may be zero for "error".
	Body jsonval.Value
	// Settled is true on the one late result or error that says how the
	// entrypoint call ended.
	Settled bool
}

// Cancel abandons one invocation.
type Cancel struct {
	ID string
	// Reason is "timeout", "canceled" or "run_ended".
	Reason string
}

// Log is attributed output of one invocation, batched.
type Log struct {
	ID    string
	Lines []string
}

// Warning is something an invocation in flight is told that fails
// nothing: a process-level error that no invocation's context raised,
// sent once for each invocation in flight when it happened (ADR 0004, the
// shared-process failure model, item 3), what an invocation left
// behind, sent just before the frame that settles it (the D9 fence), or
// what the native boundary handed a native invocation but does not track,
// sent just before that frame too (ADR 0001, D5).
type Warning struct {
	// ID is the invocation in flight.
	ID string
	// Code is WarningUnattributed, WarningLeftBehind or
	// WarningNativeUntracked.
	Code    string
	Message string
	// Left counts what a left_behind warning reports, by kind ("timeout",
	// "interval", "immediate", "fetch", "child"); nil otherwise.
	Left map[string]int
	// Untracked counts what a native_untracked warning reports, by kind (a
	// class name, or "function <name>"); nil otherwise.
	Untracked map[string]int
}

// Warning codes.
const (
	// WarningUnattributed is the code of a warning for a process-level
	// error that the harness could not attribute to any invocation.
	WarningUnattributed = "unattributed_error"
	// WarningLeftBehind is the code of the warning that reports the
	// handles and requests an invocation still had when it settled, which
	// the settle-time fence then revoked (D9).
	WarningLeftBehind = "left_behind"
	// WarningNativeUntracked is the code of the warning that reports what
	// the native boundary handed a native invocation but does not track,
	// sent just before the frame that settles it, and of the one for what
	// it handed out in no invocation's context, sent to each invocation in
	// flight (ADR 0001, D5).
	WarningNativeUntracked = "native_untracked"
)

// PlatformRequest is a fetch to the platform facade that the harness
// intercepted, attributed to the invocation whose context made it
// (ADR 0004, ADR 0007). Body holds the request's bytes, which travel as
// padded standard base64; it is nil for a request without a body.
type PlatformRequest struct {
	ID string
	// Inv is the invocation the request is attributed to.
	Inv     string
	Method  string
	URL     string
	Headers map[string]string
	Body    []byte
}

// PlatformReply answers a PlatformRequest with the same id. Status is a
// final HTTP status, 200 to 599; Body is as in PlatformRequest.
type PlatformReply struct {
	ID      string
	Status  int
	Headers map[string]string
	Body    []byte
}

// Run kinds of a run frame.
const (
	RunFlow   = "flow"
	RunScript = "script"
)

// RunRequest is a run that a host-mode harness submits (ADR 0004 host
// mode, ADR 0009): the target by kind and path and its arguments. Its id is
// the run's id in the engine, so invocations of the run carry it as `run`
// and the host can re-enter the scope it captured at submission (H11).
// Intercept, if not zero, is a JSON object of module ids to the results
// that stand in for those modules: the static, no-code form of H1
// (ADR 0008).
type RunRequest struct {
	ID        string
	Kind      string
	Path      string
	Args      jsonval.Value
	Intercept jsonval.Value
}

// RunDone is a run's outcome, answering a RunRequest with the same id.
// Status is a conformance.RunStatus value; Result is the run's final result
// in stored form, zero when the run has none (canceled, latched, halted).
type RunDone struct {
	ID     string
	Status string
	Result jsonval.Value
}

// RunCancel cancels a run the harness submitted.
type RunCancel struct {
	ID string
}

// Health is what a ping or pong reports about its sender.
type Health struct {
	InFlight  int
	Abandoned int
	// Errors counts process-level errors that no invocation owned.
	Errors int
	// LagMS is the event loop's lag, in whole milliseconds.
	LagMS int64
	// RSS is the resident set size in bytes.
	RSS int64
}

// Ping is a health request. A harness also sends one on its own every
// heartbeat, carrying its Health; the engine's pings carry none.
type Ping struct {
	ID     string
	Health *Health
}

// Pong answers a ping with the same id.
type Pong struct {
	ID     string
	Health *Health
}

// Part is one piece of a message whose payload is larger than the maximum
// frame (see parts.go). ID names the part stream, unique per sender among
// its streams; Chunk is the piece's bytes.
type Part struct {
	ID    string
	Seq   int
	Last  bool
	Chunk []byte
}

// Drain asks the harness to say bye and exit.
type Drain struct{}

// Bye is the harness's answer to drain, its last frame.
type Bye struct{}

func (Hello) Type() string           { return TypeHello }
func (Welcome) Type() string         { return TypeWelcome }
func (Invoke) Type() string          { return TypeInvoke }
func (Result) Type() string          { return TypeResult }
func (Late) Type() string            { return TypeLate }
func (Cancel) Type() string          { return TypeCancel }
func (Log) Type() string             { return TypeLog }
func (Warning) Type() string         { return TypeWarning }
func (PlatformRequest) Type() string { return TypePlatform }
func (PlatformReply) Type() string   { return TypePlatformReply }
func (RunRequest) Type() string      { return TypeRun }
func (RunDone) Type() string         { return TypeRunDone }
func (RunCancel) Type() string       { return TypeRunCancel }
func (Ping) Type() string            { return TypePing }
func (Pong) Type() string            { return TypePong }
func (Part) Type() string            { return TypePart }
func (Drain) Type() string           { return TypeDrain }
func (Bye) Type() string             { return TypeBye }

func (m Hello) appendPayload(b []byte) ([]byte, error) {
	e := newEnvelope(b, TypeHello)
	e.int("proto", int64(m.Proto))
	e.object("harness", func(e *envelope) {
		e.str("kind", m.Harness.Kind)
		e.str("version", m.Harness.Version)
	})
	e.str("runtime", m.Runtime)
	e.object("caps", func(e *envelope) {
		e.bool("fetch", m.Caps.Fetch)
		e.bool("native", m.Caps.Native)
		e.bool("fresh", m.Caps.Fresh)
		e.bool("host", m.Caps.Host)
	})
	e.str("bundle", m.Bundle)
	e.int("pid", int64(m.PID))
	return e.end()
}

func (m Welcome) appendPayload(b []byte) ([]byte, error) {
	if (m.Config == nil) == (m.Reject == nil) {
		return b, protoErr(CodeBadField, "welcome needs exactly one of config and reject")
	}
	e := newEnvelope(b, TypeWelcome)
	e.int("proto", int64(m.Proto))
	e.str("engine", m.Engine)
	e.str("bundle", m.Bundle)
	if c := m.Config; c != nil {
		e.object("config", func(e *envelope) {
			e.str("module_state", c.ModuleState)
			e.int("heartbeat_ms", int64(c.HeartbeatMS))
			e.int("max_message", c.MaxMessage)
			if c.Facade != "" {
				e.str("facade", c.Facade)
			}
		})
	} else {
		r := m.Reject
		e.object("reject", func(e *envelope) {
			e.str("code", r.Code)
			e.str("message", r.Message)
		})
	}
	return e.end()
}

func (m Invoke) appendPayload(b []byte) ([]byte, error) {
	if m.Args.IsZero() {
		return b, protoErr(CodeMissingField, "invoke without args")
	}
	e := newEnvelope(b, TypeInvoke)
	e.str("id", m.ID)
	e.str("run", m.Run)
	e.str("site", m.Site)
	e.str("script", m.Script)
	e.str("entry", m.Entry)
	e.raw("args", m.Args.Producer())
	e.strMap("env", m.Env)
	e.str("boundary", m.Boundary)
	return e.end()
}

func (m Result) appendPayload(b []byte) ([]byte, error) {
	e := newEnvelope(b, TypeResult)
	e.str("id", m.ID)
	e.str("outcome", m.Outcome)
	switch m.Outcome {
	case OutcomeOK, OutcomeError:
		if m.Body.IsZero() {
			return b, protoErr(CodeMissingField, "%s result without body", m.Outcome)
		}
		e.raw("body", m.Body.Producer())
	case OutcomeEscaped:
		e.str("tail", m.Tail)
		e.int("exit_code", int64(m.ExitCode))
	case OutcomeEmpty:
	default:
		return b, protoErr(CodeBadField, "result outcome %q", m.Outcome)
	}
	e.bool("settled", m.Settled)
	return e.end()
}

func (m Late) appendPayload(b []byte) ([]byte, error) {
	e := newEnvelope(b, TypeLate)
	e.str("id", m.ID)
	e.str("kind", m.Kind)
	if !m.Body.IsZero() {
		e.raw("body", m.Body.Producer())
	}
	e.bool("settled", m.Settled)
	return e.end()
}

func (m Cancel) appendPayload(b []byte) ([]byte, error) {
	e := newEnvelope(b, TypeCancel)
	e.str("id", m.ID)
	e.str("reason", m.Reason)
	return e.end()
}

func (m Log) appendPayload(b []byte) ([]byte, error) {
	e := newEnvelope(b, TypeLog)
	e.str("id", m.ID)
	e.strings("lines", m.Lines)
	return e.end()
}

func (m Warning) appendPayload(b []byte) ([]byte, error) {
	e := newEnvelope(b, TypeWarning)
	e.str("id", m.ID)
	e.str("code", m.Code)
	e.str("message", m.Message)
	if m.Left != nil {
		e.intMap("left", m.Left)
	}
	if m.Untracked != nil {
		e.intMap("untracked", m.Untracked)
	}
	return e.end()
}

func (m PlatformRequest) appendPayload(b []byte) ([]byte, error) {
	e := newEnvelope(b, TypePlatform)
	e.str("id", m.ID)
	e.str("inv", m.Inv)
	e.str("method", m.Method)
	e.str("url", m.URL)
	e.strMap("headers", m.Headers)
	e.bytes("body", m.Body)
	return e.end()
}

func (m PlatformReply) appendPayload(b []byte) ([]byte, error) {
	if !finalStatus(m.Status) {
		return b, protoErr(CodeBadField, "platform_reply status %d", m.Status)
	}
	e := newEnvelope(b, TypePlatformReply)
	e.str("id", m.ID)
	e.int("status", int64(m.Status))
	e.strMap("headers", m.Headers)
	e.bytes("body", m.Body)
	return e.end()
}

// finalStatus reports whether a reply status is a final HTTP status, which
// a fetch Response can carry.
func finalStatus(s int) bool { return s >= 200 && s <= 599 }

func (m RunRequest) appendPayload(b []byte) ([]byte, error) {
	if m.Args.IsZero() {
		return b, protoErr(CodeMissingField, "run without args")
	}
	e := newEnvelope(b, TypeRun)
	e.str("id", m.ID)
	e.str("kind", m.Kind)
	e.str("path", m.Path)
	e.raw("args", m.Args.Producer())
	if !m.Intercept.IsZero() {
		e.raw("intercept", m.Intercept.Producer())
	}
	return e.end()
}

func (m RunDone) appendPayload(b []byte) ([]byte, error) {
	e := newEnvelope(b, TypeRunDone)
	e.str("id", m.ID)
	e.str("status", m.Status)
	if !m.Result.IsZero() {
		e.raw("result", m.Result.Producer())
	}
	return e.end()
}

func (m RunCancel) appendPayload(b []byte) ([]byte, error) {
	e := newEnvelope(b, TypeRunCancel)
	e.str("id", m.ID)
	return e.end()
}

func appendHealth(e *envelope, h *Health) {
	if h == nil {
		return
	}
	e.int("in_flight", int64(h.InFlight))
	e.int("abandoned", int64(h.Abandoned))
	e.int("errors", int64(h.Errors))
	e.int("lag_ms", h.LagMS)
	e.int("rss", h.RSS)
}

func (m Ping) appendPayload(b []byte) ([]byte, error) {
	e := newEnvelope(b, TypePing)
	e.str("id", m.ID)
	appendHealth(e, m.Health)
	return e.end()
}

func (m Pong) appendPayload(b []byte) ([]byte, error) {
	e := newEnvelope(b, TypePong)
	e.str("id", m.ID)
	appendHealth(e, m.Health)
	return e.end()
}

func (m Part) appendPayload(b []byte) ([]byte, error) {
	if len(m.Chunk) == 0 {
		return b, protoErr(CodeBadChunk, "empty part chunk")
	}
	e := newEnvelope(b, TypePart)
	e.str("id", m.ID)
	e.int("seq", int64(m.Seq))
	e.bool("last", m.Last)
	e.key("chunk")
	e.b = append(e.b, '"')
	e.b = base64.StdEncoding.AppendEncode(e.b, m.Chunk)
	e.b = append(e.b, '"')
	return e.end()
}

func (Drain) appendPayload(b []byte) ([]byte, error) { return newEnvelope(b, TypeDrain).end() }
func (Bye) appendPayload(b []byte) ([]byte, error)   { return newEnvelope(b, TypeBye).end() }

// Outcome values of a result frame (engine.OutcomeKind spells the same).
const (
	OutcomeOK      = "ok"
	OutcomeError   = "error"
	OutcomeEscaped = "escaped"
	OutcomeEmpty   = "empty"
)

// Decode parses one frame payload into its message. Field order and
// insignificant whitespace in the envelope do not matter; raw values (args,
// body) keep their bytes exactly. Keys match exactly, as JavaScript reads
// them, and only the fields of the message's own type are read; others are
// ignored, so a later protocol revision can add optional ones.
//
// Decoding follows the same rules as the Bun harness's codec: a field that
// is null counts as absent, except a raw value, which may be JSON null; an
// integer field holds a JSON number with an integral value within
// ±(2^53-1), whatever its spelling (1, 1.0 and 1e0 are one integer).
func Decode(payload []byte) (Message, error) {
	if !utf8.Valid(payload) {
		return nil, protoErr(CodeInvalidUTF8, "payload is not valid UTF-8")
	}
	trimmed := bytes.TrimLeft(payload, " \t\r\n")
	if len(trimmed) == 0 || trimmed[0] != '{' {
		if json.Valid(payload) {
			return nil, protoErr(CodeNotObject, "payload is not a JSON object")
		}
		return nil, protoErr(CodeInvalidJSON, "payload is not JSON")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return nil, protoErr(CodeInvalidJSON, "%v", err)
	}
	var t string
	if raw, ok := fields["t"]; !ok || json.Unmarshal(raw, &t) != nil || raw[0] != '"' {
		return nil, protoErr(CodeMissingType, "object has no string field t")
	}
	var derr error
	d := &decoder{t: t, f: fields, err: &derr}
	var m Message
	switch t {
	case TypeHello:
		h := Hello{Proto: d.int("proto")}
		if hd := d.object("harness"); hd != nil {
			h.Harness = HarnessInfo{Kind: hd.str("kind"), Version: hd.str("version")}
		}
		h.Runtime = d.str("runtime")
		if cd := d.object("caps"); cd != nil {
			h.Caps = Caps{Fetch: cd.bool("fetch"), Native: cd.bool("native"), Fresh: cd.bool("fresh"), Host: cd.bool("host")}
		}
		h.Bundle = d.str("bundle")
		h.PID = d.int("pid")
		m = h
	case TypeWelcome:
		wm := Welcome{Proto: d.int("proto"), Engine: d.str("engine"), Bundle: d.str("bundle")}
		config, reject := d.present("config"), d.present("reject")
		switch {
		case config && reject:
			d.fail(CodeBadField, "welcome has both config and reject")
		case config:
			if cd := d.object("config"); cd != nil {
				wm.Config = &WelcomeConfig{ModuleState: cd.str("module_state"), HeartbeatMS: cd.int("heartbeat_ms"), MaxMessage: cd.int64("max_message")}
				if cd.present("facade") {
					wm.Config.Facade = cd.str("facade")
				}
			}
		case reject:
			if rd := d.object("reject"); rd != nil {
				wm.Reject = &Reject{Code: rd.str("code"), Message: rd.str("message")}
			}
		default:
			d.fail(CodeMissingField, "welcome has neither config nor reject")
		}
		m = wm
	case TypeInvoke:
		m = Invoke{
			ID:       d.str("id"),
			Run:      d.str("run"),
			Site:     d.str("site"),
			Script:   d.str("script"),
			Entry:    d.str("entry"),
			Args:     d.raw("args"),
			Env:      d.env("env"),
			Boundary: d.str("boundary"),
		}
	case TypeResult:
		r := Result{ID: d.str("id"), Outcome: d.str("outcome"), Settled: d.bool("settled")}
		switch r.Outcome {
		case OutcomeOK, OutcomeError:
			r.Body = d.raw("body")
		case OutcomeEscaped:
			r.Tail = d.str("tail")
			r.ExitCode = d.int("exit_code")
		case OutcomeEmpty:
		default:
			d.fail(CodeBadField, "result outcome %q", r.Outcome)
		}
		m = r
	case TypeLate:
		l := Late{ID: d.str("id"), Kind: d.str("kind"), Settled: d.bool("settled")}
		switch l.Kind {
		case LateResult, LateLog:
			l.Body = d.raw("body")
		case LateError:
			if d.present("body") {
				l.Body = d.raw("body")
			}
		default:
			d.fail(CodeBadField, "late kind %q", l.Kind)
		}
		if l.Kind == LateLog && l.Settled {
			d.fail(CodeBadField, "a late log cannot settle an invocation")
		}
		m = l
	case TypeCancel:
		c := Cancel{ID: d.str("id"), Reason: d.str("reason")}
		switch c.Reason {
		case "timeout", "canceled", "run_ended":
		default:
			d.fail(CodeBadField, "cancel reason %q", c.Reason)
		}
		m = c
	case TypeLog:
		m = Log{ID: d.str("id"), Lines: d.strings("lines")}
	case TypeWarning:
		w := Warning{ID: d.str("id"), Code: d.str("code"), Message: d.str("message")}
		if d.present("left") {
			w.Left = d.intMap("left")
		}
		if d.present("untracked") {
			w.Untracked = d.intMap("untracked")
		}
		m = w
	case TypePlatform:
		m = PlatformRequest{
			ID:      d.str("id"),
			Inv:     d.str("inv"),
			Method:  d.str("method"),
			URL:     d.str("url"),
			Headers: d.env("headers"),
			Body:    d.optBytes("body"),
		}
	case TypePlatformReply:
		r := PlatformReply{ID: d.str("id"), Status: d.int("status"), Headers: d.env("headers"), Body: d.optBytes("body")}
		if derr == nil && !finalStatus(r.Status) {
			d.fail(CodeBadField, "platform_reply status %d", r.Status)
		}
		m = r
	case TypeRun:
		r := RunRequest{ID: d.str("id"), Kind: d.str("kind"), Path: d.str("path"), Args: d.raw("args")}
		switch r.Kind {
		case RunFlow, RunScript:
		default:
			d.fail(CodeBadField, "run kind %q", r.Kind)
		}
		if d.present("intercept") {
			r.Intercept = d.raw("intercept")
		}
		m = r
	case TypeRunDone:
		r := RunDone{ID: d.str("id"), Status: d.str("status")}
		if !slices.Contains(runStatuses, r.Status) && derr == nil {
			d.fail(CodeBadField, "run_done status %q", r.Status)
		}
		// A result of JSON null is a value: a flow may return null.
		if _, ok := fields["result"]; ok {
			r.Result = d.raw("result")
		}
		m = r
	case TypeRunCancel:
		m = RunCancel{ID: d.str("id")}
	case TypePing:
		m = Ping{ID: d.str("id"), Health: d.health()}
	case TypePong:
		m = Pong{ID: d.str("id"), Health: d.health()}
	case TypePart:
		p := Part{ID: d.str("id"), Seq: d.int("seq"), Last: d.bool("last")}
		if c := d.str("chunk"); derr == nil {
			// The decoder skips line breaks, which canonical base64 has
			// none of.
			chunk, err := base64.StdEncoding.Strict().DecodeString(c)
			if err != nil || len(chunk) == 0 || strings.ContainsAny(c, "\r\n") {
				d.fail(CodeBadChunk, "part chunk is not non-empty padded base64")
			}
			p.Chunk = chunk
		}
		if p.Seq < 0 {
			d.fail(CodeBadField, "negative part seq")
		}
		m = p
	case TypeDrain:
		m = Drain{}
	case TypeBye:
		m = Bye{}
	default:
		return nil, protoErr(CodeUnknownType, "message type %q", t)
	}
	if derr != nil {
		return nil, derr
	}
	return m, nil
}

// runStatuses are the statuses of a run_done frame: the values of
// conformance.RunStatus.
var runStatuses = []string{"success", "failure", "canceled", "skipped", "latched", "halted"}

// Late kinds of a late frame (engine.LateKind spells the same).
const (
	LateResult = "result"
	LateError  = "error"
	LateLog    = "log"
)

// decoder reads the fields of one object. It keeps the first error.
type decoder struct {
	t      string // the message type, for error messages
	prefix string // "config." and the like for a nested object
	f      map[string]json.RawMessage
	err    *error // shared with nested decoders
}

func (d *decoder) fail(code, format string, args ...any) {
	if *d.err == nil {
		*d.err = protoErr(code, format, args...)
	}
}

// present reports whether a field is there and not null.
func (d *decoder) present(name string) bool {
	raw, ok := d.f[name]
	return ok && string(raw) != "null"
}

// field returns a required field's text, or nil after recording that it
// is missing. A null field counts as missing.
func (d *decoder) field(name string) json.RawMessage {
	if !d.present(name) {
		d.fail(CodeMissingField, "%s %s%s", d.t, d.prefix, name)
		return nil
	}
	return d.f[name]
}

func (d *decoder) badType(name string) {
	d.fail(CodeBadField, "field %q has the wrong type", d.prefix+name)
}

// optBytes returns an optional field of padded standard base64 bytes, nil
// when it is absent or null and non-nil (maybe empty) when present.
func (d *decoder) optBytes(name string) []byte {
	if !d.present(name) {
		return nil
	}
	s := d.str(name)
	if *d.err != nil {
		return nil
	}
	b, err := base64.StdEncoding.Strict().DecodeString(s)
	if err != nil || strings.ContainsAny(s, "\r\n") {
		d.fail(CodeBadChunk, "%s %s is not padded base64", d.t, name)
		return nil
	}
	return append([]byte{}, b...)
}

func (d *decoder) str(name string) string {
	raw := d.field(name)
	var s string
	if raw != nil && (raw[0] != '"' || json.Unmarshal(raw, &s) != nil) {
		d.badType(name)
	}
	return s
}

// maxSafeInteger is 2^53-1, the largest integer every harness's JSON reader
// represents exactly.
const maxSafeInteger = 1<<53 - 1

func (d *decoder) int64(name string) int64 {
	raw := d.field(name)
	if raw == nil {
		return 0
	}
	if c := raw[0]; c != '-' && (c < '0' || c > '9') {
		d.badType(name)
		return 0
	}
	f, err := strconv.ParseFloat(string(raw), 64)
	if err != nil || f != math.Trunc(f) || math.Abs(f) > maxSafeInteger {
		d.badType(name)
		return 0
	}
	return int64(f)
}

func (d *decoder) int(name string) int { return int(d.int64(name)) }

func (d *decoder) bool(name string) bool {
	raw := d.field(name)
	switch string(raw) {
	case "true":
		return true
	case "false", "":
	default:
		d.badType(name)
	}
	return false
}

// raw returns a raw value: any JSON value, null included, byte for byte.
func (d *decoder) raw(name string) jsonval.Value {
	raw, ok := d.f[name]
	if !ok {
		d.fail(CodeMissingField, "%s %s%s", d.t, d.prefix, name)
		return jsonval.Value{}
	}
	return jsonval.FromProducer(raw)
}

// object returns a decoder for a nested object, or nil.
func (d *decoder) object(name string) *decoder {
	raw := d.field(name)
	if raw == nil {
		return nil
	}
	var f map[string]json.RawMessage
	if raw[0] != '{' || json.Unmarshal(raw, &f) != nil {
		d.badType(name)
		return nil
	}
	return &decoder{t: d.t, prefix: d.prefix + name + ".", f: f, err: d.err}
}

// strings decodes an array of strings; a null element is a wrong type.
func (d *decoder) strings(name string) []string {
	raw := d.field(name)
	if raw == nil {
		return nil
	}
	var elems []json.RawMessage
	if raw[0] != '[' || json.Unmarshal(raw, &elems) != nil {
		d.badType(name)
		return nil
	}
	out := make([]string, len(elems))
	for i, e := range elems {
		if e[0] != '"' || json.Unmarshal(e, &out[i]) != nil {
			d.badType(name)
			return nil
		}
	}
	return out
}

// env decodes an object of strings (env, headers); a null value is a wrong
// type.
func (d *decoder) env(name string) map[string]string {
	raw := d.field(name)
	if raw == nil {
		return nil
	}
	var vals map[string]json.RawMessage
	if raw[0] != '{' || json.Unmarshal(raw, &vals) != nil {
		d.badType(name)
		return nil
	}
	out := make(map[string]string, len(vals))
	for k, v := range vals {
		var s string
		if v[0] != '"' || json.Unmarshal(v, &s) != nil {
			d.badType(name)
			return nil
		}
		out[k] = s
	}
	return out
}

// intMap decodes an object of integers whose keys are data.
func (d *decoder) intMap(name string) map[string]int {
	o := d.object(name)
	if o == nil {
		return nil
	}
	out := make(map[string]int, len(o.f))
	for k := range o.f {
		if !o.present(k) {
			d.badType(name)
			return nil
		}
		out[k] = o.int(k)
	}
	return out
}

// health decodes the optional health group: all five fields or none.
func (d *decoder) health() *Health {
	n := 0
	for _, k := range []string{"in_flight", "abandoned", "errors", "lag_ms", "rss"} {
		if d.present(k) {
			n++
		}
	}
	if n == 0 {
		return nil
	}
	return &Health{
		InFlight:  d.int("in_flight"),
		Abandoned: d.int("abandoned"),
		Errors:    d.int("errors"),
		LagMS:     d.int64("lag_ms"),
		RSS:       d.int64("rss"),
	}
}
