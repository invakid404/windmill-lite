package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// runDataPlugin is a host plugin whose onRunEnd hook answers with one
// warning, whose message is what it read as wl.run.data.
const runDataPlugin = `export default {
  onRunEnd: () => [{ code: "data", message: JSON.stringify((globalThis as any).wl.run.data ?? "undefined") }],
};
`

// runDataProcess spawns the real Bun harness with runDataPlugin and the
// RunData rd.
func runDataProcess(t *testing.T, rd *RunData) *Process {
	t.Helper()
	cfg, _ := bunConfig(t)
	plugin := filepath.Join(t.TempDir(), "plugin.ts")
	if err := os.WriteFile(plugin, []byte(runDataPlugin), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.Args = append(cfg.Args, "--plugin", plugin)
	cfg.RunData = rd
	return spawn(t, cfg)
}

// hookReads asks the plugin's onRunEnd hook what it reads as wl.run.data
// for run.
func hookReads(p *Process, run string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	reply, err := p.HostCall(ctx, "on_run_end", run, "", jsonval.FromProducer([]byte(`{"trace":{"run":"`+run+`"}}`)))
	if err != nil {
		return "", err
	}
	var got struct{ Warnings []struct{ Message string } }
	if err := json.Unmarshal(reply.Producer(), &got); err != nil || len(got.Warnings) != 1 {
		return "", fmt.Errorf("reply %s: %v", reply.Producer(), err)
	}
	return got.Warnings[0].Message, nil
}

// mustHookRead is hookReads on the test's goroutine.
func mustHookRead(t *testing.T, p *Process, run string) string {
	t.Helper()
	got, err := hookReads(p, run)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// heldInvoke starts a gated invocation whose run has data, with arguments
// large enough to go out in parts, and holds its delivery after the first
// part. resume lets the rest go; done gives Execute's error, once the test
// opens the gate.
func heldInvoke(t *testing.T, p *Process, rd *RunData) (run string, resume func(), done <-chan error, g gate, inv string) {
	t.Helper()
	g = newGate(t, t.TempDir(), "large")
	args, _ := json.Marshal(map[string]string{"started": g.started, "release": g.release, "pad": strings.Repeat("y", 4<<20)})
	in := newInvocation("gate", string(args), newRecorder())
	rd.Set(in.Run, jsonval.FromProducer([]byte(`{"tenant":"t-9"}`)))
	held, release := make(chan struct{}), make(chan struct{})
	var holdOnce, releaseOnce sync.Once
	hold := func(about string, last bool) {
		if about == in.ID && !last {
			holdOnce.Do(func() {
				close(held)
				<-release
			})
		}
	}
	partWritten.Store(&hold)
	resume = func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(func() {
		partWritten.Store(nil)
		resume()
	})
	errs := make(chan error, 1)
	go func() {
		_, err := p.Execute(context.Background(), in)
		errs <- err
	}()
	select {
	case <-held:
	case <-time.After(10 * time.Second):
		t.Fatal("the invoke's first part was never written")
	}
	return in.Run, resume, errs, g, in.ID
}

// invokeSent waits until the invoke inv has sent its last part.
func invokeSent(t *testing.T, p *Process, inv string) {
	t.Helper()
	waitFor(t, "the invoke's last part", func() bool {
		p.conn.mu.Lock()
		defer p.conn.mu.Unlock()
		return p.conn.latest[inv] == nil
	})
}

// runsHeld reports how many runs' data the process says it holds.
func runsHeld(t *testing.T, p *Process) int {
	t.Helper()
	h, err := p.Ping(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return h.RunsHeld
}

// TestRunDataOvertakingFrame: a run's first invoke to a process is large,
// so it goes out in parts, and the test holds its delivery after the first
// part. A host_call of the same run, queued meanwhile, is small and reaches
// the process first, so it carries the run's data too: the hook reads it.
// Once the invoke's last part has gone out, a later hook of the run still
// reads the data, which the process holds until the run is released.
func TestRunDataOvertakingFrame(t *testing.T) {
	rd := NewRunData()
	p := runDataProcess(t, rd)
	run, resume, done, g, inv := heldInvoke(t, p, rd)
	first := make(chan string, 1)
	go func() {
		got, err := hookReads(p, run)
		if err != nil {
			got = err.Error()
		}
		first <- got
	}()
	waitFor(t, "the host_call queued behind the held part", func() bool {
		p.conn.mu.Lock()
		defer p.conn.mu.Unlock()
		return len(p.conn.small) == 1
	})
	resume()
	if got := <-first; got != `{"tenant":"t-9"}` {
		t.Errorf("the hook that overtook the invoke read %s, want the run's data", got)
	}
	invokeSent(t, p, inv)
	if got := mustHookRead(t, p, run); got != `{"tenant":"t-9"}` {
		t.Errorf("a later hook of the run read %s, want the run's data", got)
	}
	if n := runsHeld(t, p); n != 1 {
		t.Errorf("before the release, the process holds %d runs' data, want 1", n)
	}
	if err := os.WriteFile(g.release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	rd.Release(run)
	if rd.Len() != 0 {
		t.Errorf("RunData keeps %d runs after the release", rd.Len())
	}
	waitFor(t, "the release", func() bool { return runsHeld(t, p) == 0 })
	if got := mustHookRead(t, p, run); got != `"undefined"` {
		t.Errorf("after the release, a hook of the run read %s, want undefined", got)
	}
}

// TestRunDataReleasedWhileQueued: a run is released while its first
// invoke to a process, which carries its data, is still held after its
// first part. The data reaches the process with the invoke's last part,
// and a run_released follows it: the process ends up holding none.
func TestRunDataReleasedWhileQueued(t *testing.T) {
	rd := NewRunData()
	p := runDataProcess(t, rd)
	run, resume, done, g, inv := heldInvoke(t, p, rd)
	rd.Release(run)
	resume()
	invokeSent(t, p, inv)
	waitFor(t, "the release after the invoke", func() bool { return runsHeld(t, p) == 0 })
	if got := mustHookRead(t, p, run); got != `"undefined"` {
		t.Errorf("after the release, a hook of the run read %s, want undefined", got)
	}
	if err := os.WriteFile(g.release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
