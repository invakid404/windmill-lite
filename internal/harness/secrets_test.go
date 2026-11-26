package harness

import (
	"bytes"
	"strings"
	"testing"
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
