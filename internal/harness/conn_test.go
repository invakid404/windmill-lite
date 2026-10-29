package harness

import (
	"io"
	"net"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/jsonval"
)

func connPair(t *testing.T) (*conn, net.Conn) {
	t.Helper()
	a, b, err := socketpair()
	if err != nil {
		t.Fatal(err)
	}
	ac, err := net.FileConn(a)
	a.Close()
	if err != nil {
		t.Fatal(err)
	}
	bc, err := net.FileConn(b)
	b.Close()
	if err != nil {
		t.Fatal(err)
	}
	c := newConn(ac, connConfig{onMessage: func(Message) {}, onClose: func(error) {}})
	c.run()
	t.Cleanup(func() {
		c.close()
		bc.Close()
	})
	return c, bc
}

// readTypes reads n frames and returns, for each, its type and for parts
// the stream id.
func readTypes(t *testing.T, r io.Reader, n int) []string {
	t.Helper()
	var out []string
	for range n {
		p, err := readFrame(r, MaxFrame)
		if err != nil {
			t.Fatal(err)
		}
		m, err := Decode(p)
		if err != nil {
			t.Fatal(err)
		}
		if part, ok := m.(Part); ok {
			out = append(out, "part:"+part.ID)
		} else {
			out = append(out, m.Type())
		}
	}
	return out
}

func bigResult(id string, n int) Result {
	return Result{ID: id, Outcome: OutcomeOK, Body: jsonval.FromProducer([]byte(`"` + strings.Repeat("x", n) + `"`)), Settled: true}
}

// ADR 0004 §Transport and framing item 3: a small frame queued behind a
// large message goes out after at most one of its part frames.
func TestSmallFramesOvertakeLargeMessages(t *testing.T) {
	c, peer := connPair(t)
	if err := c.send(bigResult("big", 8*PartChunk)); err != nil {
		t.Fatal(err)
	}
	if err := c.send(Cancel{ID: "i1", Reason: "timeout"}); err != nil {
		t.Fatal(err)
	}
	types := readTypes(t, peer, 10)
	at := -1
	for i, ty := range types {
		if ty == TypeCancel {
			at = i
		}
	}
	if at < 0 || at > 1 {
		t.Fatalf("cancel at frame %d of %v, want before the second part", at, types)
	}
}

// Two part streams take turns, one part frame each.
func TestPartStreamsTakeTurns(t *testing.T) {
	c, peer := connPair(t)
	// Queue both streams under the lock, so neither starts before the
	// other is queued.
	c.mu.Lock()
	c.streams = append(c.streams,
		&outStream{id: "1", payload: mustEncode(t, bigResult("a", 3*PartChunk))},
		&outStream{id: "2", payload: mustEncode(t, bigResult("b", 3*PartChunk))})
	c.mu.Unlock()
	c.wake <- struct{}{}
	got := strings.Join(readTypes(t, peer, 6), " ")
	if got != "part:1 part:2 part:1 part:2 part:1 part:2" {
		t.Fatalf("frames %s", got)
	}
}

// Messages about one invocation keep their order: a cancel queued behind a
// large invoke, and a second large message about it, wait for the first
// stream's last part, while frames about other invocations do not.
func TestFramesAboutOneInvocationKeepOrder(t *testing.T) {
	c, peer := connPair(t)
	args := jsonval.FromProducer([]byte(`{"data":"` + strings.Repeat("y", 3*PartChunk) + `"}`))
	for _, m := range []Message{
		Invoke{ID: "i1", Run: "r", Script: "s", Entry: "main", Args: args, Env: map[string]string{}, Boundary: "bun"},
		Cancel{ID: "i1", Reason: "timeout"},
		bigResult("i1", 2*PartChunk),
		Log{ID: "i1", Lines: []string{"after"}},
		Cancel{ID: "i2", Reason: "timeout"},
		Ping{ID: "e1"},
	} {
		if err := c.send(m); err != nil {
			t.Fatal(err)
		}
	}
	r := &frameStream{r: peer, asm: newAssembler(DefaultMaxMessage)}
	var mine, all []string
	for len(mine) < 4 {
		m := r.next(t)
		id := invocationOf(m)
		all = append(all, m.Type()+":"+id)
		if id == "i1" {
			mine = append(mine, m.Type())
		}
	}
	if got := strings.Join(mine, " "); got != "invoke cancel result log" {
		t.Fatalf("messages about i1 in order %s; all: %v", got, all)
	}
	if all[0] != "cancel:i2" && all[1] != "cancel:i2" {
		t.Fatalf("a frame about another invocation was held back: %v", all)
	}
}

// Milestone PF1: a platform call is about the invocation its inv names, so
// a result queued behind its large platform call waits for it, and the
// facade has accepted the call when the result arrives; another
// invocation's result is not held back.
func TestResultWaitsForItsPlatformCall(t *testing.T) {
	c, peer := connPair(t)
	for _, m := range []Message{
		PlatformRequest{ID: "p1", Inv: "i1", Method: "POST", URL: "http://127.0.0.1:1/api/x", Headers: map[string]string{}, Body: []byte(strings.Repeat("z", 3*PartChunk))},
		Result{ID: "i1", Outcome: OutcomeOK, Body: jsonval.FromProducer([]byte(`1`)), Settled: true},
		Result{ID: "i2", Outcome: OutcomeOK, Body: jsonval.FromProducer([]byte(`2`)), Settled: true},
	} {
		if err := c.send(m); err != nil {
			t.Fatal(err)
		}
	}
	r := &frameStream{r: peer, asm: newAssembler(DefaultMaxMessage)}
	var got []string
	for range 3 {
		m := r.next(t)
		got = append(got, m.Type()+":"+invocationOf(m))
	}
	if strings.Join(got, " ") != "result:i2 platform:i1 result:i1" {
		t.Fatalf("order %v", got)
	}
}

type frameStream struct {
	r   io.Reader
	asm *assembler
}

func (f *frameStream) next(t *testing.T) Message {
	t.Helper()
	for {
		p, err := readFrame(f.r, MaxFrame)
		if err != nil {
			t.Fatal(err)
		}
		m, err := Decode(p)
		if err != nil {
			t.Fatal(err)
		}
		part, ok := m.(Part)
		if !ok {
			return m
		}
		full, done, err := f.asm.add(part)
		if err != nil {
			t.Fatal(err)
		}
		if done {
			if m, err = decodeAssembled(full); err != nil {
				t.Fatal(err)
			}
			return m
		}
	}
}

func mustEncode(t *testing.T, m Message) []byte {
	t.Helper()
	b, err := Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestMatchJSON(t *testing.T) {
	vars := map[string]string{"<pid>": "42", "<bundle>": `"sha256:ab"`}
	for _, tc := range []struct {
		want, got string
		ok        bool
	}{
		{`{"a":1,"b":"<string>"}`, `{"a":1,"b":"x"}`, true},
		{`{"a":1,"b":"<string>"}`, `{"b":"x","a":1}`, false}, // key order counts
		{`{"a":1}`, `{"a":1.0}`, false},                      // so does number spelling
		{`{"p":"<pid>","b":"<bundle>"}`, `{"p":42,"b":"sha256:ab"}`, true},
		{`{"p":"<pid>"}`, `{"p":43}`, false},
		{`"<x*3>"`, `"xxx"`, true},
		{`"<x*3>"`, `"xxxx"`, false},
		{`["<int>","<any>"]`, `[7,{"z":null}]`, true},
		{`["<int>"]`, `[7.5]`, false},
		{`{"a":null}`, `{"a":null,"b":1}`, false},
	} {
		err := matchJSON([]byte(tc.want), []byte(tc.got), vars)
		if (err == nil) != tc.ok {
			t.Errorf("match(%s, %s) = %v, want ok=%v", tc.want, tc.got, err, tc.ok)
		}
	}
}

// A barrier (sendAfter) waits behind every open stream about a key with
// its prefix, and behind what waits about those keys, while an unrelated
// small frame queued after it still overtakes the streams: a host-mode bye
// goes out only after every large run_done queued before it.
func TestBarrierWaitsForRunStreams(t *testing.T) {
	c, peer := connPair(t)
	big := func(id string) RunDone {
		return RunDone{ID: id, Status: "success", Result: jsonval.FromProducer([]byte(`"` + strings.Repeat("x", 3*PartChunk) + `"`))}
	}
	for _, m := range []Message{big("a"), big("b"), RunSettled{ID: "a"}, RunDone{ID: "c", Status: "success"}} {
		if err := c.send(m); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.sendAfter(Bye{}, "run:"); err != nil {
		t.Fatal(err)
	}
	if err := c.send(Pong{ID: "p"}); err != nil {
		t.Fatal(err)
	}
	// 4 parts per stream (3 chunks of payload plus the envelope), the
	// run_settled, the small run_done, the pong and the bye.
	types := readTypes(t, peer, 12)
	index := func(ty string) int {
		for i, x := range types {
			if x == ty {
				return i
			}
		}
		return -1
	}
	lastPart := -1
	for i, x := range types {
		if strings.HasPrefix(x, "part:") {
			lastPart = i
		}
	}
	bye, pong, settled := index(TypeBye), index(TypePong), index(TypeRunSettled)
	if bye != len(types)-1 || bye < lastPart || bye < settled || bye < index(TypeRunDone) {
		t.Fatalf("frames %v: bye must follow every part and every run frame", types)
	}
	if pong < 0 || pong > lastPart {
		t.Fatalf("frames %v: the pong must overtake the streams", types)
	}
}

// With no run stream open, a barrier goes out at once, in order with the
// small frames queued before it.
func TestBarrierWithoutStreams(t *testing.T) {
	c, peer := connPair(t)
	if err := c.send(RunDone{ID: "c", Status: "success"}); err != nil {
		t.Fatal(err)
	}
	if err := c.sendAfter(Bye{}, "run:"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(readTypes(t, peer, 2), " "); got != "run_done bye" {
		t.Fatalf("frames %s", got)
	}
}
