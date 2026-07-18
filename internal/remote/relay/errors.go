package relay

import "errors"

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
	// ErrRendezvousFull is returned when a third party claims a rendezvous that
	// already has two participants.
	ErrRendezvousFull = errors.New("relay: rendezvous already has two participants")
	// ErrRendezvousExpired is returned when a rendezvous is claimed past its
	// hard relay-side TTL.
	ErrRendezvousExpired = errors.New("relay: rendezvous expired")
	// ErrRendezvousBurned is returned when a completed (single-use) rendezvous
	// id is claimed again.
	ErrRendezvousBurned = errors.New("relay: rendezvous already used")
)

// wire error codes. The client maps a received code back to the sentinel above.
const (
	codeBadRequest     = "bad_request"
	codeQuotaExceeded  = "quota_exceeded"
	codeNotAuthorized  = "not_authorized"
	codeRevoked        = "revoked"
	codeDuplicateConn  = "duplicate_connection"
	codeRendezvousFull = "rendezvous_full"
	codeRendezvousTTL  = "rendezvous_expired"
	codeRendezvousUsed = "rendezvous_burned"
	codeAuthFailed     = "auth_failed"
	codeUnsupported    = "unsupported"
)

// codeToErr maps a wire error code to its sentinel. An unrecognised code becomes
// a generic error carrying the server message.
var codeToErr = map[string]error{
	codeQuotaExceeded:  ErrQuotaExceeded,
	codeNotAuthorized:  ErrNotAuthorized,
	codeRevoked:        ErrRevoked,
	codeDuplicateConn:  ErrDuplicateConnection,
	codeRendezvousFull: ErrRendezvousFull,
	codeRendezvousTTL:  ErrRendezvousExpired,
	codeRendezvousUsed: ErrRendezvousBurned,
}

// errorBody is the JSON shape of an r_error reply.
type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}
