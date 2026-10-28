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
	closed     bool
	nextStream uint64
	wake       chan struct{}
}

type outStream struct {
	id      string
	payload []byte
	off     int
	seq     int
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
	return c.sendPayload(payload)
}

func (c *conn) sendPayload(payload []byte) error {
	if int64(len(payload)) > c.maxMessage {
		return protoErr(CodeMessageTooLarge, "%s message of %d bytes exceeds %d", typeOf(payload), len(payload), c.maxMessage)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	if len(payload) <= c.maxFrame {
		c.small = append(c.small, appendFrame(make([]byte, 0, 4+len(payload)), payload))
	} else {
		c.nextStream++
		c.streams = append(c.streams, &outStream{id: strconv.FormatUint(c.nextStream, 10), payload: payload})
	}
	select {
	case c.wake <- struct{}{}:
	default:
	}
	return nil
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
		m, err := Decode(payload)
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
	c.small, c.streams = nil, nil
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
