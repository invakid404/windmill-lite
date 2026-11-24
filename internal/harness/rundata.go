package harness

import (
	"sync"

	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// RunData holds the data of a runtime's runs (ADR 0004 host plugins, item
// 9; ADR 0008 H11) and which of its processes each run's data has reached.
// One RunData serves every process of a runtime: a pool's, or a host's.
//
// A process gets a run's data with the first invoke or host_call of the
// run that it receives, so that the host plugin can read it before any of
// its code runs for the run. Frames to one process need not leave in the
// order they were queued: a small frame goes out before a large one that
// is still being sent in parts. So every invoke and host_call of the run
// carries the data until one that carried it holds its place on the wire
// (conn.sendPlaced), and none after that. Once the run has settled the
// process gets a run_released, after which it drops the data.
type RunData struct {
	mu   sync.Mutex
	runs map[string]*runDataEntry
}

type runDataEntry struct {
	data jsonval.Value
	// placed holds the processes to which a frame that carries the data
	// holds its place on the wire.
	placed   map[*Process]bool
	released bool
}

// NewRunData returns an empty RunData.
func NewRunData() *RunData {
	return &RunData{runs: map[string]*runDataEntry{}}
}

// Set records run's data, which must not be zero, for the frames of the
// run that start after it.
func (s *RunData) Set(run string, data jsonval.Value) {
	if s == nil || data.IsZero() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[run] = &runDataEntry{data: data, placed: map[*Process]bool{}}
}

// runDataSend is what one frame of a run to a process carries: the run's
// data, or zero for none.
type runDataSend struct {
	s    *RunData
	e    *runDataEntry
	p    *Process
	run  string
	data jsonval.Value
}

// take returns what a frame of run to p carries: the run's data, unless a
// frame that carried it already holds its place on the wire to p.
func (s *RunData) take(p *Process, run string) runDataSend {
	if s == nil {
		return runDataSend{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.runs[run]
	if e == nil || e.placed[p] {
		return runDataSend{}
	}
	return runDataSend{s: s, e: e, p: p, run: run, data: e.data}
}

// placedFunc returns the connection's placed callback for a frame that
// carries the data (conn.sendPlaced, conn.queuePlaced), or nil for one
// that carries none.
func (t runDataSend) placedFunc() func() {
	if t.s == nil {
		return nil
	}
	return t.placed
}

// placed records that the frame holds its place on the wire, so the data
// has reached the process for every frame queued after it. It is called
// under the connection's lock, so a run_released that the run's
// settlement left to it is sent from a goroutine of its own, and goes out
// after the frame.
func (t runDataSend) placed() {
	t.s.mu.Lock()
	t.e.placed[t.p] = true
	released := t.e.released
	t.s.mu.Unlock()
	if released {
		// The run settled while the frame was queued: the process gets
		// the data, and must drop it again.
		go t.p.conn.send(RunReleased{Run: t.run})
	}
}

// Release forgets run's data once the run has settled, and sends a
// run_released to every process that a frame carrying it has reached. A
// frame that still waits for its place sends one once placed; a process
// that has ended meanwhile is skipped. Releasing a run without data does
// nothing.
func (s *RunData) Release(run string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	e := s.runs[run]
	if e == nil {
		s.mu.Unlock()
		return
	}
	delete(s.runs, run)
	e.released = true
	to := make([]*Process, 0, len(e.placed))
	for p := range e.placed {
		to = append(to, p)
	}
	s.mu.Unlock()
	for _, p := range to {
		// A process that cannot take the frame has ended, and the data
		// with it.
		_ = p.conn.send(RunReleased{Run: run})
	}
}

// Len returns how many runs have data recorded: set and not yet released.
func (s *RunData) Len() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.runs)
}
