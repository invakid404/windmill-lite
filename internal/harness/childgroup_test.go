package harness

import (
	"context"
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
// tests: each group's members, with their start times, and the groups
// signalled. No real signal is sent.
type fakeProcs struct {
	mu       sync.Mutex
	groups   map[int][]groupMember
	guards   map[int]string // pid → the token in its environment
	signaled []int
	// hold, when set, runs once, in the next listing of members, after the
	// listing is taken and before it is returned: it pauses whoever looks.
	hold func()
}

func useFakeProcs(t *testing.T) *fakeProcs {
	t.Helper()
	f := &fakeProcs{groups: map[int][]groupMember{}, guards: map[int]string{}}
	gm, ig, sg := groupMembers, isGuard, signalGroup
	isGuard = func(pid int, token string) bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.guards[pid] == token
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
	t.Cleanup(func() { groupMembers, isGuard, signalGroup = gm, ig, sg })
	return f
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
	waitFor(t, "the guardian's token", func() bool { return processHasGuard(pid, "012345678901234") })
	if processHasGuard(pid, "01234567890123") || processHasGuard(os.Getpid(), "012345678901234") {
		t.Fatal("the guardian's token is not told apart")
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
