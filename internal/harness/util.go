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

	"github.com/invakid404/windmill-lite/internal/engine"
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

// recentReporters remembers the reporters of the last settled invocations,
// so late output of work they left running (D9) still reaches their runs.
type recentReporters struct {
	limit int
	m     map[string]engine.Reporter
	order []string
}

func (r *recentReporters) add(id string, rep engine.Reporter) {
	if r.m == nil {
		r.m = map[string]engine.Reporter{}
	}
	if _, ok := r.m[id]; !ok {
		r.order = append(r.order, id)
	}
	r.m[id] = rep
	for len(r.order) > r.limit {
		delete(r.m, r.order[0])
		r.order = r.order[1:]
	}
}

func (r *recentReporters) get(id string) (engine.Reporter, bool) {
	rep, ok := r.m[id]
	return rep, ok
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
