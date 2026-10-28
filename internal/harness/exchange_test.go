package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// An exchange is a protocol transcript in conformance/protocol/exchanges:
// the frames the engine sends and the frames the harness must answer with.
// TestExchanges plays the engine's side, frame by frame, against the real
// Bun harness on the pinned Bun; it is milestone C1's exit gate.
type exchange struct {
	Case       string   `json:"case"`
	Title      string   `json:"title"`
	Provenance string   `json:"provenance"`
	Spec       []string `json:"spec"`
	ReliesOn   []string `json:"relies_on"`
	Design     string   `json:"design"`
	Steps      []exStep `json:"steps"`
}

// exStep is one step of a transcript; exactly one of its actions is set.
type exStep struct {
	// Send encodes a message canonically and sends it, as part frames when
	// it is larger than a frame. "<bundle>" in it stands for the fixture
	// bundle's digest.
	Send json.RawMessage `json:"send"`
	// Fill replaces the string "<fill>" in Send's payload with Bytes copies
	// of Char, for messages too large to write out.
	Fill *struct {
		Bytes int    `json:"bytes"`
		Char  string `json:"char"`
	} `json:"fill"`
	// Split sends Send as part frames of ChunkBytes each, with Between sent
	// as a whole frame after part After.
	Split *struct {
		ChunkBytes int             `json:"chunk_bytes"`
		After      int             `json:"after"`
		Between    json.RawMessage `json:"between"`
	} `json:"split"`
	// SendText sends one frame with this exact payload.
	SendText *string `json:"send_text"`

	// Expect waits for the next message from the harness and matches it
	// (see matchJSON). Parts, when set, is how many part frames must have
	// carried it.
	Expect   json.RawMessage `json:"expect"`
	Parts    int             `json:"parts"`
	WithinMS int             `json:"within_ms"`

	// QuietMS asserts that no message arrives for that long.
	QuietMS int `json:"quiet_ms"`
	// Exit waits for the process to exit with this code, having sent
	// nothing more.
	Exit *int `json:"exit"`
	// Stdout asserts that the process's stdout so far contains the text.
	Stdout string `json:"stdout_contains"`
}

func TestExchanges(t *testing.T) {
	bun := bunPath(t)
	dir := repoPath(t, "conformance/protocol/exchanges")
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no exchanges in %s: %v", dir, err)
	}
	for _, file := range files {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var x exchange
		if err := json.Unmarshal(b, &x); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		if x.Case+".json" != filepath.Base(file) || x.Provenance != "policy" || len(x.Steps) == 0 {
			t.Fatalf("%s: case %q, provenance %q, %d steps", file, x.Case, x.Provenance, len(x.Steps))
		}
		t.Run(x.Case, func(t *testing.T) {
			t.Parallel()
			runExchange(t, bun, x)
		})
	}
}

type received struct {
	payload []byte
	parts   int
}

type exRun struct {
	t      *testing.T
	nc     net.Conn
	cmd    *exec.Cmd
	digest string
	pid    int
	msgs   chan received
	readEr chan error
	exited chan struct{}
	stdout *syncBuffer
}

func runExchange(t *testing.T, bun string, x exchange) {
	bundle, digest := fixtureBundle(t)
	parent, child, err := socketpair()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bun, repoPath(t, "harnesses/bun/runtime/main.ts"), "--bundle", bundle)
	cmd.Env = BaseEnv()
	cmd.Dir = t.TempDir()
	cmd.ExtraFiles = []*os.File{child}
	stdout, stderr := &syncBuffer{}, &syncBuffer{}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	child.Close()
	nc, err := net.FileConn(parent)
	parent.Close()
	if err != nil {
		t.Fatal(err)
	}
	r := &exRun{t: t, nc: nc, cmd: cmd, digest: digest, pid: cmd.Process.Pid,
		msgs: make(chan received, 64), readEr: make(chan error, 1), exited: make(chan struct{}), stdout: stdout}
	go func() {
		cmd.Wait()
		close(r.exited)
	}()
	t.Cleanup(func() {
		cmd.Process.Kill()
		<-r.exited
		nc.Close()
		if t.Failed() {
			t.Logf("harness stderr:\n%s", stderr.String())
		}
	})
	go r.read()
	for i, s := range x.Steps {
		r.step(i, s)
	}
}

func (r *exRun) read() {
	asm := newAssembler(DefaultMaxMessage)
	counts := map[string]int{}
	for {
		payload, err := readFrame(r.nc, MaxFrame)
		if err != nil {
			r.readEr <- err
			return
		}
		m, err := Decode(payload)
		if err != nil {
			r.readEr <- err
			return
		}
		parts := 0
		if p, ok := m.(Part); ok {
			counts[p.ID]++
			full, done, err := asm.add(p)
			if err != nil {
				r.readEr <- err
				return
			}
			if !done {
				continue
			}
			parts = counts[p.ID]
			delete(counts, p.ID)
			payload = full
		}
		r.msgs <- received{payload: payload, parts: parts}
	}
}

func (r *exRun) step(i int, s exStep) {
	t := r.t
	t.Helper()
	within := 10 * time.Second
	if s.WithinMS > 0 {
		within = time.Duration(s.WithinMS) * time.Millisecond
	}
	switch {
	case s.Send != nil:
		payload := r.encode(i, s.Send)
		if s.Fill != nil {
			payload = bytes.Replace(payload, []byte(`"<fill>"`), []byte(`"`+strings.Repeat(s.Fill.Char, s.Fill.Bytes)+`"`), 1)
		}
		if s.Split == nil {
			if len(payload) <= MaxFrame {
				r.write(payload)
				return
			}
			parts, err := splitPayload("x"+strconv.Itoa(i), payload, PartChunk)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range parts {
				r.write(p)
			}
			return
		}
		parts, err := splitPayload("x"+strconv.Itoa(i), payload, s.Split.ChunkBytes)
		if err != nil {
			t.Fatal(err)
		}
		for k, p := range parts {
			r.write(p)
			if k == s.Split.After && s.Split.Between != nil {
				r.write(r.encode(i, s.Split.Between))
			}
		}
	case s.SendText != nil:
		r.write([]byte(*s.SendText))
	case s.Expect != nil:
		select {
		case m := <-r.msgs:
			if err := matchJSON(s.Expect, m.payload, r.vars()); err != nil {
				t.Fatalf("step %d: %v\n got %s", i, err, abbreviate(m.payload))
			}
			if m.parts != s.Parts {
				t.Fatalf("step %d: carried by %d part frames, want %d", i, m.parts, s.Parts)
			}
		case err := <-r.readEr:
			t.Fatalf("step %d: waiting for %s: %v", i, s.Expect, err)
		case <-time.After(within):
			t.Fatalf("step %d: no message within %s, want %s", i, within, s.Expect)
		}
	case s.QuietMS > 0:
		select {
		case m := <-r.msgs:
			t.Fatalf("step %d: want quiet, got %s", i, abbreviate(m.payload))
		case <-time.After(time.Duration(s.QuietMS) * time.Millisecond):
		}
	case s.Exit != nil:
		select {
		case <-r.exited:
		case <-time.After(within):
			t.Fatalf("step %d: still running after %s, want exit %d", i, within, *s.Exit)
		}
		if got := r.cmd.ProcessState.ExitCode(); got != *s.Exit {
			t.Fatalf("step %d: exit code %d, want %d", i, got, *s.Exit)
		}
		select {
		case m := <-r.msgs:
			t.Fatalf("step %d: unexpected message before exit: %s", i, abbreviate(m.payload))
		case err := <-r.readEr:
			if err != io.EOF {
				t.Fatalf("step %d: the stream ended with %v, want a clean end", i, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("step %d: the stream did not end", i)
		}
	case s.Stdout != "":
		if !strings.Contains(r.stdout.String(), s.Stdout) {
			t.Fatalf("step %d: stdout %q lacks %q", i, r.stdout.String(), s.Stdout)
		}
	default:
		t.Fatalf("step %d: no action", i)
	}
}

func (r *exRun) encode(i int, msg json.RawMessage) []byte {
	var b bytes.Buffer
	if err := json.Compact(&b, msg); err != nil {
		r.t.Fatal(err)
	}
	text := bytes.ReplaceAll(b.Bytes(), []byte(`"<bundle>"`), []byte(strconv.Quote(r.digest)))
	m, err := Decode(text)
	if err != nil {
		r.t.Fatalf("step %d: %v", i, err)
	}
	p, err := Encode(m)
	if err != nil {
		r.t.Fatalf("step %d: %v", i, err)
	}
	return p
}

func (r *exRun) write(payload []byte) {
	if _, err := r.nc.Write(appendFrame(nil, payload)); err != nil {
		r.t.Fatalf("write: %v", err)
	}
}

func (r *exRun) vars() map[string]string {
	return map[string]string{"<pid>": strconv.Itoa(r.pid), "<bundle>": strconv.Quote(r.digest)}
}

func abbreviate(b []byte) string {
	if len(b) > 300 {
		return fmt.Sprintf("%s… (%d bytes)", b[:300], len(b))
	}
	return string(b)
}

// matchJSON compares a received payload with an expectation. Objects must
// have the same keys in the same order and arrays the same elements;
// numbers compare by their text. A string in the expectation can be a
// placeholder:
//
//   - "<any>" matches any value, "<string>" any string, "<int>" any
//     integer;
//   - "<pid>" and "<bundle>" match the harness's pid and the fixture
//     bundle's digest;
//   - "<c*N>" matches a string of N copies of the character c.
func matchJSON(want json.RawMessage, got []byte, vars map[string]string) error {
	w, err := parseOrdered(want)
	if err != nil {
		return fmt.Errorf("expectation: %v", err)
	}
	g, err := parseOrdered(got)
	if err != nil {
		return fmt.Errorf("payload: %v", err)
	}
	return match(w, g, "", vars)
}

type node struct {
	kind  byte // '{', '[', 's' string, 'n' number, 'b' bool, 'z' null
	keys  []string
	elems []node
	text  string
}

func parseOrdered(b []byte) (node, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	n, err := parseNode(d)
	if err != nil {
		return n, err
	}
	if _, err := d.Token(); err != io.EOF {
		return n, fmt.Errorf("trailing data")
	}
	return n, nil
}

func parseNode(d *json.Decoder) (node, error) {
	tok, err := d.Token()
	if err != nil {
		return node{}, err
	}
	switch v := tok.(type) {
	case json.Delim:
		n := node{kind: byte(v)}
		for d.More() {
			if v == '{' {
				k, err := d.Token()
				if err != nil {
					return n, err
				}
				n.keys = append(n.keys, k.(string))
			}
			e, err := parseNode(d)
			if err != nil {
				return n, err
			}
			n.elems = append(n.elems, e)
		}
		_, err := d.Token()
		return n, err
	case string:
		return node{kind: 's', text: v}, nil
	case json.Number:
		return node{kind: 'n', text: v.String()}, nil
	case bool:
		return node{kind: 'b', text: strconv.FormatBool(v)}, nil
	default:
		return node{kind: 'z'}, nil
	}
}

var repeatPlaceholder = regexp.MustCompile(`^<(.)\*([0-9]+)>$`)

func match(w, g node, path string, vars map[string]string) error {
	if w.kind == 's' && strings.HasPrefix(w.text, "<") && strings.HasSuffix(w.text, ">") {
		switch w.text {
		case "<any>":
			return nil
		case "<string>":
			if g.kind == 's' {
				return nil
			}
		case "<int>":
			if _, err := strconv.ParseInt(g.text, 10, 64); g.kind == 'n' && err == nil {
				return nil
			}
		case "<pid>":
			if g.kind == 'n' && g.text == vars["<pid>"] {
				return nil
			}
		case "<bundle>":
			if g.kind == 's' && strconv.Quote(g.text) == vars["<bundle>"] {
				return nil
			}
		default:
			if m := repeatPlaceholder.FindStringSubmatch(w.text); m != nil {
				n, _ := strconv.Atoi(m[2])
				if g.kind == 's' && g.text == strings.Repeat(m[1], n) {
					return nil
				}
				return fmt.Errorf("%s: want %s, got a string of %d bytes", pathOrRoot(path), w.text, len(g.text))
			}
		}
		return fmt.Errorf("%s: want %s, got %s", pathOrRoot(path), w.text, describeNode(g))
	}
	if w.kind != g.kind {
		return fmt.Errorf("%s: want %s, got %s", pathOrRoot(path), describeNode(w), describeNode(g))
	}
	switch w.kind {
	case '{':
		if strings.Join(w.keys, ",") != strings.Join(g.keys, ",") {
			return fmt.Errorf("%s: keys %v, want %v in this order", pathOrRoot(path), g.keys, w.keys)
		}
		for i := range w.elems {
			if err := match(w.elems[i], g.elems[i], path+"/"+w.keys[i], vars); err != nil {
				return err
			}
		}
	case '[':
		if len(w.elems) != len(g.elems) {
			return fmt.Errorf("%s: %d elements, want %d", pathOrRoot(path), len(g.elems), len(w.elems))
		}
		for i := range w.elems {
			if err := match(w.elems[i], g.elems[i], path+"/"+strconv.Itoa(i), vars); err != nil {
				return err
			}
		}
	default:
		if w.text != g.text {
			return fmt.Errorf("%s: want %s, got %s", pathOrRoot(path), describeNode(w), describeNode(g))
		}
	}
	return nil
}

func pathOrRoot(p string) string {
	if p == "" {
		return "/"
	}
	return p
}

func describeNode(n node) string {
	switch n.kind {
	case '{':
		return fmt.Sprintf("an object with keys %v", n.keys)
	case '[':
		return fmt.Sprintf("an array of %d", len(n.elems))
	case 's':
		if len(n.text) > 120 {
			return fmt.Sprintf("a string of %d bytes", len(n.text))
		}
		return strconv.Quote(n.text)
	case 'z':
		return "null"
	default:
		return n.text
	}
}
