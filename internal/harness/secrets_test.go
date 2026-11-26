package harness

import (
	"bytes"
	"math"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestLineWriterCutsNoSecret checks that a line longer than 64 KiB reaches
// the diagnostics in parts that hold no piece of a secret. The first write
// fills the line one byte past the point where a part must go out, and
// ends with the start of a secret, which the second write completes:
//
//   - one secret, cut in two by that point;
//   - two secrets, one the start of the other, the shorter ending at that
//     point while the longer goes on;
//   - a secret percent-encoded, cut in two after an escape, inside one,
//     and right after its %;
//   - two secrets that overlap, the first whole before that point and the
//     second, which starts inside it, going on;
//   - a secret as written whose end, as decoded, starts a second secret
//     that goes on: the views disagree on where the second starts.
//
// Each part must hold only filler, separators and masks: one mask in all,
// or one in each part where the cut splits a span of secrets, which each
// part masks on its side.
func TestLineWriterCutsNoSecret(t *testing.T) {
	short := "synthetic-shared-prefix"
	long := short + "-and-more-5c1e"
	spaced := "synthetic/escaped secret-2d9e"
	enc := "synthetic%2Fescaped%20secret-2d9e"
	for _, c := range []struct {
		name       string
		secrets    []string
		tail, rest string
		masks      int
	}{
		{"one secret cut in two", []string{long}, "|" + long[:10], long[10:] + "|\n", 1},
		{"a shorter secret ends where a longer one goes on", []string{short, long}, "|" + short, long[len(short):] + "|\n", 1},
		{"an escaped secret cut after an escape", []string{spaced}, "|" + enc[:14], enc[14:] + "|\n", 1},
		{"an escaped secret cut inside an escape", []string{spaced}, "|" + enc[:11], enc[11:] + "|\n", 1},
		{"an escaped secret cut after its %", []string{spaced}, "|" + enc[:10], enc[10:] + "|\n", 1},
		{"two secrets that overlap", []string{"synthetic/start", "start/distinctive-tail"}, "|synthetic/start/distinctive-ta", "il|\n", 2},
		{"a written secret whose decoded end starts another", []string{"synthetic-first%2Fstart", "start/another-end"}, "|synthetic-first%2Fstart%2Fano", "ther-end|\n", 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			sec := NewSecrets(c.secrets)
			lw := &lineWriter{diag: &diagnostics{w: &out}, keep: 20, secrets: sec}
			w := lw.stream("stdout")
			limit := partLimit(sec)
			w.Write([]byte(strings.Repeat("x", limit+1-len(c.tail)) + c.tail))
			w.Write([]byte(c.rest))
			lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
			masks := 0
			for i, line := range lines {
				_, payload, ok := strings.Cut(line, " stdout: ")
				if !ok {
					t.Fatalf("line %d is not an output line: %.80q", i, line)
				}
				masks += strings.Count(payload, SecretMask)
				if rest := strings.NewReplacer(SecretMask, "", "x", "", "|", "").Replace(payload); rest != "" {
					t.Errorf("part %d holds %q besides filler, separators and masks", i, rest)
				}
			}
			if len(lines) != 2 || masks != c.masks {
				t.Errorf("%d parts with %d masks; want 2 parts and %d masks", len(lines), masks, c.masks)
			}
		})
	}
}

// TestLineWriterMasksOverlappingSecrets checks that an output line that
// holds two secrets that overlap, the end of one the start of the other,
// reaches the diagnostics with one mask over both and nothing of either.
func TestLineWriterMasksOverlappingSecrets(t *testing.T) {
	var out bytes.Buffer
	lw := &lineWriter{diag: &diagnostics{w: &out}, keep: 20, secrets: NewSecrets([]string{"synthetic/start", "start/distinctive-tail"})}
	w := lw.stream("stdout")
	w.Write([]byte("path /synthetic/start/distinctive-tail ends\n"))
	if got := out.String(); strings.Contains(got, "distinctive") || strings.Contains(got, "synthetic") || !strings.Contains(got, "/"+SecretMask+" ends") {
		t.Errorf("the diagnostics line: %q; want one mask over both secrets", got)
	}
}

// TestLineWriterProgressesInARunOfSecrets checks that an unending line
// that is one run of overlapping secrets, which the next byte always
// extends, still goes out part after part: each write past the limit sends
// a part that holds only the mask, what the writer keeps stays within what
// a secret can reach, and the end of the line, after a newline, holds only
// the mask too.
func TestLineWriterProgressesInARunOfSecrets(t *testing.T) {
	var out bytes.Buffer
	sec := NewSecrets([]string{"aa"})
	lw := &lineWriter{diag: &diagnostics{w: &out}, keep: 20, secrets: sec}
	w := lw.stream("stdout").(*streamWriter)
	chunk := []byte(strings.Repeat("a", partLimit(sec)+1))
	for i := range 5 {
		w.Write(chunk)
		if len(w.buf) > sec.lookBack() || len(w.ctx) > sec.lookBack() {
			t.Fatalf("write %d: the writer keeps %d bytes and looks back at %d; want at most %d each", i, len(w.buf), len(w.ctx), sec.lookBack())
		}
	}
	w.Write([]byte("\n"))
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 6 {
		t.Fatalf("%d lines out; want 6, one for each write", len(lines))
	}
	for i, line := range lines {
		if _, payload, _ := strings.Cut(line, " stdout: "); payload != SecretMask {
			t.Errorf("part %d: %.60q; want the mask alone", i, payload)
		}
	}
}

// TestLineWriterMasksAcrossParts checks that a secret that one part's cut
// splits is masked on both sides when the next part goes out before the
// line ends: the first part ends inside the first of two secrets that
// overlap, where the second may follow, and the second never does, so the
// next part, sent at the limit, starts with the end of the first.
func TestLineWriterMasksAcrossParts(t *testing.T) {
	var out bytes.Buffer
	sec := NewSecrets([]string{"synthetic/start", "start/distinctive-tail"})
	lw := &lineWriter{diag: &diagnostics{w: &out}, keep: 20, secrets: sec}
	w := lw.stream("stdout")
	tail := "|synthetic/start/distinctive-ta"
	w.Write([]byte(strings.Repeat("x", partLimit(sec)+1-len(tail)) + tail))
	w.Write([]byte("|" + strings.Repeat("y", partLimit(sec))))
	w.Write([]byte("|\n"))
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	for i, line := range lines {
		if _, payload, _ := strings.Cut(line, " stdout: "); strings.Contains(payload, "synthetic") || strings.Contains(payload, "start") {
			t.Errorf("part %d holds a piece of the first secret: %.80q", i, payload[max(0, len(payload)-80):])
		}
	}
	if len(lines) != 3 {
		t.Errorf("%d parts; want 3", len(lines))
	}
}

// TestMaskJSONScalesWithRepeats checks that a string that repeats a secret
// many times is masked at a cost in proportion to its length, whether its
// JSON escapes nothing or escapes a tab between the secrets: masking one
// eight times as long allocates no more than twenty times as much, and,
// the fastest of three runs, takes no more than twenty-five times as long,
// where work per secret over the whole string would cost sixty-four times
// as much. The output masks every secret and keeps the rest.
func TestMaskJSONScalesWithRepeats(t *testing.T) {
	sec := NewSecrets([]string{"synth-8b"})
	for _, sep := range []string{" ", `\t`} {
		measure := func(n int) (uint64, time.Duration) {
			text := []byte(`"` + strings.Repeat("synth-8b"+sep, n) + `"`)
			want := `"` + strings.Repeat(SecretMask+sep, n) + `"`
			var alloc uint64
			fastest := time.Duration(math.MaxInt64)
			for range 3 {
				var before, after runtime.MemStats
				runtime.GC()
				runtime.ReadMemStats(&before)
				start := time.Now()
				got := sec.MaskJSON(text)
				fastest = min(fastest, time.Since(start))
				runtime.ReadMemStats(&after)
				if string(got) != want {
					t.Fatalf("separator %q, %d repeats: the output is not every secret masked and the rest kept (%d bytes, want %d)", sep, n, len(got), len(want))
				}
				alloc = after.TotalAlloc - before.TotalAlloc
			}
			return alloc, fastest
		}
		smallAlloc, smallTime := measure(2000)
		largeAlloc, largeTime := measure(16000)
		if largeAlloc > 20*smallAlloc {
			t.Errorf("separator %q: masking 16000 repeats allocated %d bytes, %.1f times the %d bytes for 2000; want at most 20 times", sep, largeAlloc, float64(largeAlloc)/float64(smallAlloc), smallAlloc)
		}
		if largeTime > 25*smallTime {
			t.Errorf("separator %q: masking 16000 repeats took %v, %.1f times the %v for 2000; want at most 25 times", sep, largeTime, float64(largeTime)/float64(smallTime), smallTime)
		}
	}
}
