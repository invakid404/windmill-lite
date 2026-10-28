package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestReplayTableCases decodes the replay tables of testdata/replay: each
// case is a table that decodes to the given sites and outcomes, or is
// refused with the given text (ADR 0011 stub mode).
func TestReplayTableCases(t *testing.T) {
	data, err := os.ReadFile("testdata/replay/tables.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name   string                       `json:"name"`
		Table  json.RawMessage              `json:"table"`
		Want   map[string][]json.RawMessage `json:"want"`
		Refuse string                       `json:"refuse"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 10 {
		t.Fatalf("only %d cases", len(cases))
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			var tbl ReplayTable
			err := json.Unmarshal(c.Table, &tbl)
			if c.Refuse != "" {
				if err == nil || !strings.Contains(err.Error(), c.Refuse) {
					t.Fatalf("got %v, want a refusal containing %q", err, c.Refuse)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(tbl) != len(c.Want) {
				t.Fatalf("%d sites, want %d", len(tbl), len(c.Want))
			}
			for site, want := range c.Want {
				p, err := ParseSitePath(site)
				if err != nil {
					t.Fatal(err)
				}
				got := tbl[p]
				if len(got) != len(want) {
					t.Fatalf("site %s: %d outcomes, want %d", site, len(got), len(want))
				}
				for i := range want {
					data, _ := got[i].MarshalJSON()
					if !sameCompact(data, want[i]) {
						t.Errorf("site %s attempt %d: %s, want %s", site, i, data, want[i])
					}
				}
			}
		})
	}
}

func sameCompact(a, b []byte) bool {
	var ca, cb bytes.Buffer
	if json.Compact(&ca, a) != nil || json.Compact(&cb, b) != nil {
		return false
	}
	return bytes.Equal(ca.Bytes(), cb.Bytes())
}

// fakeLocator resolves jobs from a map; a job missing from it is not found.
type fakeLocator struct {
	mu    sync.Mutex
	sites map[string]string
	calls int
}

func (l *fakeLocator) Locate(ctx context.Context, root, job string) (SitePath, error) {
	l.mu.Lock()
	l.calls++
	s, ok := l.sites[job]
	l.mu.Unlock()
	if !ok {
		return SitePath{}, errors.New("not in the job tree")
	}
	return ParseSitePath(s)
}

func table(t *testing.T, js string) ReplayTable {
	t.Helper()
	var tbl ReplayTable
	if err := json.Unmarshal([]byte(js), &tbl); err != nil {
		t.Fatal(err)
	}
	return tbl
}

type replayClient struct {
	t   *testing.T
	url string
}

func (c replayClient) call(root, job string, args string) (int, string) {
	c.t.Helper()
	body := fmt.Sprintf(`{"root":%q,"root_flow":%q,"flow":"f-%s","job":%q,"step":"a","args":%s}`, root, root, job, job, args)
	resp, err := http.Post(c.url+ReplayPath, "application/json", strings.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// TestReplayServiceAnswers exercises the service as a stub sees it: the
// outcome of each attempt at a site, counted by the distinct jobs already
// seen there; the same answer for a repeated job; refusals for a foreign
// root, a malformed call, a site the table lacks and a job the locator
// cannot find; and the calls as the run's effects, in arrival order.
func TestReplayServiceAnswers(t *testing.T) {
	var s ReplayService
	mux := http.NewServeMux()
	mux.Handle(ReplayPath, &s)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := replayClient{t, srv.URL}
	const root = "r-1"
	loc := &fakeLocator{sites: map[string]string{
		"j0": "m:loop/i:1/m:a", "j1": "m:loop/i:1/m:a", "j2": "m:loop/i:1/m:a", "k": "m:b",
	}}
	s.Open(root, table(t, `{
		"m:loop/i:1/m:a": [{"error": {"message": "busy", "code": 7}}, {"result": {"z": 1, "a": 2}}],
		"m:b": {"result": null}
	}`), loc)

	if code, body := c.call("r-other", "j0", `[]`); code != http.StatusConflict || body != `{"error":"replay_foreign_root","root":"r-other"}` {
		t.Fatalf("foreign root: %d %s", code, body)
	}
	resp, err := http.Post(srv.URL+ReplayPath, "application/json", strings.NewReader(`{"root":"r-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a call without a job: %d", resp.StatusCode)
	}
	if code, body := c.call(root, "j0", `["x"]`); code != http.StatusOK || body != `{"error":{"message": "busy", "code": 7}}` {
		t.Fatalf("attempt 0: %d %s", code, body)
	}
	if code, body := c.call(root, "j0", `["x"]`); code != http.StatusOK || body != `{"error":{"message": "busy", "code": 7}}` {
		t.Fatalf("attempt 0 again: %d %s", code, body)
	}
	// The table's member order is kept.
	if code, body := c.call(root, "j1", `["x"]`); code != http.StatusOK || body != `{"result":{"z": 1, "a": 2}}` {
		t.Fatalf("attempt 1: %d %s", code, body)
	}
	if code, body := c.call(root, "j2", `["x"]`); code != http.StatusNotFound || body != `{"error":"replay_miss","site":"m:loop/i:1/m:a","attempt":2}` {
		t.Fatalf("attempt 2: %d %s", code, body)
	}
	if code, body := c.call(root, "k", `[]`); code != http.StatusOK || body != `{"result":null}` {
		t.Fatalf("null result: %d %s", code, body)
	}
	if code, body := c.call(root, "lost", `[]`); code != http.StatusGatewayTimeout || !strings.Contains(body, `"error":"replay_unresolved","job":"lost"`) {
		t.Fatalf("unresolved: %d %s", code, body)
	}
	calls := s.Close(root)
	if len(calls) != 5 {
		t.Fatalf("%d calls: %+v", len(calls), calls)
	}
	want := []struct {
		job     string
		attempt int
		dup     int
		err     string
	}{{"j0", 0, 1, ""}, {"j1", 1, 0, ""}, {"j2", 2, 0, "replay_miss"}, {"k", 0, 0, ""}, {"lost", 0, 0, "replay_unresolved"}}
	for i, w := range want {
		got := calls[i]
		if got.Job != w.job || got.Attempt != w.attempt || got.Duplicates != w.dup || !strings.Contains(got.Err, w.err) || (w.err == "") != (got.Err == "") {
			t.Errorf("call %d: %+v, want %+v", i, got, w)
		}
	}
	if string(calls[1].Effect) != `{"site":"m:loop/i:1/m:a","attempt":1,"step":"a","job":"j1","flow":"f-j1","root_flow":"r-1","root":"r-1","args":["x"]}` {
		t.Errorf("effect %s", calls[1].Effect)
	}
	if calls[4].Effect != nil {
		t.Errorf("an unresolved call has no site, so no effect: %s", calls[4].Effect)
	}
	if loc.calls != 5 {
		t.Errorf("the locator was asked %d times; a repeated job is answered without it", loc.calls)
	}
	if code, _ := c.call(root, "k", `[]`); code != http.StatusConflict {
		t.Fatalf("after Close the run is foreign: %d", code)
	}
	if s.Foreign() != 2 {
		t.Fatalf("foreign %d", s.Foreign())
	}
}

// TestReplayServiceConcurrentRuns keeps two outer runs open at once, each
// with its own table, and calls both from many goroutines: every call gets
// its own run's outcome and only its own run records it (OR-V6/a).
func TestReplayServiceConcurrentRuns(t *testing.T) {
	var s ReplayService
	srv := httptest.NewServer(&s)
	defer srv.Close()
	c := replayClient{t, srv.URL}
	locR := &fakeLocator{sites: map[string]string{}}
	locT := &fakeLocator{sites: map[string]string{}}
	tblR, tblT := ReplayTable{}, ReplayTable{}
	const n = 16
	for i := 0; i < n; i++ {
		site := SitePath{}.Child(Module("send"), Iteration(i), Module("post"))
		locR.sites[fmt.Sprintf("r%d", i)] = site.String()
		locT.sites[fmt.Sprintf("t%d", i)] = site.String()
		tblR[site] = []Outcome{{Result: json.RawMessage(fmt.Sprintf(`"R%d"`, i))}}
		tblT[site] = []Outcome{{Result: json.RawMessage(fmt.Sprintf(`"T%d"`, i))}}
	}
	s.Open("R", tblR, locR)
	s.Open("T", tblT, locT)
	var wg sync.WaitGroup
	errs := make(chan string, 2*n)
	for i := 0; i < n; i++ {
		for _, run := range []string{"R", "T"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				job := fmt.Sprintf("%s%d", strings.ToLower(run), i)
				srv := replayClient{t, c.url}
				if code, body := srv.call(run, job, `[]`); code != 200 || body != fmt.Sprintf(`{"result":"%s%d"}`, run, i) {
					errs <- fmt.Sprintf("%s: %d %s", job, code, body)
				}
			}()
		}
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	for _, run := range []string{"R", "T"} {
		calls := s.Close(run)
		if len(calls) != n {
			t.Fatalf("run %s: %d calls", run, len(calls))
		}
		for _, call := range calls {
			if !strings.HasPrefix(call.Job, strings.ToLower(run)) || call.Attempt != 0 || !strings.Contains(string(call.Effect), `"root":"`+run+`"`) {
				t.Errorf("run %s recorded %+v", run, call)
			}
		}
	}
}

// TestOutcomeRoundTrip writes outcomes the way the stub reads them.
func TestOutcomeRoundTrip(t *testing.T) {
	for _, js := range []string{`{"result":[1,{"b":2,"a":1}]}`, `{"result":null}`, `{"error":{"message":"m","name":"N","extra":1}}`} {
		var o Outcome
		if err := json.Unmarshal([]byte(js), &o); err != nil {
			t.Fatal(err)
		}
		got, _ := o.MarshalJSON()
		if string(got) != js {
			t.Errorf("%s round-trips to %s", js, got)
		}
	}
}

// TestRunBatches groups runs that share a group, at the first member's
// place, and leaves the others alone.
func TestRunBatches(t *testing.T) {
	runs := []Run{{Name: "a"}, {Name: "b", Group: "g"}, {Name: "c"}, {Name: "d", Group: "g"}, {Name: "e", Group: "h"}}
	var got []string
	for _, b := range RunBatches(runs) {
		var names []string
		for _, r := range b {
			names = append(names, r.Name)
		}
		got = append(got, strings.Join(names, "+"))
	}
	if strings.Join(got, " ") != "a b+d c e" {
		t.Fatalf("batches %q", got)
	}
}

// TestFixtureReplayNeedsStub refuses a replay table in a fixture that is
// not in stub mode, and loads one that is.
func TestFixtureReplayNeedsStub(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"windmill", "runs"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fixture := "id: x\nspec: [OR-15]\nscope: core\nprovenance: source-derived\nlanguages: [bun]\n"
	run := "target: f/x/y\ngroup: g\nreplay:\n  \"m:a\": {result: 1}\n"
	if err := os.WriteFile(filepath.Join(dir, FixtureFile), []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, RunsDir, "r.yaml"), []byte(run), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFixture(dir); err == nil || !strings.Contains(err.Error(), "replay needs stub: true") {
		t.Fatalf("got %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, FixtureFile), []byte(fixture+"stub: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := LoadFixture(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !f.Stub || f.Runs[0].Group != "g" {
		t.Fatalf("%+v", f)
	}
	o, ok := f.Runs[0].Replay.Lookup(SitePath{}.Child(Module("a")), 0)
	if !ok || string(o.Result) != "1" {
		t.Fatalf("outcome %+v %v", o, ok)
	}
	if _, ok := f.Runs[0].Replay.Lookup(SitePath{}.Child(Module("a")), 1); ok {
		t.Fatal("attempt 1 has no outcome")
	}
}

// slowLocator answers after a delay, so concurrent calls of one job race.
type slowLocator struct{ calls chan struct{} }

func (l slowLocator) Locate(ctx context.Context, root, job string) (SitePath, error) {
	l.calls <- struct{}{}
	<-ctx.Done()
	return SitePath{}.Child(Module("a")), nil
}

// TestReplayServiceRacedDuplicates calls one job many times at once: every
// call gets the one outcome of attempt 0, the job is recorded once, and
// the attempt count does not move.
func TestReplayServiceRacedDuplicates(t *testing.T) {
	s := ReplayService{LocateTimeout: 50 * time.Millisecond}
	srv := httptest.NewServer(&s)
	defer srv.Close()
	loc := slowLocator{calls: make(chan struct{}, 64)}
	s.Open("R", table(t, `{"m:a": [{"result": 0}, {"result": 1}]}`), loc)
	var wg sync.WaitGroup
	bodies := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, body := replayClient{t, srv.URL}.call("R", "j", `[]`)
			bodies <- body
		}()
	}
	wg.Wait()
	close(bodies)
	for b := range bodies {
		if b != `{"result":0}` {
			t.Errorf("a duplicate got %s", b)
		}
	}
	calls := s.Close("R")
	if len(calls) != 1 || calls[0].Attempt != 0 || calls[0].Duplicates != 7 {
		t.Fatalf("%+v", calls)
	}
}
