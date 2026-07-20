package journal

import (
	"sync"
	"time"
)

// Debouncer is the DELIVERY-layer flap coalescer (plan R-JRN.3, amendment D.0-A7):
// it NEVER lives in the durable journal (the durable log records every distinct net
// transition). A group_transition flap that settles within the window collapses to
// its final net state; a terminal lifecycle record is never delayed. A per-session
// window is anchored at the first offer for that session so a burst of flaps is
// coalesced and delivered once the burst settles.
type Debouncer struct {
	window time.Duration
	clock  func() time.Time

	mu        sync.Mutex
	pending   map[string]*pendingGroup // session_id -> coalesced group transition
	immediate []Record                 // terminal/non-group records due immediately
}

// pendingGroup is a coalescing group transition: the latest offered record and the
// window deadline anchored at the first offer.
type pendingGroup struct {
	rec      Record
	deadline time.Time
}

// NewDebouncer builds a delivery-layer debouncer with the given window and clock.
func NewDebouncer(window time.Duration, clock func() time.Time) *Debouncer {
	if clock == nil {
		clock = time.Now
	}
	return &Debouncer{window: window, clock: clock, pending: make(map[string]*pendingGroup)}
}

// Offer enqueues a record at clock-now. A group_transition coalesces into its
// session's pending window (keeping the first offer's deadline, updating to the
// latest net state); every other record type is a terminal lifecycle record and is
// queued for immediate delivery, never debounced.
func (d *Debouncer) Offer(r Record) {
	now := d.clock()
	d.mu.Lock()
	defer d.mu.Unlock()
	if r.Type != TypeGroupTransition {
		d.immediate = append(d.immediate, r)
		return
	}
	if p, ok := d.pending[r.SessionID]; ok {
		p.rec = r // keep the anchored deadline, settle to the latest net state
		return
	}
	d.pending[r.SessionID] = &pendingGroup{rec: r, deadline: now.Add(d.window)}
}

// Drain returns every record DUE for delivery as of now: all immediate (terminal)
// records plus each session's coalesced group transition whose window has elapsed.
func (d *Debouncer) Drain(now time.Time) []Record {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := d.immediate
	d.immediate = nil
	for sid, p := range d.pending {
		if !now.Before(p.deadline) {
			out = append(out, p.rec)
			delete(d.pending, sid)
		}
	}
	return out
}
