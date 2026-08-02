package conformance_test

// FAILING-FIRST (TDD RED, GG-5) test for slice S19's fourth production hole: App.TakeControl
// minted NO GATE TOKEN, so the daemon refused every take_control the real facade authored and
// the phone could neither take control nor type.
//
// WHAT THE DAEMON REQUIRES (internal/protocol/server.go handleTakeControl). Whenever the
// backend is an OperationClaimer -- which the production coreAPI always is -- an empty
// Control.GateToken is refused before authorization ("take_control requires a gate token"),
// deliberately, because a hash-only check would accept it: SHA256("") is a perfectly valid
// 32-byte hash. The token is then bound into the signature via ContentHash = SHA256(token),
// which the daemon recomputes from the WIRE token, so a relay that swaps it breaks the
// signature.
//
// WHAT THE FACADE DID. TakeControl went through sealSignedCommand's DEFAULT branch:
// ContentHash nil, SealCommandEnvelope, no token anywhere on the frame. phonecore has carried
// SignTakeControl and SealTakeControlEnvelope since A7 and internal/phonesim calls both; the
// bound facade -- the only one a handset runs -- called neither.
//
// WHY NO SHIPPED TEST COULD HAVE CAUGHT IT (PB-INPUT-2/-3, PB-APP-4). This harness's machine
// GRANTS ITSELF the lease: harness.Drain answers every take_control it sees with an OpLease it
// seals locally, so the frame's gate token is never inspected by anything. remotegw's lease
// tests build the RemoteCommand in-test with a token literal, and the one skeleton test that
// reaches a real daemon signs its take_control with phonecore directly rather than through the
// facade. Every fence in the chain sits on a path that never reaches handleTakeControl's
// present-check. That is defect class (v) in the S19 brief: a fence guarding a path production
// does not take.

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
)

// TestS19_TakeControlMintsAGateTokenBoundIntoItsSignature.
func TestS19_TakeControlMintsAGateTokenBoundIntoItsSignature(t *testing.T) {
	h := newHarness(t)
	h.PushReconcile()
	eventually(t, "reconcile never adopted", func() bool {
		s, err := h.App.StateSummary()
		return err == nil && s.Reconciled
	})

	if _, err := h.App.TakeControl(testSession); err != nil {
		t.Fatalf("App.TakeControl: %v", err)
	}
	cmd := h.AwaitCommand(protocol.ActionTakeControl)

	if cmd.GateToken == "" {
		t.Fatalf("the facade sealed a take_control with no gate token. handleTakeControl refuses "+
			"it before authorization, so this phone can never take control of %q and every "+
			"keystroke after it is dropped by the gateway for want of a lease", testSession)
	}

	// The token must be BOUND, not merely carried. An unbound token is a value the relay can
	// swap freely: the daemon recomputes SHA256 from the wire token, so a signature that does
	// not cover it verifies against whatever arrives.
	want := sha256.Sum256([]byte(cmd.GateToken))
	if len(cmd.ContentHash) != len(want) || string(cmd.ContentHash) != string(want[:]) {
		t.Fatalf("the take_control's content hash is %s, want SHA256(gate token) = %s. The daemon "+
			"recomputes the hash from the WIRE token, so a signature that does not cover it is a "+
			"signature over a token a relay may replace at will",
			base64.StdEncoding.EncodeToString(cmd.ContentHash),
			base64.StdEncoding.EncodeToString(want[:]))
	}

	// A second take_control must mint a DIFFERENT token. The daemon claims the operation id
	// single-use and the token is the one-shot gate; a constant would be a shared secret that
	// any captured frame replays.
	if _, err := h.App.TakeControl(testSession); err != nil {
		t.Fatalf("second App.TakeControl: %v", err)
	}
	second := s19AwaitNth(t, h, protocol.ActionTakeControl, 2)
	if second.GateToken == cmd.GateToken {
		t.Fatalf("two take_controls carried the SAME gate token %q; a one-shot gate that repeats "+
			"is not one", cmd.GateToken)
	}
}
