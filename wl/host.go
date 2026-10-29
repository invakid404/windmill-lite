package wl

import (
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
		Check:         rt.hostCheck(opts),
		Run:           rt.serveHost,
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
// intercept stands in for the modules it names (H1's static form), and
// its Settled closes once the run has settled (onRunSettled).
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
	settled := make(chan struct{})
	res, err := rt.run(ctx, r.Path, json.RawMessage(r.Args.Producer()), RunOptions{ID: r.ID, Kind: r.Kind}, hooks, func() { close(settled) })
	if err != nil {
		return refused(err)
	}
	done := harness.RunDone{Status: res.Status, MayHaveMutated: res.MayHaveMutated, Settled: settled}
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
