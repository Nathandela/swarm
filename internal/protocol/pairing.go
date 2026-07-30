package protocol

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/remote/pairing"
)

// PairStartReq is the pairing request handlePairStart translates from the wire
// (Control.Pairing) and hands the PairingHost: the capability tier the new device
// should be granted and the rendezvous TTL.
type PairStartReq struct {
	Capability string
	TTLSeconds int
}

// PairView is the synchronous rendezvous view BeginPairing returns (the pair_start
// reply): the QR to display, the rendezvous correlation id, and the daemon-
// authoritative expiry.
type PairView struct {
	QR           string
	RendezvousID string
	ExpiresAt    *time.Time
}

// PairResult is the terminal pairing outcome the host reports via the result
// callback (exactly once). On success DeviceID/Name/Capability describe the newly
// paired device; on failure Err is set and the identity fields are empty.
type PairResult struct {
	DeviceID   string
	Name       string
	Capability string
	Err        error
}

// PairFailure is the CLOSED set of reasons a pairing ended without enrolling a device
// (ADR-007 B71(1)). Before it, every failure reached the owner as one undifferentiated
// "not paired": a declined SAS, an expired window and a spent code were the same line on
// the terminal, during the ceremony every tester performs first.
//
// It is a fixed vocabulary rather than the host's error text ON PURPOSE. The pairing path
// parses attacker-influenced bytes -- the device payload carries a phone-chosen name, and
// the transport/decode failures under it wrap remote-supplied material -- so propagating
// err.Error() would put relay-supplied text on the owner's terminal. A code carries no
// bytes from the wire, and pairingResultFromControl normalises any code it does not know,
// so the vocabulary is enforced rather than merely intended.
type PairFailure string

const (
	// PairFailNone is the absence of a failure; set iff the pairing succeeded.
	PairFailNone PairFailure = ""
	// PairFailDeclined is the operator answering "no" at the SAS gate. It is the gate
	// doing its job and the one outcome here that is not a malfunction.
	PairFailDeclined PairFailure = "declined"
	// PairFailConfirmTimeout is the SAS gate giving up unanswered.
	PairFailConfirmTimeout PairFailure = "confirm_timeout"
	// PairFailWindowClosed is the pairing window expiring mid-handshake.
	PairFailWindowClosed PairFailure = "window_closed"
	// PairFailSessionClosed is the owner's own session ending before the outcome. It is
	// delivered by the client, never by the daemon.
	PairFailSessionClosed PairFailure = "session_closed"
	// PairFailConnectionLost is the daemon connection dropping mid-pairing -- the one
	// cause that points at the daemon rather than at the ceremony.
	PairFailConnectionLost PairFailure = "connection_lost"
	// PairFailRateLimited is the machine refusing further attempts for rate (R-PAIR.8).
	PairFailRateLimited PairFailure = "rate_limited"
	// PairFailCodeSpent is a single-use pairing secret offered twice (R-PAIR.1).
	PairFailCodeSpent PairFailure = "code_spent"
	// PairFailHeadless is the refusal to pair with no local console (R-PAIR.9).
	PairFailHeadless PairFailure = "headless"
	// PairFailNoConsent is the device never releasing its relay-route consent (B38).
	PairFailNoConsent PairFailure = "no_consent"
	// PairFailAcceptUnacknowledged is the acceptance leaving this machine and the device
	// never acknowledging it (PB-PAIR-4). It is the ONE cause whose orientation the owner
	// cannot see from the desktop: the handset may well read "paired", because the device
	// pins on the acceptance it received, while this machine deliberately claims NOTHING.
	// It therefore needs its own cause and its own line -- classified as "internal" it
	// produced a terminal saying no cause was reported next to a phone saying it worked.
	PairFailAcceptUnacknowledged PairFailure = "accept_unacknowledged"
	// PairFailInternal is a failure the daemon could not attribute. It is also what an
	// unrecognised wire code normalises to, so it means "look at the daemon log", never
	// "nothing went wrong".
	PairFailInternal PairFailure = "internal"
)

// pairFailures is the recognised vocabulary, and the ONLY thing a wire code may decode
// to. Anything else is normalised to PairFailInternal.
var pairFailures = map[PairFailure]struct{}{
	PairFailDeclined: {}, PairFailConfirmTimeout: {}, PairFailWindowClosed: {},
	PairFailSessionClosed: {}, PairFailConnectionLost: {}, PairFailRateLimited: {},
	PairFailCodeSpent: {}, PairFailHeadless: {}, PairFailNoConsent: {},
	PairFailAcceptUnacknowledged: {}, PairFailInternal: {},
}

// PairFailures returns the recognised cause vocabulary, sorted, so a presentation layer can
// assert it renders EVERY cause rather than the ones its author happened to think of.
//
// It exists because the vocabulary and the operator-facing lines were two hand-maintained
// lists with nothing tying them together: PB-PAIR-4's cause reached a terminal that had no
// line for it and fell through to "the daemon did not report a cause", which is B71(1)'s
// complaint verbatim, one cause later. A quantifier over this slice is what makes a new cause
// fail loudly at the presentation layer instead of arriving silently as "internal".
func PairFailures() []PairFailure {
	out := make([]PairFailure, 0, len(pairFailures))
	for f := range pairFailures {
		out = append(out, f)
	}
	slices.Sort(out)
	return out
}

// classifyPairFailure maps a host's terminal error to the cause the owner is told.
//
// It matches STRUCTURALLY, with errors.Is against sentinels that already exist on this
// path, and never on an error's prose: a message is not an API, and a classifier that
// read one would silently re-collapse the moment somebody reworded a wrap.
//
// An error it cannot attribute becomes PairFailInternal rather than a guess. Three
// failures reach here unattributable today because internal/skeleton/pairing.go builds
// them with errors.New and no sentinel -- the epoch-rotation abort, the "a different
// device is already paired" refusal from the registry, and the grant-write rollback. Each
// needs one exported sentinel at its origin to become distinguishable; the vocabulary
// above deliberately does NOT invent codes for causes nothing can yet produce.
func classifyPairFailure(err error) PairFailure {
	switch {
	case err == nil:
		return PairFailNone
	case errors.Is(err, pairing.ErrConfirmDeclined):
		return PairFailDeclined
	case errors.Is(err, pairing.ErrConfirmTimeout):
		return PairFailConfirmTimeout
	case errors.Is(err, context.DeadlineExceeded):
		return PairFailWindowClosed
	case errors.Is(err, context.Canceled):
		return PairFailConnectionLost
	case errors.Is(err, pairing.ErrRateLimited):
		return PairFailRateLimited
	case errors.Is(err, pairing.ErrSecretConsumed):
		return PairFailCodeSpent
	case errors.Is(err, pairing.ErrHeadlessRefused):
		return PairFailHeadless
	case errors.Is(err, pairing.ErrNoConsent):
		return PairFailNoConsent
	case errors.Is(err, pairing.ErrAcceptUnacknowledged):
		return PairFailAcceptUnacknowledged
	default:
		return PairFailInternal
	}
}

// PairingHost is the OPTIONAL interface the assembled daemon implements so an
// owner-tier Server can host a pairing. BeginPairing creates the rendezvous + QR
// SYNCHRONOUSLY (returned in PairView) and runs the handshake in a background
// goroutine: it calls confirm(sas, deviceName) at the anti-MITM SAS gate (blocking
// until the human decides) and result(...) EXACTLY ONCE at the terminal outcome.
// ctx cancellation (connection drop / TTL) MUST make an in-flight confirm return a
// NON-NIL error — fail closed, i.e. decline.
type PairingHost interface {
	BeginPairing(ctx context.Context, req PairStartReq,
		confirm func(sas []string, deviceName string) (bool, error),
		result func(PairResult)) (PairView, error)
}

// pairSession is one connection's in-flight pairing state. confirm carries the
// human's SAS-gate decision from handlePairConfirm to the blocked confirm closure
// (buffered cap 1, non-blocking send). cancel ends the connection-derived ctx at
// the terminal outcome. rvz (the rendezvous id, known only after BeginPairing
// returns the PairView) is guarded by mu because the confirm closure — running in
// the host's background goroutine — may read it concurrently with handlePairStart
// setting it.
type pairSession struct {
	confirm chan bool
	cancel  context.CancelFunc

	mu  sync.Mutex
	rvz string
}
