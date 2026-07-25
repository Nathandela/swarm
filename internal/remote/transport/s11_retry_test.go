// Slice S11 -- FAILING-FIRST (TDD RED, GG-5) tests for PB-INPUT-4: "retry policy is keyed
// on stable server error codes, never blind resend", with the acceptance criterion "test
// maps each error class to its policy".
//
// THE CONTRACT these tests freeze (undefined symbols -> compile-level RED for the
// transport test binary):
//
//	type RetryPolicy int
//	const (
//		RetryNone RetryPolicy = iota  // do not resend; surface it
//		RetryResend                   // DEFINITIVE PRE-COMMIT refusal: the relay stored
//		                              // nothing, so the op may be re-sent after a backoff
//		RetrySameEnvelope             // delivery UNKNOWN: only the IDENTICAL sealed bytes
//		                              // may be re-sent, never a fresh seal
//		RetryReauthorize              // the device's standing with the relay changed
//	)
//	func RetryFor(err error) RetryPolicy
//
// WHY THIS LIVES IN transport AND NOT IN phonecore. The stable codes are the relay's
// (relay/errors.go:44-70 maps each wire code to a sentinel), and internal/remote/relay is
// outside phonecore's bound dependency closure (PB-BIND-0, deps_allowlist.txt). transport
// already depends on relay and is the layer that receives these errors, so the mapping
// belongs here; phonecore would have to re-derive the codes from message text, which is
// exactly what "keyed on stable server error codes" forbids.
//
// WHY THE DEFAULT IS RetryNone. "Never blind resend" is a statement about the DEFAULT: an
// error the client does not recognise -- a future wire code, a bad_request, an unsupported
// op -- must not be retried on the theory that it might be transient. Each retryable class
// is named explicitly below; everything else is surfaced.
//
// THE THREE-WAY SPLIT is not invented here. SendOp's own doc records it (session.go:378-385,
// PB-GW-7): "the relay commits the item BEFORE it replies, so a lost reply is 'delivery
// unknown', not 'not delivered'. The correct answer is a definitive pre-commit refusal is
// retried, a delivery-unknown failure is retried only as the exact same sealed envelope (a
// duplicate the receiver stale-drops for free) or abandoned". Re-sealing after a lost reply
// puts a SECOND envelope at a new seq for one user action, which the phone's durable
// send-seq then cannot reconcile.
//
// This file contains NO implementation.
package transport_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remote/transport"
)

// s11TimeoutErr is a net.Error that reports Timeout() -- what a stalled relay round trip
// surfaces as once the request deadline fires.
type s11TimeoutErr struct{}

func (s11TimeoutErr) Error() string   { return "i/o timeout" }
func (s11TimeoutErr) Timeout() bool   { return true }
func (s11TimeoutErr) Temporary() bool { return true }

var _ net.Error = s11TimeoutErr{}

// TestS11Retry_EveryErrorClassMapsToItsPolicy is PB-INPUT-4's acceptance criterion. Each
// row names the class, the policy and WHY -- because the wrong answer for several of these
// is a silent duplicate of something the user asked for once.
func TestS11Retry_EveryErrorClassMapsToItsPolicy(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want transport.RetryPolicy
		why  string
	}{
		{
			"nil", nil, transport.RetryNone,
			"there is nothing to retry",
		},
		{
			"relay quota exceeded", relay.ErrQuotaExceeded, transport.RetryResend,
			"a clean pre-commit refusal: the relay stored NOTHING, so the identical op may be re-sent once the tumbling window turns over",
		},
		{
			"relay wait already in progress", relay.ErrWaitInProgress, transport.RetryResend,
			"§6.0 caps pending waits at 1 and REFUSES the second rather than queueing it; nothing was stored",
		},
		{
			"relay connection superseded", relay.ErrDuplicateConnection, transport.RetryResend,
			"the connection was taken over before the request landed; the reconnect path re-sends it",
		},
		{
			"relay not authorized", relay.ErrNotAuthorized, transport.RetryReauthorize,
			"the route is not one this device is paired to; resending changes nothing, and the user must re-pair",
		},
		{
			"relay auth revoked", relay.ErrRevoked, transport.RetryReauthorize,
			"the relay-auth registration was revoked; every resend is refused identically",
		},
		{
			"rendezvous expired", relay.ErrRendezvousExpired, transport.RetryNone,
			"a rendezvous is single-use and TTL-bounded; a resend can only fail again",
		},
		{
			"rendezvous burned", relay.ErrRendezvousBurned, transport.RetryNone,
			"same: single-use",
		},
		{
			"live frame not delivered", transport.ErrNotDelivered, transport.RetryNone,
			"ADR-007 D7: input and resize are LIVE-ONLY. A keystroke re-sent after the link returns lands against a terminal state the user has since changed",
		},
		{
			"op queue full", transport.ErrOpQueueFull, transport.RetryNone,
			"the caller was REFUSED, deliberately rather than silently evicted; a background resend would hide the refusal that was the whole point",
		},
		{
			"session closed", transport.ErrClosed, transport.RetryNone,
			"terminal",
		},
		{
			"stuck relay page", transport.ErrStuckPage, transport.RetryNone,
			"a hostile relay page that claims more without advancing; retrying it is the infinite scan it was refused for",
		},
		{
			"context cancelled", context.Canceled, transport.RetryNone,
			"the caller gave up; resending on their behalf is not the caller's decision",
		},
		{
			"context deadline exceeded", context.DeadlineExceeded, transport.RetrySameEnvelope,
			"delivery UNKNOWN: the relay commits the item BEFORE it replies, so a lost reply may name an op that landed. Only the IDENTICAL sealed envelope may go again -- the receiver stale-drops the duplicate for free, while a fresh seal puts a second envelope at a second seq",
		},
		{
			"i/o deadline exceeded", os.ErrDeadlineExceeded, transport.RetrySameEnvelope,
			"same class, surfaced by the socket instead of the context",
		},
		{
			"net timeout", s11TimeoutErr{}, transport.RetrySameEnvelope,
			"same class again: a request that was sent and whose answer never came",
		},
		{
			"an unrecognised relay code", errors.New("relay: some_future_code"), transport.RetryNone,
			"PB-INPUT-4's whole clause: an error the client cannot classify is SURFACED, never resent on the theory that it might be transient",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := transport.RetryFor(tc.err); got != tc.want {
				t.Fatalf("RetryFor(%v) = %v, want %v -- %s", tc.err, got, tc.want, tc.why)
			}
		})
	}
}

// TestS11Retry_LiveOnlyWinsOverTheUnderlyingRelayCode is the precedence assertion, and it
// is the one a natural implementation gets wrong. SendLive wraps its failure as
// fmt.Errorf("%w: %w", ErrNotDelivered, err) (session.go:366), so a keystroke refused for
// quota carries BOTH ErrNotDelivered and relay.ErrQuotaExceeded. A mapping that checked
// the relay sentinels first would answer RetryResend and re-send a keystroke -- the exact
// hazard ADR-007 D7 is structural about, arrived at through the retry table rather than
// through the queue.
func TestS11Retry_LiveOnlyWinsOverTheUnderlyingRelayCode(t *testing.T) {
	underlying := []error{
		relay.ErrQuotaExceeded,
		relay.ErrDuplicateConnection,
		relay.ErrWaitInProgress,
		context.DeadlineExceeded,
	}
	for _, u := range underlying {
		t.Run(u.Error(), func(t *testing.T) {
			// NON-VACUITY: on its own each of these maps to something OTHER than
			// RetryNone, so a blanket-RetryNone implementation cannot pass this test.
			if bare := transport.RetryFor(u); bare == transport.RetryNone {
				t.Fatalf("RetryFor(%v) = RetryNone on its own; the wrapping assertion below then proves nothing", u)
			}
			// The exact shape SendLive produces.
			err := fmt.Errorf("%w: %w", transport.ErrNotDelivered, u)
			if got := transport.RetryFor(err); got != transport.RetryNone {
				t.Fatalf("RetryFor(SendLive failure wrapping %v) = %v, want RetryNone -- a live-only frame is never re-sent whatever refused it (ADR-007 D7); this is the shape session.go:366 actually returns", u, got)
			}
		})
	}
}

// TestS11Retry_IsKeyedOnTheSentinelNotOnTheMessage is PB-INPUT-4's "keyed on stable server
// error codes". The two halves are a wrapped sentinel (which must still map) and a
// look-alike carrying the identical text but no sentinel (which must not).
//
// The second half is the mutation: a strings.Contains implementation passes every other
// assertion in this file and then classifies an arbitrary error -- including one an
// untrusted relay could put in a message field -- as a retryable quota refusal.
func TestS11Retry_IsKeyedOnTheSentinelNotOnTheMessage(t *testing.T) {
	wrapped := fmt.Errorf("append reply: %w", fmt.Errorf("mailbox_append: %w", relay.ErrQuotaExceeded))
	if got := transport.RetryFor(wrapped); got != transport.RetryResend {
		t.Fatalf("RetryFor(a doubly-wrapped ErrQuotaExceeded) = %v, want RetryResend -- the mapping must use errors.Is, since every real error reaches a caller wrapped", got)
	}

	lookalike := errors.New(relay.ErrQuotaExceeded.Error())
	if got := transport.RetryFor(lookalike); got != transport.RetryNone {
		t.Fatalf("RetryFor(an error whose TEXT matches %q but which is not the sentinel) = %v, want RetryNone -- classifying on message text is not keying on a stable code, and the text can come from the untrusted relay", relay.ErrQuotaExceeded.Error(), got)
	}
}

// TestS11Retry_ThePoliciesAreDistinct. Four names that all evaluate to the same value
// would satisfy every table row above. iota does this correctly by construction; the test
// exists because a later edit that pins two of them to one value would otherwise be
// invisible.
func TestS11Retry_ThePoliciesAreDistinct(t *testing.T) {
	all := map[transport.RetryPolicy]string{
		transport.RetryNone:         "RetryNone",
		transport.RetryResend:       "RetryResend",
		transport.RetrySameEnvelope: "RetrySameEnvelope",
		transport.RetryReauthorize:  "RetryReauthorize",
	}
	if len(all) != 4 {
		t.Fatalf("the four retry policies collapse to %d distinct values; the table-driven mapping above then proves nothing", len(all))
	}
	// The zero value must be the conservative one: a struct field or a map miss that
	// defaults to a RESEND is a blind resend arrived at by accident.
	var zero transport.RetryPolicy
	if zero != transport.RetryNone {
		t.Fatalf("the zero RetryPolicy is %v, not RetryNone -- an unset policy must never mean 'resend'", zero)
	}
}
