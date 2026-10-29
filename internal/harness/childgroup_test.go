package harness

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/engine"
)

// fakeProcs stands in for the kernel's process table in the child-group
// tests: each group's members, with their start times, the groups
// signalled, and the processes reaped. No real signal is sent and no real
// process is waited for.
type fakeProcs struct {
	mu     sync.Mutex
	groups map[int][]groupMember
	guards map[int]string // pid → the token as its sleep operand
	// scripts are the processes that carry a token in the prologue's
	// script, before their exec (pid → token); unread are those that run
	// and read no arguments, in the middle of an exec.
	scripts  map[int]string
	unread   map[int]bool
	signaled []int
	// parents are the processes whose parent is the engine (pid → true);
	// zombies are those that have exited, not yet reaped.
	parents, zombies map[int]bool
	reaped           []int
	// hold, when set, runs once, in the next listing of members, after the
	// listing is taken and before it is returned: it pauses whoever looks.
	hold func()
}

func useFakeProcs(t *testing.T) *fakeProcs {
	t.Helper()
	f := &fakeProcs{groups: map[int][]groupMember{}, guards: map[int]string{}, scripts: map[int]string{}, unread: map[int]bool{}, parents: map[int]bool{}, zombies: map[int]bool{}}
	// No reaping goroutine may read the process operations while they are
	// replaced, nor reap a fake group with the real ones afterwards.
	resetReaper()
	gm, ig, au, sg, rp, wp := groupMembers, isGuard, argsUnread, signalGroup, readProc, reapPID
	argsUnread = func(pid int) bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.unread[pid]
	}
	readProc = func(pid int) (procInfo, bool) {
		f.mu.Lock()
		defer f.mu.Unlock()
		for pgid, ms := range f.groups {
			for _, m := range ms {
				if m.pid == pid {
					info := procInfo{start: m.start, pgid: pgid, zombie: f.zombies[pid]}
					if f.parents[pid] {
						info.ppid = os.Getpid()
					}
					return info, true
				}
			}
		}
		return procInfo{}, false
	}
	reapPID = func(pid int) bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case !f.parents[pid]:
			return true // ECHILD: not the engine's child
		case !f.zombies[pid]:
			return false
		}
		f.reaped = append(f.reaped, pid)
		for pgid, ms := range f.groups {
			f.groups[pgid] = slices.DeleteFunc(ms, func(m groupMember) bool { return m.pid == pid })
		}
		return true
	}
	isGuard = func(pid int, token string, script bool) bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.guards[pid] == token || script && f.scripts[pid] == token
	}
	groupMembers = func(pgids []int) map[int][]groupMember {
		f.mu.Lock()
		out := map[int][]groupMember{}
		for _, g := range pgids {
			if ms := f.groups[g]; len(ms) > 0 {
				out[g] = slices.Clone(ms)
			}
		}
		hold := f.hold
		f.hold = nil
		f.mu.Unlock()
		if hold != nil {
			hold()
		}
		return out
	}
	signalGroup = func(pgid int) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.signaled = append(f.signaled, pgid)
	}
	t.Cleanup(func() {
		resetReaper()
		groupMembers, isGuard, argsUnread, signalGroup, readProc, reapPID = gm, ig, au, sg, rp, wp
	})
	return f
}

// resetReaper forgets the groups the reaper holds and waits for its
// goroutine to exit.
func resetReaper() {
	reaper.mu.Lock()
	reaper.sets = nil
	done := reaper.done
	reaper.mu.Unlock()
	if done != nil {
		<-done
	}
}

// adopt makes the engine the parent of pids.
func (f *fakeProcs) adopt(pids ...int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, pid := range pids {
		f.parents[pid] = true
	}
}

// exit makes pids zombies.
func (f *fakeProcs) exit(pids ...int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, pid := range pids {
		f.zombies[pid] = true
	}
}

func (f *fakeProcs) tookReaped() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.reaped
	f.reaped = nil
	slices.Sort(out)
	return out
}

// set makes the members of group pgid ms, each a pid and its start time.
func (f *fakeProcs) set(pgid int, ms ...groupMember) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.groups[pgid] = ms
}

func (f *fakeProcs) took() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.signaled
	f.signaled = nil
	return out
}

// The engine kills a child group only while it is the group the step's
// child started: while a member it last saw is still there, the same
// process. So what the leader started is killed after the leader exited,
// and a group that took the id after the original ended is never
// signalled, whether its own leader is alive or already gone (review
// round 2, finding 3; round 3, finding 1). Ids that cannot name a group
// are never signalled (round 2, finding 2).
func TestChildGroupIdentity(t *testing.T) {
	// spec: WR-55
	f := useFakeProcs(t)
	m := func(pid int, start uint64) groupMember { return groupMember{pid: pid, start: start} }
	kill := func(g *childGroup) []int {
		killChildGroups([]*childGroup{g})
		return f.took()
	}
	t.Run("the original, its leader alive", func(t *testing.T) {
		f.set(4242, m(4242, 1))
		g, ok := newChildGroup(4242, "")
		if !ok || !slices.Equal(kill(g), []int{4242}) {
			t.Fatal("the live original was not signalled")
		}
	})
	t.Run("the original's descendants, after the leader exited", func(t *testing.T) {
		f.set(4242, m(4242, 1))
		g, _ := newChildGroup(4242, "")
		f.set(4242, m(4242, 1), m(5000, 3)) // the leader started 5000
		retire(t, g)
		f.set(4242, m(5000, 3)) // the leader exited
		if got := kill(g); !slices.Equal(got, []int{4242}) {
			t.Fatalf("the descendants: signalled %v", got)
		}
	})
	t.Run("a group that ended", func(t *testing.T) {
		f.set(4242, m(4242, 1))
		g, _ := newChildGroup(4242, "")
		f.set(4242)
		if got := kill(g); len(got) != 0 {
			t.Fatalf("an ended group was signalled: %v", got)
		}
	})
	t.Run("a replacement with its leader alive", func(t *testing.T) {
		f.set(4242, m(4242, 1))
		g, _ := newChildGroup(4242, "")
		f.set(4242, m(4242, 2))
		if got := kill(g); len(got) != 0 {
			t.Fatalf("a replacement was signalled: %v", got)
		}
	})
	t.Run("a replacement whose leader already exited", func(t *testing.T) {
		// The review's sequence: the original (4242, started at 1) ends; a
		// new group takes the id (4242, started at 2) and its leader exits,
		// leaving a member of its own, all between two looks.
		f.set(4242, m(4242, 1))
		g, _ := newChildGroup(4242, "")
		f.set(4242, m(7777, 9))
		p := &Process{invs: map[string]*invocation{"i": {id: "i", children: []*childGroup{g}}}}
		p.retireChildren()
		killChildGroups(takeChildren(p.invs["i"]))
		if got := f.took(); len(got) != 0 {
			t.Fatalf("a leaderless replacement was signalled: %v", got)
		}
	})
	t.Run("once given up, for good", func(t *testing.T) {
		f.set(4242, m(4242, 1))
		g, _ := newChildGroup(4242, "")
		f.set(4242, m(8000, 5)) // everything it saw is gone
		kill(g)
		f.set(4242, m(4242, 1)) // even the old members back cannot revive it
		if got := kill(g); len(got) != 0 {
			t.Fatalf("a given-up group was signalled: %v", got)
		}
	})
	t.Run("a group that ended before the engine learned of it", func(t *testing.T) {
		f.set(300)
		if _, ok := newChildGroup(300, ""); ok {
			t.Fatal("an ended group was taken")
		}
	})
	t.Run("ids that cannot name a group", func(t *testing.T) {
		for _, pgid := range []int{0, 1, -1, 1 << 31, 1<<32 + 1, 1 << 32, 1<<32 + 4242} {
			f.set(pgid, m(pgid, 3))
			killChildGroups([]*childGroup{{pgid: pgid, seen: []groupMember{m(pgid, 3)}}})
		}
		if got := f.took(); len(got) != 0 {
			t.Fatalf("invalid ids were signalled: %v", got)
		}
	})
}

// A group started with its guardian stays the original while the guardian
// lives, whatever its other members do: what a child left when it exited
// is killed though the engine never saw it beside the child, a group with
// only its guardian left is released (dropped, then its guardian killed),
// and a group that took the id has no guardian, so it is never signalled
// (review round 3, finding 1).
func TestChildGroupGuardian(t *testing.T) {
	// spec: WR-55
	f := useFakeProcs(t)
	m := func(pid int, start uint64) groupMember { return groupMember{pid: pid, start: start} }
	f.guards[6000] = "tk"
	t.Run("a handoff the engine never saw", func(t *testing.T) {
		// The engine learned of the group before the shell forked its
		// guardian; the shell then started a daemon and exited.
		f.set(4242, m(4242, 1))
		g, _ := newChildGroup(4242, "tk")
		f.set(4242, m(6000, 2), m(5000, 3))
		killChildGroups([]*childGroup{g})
		if got := f.took(); !slices.Equal(got, []int{4242}) {
			t.Fatalf("signalled %v", got)
		}
	})
	t.Run("only the guardian left: released", func(t *testing.T) {
		f.set(4242, m(4242, 1), m(6000, 2))
		g, _ := newChildGroup(4242, "tk")
		inv := &invocation{id: "i", children: []*childGroup{g}}
		p := &Process{invs: map[string]*invocation{"i": inv}}
		f.set(4242, m(6000, 2))
		p.retireChildren()
		if got := f.took(); !slices.Equal(got, []int{4242}) || len(inv.children) != 0 {
			t.Fatalf("signalled %v, held %d", got, len(inv.children))
		}
		killChildGroups(takeChildren(inv))
		if got := f.took(); len(got) != 0 {
			t.Fatalf("a released group was signalled again: %v", got)
		}
	})
	t.Run("a replacement has no guardian", func(t *testing.T) {
		f.set(4242, m(4242, 1))
		g, _ := newChildGroup(4242, "tk")
		f.set(4242, m(4242, 9), m(7777, 10))
		killChildGroups([]*childGroup{g})
		if got := f.took(); len(got) != 0 {
			t.Fatalf("a replacement was signalled: %v", got)
		}
	})
	t.Run("a guardian killed: the members carry on", func(t *testing.T) {
		f.set(4242, m(4242, 1), m(6000, 2), m(5000, 3))
		g, _ := newChildGroup(4242, "tk")
		f.set(4242, m(5000, 3))
		killChildGroups([]*childGroup{g})
		if got := f.took(); !slices.Equal(got, []int{4242}) {
			t.Fatalf("signalled %v", got)
		}
	})
	// The leader carries the prologue's script, and so the token, until it
	// execs the step's command: alone in its group, it is not a guardian
	// left alone, and is not released (review round 5's CI).
	t.Run("the leader before its exec", func(t *testing.T) {
		f.scripts[4242] = "tk"
		defer delete(f.scripts, 4242)
		f.set(4242, m(4242, 1))
		g, _ := newChildGroup(4242, "tk")
		inv := &invocation{id: "i", children: []*childGroup{g}}
		p := &Process{invs: map[string]*invocation{"i": inv}}
		p.retireChildren()
		if got := f.took(); len(got) != 0 || len(inv.children) != 1 {
			t.Fatalf("signalled %v, held %d", got, len(inv.children))
		}
	})
	// The first listing started before the prologue forked anything and
	// read the leader after it exited, so it shows no member; the second
	// shows the guardian and what the command left (review round 5's CI).
	t.Run("a listing that missed the prologue", func(t *testing.T) {
		f.set(4242)
		f.mu.Lock()
		f.hold = func() { f.set(4242, m(6000, 2), m(5000, 3)) }
		f.mu.Unlock()
		g, ok := newChildGroup(4242, "tk")
		if !ok {
			t.Fatal("the group was dropped")
		}
		killChildGroups([]*childGroup{g})
		if got := f.took(); !slices.Equal(got, []int{4242}) {
			t.Fatalf("signalled %v", got)
		}
	})
	// The engine first finds the prologue's subshell, which carries the
	// token; it exits, and so does the step's command, at once, before the
	// engine saw the guardian: the guardian, which carries the token from
	// its fork, keeps the group the original (review round 5's CI).
	t.Run("a copy of the prologue, then the guardian", func(t *testing.T) {
		f.scripts[6001] = "tk"
		defer delete(f.scripts, 6001)
		f.set(4242, m(4242, 1))
		g, _ := newChildGroup(4242, "tk")
		inv := &invocation{id: "i", children: []*childGroup{g}}
		p := &Process{invs: map[string]*invocation{"i": inv}}
		f.set(4242, m(4242, 1), m(6001, 2))
		p.retireChildren()
		f.set(4242, m(6000, 3), m(5000, 4))
		p.mu.Lock()
		gs := takeChildren(inv)
		p.mu.Unlock()
		killChildGroups(gs)
		if got := f.took(); !slices.Equal(got, []int{4242}) {
			t.Fatalf("signalled %v", got)
		}
	})
}

// A look that finds neither the guardian nor a member it saw before, in a
// group started with a guardian, while a member other than the leader runs
// and reads no arguments, as in its exec, is undecided: the watcher keeps
// the group as it was, and a kill looks again a few milliseconds later,
// for a while, before it gives the group up. A group started without a
// guardian is never undecided (review round 5's CI: a guardian whose exec
// of sleep the look caught midway).
func TestChildGroupUndecided(t *testing.T) {
	// spec: WR-55
	f := useFakeProcs(t)
	m := func(pid int, start uint64) groupMember { return groupMember{pid: pid, start: start} }
	tries, retry := endTries, endRetry
	endTries, endRetry = 5, time.Millisecond
	t.Cleanup(func() { endTries, endRetry = tries, retry })
	// execing makes 6000 the guardian midway through its exec, running
	// with no arguments, or, done, with its sleep operand.
	execing := func(done bool) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.unread[6000] = !done
		if done {
			f.guards[6000] = "tk"
		} else {
			delete(f.guards, 6000)
		}
	}
	holding := func(token string) (*Process, *invocation) {
		f.set(4242, m(4242, 1))
		g, _ := newChildGroup(4242, token)
		inv := &invocation{id: "i", children: []*childGroup{g}}
		f.set(4242, m(6000, 2), m(5000, 3)) // the command exited
		return &Process{invs: map[string]*invocation{"i": inv}}, inv
	}
	t.Run("the watcher keeps it", func(t *testing.T) {
		execing(false)
		p, inv := holding("tk")
		p.retireChildren()
		if len(inv.children) != 1 || len(f.took()) != 0 {
			t.Fatalf("held %d", len(inv.children))
		}
		execing(true)
		killChildGroups(takeChildren(inv))
		if got := f.took(); !slices.Equal(got, []int{4242}) {
			t.Fatalf("signalled %v", got)
		}
	})
	t.Run("a kill waits for the exec", func(t *testing.T) {
		execing(false)
		_, inv := holding("tk")
		// The exec ends after the kill's first look, by its second.
		f.mu.Lock()
		f.hold = func() {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.hold = func() { execing(true) }
		}
		f.mu.Unlock()
		killChildGroups(takeChildren(inv))
		if got := f.took(); !slices.Equal(got, []int{4242}) {
			t.Fatalf("signalled %v", got)
		}
	})
	t.Run("undecided for too long", func(t *testing.T) {
		execing(false)
		_, inv := holding("tk")
		killChildGroups(takeChildren(inv))
		if got := f.took(); len(got) != 0 {
			t.Fatalf("signalled %v", got)
		}
	})
	t.Run("without a guardian", func(t *testing.T) {
		execing(false)
		p, inv := holding("")
		p.retireChildren()
		if len(inv.children) != 0 {
			t.Fatal("a group without a guardian was left undecided")
		}
	})
	execing(true)
}

// The watcher's release of a group with only its guardian left and a
// cancellation of the same group kill it once, whichever comes first, and
// the release never acts on a look that is out of date (review round 4,
// finding 1):
//
//   - the watcher looks and finds only the guardian, then a cancellation
//     kills the group, which ends the guardian that kept the id reserved,
//     and a replacement takes the id; the watcher, resuming with its look
//     from before, signals nothing;
//   - the same, with the group's owner changing between the look and the
//     release without any kill of the engine's (the guardian killed from
//     outside): the release looks again and signals nothing;
//   - a cancellation that took the group before the watcher released it
//     signals nothing afterwards.
func TestChildGroupEndsOnce(t *testing.T) {
	// spec: WR-55
	f := useFakeProcs(t)
	m := func(pid int, start uint64) groupMember { return groupMember{pid: pid, start: start} }
	f.guards[6000] = "tk"
	// guarded holds a group whose command has exited, so that only its
	// guardian is left, in an invocation of p.
	guarded := func() (*Process, *invocation) {
		f.set(4242, m(4242, 1), m(6000, 2))
		g, _ := newChildGroup(4242, "tk")
		inv := &invocation{id: "i", children: []*childGroup{g}}
		f.set(4242, m(6000, 2))
		return &Process{invs: map[string]*invocation{"i": inv}}, inv
	}
	// pausedRetire starts the watcher's look and returns once it has its
	// listing; resume lets it go on, and returns once it is done.
	pausedRetire := func(p *Process) (resume func()) {
		looked, goOn, retired := make(chan struct{}), make(chan struct{}), make(chan struct{})
		f.mu.Lock()
		f.hold = func() { close(looked); <-goOn }
		f.mu.Unlock()
		go func() {
			defer close(retired)
			p.retireChildren()
		}()
		<-looked
		return func() { close(goOn); <-retired }
	}
	take := func(p *Process, inv *invocation) []*childGroup {
		p.mu.Lock()
		defer p.mu.Unlock()
		return takeChildren(inv)
	}
	t.Run("a cancellation overtakes the release", func(t *testing.T) {
		p, inv := guarded()
		resume := pausedRetire(p)
		killChildGroups(take(p, inv))
		if got := f.took(); !slices.Equal(got, []int{4242}) {
			t.Fatalf("the cancellation signalled %v", got)
		}
		f.set(4242, m(4242, 9), m(7777, 10)) // the id's new owner
		resume()
		if got := f.took(); len(got) != 0 {
			t.Fatalf("the release signalled the replacement: %v", got)
		}
	})
	t.Run("the owner changes under the release's look", func(t *testing.T) {
		p, inv := guarded()
		resume := pausedRetire(p)
		f.set(4242, m(4242, 9), m(7777, 10))
		resume()
		killChildGroups(take(p, inv))
		if got := f.took(); len(got) != 0 {
			t.Fatalf("the replacement was signalled: %v", got)
		}
	})
	t.Run("a cancellation after the release", func(t *testing.T) {
		p, inv := guarded()
		gs := take(p, inv)
		p.invs["i"].children = slices.Clone(gs) // the watcher still holds it
		p.retireChildren()
		killChildGroups(gs)
		if got := f.took(); !slices.Equal(got, []int{4242}) {
			t.Fatalf("signalled %v, want the release's kill alone", got)
		}
	})
}

// The engine reaps the members of the groups it owns that it is the parent
// of, as when it runs as PID 1 or a subreaper and adopted them, each by its
// own pid and only while it is still the process the engine saw: a
// released guardian, what a cancellation killed, a process forked as the
// kill landed, and a descendant that exits while its group still runs.
// It leaves another's child to its parent, never waits for a pid another
// process took, does not trust a listing once no member it holds is left
// in the group, and never waits for a harness's own pid (review round 5,
// finding 1).
func TestReapChildGroups(t *testing.T) {
	// spec: WR-55
	f := useFakeProcs(t)
	m := func(pid int, start uint64) groupMember { return groupMember{pid: pid, start: start} }
	f.guards[6000] = "tk"
	held := func() int {
		reaper.mu.Lock()
		defer reaper.mu.Unlock()
		return len(reaper.sets)
	}
	// Each case starts with no process adopted, exited or reaped.
	clean := func() {
		resetReaper()
		f.mu.Lock()
		defer f.mu.Unlock()
		f.parents, f.zombies, f.reaped, f.signaled = map[int]bool{}, map[int]bool{}, nil, nil
	}
	t.Run("a released guardian", func(t *testing.T) {
		clean()
		f.set(4242, m(4242, 1), m(6000, 2))
		f.adopt(6000)
		g, _ := newChildGroup(4242, "tk")
		inv := &invocation{id: "i", children: []*childGroup{g}}
		p := &Process{invs: map[string]*invocation{"i": inv}}
		f.set(4242, m(6000, 2))
		p.retireChildren()
		if got := f.took(); !slices.Equal(got, []int{4242}) {
			t.Fatalf("signalled %v", got)
		}
		f.exit(6000)
		p.retireChildren()
		if got := f.tookReaped(); !slices.Equal(got, []int{6000}) || held() != 0 {
			t.Fatalf("reaped %v, %d groups left", got, held())
		}
	})
	t.Run("a cancellation's kill", func(t *testing.T) {
		clean()
		f.set(4242, m(4242, 1), m(6000, 2), m(5000, 3))
		f.adopt(6000, 5000) // 4242 is the harness's child
		g, _ := newChildGroup(4242, "tk")
		killChildGroups([]*childGroup{g})
		f.took()
		f.exit(4242, 6000, 5000)
		reapPass()
		if got := f.tookReaped(); !slices.Equal(got, []int{5000, 6000}) || held() != 1 {
			t.Fatalf("reaped %v, %d groups left", got, held())
		}
		f.set(4242) // the harness reaped its child
		reapPass()
		if got := f.tookReaped(); len(got) != 0 || held() != 0 {
			t.Fatalf("reaped %v, %d groups left", got, held())
		}
	})
	t.Run("a pid another process took", func(t *testing.T) {
		clean()
		f.set(4242, m(6000, 2))
		f.adopt(6000)
		reapLater(4242, 0, []groupMember{m(6000, 2)})
		f.set(4242, m(6000, 99)) // reaped elsewhere, and its pid reused
		f.exit(6000)
		reapPass()
		if got := f.tookReaped(); len(got) != 0 || held() != 0 {
			t.Fatalf("reaped %v, %d groups left", got, held())
		}
	})
	t.Run("a process forked as the kill landed", func(t *testing.T) {
		clean()
		f.set(4242, m(6000, 2))
		reapLater(4242, 0, []groupMember{m(6000, 2)})
		f.set(4242, m(6000, 2), m(5001, 4))
		f.adopt(6000, 5001)
		f.exit(6000, 5001)
		reapPass()
		if got := f.tookReaped(); !slices.Equal(got, []int{5001, 6000}) || held() != 0 {
			t.Fatalf("reaped %v, %d groups left", got, held())
		}
	})
	t.Run("no member held is left: the listing is not trusted", func(t *testing.T) {
		clean()
		f.set(4242, m(6000, 2))
		reapLater(4242, 0, []groupMember{m(6000, 2)})
		f.set(4242, m(4242, 50)) // the group ended and its id was reused
		f.adopt(4242)
		f.exit(4242)
		reapPass()
		if got := f.tookReaped(); len(got) != 0 || held() != 0 {
			t.Fatalf("reaped %v, %d groups left", got, held())
		}
	})
	t.Run("a descendant that exits while its group runs", func(t *testing.T) {
		clean()
		f.set(4242, m(4242, 1), m(6000, 2))
		f.adopt(6000)
		g, _ := newChildGroup(4242, "tk")
		inv := &invocation{id: "i", children: []*childGroup{g}}
		p := &Process{invs: map[string]*invocation{"i": inv}}
		f.set(4242, m(4242, 1), m(6000, 2), m(5002, 5))
		f.adopt(5002)
		p.retireChildren() // seen running
		f.set(4242, m(4242, 1), m(6000, 2), m(5002, 5), m(5003, 6))
		f.adopt(5003)
		f.exit(5002, 5003)
		p.retireChildren()
		if got := f.tookReaped(); !slices.Equal(got, []int{5002}) {
			t.Fatalf("reaped %v, want the one seen at two looks", got)
		}
		p.retireChildren()
		if got := f.tookReaped(); !slices.Equal(got, []int{5003}) || len(inv.children) != 1 || len(f.took()) != 0 {
			t.Fatalf("reaped %v, held %d", got, len(inv.children))
		}
	})
	t.Run("a harness's own pid", func(t *testing.T) {
		clean()
		f.set(7000, m(7000, 1), m(7001, 2))
		f.adopt(7000, 7001)
		f.exit(7000, 7001)
		reapLater(7000, 7000, []groupMember{m(7000, 1), m(7001, 2)})
		reapPass()
		if got := f.tookReaped(); !slices.Equal(got, []int{7001}) || held() != 0 {
			t.Fatalf("reaped %v, %d groups left", got, held())
		}
	})
}

// A guardian the engine is the parent of, as when it adopted the guardian
// as PID 1 or a subreaper, is reaped once its group is retired: here a real
// guardian, the test's own child in a group of its own, released by the
// engine's watcher, leaves the process table without the test waiting for
// it (review round 5, finding 1: the reviewer's probe).
func TestReviewOwnedGuardianReaped(t *testing.T) {
	// spec: WR-55
	resetReaper()
	const token = "123451234512345"
	cmd := exec.Command("sleep", guardArg(token))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		syscall.Kill(-pid, syscall.SIGKILL)
		var ws syscall.WaitStatus
		syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
	})
	waitFor(t, "the guardian's token", func() bool { return processHasGuard(pid, token, false) })
	g, ok := newChildGroup(pid, token)
	if !ok {
		t.Fatal("no group")
	}
	inv := &invocation{id: "i", children: []*childGroup{g}}
	p := &Process{invs: map[string]*invocation{"i": inv}}
	p.retireChildren()
	if len(inv.children) != 0 {
		t.Fatal("the guardian-only group was not released")
	}
	// A zombie is still listed in its group; only a wait removes it, and
	// the test makes none.
	waitFor(t, "the engine to reap the retired guardian", func() bool {
		p.retireChildren()
		return len(groupMembersOf([]int{pid})[pid]) == 0
	})
	var ws syscall.WaitStatus
	if got, err := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil); !errors.Is(err, syscall.ECHILD) {
		t.Fatalf("the retired guardian %d was still an unreaped child: wait4 gave %d, %v", pid, got, err)
	}
}

// retire runs one look of the engine's watcher over g alone.
func retire(t *testing.T, g *childGroup) {
	t.Helper()
	p := &Process{invs: map[string]*invocation{"i": {id: "i", children: []*childGroup{g}}}}
	p.retireChildren()
}

// The engine retires the child groups that no longer continue: one that
// ended, and one whose id a later group took, so that cancelling the
// invocation afterwards cannot signal the id's new owner; a group that
// outlived its leader is kept, and its members renewed (review round 2,
// finding 3).
func TestRetireChildren(t *testing.T) {
	// spec: WR-55
	f := useFakeProcs(t)
	m := func(pid int, start uint64) groupMember { return groupMember{pid: pid, start: start} }
	f.set(10, m(10, 1))
	f.set(11, m(11, 2))
	f.set(12, m(12, 3))
	f.set(13, m(13, 4), m(14, 5))
	inv := &invocation{id: "i"}
	for _, pid := range []int{10, 11, 12, 13} {
		g, _ := newChildGroup(pid, "")
		inv.children = append(inv.children, g)
	}
	p := &Process{invs: map[string]*invocation{"i": inv}}
	f.set(11)           // ended
	f.set(12, m(12, 8)) // replaced
	f.set(13, m(14, 5)) // its leader exited, a descendant stays
	p.retireChildren()
	var kept []int
	for _, g := range inv.children {
		kept = append(kept, g.pgid)
	}
	if !slices.Equal(kept, []int{10, 13}) {
		t.Fatalf("kept %v, want the live original and the leaderless one", kept)
	}
	killChildGroups(takeChildren(inv))
	if got := f.took(); !slices.Equal(got, []int{10, 13}) {
		t.Fatalf("the cancel signalled %v", got)
	}
}

// processHasGuard finds a guardian's token in a real sleeping process's
// arguments, as the harness starts it, and no other token. On Linux the
// arguments can still read empty just after Start returns, until the exec
// has set them up, so the test waits for them, as the engine looks again
// at each look (findGuard).
func TestGuardianToken(t *testing.T) {
	cmd := exec.Command("sleep", guardArg("012345678901234"))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	pid := cmd.Process.Pid
	waitFor(t, "the guardian's token", func() bool { return processHasGuard(pid, "012345678901234", false) })
	if processHasGuard(pid, "01234567890123", true) || processHasGuard(os.Getpid(), "012345678901234", true) {
		t.Fatal("the guardian's token is not told apart")
	}
	// Before its exec, a process of the prologue carries the token as a
	// word of the script (hasGuardArg): here a shell that keeps running
	// with such a script.
	sh := exec.Command("/bin/sh", "-c", "sleep 5; : sleep "+guardArg("543210987654321")+" &")
	if err := sh.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sh.Process.Kill(); sh.Wait() })
	waitFor(t, "the script's token", func() bool { return processHasGuard(sh.Process.Pid, "543210987654321", true) })
	if processHasGuard(sh.Process.Pid, "543210987654321", false) || processHasGuard(sh.Process.Pid, "54321098765432", true) {
		t.Fatal("the script's token is not told apart")
	}
}

// hasGuardArg finds the token as a whole argument, or, with script, as a
// whole word of one too, and nowhere else.
func TestHasGuardArg(t *testing.T) {
	for _, c := range []struct {
		args          string
		whole, script bool
	}{
		{"sleep\x00999999999.123\x00", true, true},
		{"/bin/sh\x00-c\x00( exec </dev/null; sleep 999999999.123 & ); exec \"$0\" \"$@\"\x00sh\x00", false, true},
		{"999999999.123\x00", true, true},
		{"sleep\x00999999999.1234\x00", false, false},
		{"sleep\x00999999999.12\x00", false, false},
		{"sleep\x00x999999999.123\x00", false, false},
		{"echo\x00999999999.123x\x00", false, false},
		{"sleep\x00999999999.1234\x00999999999.123\x00", true, true},
		{"", false, false},
	} {
		if got := hasGuardArg([]byte(c.args), "123", false); got != c.whole {
			t.Errorf("hasGuardArg(%q, false) = %v", c.args, got)
		}
		if got := hasGuardArg([]byte(c.args), "123", true); got != c.script {
			t.Errorf("hasGuardArg(%q, true) = %v", c.args, got)
		}
	}
}

// groupMembersOf lists a real process group's members with their start
// times, the same each time, and nothing once the group has ended.
func TestGroupMembersOf(t *testing.T) {
	cmd := exec.Command("sleep", "5")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	a, b := groupMembersOf([]int{pid})[pid], groupMembersOf([]int{pid})[pid]
	if len(a) != 1 || a[0].pid != pid || a[0].start == 0 || !slices.Equal(a, b) {
		t.Fatalf("members %v, then %v", a, b)
	}
	cmd.Process.Kill()
	cmd.Wait()
	if ms := groupMembersOf([]int{pid})[pid]; len(ms) != 0 {
		t.Fatalf("an ended group lists %v", ms)
	}
}

// The engine forgets a step's child group once it has ended, while the
// invocation is still in flight, so a later cancel of that invocation
// cannot signal whatever takes the id (review round 2, finding 3).
func TestBunWarmRetiresEndedChildren(t *testing.T) {
	// spec: WR-55
	p := spawn(t, childrenConfig(t))
	pidFile := filepath.Join(t.TempDir(), "pid")
	rec := newRecorder()
	inv := newInvocation("spawn-hold", childArgs(t, map[string]any{"seconds": 0.1, "pidFile": pidFile}), rec)
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	go p.Execute(ctx, inv)
	pid := readPID(t, pidFile)
	held := func() []*childGroup {
		p.mu.Lock()
		defer p.mu.Unlock()
		if e := p.invs[inv.ID]; e != nil {
			return slices.Clone(e.children)
		}
		return nil
	}
	waitFor(t, "the engine to hold the child's group", func() bool { return len(held()) == 1 })
	g := held()[0]
	g.mu.Lock()
	seen := len(g.seen)
	g.mu.Unlock()
	if g.pgid != pid || seen == 0 {
		t.Fatalf("held group %d with %d members for the child %d", g.pgid, seen, pid)
	}
	waitFor(t, "the child to exit", func() bool { return gone(pid) })
	// Only the guardian is left: the engine forgets the group, then ends it.
	waitFor(t, "the engine to retire the ended group", func() bool { return len(held()) == 0 })
	waitFor(t, "the guardian to be released", func() bool { return len(groupMembersOf([]int{pid})[pid]) == 0 })
	cancel(engine.CancelTimeout)
}

// A shell that backgrounds a daemon and exits at once hands its group to
// the daemon, which the engine may never have seen beside it. The group's
// guardian keeps it the original, so the settlement's kill reaches the
// daemon, and then nothing is left in the group (review round 3,
// finding 1).
func TestBunWarmGuardedHandoff(t *testing.T) {
	// spec: WR-55
	p := spawn(t, childrenConfig(t))
	pidFile := filepath.Join(t.TempDir(), "pid")
	out, err := execute(t, p, newInvocation("spawn-daemon", childArgs(t, map[string]any{"seconds": 30, "pidFile": pidFile}), newRecorder()), 10*time.Second)
	if err != nil || out.Kind != engine.OutcomeOK {
		t.Fatalf("got %+v, %v", out, err)
	}
	daemon := childPID(t, out.Body.Producer())
	leftover(t, daemon)
	waitFor(t, "the engine to kill the daemon the shell left", func() bool { return ended(daemon) })
}
