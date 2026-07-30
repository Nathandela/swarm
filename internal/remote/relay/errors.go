package relay

import (
	"errors"
	"fmt"
)

// Sentinel errors returned by Client/Conn operations. Each maps to a stable wire
// error code so a caller can errors.Is against it after a round-trip. Every
// over-limit or refusal is a CLEAN error, never resource exhaustion (R-REL.8).
var (
	// ErrQuotaExceeded is a clean refusal past a rate/quota cap.
	ErrQuotaExceeded = errors.New("relay: quota exceeded")
	// ErrNotAuthorized is returned when a caller acts on a route it is not
	// paired to (R-REL.12).
	ErrNotAuthorized = errors.New("relay: not authorized for route")
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
	// AFTER it stores the item, so a timed-out append may well have committed:
	// remotegw.ClassifyAppend leaves it in AppendUnknown, where re-appending the
	// IDENTICAL sealed envelope is the only safe retry.
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

// wire error codes. The client maps a received code back to the sentinel above.
const (
	codeBadRequest       = "bad_request"
	codeQuotaExceeded    = "quota_exceeded"
	codeNotAuthorized    = "not_authorized"
	codeRevoked          = "revoked"
	codeDuplicateConn    = "duplicate_connection"
	codeWaitInProgress   = "wait_in_progress"
	codeRendezvousFull   = "rendezvous_full"
	codeRendezvousTTL    = "rendezvous_expired"
	codeRendezvousUsed   = "rendezvous_burned"
	codeRendezvousExists = "rendezvous_exists"
	codeAuthFailed       = "auth_failed"
	codeUnsupported      = "unsupported"
	codeConsentRetired   = "consent_retired"
)

// codeToErr maps a wire error code to its sentinel. An unrecognised code becomes
// a generic error carrying the server message.
var codeToErr = map[string]error{
	codeQuotaExceeded:    ErrQuotaExceeded,
	codeNotAuthorized:    ErrNotAuthorized,
	codeRevoked:          ErrRevoked,
	codeDuplicateConn:    ErrDuplicateConnection,
	codeWaitInProgress:   ErrWaitInProgress,
	codeRendezvousFull:   ErrRendezvousFull,
	codeRendezvousTTL:    ErrRendezvousExpired,
	codeRendezvousUsed:   ErrRendezvousBurned,
	codeRendezvousExists: ErrRendezvousExists,
	codeConsentRetired:   ErrConsentRetired,
}

// errorBody is the JSON shape of an r_error reply.
type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// errForCode maps a wire error code to its sentinel, or to a generic error
// naming the code. A MsgWaitReply carries its refusal as a code rather than as
// an r_error frame (it must reach the parked waiter, not the request queue), so
// both reply shapes resolve to the same sentinel through here.
func errForCode(code string) error {
	if e, ok := codeToErr[code]; ok {
		return e
	}
	return fmt.Errorf("relay: %s", code)
}
