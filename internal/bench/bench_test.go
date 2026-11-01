package bench

import (
	"bytes"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// seq returns the durations 1 to n, in an order that is not sorted (37
// is prime, so i·37 mod n runs over every residue for the n used here).
func seq(n int) []time.Duration {
	ds := make([]time.Duration, n)
	for i := range n {
		ds[i] = time.Duration((i*37)%n + 1)
	}
	return ds
}

// TestPercentile checks the nearest-rank method, the ⌈q·n⌉-th smallest
// observation, at sample counts where q·n is an integer and where it is
// not.
func TestPercentile(t *testing.T) {
	for _, c := range []struct {
		n    int
		q    float64
		want time.Duration
	}{
		{1, 0.50, 1}, {1, 0.95, 1}, {1, 0, 1},
		{2, 0.50, 1}, {2, 0.95, 2}, {2, 0.51, 2},
		{20, 0.50, 10}, {20, 0.95, 19}, {20, 0.99, 20}, {20, 1, 20}, {20, 0, 1},
		{100, 0.50, 50}, {100, 0.95, 95}, {100, 0.99, 99}, {100, 0.951, 96},
		{7, 0.50, 4}, {3, 0.95, 3},
	} {
		ds := seq(c.n)
		before := append([]time.Duration(nil), ds...)
		if got := Percentile(ds, c.q); got != c.want {
			t.Errorf("Percentile(1..%d, %v) = %d, want %d", c.n, c.q, got, c.want)
		}
		for i := range ds {
			if ds[i] != before[i] {
				t.Fatalf("Percentile reordered its input")
			}
		}
	}
	if Percentile(nil, 0.5) != 0 || Mean(nil) != 0 {
		t.Error("no observations must give 0")
	}
	if got := Mean(seq(4)); got != 2 { // (1+2+3+4)/4, truncated
		t.Errorf("Mean(1..4) = %d", got)
	}
}

// TestWorkers checks that the workers start together and each does its
// operations: one operation each already puts every worker in flight at
// once, and the mean concurrency counts the overlap.
func TestWorkers(t *testing.T) {
	for _, c := range []struct{ conc, ops int }{{8, 1}, {8, 3}, {1, 3}, {3, 2}} {
		var calls atomic.Int64
		got, err := Workers(c.conc, c.ops, func(int) error {
			calls.Add(1)
			time.Sleep(20 * time.Millisecond)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if int(calls.Load()) != c.conc*c.ops || got.Peak != c.conc {
			t.Fatalf("conc %d, ops %d: %d calls, peak %d", c.conc, c.ops, calls.Load(), got.Peak)
		}
		for w, ops := range got.Ops {
			if len(ops) != c.ops {
				t.Fatalf("worker %d did %d operations, want %d", w, len(ops), c.ops)
			}
		}
		if got.Mean < 0.8*float64(c.conc) || got.Mean > float64(c.conc)+0.01 {
			t.Fatalf("conc %d, ops %d: mean concurrency %.2f", c.conc, c.ops, got.Mean)
		}
	}
	boom := errors.New("boom")
	if _, err := Workers(4, 2, func(w int) error {
		if w == 2 {
			return boom
		}
		return nil
	}); !errors.Is(err, boom) {
		t.Fatalf("the worker's error: %v", err)
	}
}

// TestSynthetic checks the payloads' sizes, that each is one JSON object
// and that a size always gives the same text. internal/jsonval checks that
// its canonicaliser and its test implementation agree on them.
func TestSynthetic(t *testing.T) {
	for _, n := range append([]int{0, 63, 64, 65, 200, 1000}, SyntheticSizes...) {
		text := Synthetic(n)
		want := n
		if n < 64 {
			want = SyntheticMin
		}
		if len(text) != want {
			t.Fatalf("Synthetic(%d) has %d bytes, want %d", n, len(text), want)
		}
		var v map[string]any
		if err := json.Unmarshal(text, &v); err != nil || !bytes.Equal(Synthetic(n), text) {
			t.Fatalf("Synthetic(%d) is not one stable object (%v): %.80s", n, err, text)
		}
	}
	for n, want := range map[int]string{26: "26B", 1 << 10: "1KiB", 4 << 20: "4MiB", 1500: "1500B"} {
		if got := SizeLabel(n); got != want {
			t.Errorf("SizeLabel(%d) = %q, want %q", n, got, want)
		}
	}
}
