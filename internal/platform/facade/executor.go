package facade

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// Executor returns a step executor that runs each invocation on inner as
// the facade's: it registers the invocation before it runs, so its calls
// are attributed to it; when inner returns an outcome, it applies the 501
// policy; and when the invocation settles, it ends the registration, so a
// call that replays its token answers 401 (ADR 0007).
//
// The 501 policy (WA-41, policy case WA-41/b): in PolicyFail, an outcome
// of an invocation with any recorded unsupported call, whatever its kind,
// becomes the error
//
//	{"name":"UnsupportedPlatformCall","message":"Not implemented in windmill-lite: <first call>","calls":[<every call>, …]}
//
// with origin facade, which the engine stores as {"error": …}. The message
// is the first unsupported call's answer (Record.Message), which for a
// refused dispatch is "dispatch target not in bundle: <path>" (ADR 0001
// §5a). The calls
// counted are those accepted before the outcome: on the intercepted path
// every platform frame that arrived ahead of the invocation's result, and
// the answers of the calls still being answered are waited for, until the
// step's context ends. In PolicyObserve the outcome stands. An abandoned
// invocation has no outcome to replace.
func (f *Facade) Executor(inner engine.StepExecutor) engine.StepExecutor {
	return &executor{f: f, inner: inner}
}

type executor struct {
	f     *Facade
	inner engine.StepExecutor
}

func (x *executor) Execute(ctx context.Context, inv engine.Invocation) (engine.StepOutcome, error) {
	s := x.f.Register(inv.ID, inv.Run, inv.Env)
	// The record is released once the invocation has settled and this
	// call is done with it, in either order.
	var mu sync.Mutex
	settled, done := false, false
	release := func(settling bool) {
		mu.Lock()
		if settling {
			settled = true
		} else {
			done = true
		}
		both := settled && done
		mu.Unlock()
		if both {
			s.Release()
		}
	}
	report := inv.Report
	if report == nil {
		report = engine.NopReporter{}
	}
	inv.Report = &sessionReporter{Reporter: report, s: s, settled: func() { release(true) }}
	defer release(false)
	out, err := x.inner.Execute(ctx, inv)
	if err != nil || x.f.policy != PolicyFail {
		return out, err
	}
	if calls, message := s.unsupported(ctx); len(calls) > 0 {
		return unsupportedOutcome(message, calls), nil
	}
	return out, nil
}

// unsupportedOutcome is the 501 policy's error (ADR 0007).
func unsupportedOutcome(message string, calls []string) engine.StepOutcome {
	body, err := json.Marshal(struct {
		Name    string   `json:"name"`
		Message string   `json:"message"`
		Calls   []string `json:"calls"`
	}{"UnsupportedPlatformCall", message, calls})
	if err != nil {
		panic(err)
	}
	return engine.StepOutcome{Kind: engine.OutcomeError, Body: jsonval.FromProducer(body), Origin: engine.OriginFacade}
}

// sessionReporter ends the session's registration when the invocation
// settles, and releases its record if Execute is done with it, before
// passing Settled on: whoever hears Settled after Execute returned finds
// the record released.
type sessionReporter struct {
	engine.Reporter
	s       *Session
	settled func()
}

func (r *sessionReporter) Settled(k engine.SettleKind) {
	r.s.End()
	r.settled()
	r.Reporter.Settled(k)
}
