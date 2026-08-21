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
// IT IS THE ONE PLACE THE COMBINED CEILING CAN BE ENFORCED. Every machine->phone append the
// gateway makes on the journal/terminal stream passes here, and the ceiling is per TARGET
// across both streams (§6.0, IS-DELTA-2a: "admission SHALL be bounded per target and SHALL
// govern every kind ... it exempts nobody"). ItemAdmission is a second, upstream floor in a
// DIFFERENT PROCESS -- the daemon's -- so the two cannot share a budget object; what makes
// the ceiling hold anyway is that an item release arrives here as a journal record and is
// charged to the same slot as a snapshot (see debitLocked).
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

	mu       sync.Mutex
	nextFree time.Time                           // when the shared slot is next free
	stash    map[string]*protocol.TerminalViewV1 // session -> its newest held-back snapshot
	order    []string                            // sessions holding one, oldest-first
}

// debitLocked charges ONE append to the shared per-target slot and returns nothing: the
// charge is unconditional, the wait is not. A journal record does not wait (R-GW.5) but it
// still spends, so the slot's next free instant moves forward from wherever it already was
// rather than from now -- which is the whole of the fix for the combined ceiling. Spending
// from `now` on every append (`last = now`) is what let the two streams interleave at 2x: a
// journal record landing 1 ms after a snapshot released reset the clock the snapshot had just
// paid for, so each stream saw a free slot every window and the target saw two.
//
// A slot in the past means the stream is idle and the next append is admitted at once, so
// this stays a SPACING FLOOR and never a batching delay (IS-DELTA-2).
//
// ponytail: the debt is UNCLAMPED. A burst of journal records pushes the terminal's next
// release out by one window each, and that is the honest arithmetic rather than a bug to cap:
// those appends really were spent out of MailboxAppendPerMin, and the terminal is the only
// stream that can pay them back (R-GW.5 forbids the journal doing it). The debt is bounded in
// practice by the producer's own floor, which holds the journal side to one release per
// window machine-wide (ADR-010 §7); the test fence asserts the peek recovers within a handful
// of idle wakes after a saturated transcript, which is what would break if it were not.
func (c *CoalescingSink) debitLocked(now time.Time) {
	if now.After(c.nextFree) {
		c.nextFree = now
	}
	c.nextFree = c.nextFree.Add(c.window)
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
		stash:  make(map[string]*protocol.TerminalViewV1),
	}
}

// SetMachine passes the daemon's endpoint id through to the wrapped sink. Admission policy
// is a wrapper, so a sink behind it would otherwise never be reachable for the stamp and the
// reconcile record would go out unattributable (see RelaySink.SetMachine).
func (c *CoalescingSink) SetMachine(machine string) {
	if m, ok := c.inner.(machineNamer); ok {
		m.SetMachine(machine)
	}
}

// Reseed forwards the journal repair frame straight through, UNCOALESCED. Coalescing is
// latest-wins per session and exists for the peek's ~62 snapshots/s; a reseed is a
// one-per-request whole-roster repair the phone is blocked on, and holding it would be
// holding the only thing that clears a stale channel. Its own rate bound is the phone's
// (§6.0: <= 1 per stream per 5 s), enforced before the frame is ever authored.
//
// It is still CHARGED to the shared slot: it is an append on the same target and the same seq
// stream, and the ceiling counts appends, not streams (IS-DELTA-2a). Charging without waiting
// is exactly what Event does, for the same reason.
func (c *CoalescingSink) Reseed(rs protocol.JournalReseed) error {
	rr, ok := c.inner.(ReseedSink)
	if !ok {
		return errNoReseedSink
	}
	c.mu.Lock()
	c.debitLocked(c.now())
	c.mu.Unlock()
	return rr.Reseed(rs)
}

// Snapshot forwards the reconnect roster immediately, consuming the shared slot.
func (c *CoalescingSink) Snapshot(roster []protocol.JournalRecord, cursor uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.debitLocked(c.now())
	return c.inner.Snapshot(roster, cursor)
}

// Event forwards one live journal record immediately, consuming the shared slot. A journal
// record is never coalesced, deferred or dropped: it is the one stream that must not lose a
// frame behind a saturating peek.
//
// It is also where an interaction item's release lands (ADR-010 §7's floor releases into the
// daemon's journal, one process upstream), so charging it here is what makes the item stream
// and the terminal stream share ONE per-target ceiling instead of two (IS-DELTA-2a).
func (c *CoalescingSink) Event(rec protocol.JournalRecord) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.debitLocked(c.now())
	return c.inner.Event(rec)
}

// Terminal holds the snapshot as its session's newest (latest-wins per session) and then
// releases the oldest held snapshot if the shared slot is free. With one session that is the
// old behaviour exactly: the incoming snapshot is forwarded when the window has elapsed and
// coalesced away when it has not. The returned error is the release's, so a real seal/append
// failure still reaches the peek that triggered it; being coalesced is never an error.
func (c *CoalescingSink) Terminal(view protocol.TerminalViewV1) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, held := c.stash[view.Session]; !held {
		c.order = append(c.order, view.Session) // first hold since its last release: queue at the back
	}
	held := view
	c.stash[view.Session] = &held
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
//
// THE ARBITRATION between the two streams lives in this one condition. The terminal is the
// stream that YIELDS when both press at once, because the journal cannot: R-GW.5 forbids
// delaying a record, and ADR-009 (2) already spends the whole budget on the journal ("no
// snapshot frames are appended to a phone ... the transcript inherits the whole of what the
// peek used to spend"). Yielding is not loss -- the stash is latest-wins per session, so a
// held frame is superseded by a newer one and ships on the first idle wake after the
// transcript goes quiet.
func (c *CoalescingSink) release(now time.Time) error {
	if len(c.order) == 0 || now.Before(c.nextFree) {
		return nil
	}
	session := c.order[0]
	c.order = c.order[1:]
	snap := c.stash[session]
	delete(c.stash, session)
	c.debitLocked(now)
	return c.inner.Terminal(*snap)
}

// DeliveredCursor forwards the inner sink's durable PB-GW-8 cursor so a restarted gateway
// still finds its resume point through the wrapper.
func (c *CoalescingSink) DeliveredCursor() uint64 {
	if cs, ok := c.inner.(CursorSource); ok {
		return cs.DeliveredCursor()
	}
	return 0
}
