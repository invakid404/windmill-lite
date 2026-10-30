package oracle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/invakid404/windmill-lite/internal/conformance"
)

// lockedBuffer is a bytes.Buffer that several goroutines can write.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

// TestRedactingWriter: whole lines pass with the token replaced, a token
// split across writes is still replaced, and Flush writes an unfinished
// last line.
func TestRedactingWriter(t *testing.T) {
	var out bytes.Buffer
	w := &redactingWriter{w: &out, token: "s3cr3tt0k3n"}
	for _, p := range []string{"one s3cr", "3tt0k3n two\nthree s3cr3t", "t0k3n", " four"} {
		if n, err := w.Write([]byte(p)); err != nil || n != len(p) {
			t.Fatalf("Write(%q) = %d, %v", p, n, err)
		}
	}
	if got, want := out.String(), "one "+RedactedToken+" two\n"; got != want {
		t.Fatalf("before Flush: %q, want %q", got, want)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "one "+RedactedToken+" two\nthree "+RedactedToken+" four"; got != want {
		t.Fatalf("after Flush: %q, want %q", got, want)
	}
}

// TestClientHidesItsToken: a server that answers with the client's own
// token in its body, as a job's arguments do when they hold a trigger
// event's headers, and in its Content-Type, which a flow's result can set
// for run-and-wait: the body the client returns, the error of an
// unexpected status, a run-and-wait response with its Content-Type and the
// capture all hold RedactedToken instead. Before login, with no token, a
// body is returned as it is.
func TestClientHidesItsToken(t *testing.T) {
	const token = "session-token"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/auth/login" {
			_, _ = w.Write([]byte(token))
			return
		}
		body := `{"headers":{"authorization":` + jsonString(r.Header.Get("Authorization")) + `}}`
		w.Header().Set("Content-Type", "application/json; auth="+r.Header.Get("Authorization"))
		if r.URL.Path == "/api/version" {
			w.WriteHeader(http.StatusInternalServerError)
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	ctx := context.Background()
	c := &Client{BaseURL: srv.URL, Capture: &Capture{}}
	if err := Bootstrap(ctx, c); err != nil || c.Token != token {
		t.Fatalf("login: token %q, %v", c.Token, err)
	}
	want := `{"headers":{"authorization":"Bearer ` + RedactedToken + `"}}`

	data, err := c.do(ctx, http.MethodGet, "/w/ws/jobs_u/get_args/x", nil, nil, http.StatusOK)
	if err != nil || string(data) != want {
		t.Fatalf("body %s, %v; want %s", data, err, want)
	}
	_, err = c.Version(ctx)
	var api *APIError
	if !errors.As(err, &api) || strings.Contains(err.Error(), token) || !strings.Contains(err.Error(), RedactedToken) {
		t.Fatalf("error of an unexpected status: %v", err)
	}
	resp, err := c.RunWaitFlow(ctx, "ws", "f/x/y", json.RawMessage(`{}`), "id")
	if err != nil || string(resp.Body) != want {
		t.Fatalf("run-and-wait body %s, %v; want %s", resp.Body, err, want)
	}
	if wantType := "application/json; auth=Bearer " + RedactedToken; resp.ContentType != wantType {
		t.Fatalf("run-and-wait Content-Type %q, want %q", resp.ContentType, wantType)
	}
	captured, err := json.Marshal(c.Capture.Exchanges)
	if err != nil {
		t.Fatal(err)
	}
	// Login is not captured: its body is the token.
	if bytes.Contains(captured, []byte(token)) || len(c.Capture.Exchanges) != 3 {
		t.Fatalf("capture: %s", captured)
	}
}

// TestSessionNeverWritesTheToken records a run that hands the session's
// token back in every way a run can: a trigger event's headers in a step's
// arguments, the flow's result, an effect, and the run-and-wait response's
// body and Content-Type, with a CLI that prints its arguments, the token
// among them. A second
// session checks the run against a changed oracle, so the report's
// differences quote the values. No recording, raw capture, report or log
// line holds the token, and RedactedToken stands where it was.
func TestSessionNeverWritesTheToken(t *testing.T) {
	const token = "session-token" // the fake oracle's login answer
	rig := newRig(t)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(rig.fixture, "runs", "leak.yaml"), []byte("target: f/wl_smoke/two_step\nsubmit: run_wait_result\nargs: {x: 4, effects: true, leak: true}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw := t.TempDir()
	var log, cliOut lockedBuffer
	session := func() *Session {
		s := rig.session(20 * GiB)
		s.CLI.Command[len(s.CLI.Command)-1] = fakeCLIEcho
		s.CLI.Stdout, s.CLI.Stderr, s.Log = &cliOut, &log, &log
		s.RawDir = raw
		return s
	}

	rep, err := session().Run(ctx, []*conformance.Fixture{rig.load(t)})
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomes(rep)["leak"]; got.Outcome != OutcomeNew {
		t.Fatalf("leak: %+v", got)
	}
	rig.oracle.tag = "ho"
	s := session()
	s.Check = true
	checked, err := s.Run(ctx, []*conformance.Fixture{rig.load(t)})
	if err != nil {
		t.Fatal(err)
	}
	got := outcomes(checked)["leak"]
	if got.Outcome != OutcomeChanged || len(got.Differences) == 0 {
		t.Fatalf("leak, checked against the changed oracle: %+v", got)
	}
	// The Content-Type changed with the tag, so the report quotes it.
	if !slices.ContainsFunc(got.Differences, func(d string) bool {
		return strings.HasPrefix(d, "/response/content_type") && strings.Contains(d, "auth=Bearer "+RedactedToken)
	}) {
		t.Fatalf("no difference quotes the redacted Content-Type: %q", got.Differences)
	}

	outputs := map[string][]byte{"the log": log.Bytes(), "the CLI's output": cliOut.Bytes()}
	for i, r := range []*Report{rep, checked} {
		data, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		outputs[[]string{"the report", "the check's report"}[i]] = data
	}
	for _, dir := range []string{filepath.Join(rig.fixture, "recorded"), raw} {
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := os.ReadFile(p)
			outputs[p] = data
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range outputs {
		if bytes.Contains(data, []byte(token)) {
			t.Errorf("%s holds the session's token:\n%s", name, data)
		}
	}
	// Each output had the token to hold, so the replacement stands in it.
	// JSON output may escape its angle brackets, so the check leaves them
	// out.
	replaced := []byte(strings.Trim(RedactedToken, "<>"))
	for _, name := range []string{"the log", "the CLI's output", "the check's report", filepath.Join(rig.fixture, "recorded", "leak.json")} {
		if !bytes.Contains(outputs[name], replaced) {
			t.Errorf("%s does not hold %s:\n%s", name, RedactedToken, outputs[name])
		}
	}
	captures := 0
	for name, data := range outputs {
		if strings.HasPrefix(name, raw) && strings.Contains(name, "-leak-") && bytes.Contains(data, replaced) {
			captures++
		}
	}
	// One capture for each repeat; the second session rewrites the same two.
	if captures != 2 {
		t.Errorf("%d raw captures of run leak hold %s, want 2", captures, RedactedToken)
	}
}
