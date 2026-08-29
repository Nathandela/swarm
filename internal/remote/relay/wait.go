package relay

import (
	"encoding/json"
	"sync/atomic"
	"time"
)

// The bounded server-side wait (ADR-007 B7, PB-NET-5). A wait is the one relay
// operation that legitimately parks a connection for tens of seconds, so it is
// served on its OWN goroutine while the connection's request loop keeps reading:
// without that, a wait would head-of-line-block the very keystrokes it exists to
// accelerate, since serveConn is otherwise strictly readFrame -> dispatch ->
// readFrame and a second connection is not available (one conn per routing id
// with newest-wins takeover).
//
// Three properties the wait must NOT weaken, each with a test in
// s6b_wait_test.go:
//
//   - It is AUTHENTICATED-ONLY and is refused inline on the request loop. A
//     servable pre-auth wait would hand a slowloris a free MaxServerWait per
//     attempt, and readFrame bounds the cumulative time-to-authenticate only
//     while the connection is neither authed nor in a rendezvous.
//   - A takeover RELEASES the superseded connection's wait. The superseded
//     connection issues no further requests, so nothing else would ever tell it;
//     leaving it parked would hold the single per-client wait slot and kill live
//     typing for up to a whole ceiling after every reconnect.
//   - It holds no relay-wide lock while parked. s.mu is taken only to register,
//     release and signal.

// defaultMaxServerWait is §6.0's ceiling, used when Config.MaxServerWait is unset.
const defaultMaxServerWait = 25 * time.Second

// waitReason is why a parked wait was released.
type waitReason int32

const (
	// waitItems: the mailbox changed; re-read it.
	waitItems waitReason = iota
	// waitSuperseded: a newer connection took over this routing id.
	waitSuperseded
	// waitCancelled: the client withdrew the wait.
	waitCancelled
)

// pendingWait is one parked server-side wait. §6.0 caps them at one per client,
// so Server.waits is keyed by routing id and the map itself enforces the cap.
type pendingWait struct {
	id          uint64
	cursor      uint64
	incarnation string
	// rid is the routing id this wait was REGISTERED under, captured once under
	// s.mu. It is deliberately a copy rather than a read of sc.rid: re-running the
	// handshake on an established connection is a legal frame sequence, so sc.rid
	// can be rewritten on the request loop while this wait's own goroutine is
	// serving. Reading it from here would be an unsynchronised read of a string
	// header AND would silently move the wait onto a different mailbox.
	rid string
	sc  *serverConn
	// reason is sticky for the terminal reasons: a supersede or a cancel that
	// races an append must not be overwritten by the append's wake.
	reason atomic.Int32
	// wake is a capacity-1 pulse. It is never blocking-sent, so a signal can be
	// raised from under s.mu.
	wake chan struct{}
}

func newPendingWait(sc *serverConn, rid string, id, cursor uint64, incarnation string) *pendingWait {
	return &pendingWait{id: id, cursor: cursor, incarnation: incarnation, rid: rid, sc: sc, wake: make(chan struct{}, 1)}
}

// signal releases the wait. Terminal reasons win over waitItems and over each
// other's ordering, so a released wait never reports a delivery instead.
func (w *pendingWait) signal(r waitReason) {
	if r != waitItems {
		w.reason.CompareAndSwap(int32(waitItems), int32(r))
	}
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// waitReplyBody is the MsgWaitReply frame. Code is empty on success; a non-empty
// Code is the same wire code an r_error would have carried, so the client maps it
// back to the identical sentinel.
type waitReplyBody struct {
	WaitID      uint64 `json:"wait_id"`
	Items       []Item `json:"items"`
	HasMore     bool   `json:"has_more"`
	Incarnation string `json:"mailbox_incarnation,omitempty"`
	Code        string `json:"code,omitempty"`
}

// registerWait claims this client's single pending-wait slot. sc.rid is read HERE,
// under s.mu, on the connection's own request loop -- the one place it is safe to
// read it -- and every later use goes through the captured copy.
func (s *Server) registerWait(sc *serverConn, id, cursor uint64, incarnation string) (*pendingWait, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Refused both per CONNECTION and per routing id: the §6.0 cap is per client,
	// and a connection that re-authenticated could otherwise hold one of each.
	if sc.wait != nil {
		return nil, false
	}
	if _, busy := s.waits[sc.rid]; busy {
		return nil, false
	}
	w := newPendingWait(sc, sc.rid, id, cursor, incarnation)
	s.waits[w.rid] = w
	sc.wait = w
	return w, true
}

// releaseWait frees the slot, but only if it is still held by w: a superseded or
// cancelled wait was already unregistered by whoever released it, and its
// goroutine must not evict the successor's registration.
func (s *Server) releaseWait(w *pendingWait) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releaseWaitLocked(w)
}

func (s *Server) releaseWaitLocked(w *pendingWait) {
	if cur, ok := s.waits[w.rid]; ok && cur == w {
		delete(s.waits, w.rid)
	}
	if w.sc.wait == w {
		w.sc.wait = nil
	}
}

// severWaitLocked releases sc's parked wait, if any, with reason r. s.mu is held.
// It finds the wait through the connection's own back-pointer rather than by
// looking up sc.rid, so a connection that re-authenticated still releases the slot
// it actually holds instead of orphaning it under the old routing id.
func (s *Server) severWaitLocked(sc *serverConn, r waitReason) {
	w := sc.wait
	if w == nil {
		return
	}
	s.releaseWaitLocked(w)
	w.signal(r)
}

// notifyMailbox wakes the wait parked on rid's mailbox, if there is one. It is
// the append path's only coupling to the wait machinery.
func (s *Server) notifyMailbox(rid string) {
	s.mu.Lock()
	w := s.waits[rid]
	s.mu.Unlock()
	if w != nil {
		w.signal(waitItems)
	}
}

func (sc *serverConn) replyWait(b waitReplyBody) error {
	if b.Items == nil {
		b.Items = []Item{}
	}
	return sc.reply(MsgWaitReply, b)
}

// concludeWait ends a served wait: it frees the client's single wait slot BEFORE
// the reply reaches the wire, then replies.
//
// The order is load-bearing. A client that reads this reply sends its NEXT wait
// immediately, and that wait is admitted on the connection's request loop — a
// different goroutine from this one. Releasing after the write, even one
// statement later in a defer, leaves a window in which the slot is logically free
// but still registered, and the next wait loses the race and is refused with
// wait_in_progress: a spurious refusal for a client that broke no rule. Releasing
// first closes the window by construction rather than by timing.
//
// No lock is held across the write: releaseWait takes and drops s.mu, and only
// then does replyWait touch the socket.
func (sc *serverConn) concludeWait(w *pendingWait, b waitReplyBody) {
	sc.s.releaseWait(w)
	_ = sc.replyWait(b)
}

// handleMailboxWait admits a wait and hands it to its own goroutine. Everything
// that can refuse the wait is decided HERE, on the connection's request loop, so
// a refusal is immediate and no refused wait ever parks anything.
func (sc *serverConn) handleMailboxWait(payload []byte) error {
	var req struct {
		Cursor      uint64  `json:"cursor"`
		WaitID      uint64  `json:"wait_id"`
		Incarnation *string `json:"mailbox_incarnation"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return sc.replyErr(codeBadRequest)
	}
	// A wait is state-touching and must meter, exactly once per CALL regardless of
	// how many items it hands back — an unmetered state-touching op is the one
	// abuse hole the relay does not otherwise have, and metering per item would
	// make the batching that keeps the drain inside §6.0's budget buy nothing.
	if !sc.meterOp() {
		return sc.replyWait(waitReplyBody{WaitID: req.WaitID, Code: codeQuotaExceeded})
	}
	// An UNAUTHENTICATED connection is refused on the ordinary error frame: no
	// wait was ever created, so there is nothing to correlate, and the connection
	// stays in readFrame's cumulative handshake-deadline regime (PB-NET-5(d)).
	if !sc.authed {
		return sc.replyErr(codeNotAuthorized)
	}
	if sc.superseded.Load() {
		return sc.replyWait(waitReplyBody{WaitID: req.WaitID, Code: codeDuplicateConn})
	}
	if req.Incarnation != nil && *req.Incarnation == "" && req.Cursor > 0 {
		return sc.replyWait(waitReplyBody{WaitID: req.WaitID, Code: codeMailboxCursorReset})
	}
	expected := ""
	if req.Incarnation != nil {
		expected = *req.Incarnation
	}
	w, ok := sc.s.registerWait(sc, req.WaitID, req.Cursor, expected)
	if !ok {
		return sc.replyWait(waitReplyBody{WaitID: req.WaitID, Code: codeWaitInProgress})
	}
	go sc.serveWait(w)
	return nil
}

// handleMailboxWaitCancel withdraws a parked wait. It sends NO reply — the wait's
// own reply is its answer — and it is deliberately NOT metered: a cancel strictly
// RELEASES server state, so refusing it on quota would strand the single wait slot
// for the remaining ceiling, and it is already bounded by the one-wait-per-client
// cap.
func (sc *serverConn) handleMailboxWaitCancel(payload []byte) error {
	var req struct {
		WaitID uint64 `json:"wait_id"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return sc.replyErr(codeBadRequest)
	}
	// Pre-auth this could only ever be a no-op (rid is empty and no wait is keyed
	// there), but say so structurally rather than relying on that: an unmetered op
	// must be provably incapable of touching shared state, not merely unlikely to.
	if !sc.authed {
		return nil
	}
	sc.s.mu.Lock()
	w := sc.wait
	if w != nil && w.id == req.WaitID {
		sc.s.releaseWaitLocked(w)
	} else {
		w = nil // a stale id: the wait it names is already gone
	}
	sc.s.mu.Unlock()
	if w != nil {
		w.signal(waitCancelled)
	}
	return nil
}

// serveWait parks until the mailbox has something past the wait's cursor, the
// ceiling elapses, or the wait is released. It runs on its own goroutine and
// holds no lock while parked, so an append, a presence query or another client's
// traffic is served normally alongside it.
func (sc *serverConn) serveWait(w *pendingWait) {
	// The backstop for the paths that conclude WITHOUT a reply (the connection is
	// gone). Every replying path releases the slot first, through concludeWait, and
	// releaseWaitLocked is a no-op once the slot is free, so this defer never
	// double-releases nor evicts a successor's registration.
	defer sc.s.releaseWait(w)

	ceiling := sc.s.cfg.MaxServerWait
	if ceiling <= 0 {
		ceiling = defaultMaxServerWait
	}
	timer := time.NewTimer(ceiling)
	defer timer.Stop()

	for {
		// The wait returns the ITEMS, not a bare signal: a signal-then-read costs two
		// metered ops per batch, which §6.0's 240/min drain budget cannot absorb.
		items, hasMore, resetRequired, incarnation, err := sc.s.st.readItemsPageForIncarnation(w.rid, w.incarnation, w.cursor, defaultMailboxPageItems, mailboxPageByteBudget)
		if err != nil {
			sc.concludeWait(w, waitReplyBody{WaitID: w.id, Code: codeBadRequest})
			return
		}
		if resetRequired {
			sc.concludeWait(w, waitReplyBody{WaitID: w.id, Code: codeMailboxCursorReset})
			return
		}
		if len(items) > 0 {
			sc.concludeWait(w, waitReplyBody{WaitID: w.id, Items: items, HasMore: hasMore, Incarnation: incarnation})
			return
		}
		select {
		case <-w.wake:
			switch waitReason(w.reason.Load()) {
			case waitSuperseded:
				sc.concludeWait(w, waitReplyBody{WaitID: w.id, Code: codeDuplicateConn})
				return
			case waitCancelled:
				sc.concludeWait(w, waitReplyBody{WaitID: w.id})
				return
			}
			// waitItems: loop and re-read. A wake that finds nothing (the item was
			// acked away underneath us) simply keeps waiting.
		case <-timer.C:
			// Bounded: a clean empty page at the ceiling, never a socket held open
			// until an intermediary kills it.
			sc.concludeWait(w, waitReplyBody{WaitID: w.id, Incarnation: incarnation})
			return
		case <-sc.ctx.Done():
			// The connection is gone (close, revoke severance, server shutdown).
			return
		}
	}
}
