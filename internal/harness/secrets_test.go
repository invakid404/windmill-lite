package harness

import (
	"bytes"
	"fmt"
	"runtime"
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

// TestMaskJSONScalesWithRepeats checks that a string that repeats a secret
// many times, its JSON escaping nothing or a tab between the secrets, is
// masked with allocations in proportion to its length: masking one eight
// times as long allocates no more than twenty times as much, where building
// a token's index for each secret would allocate sixty-four times as much.
// The output masks every secret and keeps the rest. Allocations are counted,
// not timed; BenchmarkMaskJSONRepeats reports the time.
func TestMaskJSONScalesWithRepeats(t *testing.T) {
	sec := NewSecrets([]string{"synth-8b"})
	for _, sep := range []string{" ", `\t`} {
		measure := func(n int) uint64 {
			text, want := repeatsJSON(n, sep)
			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			got := sec.MaskJSON(text)
			runtime.ReadMemStats(&after)
			if string(got) != want {
				t.Fatalf("separator %q, %d repeats: the output is not every secret masked and the rest kept (%d bytes, want %d)", sep, n, len(got), len(want))
			}
			return after.TotalAlloc - before.TotalAlloc
		}
		small, large := measure(2000), measure(16000)
		if large > 20*small {
			t.Errorf("separator %q: masking 16000 repeats allocated %d bytes, %.1f times the %d bytes for 2000; want at most 20 times", sep, large, float64(large)/float64(small), small)
		}
	}
}

// repeatsJSON returns a JSON string of n repeats of the secret synth-8b,
// each followed by sep as JSON writes it, and that string masked.
func repeatsJSON(n int, sep string) ([]byte, string) {
	return []byte(`"` + strings.Repeat("synth-8b"+sep, n) + `"`), `"` + strings.Repeat(SecretMask+sep, n) + `"`
}

// BenchmarkMaskJSONRepeats reports the time to mask a string that repeats a
// secret, with and without an escape, at two lengths.
func BenchmarkMaskJSONRepeats(b *testing.B) {
	sec := NewSecrets([]string{"synth-8b"})
	for _, sep := range []string{" ", `\t`} {
		for _, n := range []int{2000, 16000} {
			text, _ := repeatsJSON(n, sep)
			b.Run(fmt.Sprintf("sep=%q/n=%d", sep, n), func(b *testing.B) {
				for range b.N {
					sec.MaskJSON(text)
				}
			})
		}
	}
}

// TestMaskJSONSecretInTheMask checks that a secret that the mask itself
// holds, such as e in [secret], shows in the mask and is not masked again:
// the rounds that read the masked text pass over it, and end at once, well
// within a guard lowered to four rounds.
func TestMaskJSONSecretInTheMask(t *testing.T) {
	defer func(n int) { maskRounds = n }(maskRounds)
	maskRounds = 4
	if got := string(NewSecrets([]string{"e"}).MaskJSON([]byte(`["e"]`))); got != `["[secret]"]` {
		t.Errorf("masking with a secret inside the mask: %s; want the string masked once", got)
	}
}

// TestMaskJSONKeepsOtherErrors checks two selected errors, one whose
// secret the mask itself holds: that error's string is masked once, and the
// other keeps its text, also with a second secret that would join a mask's
// closing quote to the other error were the first one masked whole.
func TestMaskJSONKeepsOtherErrors(t *testing.T) {
	defer func(n int) { maskRounds = n }(maskRounds)
	maskRounds = 4
	sec := NewSecrets([]string{"cre", `",{"error":"timeout"}`})
	got := string(sec.MaskJSONAt([]byte(`[{"error":"cre"},{"error":"timeout"}]`), []string{"/0", "/1"}))
	if want := `[{"error":"[secret]"},{"error":"timeout"}]`; got != want {
		t.Errorf("masking two errors, one whose secret the mask holds: %s; want %s", got, want)
	}
}

// TestMaskJSONAtCrossesIntoData checks a selected error that a secret
// document masks whole, whose mask's closing quote then meets the data
// beside it, which a second secret is: the error is written as null, and
// the data stays. With a third secret that the null meets the data in, the
// smallest array around both is masked, and the member beside the array
// stays.
func TestMaskJSONAtCrossesIntoData(t *testing.T) {
	for _, c := range []struct {
		text, pointer string
		secrets       []string
		want          string
	}{
		{`[{"error":"synthetic"},98765]`, "/0", []string{`{"error":"synthetic"}`, `",98765`}, `[null,98765]`},
		{`{"keep":1,"list":[{"error":"synthetic"},98765]}`, "/list/0", []string{`{"error":"synthetic"}`, `",98765`, `null,98765`}, `{"keep":1,"list":"[secret]"}`},
	} {
		if got := string(NewSecrets(c.secrets).MaskJSONAt([]byte(c.text), []string{c.pointer})); got != c.want {
			t.Errorf("secrets %q: %s; want %s", c.secrets, got, c.want)
		}
	}
}

// TestMaskJSONRoundsBound checks the guard on the rounds that read masked
// text again: lowered to one round, masking that needs a second, as when
// a mask is written as null, masks the whole value instead.
func TestMaskJSONRoundsBound(t *testing.T) {
	defer func(n int) { maskRounds = n }(maskRounds)
	maskRounds = 1
	sec := NewSecrets([]string{`{"error":"synthetic"}`, `",98765`})
	if got := string(sec.MaskJSONAt([]byte(`[{"error":"synthetic"},98765]`), []string{"/0"})); got != fmt.Sprintf("%q", SecretMask) {
		t.Errorf("masking past the bound: %s; want the whole value masked", got)
	}
}

// TestMaskJSONManyErrorsScale checks that a result of many selected errors,
// one of which holds a secret that the mask itself holds, is masked with
// allocations in proportion to its size: eight times as many errors
// allocate no more than twenty times as much, where a pass over the whole
// result for each error would allocate sixty-four times as much. Every
// other error keeps its text.
func TestMaskJSONManyErrorsScale(t *testing.T) {
	sec := NewSecrets([]string{"cre"})
	measure := func(n int) uint64 {
		var text, want strings.Builder
		pointers := make([]string, n)
		text.WriteString("[")
		want.WriteString("[")
		for i := range n {
			if i > 0 {
				text.WriteString(",")
				want.WriteString(",")
			}
			pointers[i] = fmt.Sprintf("/%d", i)
			if i == n/2 {
				text.WriteString(`{"error":"cre"}`)
				want.WriteString(`{"error":"[secret]"}`)
				continue
			}
			fmt.Fprintf(&text, `{"error":"timeout %d"}`, i)
			fmt.Fprintf(&want, `{"error":"timeout %d"}`, i)
		}
		text.WriteString("]")
		want.WriteString("]")
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		got := sec.MaskJSONAt([]byte(text.String()), pointers)
		runtime.ReadMemStats(&after)
		if string(got) != want.String() {
			t.Fatalf("%d errors: the output is not the one error masked and the rest kept", n)
		}
		return after.TotalAlloc - before.TotalAlloc
	}
	small, large := measure(200), measure(1600)
	if large > 20*small {
		t.Errorf("masking 1600 errors allocated %d bytes, %.1f times the %d bytes for 200; want at most 20 times", large, float64(large)/float64(small), small)
	}
}

// BenchmarkMaskJSONWideObject reports the time to mask an object written
// with spaces, of 20,000 numbered members and one more whose value is a
// secret, so that its stored text, read too, carries its find over to the
// object.
func BenchmarkMaskJSONWideObject(b *testing.B) {
	sec := NewSecrets([]string{"synth-8b"})
	var obj strings.Builder
	obj.WriteString("{")
	for i := range 20000 {
		if i > 0 {
			obj.WriteString(", ")
		}
		fmt.Fprintf(&obj, `"k%d": "v%d"`, i, i)
	}
	obj.WriteString(`, "last": "synth-8b"}`)
	text := []byte(obj.String())
	for range b.N {
		sec.MaskJSON(text)
	}
}
