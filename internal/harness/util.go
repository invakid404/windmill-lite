package harness

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"unicode/utf8"
)

// TailLength is how many characters of an invocation's log the engine
// keeps for the log-tail error of an escaped failure (WR-20).
const TailLength = 600

// tail keeps the last TailLength characters of the log lines it is given,
// joined by newlines.
type tail struct{ s string }

func (t *tail) add(line string) {
	if t.s != "" {
		t.s += "\n"
	}
	t.s += line
	if n := utf8.RuneCountInString(t.s); n > TailLength {
		for drop := n - TailLength; drop > 0; drop-- {
			_, size := utf8.DecodeRuneInString(t.s)
			t.s = t.s[size:]
		}
	}
}

func (t *tail) String() string { return t.s }

// diagnostics writes the engine's own lines about one process.
type diagnostics struct {
	mu  sync.Mutex
	w   io.Writer
	pid atomic.Int64
}

func (d *diagnostics) printf(format string, args ...any) {
	d.line(fmt.Sprintf("windmill-lite: harness pid %d: %s", d.pid.Load(), fmt.Sprintf(format, args...)))
}

func (d *diagnostics) line(s string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	io.WriteString(d.w, s+"\n")
}

// maxPendingKillLines is how many kill lines may wait for their
// diagnostics writers at once, across every process in the program.
const maxPendingKillLines = 64

// killLineBudget bounds the goroutines that write kill lines, across all
// the processes that share it. A kill writes its line on a goroutine of
// its own, because the diagnostics writer can block, and a kill must never
// wait for it. Each process writes at most one kill line, but without a
// shared bound a writer that stays blocked would hold one goroutine for
// every process the engine kills. Past the bound a line is dropped and
// counted, and the next line that the budget accepts reports the count.
type killLineBudget struct {
	max     int32
	pending atomic.Int32
	dropped atomic.Int64
}

// runtimeKillLines is the budget of every process whose config names no
// other one.
var runtimeKillLines = &killLineBudget{max: maxPendingKillLines}

// write writes line to d on a goroutine of its own, or drops it when the
// budget's maximum of lines is already waiting.
func (b *killLineBudget) write(d *diagnostics, line string) {
	if b.pending.Add(1) > b.max {
		b.pending.Add(-1)
		b.dropped.Add(1)
		return
	}
	// The count is taken here, not on the goroutine, so it goes to the
	// first line the budget accepts after the drops, however late that
	// line's goroutine runs.
	if n := b.dropped.Swap(0); n > 0 {
		line += fmt.Sprintf(" (%d earlier kill lines were dropped while %d waited for blocked diagnostics writers)", n, b.max)
	}
	go func() {
		defer b.pending.Add(-1)
		d.printf("%s", line)
	}()
}

// lineWriter forwards a process's stdout and stderr to the diagnostics,
// one prefixed line at a time, and keeps the last few lines for error
// messages.
type lineWriter struct {
	diag *diagnostics
	keep int

	mu   sync.Mutex
	last []string
}

func (lw *lineWriter) stream(name string) io.Writer { return &streamWriter{lw: lw, name: name} }

func (lw *lineWriter) emit(name, line string) {
	lw.diag.line(fmt.Sprintf("harness pid %d %s: %s", lw.diag.pid.Load(), name, line))
	lw.mu.Lock()
	lw.last = append(lw.last, line)
	if len(lw.last) > lw.keep {
		lw.last = lw.last[len(lw.last)-lw.keep:]
	}
	lw.mu.Unlock()
}

// lastLines formats the kept lines for an error message.
func (lw *lineWriter) lastLines() string {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	if len(lw.last) == 0 {
		return ""
	}
	return "; its last output:\n" + strings.Join(lw.last, "\n")
}

type streamWriter struct {
	lw   *lineWriter
	name string
	buf  []byte
}

func (s *streamWriter) Write(b []byte) (int, error) {
	s.buf = append(s.buf, b...)
	for {
		i := bytes.IndexByte(s.buf, '\n')
		if i < 0 {
			break
		}
		s.lw.emit(s.name, string(s.buf[:i]))
		s.buf = s.buf[i+1:]
	}
	if len(s.buf) > 64<<10 {
		s.lw.emit(s.name, string(s.buf))
		s.buf = nil
	}
	return len(b), nil
}

// recentInvocations remembers the last settled invocations, so late output
// of work they left running (D9) still reaches their runs, through their
// own call queues.
type recentInvocations struct {
	limit int
	m     map[string]*invocation
	order []string
}

func (r *recentInvocations) add(id string, inv *invocation) {
	if r.m == nil {
		r.m = map[string]*invocation{}
	}
	if _, ok := r.m[id]; !ok {
		r.order = append(r.order, id)
	}
	r.m[id] = inv
	for len(r.order) > r.limit {
		delete(r.m, r.order[0])
		r.order = r.order[1:]
	}
}

func (r *recentInvocations) get(id string) (*invocation, bool) {
	inv, ok := r.m[id]
	return inv, ok
}

// serial runs functions one at a time, in the order they were queued, on a
// goroutine that exists only while there is work: one do starts, or one
// that claim's caller provides.
type serial struct {
	mu      sync.Mutex
	queue   []func()
	running bool
}

func (s *serial) do(fs ...func()) {
	if len(fs) > 0 && s.claim(fs...) {
		go s.run()
	}
}

// claim queues fs as do does, but starts no goroutine: when none is
// running it reports true, and the caller must then see that run is
// called, on a goroutine of its choosing, which runs fs and whatever is
// queued until the queue is empty. Otherwise the running goroutine runs
// fs after what is queued before them.
func (s *serial) claim(fs ...func()) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queue = append(s.queue, fs...)
	if s.running {
		return false
	}
	s.running = true
	return true
}

// runUntil runs the queue as run does, on the caller's goroutine, but
// only until done reports true after one of the functions: what is queued
// then goes on, in order, on a goroutine of its own.
func (s *serial) runUntil(done func() bool) {
	for {
		s.mu.Lock()
		if len(s.queue) == 0 {
			s.running = false
			s.mu.Unlock()
			return
		}
		f := s.queue[0]
		s.queue = s.queue[1:]
		s.mu.Unlock()
		f()
		if done() {
			s.mu.Lock()
			rest := len(s.queue) > 0
			s.running = rest
			s.mu.Unlock()
			if rest {
				go s.run()
			}
			return
		}
	}
}

func (s *serial) run() {
	for {
		s.mu.Lock()
		if len(s.queue) == 0 {
			s.running = false
			s.mu.Unlock()
			return
		}
		f := s.queue[0]
		s.queue = s.queue[1:]
		s.mu.Unlock()
		f()
	}
}

// signalName returns the conventional name of a signal, as in
// "process terminated by signal: SIGKILL" (WR-4).
func signalName(s syscall.Signal) string {
	switch s {
	case syscall.SIGKILL:
		return "SIGKILL"
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGHUP:
		return "SIGHUP"
	case syscall.SIGQUIT:
		return "SIGQUIT"
	case syscall.SIGABRT:
		return "SIGABRT"
	case syscall.SIGSEGV:
		return "SIGSEGV"
	case syscall.SIGBUS:
		return "SIGBUS"
	case syscall.SIGILL:
		return "SIGILL"
	case syscall.SIGFPE:
		return "SIGFPE"
	case syscall.SIGPIPE:
		return "SIGPIPE"
	case syscall.SIGALRM:
		return "SIGALRM"
	case syscall.SIGTRAP:
		return "SIGTRAP"
	case syscall.SIGUSR1:
		return "SIGUSR1"
	case syscall.SIGUSR2:
		return "SIGUSR2"
	}
	return fmt.Sprintf("signal %d", int(s))
}
