package oracle

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/invakid404/windmill-lite/internal/conformance"
)

// Sink is the effect sink of OR-16: an HTTP recorder on the harness host
// that fixture scripts POST their effects to. The oracle reaches it through
// WL_EFFECT_SINK, the only host variable whitelisted for jobs.
//
// An effect is a JSON object that carries "root" (WM_ROOT_JOB_ID), "job"
// and "step". The sink groups effects by the outer run: it accepts one only
// if root is the submitted root of a run that is open, and otherwise
// answers 409 with {"error":"foreign_effect"} and drops it (OR-16/c,
// OR-V6). Several runs can be open at once, for concurrent outer runs.
// Arrival order is kept per run; with one worker it is execution order.
//
// The same server answers stub calls at conformance.ReplayPath when Replay
// is set (ADR 0011 stub mode), and GET UserAgentPath with the request's
// User-Agent header as text, for the cases that read the user agent a
// step's fetch sends (WR-45/g); that request records nothing.
type Sink struct {
	// Replay serves POST /replay; nil answers it as an ordinary effect.
	Replay http.Handler

	mu      sync.Mutex
	runs    map[string]*sinkRun
	foreign int

	srv *http.Server
	ln  net.Listener
}

type sinkRun struct {
	effects []json.RawMessage
	// foreign is the sink's foreign count when the run was opened.
	foreign int
}

// maxEffect bounds one effect body.
const maxEffect = 1 << 20

// UserAgentPath is the sink path that answers a GET with the request's
// User-Agent header (WR-45/g).
const UserAgentPath = "/user-agent"

// ServeHTTP implements the sink endpoint.
func (s *Sink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.Replay != nil && r.URL.Path == conformance.ReplayPath {
		s.Replay.ServeHTTP(w, r)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == UserAgentPath {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, r.Header.Get("User-Agent"))
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST an effect", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxEffect+1))
	if err != nil || len(body) > maxEffect {
		http.Error(w, "effect too large or unreadable", http.StatusBadRequest)
		return
	}
	var probe struct {
		Root *string `json:"root"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		http.Error(w, "effect is not a JSON object", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	var run *sinkRun
	if probe.Root != nil {
		run = s.runs[*probe.Root]
	}
	if run != nil {
		run.effects = append(run.effects, json.RawMessage(append([]byte(nil), body...)))
	} else {
		s.foreign++
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if run == nil {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write(ErrForeignEffect().JSON())
		return
	}
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// Open starts collecting the effects of the run whose root job id is root.
func (s *Sink) Open(root string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runs == nil {
		s.runs = map[string]*sinkRun{}
	}
	s.runs[root] = &sinkRun{foreign: s.foreign}
}

// Close stops collecting the run's effects and returns them, with the
// number of foreign effects refused while it was open.
func (s *Sink) Close(root string) (effects []json.RawMessage, foreign int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[root]
	delete(s.runs, root)
	if r == nil {
		return nil, 0
	}
	return r.effects, s.foreign - r.foreign
}

// Listen starts serving on addr (host:port; port 0 picks one) and returns
// the port.
func (s *Sink) Listen(addr string) (int, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return 0, err
	}
	s.ln = ln
	s.srv = &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = s.srv.Serve(ln) }()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// Shutdown stops the server.
func (s *Sink) Shutdown() error {
	if s.srv == nil {
		return nil
	}
	return s.srv.Close()
}
