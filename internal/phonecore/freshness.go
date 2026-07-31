package phonecore

// PB-APP-11 -- staleness BY SILENCE. Section 6.0's cached-state freshness budget, which was
// specified in v2 and never built.
//
// THE ATTACK THIS ANSWERS. Every other staleness mechanism on this phone keys on a GAP, and a
// gap is observable only when a LATER seq arrives. So the declared adversary (ADR-007 D9)
// never has to forge, reorder or replay: it stops delivering the newest frames and keeps
// answering the polls, with an empty page. No gap forms, so nothing is marked stale; the poll
// SUCCEEDS, so no connection-state machinery fires; and the relay is also the source of the
// only other liveness signal the phone has, so Presence() asks the withholding party whether
// the machine is alive. The phone then renders arbitrarily old sessions and terminal grids as
// live, indefinitely (ADR-007 B121/M-1).
//
// WHY IssuedAt IS THE CLOCK, and not "time since a successful poll" as section 6.0 first
// worded it. A poll-keyed budget is armed by the party it defends against: the relay keeps
// answering, so the budget never fires. IssuedAt is AAD-covered, so the relay can only make a
// frame look OLDER by holding it and never newer -- measuring against it fails closed under
// exactly the attack, and it costs no new wire field, because every inbound frame already
// carries one.
//
// WHAT IT CANNOT DISTINGUISH, said here rather than implied. Nothing on this wire is a
// liveness beacon, so an IDLE machine and a WITHHELD one look identical from the handset. The
// verdict is therefore worded as what the phone actually knows -- it has not heard from the
// machine since a stated instant -- and never as a claim that the machine is down. A beacon
// would need an interval in section 6.0 that nobody has decided; it is a recorded residual,
// not an invention.

import "time"

// FreshnessBudget is section 6.0's "cached-state freshness before it is shown as stale",
// measured against the newest authenticated machine timestamp the phone has accepted.
//
// It is TIGHTER than InboundMaxAge (10 min) on purpose, and the two are different mechanisms:
// InboundMaxAge REFUSES a frame outright as a replay backstop, while this one accepts
// everything it is given and bounds what may be PRESENTED from it. A budget at or above
// InboundMaxAge would be unreachable -- every accepted frame is inside that window by
// construction -- which is another way of saying it would never fire.
const FreshnessBudget = 5 * time.Minute

// heardAt is the freshness coordinate one accepted frame contributes: the machine's own
// authenticated stamp, CLAMPED to the instant the phone accepted it.
//
// The clamp is the only defence against a stamp from the future, and the case is not
// adversarial -- the relay cannot forge IssuedAt -- it is a machine whose clock runs fast.
// Unclamped, one such frame buys an unbounded freshness window: the phone would go on
// presenting its caches as live long after the machine stopped speaking, which is the exact
// condition this coordinate exists to end. Clamping can only make the verdict MORE
// conservative, never less, so it fails closed in both directions of skew.
func heardAt(issuedAt int64, now time.Time) int64 {
	arrival := now.UnixMilli()
	if issuedAt > arrival {
		return arrival
	}
	return issuedAt
}

// LastHeard is the newest authenticated machine timestamp the phone has accepted, or the zero
// time when it has never heard from its machine at all.
func (c *Core) LastHeard() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.st.LastHeardAt == 0 {
		return time.Time{}
	}
	return time.UnixMilli(c.st.LastHeardAt)
}

// MachineSilentAt is PB-APP-11's verdict at now: the machine's newest word is older than
// section 6.0's budget, so nothing derived from it may be presented as live.
//
// A phone that has NEVER heard from its machine is silent, not live. That is the fail-closed
// answer and it is also the true one: a first launch that has restored caches from disk has,
// by definition, not heard anything this session, and the state clears itself the moment a
// frame lands.
func (c *Core) MachineSilentAt(now time.Time) bool {
	last := c.LastHeard()
	if last.IsZero() {
		return true
	}
	return now.Sub(last) > FreshnessBudget
}
