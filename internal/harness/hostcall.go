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

// HostCall asks the harness's host plugin to answer hook for run at site
// with payload, a JSON object, and returns its answer, a JSON object
// (ADR 0008, milestone F2c). It waits until the harness replies, ctx ends
// (it then returns ctx's cause, and a reply that comes later is dropped),
// or the connection does. A plugin's error reply is a *HostCallError.
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
		if _, ok := p.hostCalls[id]; ok {
			delete(p.hostCalls, id)
			// Its reply, if it still comes, is dropped.
			p.lateHostCalls.add(id)
		}
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
		forget()
		return jsonval.Value{}, context.Cause(ctx)
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
	if p.lateHostCalls.has(r.ID) {
		return nil
	}
	return protoErr(CodeUnexpected, "host_reply %q answers no host_call", r.ID)
}

// lateCalls remembers the most recent host_calls the engine stopped
// waiting for, whose replies are dropped, up to a bound.
type lateCalls struct {
	ids   map[string]struct{}
	order []string
}

// lateCallsKept bounds lateCalls.
const lateCallsKept = 256

func (l *lateCalls) add(id string) {
	if l.ids == nil {
		l.ids = map[string]struct{}{}
	}
	l.ids[id] = struct{}{}
	l.order = append(l.order, id)
	if len(l.order) > lateCallsKept {
		delete(l.ids, l.order[0])
		l.order = l.order[1:]
	}
}

func (l *lateCalls) has(id string) bool {
	_, ok := l.ids[id]
	return ok
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
