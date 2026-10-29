package facade

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/invakid404/windmill-lite/internal/harness"
)

// Platform returns the harness's handler for the intercepted path: each
// platform frame is attributed to the invocation the harness named, from
// the async context of the fetch that made it (ADR 0004, WA-31), never
// from the token it carries. It accepts the call on the connection's
// reader, in frame order.
func (f *Facade) Platform() harness.PlatformHandler {
	return func(req harness.PlatformRequest) func(context.Context) harness.PlatformReply {
		body := req.Body
		if len(body) == 0 {
			body = nil // as over TCP, where an empty body is none
		}
		serve := f.accept(request{method: req.Method, url: req.URL, headers: req.Headers, body: body}, req.Inv, true)
		return func(ctx context.Context) harness.PlatformReply {
			rep := serve(ctx)
			return harness.PlatformReply{ID: req.ID, Status: rep.Status, Headers: rep.Headers, Body: rep.Body}
		}
	}
}

// reservation is the warm origin: a loopback port in use by a connection
// the facade holds both ends of, so nothing else is handed the port and
// nothing listens on it. The kernel refuses every connection to it, on
// Linux and on macOS alike. A socket that is only bound would not do:
// macOS drops the SYNs that reach a bound socket that does not listen, so
// a client would wait for its timeout instead of being refused.
type reservation struct {
	origin string
	conns  [2]net.Conn
	once   sync.Once
}

func reserve() (*reservation, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	defer l.Close()
	type accepted struct {
		c   net.Conn
		err error
	}
	ch := make(chan accepted, 1)
	go func() {
		c, err := l.Accept()
		ch <- accepted{c, err}
	}()
	near, err := net.DialTimeout("tcp", l.Addr().String(), 5*time.Second)
	if err != nil {
		return nil, err
	}
	far := <-ch
	if far.err != nil {
		near.Close()
		return nil, far.err
	}
	// The connection's local end is the reserved port; the listener that
	// took it closes on return, so nothing listens there.
	return &reservation{origin: "http://" + near.LocalAddr().String(), conns: [2]net.Conn{near, far.c}}, nil
}

func (r *reservation) close() {
	r.once.Do(func() {
		r.conns[0].Close()
		r.conns[1].Close()
	})
}

// listener is the fresh origin: loopback TCP, where a process is one
// invocation and its token (WM_TOKEN) names it (ADR 0007). A call whose
// token is unknown, or names an invocation that has ended, answers 401
// and is recorded as a stray; it is never guessed onto another
// invocation.
type listener struct {
	origin string
	srv    *http.Server
	done   chan struct{}
}

func listen(f *Facade) (*listener, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	t := &listener{origin: "http://" + l.Addr().String(), done: make(chan struct{})}
	t.srv = &http.Server{Handler: http.HandlerFunc(f.serveHTTP), ReadHeaderTimeout: time.Minute}
	go func() {
		defer close(t.done)
		t.srv.Serve(l)
	}()
	return t, nil
}

func (t *listener) close() error {
	err := t.srv.Close()
	<-t.done
	return err
}

func (f *Facade) serveHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, f.maxBody))
	if err != nil {
		// The call never reaches the facade, so it names no invocation and
		// is recorded nowhere; its client sees the transport's failure.
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "Bad request: the request body exceeds windmill-lite's limit", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "Bad request: the request body could not be read", http.StatusBadRequest)
		}
		return
	}
	headers := make(map[string]string, len(r.Header))
	for k, vs := range r.Header {
		headers[strings.ToLower(k)] = strings.Join(vs, ", ")
	}
	if len(body) == 0 {
		body = nil
	}
	rep := f.accept(request{method: r.Method, url: r.URL.RequestURI(), headers: headers, body: body}, "", false)(r.Context())
	for k, v := range rep.Headers {
		w.Header().Set(k, v)
	}
	w.WriteHeader(rep.Status)
	w.Write(rep.Body)
}
