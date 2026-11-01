package harness

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
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
//   - A barrier (sendAfter) waits behind every open stream whose key has a
//     prefix: a host-mode bye goes out only once every run_done queued
//     before it has gone out whole, while unrelated small frames keep
//     their priority.
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

	// msgRead and msgDecoded are, while a times hook is set
	// (SetTimesHook), when the reader had read the last frame of the
	// message it is handing to onMessage, and when it had decoded it.
	// Only the reader goroutine reads and writes them, onMessage included.
	msgRead, msgDecoded time.Time

	mu         sync.Mutex
	small      [][]byte // whole frames, length prefix included
	streams    []*outStream
	latest     map[string]*outStream // the last stream about each invocation
	barriers   []barrier             // held by sendAfter, in order
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

// barrier is a message that waits until no stream about a key with prefix
// is open (sendAfter).
type barrier struct {
	prefix string
	q      queued
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

// sendAfter encodes m and queues it behind every message about a key that
// begins with prefix that is still being sent in parts, and behind what
// waits about those keys: it goes out once all of them have gone out
// whole. Other messages are not held by it, nor held behind it, unless it
// is itself a stream. A host-mode bye waits so behind the run_done frames
// queued before it ("run:", ADR 0004 host mode item 3).
func (c *conn) sendAfter(m Message, prefix string) error {
	payload, err := Encode(m)
	if err != nil {
		return err
	}
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
		q.stream = &outStream{id: strconv.FormatUint(c.nextStream, 10), payload: payload}
	}
	if len(c.barriers) > 0 || c.openLocked(prefix) {
		c.barriers = append(c.barriers, barrier{prefix, q})
		return nil
	}
	c.enqueueLocked(q)
	select {
	case c.wake <- struct{}{}:
	default:
	}
	return nil
}

// openLocked reports whether a stream about a key that begins with prefix
// has not sent its last part.
func (c *conn) openLocked(prefix string) bool {
	for about := range c.latest {
		if strings.HasPrefix(about, prefix) {
			return true
		}
	}
	return false
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
	defer c.releaseBarriersLocked()
	for i, q := range s.after {
		c.enqueueLocked(q)
		if q.stream != nil {
			q.stream.after = append(q.stream.after, s.after[i+1:]...)
			return
		}
	}
}

// releaseBarriersLocked queues the barriers, in order, whose streams have
// all gone out; one that still waits keeps the later ones behind it.
func (c *conn) releaseBarriersLocked() {
	for len(c.barriers) > 0 && !c.openLocked(c.barriers[0].prefix) {
		c.enqueueLocked(c.barriers[0].q)
		c.barriers = c.barriers[1:]
	}
}

// partWritten, when set, is called by every connection's writer after it
// has written a part frame of a message about an invocation, with the
// invocation and whether the part was the last; tests hold delivery there.
var partWritten atomic.Pointer[func(about string, last bool)]

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
		var part *outStream
		var last bool
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
			part, last = s, end == len(s.payload)
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
		if f := partWritten.Load(); f != nil && part != nil && part.about != "" {
			(*f)(part.about, last)
		}
	}
}

// endOfStream reports whether a read error is the end of the peer's stream
// rather than a failed connection: EOF, a connection the engine closed, or
// ECONNRESET, with which Linux ends the reads of a stream socket whose peer
// closed it with data it had not read, as a harness does that exits right
// after its last frame while a pong to its heartbeat is still on its way
// (FU8). Whether the peer itself failed is its exit status's to tell.
func endOfStream(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, syscall.ECONNRESET)
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
		timed := timesHook.Load() != nil
		if timed {
			c.msgRead = time.Now()
		}
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
		if timed {
			c.msgDecoded = time.Now()
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
	c.small, c.streams, c.latest, c.barriers = nil, nil, nil, nil
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
