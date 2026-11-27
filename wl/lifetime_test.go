package wl

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
)

// lifetimeFiles is a synthetic folder: f/life/gate creates the file
// started and returns once the file release exists; f/life/one returns 1.
var lifetimeFiles = map[string]string{
	"wmill.yaml": "defaultTs: bun\n",
	"f/life/gate.ts": `import { existsSync, writeFileSync } from "node:fs";
// Creates started, then returns once release exists.
export async function main(started: string, release: string) {
  writeFileSync(started, "started");
  while (!existsSync(release)) await new Promise((r) => setTimeout(r, 5));
  return "released";
}
`,
	"f/life/one.ts": `// Returns 1.
export async function main() {
  return 1;
}
`,
	"f/life/gate.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/life/gate",` +
		`"input_transforms":{"started":{"type":"javascript","expr":"flow_input.started"},"release":{"type":"javascript","expr":"flow_input.release"}}}}]}}`,
	"f/life/one.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"script","path":"f/life/one","input_transforms":{}}}]}}`,
}

// lifetimePlugin reports as heldAge the number in the file that
// WL_TEST_AGE_FILE names, and its onRunEnd never returns for a run whose
// data is {"hang": true}.
const lifetimePlugin = `import { readFileSync } from "node:fs";
export default {
  heldAge() {
    const f = process.env.WL_TEST_AGE_FILE;
    return f ? Number(readFileSync(f, "utf8")) : undefined;
  },
  onRunEnd() {
    if ((globalThis as any).wl.run.data?.hang) return new Promise(() => {});
  },
};
`

// TestProcessesRecycleReasons checks that Processes names the recycle
// trigger that a warm process tripped, for each of the triggers on what a
// process holds past its runs. A run pinned to the process, held in a
// step, keeps it in the pool once it has tripped:
//   - held_age: the plugin reports an age at the bound, and Processes shows
//     the process out of service for "held_age", with the age it reported;
//   - unsettled_run: a run whose onRunEnd never returns, past the plugin
//     timeout, ends, and Processes shows the process out of service for
//     "unsettled_run".
//
// Before either trips, the process is serving and names no trigger.
func TestProcessesRecycleReasons(t *testing.T) {
	bun := bunOrSkip(t)
	out, runtimeDir := buildBunBundle(t, bun, lifetimeFiles)
	for _, c := range []struct {
		trigger string
		recycle RecycleTriggers
	}{
		{"held_age", RecycleTriggers{HeldAge: 2 * time.Minute}},
		// The bound is the smallest there is: the trigger trips at the
		// first heartbeat after the run's end.
		{"unsettled_run", RecycleTriggers{UnsettledRun: time.Nanosecond}},
	} {
		t.Run(c.trigger, func(t *testing.T) {
			dir := t.TempDir()
			ageFile := filepath.Join(dir, "age")
			if err := os.WriteFile(ageFile, []byte("0"), 0o600); err != nil {
				t.Fatal(err)
			}
			plugin := filepath.Join(dir, "plugin.ts")
			if err := os.WriteFile(plugin, []byte(lifetimePlugin), 0o644); err != nil {
				t.Fatal(err)
			}
			rt, err := Open(out, Options{
				Bun: bun, HarnessRuntime: runtimeDir, Plugin: plugin, PluginTimeout: 200 * time.Millisecond,
				HarnessEnv: map[string]EnvValue{"WL_TEST_AGE_FILE": {Value: ageFile}},
				Pool:       PoolOptions{Recycle: c.recycle},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer rt.Close()
			started, release := filepath.Join(dir, "started"), filepath.Join(dir, "release")
			t.Cleanup(func() { os.WriteFile(release, nil, 0o600) })
			args, _ := json.Marshal(map[string]string{"started": started, "release": release})
			held := make(chan error, 1)
			go func() {
				res, err := rt.Run(context.Background(), "f/life/gate", args, RunOptions{Pin: true})
				if err == nil && res.Status != StatusSuccess {
					err = fmt.Errorf("status %s", res.Status)
				}
				held <- err
			}()
			for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(5 * time.Millisecond) {
				if _, err := os.Stat(started); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("the pinned run's step did not start within 30 s")
				}
			}
			ps := rt.Processes()
			if len(ps) != 1 || !ps[0].Serving || ps[0].Recycle != "" {
				t.Fatalf("before a trip, Processes = %+v; want one serving process that names no trigger", ps)
			}
			first := ps[0].PID
			switch c.trigger {
			case "held_age":
				if err := os.WriteFile(ageFile, []byte("120000"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "unsettled_run":
				res, err := rt.Run(context.Background(), "f/life/one", nil, RunOptions{Data: json.RawMessage(`{"hang":true}`)})
				if err != nil || res.Status != StatusSuccess {
					t.Fatalf("the run whose onRunEnd hangs: %+v, %v", res, err)
				}
			}
			var got ProcessHealth
			for deadline := time.Now().Add(20 * time.Second); got.Recycle == ""; time.Sleep(10 * time.Millisecond) {
				for _, p := range rt.Processes() {
					if p.PID == first {
						got = p
					}
				}
				if time.Now().After(deadline) {
					t.Fatalf("Processes never named a trigger for %d: %+v", first, rt.Processes())
				}
			}
			if got.Recycle != c.trigger || got.Serving {
				t.Errorf("Processes shows %+v; want it out of service for %q", got, c.trigger)
			}
			if c.trigger == "held_age" && got.HeldAge != 2*time.Minute {
				t.Errorf("HeldAge = %s; want the reported 2m0s", got.HeldAge)
			}
			os.WriteFile(release, nil, 0o600)
			if err := <-held; err != nil {
				t.Fatalf("the pinned run: %v", err)
			}
		})
	}
}

// lateSink is a Diagnostics writer that records each write once it
// returns. A write that holds hold, when hold is not empty, waits until
// release closes, and tells blocked that one waits.
type lateSink struct {
	hold    string
	release chan struct{}
	blocked chan struct{}
	once    sync.Once
	mu      sync.Mutex
	got     strings.Builder
}

func (s *lateSink) Write(b []byte) (int, error) {
	if s.hold != "" && strings.Contains(string(b), s.hold) {
		s.once.Do(func() { close(s.blocked) })
		<-s.release
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.got.Write(b)
}

func (s *lateSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.got.String()
}

// TestDiagnosticsLineAfterFailedOpen checks when a pool's line reaches
// Options.Diagnostics, as its documentation says, for a warm start that
// fails, its Bun gone:
//   - default: the failed start's line is in the writer when Open returns;
//   - held_age: with a held-age trigger set, the line is best effort and
//     can come after Open has returned. Here the writer holds it until
//     after Open has failed, and gets it once it goes on.
func TestDiagnosticsLineAfterFailedOpen(t *testing.T) {
	bun := bunOrSkip(t)
	out, runtimeDir := buildBunBundle(t, bun, lifetimeFiles)
	const line = "starting a process failed"
	open := func(t *testing.T, sink *lateSink, recycle RecycleTriggers) {
		t.Helper()
		rt, err := Open(out, Options{Bun: filepath.Join(t.TempDir(), "gone"), HarnessRuntime: runtimeDir, Diagnostics: sink,
			Pool: PoolOptions{WarmStart: true, Recycle: recycle}})
		if err == nil {
			rt.Close()
			t.Fatal("Open with its Bun gone succeeded")
		}
	}
	t.Run("default", func(t *testing.T) {
		sink := &lateSink{}
		open(t, sink, RecycleTriggers{})
		if !strings.Contains(sink.String(), line) {
			t.Fatalf("when Open returned, the writer lacked the failed start's line:\n%s", sink.String())
		}
	})
	t.Run("held_age", func(t *testing.T) {
		sink := &lateSink{hold: line, release: make(chan struct{}), blocked: make(chan struct{})}
		t.Cleanup(func() {
			select {
			case <-sink.release:
			default:
				close(sink.release)
			}
		})
		open(t, sink, RecycleTriggers{HeldAge: time.Minute})
		select {
		case <-sink.blocked:
		case <-time.After(10 * time.Second):
			t.Fatal("the failed start's line never reached the writer")
		}
		if strings.Contains(sink.String(), line) {
			t.Fatal("the held line was written before its release")
		}
		close(sink.release)
		for deadline := time.Now().Add(10 * time.Second); !strings.Contains(sink.String(), line); time.Sleep(5 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal("the failed start's line never came after Open failed")
			}
		}
	})
}
