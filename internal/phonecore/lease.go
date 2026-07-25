package phonecore

// PB-INPUT-2 / PB-INPUT-3 -- the phone's view of the control lease.
//
// Before this the phone had NO notion of a lease: SendInput sealed and appended a keystroke
// with no check of any kind, so "input is suppressed until a new lease is visibly
// confirmed" had nothing to suppress against. PB-SYNC-7 supplied the CONFIRMATION half
// (remotegw seals an OpLease carrying the daemon-granted generation, and an OpDetach when
// the lease dies); this is the consumer.
//
// NOTHING HERE IS PERSISTED. A lease IS a live daemon connection, so a lease restored from
// disk is by construction a lease the machine does not hold -- which is precisely the
// "assume the lease" failure PB-INPUT-2 forbids. Process death therefore severs it by
// having nowhere to survive.

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// The daemon control ops the lease lifecycle rides. They are written as literals because
// PB-BIND-0 keeps internal/protocol out of phonecore's bound closure (deps_allowlist.txt);
// the values are pinned against protocol.OpLease / OpDetach / OpError by the S11 tests,
// which are test-only and may import it.
const (
	opLease  = "lease"
	opDetach = "detach"
	opError  = "error"
)

var (
	// ErrNoLease refuses input for a session with no CONFIRMED lease generation: never
	// granted, refused, or severed since. The UX is "take control again".
	ErrNoLease = errors.New("phonecore: no confirmed control lease for this session")
	// ErrLeaseExpired refuses input past the lease horizon. It is DISTINCT from ErrNoLease
	// because the UX differs (re-authorize vs. take control again), and because an expired
	// lease that reported nothing would lose the user's keystrokes silently (PB-INPUT-3).
	ErrLeaseExpired = errors.New("phonecore: the control lease has expired")
)

const (
	// CommandTTL is how long an ordinary signed command stays valid (§6.0, PB-TIME-1).
	CommandTTL = 1 * time.Minute
	// TakeControlTTL is §6.0's stated exception. The daemon's lease deadline is the
	// EARLIEST of the device-signed ExpiresAt, now+TTLSeconds and a 30-minute cap, so a
	// blanket 1-minute signed horizon would make the SIGNATURE the thing that ends a typing
	// session -- colliding with PB-INPUT-5's >= 60 s sustained-typing criterion and §6.0's
	// 60 s biometric freshness at once. 15 minutes clears both walls and stays under the cap.
	TakeControlTTL = 15 * time.Minute
)

// CommandTTLFor is the signed-ExpiresAt horizon for one action class (§6.0). Only
// take_control gets the exception; everything else -- kill, delete, launch, device_revoke,
// terminal_watch -- stays at the 1-minute command TTL, which bounds how long a captured
// envelope is replayable.
func CommandTTLFor(action string) time.Duration {
	if action == schema.ActionTakeControl {
		return TakeControlTTL
	}
	return CommandTTL
}

// Lease is one confirmed control lease. Generation is the DAEMON's, never the phone's:
// PB-INPUT-2 gates keystrokes on the generation the machine granted.
type Lease struct {
	Session    string
	Generation uint64
	ExpiresAt  time.Time
}

// LeaseState tracks, per session, whether the machine has confirmed a lease the phone may
// type on. Safe for concurrent use: the inbound drain feeds it while the UI thread types.
type LeaseState struct {
	mu       sync.Mutex
	sessions map[string]*leaseEntry
}

// leaseEntry is one session's lease lifecycle.
type leaseEntry struct {
	op     string    // the take_control operation id the phone last authored
	signed time.Time // the horizon the phone SIGNED for it (an upper bound on the truth)

	live      bool
	gen       uint64
	expiresAt time.Time // zero => no horizon is known; the severance notice is the authority

	dead   uint64 // highest generation known to be severed
	reason string // why there is no lease, for PB-INPUT-2's "visibly"
}

// NewLeaseState returns an empty state: no session holds a lease until the machine says so.
func NewLeaseState() *LeaseState {
	return &LeaseState{sessions: map[string]*leaseEntry{}}
}

// Requested records that the phone AUTHORED a take_control, with the horizon it signed. It
// is explicitly NOT a lease -- Require still refuses until the confirmation lands -- but it
// is what lets a confirmation that carries no expiry of its own fall back to the phone's
// own horizon (see Apply).
func (l *LeaseState) Requested(session, operationID string, expiresAt time.Time) {
	if session == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entry(session)
	e.op, e.signed = operationID, expiresAt
}

// Apply folds one machine-sealed command reply into the lease lifecycle. It is fed from the
// AUTHENTICATED inbound path (MailboxRouter), so a relay cannot forge a grant or a
// severance.
//
//   - OpLease with a NON-ZERO generation confirms a lease. Generation 0 names a lease the
//     daemon does not hold (LeaseManager.Generation reports 0 for a session holding no
//     conn), so it never opens the gate.
//   - OpDetach severs it. The notice carries the DEAD generation, so a supersede's late
//     notice cannot kill the replacement lease the daemon is holding open.
//   - OpError answering the phone's own take_control is a refusal: the gate stays shut and
//     the reason is kept, because a refused lease that reported nothing is indistinguishable
//     from a slow one.
//
// A generation at or below one already severed is NEVER reconfirmed: the relay may reorder,
// so a confirmation sealed before the severance can arrive after it, and re-opening the gate
// on it would let a keystroke ride a lease the daemon released.
func (l *LeaseState) Apply(ctrl schema.Control) {
	if ctrl.SessionID == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entry(ctrl.SessionID)

	switch ctrl.Op {
	case opLease:
		if ctrl.Generation == 0 || ctrl.Generation <= e.dead {
			return
		}
		e.live, e.gen, e.reason = true, ctrl.Generation, ""
		switch {
		case ctrl.ExpiresAt != nil:
			// The machine's value is the authority: it may have clamped the lease shorter
			// than the phone asked for.
			e.expiresAt = *ctrl.ExpiresAt
		case ctrl.OperationID != "" && ctrl.OperationID == e.op:
			e.expiresAt = e.signed
		default:
			e.expiresAt = time.Time{}
		}
	case opDetach:
		if ctrl.Generation != 0 && e.live && ctrl.Generation < e.gen {
			return // a superseded generation's late notice; the live lease outlives it
		}
		if ctrl.Generation > e.dead {
			e.dead = ctrl.Generation
		}
		l.severLocked(e, reasonOr(ctrl.Error, "the control lease ended"))
	case opError:
		if ctrl.OperationID == "" || ctrl.OperationID != e.op {
			return // an outcome for some other op, not a refusal of this lease
		}
		e.reason = reasonOr(ctrl.Error, "take_control was refused")
	}
}

// Sever ends a session's lease from the phone's own side: a transport loss, a release, app
// backgrounding, a biometric-freshness lapse. The severed generation is remembered so a
// confirmation for it cannot resurrect the lease.
func (l *LeaseState) Sever(session, reason string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.severLocked(l.entry(session), reason)
}

// SeverAll ends every session's lease. It is the whole-device boundary: the relay
// connection dropped (so the gateway can seal no notice), or the app stopped being allowed
// to type at all.
func (l *LeaseState) SeverAll(reason string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.sessions {
		l.severLocked(e, reason)
	}
}

// Lease is the session's confirmed lease, if it holds one.
func (l *LeaseState) Lease(session string) (Lease, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.sessions[session]
	if !ok || !e.live {
		return Lease{}, false
	}
	return Lease{Session: session, Generation: e.gen, ExpiresAt: e.expiresAt}, true
}

// Require is the gate every keystroke, resize and Ctrl-C passes: nil only while a confirmed
// lease is live and inside its horizon. Exactly AT the horizon is still live, matching the
// daemon's own deadline comparison -- a phone that gave up a millisecond early would refuse
// a keystroke the machine would have accepted.
func (l *LeaseState) Require(session string, now time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.sessions[session]
	if !ok {
		return ErrNoLease
	}
	if !e.live {
		return noLease(e.reason)
	}
	if !e.expiresAt.IsZero() && now.After(e.expiresAt) {
		return fmt.Errorf("%w at %s", ErrLeaseExpired, e.expiresAt.Format(time.RFC3339))
	}
	return nil
}

// Reason is why the session holds no lease, for the screen. PB-INPUT-2 requires the
// suppression be VISIBLE, and an invisible suppression is a dead keyboard.
func (l *LeaseState) Reason(session string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, ok := l.sessions[session]; ok {
		return e.reason
	}
	return ""
}

// entry returns the session's lifecycle record, creating it on first use. Caller holds l.mu.
func (l *LeaseState) entry(session string) *leaseEntry {
	e, ok := l.sessions[session]
	if !ok {
		e = &leaseEntry{}
		l.sessions[session] = e
	}
	return e
}

// severLocked drops a live lease and remembers its generation as dead.
func (l *LeaseState) severLocked(e *leaseEntry, reason string) {
	if e.live && e.gen > e.dead {
		e.dead = e.gen
	}
	e.live = false
	e.reason = reasonOr(reason, "the control lease ended")
}

func noLease(reason string) error {
	if reason == "" {
		return ErrNoLease
	}
	return fmt.Errorf("%w: %s", ErrNoLease, reason)
}

func reasonOr(reason, fallback string) string {
	if reason == "" {
		return fallback
	}
	return reason
}
