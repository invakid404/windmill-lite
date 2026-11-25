// Package host is the engine's side of its host adapters (ADR 0009): the
// windmill_lite.v1.Runner gRPC service over one wl runtime, which
// `windmill-lite plugin` serves to a go-plugin host.
package host

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"slices"
	"sync"
	"time"

	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/wlext"
	"github.com/invakid404/windmill-lite/wl"
	"github.com/invakid404/windmill-lite/wl/plugin"
)

// Config is what a Server serves.
type Config struct {
	// Runtime runs the requests. The Server never closes it.
	Runtime *wl.Runtime
	// Labels are the configuration's labels, which GetBuildInfo echoes.
	Labels map[string]string
	// ConfigDigest is the SHA-256 of the configuration file, in lowercase
	// hexadecimal, which GetBuildInfo returns.
	ConfigDigest string
	// Now is the clock that stamps the receipt of a request, from which
	// its deadlines count; nil means time.Now. Tests set it.
	Now func() time.Time
	// Broker returns go-plugin's broker of the connection, on which the
	// host serves the runs' hooks (RunRequest.hooks). go-plugin hands it
	// over when it sets its gRPC server up, before any call reaches the
	// Server. Nil, or a nil broker, refuses the hooks.
	Broker func() *goplugin.GRPCBroker
	// PluginTimeout bounds the release call of a run's hooks; zero means
	// wl.DefaultPluginTimeout.
	PluginTimeout time.Duration
	// MaxMessageBytes is the largest message of a call of a run's hooks
	// that the engine sends or receives; zero means
	// plugin.DefaultMaxMessageBytes.
	MaxMessageBytes int
	// TokenBound is how many tokens that GetRun answered gone for the
	// Server keeps before it calls OnTokenBound; zero means
	// DefaultTokenBound.
	TokenBound int
	// OnTokenBound is called once, when the Server keeps TokenBound gone
	// tokens; the engine then drains.
	OnTokenBound func()
}

// Server serves windmill_lite.v1.Runner over a wl runtime. Its methods are
// safe for concurrent use.
type Server struct {
	plugin.UnimplementedRunnerServer

	rt   *wl.Runtime
	info *plugin.BuildInfo
	now  func() time.Time

	// hooks serves the runs' hooks on go-plugin's broker.
	hooks *brokerHooks

	// mu guards runs: the cancellation of each run in flight, by its id,
	// which Cancel finds.
	mu   sync.Mutex
	runs map[string]context.CancelCauseFunc
}

// New returns the Server of cfg.
func New(cfg Config) *Server {
	bi := cfg.Runtime.BuildInfo()
	labels := make(map[string]string, len(cfg.Labels))
	for k, v := range cfg.Labels {
		labels[k] = v
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	timeout, bound := cfg.PluginTimeout, cfg.TokenBound
	if timeout <= 0 {
		timeout = wl.DefaultPluginTimeout
	}
	if bound <= 0 {
		bound = DefaultTokenBound
	}
	limit := cfg.MaxMessageBytes
	if limit <= 0 {
		limit = plugin.DefaultMaxMessageBytes
	}
	return &Server{
		rt:  cfg.Runtime,
		now: now,
		hooks: &brokerHooks{broker: cfg.Broker, timeout: timeout, limit: limit, bound: bound, onBound: cfg.OnTokenBound,
			conns: map[uint32]*brokerConn{}, held: map[string]heldToken{}, gone: map[string]struct{}{}},
		info: &plugin.BuildInfo{
			BundleDigest:         bi.BundleDigest,
			EngineVersion:        bi.EngineVersion,
			BunVersion:           bi.BunVersion,
			EsbuildVersion:       bi.EsbuildVersion,
			HarnessRuntimes:      bi.HarnessRuntimes,
			ModuleState:          bi.ModuleState,
			SharedCode:           bi.SharedCode,
			Contract:             bi.Contract,
			Deviations:           bi.Deviations,
			NonconformantRuntime: bi.NonconformantRuntime,
			ProtocolVersion:      plugin.ProtocolVersion,
			Labels:               labels,
			ConfigDigest:         cfg.ConfigDigest,
		},
		runs: map[string]context.CancelCauseFunc{},
	}
}

// GetBuildInfo returns the build identity of the engine, its bundle and
// its configuration.
func (s *Server) GetBuildInfo(context.Context, *plugin.GetBuildInfoRequest) (*plugin.BuildInfo, error) {
	return s.info, nil
}

// Run runs a flow or a root script to its end. A run that starts answers a
// RunResult, whatever its outcome; a run that does not start answers a
// gRPC error with the RUN_NOT_STARTED detail (ADR 0009, the Runner
// service).
func (s *Server) Run(ctx context.Context, req *plugin.RunRequest) (*plugin.RunResult, error) {
	// Both deadlines count from the receipt of the request.
	receipt := s.now()
	r, err := mapRequest(req, receipt)
	if err != nil {
		return nil, err
	}
	id := req.GetId()
	if id == "" {
		id = newUUID()
	}
	r.opts.ID = id
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	if !s.register(id, cancel) {
		return nil, refusal(codes.AlreadyExists, fmt.Sprintf("a run with the id %s is in flight", id))
	}
	defer s.unregister(id)
	if r.deadline != nil {
		var stop context.CancelFunc
		ctx, stop = context.WithDeadline(ctx, *r.deadline)
		defer stop()
	}
	if r.intercept != nil {
		ctx = wlext.WithStaticIntercepts(ctx, r.intercept)
	}
	// The id of a run that a step dispatched refuses the run too, from
	// before the dispatch returned it until that run ends.
	ctx = wlext.WithExclusiveID(ctx)
	forget := func() {}
	if h := req.GetHooks(); h != nil {
		// The run's hooks on the host's broker connection: the engine
		// holds the token until it has sent the run's release call.
		if ctx, forget, err = s.hooks.admit(ctx, id, h); err != nil {
			return nil, err
		}
	}
	res, err := s.rt.Run(ctx, req.GetPath(), req.GetArgs(), r.opts)
	if err != nil {
		// A run that did not start never settles: the host drops its
		// registration at this refusal.
		forget()
		return nil, runRefusal(err)
	}
	return resultOf(res, r.trace)
}

// GetRun says whether the engine still holds the run of a hooks
// registration: held from the run's admission until the engine has sent
// its release call, and gone otherwise, which the engine then remembers
// for the life of the process, refusing a later run with that token.
func (s *Server) GetRun(_ context.Context, req *plugin.GetRunRequest) (*plugin.GetRunResponse, error) {
	state, err := s.hooks.lookup(req.GetId(), req.GetToken())
	if err != nil {
		return nil, err
	}
	return &plugin.GetRunResponse{State: state}, nil
}

// Cancel cancels a run in flight by its id, with the reason and the
// canceler that the run's Canceled error names. An id of no run in flight
// answers NOT_FOUND.
func (s *Server) Cancel(_ context.Context, req *plugin.CancelRequest) (*plugin.CancelResponse, error) {
	s.mu.Lock()
	cancel := s.runs[req.GetId()]
	s.mu.Unlock()
	if cancel == nil {
		return nil, status.Errorf(codes.NotFound, "no run with the id %q is in flight", req.GetId())
	}
	cancel(&wl.Cancellation{Reason: req.GetReason(), Canceler: req.GetCanceler()})
	return &plugin.CancelResponse{}, nil
}

// register records the cancellation of the run id, unless a run with that
// id is in flight.
func (s *Server) register(id string, cancel context.CancelCauseFunc) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runs[id] != nil {
		return false
	}
	s.runs[id] = cancel
	return true
}

func (s *Server) unregister(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.runs, id)
}

// request is a RunRequest as wl takes it.
type request struct {
	opts wl.RunOptions
	// deadline is the run's hard deadline, counted from the receipt; nil
	// for none.
	deadline *time.Time
	// intercept is the flow run's static interceptions, nil for none.
	intercept map[string]json.RawMessage
	// trace is the trace level of the result: traceNone, traceSummary or
	// traceFull.
	trace string
}

// The trace levels of a RunRequest.
const (
	traceNone    = "none"
	traceSummary = "summary"
	traceFull    = "full"
)

// flowOnlyFields are the fields of RunRequest that only a flow run takes,
// each with whether a request sets it. A root script's run takes id,
// kind, path, args, deadline_ms, identity, run_data, pin and the trace
// level none.
var flowOnlyFields = []struct {
	name string
	set  func(*plugin.RunRequest) bool
}{
	{"pre_write_deadline_ms", func(r *plugin.RunRequest) bool { return r.PreWriteDeadlineMs != nil }},
	{"write_capable", func(r *plugin.RunRequest) bool { return len(r.GetWriteCapable()) > 0 }},
	{"halt_before", func(r *plugin.RunRequest) bool { return len(r.GetHaltBefore()) > 0 }},
	{"halt_after", func(r *plugin.RunRequest) bool { return len(r.GetHaltAfter()) > 0 }},
	{"fatal_error_names", func(r *plugin.RunRequest) bool { return len(r.GetFatalErrorNames()) > 0 }},
	{"intercept", func(r *plugin.RunRequest) bool { return len(r.GetIntercept()) > 0 }},
	{"clock", func(r *plugin.RunRequest) bool { return r.GetClock() != "" }},
	{"resources", func(r *plugin.RunRequest) bool { return len(r.GetResources()) > 0 }},
	{"variables", func(r *plugin.RunRequest) bool { return len(r.GetVariables()) > 0 }},
	{"hooks", func(r *plugin.RunRequest) bool { return r.GetHooks() != nil }},
	{"limits", func(r *plugin.RunRequest) bool { return r.GetLimits() != nil }},
	{"trace", func(r *plugin.RunRequest) bool { return r.GetTrace() != "" && r.GetTrace() != traceNone }},
}

// mapRequest maps req to wl's run options, or refuses it with
// INVALID_ARGUMENT, naming the field: a field that this engine does not
// know or does not serve yet, a field that a script run does not take, or
// a value that is not well formed. The pre-write deadline counts from
// receipt.
func mapRequest(req *plugin.RunRequest, receipt time.Time) (*request, error) {
	if req == nil {
		req = &plugin.RunRequest{}
	}
	if u := req.ProtoReflect().GetUnknown(); len(u) > 0 {
		return nil, invalid("", fmt.Sprintf("the request sets fields that this engine does not know (%s)", unknownFields(u)))
	}
	r := &request{trace: traceNone}
	switch req.GetKind() {
	case "", wl.KindFlow:
		r.opts.Kind = wl.KindFlow
	case wl.KindScript:
		r.opts.Kind = wl.KindScript
		for _, f := range flowOnlyFields {
			if f.set(req) {
				return nil, invalid(f.name, "a root script's run does not take it")
			}
		}
	default:
		return nil, invalid("kind", fmt.Sprintf("%q is neither %s nor %s", req.GetKind(), wl.KindFlow, wl.KindScript))
	}
	if req.DeadlineMs != nil {
		d := receipt.Add(budget(req.GetDeadlineMs()))
		r.deadline = &d
	}
	if err := nestedUnknown(req); err != nil {
		return nil, err
	}
	if id := req.GetIdentity(); id != nil {
		r.opts.Identity = wl.Identity{
			Workspace: id.GetWorkspace(), Username: id.GetUsername(), Email: id.GetEmail(),
			PermissionedAs: id.GetPermissionedAs(), BaseURL: id.GetBaseUrl(), WorkerGroup: id.GetWorkerGroup(),
		}
	}
	// Runtime.Run refuses run data that is not JSON, and a pin that the
	// runtime cannot keep: a fresh runtime's.
	r.opts.Data = json.RawMessage(req.GetRunData())
	r.opts.Pin = req.GetPin()
	if r.opts.Kind == wl.KindScript {
		return r, nil
	}
	if l := req.GetLimits(); l != nil {
		limits, err := runLimits(l)
		if err != nil {
			return nil, err
		}
		r.opts.Limits = limits
	}
	if req.PreWriteDeadlineMs != nil {
		r.opts.PreWriteDeadline = receipt.Add(budget(req.GetPreWriteDeadlineMs()))
	}
	var h wl.Hooks
	for _, f := range []struct {
		name string
		raw  []byte
		sels *[]wl.Selector
	}{
		{"write_capable", req.GetWriteCapable(), &h.WriteCapable},
		{"halt_before", req.GetHaltBefore(), &h.HaltBefore},
		{"halt_after", req.GetHaltAfter(), &h.HaltAfter},
	} {
		if len(f.raw) == 0 {
			continue
		}
		if err := decodeSelectors(f.raw, f.sels); err != nil {
			return nil, invalid(f.name, err.Error())
		}
	}
	h.FatalErrorNames = req.GetFatalErrorNames()
	if len(h.WriteCapable) > 0 || len(h.HaltBefore) > 0 || len(h.HaltAfter) > 0 || len(h.FatalErrorNames) > 0 {
		r.opts.Hooks = &h
	}
	if raw := req.GetIntercept(); len(raw) > 0 {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil || m == nil {
			return nil, invalid("intercept", "not a JSON object of results by module id")
		}
		r.intercept = m
	}
	switch req.GetClock() {
	case "", "real":
	case "record":
		r.opts.Clock = &wl.RecordClock{}
	default:
		return nil, invalid("clock", fmt.Sprintf("%q is neither real nor record", req.GetClock()))
	}
	if len(req.GetResources()) > 0 || len(req.GetVariables()) > 0 {
		p := &wl.RunPlatform{}
		if rs := req.GetResources(); len(rs) > 0 {
			p.Resources = make(map[string]wl.Resource, len(rs))
			for path, res := range rs {
				if !json.Valid(res.GetValue()) {
					return nil, invalid(fmt.Sprintf("resources[%q].value", path), "not JSON")
				}
				p.Resources[path] = wl.Resource{Type: res.GetType(), Value: json.RawMessage(res.GetValue())}
			}
		}
		if vs := req.GetVariables(); len(vs) > 0 {
			p.Variables = make(map[string]wl.Variable, len(vs))
			for path, v := range vs {
				p.Variables[path] = wl.Variable{Value: v.GetValue(), Secret: v.GetSecret(), Ciphertext: v.GetCiphertext()}
			}
		}
		r.opts.Platform = p
	}
	switch req.GetTrace() {
	case "", traceNone:
	case traceSummary, traceFull:
		r.trace = req.GetTrace()
	default:
		return nil, invalid("trace", fmt.Sprintf("%q is not none, summary or full", req.GetTrace()))
	}
	return r, nil
}

// decodeSelectors decodes raw, which must be exactly one JSON array of
// selector objects and nothing after it, into sels. A selector key that
// wl.Selector does not have refuses it.
func decodeSelectors(raw []byte, sels *[]wl.Selector) error {
	if t := bytes.TrimLeft(raw, " \t\r\n"); len(t) == 0 || t[0] != '[' {
		return errors.New("not a JSON array of selectors")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(sels); err != nil {
		return fmt.Errorf("not a JSON array of selectors: %v", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("more than one JSON array of selectors")
	}
	return nil
}

// runLimits maps a request's limits to wl's. A count that an int cannot
// hold, either way, refuses here, naming its field, so that no value
// changes as it narrows; Runtime.Run refuses a negative limit that an int
// holds.
func runLimits(l *plugin.Limits) (wl.RunLimits, error) {
	for _, f := range []struct {
		name string
		v    int64
	}{
		{"limits.max_in_flight", l.GetMaxInFlight()},
		{"limits.max_args_bytes", l.GetMaxArgsBytes()},
	} {
		if f.v < math.MinInt || f.v > math.MaxInt {
			return wl.RunLimits{}, invalid(f.name, fmt.Sprintf("%d is outside what this engine can count (%d to %d)", f.v, math.MinInt, math.MaxInt))
		}
	}
	return wl.RunLimits{
		MaxInFlight:  int(l.GetMaxInFlight()),
		MaxArgsBytes: int(l.GetMaxArgsBytes()),
		MaxHeldBytes: l.GetMaxHeldBytes(),
		Bytes:        l.GetBytes(),
	}, nil
}

// nestedUnknown refuses a request whose identity, limits, hooks, resources
// or variables hold fields that this engine does not know, naming the
// field that holds them and their field numbers.
func nestedUnknown(req *plugin.RunRequest) error {
	if id := req.GetIdentity(); id != nil && len(id.ProtoReflect().GetUnknown()) > 0 {
		u := id.ProtoReflect().GetUnknown()
		return invalid("identity", fmt.Sprintf("it sets fields that this engine does not know (%s)", unknownFields(u)))
	}
	if l := req.GetLimits(); l != nil && len(l.ProtoReflect().GetUnknown()) > 0 {
		u := l.ProtoReflect().GetUnknown()
		return invalid("limits", fmt.Sprintf("it sets fields that this engine does not know (%s)", unknownFields(u)))
	}
	if h := req.GetHooks(); h != nil && len(h.ProtoReflect().GetUnknown()) > 0 {
		u := h.ProtoReflect().GetUnknown()
		return invalid("hooks", fmt.Sprintf("it sets fields that this engine does not know (%s)", unknownFields(u)))
	}
	for _, path := range slices.Sorted(maps.Keys(req.GetResources())) {
		if r := req.GetResources()[path]; r != nil && len(r.ProtoReflect().GetUnknown()) > 0 {
			u := r.ProtoReflect().GetUnknown()
			return invalid(fmt.Sprintf("resources[%q]", path), fmt.Sprintf("it sets fields that this engine does not know (%s)", unknownFields(u)))
		}
	}
	for _, path := range slices.Sorted(maps.Keys(req.GetVariables())) {
		if v := req.GetVariables()[path]; v != nil && len(v.ProtoReflect().GetUnknown()) > 0 {
			u := v.ProtoReflect().GetUnknown()
			return invalid(fmt.Sprintf("variables[%q]", path), fmt.Sprintf("it sets fields that this engine does not know (%s)", unknownFields(u)))
		}
	}
	return nil
}

// unknownFields names the field numbers of a message's unknown fields.
func unknownFields(raw protoreflect.RawFields) string {
	var out []byte
	for len(raw) > 0 {
		num, _, n := protowire.ConsumeField(raw)
		if n <= 0 {
			break
		}
		if len(out) > 0 {
			out = append(out, ", "...)
		}
		out = fmt.Appendf(out, "field %d", num)
		raw = raw[n:]
	}
	if len(out) == 0 {
		return "unparsed fields"
	}
	return string(out)
}

// maxBudgetMS is the longest budget in milliseconds that a time.Duration
// holds: 9,223,372,036,854 ms, about 292 years.
const maxBudgetMS = math.MaxInt64 / int64(time.Millisecond)

// budget converts a deadline in milliseconds to a duration, as host mode's
// run frame converts it: 0 for one of 0 or less, a deadline that has
// passed, and the longest duration for one above maxBudgetMS.
func budget(ms int64) time.Duration {
	switch {
	case ms <= 0:
		return 0
	case ms > maxBudgetMS:
		return math.MaxInt64
	}
	return time.Duration(ms) * time.Millisecond
}

// resultOf is the RunResult of a run that started, with its trace at the
// level trace.
func resultOf(res *wl.Result, trace string) (*plugin.RunResult, error) {
	out := &plugin.RunResult{
		Id:     res.ID,
		Status: res.Status,
		Result: res.Result,
		// A script's writes cannot be seen: it may have made some once its
		// invocation was sent.
		MayHaveMutated: res.MayHaveMutated || res.Started,
		CancelCause:    res.CancelCause,
	}
	if res.Latched != nil {
		out.Latched = res.Latched.Error()
	}
	if len(res.Warnings) > 0 {
		type warning struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		ws := make([]warning, len(res.Warnings))
		for i, w := range res.Warnings {
			ws[i] = warning{w.Code, w.Message}
		}
		b, err := json.Marshal(ws)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "the run's warnings: %v", err)
		}
		out.Warnings = b
	}
	if res.Trace != nil {
		out.Deviations = res.Trace.Deviations
		if trace != traceNone {
			b, err := wlext.TraceJSON(res.Trace, trace)
			if err != nil {
				return nil, status.Errorf(codes.Internal, "the run's trace: %v", err)
			}
			out.Trace = b
		}
	}
	return out, nil
}

// runRefusal is the gRPC error of a run that wl did not start: err is
// Runtime.Run's error.
func runRefusal(err error) error {
	switch {
	case errors.Is(err, wl.ErrClosed):
		return refusal(codes.Unavailable, err.Error())
	case errors.Is(err, wl.ErrRunInFlight):
		return refusal(codes.AlreadyExists, err.Error())
	case errors.Is(err, engine.ErrNoTarget):
		return refusal(codes.NotFound, err.Error())
	}
	return refusal(codes.InvalidArgument, err.Error())
}

// invalid is the INVALID_ARGUMENT refusal of a request, naming field when
// it is not empty.
func invalid(field, why string) error {
	if field == "" {
		return refusal(codes.InvalidArgument, why)
	}
	return refusal(codes.InvalidArgument, fmt.Sprintf("the request's %s: %s", field, why))
}

// refusal is the gRPC error of a run that did not start: code and msg,
// with the RUN_NOT_STARTED detail that marks it as the engine's own
// refusal.
func refusal(code codes.Code, msg string) error {
	st := status.New(code, msg)
	with, err := st.WithDetails(&errdetails.ErrorInfo{Domain: plugin.ErrorDomain, Reason: plugin.ReasonRunNotStarted})
	if err != nil {
		return st.Err()
	}
	return with.Err()
}

// newUUID returns a random (version 4) UUID, the job id of a run whose
// request has none.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
