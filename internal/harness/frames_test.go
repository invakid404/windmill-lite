package harness

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"slices"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files in conformance/protocol from this run")

// framesFile is conformance/protocol/frames.json: the codec's golden
// frames, which the Bun harness's developer tests check its own codec
// against.
type framesFile struct {
	Level      string      `json:"level"`
	Title      string      `json:"title"`
	Design     string      `json:"design"`
	Provenance string      `json:"provenance"`
	ReliesOn   []string    `json:"relies_on"`
	Note       string      `json:"note"`
	Cases      []frameCase `json:"cases"`
}

type frameCase struct {
	Case  string   `json:"case"`
	Title string   `json:"title"`
	Sides []string `json:"sides,omitempty"`

	// Encode and split cases: the message, as JSON or as a payload text in
	// any key order, and the canonical payload Go writes for it.
	Message     json.RawMessage   `json:"message,omitempty"`
	MessageText string            `json:"message_text,omitempty"`
	Raw         map[string]string `json:"raw,omitempty"`
	Split       *splitSpec        `json:"split,omitempty"`
	Payload     string            `json:"payload,omitempty"`

	// Split cases: the part payloads. Sequence and reject cases: payloads
	// to read in order.
	Frames []string `json:"frames,omitempty"`
	// Sequence cases: what the frames decode to.
	Messages []json.RawMessage `json:"messages,omitempty"`

	// Reject cases: the raw bytes to read (or Frames), the limits, and the
	// protocol error code expected.
	BytesHex   string `json:"bytes_hex,omitempty"`
	MaxFrame   int    `json:"max_frame,omitempty"`
	MaxMessage int64  `json:"max_message,omitempty"`
	Error      string `json:"error,omitempty"`
}

type splitSpec struct {
	Stream     string `json:"stream"`
	ChunkBytes int    `json:"chunk_bytes"`
}

const framesPath = "conformance/protocol/frames.json"

func loadFrames(t *testing.T) (string, framesFile) {
	t.Helper()
	path := repoPath(t, framesPath)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f framesFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("%s: %v", framesPath, err)
	}
	return path, f
}

// TestFrameCases runs the shared codec cases. With -update it rewrites the
// payloads and part frames from Go's encoder.
func TestFrameCases(t *testing.T) {
	path, f := loadFrames(t)
	if f.Level != "protocol" || f.Provenance != "policy" || len(f.Cases) == 0 {
		t.Fatalf("%s: unexpected header %+v", framesPath, f)
	}
	seen := map[string]bool{}
	for i := range f.Cases {
		c := &f.Cases[i]
		if seen[c.Case] {
			t.Fatalf("duplicate case %q", c.Case)
		}
		seen[c.Case] = true
		if len(c.Sides) > 0 && !slices.Contains(c.Sides, "go") {
			continue
		}
		t.Run(c.Case, func(t *testing.T) {
			switch {
			case c.Error != "":
				runReject(t, c)
			case c.Messages != nil:
				runSequence(t, c)
			default:
				runEncode(t, c)
			}
		})
	}
	if *update {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(f); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func runEncode(t *testing.T, c *frameCase) {
	// A message given as JSON is compacted first, which keeps key order and
	// number spelling, so the file's indentation does not reach raw values;
	// message_text is decoded exactly as written.
	src := []byte(c.MessageText)
	if c.Message != nil {
		var b bytes.Buffer
		if err := json.Compact(&b, c.Message); err != nil {
			t.Fatal(err)
		}
		src = b.Bytes()
	}
	m, err := Decode(src)
	if err != nil {
		t.Fatalf("decode message: %v", err)
	}
	got, err := Encode(m)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if *update {
		c.Payload = string(got)
	} else if string(got) != c.Payload {
		t.Fatalf("payload\n got %s\nwant %s", got, c.Payload)
	}
	// The canonical payload decodes to the same message and re-encodes to
	// itself.
	m2, err := Decode([]byte(c.Payload))
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if again, _ := Encode(m2); string(again) != c.Payload {
		t.Fatalf("payload does not re-encode to itself: %s", again)
	}
	for field, want := range c.Raw {
		var raw []byte
		switch v := m.(type) {
		case Invoke:
			if field == "args" {
				raw = v.Args.Producer()
			}
		case Result:
			raw = v.Body.Producer()
		case RunRequest:
			switch field {
			case "args":
				raw = v.Args.Producer()
			case "intercept":
				raw = v.Intercept.Producer()
			}
		case RunDone:
			raw = v.Result.Producer()
		}
		if string(raw) != want {
			t.Fatalf("raw %s = %q, want %q byte for byte", field, raw, want)
		}
	}
	if c.Split == nil {
		if len(got) > MaxFrame {
			t.Fatalf("an encode case must fit one frame")
		}
		return
	}
	parts, err := splitPayload(c.Split.Stream, got, c.Split.ChunkBytes)
	if err != nil {
		t.Fatal(err)
	}
	strs := make([]string, len(parts))
	for i, p := range parts {
		strs[i] = string(p)
	}
	if *update {
		c.Frames = strs
	} else if !slices.Equal(strs, c.Frames) {
		t.Fatalf("part frames\n got %q\nwant %q", strs, c.Frames)
	}
	msgs, err := readAll(framesOf(c.Frames), MaxFrame, DefaultMaxMessage)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("reassembly: %v, %d messages", err, len(msgs))
	}
	if b, _ := Encode(msgs[0]); string(b) != c.Payload {
		t.Fatalf("reassembled %s", b)
	}
}

func runSequence(t *testing.T, c *frameCase) {
	msgs, err := readAll(framesOf(c.Frames), MaxFrame, DefaultMaxMessage)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != len(c.Messages) {
		t.Fatalf("got %d messages, want %d", len(msgs), len(c.Messages))
	}
	for i, m := range msgs {
		want, err := Decode(c.Messages[i])
		if err != nil {
			t.Fatal(err)
		}
		a, _ := Encode(m)
		b, _ := Encode(want)
		if !bytes.Equal(a, b) {
			t.Fatalf("message %d\n got %s\nwant %s", i, a, b)
		}
	}
}

func runReject(t *testing.T, c *frameCase) {
	in := framesOf(c.Frames)
	if c.BytesHex != "" {
		b, err := hex.DecodeString(c.BytesHex)
		if err != nil {
			t.Fatal(err)
		}
		in = b
	}
	maxFrame, maxMessage := MaxFrame, DefaultMaxMessage
	if c.MaxFrame != 0 {
		maxFrame = c.MaxFrame
	}
	if c.MaxMessage != 0 {
		maxMessage = c.MaxMessage
	}
	_, err := readAll(in, maxFrame, maxMessage)
	if !IsProtocolError(err, c.Error) {
		t.Fatalf("got %v, want protocol error %s", err, c.Error)
	}
}

// framesOf concatenates payloads into a byte stream of frames.
func framesOf(payloads []string) []byte {
	var b []byte
	for _, p := range payloads {
		b = appendFrame(b, []byte(p))
	}
	return b
}

// readAll runs the reader's pipeline over a byte stream: frames, decoding
// and part reassembly, as conn.readLoop does.
func readAll(stream []byte, maxFrame int, maxMessage int64) ([]Message, error) {
	r := bytes.NewReader(stream)
	asm := newAssembler(maxMessage)
	var out []Message
	for {
		payload, err := readFrame(r, maxFrame)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		m, err := decodeFrame(payload, maxMessage)
		if err != nil {
			return out, err
		}
		if p, ok := m.(Part); ok {
			full, done, err := asm.add(p)
			if err != nil {
				return out, err
			}
			if !done {
				continue
			}
			if m, err = decodeAssembled(full); err != nil {
				return out, err
			}
		}
		out = append(out, m)
	}
}

// TestFrameCaseCodesAreKnown keeps the shared cases to the codes both
// codecs define.
func TestFrameCaseCodesAreKnown(t *testing.T) {
	_, f := loadFrames(t)
	known := []string{CodeFrameEmpty, CodeFrameTooLarge, CodeTruncated, CodeInvalidUTF8, CodeInvalidJSON, CodeNotObject,
		CodeMissingType, CodeUnknownType, CodeMissingField, CodeBadField, CodePartSequence, CodePartInPart, CodeBadChunk,
		CodeMessageTooLarge, CodeUnexpected}
	n := 0
	for _, c := range f.Cases {
		if c.Error != "" {
			n++
			if !slices.Contains(known, c.Error) {
				t.Errorf("case %s: unknown code %q", c.Case, c.Error)
			}
		}
	}
	if n == 0 {
		t.Fatal("no reject cases")
	}
}
