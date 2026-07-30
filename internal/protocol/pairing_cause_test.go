package protocol

// FAILING-FIRST tests for ADR-007 B71(1): a pairing failure is CAUSELESS.
//
// server.go's result closure DISCARDS PairResult.Err and pushes a nil Pairing, and
// PairingResult has no field to carry a reason — so a deadline expiry, a declined SAS,
// a spent QR and a relay-consent failure are one indistinguishable "not paired" at the
// owner's terminal. Pairing is the first thing every tester does, and this is what they
// have to diagnose it with.
//
// WHAT CROSSES THE WIRE, and why it is not the error string. The pairing path parses
// attacker-influenced bytes (the device payload carries a phone-chosen DeviceName, and
// the transport/decode errors below it wrap remote-supplied material), so propagating
// err.Error() to the operator's terminal would put relay-supplied text on a trusted
// surface. What crosses instead is a CLOSED set of cause codes: a fixed vocabulary the
// daemon chooses from, carrying no bytes from the wire.
//
// CLASSIFICATION IS STRUCTURAL, NEVER BY PROSE. Every row below is an errors.Is match
// against a sentinel that already exists on this path — internal/remote/pairing's
// exported errors and context's — asserted through a WRAPPED error so a classifier that
// compared with == or matched a message would fail. This package has the same rule
// elsewhere (supervise's TestHostStop_ClassifiesNothingByMessage); an error's prose is
// not an API.
//
// INTENDED PRODUCTION SURFACE (RED — none of it exists yet):
//
//	// internal/protocol/pairing.go
//	type PairFailure string
//	const (
//		PairFailNone, PairFailDeclined, PairFailConfirmTimeout, PairFailWindowClosed,
//		PairFailSessionClosed, PairFailConnectionLost, PairFailRateLimited,
//		PairFailCodeSpent, PairFailHeadless, PairFailNoConsent, PairFailInternal PairFailure = ...
//	)
//	func classifyPairFailure(err error) PairFailure
//
//	// internal/protocol/schema: PairingControl gains Failure string `json:"failure,omitempty"`
//	// internal/protocol/client.go: PairingResult gains Failure PairFailure

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/pairing"
)

// pairFailureCases is one row per SITUATION an owner can actually land in, paired with
// the error that situation really produces on this path. Two rows may never share a
// cause: that is the whole complaint in B71(1), and the assertion that catches a
// regression which collapses them.
var pairFailureCases = []struct {
	situation string
	err       error
	want      PairFailure
}{
	{"the operator declined at the desktop SAS gate", pairing.ErrConfirmDeclined, PairFailDeclined},
	{"nobody answered the SAS gate before it gave up", pairing.ErrConfirmTimeout, PairFailConfirmTimeout},
	{"the pairing window closed mid-handshake", context.DeadlineExceeded, PairFailWindowClosed},
	{"the owner connection dropped mid-handshake", context.Canceled, PairFailConnectionLost},
	{"the machine is refusing attempts for rate", pairing.ErrRateLimited, PairFailRateLimited},
	{"the QR had already been used once", pairing.ErrSecretConsumed, PairFailCodeSpent},
	{"there is no local console to confirm at", pairing.ErrHeadlessRefused, PairFailHeadless},
	{"the phone never released its relay-route consent", pairing.ErrNoConsent, PairFailNoConsent},
	// PB-PAIR-4's own failure. It classified as PairFailInternal, so the owner was told the
	// daemon reported no cause -- while the handset, which pins on the acceptance it did
	// receive, may be showing "paired". The one orientation the desktop cannot infer was the
	// one with no words for it.
	{"the device never acknowledged the acceptance", pairing.ErrAcceptUnacknowledged, PairFailAcceptUnacknowledged},
	{"the daemon failed for a reason it could not attribute", errors.New("some unclassified failure"), PairFailInternal},
}

// TestClassifyPairFailure_EverySituationKeepsItsOwnCause is the core of B71(1). Each
// situation must classify to its OWN cause, through a WRAPPED error, and no two
// situations may land on the same one.
func TestClassifyPairFailure_EverySituationKeepsItsOwnCause(t *testing.T) {
	seen := map[PairFailure]string{}
	for _, tc := range pairFailureCases {
		// Wrapped, not bare: the production classifier must use errors.Is. A == compare
		// or a message match passes the bare case and fails here, which is the point.
		wrapped := fmt.Errorf("pairing: begin handshake: %w", tc.err)
		if got := classifyPairFailure(wrapped); got != tc.want {
			t.Errorf("classifyPairFailure(wrapped %v) = %q, want %q -- %s",
				tc.err, got, tc.want, tc.situation)
		}
		if got := classifyPairFailure(tc.err); got != tc.want {
			t.Errorf("classifyPairFailure(%v) = %q, want %q -- %s", tc.err, got, tc.want, tc.situation)
		}
		if prev, dup := seen[tc.want]; dup {
			t.Errorf("cause %q is shared by two situations -- %q and %q; the owner cannot tell "+
				"them apart, which is B71(1) all over again", tc.want, prev, tc.situation)
		}
		seen[tc.want] = tc.situation
	}
	// A success carries no cause at all, so "did it fail" stays one comparison.
	if got := classifyPairFailure(nil); got != PairFailNone {
		t.Errorf("classifyPairFailure(nil) = %q, want %q (a success has no cause)", got, PairFailNone)
	}
}

// causePairingHost is a PairingHost whose handshake fails IMMEDIATELY with a chosen
// error — the shape of every pre-SAS-gate refusal (rate limit, spent QR, headless) and
// enough to drive the cause down the wire without a confirm round-trip.
type causePairingHost struct {
	*stubDaemon
	view PairView
	fail error
}

func newCausePairingHost(fail error) *causePairingHost {
	return &causePairingHost{
		stubDaemon: newStubDaemon(),
		view:       PairView{QR: "otpauth://swarm-pair/cause", RendezvousID: "rvz-cause"},
		fail:       fail,
	}
}

func (h *causePairingHost) BeginPairing(_ context.Context, _ PairStartReq,
	_ func(sas []string, deviceName string) (bool, error),
	result func(PairResult)) (PairView, error) {
	go result(PairResult{Err: h.fail})
	return h.view, nil
}

var _ PairingHost = (*causePairingHost)(nil)

// TestPairResult_CauseSurvivesTheWireToTheClient walks the whole path B71(1) says is
// broken: a host reports PairResult.Err, the daemon must put a cause on the pushed
// pair_result, and the client must decode it into PairingResult. Driven for EVERY
// situation, and the frames are compared against each other so a daemon that sent one
// blanket cause for all of them fails.
func TestPairResult_CauseSurvivesTheWireToTheClient(t *testing.T) {
	onWire := map[PairFailure]string{}
	for _, tc := range pairFailureCases {
		t.Run(string(tc.want), func(t *testing.T) {
			sock := servePairingHost(t, newCausePairingHost(fmt.Errorf("host: %w", tc.err)))
			rc := rawDial(t, sock)
			rep := rc.hello(Version, []string{CapPairing})
			rc.writeControl(Control{Op: OpPairStart, EndpointID: rep.EndpointID,
				Pairing: &PairingControl{Capability: "full"}})

			res := awaitPairResult(t, rc)
			if res.Pairing == nil {
				t.Fatalf("pair_result carries a nil Pairing, so the failure reached the owner with "+
					"NO cause at all (B71(1)); want one naming %q", tc.want)
			}
			if res.Pairing.DeviceID != "" {
				t.Fatalf("pair_result carries DeviceID %q on a FAILURE; nothing may be enrolled", res.Pairing.DeviceID)
			}
			if got := PairFailure(res.Pairing.Failure); got != tc.want {
				t.Errorf("pair_result failure = %q, want %q -- %s", got, tc.want, tc.situation)
			}

			// The client half: the pushed payload must decode into a PairingResult that
			// still names the cause, because that is the only thing cmd/swarm ever sees.
			decoded := pairingResultFromControl(res.Pairing)
			if decoded.Paired {
				t.Errorf("PairingResult.Paired = true for a failure (%s)", tc.situation)
			}
			if decoded.Failure != tc.want {
				t.Errorf("PairingResult.Failure = %q, want %q -- %s", decoded.Failure, tc.want, tc.situation)
			}
		})
		if prev, dup := onWire[tc.want]; dup {
			t.Errorf("the wire carried cause %q for BOTH %q and %q", tc.want, prev, tc.situation)
		}
		onWire[tc.want] = tc.situation
	}
}

// TestPairingSession_FailClosedPathsNameTheirCause covers the two terminal outcomes the
// daemon never sends: the client's own fail-closed deliveries. Both used to hand the
// caller a bare PairingResult{Paired:false}, which is the same causeless outcome by
// another route -- and the two are NOT the same thing to an operator. A closed session
// is the owner's own doing; a lost connection means the daemon went away mid-pairing,
// which is the one that deserves a look at the daemon.
func TestPairingSession_FailClosedPathsNameTheirCause(t *testing.T) {
	t.Run("session closed by the caller", func(t *testing.T) {
		host := newFakePairingHost()
		sock := servePairingHost(t, host)
		c := dialClient(t, sock, []string{CapPairing})
		sess, err := c.StartPairing(PairStartReq{Capability: "full"})
		if err != nil {
			t.Fatalf("StartPairing: %v", err)
		}
		_ = recvPending(t, sess.Pending(), recvTimeout)

		sess.Close()

		res := recvResult(t, sess.Result(), recvTimeout)
		if res.Paired {
			t.Fatalf("Result Paired = true after Close; want false (fail closed)")
		}
		if res.Failure != PairFailSessionClosed {
			t.Errorf("Close() delivered failure %q, want %q; a caller that closed its own session "+
				"must not be reported the same as a daemon that vanished", res.Failure, PairFailSessionClosed)
		}
	})

	t.Run("daemon connection lost", func(t *testing.T) {
		host := newFakePairingHost()
		sock := servePairingHost(t, host)
		c := dialClient(t, sock, []string{CapPairing})
		sess, err := c.StartPairing(PairStartReq{Capability: "full"})
		if err != nil {
			t.Fatalf("StartPairing: %v", err)
		}
		_ = recvPending(t, sess.Pending(), recvTimeout)

		// Drop the connection under the session WITHOUT closing the session first, so the
		// read loop's fail-closed teardown is what delivers the result.
		_ = c.Close()

		res := recvResult(t, sess.Result(), recvTimeout)
		if res.Paired {
			t.Fatalf("Result Paired = true after a dropped connection; want false (fail closed)")
		}
		if res.Failure != PairFailConnectionLost {
			t.Errorf("a dropped connection delivered failure %q, want %q", res.Failure, PairFailConnectionLost)
		}
	})
}

// TestPairingResult_UnknownCauseIsNormalized is the CLOSED-SET property, enforced at
// the boundary rather than promised in a comment: a cause the client does not recognise
// becomes PairFailInternal and never reaches the caller as itself.
//
// It is what makes "no free text on the operator's terminal" true MECHANICALLY. cmd/swarm
// prints from its own message table keyed on these constants, so even a daemon that put
// relay-supplied bytes -- or a terminal escape sequence -- in the field cannot paint the
// owner's screen with them.
func TestPairingResult_UnknownCauseIsNormalized(t *testing.T) {
	for _, hostile := range []string{
		"not-a-cause",
		"\x1b]0;pwned\a",             // OSC title-set: terminal injection if it were ever printed
		"declined\ndevice_id=devEvil", // a second directive smuggled behind a newline
		string(make([]byte, 4096)),
	} {
		got := pairingResultFromControl(&PairingControl{Failure: hostile})
		if got.Paired {
			t.Errorf("PairingResult.Paired = true for a payload with no DeviceID (failure %q)", hostile)
		}
		if got.Failure != PairFailInternal {
			t.Errorf("unrecognised wire cause %q decoded to %q, want %q; an unknown cause must be "+
				"normalised, never passed through -- the closed set is the whole safety argument",
				hostile, got.Failure, PairFailInternal)
		}
	}
}

// TestPairingResult_SuccessCarriesNoCause pins the other polarity: a paired outcome must
// leave Failure empty, so `res.Failure != PairFailNone` and `!res.Paired` can never
// disagree and no caller has to consult both.
func TestPairingResult_SuccessCarriesNoCause(t *testing.T) {
	res := pairingResultFromControl(&PairingControl{DeviceID: "devX", Name: "phone", Capability: "full"})
	if !res.Paired {
		t.Fatalf("PairingResult.Paired = false for a payload carrying a DeviceID")
	}
	if res.Failure != PairFailNone {
		t.Errorf("a paired result carries failure %q, want %q", res.Failure, PairFailNone)
	}
}
