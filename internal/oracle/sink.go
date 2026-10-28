package oracle

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// Sink is the effect sink of OR-16: an HTTP recorder on the harness host
// that fixture scripts POST their effects to. The oracle reaches it through
// WL_EFFECT_SINK, the only host variable whitelisted for jobs.
//
// An effect is a JSON object that carries "root" (WM_ROOT_JOB_ID), "job"
// and "step". The sink groups effects by the outer run: it accepts one only
// while a run is open and only if root is that run's submitted root, and
// otherwise answers 409 with {"error":"foreign_effect"} and drops it
// (OR-16/c, OR-V6). Arrival order is kept; with one worker it is execution
// order.
type Sink struct {
	mu      sync.Mutex
	root    string
	effects []json.RawMessage
	foreign int

	srv *http.Server
	ln  net.Listener
}

// maxEffect bounds one effect body.
const maxEffect = 1 << 20

// ServeHTTP implements the sink endpoint.
func (s *Sink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	accepted := s.root != "" && probe.Root != nil && *probe.Root == s.root
	if accepted {
		s.effects = append(s.effects, json.RawMessage(append([]byte(nil), body...)))
	} else {
		s.foreign++
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if !accepted {
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
	s.root, s.effects, s.foreign = root, nil, 0
}

// Close stops collecting and returns the run's effects and the number of
// foreign effects refused while it was open.
func (s *Sink) Close() (effects []json.RawMessage, foreign int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	effects, foreign = s.effects, s.foreign
	s.root, s.effects, s.foreign = "", nil, 0
	return effects, foreign
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
