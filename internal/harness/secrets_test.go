package harness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/engine"
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

// TestMaskTextRemadeByTheMask checks the masking of text where a mask makes
// a secret again with the text after it: the secret starts with "]", the
// mask's last byte, and its own tail follows it. In a text, one with %XX
// escapes, a path, a query, a log tail and an output line, also split
// between two parts of a long line, the secret and its tail become one
// mask, and no line, nor two parts joined, holds the secret. A second
// secret that the mask's own text holds is masked once. The bound on the
// rounds, lowered to one round, masks whole a text that needs a second, and
// keeps one that does not.
func TestMaskTextRemadeByTheMask(t *testing.T) {
	const secret = "]synthetic-tail"
	tail := secret[1:]
	sec := NewSecrets([]string{secret, "cre"})
	for _, c := range []struct{ name, got, want string }{
		{"a text", sec.Mask("a " + secret + tail + " b"), "a [secret] b"},
		{"an escaped text", sec.Mask("a %5D" + tail + tail + " b"), "a [secret] b"},
		{"a path", sec.MaskURL("/p/%5D"+tail+tail, false), "/p/%5Bsecret%5D"},
		{"a query", sec.MaskURL("q=%5D"+tail+tail+"&r=1", true), "q=%5Bsecret%5D&r=1"},
		{"a log tail", sec.MaskTail("logged " + secret + tail), "logged [secret]"},
		{"the mask's own text", sec.Mask("a cre b"), "a [secret] b"},
	} {
		if c.got != c.want {
			t.Errorf("%s: %q; want %q", c.name, c.got, c.want)
		}
	}
	var out bytes.Buffer
	lw := &lineWriter{diag: &diagnostics{w: &out}, keep: 20, secrets: sec}
	w := lw.stream("stderr")
	w.Write([]byte("x " + secret + tail + "\n"))
	// A long line whose first part ends with the secret and whose second
	// starts with its tail.
	w.Write([]byte(strings.Repeat("y", partLimit(sec)+1-len(secret)) + secret))
	w.Write([]byte(tail + "|\n"))
	var payloads []string
	for _, line := range strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n") {
		_, payload, _ := strings.Cut(line, " stderr: ")
		payloads = append(payloads, payload)
	}
	if len(payloads) != 3 || payloads[0] != "x [secret]" || payloads[2] != "[secret]|" {
		t.Errorf("output lines %.200q; want the line, then the long line's two parts, each with the secret and its tail as one mask", payloads)
	}
	for i := range payloads {
		if joined := strings.Join(payloads[max(0, i-1):i+1], ""); strings.Contains(joined, secret) {
			t.Errorf("output line %d, joined to the one before, holds the secret", i)
		}
	}
	defer func(n int) { maskRounds = n }(maskRounds)
	maskRounds = 1
	if got := sec.Mask("a " + secret + tail + " b"); got != SecretMask {
		t.Errorf("past the bound: %q; want the text masked whole", got)
	}
	if got := sec.Mask("a cre b"); got != "a [secret] b" {
		t.Errorf("within the bound: %q; want %q", got, "a [secret] b")
	}
}

// TestSettingsRefusalsMaskSecrets checks three refusals that quote what
// they refuse, where that is a synthetic secret of the process: Spawn's of
// a module state, before any process starts, NewPool's of a key whose
// harness class differs from its process's, and a pool's Execute's of an
// invocation whose module state is not the pool's, which quotes its id.
func TestSettingsRefusalsMaskSecrets(t *testing.T) {
	const secret = "synthetic-mode-20"
	if _, err := Spawn(context.Background(), ProcessConfig{ModuleState: secret, Secrets: []string{secret}}); err == nil || !strings.Contains(err.Error(), SecretMask) || strings.Contains(err.Error(), secret) {
		t.Errorf("Spawn with a module state that is a secret: %v; want an error with the mask and no secret", err)
	}
	if _, err := NewPool(PoolConfig{Key: Key{Class: secret}, Process: ProcessConfig{Kind: "bun", Secrets: []string{secret}}}); err == nil || !strings.Contains(err.Error(), SecretMask) || strings.Contains(err.Error(), secret) {
		t.Errorf("NewPool with a harness class that is a secret: %v; want an error with the mask and no secret", err)
	}
	pl, err := NewPool(PoolConfig{Key: Key{Class: "bun"}, Process: ProcessConfig{Kind: "bun", Secrets: []string{secret}}})
	if err != nil {
		t.Fatal(err)
	}
	defer pl.Close(context.Background())
	if _, err := pl.Execute(context.Background(), engine.Invocation{ID: secret, ModuleState: engine.ModuleStateFresh}); err == nil || !strings.Contains(err.Error(), SecretMask) || strings.Contains(err.Error(), secret) {
		t.Errorf("Execute of an invocation whose id is a secret, in another module state: %v; want an error with the mask and no secret", err)
	}
}

// TestMaskErrKeepsLateCall checks the masking of a late host_call's error
// whose text holds a synthetic secret: the result is still a
// *LateHostCallError, with the same Done, which the run's settlement waits
// for, and its text holds the mask and no secret.
func TestMaskErrKeepsLateCall(t *testing.T) {
	const secret = "synthetic-late-21"
	done := make(chan struct{})
	err := NewSecrets([]string{secret}).MaskErr(&LateHostCallError{Err: errors.New("hook x: " + secret + " got no reply"), Done: done})
	var late *LateHostCallError
	if !errors.As(err, &late) || late.Done != (<-chan struct{})(done) || !strings.Contains(err.Error(), SecretMask) || strings.Contains(err.Error(), secret) {
		t.Errorf("a late call's masked error: %T %q; want a LateHostCallError with its Done, the mask and no secret", err, err)
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
// text again, lowered to one round on masking that needs a second, as when
// a mask is written as null: the guard masks the selected part whole, finds
// that its mask meets the data beside it in a secret, writes it as null and
// keeps the data. With a secret that the null meets the data in too, it
// masks the whole value.
func TestMaskJSONRoundsBound(t *testing.T) {
	defer func(n int) { maskRounds = n }(maskRounds)
	maskRounds = 1
	text := []byte(`[{"error":"synthetic"},98765]`)
	for _, c := range []struct {
		secrets []string
		want    string
	}{
		{[]string{`{"error":"synthetic"}`, `",98765`}, `[null,98765]`},
		{[]string{`{"error":"synthetic"}`, `",98765`, `null,98765`}, fmt.Sprintf("%q", SecretMask)},
	} {
		if got := string(NewSecrets(c.secrets).MaskJSONAt(text, []string{"/0"})); got != c.want {
			t.Errorf("secrets %q, masking past the bound: %s; want %s", c.secrets, got, c.want)
		}
	}
}

// TestMaskJSONAtCarriesTheContainer checks a selected error two arrays
// down whose null gives way to the array around it: that array, masked, is
// read as a part from then on, so its own mask, which meets the number
// beside it in a further secret, is written as null; the number and the
// object after it stay.
func TestMaskJSONAtCarriesTheContainer(t *testing.T) {
	sec := NewSecrets([]string{`{"error":"synthetic"}`, `",98765`, `null,98765`, `",777`})
	got := string(sec.MaskJSONAt([]byte(`[[{"error":"synthetic"},98765],777,{"ok":1}]`), []string{"/0/0"}))
	if want := `[null,777,{"ok":1}]`; got != want {
		t.Errorf("masking an error whose array gives way: %s; want %s", got, want)
	}
}

// TestMaskJSONGuardKeepsOtherData checks a selected error that holds a
// chain of 40 nested arrays, each of which a secret masks only once the
// array inside it is masked, so that masking needs more rounds than the
// guard allows: the guard masks the error whole and keeps the data beside
// it, which no secret touches.
func TestMaskJSONGuardKeepsOtherData(t *testing.T) {
	const depth = 40
	secrets := []string{"synthetic-root"}
	chain := `"synthetic-root"`
	for k := 1; k <= depth; k++ {
		secrets = append(secrets, fmt.Sprintf(`",%d`, k))
		chain = fmt.Sprintf("[%s,%d]", chain, k)
	}
	text := []byte(`[{"ok":"data"},{"error":` + chain + `}]`)
	got := string(NewSecrets(secrets).MaskJSONAt(text, []string{"/1"}))
	if want := `[{"ok":"data"},"[secret]"]`; got != want {
		t.Errorf("masking a chain longer than the guard: %.80s; want %s", got, want)
	}
}

// TestMaskJSONKeepsTokens checks that MaskJSON, where it masks a part of a
// value, writes the rest with its tokens as written, but for the spaces
// between them: escapes in strings and keys, two keys that are lone
// surrogates, which decoding makes alike, a number in its own notation. A
// key and a string in which a secret was found are written from their
// decoded form, escapes and all. It checks that for a round of masking, and
// for the guard at the bound of rounds, which masks a part whole.
func TestMaskJSONKeepsTokens(t *testing.T) {
	s := NewSecrets([]string{"synthetic-kept"})
	for _, c := range []struct{ in, want string }{
		{`{ "\ud800" : "\udfff" , "\ud801" : [ 1e3 , "A" ] , "t" : "synthetic-kept" }`, `{"\ud800":"\udfff","\ud801":[1e3,"A"],"t":"[secret]"}`},
		{`{"Asynthetic-kept":"A","b":"x\/y"}`, `{"A[secret]":"A","b":"x\/y"}`},
		{`["A", "synthetic-kept"]`, `["A","[secret]"]`},
	} {
		if got := string(s.MaskJSON([]byte(c.in))); got != c.want {
			t.Errorf("MaskJSON(%s) = %s; want %s", c.in, got, c.want)
		}
	}
	defer func(n int) { maskRounds = n }(maskRounds)
	maskRounds = 0
	text := `[{"k":"synthetic-kept"}, "A", {"\ud800":1,"\ud801":2}]`
	if got, want := string(s.MaskJSONAt([]byte(text), []string{"/0"})), `["[secret]","A",{"\ud800":1,"\ud801":2}]`; got != want {
		t.Errorf("the guard on %s: %s; want %s", text, got, want)
	}
}

// TestMaskJSONRepeatsInsideMasks checks that a string that repeats a secret
// which the mask itself holds is masked with allocations in proportion to
// its length, the rounds passing over the secret inside each of its masks:
// eight times as many repeats allocate no more than twenty times as much.
func TestMaskJSONRepeatsInsideMasks(t *testing.T) {
	sec := NewSecrets([]string{"cre"})
	measure := func(n int) uint64 {
		text := []byte(`"` + strings.Repeat("cre ", n) + `"`)
		want := `"` + strings.Repeat(SecretMask+" ", n) + `"`
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		got := sec.MaskJSON(text)
		runtime.ReadMemStats(&after)
		if string(got) != want {
			t.Fatalf("%d repeats: the output is not every secret masked once", n)
		}
		return after.TotalAlloc - before.TotalAlloc
	}
	small, large := measure(512), measure(4096)
	if large > 20*small {
		t.Errorf("masking 4096 repeats allocated %d bytes, %.1f times the %d bytes for 512; want at most 20 times", large, float64(large)/float64(small), small)
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
