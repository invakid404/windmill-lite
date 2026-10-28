package oracle

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func gibs(v float64) uint64 { return uint64(v * GiB) }

// TestRefusalForms checks the exact JSON the reference gives each refusal.
func TestRefusalForms(t *testing.T) {
	for _, c := range []struct {
		spec string
		err  *Refusal
		want string
	}{
		// spec: OR-1/b
		{"OR-1/b", ErrVersionMismatch("EE v1.767.0"), `{"error":"oracle_version_mismatch","got":"EE v1.767.0"}`},
		// spec: OR-5/a
		{"OR-5/a", ErrRequiresWorkers(2, 1), `{"error":"fixture_requires_workers","need":2,"have":1}`},
		// spec: OR-16/c
		{"OR-16/c", ErrForeignEffect(), `{"error":"foreign_effect"}`},
		// spec: OR-30/a
		{"OR-30/a", ErrInsufficientDisk(gibs(9), gibs(4.9), Reserve), `{"error":"insufficient_disk","free_gib":9.0,"need_gib":4.9,"reserve_gib":5}`},
		{"OR-30/b", ErrDiskFloor("pull", gibs(4.99), Reserve), `{"error":"disk_floor_reached","phase":"pull","free_gib":4.99,"floor_gib":5}`},
		{"OR-30/c", ErrDiskAfterPull(gibs(4.8), Reserve, []string{WindmillImage}), `{"error":"insufficient_disk_after_pull","free_gib":4.8,"floor_gib":5,"removed":["` + WindmillImage + `"]}`},
	} {
		if got := c.err.Error(); got != c.want {
			t.Errorf("%s: got %s, want %s", c.spec, got, c.want)
		}
	}
}

// TestCheckPull is OR-30 step 3 (case OR-30/a).
func TestCheckPull(t *testing.T) {
	// spec: OR-30/a
	err := CheckPull(gibs(9), gibs(4.9))
	var r *Refusal
	if !errors.As(err, &r) || r.Error() != `{"error":"insufficient_disk","free_gib":9.0,"need_gib":4.9,"reserve_gib":5}` {
		t.Fatalf("free 9, need 4.9: %v", err)
	}
	for _, c := range []struct {
		free, need float64
		ok         bool
	}{
		{7.9, 0.5, false}, // below the 8 GiB minimum
		{8, 3, true},
		{8, 3.1, false},
		{20, 5.6, true},
		{3, 4, false}, // need above free
	} {
		if got := CheckPull(gibs(c.free), gibs(c.need)) == nil; got != c.ok {
			t.Errorf("free %v need %v: ok %v, want %v", c.free, c.need, got, c.ok)
		}
	}
}

func TestPullNeed(t *testing.T) {
	if PullNeed(nil, "amd64") != 0 {
		t.Error("nothing missing needs nothing")
	}
	got := PullNeed([]string{WindmillImage, PostgresImage}, "amd64")
	if want := uint64(3*1_373_000_000+288_118_565) + TransientMargin; got != want {
		t.Errorf("amd64 need %d, want %d", got, want)
	}
	if got := PullNeed([]string{WindmillImage}, "arm64"); got != 3_940_469_084+TransientMargin {
		t.Errorf("arm64 need %d", got)
	}
}

// TestWatchAbortsBelowFloor is OR-30/b: a reading below the floor during a
// pull cancels it, and nothing resumes it.
func TestWatchAbortsBelowFloor(t *testing.T) {
	// spec: OR-30/b
	var pulls atomic.Int32
	err := Watch(context.Background(), "pull", func() (uint64, error) { return gibs(4.99), nil }, time.Millisecond, func(ctx context.Context) error {
		pulls.Add(1)
		<-ctx.Done()
		return ctx.Err()
	})
	var r *Refusal
	if !errors.As(err, &r) || r.Code != "disk_floor_reached" {
		t.Fatalf("got %v", err)
	}
	if pulls.Load() != 1 {
		t.Fatalf("the pull ran %d times; there is no automatic resume", pulls.Load())
	}
}

func TestWatchLetsWorkFinish(t *testing.T) {
	err := Watch(context.Background(), "run", func() (uint64, error) { return gibs(12), nil }, time.Millisecond, func(ctx context.Context) error {
		time.Sleep(5 * time.Millisecond)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	if err := Watch(context.Background(), "run", func() (uint64, error) { return gibs(12), nil }, time.Millisecond, func(context.Context) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("work's own error: %v", err)
	}
	// A failed measurement stops the work too: the policy fails closed.
	measureErr := errors.New("statfs failed")
	err = Watch(context.Background(), "run", func() (uint64, error) { return 0, measureErr }, time.Millisecond, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if !errors.Is(err, measureErr) {
		t.Fatalf("failed measurement: %v", err)
	}
}

// TestSettledFree is OR-30 step 6: a reading below the floor after a pull is
// re-read after settling, and the second reading decides.
func TestSettledFree(t *testing.T) {
	readings := []uint64{gibs(4.8), gibs(4.8)}
	i := 0
	free, err := SettledFree(context.Background(), func() (uint64, error) { r := readings[i]; i++; return r, nil }, time.Millisecond)
	if err != nil || free != gibs(4.8) || i != 2 {
		t.Fatalf("free %d after %d readings: %v", free, i, err)
	}
	i = 0
	readings = []uint64{gibs(6)}
	if free, _ := SettledFree(context.Background(), func() (uint64, error) { r := readings[i]; i++; return r, nil }, time.Hour); free != gibs(6) || i != 1 {
		t.Fatalf("a reading above the floor is taken at once")
	}
}
