package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/engine"
)

// These tests run Process and Pool against the real Bun harness on the
// pinned Bun (see bunPath).

func TestBunHandshake(t *testing.T) {
	cfg, _ := bunConfig(t)
	p := spawn(t, cfg)
	h := p.Hello()
	if h.Proto != ProtocolVersion || h.Harness.Kind != "bun" || h.Runtime != PinnedBun || h.Bundle != cfg.Bundle || h.PID != p.PID() {
		t.Fatalf("hello %+v", h)
	}
	// The runtime intercepts facade fetches (W1) and runs the native
	// boundary (C3n); fresh and host are C3b's and the host SDK's.
	if h.Caps != (Caps{Fetch: true, Native: true}) {
		t.Fatalf("caps %+v, want fetch and native", h.Caps)
	}
}

// The engine refuses a native invocation, before it is dispatched, to a
// harness whose hello does not offer the native boundary, and to a process
// with no facade origin, which a native step must read as BASE_URL and
// BASE_INTERNAL_URL (WR-45; c3n review r3, finding 6). A Bun invocation
// needs neither.
func TestCheckInvocationNeedsTheNativeCap(t *testing.T) {
	inv := newInvocation("echo", `{}`, newRecorder())
	inv.Boundary = engine.BoundaryNative
	const facade = "http://127.0.0.1:9"
	if err := checkInvocation(inv, Caps{Fetch: true}, facade); err == nil || !strings.Contains(err.Error(), "no native boundary") {
		t.Fatalf("without the cap: %v, want a refusal", err)
	}
	if err := checkInvocation(inv, Caps{Fetch: true, Native: true}, ""); err == nil || !strings.Contains(err.Error(), "facade") {
		t.Fatalf("without a facade: %v, want a refusal", err)
	}
	if err := checkInvocation(inv, Caps{Fetch: true, Native: true}, facade); err != nil {
		t.Fatalf("with the cap and a facade: %v", err)
	}
	inv.Boundary = engine.BoundaryBun
	if err := checkInvocation(inv, Caps{Fetch: true}, ""); err != nil {
		t.Fatalf("a Bun invocation: %v", err)
	}
}

// A script built for one boundary and invoked with the other escapes,
// naming the mismatch, instead of running.
func TestBunBoundaryMismatch(t *testing.T) {
	cfg, _ := bunConfig(t)
	// A native invocation needs a facade origin (WR-45).
	cfg.Facade = "http://127.0.0.1:9"
	p := spawn(t, cfg)
	inv := newInvocation("echo", `{}`, newRecorder())
	inv.Boundary = engine.BoundaryNative
	out, err := p.Execute(context.Background(), inv)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != engine.OutcomeEscaped || !strings.Contains(out.Tail, "is not built for the native boundary") {
		t.Fatalf("outcome %+v, want escaped for the boundary", out)
	}
}

func TestBunHandshakeRejects(t *testing.T) {
	for _, tc := range []struct {
		code string
		edit func(*ProcessConfig)
	}{
		{RejectRuntime, func(c *ProcessConfig) { c.Runtime = "1.4.2" }},
		{RejectBundle, func(c *ProcessConfig) { c.Bundle = "sha256:other" }},
		{RejectHarness, func(c *ProcessConfig) { c.Kind = "python" }},
	} {
		t.Run(tc.code, func(t *testing.T) {
			cfg, diag := bunConfig(t)
			tc.edit(&cfg)
			_, err := Spawn(context.Background(), cfg)
			var he *HandshakeError
			if !errors.As(err, &he) || he.Code != tc.code {
				t.Fatalf("got %v, want %s", err, tc.code)
			}
			// The harness said why it exits on its stderr.
			deadline := time.Now().Add(2 * time.Second)
			for !strings.Contains(diag.String(), "rejected: "+tc.code) {
				if time.Now().After(deadline) {
					t.Fatalf("diagnostics %q", diag.String())
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

// cl P4-3: the watchdog's SIGKILL must reach the process that speaks the
// protocol. A harness started through a wrapper that forks it says hello
// from another pid, and the engine refuses it.
func TestBunPIDCheckRefusesAWrapper(t *testing.T) {
	cfg, _ := bunConfig(t)
	script := `"$0" "$@"; exit $?`
	cfg.Args = append([]string{"-c", script, cfg.Path}, cfg.Args...)
	cfg.Path = "/bin/sh"
	_, err := Spawn(context.Background(), cfg)
	var he *HandshakeError
	if !errors.As(err, &he) || he.Code != RejectPID {
		t.Fatalf("got %v, want %s", err, RejectPID)
	}
	// The same wrapper with exec keeps the pid and is accepted.
	cfg.Args[1] = `exec "$0" "$@"`
	spawn(t, cfg)
}

func TestBunEcho(t *testing.T) {
	cfg, _ := bunConfig(t)
	p := spawn(t, cfg)
	rec := newRecorder()
	out, err := execute(t, p, newInvocation("echo", `{"value":{"b":[1,2],"a":{"x":null},"1":"one"}}`, rec), 5*time.Second)
	if err != nil || out.Kind != engine.OutcomeOK {
		t.Fatalf("got %+v, %v", out, err)
	}
	// The harness's own JSON text, unchanged by Go (ADR 0005).
	if got := string(out.Body.Producer()); got != `{"1":"one","b":[1,2],"a":{"x":null}}` {
		t.Fatalf("body %s", got)
	}
	// Settled before Execute returned.
	if got := rec.log(); got != "settled completed" {
		t.Fatalf("reporter calls %q", got)
	}
}

func TestBunErrorAndEscapedOutcomes(t *testing.T) {
	cfg, _ := bunConfig(t)
	p := spawn(t, cfg)
	out, err := execute(t, p, newInvocation("throws", `{"message":"boom"}`, newRecorder()), 5*time.Second)
	var body struct{ Message, Name, Stack string }
	if err != nil || out.Kind != engine.OutcomeError || json.Unmarshal(out.Body.Producer(), &body) != nil ||
		body.Message != "boom" || body.Name != "Error" || !strings.Contains(body.Stack, "boom") {
		t.Fatalf("got %+v, %v", out, err)
	}
	out, err = execute(t, p, newInvocation("top-level-throw", `{}`, newRecorder()), 5*time.Second)
	if err != nil || out.Kind != engine.OutcomeEscaped || out.ExitCode != 1 || !strings.Contains(out.Tail, "top-level failure") {
		t.Fatalf("got %+v, %v", out, err)
	}
}

// ADR 0004: a harness process that dies fails its in-flight invocations as
// escaped with its real exit code; co-resident ones included.
func TestBunProcessDeathFailsEveryInFlightInvocation(t *testing.T) {
	for _, tc := range []struct {
		script, args string
		code         int
	}{
		// process.reallyExit ends the process: the harness intercepts
		// process.exit only (D11).
		{"die", `{"ms":100,"code":7}`, 7},
	} {
		t.Run(tc.script, func(t *testing.T) {
			cfg, _ := bunConfig(t)
			p := spawn(t, cfg)
			type res struct {
				out engine.StepOutcome
				err error
				rec *recorder
			}
			results := make(chan res, 2)
			for _, inv := range []struct{ script, args string }{{tc.script, tc.args}, {"sleep", `{"ms":5000}`}} {
				go func() {
					rec := newRecorder()
					out, err := execute(t, p, newInvocation(inv.script, inv.args, rec), 10*time.Second)
					results <- res{out, err, rec}
				}()
			}
			for range 2 {
				r := <-results
				if r.err != nil || r.out.Kind != engine.OutcomeEscaped || r.out.ExitCode != tc.code || r.out.Signal != "" {
					t.Fatalf("got %+v, %v; want escaped with exit code %d", r.out, r.err, tc.code)
				}
				if got := r.rec.log(); got != "settled process_exited" {
					t.Fatalf("reporter calls %q", got)
				}
			}
			<-p.Done()
			if st := p.ExitStatus(); st.Code != tc.code {
				t.Fatalf("exit status %v", st)
			}
		})
	}
}

// ADR 0004, the shared-process failure model, item 6 (D11): process.exit
// fixes its own invocation's outcome and ends neither the process nor a
// co-resident invocation. The exiting call never ends, so it settles only
// with the process.
func TestBunProcessExitEndsOnlyItsInvocation(t *testing.T) {
	cfg, _ := bunConfig(t)
	p := spawn(t, cfg)
	sleeper := make(chan engine.StepOutcome, 1)
	go func() {
		out, _ := execute(t, p, newInvocation("sleep", `{"ms":400}`, newRecorder()), 5*time.Second)
		sleeper <- out
	}()
	rec := newRecorder()
	out, err := execute(t, p, newInvocation("exit", `{"ms":50,"code":7}`, rec), 5*time.Second)
	if err != nil || out.Kind != engine.OutcomeEscaped || out.ExitCode != 7 || out.Signal != "" {
		t.Fatalf("exiting invocation: %+v, %v", out, err)
	}
	if out := <-sleeper; out.Kind != engine.OutcomeOK || string(out.Body.Producer()) != "400" {
		t.Fatalf("co-resident invocation: %+v", out)
	}
	if got := rec.log(); got != "" {
		t.Fatalf("the exiting call settled: %q", got)
	}
	out, err = execute(t, p, newInvocation("exit-now", `{"code":0}`, newRecorder()), 5*time.Second)
	if err != nil || out.Kind != engine.OutcomeEmpty {
		t.Fatalf("process.exit(0): %+v, %v", out, err)
	}
	select {
	case <-p.Done():
		t.Fatalf("the process ended: %v", p.ExitStatus())
	default:
	}
	p.Kill()
	<-p.Done()
	if k := rec.waitSettled(t, 5*time.Second); k != engine.SettleProcessExited {
		t.Fatalf("settled %s; calls %s", k, rec.log())
	}
}

// ADR 0004, the shared-process failure model, items 2 and 3: an error
// thrown from a timer of a pending call fails that invocation as escaped
// with exit code 1 while the call goes on, and its return settles it late;
// a rejection no invocation's context owns fails nothing and reaches every
// invocation in flight as a warning.
func TestBunProcessErrorAttribution(t *testing.T) {
	cfg, _ := bunConfig(t)
	p := spawn(t, cfg)
	rec := newRecorder()
	out, err := execute(t, p, newInvocation("timer-throw", `{"ms":30}`, rec), 5*time.Second)
	if err != nil || out.Kind != engine.OutcomeEscaped || out.ExitCode != 1 || !strings.Contains(out.Tail, "from a timer") {
		t.Fatalf("attributed error: %+v, %v", out, err)
	}
	if k := rec.waitSettled(t, 5*time.Second); k != engine.SettleCompleted {
		t.Fatalf("settled %s", k)
	}
	if got := rec.log(); got != `late result final=true "done"; settled completed` {
		t.Fatalf("reporter calls %q", got)
	}

	bystander := newRecorder()
	done := make(chan engine.StepOutcome, 1)
	go func() {
		out, _ := execute(t, p, newInvocation("sleep", `{"ms":400}`, bystander), 5*time.Second)
		done <- out
	}()
	time.Sleep(50 * time.Millisecond)
	ctx, cancel := context.WithCancelCause(context.Background())
	rejecter := newRecorder()
	go p.Execute(ctx, newInvocation("detached-reject", `{"ms":20}`, rejecter))
	if out := <-done; out.Kind != engine.OutcomeOK {
		t.Fatalf("bystander: %+v", out)
	}
	if got := bystander.log(); got != "warning unattributed_error; settled completed" {
		t.Fatalf("bystander's reporter calls %q", got)
	}
	cancel(engine.CancelCanceled)
	h, err := p.Ping(context.Background())
	if err != nil || h.Errors != 1 {
		t.Fatalf("health %+v, %v; want one unattributed error", h, err)
	}
	if got := rejecter.log(); !strings.HasPrefix(got, "warning unattributed_error") {
		t.Fatalf("rejecter's reporter calls %q", got)
	}
}

// ADR 0004, the shared-process failure model, items 2 and 4 (D9, D11): an
// error a timer throws, or a process.exit it calls, after the invocation
// settled changes nothing: the outcome stands, the invocation does not
// settle again, the error is a late error that settles nothing, and the
// process serves the next invocation (c3a review 1, finding 8).
func TestBunWorkAfterTheResultIsLate(t *testing.T) {
	for _, tc := range []struct{ script, body string }{
		{"late-throw", `"message":"after the result"`},
		{"late-exit", `"name":"ProcessExit"`},
	} {
		t.Run(tc.script, func(t *testing.T) {
			cfg, _ := bunConfig(t)
			p := spawn(t, cfg)
			rec := newRecorder()
			out, err := execute(t, p, newInvocation(tc.script, `{"ms":20}`, rec), 5*time.Second)
			if err != nil || out.Kind != engine.OutcomeOK || string(out.Body.Producer()) != `"done"` {
				t.Fatalf("got %+v, %v", out, err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for !strings.Contains(rec.log(), "late error") && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			got := rec.log()
			if !strings.HasPrefix(got, "settled completed; late error final=false {") || strings.Count(got, "settled") != 1 || !strings.Contains(got, tc.body) {
				t.Fatalf("reporter calls %q", got)
			}
			out, err = execute(t, p, newInvocation("sleep", `{"ms":1}`, newRecorder()), 5*time.Second)
			if err != nil || out.Kind != engine.OutcomeOK {
				t.Fatalf("the next invocation: %+v, %v", out, err)
			}
		})
	}
}

// The handlers never throw, whatever the error (c3a review 1, finding 2): a
// rejection whose text is longer than max_message still fails nothing, and
// the co-resident invocation's warning carries as much of it as fits.
func TestBunLongUnattributedErrorUnderASmallMessageLimit(t *testing.T) {
	cfg, _ := bunConfig(t)
	cfg.MaxMessage = 1024
	p := spawn(t, cfg)
	bystander := newRecorder()
	done := make(chan engine.StepOutcome, 1)
	go func() {
		out, _ := execute(t, p, newInvocation("sleep", `{"ms":400}`, bystander), 5*time.Second)
		done <- out
	}()
	time.Sleep(50 * time.Millisecond)
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	go p.Execute(ctx, newInvocation("detached-reject", `{"ms":20,"size":5000}`, newRecorder()))
	if out := <-done; out.Kind != engine.OutcomeOK || string(out.Body.Producer()) != "400" {
		t.Fatalf("bystander: %+v", out)
	}
	if got := bystander.log(); got != "warning unattributed_error; settled completed" {
		t.Fatalf("bystander's reporter calls %q", got)
	}
	bystander.mu.Lock()
	w := bystander.warnings
	bystander.mu.Unlock()
	if len(w) != 1 || !strings.HasPrefix(w[0].Message, "unhandledRejection: Error: xxxx") || !strings.HasSuffix(w[0].Message, "…") || len(w[0].Message) > 1024 {
		t.Fatalf("warning %+v", w)
	}
	h, err := p.Ping(context.Background())
	if err != nil || h.Errors != 1 {
		t.Fatalf("health %+v, %v; want the one unattributed error", h, err)
	}
	select {
	case <-p.Done():
		t.Fatalf("the process ended: %v", p.ExitStatus())
	default:
	}
}

// ADR 0004, the shared-process failure model, item 1 (W1's exit gate): one
// detached rejection does not end the process; it fails no invocation, the
// co-resident one completes, and it is counted in health as an error no
// invocation owned (item 3; attribution is C3a's).
func TestBunDetachedRejectionDoesNotEndTheProcess(t *testing.T) {
	cfg, diag := bunConfig(t)
	p := spawn(t, cfg)
	rejecter := make(chan error, 1)
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	go func() {
		// detached-reject rejects after 100 ms and never settles; it is
		// abandoned once the rejection has been seen.
		_, err := p.Execute(ctx, newInvocation("detached-reject", `{"ms":100}`, newRecorder()))
		rejecter <- err
	}()
	out, err := execute(t, p, newInvocation("sleep", `{"ms":400}`, newRecorder()), 5*time.Second)
	if err != nil || out.Kind != engine.OutcomeOK || string(out.Body.Producer()) != "400" {
		t.Fatalf("co-resident invocation: %+v, %v", out, err)
	}
	h, err := p.Ping(context.Background())
	if err != nil || h == nil || h.Errors != 1 || h.InFlight != 1 {
		t.Fatalf("health %+v, %v; want one unattributed error and the rejecter still in flight", h, err)
	}
	cancel(engine.CancelCanceled)
	if err := <-rejecter; !errors.Is(err, engine.CancelCanceled) {
		t.Fatalf("rejecter: %v", err)
	}
	out, err = execute(t, p, newInvocation("echo", `{"after":true}`, newRecorder()), 5*time.Second)
	if err != nil || out.Kind != engine.OutcomeOK {
		t.Fatalf("after the rejection: %+v, %v", out, err)
	}
	select {
	case <-p.Done():
		t.Fatalf("the process ended: %v", p.ExitStatus())
	default:
	}
	if !strings.Contains(diag.String(), "unhandledRejection that no invocation owns: Error: detached") {
		t.Fatalf("diagnostics:\n%s", diag)
	}
}

func TestBunKillGivesTheSignal(t *testing.T) {
	cfg, _ := bunConfig(t)
	p := spawn(t, cfg)
	done := make(chan engine.StepOutcome, 1)
	go func() {
		out, _ := execute(t, p, newInvocation("never", `{}`, newRecorder()), 10*time.Second)
		done <- out
	}()
	time.Sleep(100 * time.Millisecond)
	p.Kill()
	out := <-done
	if out.Kind != engine.OutcomeEscaped || out.Signal != "SIGKILL" || out.ExitCode != -1 {
		t.Fatalf("got %+v", out)
	}
	if got := p.ExitStatus().String(); got != "process terminated by signal: SIGKILL" {
		t.Fatalf("exit status %q", got)
	}
}

// Abandonment (deviation D2): Execute returns the context's cause while the
// call goes on, the harness gets a cancel, and the call's end arrives later
// as the final late result, then the settlement. The step is a gate that
// the test opens only after those checks, so none depends on timing. A
// deadline (context.WithTimeoutCause) takes the same path: Execute reads
// only ctx.Done and the cause.
func TestBunCancelAbandonsAndSettlesLate(t *testing.T) {
	gates := t.TempDir()
	cfg, _ := bunConfig(t)
	p := spawn(t, cfg)
	held := newGate(t, gates, "held")
	rec := newRecorder()
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	held.cancelOnceStarted(cancel, engine.CancelTimeout)
	if _, err := p.Execute(ctx, held.invocation(rec)); !errors.Is(err, engine.CancelTimeout) {
		t.Fatalf("got %v", err)
	}
	// The gate is still closed: Execute did not wait for the call.
	if in, ab := p.Load(); in != 0 || ab != 1 {
		t.Fatalf("load %d in flight, %d abandoned", in, ab)
	}
	h, err := p.Ping(context.Background())
	if err != nil || h == nil || h.Abandoned != 1 || h.InFlight != 0 {
		t.Fatalf("pong %+v, %v", h, err)
	}
	if got := rec.log(); got != "" {
		t.Fatalf("reporter calls before the call ended: %q", got)
	}
	held.open(t)
	if k := rec.waitSettled(t, 10*time.Second); k != engine.SettleCompleted {
		t.Fatalf("settled %s", k)
	}
	if got := rec.log(); got != `late result final=true "released"; settled completed` {
		t.Fatalf("reporter calls %q", got)
	}
	if in, ab := p.Load(); in != 0 || ab != 0 {
		t.Fatalf("load after settlement %d, %d", in, ab)
	}
}

// A cancel queued while a large invoke is still being written follows its
// last part, so the harness knows the invocation when the cancel arrives
// and abandons it. Ahead of the invoke it would name an unknown invocation
// and be ignored, and the harness would report the call as in flight.
func TestBunCancelDuringLargeInvoke(t *testing.T) {
	cfg, _ := bunConfig(t)
	p := spawn(t, cfg)
	rec := newRecorder()
	ctx, cancel := context.WithCancelCause(context.Background())
	inv := newInvocation("sleep", `{"ms":1500,"pad":"`+strings.Repeat("y", 24<<20)+`"}`, rec)
	done := make(chan error, 1)
	go func() {
		_, err := p.Execute(ctx, inv)
		done <- err
	}()
	// Cancel as soon as it is dispatched, while its parts are still going
	// out.
	for in, _ := p.Load(); in == 0; in, _ = p.Load() {
		time.Sleep(50 * time.Microsecond)
	}
	cancel(engine.CancelCanceled)
	if err := <-done; !errors.Is(err, engine.CancelCanceled) {
		t.Fatalf("got %v", err)
	}
	// A ping is about no invocation, so it may overtake the invoke; ask
	// until the harness has the invocation.
	deadline := time.Now().Add(5 * time.Second)
	for {
		h, err := p.Ping(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if h.InFlight+h.Abandoned == 1 {
			if h.Abandoned != 1 {
				t.Fatalf("the harness runs the invocation as in flight: %+v", h)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the harness never had the invocation: %+v", h)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if k := rec.waitSettled(t, 10*time.Second); k != engine.SettleCompleted {
		t.Fatalf("settled %s", k)
	}
	if got := rec.log(); got != "late result final=true 1500; settled completed" {
		t.Fatalf("reporter calls %q", got)
	}
}

func TestBunNotDispatched(t *testing.T) {
	cfg, _ := bunConfig(t)
	p := spawn(t, cfg)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for name, tc := range map[string]struct {
		ctx  context.Context
		edit func(*engine.Invocation)
		want string
	}{
		"canceled before dispatch": {canceled, func(*engine.Invocation) {}, "context canceled"},
		"fresh mode":               {context.Background(), func(i *engine.Invocation) { i.ModuleState = engine.ModuleStateFresh }, "C3b"},
		"invalid entrypoint":       {context.Background(), func(i *engine.Invocation) { i.Entry = "not-a-name" }, "not a valid name"},
		"env not UTF-8":            {context.Background(), func(i *engine.Invocation) { i.Env = map[string]string{"X": "\xff"} }, "invalid_utf8"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := newRecorder()
			inv := newInvocation("echo", `{}`, rec)
			tc.edit(&inv)
			_, err := p.Execute(tc.ctx, inv)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
			if got := rec.log(); got != "settled not_started" {
				t.Fatalf("reporter calls %q", got)
			}
		})
	}
	// The process still serves.
	if out, err := execute(t, p, newInvocation("echo", `{}`, newRecorder()), 5*time.Second); err != nil || out.Kind != engine.OutcomeOK {
		t.Fatalf("got %+v, %v", out, err)
	}
}

func TestBunMessageLimit(t *testing.T) {
	cfg, _ := bunConfig(t)
	cfg.MaxMessage = 2 << 20
	p := spawn(t, cfg)
	// The engine refuses to send a message over the limit.
	rec := newRecorder()
	_, err := execute(t, p, newInvocation("length", `{"data":"`+strings.Repeat("y", 3<<20)+`"}`, rec), 5*time.Second)
	if !IsProtocolError(err, CodeMessageTooLarge) || rec.log() != "settled not_started" {
		t.Fatalf("got %v, calls %q", err, rec.log())
	}
	// The harness refuses to send a result over it, and reports why.
	out, err := execute(t, p, newInvocation("big", `{"bytes":3145728}`, newRecorder()), 5*time.Second)
	if err != nil || out.Kind != engine.OutcomeEscaped || !strings.Contains(out.Tail, "maximum message size of 2097152 bytes") {
		t.Fatalf("got %+v, %v", out, err)
	}
	// Under the limit, both directions go as part streams.
	out, err = execute(t, p, newInvocation("length", `{"data":"`+strings.Repeat("y", 1<<20+100)+`"}`, newRecorder()), 5*time.Second)
	if err != nil || string(out.Body.Producer()) != strconv.Itoa(1<<20+100) {
		t.Fatalf("got %+v, %v", out, err)
	}
	out, err = execute(t, p, newInvocation("big", `{"bytes":1500000}`, newRecorder()), 5*time.Second)
	if err != nil || len(out.Body.Producer()) != 1500002 {
		t.Fatalf("got %d bytes, %v", len(out.Body.Producer()), err)
	}
}

// ADR 0004 failure model item 5: a private working directory per spawned
// process, removed with it; stdout and stderr go to the diagnostics, never
// into a frame (WR-23).
func TestBunWorkingDirectoryAndStdio(t *testing.T) {
	cfg, diag := bunConfig(t)
	p := spawn(t, cfg)
	out, err := execute(t, p, newInvocation("process-info", `{}`, newRecorder()), 5*time.Second)
	var info struct {
		PID int
		Cwd string
	}
	if err != nil || json.Unmarshal(out.Body.Producer(), &info) != nil {
		t.Fatalf("got %+v, %v", out, err)
	}
	if info.PID != p.PID() || !sameDir(t, info.Cwd, p.Dir()) {
		t.Fatalf("step ran in pid %d, %s; the process is %d in %s", info.PID, info.Cwd, p.PID(), p.Dir())
	}
	st, err := os.Stat(p.Dir())
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("working directory %v, %v", st, err)
	}
	if out, err := execute(t, p, newInvocation("print", `{}`, newRecorder()), 5*time.Second); err != nil || string(out.Body.Producer()) != `"printed"` {
		t.Fatalf("got %+v, %v", out, err)
	}
	// The lines arrive through pipes, asynchronously to the frames. Bun
	// colours console.error because the base environment has FORCE_COLOR=1
	// (WR-22), so the stderr line is matched around its escapes.
	pid := strconv.Itoa(p.PID())
	for _, want := range [][2]string{
		{"harness pid " + pid + " stdout: ", "printed on stdout"},
		{"harness pid " + pid + " stderr: ", "printed on stderr"},
		{"harness pid " + pid + " stdout: ", `{"t":"result","id":"forged"`},
	} {
		deadline := time.Now().Add(2 * time.Second)
		for !hasLine(diag.String(), want[0], want[1]) {
			if time.Now().After(deadline) {
				t.Fatalf("diagnostics lack a line %q…%q:\n%s", want[0], want[1], diag.String())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if err := p.Drain(context.Background()); err != nil || !p.Bye() || p.ExitStatus().Code != 0 {
		t.Fatalf("drain %v, bye %v, status %v", err, p.Bye(), p.ExitStatus())
	}
	if _, err := os.Stat(p.Dir()); !os.IsNotExist(err) {
		t.Fatalf("working directory survives the process: %v", err)
	}
}

func hasLine(text, prefix, sub string) bool {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, prefix) && strings.Contains(line, sub) {
			return true
		}
	}
	return false
}

func sameDir(t *testing.T, a, b string) bool {
	sa, err1 := os.Stat(a)
	sb, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(sa, sb)
}

// Case watchdog/finite-cpu of ADR 0004, warm mode, which C3b gates: a step
// that computes for 2 s with killAfter 1 s gets its process killed after
// about 1 s; it and a co-resident invocation awaiting a timer fail as
// escaped by SIGKILL; the next invocation runs on a replacement. With the
// watchdog off the step completes.
func TestBunWatchdogFiniteCPU(t *testing.T) {
	t.Run("killAfter 1s", func(t *testing.T) {
		cfg, diag := bunConfig(t)
		cfg.KillAfter = time.Second
		pool := newPool(t, PoolConfig{Process: cfg})
		first := pidOf(t, pool, "warm-up")
		type res struct {
			out engine.StepOutcome
			err error
		}
		results := make(chan res, 2)
		start := time.Now()
		for _, inv := range []struct{ script, args string }{{"sleep", `{"ms":5000}`}, {"spin", `{"ms":2000}`}} {
			go func() {
				out, err := execute(t, pool, newInvocation(inv.script, inv.args, newRecorder()), 10*time.Second)
				results <- res{out, err}
			}()
			time.Sleep(50 * time.Millisecond)
		}
		for range 2 {
			r := <-results
			if r.err != nil || r.out.Kind != engine.OutcomeEscaped || r.out.Signal != "SIGKILL" {
				t.Fatalf("got %+v, %v; want escaped by SIGKILL", r.out, r.err)
			}
		}
		// Silence starts at the last frame before the spin, which can be a
		// heartbeat up to a quarter of killAfter earlier.
		if d := time.Since(start); d < 700*time.Millisecond || d > 2500*time.Millisecond {
			t.Fatalf("killed after %s, want about 1s", d)
		}
		if !strings.Contains(diag.String(), "watchdog: no frame for") {
			t.Fatalf("no watchdog diagnostic:\n%s", diag.String())
		}
		if next := pidOf(t, pool, "after"); next == first {
			t.Fatalf("the next invocation ran in the killed process %d", first)
		}
	})
	t.Run("watchdog off", func(t *testing.T) {
		cfg, _ := bunConfig(t)
		cfg.KillAfter = WatchdogOff
		cfg.Heartbeat = 100 * time.Millisecond
		pool := newPool(t, PoolConfig{Process: cfg})
		out, err := execute(t, pool, newInvocation("spin", `{"ms":2000}`, newRecorder()), 10*time.Second)
		if err != nil || out.Kind != engine.OutcomeOK || string(out.Body.Producer()) != "2000" {
			t.Fatalf("got %+v, %v", out, err)
		}
	})
}

func newPool(t *testing.T, cfg PoolConfig) *Pool {
	t.Helper()
	pool, err := NewPool(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		pool.Close(ctx)
	})
	return pool
}

// pidOf runs the process-info step for run and returns the pid it ran in.
func pidOf(t *testing.T, x engine.StepExecutor, run string) int {
	t.Helper()
	inv := newInvocation("process-info", `{}`, newRecorder())
	inv.Run = run
	out, err := execute(t, x, inv, 10*time.Second)
	var info struct{ PID int }
	if err != nil || out.Kind != engine.OutcomeOK || json.Unmarshal(out.Body.Producer(), &info) != nil {
		t.Fatalf("process-info: %+v, %v", out, err)
	}
	return info.PID
}

// gate is an invocation of the gate fixture step, which creates the file
// started and then runs until the test opens the gate, whatever happens to
// the invocation meanwhile.
type gate struct{ started, release string }

// newGate returns a gate with its files in dir, which it opens when the
// test ends. dir must be made before the pool, so that it outlives the
// pool: a step still held at a failure then ends before the pool closes.
func newGate(t *testing.T, dir, name string) gate {
	g := gate{started: filepath.Join(dir, name+".started"), release: filepath.Join(dir, name+".release")}
	t.Cleanup(func() { os.WriteFile(g.release, nil, 0o600) })
	return g
}

func (g gate) invocation(rep engine.Reporter) engine.Invocation { return g.step("gate", rep) }

// spinInvocation is an invocation of the spin-gate step, which blocks its
// process's event loop until the gate opens.
func (g gate) spinInvocation(rep engine.Reporter) engine.Invocation { return g.step("spin-gate", rep) }

func (g gate) step(script string, rep engine.Reporter) engine.Invocation {
	args, _ := json.Marshal(map[string]string{"started": g.started, "release": g.release})
	return newInvocation(script, string(args), rep)
}

// cancelOnceStarted cancels with cause once the step has started, so the
// invocation is abandoned only once it runs in the harness; if it does not
// start, it cancels with an error that says so.
func (g gate) cancelOnceStarted(cancel context.CancelCauseFunc, cause error) {
	go func() {
		if g.startedWithin(10 * time.Second) {
			cancel(cause)
		} else {
			cancel(errors.New("the step to abandon did not start"))
		}
	}()
}

// startedWithin reports whether the step has started, waiting up to d.
func (g gate) startedWithin(d time.Duration) bool {
	for deadline := time.Now().Add(d); ; time.Sleep(2 * time.Millisecond) {
		if _, err := os.Stat(g.started); err == nil {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
	}
}

func (g gate) open(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(g.release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// statsRecorder is a recorder that also takes the pool's stats when the
// invocation settles, from inside the Reporter call.
type statsRecorder struct {
	*recorder
	pool *Pool
	// atSettled is written before Settled is recorded, so it is safe to read
	// after waitSettled.
	atSettled []ProcessStats
}

func (r *statsRecorder) Settled(k engine.SettleKind) {
	r.atSettled = r.pool.Stats()
	r.recorder.Settled(k)
}

// ADR 0004 §Cancellation item 4: an abandoned invocation taints its
// process, which gets no new invocations; with nothing else in flight it
// is drained and replaced, which settles the abandoned call. The call is a
// gate that is never opened, so only the recycle can end it.
func TestBunPoolRecyclesATaintedProcess(t *testing.T) {
	gates := t.TempDir()
	cfg, _ := bunConfig(t)
	pool := newPool(t, PoolConfig{Process: cfg})
	first := pidOf(t, pool, "r")
	held := newGate(t, gates, "held")
	rec := newRecorder()
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	held.cancelOnceStarted(cancel, engine.CancelTimeout)
	inv := held.invocation(rec)
	inv.Run = "r"
	if _, err := pool.Execute(ctx, inv); !errors.Is(err, engine.CancelTimeout) {
		t.Fatalf("got %v", err)
	}
	if k := rec.waitSettled(t, 10*time.Second); k != engine.SettleProcessExited {
		t.Fatalf("settled %s; the recycle should end the call", k)
	}
	if next := pidOf(t, pool, "r"); next == first {
		t.Fatalf("the tainted process %d got a new invocation", first)
	}
}

// A tainted process with other work in flight keeps it and gets no new
// invocations (a new process takes them). Once its abandoned invocation
// settles it returns to service when the pool has room below Max, and
// leaves once idle when it has not. The pool applies the settlement before
// the invocation's Reporter hears of it. Gate steps report that they have
// started and end when the test opens them, and lag routing is off, so no
// check depends on timing.
func TestBunPoolTaintedProcessReturnsToService(t *testing.T) {
	for _, tc := range []struct {
		max  int
		back bool
	}{{2, true}, {1, false}} {
		t.Run(fmt.Sprintf("max %d", tc.max), func(t *testing.T) {
			gates := t.TempDir()
			cfg, _ := bunConfig(t)
			pool := newPool(t, PoolConfig{Process: cfg, Max: tc.max, LagThreshold: -1})
			first := pidOf(t, pool, "a")
			long := newGate(t, gates, "long")
			longOut := make(chan engine.StepOutcome, 1)
			go func() {
				out, _ := execute(t, pool, long.invocation(newRecorder()), 30*time.Second)
				longOut <- out
			}()
			if !long.startedWithin(10 * time.Second) {
				t.Fatal("the co-resident step did not start")
			}
			held := newGate(t, gates, "held")
			rec := &statsRecorder{recorder: newRecorder(), pool: pool}
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			held.cancelOnceStarted(cancel, engine.CancelTimeout)
			if _, err := pool.Execute(ctx, held.invocation(rec)); !errors.Is(err, engine.CancelTimeout) {
				t.Fatalf("got %v", err)
			}
			if other := pidOf(t, pool, "b"); other == first {
				t.Fatalf("a new invocation went to the tainted process %d", first)
			}
			held.open(t)
			if k := rec.waitSettled(t, 10*time.Second); k != engine.SettleCompleted {
				t.Fatalf("settled %s", k)
			}
			var st *ProcessStats
			for _, s := range rec.atSettled {
				if s.PID == first {
					st = &s
				}
			}
			if st == nil || st.Tainted || st.Abandoned != 0 || st.Retiring || st.Surplus != !tc.back {
				t.Fatalf("process %d when its abandoned invocation settled: %+v", first, rec.atSettled)
			}
			// Routing agrees: run a goes back to its process only if that
			// process returned to service.
			if again := pidOf(t, pool, "a"); (again == first) != tc.back {
				t.Fatalf("run a went to process %d; the formerly tainted one is %d", again, first)
			}
			long.open(t)
			if out := <-longOut; out.Kind != engine.OutcomeOK {
				t.Fatalf("the co-resident invocation got %+v", out)
			}
			if tc.back {
				return
			}
			// The surplus process leaves once idle; the pool is back to Max.
			deadline := time.Now().Add(5 * time.Second)
			for len(pool.Stats()) != 1 {
				if time.Now().After(deadline) {
					t.Fatalf("still %+v", pool.Stats())
				}
				time.Sleep(20 * time.Millisecond)
			}
			if pool.Stats()[0].PID == first {
				t.Fatal("the surplus process stayed and the other left")
			}
		})
	}
}

func TestBunPoolReplacesADeadProcess(t *testing.T) {
	cfg, _ := bunConfig(t)
	pool := newPool(t, PoolConfig{Process: cfg})
	first := pidOf(t, pool, "r")
	out, err := execute(t, pool, newInvocation("die", `{"code":3}`, newRecorder()), 5*time.Second)
	if err != nil || out.Kind != engine.OutcomeEscaped || out.ExitCode != 3 {
		t.Fatalf("got %+v, %v", out, err)
	}
	if next := pidOf(t, pool, "r"); next == first {
		t.Fatal("ran in the dead process")
	}
	if n := len(pool.Stats()); n != 1 {
		t.Fatalf("%d processes, want 1", n)
	}
}

// A CPU-bound step makes its process lag; with room below Max the pool
// starts another process for the next invocation. The step spins until the
// test opens its gate, and the test waits for the lag to show, so nothing
// depends on how long either takes. (Should the idle process stall before
// the spin is routed, the pool scales out for the spin itself; the test
// follows the process the spin runs in.)
func TestBunPoolScalesOutOnLag(t *testing.T) {
	gates := t.TempDir()
	cfg, _ := bunConfig(t)
	cfg.KillAfter = WatchdogOff
	cfg.Heartbeat = 50 * time.Millisecond
	pool := newPool(t, PoolConfig{Process: cfg, Max: 2, LagThreshold: 150 * time.Millisecond})
	pidOf(t, pool, "r1")
	spin := newGate(t, gates, "spin")
	spun := make(chan engine.StepOutcome, 1)
	go func() {
		out, _ := execute(t, pool, spin.spinInvocation(newRecorder()), 30*time.Second)
		spun <- out
	}()
	if !spin.startedWithin(10 * time.Second) {
		t.Fatal("the spin did not start")
	}
	spinning := busyPID(t, pool)
	waitFor(t, "the spinning process to lag", func() bool {
		for _, s := range pool.Stats() {
			if s.PID == spinning {
				return s.Lag > 150*time.Millisecond
			}
		}
		return false
	})
	if second := pidOf(t, pool, "r2"); second == spinning {
		t.Fatal("the invocation went to the lagging process")
	}
	if n := len(pool.Stats()); n != 2 {
		t.Fatalf("%d processes, want 2", n)
	}
	// The spin cannot end before its gate opens, so the second invocation
	// did not wait for it.
	select {
	case out := <-spun:
		t.Fatalf("the spin ended before its gate opened: %+v", out)
	default:
	}
	spin.open(t)
	if out := <-spun; out.Kind != engine.OutcomeOK {
		t.Fatalf("the spin got %+v", out)
	}
}

// A run goes back to the process it used last, even when another one is
// less loaded. Gate steps keep a process busy until the test opens them,
// and lag routing is off, so no check depends on timing.
func TestBunPoolAffinity(t *testing.T) {
	gates := t.TempDir()
	cfg, _ := bunConfig(t)
	pool := newPool(t, PoolConfig{Process: cfg, Min: 2, Max: 2, LagThreshold: -1})
	if err := pool.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	stats := pool.Stats()
	if len(stats) != 2 {
		t.Fatalf("%d processes after Start, want 2", len(stats))
	}
	// hold runs a gate step for run and returns, once it runs, the gate,
	// the process it occupies, and a channel closed when it has ended.
	hold := func(name, run string) (gate, int, chan struct{}) {
		g := newGate(t, gates, name)
		inv := g.invocation(newRecorder())
		inv.Run = run
		done := make(chan struct{})
		go func() {
			execute(t, pool, inv, 30*time.Second)
			close(done)
		}()
		if !g.startedWithin(10 * time.Second) {
			t.Fatalf("the step of run %s did not start", run)
		}
		return g, busyPID(t, pool), done
	}
	// Occupy one process, so run r1 lands on the other.
	g, busy, d := hold("r0", "r0")
	p1 := pidOf(t, pool, "r1")
	if p1 == busy {
		t.Fatalf("run r1 went to the busy process %d", busy)
	}
	g.open(t)
	<-d
	// Now occupy r1's process: least-loaded would pick the other one, but
	// r1 goes back to its own.
	g, busy, d = hold("r1", "r1")
	if busy != p1 {
		t.Fatalf("run r1's step went to process %d, not to %d", busy, p1)
	}
	if again := pidOf(t, pool, "r1"); again != p1 {
		t.Fatalf("run r1 moved from %d to %d", p1, again)
	}
	g.open(t)
	<-d
}

// busyPID returns the pid of the one pool process with an invocation in
// flight.
func busyPID(t *testing.T, pool *Pool) int {
	t.Helper()
	pid := 0
	for _, s := range pool.Stats() {
		if s.Active == 0 {
			continue
		}
		if pid != 0 {
			t.Fatalf("more than one busy process: %+v", pool.Stats())
		}
		pid = s.PID
	}
	if pid == 0 {
		t.Fatalf("no busy process: %+v", pool.Stats())
	}
	return pid
}

// Deviation D15: a cap bounds the invocations in flight per process; the
// rest wait.
func TestBunPoolCap(t *testing.T) {
	cfg, _ := bunConfig(t)
	pool := newPool(t, PoolConfig{Process: cfg, Cap: 1})
	pidOf(t, pool, "warm-up")
	var wg sync.WaitGroup
	start := time.Now()
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			execute(t, pool, newInvocation("sleep", `{"ms":500}`, newRecorder()), 10*time.Second)
		}()
	}
	wg.Wait()
	if d := time.Since(start); d < time.Second {
		t.Fatalf("two capped invocations took %s, want them one after the other", d)
	}
	// Without the cap they run at once.
	free := newPool(t, PoolConfig{Process: cfg})
	pidOf(t, free, "warm-up")
	start = time.Now()
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			execute(t, free, newInvocation("sleep", `{"ms":500}`, newRecorder()), 10*time.Second)
		}()
	}
	wg.Wait()
	if d := time.Since(start); d > 950*time.Millisecond {
		t.Fatalf("two uncapped invocations took %s", d)
	}
}

func TestBunPoolClose(t *testing.T) {
	cfg, _ := bunConfig(t)
	pool, err := NewPool(PoolConfig{Process: cfg})
	if err != nil {
		t.Fatal(err)
	}
	pidOf(t, pool, "r")
	done := make(chan engine.StepOutcome, 1)
	go func() {
		out, _ := execute(t, pool, newInvocation("sleep", `{"ms":300}`, newRecorder()), 10*time.Second)
		done <- out
	}()
	time.Sleep(50 * time.Millisecond)
	if err := pool.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Close waited for the invocation, then drained the process.
	if out := <-done; out.Kind != engine.OutcomeOK {
		t.Fatalf("in-flight invocation got %+v", out)
	}
	// The drained process said bye and left the pool.
	deadline := time.Now().Add(2 * time.Second)
	for len(pool.Stats()) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("processes left: %+v", pool.Stats())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := execute(t, pool, newInvocation("echo", `{}`, newRecorder()), time.Second); !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("got %v", err)
	}
}

// A Reporter may run another step on the same pool when one settles, even
// with a cap of one: the finished invocation's slot is freed before its
// Reporter is told.
func TestBunPoolReporterRunsAStepUnderACap(t *testing.T) {
	cfg, _ := bunConfig(t)
	pool := newPool(t, PoolConfig{Process: cfg, Cap: 1})
	inner := make(chan error, 1)
	rep := &reentrantReporter{recorder: newRecorder(), call: func() {
		_, err := execute(t, pool, newInvocation("echo", `{}`, newRecorder()), 2*time.Second)
		inner <- err
	}}
	if out, err := execute(t, pool, newInvocation("echo", `{}`, rep), 5*time.Second); err != nil || out.Kind != engine.OutcomeOK {
		t.Fatalf("got %+v, %v", out, err)
	}
	if err := <-inner; err != nil {
		t.Fatalf("the step started from Settled: %v", err)
	}
}

// A tainted process whose abandoned invocation settles while its
// replacement is still starting does not take the pool past Max: the
// starting process counts, so the old one becomes surplus and leaves once
// idle, without losing work. The steps are gates, and so is every start
// after the first, so the settlement comes while the replacement starts
// whatever the timing.
func TestBunPoolTaintClearsWhileTheReplacementStarts(t *testing.T) {
	gates := t.TempDir()
	cfg, _ := bunConfig(t)
	// The first process starts at once; every later one waits for the gate
	// "start", through a wrapper that keeps the pid.
	cfg.Args = append([]string{"-c", `mkdir "$WL_GATES/first" 2>/dev/null || {
	: > "$WL_GATES/start.started"
	while [ ! -e "$WL_GATES/start.release" ]; do sleep 0.01; done
}
exec "$0" "$@"`, cfg.Path}, cfg.Args...)
	cfg.Path = "/bin/sh"
	cfg.Env = append(BaseEnv(), "WL_GATES="+gates)
	pool := newPool(t, PoolConfig{Process: cfg, Max: 1, LagThreshold: -1})
	// Made after the pool, so that a failing test opens it before the pool
	// closes.
	start := newGate(t, gates, "start")
	if err := pool.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := pool.Stats()[0].PID
	long := newGate(t, gates, "long")
	longOut := make(chan engine.StepOutcome, 1)
	go func() {
		out, _ := execute(t, pool, long.invocation(newRecorder()), 30*time.Second)
		longOut <- out
	}()
	if !long.startedWithin(10 * time.Second) {
		t.Fatal("the co-resident step did not start")
	}
	held := newGate(t, gates, "held")
	rec := &statsRecorder{recorder: newRecorder(), pool: pool}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	held.cancelOnceStarted(cancel, engine.CancelTimeout)
	if _, err := pool.Execute(ctx, held.invocation(rec)); !errors.Is(err, engine.CancelTimeout) {
		t.Fatalf("got %v", err)
	}
	if s := pool.Stats(); len(s) != 1 || !s[0].Tainted {
		t.Fatalf("after the abandonment: %+v", s)
	}
	// The next invocation needs a replacement, held at its start while the
	// abandoned call settles.
	next := make(chan engine.StepOutcome, 1)
	go func() {
		out, _ := execute(t, pool, newInvocation("process-info", `{}`, newRecorder()), 30*time.Second)
		next <- out
	}()
	if !start.startedWithin(10 * time.Second) {
		t.Fatal("no replacement started")
	}
	held.open(t)
	if k := rec.waitSettled(t, 10*time.Second); k != engine.SettleCompleted {
		t.Fatalf("settled %s", k)
	}
	if s := rec.atSettled; len(s) != 1 || s[0].PID != first || s[0].Tainted || !s[0].Surplus || s[0].Retiring || s[0].Active != 1 {
		t.Fatalf("when the abandoned invocation settled: %+v; want process %d surplus, with the co-resident step", s, first)
	}
	start.open(t)
	out := <-next
	var info struct{ PID int }
	if out.Kind != engine.OutcomeOK || json.Unmarshal(out.Body.Producer(), &info) != nil || info.PID == first {
		t.Fatalf("the next invocation got %+v; the surplus process is %d", out, first)
	}
	long.open(t)
	if out := <-longOut; out.Kind != engine.OutcomeOK {
		t.Fatalf("the co-resident invocation got %+v", out)
	}
	waitFor(t, "the surplus process to leave", func() bool {
		s := pool.Stats()
		return len(s) == 1 && s[0].PID == info.PID
	})
	// The replacement serves alone, and it stays that way.
	time.Sleep(300 * time.Millisecond)
	if s := pool.Stats(); len(s) != 1 || s[0].PID != info.PID || s[0].Tainted || s[0].Surplus || s[0].Retiring {
		t.Fatalf("Max 1, but the pool has %+v", s)
	}
}

// The first Execute establishes the pool's minimum, as Start does.
func TestBunPoolLazyMinimum(t *testing.T) {
	cfg, _ := bunConfig(t)
	pool := newPool(t, PoolConfig{Process: cfg, Min: 3, Max: 3})
	if out, err := execute(t, pool, newInvocation("echo", `{}`, newRecorder()), 10*time.Second); err != nil || out.Kind != engine.OutcomeOK {
		t.Fatalf("got %+v, %v", out, err)
	}
	waitFor(t, "three processes", func() bool { return len(pool.Stats()) == 3 })
}

// A result too large for a lowered message limit, for an invocation whose
// id is long, still ends the invocation: the escaped fallback that fits is
// sent, and Execute returns.
func TestBunOversizeResultFallbackFits(t *testing.T) {
	cfg, _ := bunConfig(t)
	cfg.MaxMessage = 512
	p := spawn(t, cfg)
	rec := newRecorder()
	inv := newInvocation("big", `{"bytes":1000}`, rec)
	inv.ID = strings.Repeat("i", 370)
	inv.Run, inv.Env = "r", nil
	out, err := execute(t, p, inv, 5*time.Second)
	if err != nil || out.Kind != engine.OutcomeEscaped || out.ExitCode != 1 {
		t.Fatalf("got %+v, %v", out, err)
	}
	if got := rec.log(); got != "settled completed" {
		t.Fatalf("reporter calls %q", got)
	}
	if h, err := p.Ping(context.Background()); err != nil || h.InFlight != 0 || h.Abandoned != 0 {
		t.Fatalf("health %+v, %v", h, err)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
