package remotegw

import (
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
)

// DefaultAppendWindow is the machine->phone append budget of §6.0: at most one append per
// window, i.e. <= 8/s sustained across journal AND terminal combined. It exists because the
// daemon's render debounce (16ms) can emit ~62 snapshots/s while the relay caps a target at
// MailboxAppendPerMin=600 on a TUMBLING minute -- so an uncoalesced peek exhausts the whole
// window in ~10s and is refused for the rest of it, starving the journal that shares the
// target (PB-GW-7).
const DefaultAppendWindow = 125 * time.Millisecond

// OutboundSink is everything the gateway sends toward the phone on the shared seq stream.
// *RelaySink is the production implementation; CoalescingSink wraps one.
type OutboundSink interface {
	JournalSink
	TerminalSink
}

// CoalesceConfig configures a CoalescingSink.
type CoalesceConfig struct {
	Inner  OutboundSink     // the sink that actually seals and appends
	Window time.Duration    // minimum spacing between appends (0 => DefaultAppendWindow)
	Now    func() time.Time // clock seam (nil => time.Now)
}

// CoalescingSink is the gateway's ADMISSION POLICY for the shared machine->phone stream: it
// admits at most one append per window and coalesces terminal snapshots latest-wins into the
// gaps. It is a WRAPPER around the sink rather than a change inside it so that sealing, seq
// allocation and outbound durability stay one concern (RelaySink) and the rate budget stays
// another.
//
// The split of duties is deliberate:
//   - Event and Snapshot are forwarded IMMEDIATELY and are never coalesced or dropped
//     (R-GW.5: journal records are never lost, and Gateway.deliver still gates its cursor on
//     the returned error). Each one CONSUMES the shared slot, which is what makes the budget
//     "combined" rather than per-stream.
//   - Terminal is latest-wins PER SESSION: the gateway runs one RunTerminal per watched
//     session and they all forward here, so the stash is KEYED BY SESSION. A single shared
//     slot would let one peek discard another's held-back frame, leaving that peek on a stale
//     grid forever (B1). A coalesced-away snapshot is ADMITTED, not failed -- only a real
//     seal/append failure is an error.
//   - Held snapshots are released OLDEST-FIRST through the one shared slot, so keying the
//     stash by session does not buy each session its own budget and no peek can monopolize
//     the slot while the others sit stale.
//   - A held snapshot still reaches the phone once production stops, via Flush (RunTerminal
//     calls it on every idle read wake). Without that the peek would sit on a stale grid
//     until the session emitted again -- for an idle terminal, never.
type CoalescingSink struct {
	inner  OutboundSink
	window time.Duration
	now    func() time.Time

	mu    sync.Mutex
	last  time.Time                             // when the shared slot was last consumed
	stash map[string]*protocol.TerminalSnapshot // session -> its newest held-back snapshot
	order []string                              // sessions holding one, oldest-first
}

// NewCoalescingSink returns a sink that forwards to cfg.Inner under the §6.0 append budget.
func NewCoalescingSink(cfg CoalesceConfig) *CoalescingSink {
	window := cfg.Window
	if window <= 0 {
		window = DefaultAppendWindow
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &CoalescingSink{
		inner:  cfg.Inner,
		window: window,
		now:    now,
		stash:  make(map[string]*protocol.TerminalSnapshot),
	}
}

// Snapshot forwards the reconnect roster immediately, consuming the shared slot.
func (c *CoalescingSink) Snapshot(roster []protocol.JournalRecord, cursor uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.last = c.now()
	return c.inner.Snapshot(roster, cursor)
}

// Event forwards one live journal record immediately, consuming the shared slot. A journal
// record is never coalesced, deferred or dropped: it is the one stream that must not lose a
// frame behind a saturating peek.
func (c *CoalescingSink) Event(rec protocol.JournalRecord) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.last = c.now()
	return c.inner.Event(rec)
}

// Terminal holds the snapshot as its session's newest (latest-wins per session) and then
// releases the oldest held snapshot if the shared slot is free. With one session that is the
// old behaviour exactly: the incoming snapshot is forwarded when the window has elapsed and
// coalesced away when it has not. The returned error is the release's, so a real seal/append
// failure still reaches the peek that triggered it; being coalesced is never an error.
func (c *CoalescingSink) Terminal(session string, lines []string, cols, rows int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, held := c.stash[session]; !held {
		c.order = append(c.order, session) // first hold since its last release: queue at the back
	}
	c.stash[session] = &protocol.TerminalSnapshot{Session: session, Lines: lines, Cols: cols, Rows: rows}
	return c.release(c.now())
}

// Flush releases the oldest held snapshot once the shared slot is free, so a peek that has
// gone idle still ships its final grid (RunTerminal calls it on every idle read wake). It is
// a no-op when nothing is held, so an idle peek costs no appends.
//
// It obeys the same slot as Terminal on purpose: there is one flusher PER LIVE PEEK, so a
// Flush that forced an append would multiply the §6.0 budget by the number of watched
// sessions. Waking at terminalIdleWake (== DefaultAppendWindow) means the first idle wake
// after production stops finds the slot free.
func (c *CoalescingSink) Flush() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.release(c.now())
}

// release forwards the snapshot held longest, if any, and consumes the shared slot. Oldest-
// first is what keeps the budget COMBINED while the stash is per session: every held session
// takes its turn at the one slot instead of the loudest peek winning every window. Caller
// holds c.mu.
func (c *CoalescingSink) release(now time.Time) error {
	if len(c.order) == 0 || now.Sub(c.last) < c.window {
		return nil
	}
	session := c.order[0]
	c.order = c.order[1:]
	snap := c.stash[session]
	delete(c.stash, session)
	c.last = now
	return c.inner.Terminal(snap.Session, snap.Lines, snap.Cols, snap.Rows)
}

// DeliveredCursor forwards the inner sink's durable PB-GW-8 cursor so a restarted gateway
// still finds its resume point through the wrapper.
func (c *CoalescingSink) DeliveredCursor() uint64 {
	if cs, ok := c.inner.(CursorSource); ok {
		return cs.DeliveredCursor()
	}
	return 0
}
