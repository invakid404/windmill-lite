package harness

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/bench"
	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/jsonval"
	"github.com/invakid404/windmill-lite/internal/platform"
)

// The boundary benchmarks of milestone G1 (ADR 0005, benchmark plan item
// 2) split one step's round trip between the engine and its harness into
// the parts this package and the harness runtime own, each measured on its
// own, on the payloads of bench.Synthetic from 26 B to 4 MiB. A step's
// argument is the payload ({"p": <payload>}) and its result is the
// payload again, as the echo step of cmd/windmill-lite's end-to-end
// benchmarks returns it:
//
//   - BenchmarkGoInvocation: the engine side's per-invocation codec, the
//     script's arguments without the worker's internal keys (scriptArgs)
//     and the invoke frame's payload (encode-invoke), and the result
//     frame's payload decoded (decode-result);
//   - BenchmarkArgsFile: the args.json file each invocation writes into
//     the harness's working directory (WR-5);
//   - BenchmarkHarnessCodec: the harness's parse and serialise, on the
//     pinned Bun, with the runtime's own frame.ts and wrapper.ts
//     (testdata/bench.ts codec);
//   - BenchmarkTransport: the transport alone: this package's connection
//     (its writer, part streams, reader and the result's decode) against
//     the runtime's FrameReader, Assembler and part splitting over a
//     socketpair, one invocation at a time and eight at once, the harness
//     answering without parsing the invoke (testdata/bench.ts echo).
//
// They are microbenchmarks of each part on its own, means of repeated
// calls; they do not divide a real step, whose stages cmd/windmill-lite's
// BenchmarkStepBoundary measures on the same invocations with the times
// hook (SetTimesHook) and the runtime's benchmark timings. They need the
// pinned Bun in WL_BUN, except the first two, and run only with -bench.

// benchArgs is a step's arguments around the payload of size n.
func benchArgs(n int) jsonval.Value {
	return jsonval.FromProducer(fmt.Appendf(nil, `{"p":%s}`, bench.Synthetic(n)))
}

// benchInvoke is the invoke the engine sends for an echo step with the
// payload of size n, its job variables made as the engine makes them.
func benchInvoke(id string, args jsonval.Value) Invoke {
	run := "0191b5d2-6c34-7c1e-9a51-6d7a1c4e2f00"
	env := platform.Identity{
		JobID: "0191b5d2-6c34-7c1e-9a51-6d7a1c4e2f01", FlowJobID: run, RootFlowJobID: run, RootJobID: run,
		FlowPath: "f/bench/chain", StepID: "a", JobPath: "f/bench/echo", Token: platform.NewToken(),
	}.Env(platform.Host{})
	return Invoke{ID: id, Run: run, Site: "s:a", Script: "f/bench/echo", Entry: "main", Args: args, Env: env, Boundary: "bun"}
}

// benchResult is the result frame's payload of an echo step with the
// payload of size n.
func benchResult(id string, n int) []byte {
	b, err := Encode(Result{ID: id, Outcome: "ok", Body: jsonval.FromProducer(bench.Synthetic(n)), Settled: true})
	if err != nil {
		panic(err)
	}
	return b
}

func BenchmarkGoInvocation(b *testing.B) {
	for _, n := range bench.SyntheticSizes {
		args := benchArgs(n)
		inv := benchInvoke("b0", args)
		b.Run("size="+bench.SizeLabel(n)+"/part=encode-invoke", func(b *testing.B) {
			b.SetBytes(int64(n))
			for b.Loop() {
				m := inv
				m.Args = scriptArgs(args)
				if _, err := Encode(m); err != nil {
					b.Fatal(err)
				}
			}
		})
		payload := benchResult("b0", n)
		b.Run("size="+bench.SizeLabel(n)+"/part=decode-result", func(b *testing.B) {
			b.SetBytes(int64(n))
			for b.Loop() {
				if _, err := Decode(payload); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkArgsFile(b *testing.B) {
	for _, n := range bench.SyntheticSizes {
		args := benchArgs(n)
		b.Run("size="+bench.SizeLabel(n), func(b *testing.B) {
			p := &Process{dir: b.TempDir()}
			b.SetBytes(int64(n))
			for b.Loop() {
				if err := p.writeArgs(args); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// benchFiles writes the invoke payload and the result body of size n into
// dir for testdata/bench.ts.
func benchFiles(b *testing.B, dir string, n int) (invoke, body string) {
	b.Helper()
	payload, err := Encode(benchInvoke("b0", benchArgs(n)))
	if err != nil {
		b.Fatal(err)
	}
	invoke, body = filepath.Join(dir, "invoke.json"), filepath.Join(dir, "body.json")
	if err := os.WriteFile(invoke, payload, 0o644); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(body, bench.Synthetic(n), 0o644); err != nil {
		b.Fatal(err)
	}
	return invoke, body
}

func BenchmarkHarnessCodec(b *testing.B) {
	bun := bunPath(b)
	script := repoPath(b, "internal/harness/testdata/bench.ts")
	for _, n := range bench.SyntheticSizes {
		b.Run("size="+bench.SizeLabel(n), func(b *testing.B) {
			invoke, body := benchFiles(b, b.TempDir(), n)
			out, err := exec.Command(bun, script, "codec", invoke, body, strconv.Itoa(b.N)).Output()
			if err != nil {
				b.Fatalf("bench.ts codec: %v", err)
			}
			var r struct {
				Decode float64 `json:"decode_invoke_ns"`
				Text   float64 `json:"result_text_ns"`
				Encode float64 `json:"encode_result_ns"`
			}
			if err := json.Unmarshal(out, &r); err != nil {
				b.Fatalf("bench.ts codec: %v: %s", err, out)
			}
			// The Bun process's own timing replaces the wall time, which
			// includes its start.
			b.ReportMetric(r.Decode+r.Text+r.Encode, "ns/op")
			b.ReportMetric(r.Decode, "decode-invoke-ns/op")
			b.ReportMetric(r.Text, "result-text-ns/op")
			b.ReportMetric(r.Encode, "encode-result-ns/op")
			b.ReportMetric(float64(n)*1e3/(r.Decode+r.Text+r.Encode), "MB/s")
		})
	}
}

func BenchmarkTransport(b *testing.B) {
	bun := bunPath(b)
	script := repoPath(b, "internal/harness/testdata/bench.ts")
	for _, conc := range []int{1, 8} {
		for _, n := range bench.SyntheticSizes {
			b.Run(fmt.Sprintf("conc=%d/size=%s", conc, bench.SizeLabel(n)), func(b *testing.B) {
				benchTransport(b, bun, script, conc, n)
			})
		}
	}
}

// echo is a connection to testdata/bench.ts's echo, which answers each
// invoke of the payload of size n with the result of that size.
type echo struct {
	c        *conn
	ids      []string
	payloads [][]byte
	answered map[string]chan struct{}
	closed   chan error
}

// startEcho starts the echo with conc invoke ids, b0 to b<conc-1>. Its
// cleanup closes the connection, waits up to 5 s for the echo to exit,
// and then kills it, and is registered as soon as the echo has started.
func startEcho(tb testing.TB, bun, script string, conc, n int) *echo {
	tb.Helper()
	dir := tb.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "body.json"), bench.Synthetic(n), 0o644); err != nil {
		tb.Fatal(err)
	}
	parent, child, err := socketpair()
	if err != nil {
		tb.Fatal(err)
	}
	cmd := exec.Command(bun, script, "echo", filepath.Join(dir, "body.json"))
	cmd.ExtraFiles = []*os.File{child}
	cmd.Stderr = os.Stderr
	err = cmd.Start()
	child.Close()
	if err != nil {
		parent.Close()
		tb.Fatal(err)
	}
	e := &echo{answered: make(map[string]chan struct{}, conc), closed: make(chan error, 1)}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	tb.Cleanup(func() {
		if e.c != nil {
			e.c.close()
		}
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			cmd.Process.Kill()
			<-exited
		}
	})
	nc, err := net.FileConn(parent)
	parent.Close()
	if err != nil {
		tb.Fatal(err)
	}
	args := benchArgs(n)
	for i := range conc {
		id := "b" + strconv.Itoa(i)
		payload, err := Encode(benchInvoke(id, args))
		if err != nil {
			nc.Close()
			tb.Fatal(err)
		}
		e.ids, e.payloads = append(e.ids, id), append(e.payloads, payload)
		e.answered[id] = make(chan struct{}, 1)
	}
	e.c = newConn(nc, connConfig{
		onMessage: func(m Message) {
			if r, ok := m.(Result); ok {
				if ch := e.answered[r.ID]; ch != nil && len(r.Body.Producer()) == n {
					ch <- struct{}{}
				}
			}
		},
		onClose: func(err error) { e.closed <- err },
	})
	e.c.run()
	return e
}

// trip sends invoke k and waits for its result.
func (e *echo) trip(k int) (time.Duration, error) {
	start := time.Now()
	if err := e.c.sendPayload(e.payloads[k], e.ids[k]); err != nil {
		return 0, err
	}
	select {
	case <-e.answered[e.ids[k]]:
		return time.Since(start), nil
	case err := <-e.closed:
		return 0, fmt.Errorf("the echo's connection ended: %v", err)
	case <-time.After(30 * time.Second):
		return 0, fmt.Errorf("no answer to %s", e.ids[k])
	}
}

// benchTransport runs b.N rounds of conc round trips of an invoke and its
// result of size n against the echo, one per worker, the workers started
// together (bench.Workers), so even b.N = 1 has conc in flight at once. It
// reports the time per round trip at that concurrency (ns/op), the round
// trip's latency at p50 and p95, and the concurrency reached.
func benchTransport(b *testing.B, bun, script string, conc, n int) {
	e := startEcho(b, bun, script, conc, n)
	// Warm-up: the echo's JIT and both sides' buffers.
	for range 20 {
		if _, err := e.trip(0); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	c, err := bench.Workers(conc, b.N, func(k int) error {
		_, err := e.trip(k)
		return err
	})
	b.StopTimer()
	if err != nil {
		b.Fatal(err)
	}
	var all []time.Duration
	for _, w := range c.Ops {
		all = append(all, w...)
	}
	trips := float64(conc * b.N)
	b.ReportMetric(float64(c.Wall)/trips, "ns/op")
	b.ReportMetric(float64(bench.Percentile(all, 0.50)), "p50-ns")
	b.ReportMetric(float64(bench.Percentile(all, 0.95)), "p95-ns")
	b.ReportMetric(float64(c.Peak), "conc-peak")
	b.ReportMetric(c.Mean, "conc-mean")
	b.ReportMetric(trips*float64(2*n)/c.Wall.Seconds()/1e6, "MB/s")
}

// TestBenchEcho checks the transport benchmark's echo once per payload
// class, a whole frame and part streams both ways, with two invocations in
// flight, so that a broken echo fails here rather than in a benchmark run.
func TestBenchEcho(t *testing.T) {
	bun := bunPath(t)
	script := repoPath(t, "internal/harness/testdata/bench.ts")
	for _, n := range []int{bench.SyntheticMin, 2 << 20} {
		t.Run(bench.SizeLabel(n), func(t *testing.T) {
			e := startEcho(t, bun, script, 2, n)
			var wg sync.WaitGroup
			for k := range 2 {
				wg.Go(func() {
					for range 3 {
						if _, err := e.trip(k); err != nil {
							t.Error(err)
							return
						}
					}
				})
			}
			wg.Wait()
		})
	}
}

// TestTimesHook runs one invocation on the real Bun harness with the times
// hook set and checks its readings: one report, for that invocation, with
// every reading taken and in the order of the invocation's stages.
func TestTimesHook(t *testing.T) {
	cfg, _ := bunConfig(t)
	p := spawn(t, cfg)
	var mu sync.Mutex
	var got []InvocationTimes
	SetTimesHook(func(tm InvocationTimes) {
		mu.Lock()
		got = append(got, tm)
		mu.Unlock()
	})
	t.Cleanup(func() { SetTimesHook(nil) })
	inv := newInvocation("echo", `{"value":"timed"}`, newRecorder())
	out, err := execute(t, p, inv, 5*time.Second)
	if err != nil || out.Kind != engine.OutcomeOK {
		t.Fatalf("got %+v, %v", out, err)
	}
	SetTimesHook(nil)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0].ID != inv.ID {
		t.Fatalf("reports %+v, want one for %s", got, inv.ID)
	}
	tm := got[0]
	stages := []time.Time{tm.Start, tm.Encoded, tm.ArgsWritten, tm.Queued, tm.Received, tm.Decoded, tm.Done}
	for i, at := range stages {
		if at.IsZero() || i > 0 && at.Before(stages[i-1]) {
			t.Fatalf("reading %d of %+v is missing or out of order", i, tm)
		}
	}
	// Without the hook, nothing is reported.
	if _, err := execute(t, p, newInvocation("echo", `{"value":"untimed"}`, newRecorder()), 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("%d reports after the hook was cleared", len(got))
	}
}
