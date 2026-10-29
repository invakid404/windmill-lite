package harness

import (
	"bufio"
	"encoding/binary"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// conn multiplexes messages over one stream socket (ADR 0004 §Transport and
// framing). It has one writer goroutine and one reader goroutine:
//
//   - The writer sends every queued whole frame before the next part frame,
//     and takes part streams in turn, one part frame each. A small frame
//     (cancel, pong, a short result) therefore waits behind at most one
//     part frame on the wire, never behind a whole large message.
//   - Messages about one invocation keep their order: one queued behind a
//     part stream about the same invocation waits until that stream has
//     sent its last part. A cancel never overtakes its own large invoke.
//   - The reader reassembles part streams and hands each message to
//     onMessage, in arrival order, on the reader goroutine. It records when
//     it last received a frame, which is what the watchdog measures; that
//     time does not depend on whether the writer is blocked.
type conn struct {
	nc         net.Conn
	maxFrame   int
	chunk      int
	maxMessage int64
	onMessage  func(Message)
	onClose    func(error)

	born     time.Time
	lastRecv atomic.Int64 // nanoseconds since born

	mu         sync.Mutex
	small      [][]byte // whole frames, length prefix included
	streams    []*outStream
	latest     map[string]*outStream // the last stream about each invocation
	closed     bool
	nextStream uint64
	wake       chan struct{}
}

type outStream struct {
	id      string
	about   string // the invocation the message is about, if any
	payload []byte
	off     int
	seq     int
	// after holds what was queued about the same invocation while this
	// stream was open, in order: whole frames, or a stream that has not
	// started.
	after []queued
}

type queued struct {
	frame  []byte
	stream *outStream
}

type connConfig struct {
	maxMessage int64
	// maxFrame and chunk default to MaxFrame and PartChunk; tests lower
	// them to exercise splitting with small messages.
	maxFrame int
	chunk    int
	// onMessage receives every message; onClose is called once, after the
	// reader stops, with the error that stopped it (io.EOF at a clean end).
	onMessage func(Message)
	onClose   func(error)
}

func newConn(nc net.Conn, cfg connConfig) *conn {
	c := &conn{
		nc:         nc,
		maxFrame:   cfg.maxFrame,
		chunk:      cfg.chunk,
		maxMessage: cfg.maxMessage,
		onMessage:  cfg.onMessage,
		onClose:    cfg.onClose,
		born:       time.Now(),
		wake:       make(chan struct{}, 1),
	}
	if c.maxFrame == 0 {
		c.maxFrame = MaxFrame
	}
	if c.chunk == 0 {
		c.chunk = PartChunk
	}
	if c.maxMessage == 0 {
		c.maxMessage = DefaultMaxMessage
	}
	return c
}

// run starts the writer and the reader. It is separate from newConn so the
// owner can store the conn before its callbacks can run.
func (c *conn) run() {
	go c.writeLoop()
	go c.readLoop()
}

// send encodes m and queues it. It fails without sending anything when m
// cannot be encoded or is larger than the maximum message.
func (c *conn) send(m Message) error {
	payload, err := Encode(m)
	if err != nil {
		return err
	}
	return c.sendPayload(payload, invocationOf(m))
}

// invocationOf returns the invocation a message is about, for the
// ordering rule, or "" for one that is about none: a platform call is
// about the invocation its Inv names, so a result never overtakes a large
// platform call made before it (milestone PF1). A host's runs are ordered
// the same way, under a key of their own: a run_cancel never overtakes the
// large run it cancels.
func invocationOf(m Message) string {
	switch m := m.(type) {
	case PlatformRequest:
		return m.Inv
	case Invoke:
		return m.ID
	case Cancel:
		return m.ID
	case Result:
		return m.ID
	case Late:
		return m.ID
	case Log:
		return m.ID
	case Warning:
		return m.ID
	case RunRequest:
		return "run:" + m.ID
	case RunDone:
		return "run:" + m.ID
	case RunCancel:
		return "run:" + m.ID
	case RunSettled:
		return "run:" + m.ID
	}
	return ""
}

// sendPayload queues a payload; about is the invocation it is about, or "".
func (c *conn) sendPayload(payload []byte, about string) error {
	if int64(len(payload)) > c.maxMessage {
		return protoErr(CodeMessageTooLarge, "%s message of %d bytes exceeds %d", typeOf(payload), len(payload), c.maxMessage)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	var q queued
	if len(payload) <= c.maxFrame {
		q.frame = appendFrame(make([]byte, 0, 4+len(payload)), payload)
	} else {
		c.nextStream++
		q.stream = &outStream{id: strconv.FormatUint(c.nextStream, 10), about: about, payload: payload}
	}
	if held := c.latest[about]; about != "" && held != nil {
		held.after = append(held.after, q)
	} else {
		c.enqueueLocked(q)
	}
	if q.stream != nil && about != "" {
		if c.latest == nil {
			c.latest = map[string]*outStream{}
		}
		c.latest[about] = q.stream
	}
	select {
	case c.wake <- struct{}{}:
	default:
	}
	return nil
}

func (c *conn) enqueueLocked(q queued) {
	if q.frame != nil {
		c.small = append(c.small, q.frame)
	} else {
		c.streams = append(c.streams, q.stream)
	}
}

// finishLocked releases what waited behind a stream that sent its last
// part: whole frames up to the next stream, which starts and keeps the
// rest behind it.
func (c *conn) finishLocked(s *outStream) {
	if c.latest[s.about] == s {
		delete(c.latest, s.about)
	}
	for i, q := range s.after {
		c.enqueueLocked(q)
		if q.stream != nil {
			q.stream.after = append(q.stream.after, s.after[i+1:]...)
			return
		}
	}
}

func (c *conn) writeLoop() {
	var scratch []byte
	for {
		c.mu.Lock()
		for len(c.small) == 0 && len(c.streams) == 0 && !c.closed {
			c.mu.Unlock()
			<-c.wake
			c.mu.Lock()
		}
		if c.closed {
			c.mu.Unlock()
			return
		}
		var bufs net.Buffers
		if len(c.small) > 0 {
			bufs, c.small = c.small, nil
		} else {
			s := c.streams[0]
			end := min(s.off+c.chunk, len(s.payload))
			// The frame is built in place behind a 4-byte gap for its
			// length; scratch is reused because WriteTo finishes with it
			// before the next turn.
			frame, err := Part{ID: s.id, Seq: s.seq, Last: end == len(s.payload), Chunk: s.payload[s.off:end]}.appendPayload(append(scratch[:0], 0, 0, 0, 0))
			if err != nil {
				// Unreachable: the chunk is non-empty and the id ASCII.
				panic(err)
			}
			binary.BigEndian.PutUint32(frame, uint32(len(frame)-4))
			scratch = frame
			s.off, s.seq = end, s.seq+1
			c.streams = c.streams[1:]
			if s.off < len(s.payload) {
				c.streams = append(c.streams, s)
			} else {
				c.finishLocked(s)
			}
			bufs = net.Buffers{frame}
		}
		c.mu.Unlock()
		if _, err := bufs.WriteTo(c.nc); err != nil {
			// The reader sees the same failure (or the peer's exit) and
			// reports it; the writer only stops.
			c.close()
			return
		}
	}
}

func (c *conn) readLoop() {
	r := bufio.NewReaderSize(c.nc, 64<<10)
	asm := newAssembler(c.maxMessage)
	for {
		payload, err := readFrame(r, c.maxFrame)
		if err != nil {
			c.onClose(err)
			return
		}
		c.lastRecv.Store(int64(time.Since(c.born)))
		m, err := decodeFrame(payload, c.maxMessage)
		if err == nil {
			if p, ok := m.(Part); ok {
				full, done, aerr := asm.add(p)
				if aerr != nil {
					err = aerr
				} else if !done {
					continue
				} else {
					m, err = decodeAssembled(full)
				}
			}
		}
		if err != nil {
			c.onClose(err)
			return
		}
		c.onMessage(m)
	}
}

// lastReceived returns when the last frame arrived, or when the conn was
// made if none has.
func (c *conn) lastReceived() time.Time {
	return c.born.Add(time.Duration(c.lastRecv.Load()))
}

// close stops the writer and closes the socket, which ends the reader.
// Queued frames are dropped.
func (c *conn) close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.small, c.streams, c.latest = nil, nil, nil
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
	c.nc.Close()
}

// typeOf names the message type of a payload for diagnostics, without
// decoding it.
func typeOf(payload []byte) string {
	const prefix = `{"t":"`
	if len(payload) > len(prefix) && string(payload[:len(prefix)]) == prefix {
		rest := payload[len(prefix):]
		for i, b := range rest {
			if b == '"' {
				return string(rest[:i])
			}
			if i > 16 {
				break
			}
		}
	}
	return "a"
}
