package transport

// PB-INPUT-4 -- "retry policy is keyed on stable server error codes, never blind resend".
//
// The stable codes are the relay's (relay/errors.go maps each wire code to a sentinel), and
// internal/remote/relay is outside phonecore's bound dependency closure (PB-BIND-0). This
// package already depends on relay and is the layer that receives these errors, so the
// mapping belongs here; phonecore would have to re-derive the codes from message text, which
// is exactly what "keyed on stable server error codes" forbids.
//
// THE THREE-WAY SPLIT is SendOp's own (session.go, PB-GW-7): the relay COMMITS an item
// before it replies, so a lost reply is "delivery unknown", not "not delivered". A
// definitive PRE-COMMIT refusal may be re-sent; a delivery-unknown failure may only be
// re-sent as the EXACT SAME sealed envelope, which the receiver stale-drops for free -- a
// fresh seal would put a second envelope at a second seq for one user action, and the
// phone's durable send-seq cannot then reconcile it.

import (
	"context"
	"errors"
	"net"
	"os"

	"github.com/Nathandela/swarm/internal/remote/relay"
)

// RetryPolicy is what a caller may do with a failed send.
type RetryPolicy int

const (
	// RetryNone surfaces the failure. It is the ZERO VALUE deliberately: an unset policy,
	// a map miss or a new struct field must never mean "resend".
	RetryNone RetryPolicy = iota
	// RetryResend follows a DEFINITIVE PRE-COMMIT refusal: the relay stored nothing, so the
	// identical op may be re-sent after a backoff.
	RetryResend
	// RetrySameEnvelope follows a DELIVERY-UNKNOWN failure: only the identical sealed bytes
	// may go again, never a fresh seal.
	RetrySameEnvelope
	// RetryReauthorize means the device's standing with the relay changed; resending
	// changes nothing until the user re-pairs.
	RetryReauthorize
)

// RetryFor classifies one send failure. Everything it cannot classify -- a future wire
// code, a bad_request, an unsupported op -- is RetryNone: "never blind resend" is a
// statement about the DEFAULT, so each retryable class is named here explicitly and
// everything else is surfaced.
func RetryFor(err error) RetryPolicy {
	switch {
	case err == nil:
		return RetryNone

	// LIVE-ONLY WINS OVER EVERYTHING (ADR-007 D7). SendLive wraps its cause as
	// fmt.Errorf("%w: %w", ErrNotDelivered, err), so a keystroke refused for quota carries
	// BOTH sentinels; checking the relay codes first would answer RetryResend and re-send a
	// keystroke -- the same hazard the queue is structured to prevent, reached instead
	// through the retry table.
	case errors.Is(err, ErrNotDelivered):
		return RetryNone

	case errors.Is(err, context.Canceled),
		// The caller was REFUSED deliberately rather than silently evicted; a background
		// resend would hide the refusal that was the whole point.
		errors.Is(err, ErrOpQueueFull),
		errors.Is(err, ErrClosed),
		// A hostile relay page that claims more without advancing: retrying it is the
		// infinite scan it was refused for.
		errors.Is(err, ErrStuckPage),
		// A rendezvous is single-use and TTL-bounded; a resend can only fail again.
		errors.Is(err, relay.ErrRendezvousExpired),
		errors.Is(err, relay.ErrRendezvousBurned):
		return RetryNone

	case errors.Is(err, relay.ErrNotAuthorized), errors.Is(err, relay.ErrRevoked):
		return RetryReauthorize

	case errors.Is(err, relay.ErrQuotaExceeded),
		errors.Is(err, relay.ErrWaitInProgress),
		errors.Is(err, relay.ErrDuplicateConnection):
		return RetryResend

	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded), isTimeout(err):
		// Sent, and the answer never came: the item may well have been committed.
		return RetrySameEnvelope
	}
	return RetryNone
}

// isTimeout reports a net.Error that timed out -- the same delivery-unknown class as a
// deadline, surfaced by the socket instead of the context.
func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
