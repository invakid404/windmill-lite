package harness

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// HostCallError is the error of a host_call the host plugin answered with
// an error (ADR 0008, milestone F2c): its hook threw, answered something
// the hook does not answer (HostErrorPlugin), or is not declared
// (HostErrorUnknownHook).
type HostCallError struct {
	Hook    string
	Code    string
	Message string
}

func (e *HostCallError) Error() string {
	return fmt.Sprintf("%s: host plugin hook %s: %s", e.Code, e.Hook, e.Message)
}

// ErrNoPlugin is the error of a host_call to a harness that loaded no
// host plugin: the engine asks a plugin only for what it declared.
var ErrNoPlugin = errors.New("harness: the harness loaded no host plugin")

// LateHostCallError is the error of a host_call that the engine stopped
// waiting for when ctx ended, before its reply came; Err is ctx's cause.
// The hook may still be running in the harness, in the run's scope: Done
// closes once it has finished, when its reply comes (which is dropped), or
// once the harness has exited, so that the run settles only after it
// (ADR 0004 §Host plugins, ADR 0008 H9).
type LateHostCallError struct {
	Err  error
	Done <-chan struct{}
}

func (e *LateHostCallError) Error() string { return e.Err.Error() }

func (e *LateHostCallError) Unwrap() error { return e.Err }

// HostCall asks the harness's host plugin to answer hook for run at site
// with payload, a JSON object, and returns its answer, a JSON object
// (ADR 0008, milestone F2c). It waits until the harness replies, ctx ends
// (it then returns a *LateHostCallError with ctx's cause, and the reply
// that comes later is dropped), or the connection does. A plugin's error
// reply is a *HostCallError.
func (p *Process) HostCall(ctx context.Context, hook, run, site string, payload jsonval.Value) (jsonval.Value, error) {
	if p.Hello().Plugin.IsZero() {
		return jsonval.Value{}, ErrNoPlugin
	}
	reply := make(chan HostReply, 1)
	p.mu.Lock()
	if p.state != stateServing && p.state != stateDraining || p.ending {
		p.mu.Unlock()
		return jsonval.Value{}, errUnavailable
	}
	p.hostCallSeq++
	id := "hc" + strconv.FormatUint(p.hostCallSeq, 10)
	if p.hostCalls == nil {
		p.hostCalls = map[string]chan HostReply{}
	}
	p.hostCalls[id] = reply
	p.mu.Unlock()
	forget := func() {
		p.mu.Lock()
		delete(p.hostCalls, id)
		p.mu.Unlock()
	}
	if err := p.conn.send(HostCall{ID: id, Hook: hook, Run: run, Site: site, Payload: payload}); err != nil {
		forget()
		return jsonval.Value{}, fmt.Errorf("harness: host_call %s: %w", hook, err)
	}
	select {
	case r := <-reply:
		if r.Error != nil {
			return jsonval.Value{}, &HostCallError{Hook: hook, Code: r.Error.Code, Message: r.Error.Message}
		}
		return r.Payload, nil
	case <-ctx.Done():
		// The call is remembered until its reply comes, however many
		// others ended meanwhile, so that the reply is told apart from one
		// to no call; the harness always replies once the hook has run.
		replied := make(chan struct{})
		p.mu.Lock()
		if _, ok := p.hostCalls[id]; !ok {
			// The reply crossed ctx's end: the hook has finished.
			close(replied)
		} else {
			delete(p.hostCalls, id)
			if p.lateHostCalls == nil {
				p.lateHostCalls = map[string]chan struct{}{}
			}
			p.lateHostCalls[id] = replied
		}
		p.mu.Unlock()
		done := make(chan struct{})
		go func() {
			select {
			case <-replied:
			case <-p.exited:
			}
			close(done)
		}()
		return jsonval.Value{}, &LateHostCallError{Err: context.Cause(ctx), Done: done}
	case <-p.exited:
		forget()
		if p.host != nil {
			return jsonval.Value{}, ErrHostLost
		}
		return jsonval.Value{}, fmt.Errorf("harness: host_call %s: the harness exited (%s)", hook, p.ExitStatus())
	}
}

// onHostReply hands a host_reply to the HostCall that waits for it. It runs
// with p.mu held. A reply to a call the engine stopped waiting for is
// dropped; one to no call the engine made is a protocol error.
func (p *Process) onHostReply(r HostReply) error {
	if ch, ok := p.hostCalls[r.ID]; ok {
		delete(p.hostCalls, r.ID)
		ch <- r
		return nil
	}
	if done, ok := p.lateHostCalls[r.ID]; ok {
		delete(p.lateHostCalls, r.ID)
		close(done)
		return nil
	}
	return protoErr(CodeUnexpected, "host_reply %q answers no host_call", r.ID)
}

// HostCall asks the host plugin of one of the pool's processes, the run's
// own when it has one, to answer hook (Process.HostCall). The pool starts
// a process when none serves.
func (pl *Pool) HostCall(ctx context.Context, hook, run, site string, payload jsonval.Value) (jsonval.Value, error) {
	if pl.isFresh() {
		return jsonval.Value{}, errors.New("harness: a fresh pool has no process to ask a host plugin")
	}
	for {
		m, err := pl.acquire(ctx, run)
		if err != nil {
			return jsonval.Value{}, err
		}
		out, err := m.p.HostCall(ctx, hook, run, site, payload)
		unavailable := errors.Is(err, errUnavailable)
		pl.release(m, unavailable)
		if unavailable {
			continue
		}
		return out, err
	}
}

// Plugin returns the declaration of the host plugin the pool's processes
// loaded, as the first of them said in its hello, starting it if need be;
// zero when they loaded none.
func (pl *Pool) Plugin(ctx context.Context) (jsonval.Value, error) {
	if pl.isFresh() {
		return jsonval.Value{}, nil
	}
	m, err := pl.acquire(ctx, "")
	if err != nil {
		return jsonval.Value{}, err
	}
	defer pl.release(m, false)
	return m.p.Hello().Plugin, nil
}
