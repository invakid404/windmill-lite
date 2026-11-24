package wl

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/harness"
)

// A pinned run never moves to another process. When its process ends
// between two of its steps (the test kills it from the first step's
// OnModuleEnd hook, and waits until the pool has seen the exit), the next
// step latches the run with ErrPinnedProcessLost and never launches. When
// the process ends during the run's last step, that step fails as escaped,
// and the run fails as its error, with no latch.
func TestPinnedRunProcessLost(t *testing.T) {
	bun := bunOrSkip(t)
	pidStep := `{"type":"rawscript","language":"bun","content":"export async function main() { return process.pid }","input_transforms":{}}`
	out, runtimeDir := buildBunBundle(t, bun, map[string]string{
		"wmill.yaml": "defaultTs: bun\n",
		"f/t/two.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":` + pidStep + `},` +
			`{"id":"b","value":` + pidStep + `}]}}`,
		"f/t/die.flow/flow.yaml": `{"summary":"t","value":{"modules":[{"id":"a","value":{"type":"rawscript","language":"bun",` +
			`"content":"export async function main() { process.kill(process.pid, 'SIGKILL'); await new Promise(() => {}) }","input_transforms":{}}}]}}`,
	})
	rt, err := Open(out, Options{Bun: bun, HarnessRuntime: runtimeDir})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	gone := func(pid int) bool {
		return !slices.ContainsFunc(rt.pool.Stats(), func(s harness.ProcessStats) bool { return s.PID == pid })
	}
	hooks := &Hooks{OnModuleEnd: func(ctx context.Context, run RunHandle, site Site, rec StepRecord) error {
		if site.Module != "a" {
			return nil
		}
		pid, err := strconv.Atoi(string(rec.Result.Producer()))
		if err != nil {
			return err
		}
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
			return err
		}
		for deadline := time.Now().Add(30 * time.Second); !gone(pid); time.Sleep(5 * time.Millisecond) {
			if time.Now().After(deadline) {
				return errors.New("the pool did not see the exit")
			}
		}
		return nil
	}}
	res, err := rt.Run(context.Background(), "f/t/two", json.RawMessage(`{}`), RunOptions{Pin: true, Hooks: hooks})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusLatched || !errors.Is(res.Latched, ErrPinnedProcessLost) {
		t.Fatalf("%s, latched %v; want latched with pinned_process_lost", res.Status, res.Latched)
	}
	for _, s := range res.Trace.Steps {
		if s.Site.String() == "m:b" && s.Ran {
			t.Fatalf("step b ran after its pinned process ended: %+v", s)
		}
	}
	// The same flow unpinned runs its second step in the replacement.
	if res, err := rt.Run(context.Background(), "f/t/two", json.RawMessage(`{}`), RunOptions{Hooks: hooks}); err != nil || res.Status != StatusSuccess {
		t.Fatalf("unpinned: %+v, %v", res, err)
	}
	res, err = rt.Run(context.Background(), "f/t/die", json.RawMessage(`{}`), RunOptions{Pin: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusFailure || res.Latched != nil {
		t.Fatalf("%s, latched %v; want the step's failure and no latch", res.Status, res.Latched)
	}
}
