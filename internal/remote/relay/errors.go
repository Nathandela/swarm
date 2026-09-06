package relay

import (
	"errors"
)

// Shared semantic sentinels returned by relay-v2 consumers. Every over-limit or refusal is a
// clean error, never resource exhaustion (R-REL.8).

var (
	// ErrQuotaExceeded is a clean refusal past a rate/quota cap.
	ErrQuotaExceeded = errors.New("relay: quota exceeded")
	// ErrNotAuthorized is returned when a caller acts on a route it is not
	// paired to (R-REL.12).
	ErrNotAuthorized = errors.New("relay: not authorized for route")
	// ErrPeerCapabilityUnavailable is returned when an operation is safe only
	// while the caller's paired peer is connected through a protocol generation
	// that explicitly advertises support for it. Callers may retry after the peer
	// reconnects or is upgraded; the refused operation has made no state change.
	ErrPeerCapabilityUnavailable = errors.New("relay: active peer does not advertise required capability")
	// ErrRevoked is returned when a de-authorized relay-auth key tries to
	// authenticate (R-REL.13).
	ErrRevoked = errors.New("relay: relay-auth registration revoked")
	// ErrDuplicateConnection is returned to a connection that has been
	// superseded by a newer connection for the same routing id (takeover).
	ErrDuplicateConnection = errors.New("relay: connection superseded by a newer one")
	// ErrWaitInProgress refuses a SECOND concurrent bounded server-side wait on
	// one client. §6.0 caps pending waits per client at 1 and REFUSES the extra
	// one rather than queueing it: a queue would let a client pin unbounded
	// server-side wait state on one connection and make cancellation ambiguous.
	ErrWaitInProgress = errors.New("relay: a mailbox wait is already outstanding on this client")
	// ErrMailboxCursorResetRequired means the caller's durable mailbox cursor cannot
	// be a resume point in the mailbox the relay currently holds. This is recoverable:
	// the caller rewinds ONLY the relay storage cursor, preserving its authenticated
	// per-stream replay high-waters, and drains again from zero.
	//
	// It is distinct from a malformed cursor. A store reinitialisation or rollback can
	// leave an otherwise valid persisted cursor past the new mailbox's high-water. The
	// authenticated replay high-waters, not this relay cursor, prevent duplicate effects
	// when the caller drains again from zero.
	ErrMailboxCursorResetRequired = errors.New("relay: mailbox cursor no longer names a safe resume point; reset required")
	// ErrRendezvousFull is returned when a third party claims a rendezvous that
	// already has two participants.
	ErrRendezvousFull = errors.New("relay: rendezvous already has two participants")
	// ErrRendezvousExpired is returned when a rendezvous is claimed past its
	// hard relay-side TTL.
	ErrRendezvousExpired = errors.New("relay: rendezvous expired")
	// ErrRendezvousExists is returned when rendezvous_create targets an id that
	// already holds a live slot, so the original creator is never overwritten.
	ErrRendezvousExists = errors.New("relay: rendezvous id already in use")
	// ErrRendezvousBurned is returned when a completed (single-use) rendezvous
	// id is claimed again.
	ErrRendezvousBurned = errors.New("relay: rendezvous already used")
	// ErrConsentRetired refuses a route consent whose pairing ceremony has been
	// superseded or revoked (ADR-007 B47). It is DISTINCT from ErrNotAuthorized
	// because its remedy is: the credential is well-formed and genuinely signed by
	// the named device, and what it needs is a new pairing, not a different caller.
	ErrConsentRetired = errors.New("relay: this pairing's route consent has been retired; pair the device again")
	// ErrConsentMalformed refuses a credential that is not a consent at all.
	ErrConsentMalformed = errors.New("relay: malformed route consent")

	// ErrTimeout reports an exchange that reached its deadline with no reply. It is
	// the relay ANSWERING NOTHING -- distinct from every refusal above, which are
	// answers -- and it is what a caller sees instead of parking forever when the
	// relay completes the handshake and then goes quiet (DefaultCallTimeout).
	//
	// IT IS NOT A REFUSAL AND MUST NOT BE TREATED AS ONE. The relay writes its reply
	// AFTER it stores the item, so a timed-out append may well have committed, and
	// re-appending the IDENTICAL sealed envelope is the only safe retry. Note that the
	// gateway draws no seq consequence from the refusal sentinels either: a seq handed
	// to the appender is spent whatever the reply says (ADR-007 B127).
	ErrTimeout = errors.New("relay: the relay did not answer within the call deadline")

	// ErrConnClosed reports a connection that died underneath a caller. The underlying
	// network error is not propagated: every caller's response is the same (the
	// connection is gone), and a resilient one reconnects.
	//
	// IT IS EXPORTED SO THE PHONE CAN CLASS IT (mobile/errorclass.go). It was reachable
	// by any call racing a drop -- the ordinary end of a mobile outage -- and, being
	// unexported, matched no arm of the phone's classifier and landed in the class whose
	// remedy is "report a bug". It is the same user-visible condition as ErrTimeout and
	// must not depend on which of the two won the race.
	ErrConnClosed = errors.New("relay: connection closed")
)
