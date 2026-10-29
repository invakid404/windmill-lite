package harness

import (
	"errors"
	"os"
	"slices"
	"sync"
	"syscall"
	"time"
)

// Reaping (WR-55; review round 5, finding 1). A process whose parent exits
// is adopted by the nearest subreaper, or by PID 1: by the engine, when it
// runs as PID 1 without an init or as a subreaper. The guardian of every
// child group is such an orphan from the start (guardedCommand), and so is
// whatever a group's member leaves when it exits. Once one of them has
// exited it stays a zombie, holding its pid and its group's id, until its
// parent waits for it. So the engine reaps the members of the groups it
// owns: each by its own pid, without blocking (wait4(pid, WNOHANG)), only
// while it is still the process the engine saw in the group (its pid and
// start time) and only when the engine is its parent. No wait takes
// another child's exit status: the engine's own children, its harness
// processes among them, are in no group it reaps, and a wait names one
// process, never "any child".

// procInfo is what reaping reads of a process: its start time, in
// groupMembers' units, its parent, its group, and whether it is a zombie.
type procInfo struct {
	start      uint64
	ppid, pgid int
	zombie     bool
}

// The process operations of reaping, replaced in tests so that no real
// process is waited for: reading a process, and waiting for one, which
// reports whether the engine is done with it (reaped, or not its child
// after all).
var (
	readProc = procInfoOf
	reapPID  = func(pid int) bool {
		var ws syscall.WaitStatus
		got, err := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
		return got == pid || errors.Is(err, syscall.ECHILD)
	}
)

// reapMember reaps m, a member the engine saw in its group pgid (its pid
// and start time), if it has exited and the engine is its parent, and
// reports whether the engine is done with it: reaped; gone, its parent
// having reaped it; its pid now another process's; or still running but
// out of the group, which it left before a kill could reach it. One whose
// parent is another process is kept: that parent reaps it, or it is
// adopted when that parent exits.
func reapMember(pgid int, m groupMember) (done bool) {
	info, ok := readProc(m.pid)
	switch {
	case !ok || info.start != m.start:
		return true
	case !info.zombie && info.pgid != pgid:
		return true
	case info.ppid != os.Getpid():
		return false
	}
	return reapPID(m.pid)
}

// reapSet is a group the engine killed, with the members it holds of it,
// each reaped as it exits (reapMember).
type reapSet struct {
	pgid int
	// skip is a pid never reaped here: a harness's own, which its
	// exec.Cmd waits for.
	skip    int
	members []groupMember
	since   time.Time
}

// step reaps what of s has exited. It first lists the group again: while a
// member s holds is still in the group after that listing, the group was
// never empty meanwhile, so its id was never given to another group and
// every process listed is the killed group's; s holds those too (a process
// forked as the kill landed). Without such a member the listing is not
// trusted.
func (s *reapSet) step() {
	listed := groupMembers([]int{s.pgid})[s.pgid]
	if len(listed) > 0 && slices.ContainsFunc(s.members, s.inGroup) {
		for _, m := range listed {
			if m.pid != s.skip && !slices.Contains(s.members, m) {
				s.members = append(s.members, m)
			}
		}
	}
	s.members = slices.DeleteFunc(s.members, func(m groupMember) bool { return reapMember(s.pgid, m) })
}

// inGroup reports whether m is still the process the engine saw, in s's
// group.
func (s *reapSet) inGroup(m groupMember) bool {
	info, ok := readProc(m.pid)
	return ok && info.start == m.start && info.pgid == s.pgid
}

// reaper holds the groups the engine killed whose members are not all
// reaped yet. It is the package's, not a Process's: a group killed as its
// harness process ends (failInvocations, endGroup) is reaped after that
// process is gone.
var reaper struct {
	mu   sync.Mutex
	sets []*reapSet
	// done is closed when the reaping goroutine exits; nil while none
	// runs.
	done chan struct{}
}

// passMu serialises reaping passes (reapPass).
var passMu sync.Mutex

// reapLater hands the reaper the members of group pgid, which the engine
// has just killed, but skip; a goroutine reaps them as they exit, until
// none is left.
func reapLater(pgid, skip int, members []groupMember) {
	s := &reapSet{pgid: pgid, skip: skip, since: time.Now()}
	for _, m := range members {
		if m.pid != skip {
			s.members = append(s.members, m)
		}
	}
	reaper.mu.Lock()
	defer reaper.mu.Unlock()
	reaper.sets = append(reaper.sets, s)
	if reaper.done == nil {
		reaper.done = make(chan struct{})
		go reapLoop(reaper.done)
	}
}

// reapLoop runs reaping passes, as often as the engine looks at the child
// groups it holds (childWatchYoung), until no group is left to reap.
func reapLoop(done chan struct{}) {
	defer close(done)
	for {
		reaper.mu.Lock()
		every := childWatch
		for _, s := range reaper.sets {
			if time.Since(s.since) < childYoung {
				every = childWatchYoung
			}
		}
		reaper.mu.Unlock()
		time.Sleep(every)
		reapPass()
		reaper.mu.Lock()
		if len(reaper.sets) == 0 {
			reaper.done = nil
			reaper.mu.Unlock()
			return
		}
		reaper.mu.Unlock()
	}
}

// reapPass runs one step of every group the reaper holds and drops the
// groups with no member left.
func reapPass() {
	passMu.Lock()
	defer passMu.Unlock()
	reaper.mu.Lock()
	sets := slices.Clone(reaper.sets)
	reaper.mu.Unlock()
	for _, s := range sets {
		s.step()
	}
	reaper.mu.Lock()
	defer reaper.mu.Unlock()
	reaper.sets = slices.DeleteFunc(reaper.sets, func(s *reapSet) bool { return len(s.members) == 0 })
}
