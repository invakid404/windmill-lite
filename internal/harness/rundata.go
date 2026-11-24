package harness

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// RunData is the data of one run (wl.RunOptions.Data), which the host
// plugin of each process that the run uses reads as wl.run.data (ADR 0004
// host plugins, item 9). It travels in the run's context (WithRunData), so
// the invocations and hook calls of the run carry it, and those of another
// run never do, even one that reuses its id while this one has not settled.
// Each RunData has an id of its own, unique in the engine, which its frames
// carry as run_data_id and by which the process keeps the data.
//
// A process gets the data with the first invoke or host_call of the run
// that it receives. Frames to one process need not leave in the order they
// were queued: a small frame goes out before a large one that is still
// being sent in parts. So each frame of the run carries the data until one
// that carried it holds its place on the wire (conn.sendPlaced), and none
// after that. Once the run has settled, Release sends each process that the
// data reached a run_released, after which it drops the data.
type RunData struct {
	id   string
	run  string
	data jsonval.Value

	mu sync.Mutex
	// placed holds the processes to which a frame that carries the data
	// holds its place on the wire.
	placed   map[*Process]bool
	released bool
}

// runDataSeq numbers the RunData of the engine.
var runDataSeq atomic.Uint64

// NewRunData returns run's data, or nil for zero data: a run without data.
func NewRunData(run string, data jsonval.Value) *RunData {
	if data.IsZero() {
		return nil
	}
	return &RunData{id: strconv.FormatUint(runDataSeq.Add(1), 10), run: run, data: data, placed: map[*Process]bool{}}
}

type runDataKey struct{}

// WithRunData returns ctx with d as its run's data; a nil d returns ctx.
func WithRunData(ctx context.Context, d *RunData) context.Context {
	if d == nil {
		return ctx
	}
	return context.WithValue(ctx, runDataKey{}, d)
}

// RunDataOf returns the run data of ctx, or nil.
func RunDataOf(ctx context.Context) *RunData {
	d, _ := ctx.Value(runDataKey{}).(*RunData)
	return d
}

// runDataSend is what one frame of a run to a process carries: the id of
// the run's data, and the data itself unless a frame that carried it holds
// its place on the wire to the process.
type runDataSend struct {
	d    *RunData
	p    *Process
	id   string
	data jsonval.Value
}

// take returns what a frame of d's run to p carries; zero for a nil d.
func (d *RunData) take(p *Process) runDataSend {
	if d == nil {
		return runDataSend{}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.placed[p] {
		return runDataSend{id: d.id}
	}
	return runDataSend{d: d, p: p, id: d.id, data: d.data}
}

// placedFunc returns the connection's placed callback for a frame that
// carries the data (conn.sendPlaced, conn.queuePlaced), or nil for one
// that carries none.
func (t runDataSend) placedFunc() func() {
	if t.d == nil {
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
	d := t.d
	d.mu.Lock()
	d.placed[t.p] = true
	released := d.released
	d.mu.Unlock()
	if released {
		// The run settled while the frame was queued: the process gets
		// the data, and must drop it again.
		go t.p.conn.send(RunReleased{Run: d.run, RunDataID: d.id})
	}
}

// Release records that the run has settled, and sends a run_released to
// every process that a frame carrying the data has reached. A frame that
// still waits for its place sends one once placed; a process that has
// ended meanwhile is skipped. Releasing a nil RunData, or a released one,
// does nothing.
func (d *RunData) Release() {
	if d == nil {
		return
	}
	d.mu.Lock()
	if d.released {
		d.mu.Unlock()
		return
	}
	d.released = true
	to := make([]*Process, 0, len(d.placed))
	for p := range d.placed {
		to = append(to, p)
	}
	d.mu.Unlock()
	for _, p := range to {
		// A process that cannot take the frame has ended, and the data
		// with it.
		_ = p.conn.send(RunReleased{Run: d.run, RunDataID: d.id})
	}
}
