package wl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"time"

	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/harness"
	"github.com/invakid404/windmill-lite/internal/ir"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// Host is host mode (ADR 0004 host mode, ADR 0009), for Options.Host: the
// runtime is the engine of a host process that is also its harness, and
// that started it with one end of a socketpair. The host greets the
// runtime as a Bun harness that offers the host capability, submits runs
// over the connection (run frames), each in a scope of its own that its
// invocations re-enter (H11), and runs every step of them itself. The Bun
// host SDK (harnesses/bun/host) is such a host, and `windmill-lite host`
// the engine it starts; any host that speaks the protocol is one too.
type Host struct {
	// Conn is the socket to the host: the engine's end of the socketpair,
	// which `windmill-lite host` has as file descriptor 3.
	Conn net.Conn
	// Children, when the host passes one, is the side channel on which it
	// announces its steps' child process groups (WR-55), file descriptor 4
	// of `windmill-lite host`; nil means they come on Conn.
	Children io.Reader
	// Workdir is the working directory the host passed: a directory the
	// host can write, which must already be its working directory (ADR
	// 0004, the shared-process failure model, item 5). Open refuses the
	// host otherwise, with workdir_not_writable or workdir_mismatch, which
	// the host's hello lets it check. The host extracts the bundle's
	// codebase assets there, and the runtime writes each step's args.json
	// there (WR-5). It is required.
	Workdir string
	// KillAfter is the watchdog's threshold (ADR 0004 host mode item 4):
	// after that long without a frame from the host, whose event loop has
	// stopped, the runtime sends it SIGKILL and its connection ends. Zero
	// means 30 s; a negative value disables the watchdog.
	KillAfter time.Duration
}

// The reject codes with which Open refuses a host (ADR 0004 host mode):
// its working directory cannot be used, or the runtime cannot serve the
// bundle at all (another engine version built it, or it does not verify).
const (
	RejectWorkdirNotWritable = harness.RejectWorkdirNotWritable
	RejectWorkdirMismatch    = harness.RejectWorkdirMismatch
	RejectEngine             = harness.RejectEngine
)

// HandshakeError is Open's error when the host's greeting did not lead to
// a connection: the runtime refused the host (Code is one of the Reject
// codes of the harness protocol, such as RejectWorkdirMismatch), or the
// host's connection ended first.
type HandshakeError = harness.HandshakeError

// Done is closed once host mode ends: the host's connection ended, because
// the host exited or closed it, broke the protocol, or the watchdog killed
// it. Its runs in flight were lost with it (ADR 0004 host mode item 6).
// It is nil for a runtime that is not in host mode.
func (rt *Runtime) Done() <-chan struct{} {
	if rt.host == nil {
		return nil
	}
	return rt.host.Done()
}

// attachHost connects the runtime to its host, whose runs it then serves:
// the handshake checks the host as it checks a spawned harness, plus its
// working directory. Runs can start as soon as it returns.
func (rt *Runtime) attachHost(h *Host, diag io.Writer, opts Options) error {
	killAfter := h.KillAfter
	if killAfter < 0 {
		killAfter = harness.WatchdogOff
	}
	p, err := harness.AttachHost(context.Background(), harness.HostConfig{
		Conn:          h.Conn,
		Children:      h.Children,
		Kind:          string(ir.ClassBun),
		Runtime:       harness.PinnedBun,
		Bundle:        rt.digest,
		EngineVersion: rt.version,
		KillAfter:     killAfter,
		Diagnostics:   diag,
		Facade:        rt.fac.WarmOrigin(),
		Platform:      rt.fac.Platform(),
		Workdir:       h.Workdir,
		// The arguments travel in the invoke frame; the file is left out
		// only for the scripts the build cleared (ADR 0004 host mode item
		// 7).
		NoArgsFile: noArgsFile(rt.manifest.NoArgsFile),
		Check:      rt.hostCheck(opts),
		Run:        rt.serveHost,
	})
	if err != nil {
		return err
	}
	rt.hostProc.Store(p)
	rt.host = p
	return nil
}

// Refuse refuses the host in the handshake, with reason, for an engine
// that cannot open a runtime for it (code RejectEngine): the host, which
// waits for its welcome, learns why before the connection ends. version
// is the engine's version, and diag receives the engine's diagnostics (nil
// means os.Stderr). Open does this itself when it fails.
func (h *Host) Refuse(version string, diag io.Writer, reason error) {
	refuseHost(h, version, diag, reason)
}

// refuseHost refuses the host in the handshake with the reason Open
// failed, so that the host learns why before the connection ends.
func refuseHost(h *Host, version string, diag io.Writer, cause error) {
	harness.AttachHost(context.Background(), harness.HostConfig{
		Conn:          h.Conn,
		Kind:          string(ir.ClassBun),
		Runtime:       harness.PinnedBun,
		EngineVersion: version,
		Diagnostics:   diag,
		Refuse:        &harness.Reject{Code: harness.RejectEngine, Message: cause.Error()},
	})
}

// serveHost runs one run the host submitted, with Run, and returns what
// goes back in run_done: its outcome, or why it did not start. Its
// intercept stands in for the modules it names (H1's static form), its
// own hooks and budgets are the run's (hostBudgets), and its Settled
// closes once the run has settled (onRunSettled).
func (rt *Runtime) serveHost(ctx context.Context, p *harness.Process, r harness.RunRequest) harness.RunDone {
	// Runs can arrive before AttachHost has returned.
	rt.hostProc.Store(p)
	var hooks engine.Hooks = engine.NoHooks{}
	if !r.Intercept.IsZero() {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(r.Intercept.Producer(), &raw); err != nil {
			return refused(fmt.Errorf("the run's intercept is not a JSON object: %w", err))
		}
		results := make(map[string]jsonval.Value, len(raw))
		for id, v := range raw {
			results[id] = jsonval.FromProducer(v)
		}
		hooks = engine.StaticIntercepts{Results: results}
	}
	opts, err := hostBudgets(r, rt.prog)
	if err != nil {
		return refused(err)
	}
	// The hard deadline bounds the run's context until the run has
	// settled: iterations a latch or a cancellation left running are
	// abandoned at it (H5).
	cancel := context.CancelFunc(func() {})
	if r.DeadlineMS > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(r.DeadlineMS)*time.Millisecond)
	}
	settled := make(chan struct{})
	res, err := rt.run(ctx, r.Path, json.RawMessage(r.Args.Producer()), opts, hooks, func() { close(settled); cancel() })
	if err != nil {
		cancel()
		return refused(err)
	}
	done := harness.RunDone{Status: res.Status, MayHaveMutated: res.MayHaveMutated, Settled: settled}
	if res.Trace != nil {
		done.Deviations = res.Trace.Deviations
	}
	if res.Latched != nil {
		done.Latched = res.Latched.Error()
	}
	if res.Result != nil {
		done.Result = jsonval.FromProducer(res.Result)
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
		b, _ := json.Marshal(ws)
		done.Warnings = jsonval.FromProducer(b)
	}
	return done
}

// hostBudgets are the RunOptions of a run frame: its id and kind, and what
// a Go host gives in RunOptions (ADR 0008): write-capable selectors (H3),
// halts (H8) and fatal error names (H7), as the run's own hooks, which
// come ahead of the plugin's (mergeHooks); the pre-write deadline (H4),
// from now; and the record clock (H10). The hard deadline is the run's
// context's (serveHost). The run's selectors are checked as a plugin's
// are (checkSelectorIn), against prog. A selector that fails refuses the
// run, and the error names it as the host SDK's run options do, such as
// haltAfter[0].
func hostBudgets(r harness.RunRequest, prog *ir.Program) (RunOptions, error) {
	opts := RunOptions{ID: r.ID, Kind: r.Kind}
	var h Hooks
	for _, f := range []struct {
		field, option string
		raw           jsonval.Value
		sels          *[]Selector
	}{
		{"write_capable", "writeCapable", r.WriteCapable, &h.WriteCapable},
		{"halt_before", "haltBefore", r.HaltBefore, &h.HaltBefore},
		{"halt_after", "haltAfter", r.HaltAfter, &h.HaltAfter},
	} {
		if f.raw.IsZero() {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(f.raw.Producer()))
		dec.DisallowUnknownFields()
		if err := dec.Decode(f.sels); err != nil {
			return opts, fmt.Errorf("the run's %s: %w", f.field, err)
		}
		for i, s := range *f.sels {
			if err := checkSelectorIn(s, prog); err != nil {
				return opts, fmt.Errorf("the run's %s[%d]: %w", f.option, i, err)
			}
		}
	}
	h.FatalErrorNames = r.FatalErrorNames
	if len(h.WriteCapable) > 0 || len(h.HaltBefore) > 0 || len(h.HaltAfter) > 0 || len(h.FatalErrorNames) > 0 {
		opts.Hooks = &h
	}
	if r.PreWriteDeadlineMS > 0 {
		opts.PreWriteDeadline = time.Now().Add(time.Duration(r.PreWriteDeadlineMS) * time.Millisecond)
	}
	if r.Clock == harness.ClockRecord {
		opts.Clock = &RecordClock{}
	}
	return opts, nil
}

// refused is the run_done of a run that did not start.
func refused(err error) harness.RunDone {
	code := harness.RunRefused
	if errors.Is(err, ErrClosed) {
		code = harness.RunDraining
	}
	return harness.RunDone{Error: &harness.RunError{Code: code, Message: err.Error()}}
}

// hostExec runs the steps of a host-mode runtime in the host.
type hostExec struct {
	proc *atomic.Pointer[harness.Process]
}

func (x hostExec) Execute(ctx context.Context, inv engine.Invocation) (engine.StepOutcome, error) {
	p := x.proc.Load()
	if p == nil {
		if inv.Report != nil {
			inv.Report.Settled(engine.SettleNotStarted)
		}
		return engine.StepOutcome{}, errors.New("wl: the host is not connected")
	}
	return p.Execute(ctx, inv)
}

// settledHooks are a flow run's hooks with onRunSettled reported to the
// host too (H9).
type settledHooks struct {
	engine.Hooks
	settled func()
}

func (h settledHooks) OnRunSettled(t *engine.Trace) {
	h.Hooks.OnRunSettled(t)
	h.settled()
}

// settlingExec counts a script job's invocation in its run's settlement.
type settlingExec struct {
	inner  engine.StepExecutor
	settle *engine.RunSettlement
}

func (x settlingExec) Execute(ctx context.Context, inv engine.Invocation) (engine.StepOutcome, error) {
	inv.Report = x.settle.Track(inv.Report)
	return x.inner.Execute(ctx, inv)
}

// interceptFirst are a run's hooks with host mode's static intercepts
// asked first (H1's no-code form, from the run frame), then the hooks'
// own interceptions.
type interceptFirst struct {
	engine.Hooks
	first engine.Hooks
}

func (x interceptFirst) Intercept(ctx context.Context, r engine.Run, s engine.Site, args jsonval.Value) (engine.StepOutcome, bool, error) {
	if out, ok, err := x.first.Intercept(ctx, r, s, args); ok || err != nil {
		return out, ok, err
	}
	return x.Hooks.Intercept(ctx, r, s, args)
}

// noArgsFile reports the scripts of a bundle's no_args_file list, or nil
// when it lists none, so that every invocation's args.json is written.
func noArgsFile(scripts []string) func(string) bool {
	if len(scripts) == 0 {
		return nil
	}
	free := make(map[string]bool, len(scripts))
	for _, s := range scripts {
		free[s] = true
	}
	return func(script string) bool { return free[script] }
}
