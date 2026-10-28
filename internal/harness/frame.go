package harness

import (
	"encoding/binary"
	"errors"
	"io"
	"strconv"
)

// Size limits of ADR 0004 §Transport and framing.
const (
	// MaxFrame is the largest frame payload: 1 MiB. A message whose
	// payload is longer is sent as a part stream.
	MaxFrame = 1 << 20
	// DefaultMaxMessage is the largest message accepted unless the engine
	// lowers it in the welcome: 512 MiB, above Windmill's default result
	// limit of 500 MB (WR-15), so that limit stays the engine's to apply.
	DefaultMaxMessage int64 = 512 << 20
	// PartChunk is how many message bytes a part frame carries when this
	// package splits a message: the largest multiple of 3 whose base64 form
	// leaves 1 KiB of the frame for the rest of the part envelope. It is
	// not a protocol constant; receivers check only MaxFrame.
	PartChunk = (MaxFrame - 1024) / 4 * 3
)

// appendFrame appends the frame carrying payload: its length as 4 bytes,
// big-endian, then the payload.
func appendFrame(dst, payload []byte) []byte {
	dst = binary.BigEndian.AppendUint32(dst, uint32(len(payload)))
	return append(dst, payload...)
}

// readFrame reads one frame from r and returns its payload. It returns
// io.EOF only when r ends exactly between frames.
func readFrame(r io.Reader, maxFrame int) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, protoErr(CodeTruncated, "stream ended inside a frame length")
		}
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	switch {
	case n == 0:
		return nil, protoErr(CodeFrameEmpty, "frame of length 0")
	case uint64(n) > uint64(maxFrame):
		return nil, protoErr(CodeFrameTooLarge, "frame of %d bytes exceeds %d", n, maxFrame)
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, protoErr(CodeTruncated, "stream ended inside a frame of %d bytes", n)
		}
		return nil, err
	}
	return payload, nil
}

// splitPayload cuts payload into the part frames of stream id, chunk bytes
// at a time. Each returned slice is a part frame's payload.
func splitPayload(id string, payload []byte, chunk int) ([][]byte, error) {
	var out [][]byte
	for seq, off := 0, 0; off < len(payload); seq++ {
		end := min(off+chunk, len(payload))
		p, err := Encode(Part{ID: id, Seq: seq, Last: end == len(payload), Chunk: payload[off:end]})
		if err != nil {
			return nil, err
		}
		out = append(out, p)
		off = end
	}
	return out, nil
}

// assembler reassembles part streams. Streams from one sender may
// interleave with each other and with whole frames.
type assembler struct {
	maxMessage int64
	streams    map[string]*inStream
}

type inStream struct {
	buf  []byte
	next int
}

func newAssembler(maxMessage int64) *assembler {
	return &assembler{maxMessage: maxMessage, streams: map[string]*inStream{}}
}

// add takes one part frame. When it completes its stream, add returns the
// reassembled payload and true.
func (a *assembler) add(p Part) ([]byte, bool, error) {
	s := a.streams[p.ID]
	if p.Seq == 0 {
		if s != nil {
			return nil, false, protoErr(CodePartSequence, "part stream %q started twice", p.ID)
		}
		s = &inStream{}
		a.streams[p.ID] = s
	} else if s == nil || p.Seq != s.next {
		want := "seq 0"
		if s != nil {
			want = "seq " + strconv.Itoa(s.next)
		}
		return nil, false, protoErr(CodePartSequence, "part stream %q got seq %d, want %s", p.ID, p.Seq, want)
	}
	if int64(len(s.buf))+int64(len(p.Chunk)) > a.maxMessage {
		delete(a.streams, p.ID)
		return nil, false, protoErr(CodeMessageTooLarge, "part stream %q exceeds %d bytes", p.ID, a.maxMessage)
	}
	s.buf = append(s.buf, p.Chunk...)
	s.next++
	if !p.Last {
		return nil, false, nil
	}
	delete(a.streams, p.ID)
	return s.buf, true, nil
}

// decodeFrame decodes a whole frame's payload, which counts against the
// message limit like a reassembled one (a host may lower the limit below
// MaxFrame).
func decodeFrame(payload []byte, maxMessage int64) (Message, error) {
	if int64(len(payload)) > maxMessage {
		return nil, protoErr(CodeMessageTooLarge, "frame of %d bytes exceeds the message limit of %d", len(payload), maxMessage)
	}
	return Decode(payload)
}

// decodeAssembled decodes a reassembled payload, which must be a whole
// message and not another part.
func decodeAssembled(payload []byte) (Message, error) {
	m, err := Decode(payload)
	if err != nil {
		return nil, err
	}
	if _, ok := m.(Part); ok {
		return nil, protoErr(CodePartInPart, "a part stream reassembled to a part frame")
	}
	return m, nil
}
