package harness

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/invakid404/windmill-lite/internal/engine"
)

// fakeProcs stands in for the kernel's process table in the child-group
// tests: each pid's start time, the groups that have a member, and the
// groups signalled. No real signal is sent.
type fakeProcs struct {
	mu       sync.Mutex
	start    map[int]uint64
	groups   map[int]bool
	signaled []int
}

func useFakeProcs(t *testing.T) *fakeProcs {
	t.Helper()
	f := &fakeProcs{start: map[int]uint64{}, groups: map[int]bool{}}
	ps, ga, sg := procStart, childGroupAlive, signalGroup
	procStart = func(pid int) (uint64, bool) {
		f.mu.Lock()
		defer f.mu.Unlock()
		s, ok := f.start[pid]
		return s, ok
	}
	childGroupAlive = func(pgid int) bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.groups[pgid]
	}
	signalGroup = func(pgid int) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.signaled = append(f.signaled, pgid)
	}
	t.Cleanup(func() { procStart, childGroupAlive, signalGroup = ps, ga, sg })
	return f
}

func (f *fakeProcs) set(pid int, start uint64, group bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if start == 0 {
		delete(f.start, pid)
	} else {
		f.start[pid] = start
	}
	f.groups[pid] = group
}

func (f *fakeProcs) took() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.signaled
	f.signaled = nil
	return out
}

// A child group is identified by its leader's start time, read as the
// engine learns of it, so a kill reaches the original group only: a
// recycled id is never signalled, while what the original leader started
// is killed after the leader exited. An id that cannot name a group is
// never signalled (review round 2, findings 2 and 3).
func TestChildGroupIdentity(t *testing.T) {
	// spec: WR-55
	f := useFakeProcs(t)
	f.set(100, 5, true)
	g, ok := newChildGroup(100)
	if !ok || g != (childGroup{pgid: 100, start: 5}) {
		t.Fatalf("newChildGroup: %+v %v", g, ok)
	}
	killChildGroups([]childGroup{g})
	if got := f.took(); !slices.Equal(got, []int{100}) {
		t.Fatalf("the live original: signalled %v", got)
	}
	// The leader exited; what it started still holds the group.
	f.set(100, 0, true)
	killChildGroups([]childGroup{g})
	if got := f.took(); !slices.Equal(got, []int{100}) {
		t.Fatalf("the original's descendants: signalled %v", got)
	}
	// The group ended and a later process took the id, as a group leader.
	f.set(100, 9, true)
	killChildGroups([]childGroup{g})
	if got := f.took(); len(got) != 0 {
		t.Fatalf("a recycled id was signalled: %v", got)
	}
	// The group ended: nothing to kill.
	f.set(100, 0, false)
	killChildGroups([]childGroup{g})
	if got := f.took(); len(got) != 0 {
		t.Fatalf("an ended group was signalled: %v", got)
	}
	// A leader that exited before the engine learned of the group leaves
	// the group to its descendants; any later process with the id is not
	// the original.
	f.set(200, 0, true)
	g2, ok := newChildGroup(200)
	if !ok || g2 != (childGroup{pgid: 200}) {
		t.Fatalf("newChildGroup of a group without its leader: %+v %v", g2, ok)
	}
	killChildGroups([]childGroup{g2})
	if got := f.took(); !slices.Equal(got, []int{200}) {
		t.Fatalf("the leaderless original: signalled %v", got)
	}
	f.set(200, 7, true)
	killChildGroups([]childGroup{g2})
	if got := f.took(); len(got) != 0 {
		t.Fatalf("a process that took a leaderless group's id was signalled: %v", got)
	}
	// A group that ended before the engine learned of it is not held.
	f.set(300, 0, false)
	if _, ok := newChildGroup(300); ok {
		t.Fatal("an ended group was taken")
	}
	// Ids that would narrow to another target, or name no group, are
	// never signalled, whatever the probes say.
	for _, pgid := range []int{0, 1, -1, 1 << 31, 1<<32 + 1, 1 << 32, 1<<32 + 4242} {
		f.set(pgid, 3, true)
		killChildGroups([]childGroup{{pgid: pgid, start: 3}})
	}
	if got := f.took(); len(got) != 0 {
		t.Fatalf("invalid ids were signalled: %v", got)
	}
}

// The engine retires the child groups that are no longer the steps': one
// that ended, and one whose id a later process took, so that cancelling
// the invocation afterwards cannot signal the id's new owner; one that
// outlived its leader is kept (review round 2, finding 3).
func TestRetireChildren(t *testing.T) {
	// spec: WR-55
	f := useFakeProcs(t)
	f.set(10, 1, true) // live original
	f.set(11, 2, true) // will end
	f.set(12, 3, true) // will be recycled
	f.set(13, 4, true) // its leader will exit, descendants stay
	inv := &invocation{id: "i"}
	for _, pid := range []int{10, 11, 12, 13} {
		g, _ := newChildGroup(pid)
		inv.children = append(inv.children, g)
	}
	p := &Process{invs: map[string]*invocation{"i": inv}}
	f.set(11, 0, false)
	f.set(12, 8, true)
	f.set(13, 0, true)
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

// processStart reads a live process's start time, the same each time, and
// none for a process that has exited and been reaped.
func TestProcessStart(t *testing.T) {
	a, ok := processStart(os.Getpid())
	b, ok2 := processStart(os.Getpid())
	if !ok || !ok2 || a == 0 || a != b {
		t.Fatalf("the test's own start: %d %v, %d %v", a, ok, b, ok2)
	}
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if s, ok := processStart(cmd.Process.Pid); ok {
		t.Fatalf("an exited process reads as started at %d", s)
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
	held := func() []childGroup {
		p.mu.Lock()
		defer p.mu.Unlock()
		if e := p.invs[inv.ID]; e != nil {
			return slices.Clone(e.children)
		}
		return nil
	}
	waitFor(t, "the engine to hold the child's group", func() bool { return len(held()) == 1 })
	if g := held()[0]; g.pgid != pid || g.start == 0 {
		t.Fatalf("held %+v for the child %d", g, pid)
	}
	waitFor(t, "the child to exit", func() bool { return gone(pid) })
	waitFor(t, "the engine to retire the ended group", func() bool { return len(held()) == 0 })
	cancel(engine.CancelTimeout)
}
