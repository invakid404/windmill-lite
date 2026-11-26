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
//     second, which starts inside it, going on.
//
// Each part must hold only filler, separators and masks, and the two
// writes one mask in all.
func TestLineWriterCutsNoSecret(t *testing.T) {
	short := "synthetic-shared-prefix"
	long := short + "-and-more-5c1e"
	spaced := "synthetic/escaped secret-2d9e"
	enc := "synthetic%2Fescaped%20secret-2d9e"
	for _, c := range []struct {
		name       string
		secrets    []string
		tail, rest string
	}{
		{"one secret cut in two", []string{long}, "|" + long[:10], long[10:] + "|\n"},
		{"a shorter secret ends where a longer one goes on", []string{short, long}, "|" + short, long[len(short):] + "|\n"},
		{"an escaped secret cut after an escape", []string{spaced}, "|" + enc[:14], enc[14:] + "|\n"},
		{"an escaped secret cut inside an escape", []string{spaced}, "|" + enc[:11], enc[11:] + "|\n"},
		{"an escaped secret cut after its %", []string{spaced}, "|" + enc[:10], enc[10:] + "|\n"},
		{"two secrets that overlap", []string{"synthetic/start", "start/distinctive-tail"}, "|synthetic/start/distinctive-ta", "il|\n"},
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
			if len(lines) != 2 || masks != 1 {
				t.Errorf("%d parts with %d masks; want 2 parts and 1 mask", len(lines), masks)
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
