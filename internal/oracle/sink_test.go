package oracle

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSinkGroupsByOuterRoot is OR-16/c and OR-V6: an effect is attributed
// only to the open run whose submitted root it names; anything else is
// refused with {"error":"foreign_effect"} and dropped. Arrival order is
// kept.
func TestSinkGroupsByOuterRoot(t *testing.T) {
	var s Sink
	srv := httptest.NewServer(&s)
	defer srv.Close()
	post := func(body string) (int, string) {
		t.Helper()
		resp, err := http.Post(srv.URL, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	const root, other = "11111111-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "22222222-bbbb-4bbb-8bbb-bbbbbbbbbbbb"

	// spec: OR-16/c (no run open yet)
	if code, body := post(`{"root":"` + root + `","step":"early"}`); code != http.StatusConflict || body != `{"error":"foreign_effect"}` {
		t.Fatalf("before Open: %d %s", code, body)
	}
	s.Open(root)
	for _, step := range []string{"a", "b"} {
		if code, _ := post(`{"root":"` + root + `","job":"j","step":"` + step + `"}`); code != http.StatusOK {
			t.Fatalf("effect of step %s: %d", step, code)
		}
	}
	// spec: OR-16/c
	if code, body := post(`{"root":"` + other + `","step":"t"}`); code != http.StatusConflict || body != `{"error":"foreign_effect"}` {
		t.Fatalf("foreign root: %d %s", code, body)
	}
	if code, _ := post(`{"step":"no root"}`); code != http.StatusConflict {
		t.Fatalf("no root: %d", code)
	}
	if code, _ := post(`not json`); code != http.StatusBadRequest {
		t.Fatalf("not JSON: %d", code)
	}
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d", resp.StatusCode)
	}
	effects, foreign := s.Close(root)
	if len(effects) != 2 || !strings.Contains(string(effects[0]), `"step":"a"`) || !strings.Contains(string(effects[1]), `"step":"b"`) {
		t.Fatalf("effects %s", effects)
	}
	if foreign != 2 {
		t.Fatalf("foreign %d, want 2", foreign)
	}
	if e, _ := s.Close(root); len(e) != 0 {
		t.Fatal("Close forgets the run")
	}
	// spec: OR-16/c (the run is closed)
	if code, _ := post(`{"root":"` + root + `","step":"late"}`); code != http.StatusConflict {
		t.Fatalf("after Close: %d", code)
	}
}

// TestSinkKeepsConcurrentRunsApart is OR-V6/a's grouping on the sink: two
// outer runs open at once each get only the effects that name their own
// root, interleaved as they arrive.
func TestSinkKeepsConcurrentRunsApart(t *testing.T) {
	var s Sink
	srv := httptest.NewServer(&s)
	defer srv.Close()
	const r, tt = "11111111-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "22222222-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	s.Open(r)
	s.Open(tt)
	for _, e := range []string{`{"root":"` + r + `","step":"r1"}`, `{"root":"` + tt + `","step":"t1"}`, `{"root":"` + r + `","step":"r2"}`} {
		resp, err := http.Post(srv.URL, "application/json", strings.NewReader(e))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d", e, resp.StatusCode)
		}
	}
	got, _ := s.Close(r)
	if len(got) != 2 || !strings.Contains(string(got[0]), `"r1"`) || !strings.Contains(string(got[1]), `"r2"`) {
		t.Fatalf("run R: %s", got)
	}
	if got, _ := s.Close(tt); len(got) != 1 || !strings.Contains(string(got[0]), `"t1"`) {
		t.Fatalf("run T: %s", got)
	}
}

// TestSinkRoutesReplayCalls sends POST /replay to the replay service and
// everything else to the effect sink.
func TestSinkRoutesReplayCalls(t *testing.T) {
	var replayed int
	s := Sink{Replay: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		replayed++
		_, _ = io.WriteString(w, `{"result":1}`)
	})}
	srv := httptest.NewServer(&s)
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/replay", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	resp, err = http.Post(srv.URL+"/", "application/json", strings.NewReader(`{"root":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if replayed != 1 || resp.StatusCode != http.StatusConflict {
		t.Fatalf("replayed %d, effect status %d", replayed, resp.StatusCode)
	}
}

func TestSinkListen(t *testing.T) {
	var s Sink
	port, err := s.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	if port == 0 {
		t.Fatal("no port")
	}
}
