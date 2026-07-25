package phonecore

// PB-INPUT-5 / PB-INPUT-6 / PB-INPUT-1 -- the phone's input coalescer.
//
// One MailboxAppend per keystroke is 30 appends/s at a held-down key, against the relay's
// MailboxAppendPerMin of 600: the lease dies with codeQuotaExceeded after roughly twenty
// seconds of autorepeat. This buffers keystrokes into at most one frame per
// InputFrameInterval, which is 8 frames/s -- §6.0's sustained ceiling, with 20% headroom
// under the relay's cap.
//
// LEADING EDGE, NOT TRAILING. The first byte of a burst is emitted immediately and the
// window is held afterwards. A purely trailing-edge coalescer would add a flat 125 ms to
// the first keystroke after any pause, which on its own is past §6.0's 150 ms p50 budget
// (S6b measured p50 31 ms phone -> PTY). §6.0's "<= 8 frames/s" is a SUSTAINED-REGIME
// average, not a ban on the leading edge.
//
// NOTHING HERE IS A QUEUE (ADR-007 D7). Buffered bytes are flushed at every boundary or
// resolved as an explicit Undelivered entry; they are never held across a disconnect and
// replayed, and a frame whose send failed is recorded, never handed back.

import (
	"sync"
	"time"
)

const (
	// InputFrameInterval is the minimum spacing between two coalesced input frames for
	// one session: 125 ms, i.e. 8 frames/s (§6.0). 100 ms would be exactly the relay's
	// 10/s mailbox_append cap, so the first jitter would trip codeQuotaExceeded mid-lease.
	InputFrameInterval = 125 * time.Millisecond

	// MaxInputPayload caps one coalesced frame at 4 KiB (§6.0). A window that accumulated
	// without a cap would eventually produce a frame the relay refuses on size, losing the
	// whole burst at once rather than pacing it.
	MaxInputPayload = 4096
)

// Undelivered is one accepted-but-not-delivered unit of input, kept so PB-INPUT-1's
// "delivery unknown / not sent" can be SURFACED. Bytes is how much the user typed (0 for a
// resize); At is read from the injected clock, never from a second clock authority.
type Undelivered struct {
	Session string
	Bytes   int
	At      time.Time
	Reason  string
}

// InputCoalescer buffers per-session keystrokes into frames the caller seals verbatim with
// SealInputData / SealInputResize. Safe for concurrent use: the Android UI thread types
// while the drain timer flushes.
type InputCoalescer struct {
	now func() time.Time

	mu       sync.Mutex
	sessions []*inputBuffer // insertion-ordered, so multi-session output is deterministic
	ledger   []Undelivered
}

// inputBuffer is one session's held bytes and its window.
type inputBuffer struct {
	session  string
	buf      []byte
	lastEmit time.Time
	started  bool // an emission has happened, so lastEmit is meaningful
}

// NewInputCoalescer returns a coalescer reading now for every window decision. The clock is
// injected because §6.0's rate is a property of the algorithm, not of the host.
func NewInputCoalescer(now func() time.Time) *InputCoalescer {
	if now == nil {
		now = time.Now
	}
	return &InputCoalescer{now: now}
}

// Type accepts a keystroke burst and returns the frames that are due now: the leading edge
// of a burst, plus any 4 KiB chunks the buffer has accumulated. Bytes not returned are held
// for the rest of the window.
func (c *InputCoalescer) Type(session string, data []byte) []InputFrame {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.buffer(session)
	s.buf = append(s.buf, data...)
	return c.drain(s, c.now(), false)
}

// Insert is the atomic entry point for a PASTE or an IME COMMIT (PB-INPUT-6). Neither is a
// keystroke stream, so neither is subject to the 125 ms window -- that window exists to
// coalesce autorepeat, and holding a single event for it buys nothing and costs 125 ms of
// visible latency. Buffered keystrokes are flushed FIRST so a paste can never overtake
// characters the user typed before it, and an oversize unit is split at MaxInputPayload and
// at no other point.
//
// An IME PREEDIT is never sent, and the absence of an entry point for one is the decision:
// a preedit is local until the IME commits, at which point it arrives here. Sending preedit
// text would type-then-correct against a live shell.
func (c *InputCoalescer) Insert(session string, data []byte) []InputFrame {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.buffer(session)
	now := c.now()
	out := c.drain(s, now, true)
	for len(data) > 0 {
		n := min(len(data), MaxInputPayload)
		out = append(out, InputFrame{T: "data", Session: session, Data: copyBytes(data[:n])})
		data = data[n:]
	}
	if len(out) > 0 {
		s.started, s.lastEmit = true, now
	}
	return out
}

// Resize flushes the session's buffered keystrokes BEFORE the resize frame (PB-INPUT-6). A
// resize that overtook buffered input is not a cosmetic reordering: the shell re-wraps the
// line and the bytes land against a grid the user was not looking at when they typed them.
func (c *InputCoalescer) Resize(session string, cols, rows int) []InputFrame {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.buffer(session)
	now := c.now()
	out := append(c.drain(s, now, true), InputFrame{T: "resize", Session: session, Cols: cols, Rows: rows})
	s.started, s.lastEmit = true, now
	return out
}

// Due returns the frames whose window has elapsed. It is the caller's cadence hook: the
// tail of a burst is held until either the next Type or the next Due.
func (c *InputCoalescer) Due() []InputFrame {
	return c.all(false)
}

// Flush empties every buffer regardless of the window. It is PB-INPUT-6's boundary
// mechanism -- release / take_control_end, app backgrounding and biometric-freshness expiry
// are one mechanism, because each ends the ability to type and must leave nothing buffered.
// The bytes are CONSUMED, never copied, so a flushed line cannot be typed twice.
func (c *InputCoalescer) Flush() []InputFrame {
	return c.all(true)
}

// Buffered is how many bytes are held for a session.
func (c *InputCoalescer) Buffered(session string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.sessions {
		if s.session == session {
			return len(s.buf)
		}
	}
	return 0
}

// Abandon discards every buffered byte and resolves it as an explicit "delivery unknown /
// not sent" (PB-INPUT-1). It is what a disconnect calls: the bytes are neither replayed --
// a keystroke landing minutes later is the hazard ADR-007 D7 makes structural -- nor
// silently dropped, which would leave the user believing they typed it. The returned
// entries are also retained for the UI to read.
func (c *InputCoalescer) Abandon(reason string) []Undelivered {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	var out []Undelivered
	for _, s := range c.sessions {
		if len(s.buf) == 0 {
			continue
		}
		out = append(out, Undelivered{Session: s.session, Bytes: len(s.buf), At: now, Reason: reason})
		s.buf = nil
	}
	c.ledger = append(c.ledger, out...)
	return out
}

// Fail records a frame the coalescer already handed out whose send failed (transport
// ErrNotDelivered). It is recorded and NOT re-buffered: re-buffering a failed live frame
// turns the live-only path into a queue, one frame at a time.
func (c *InputCoalescer) Fail(f InputFrame, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ledger = append(c.ledger, Undelivered{
		Session: f.Session,
		Bytes:   len(f.Data),
		At:      c.now(),
		Reason:  reason,
	})
}

// Undelivered is the ledger of everything the phone accepted from the user and could not
// deliver. It is READ, not drained: the UX state must survive the call that produced it.
func (c *InputCoalescer) Undelivered() []Undelivered {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Undelivered(nil), c.ledger...)
}

// buffer returns the session's buffer, creating it on first use. Caller holds c.mu.
func (c *InputCoalescer) buffer(session string) *inputBuffer {
	for _, s := range c.sessions {
		if s.session == session {
			return s
		}
	}
	s := &inputBuffer{session: session}
	c.sessions = append(c.sessions, s)
	return s
}

// all drains every session at one clock reading.
func (c *InputCoalescer) all(force bool) []InputFrame {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	var out []InputFrame
	for _, s := range c.sessions {
		out = append(out, c.drain(s, now, force)...)
	}
	return out
}

// drain emits what s owes at now, in order. A frame goes out when the window has elapsed,
// when nothing has been emitted yet (the leading edge), when the buffer has reached the
// 4 KiB cap, or when the caller forces it. Caller holds c.mu.
func (c *InputCoalescer) drain(s *inputBuffer, now time.Time, force bool) []InputFrame {
	var out []InputFrame
	for len(s.buf) > 0 {
		if !force && s.started && now.Sub(s.lastEmit) < InputFrameInterval && len(s.buf) < MaxInputPayload {
			break
		}
		n := min(len(s.buf), MaxInputPayload)
		out = append(out, InputFrame{T: "data", Session: s.session, Data: copyBytes(s.buf[:n])})
		s.buf = s.buf[n:]
		s.started, s.lastEmit = true, now
	}
	return out
}

// copyBytes copies a payload out of the buffer, so the emitted frame never aliases bytes a
// later append may reuse.
func copyBytes(b []byte) []byte { return append([]byte(nil), b...) }
