package skeleton

// FAILING-FIRST (TDD RED, GG-5) for W-APPROVE's whole point: A TAP ON THE PHONE ANSWERS A REAL
// APPROVAL, over the shipped stack, with nothing in the chain faked.
//
// WHY THIS FILE EXISTS BESIDE interaction_chain_e2e_test.go. That test proves the two VERDICTS
// -- allow and deny -- resolve correctly and dismiss the card, and it does so by calling
// `rig.sk.approveInteraction(...)` DIRECTLY, in the test's own process. It says why in its
// header: at the time, "approve is not a daemon remote op", so the wire hop did not exist and
// calling the validator in place was the honest thing to write. Everything between the phone
// and that function was the missing product:
//
//	App.Approve      did not exist; the facade was pinned READ ONLY by an adversarial test
//	SealApprove      did not exist; RemoteCommand carried no ApproveReq
//	opForAction      refused approve one hop short of the daemon, sealing no reply
//	OpApprove        did not exist; handleControl had no arm and no backend seam
//
// So this test enters where a USER does -- swarmmobile.App.Approve, the verb a button calls --
// and asserts the four things only a full round trip can see:
//
//	1. the phone authors an approve from the card IT holds (IS-APR-2's echo, end to end);
//	2. it crosses the untrusted relay sealed, is opened by a SEPARATE gateway PROCESS, is
//	   forwarded to the daemon and passes requireRemoteAuthz with a real device signature
//	   (ADR-007 D4) -- not a stub authenticator;
//	3. the daemon resolves the card and journals approval_resolved attributed to the PHONE,
//	   carrying the operation_id the phone minted (§3.6);
//	4. that resolution travels BACK and the card leaves PendingApprovals, which is what lifts
//	   IS-LIFE-3's retention exemption.
//
// RED: swarmmobile.App has no Approve, so this file does not compile. Once it does, the daemon
// still refuses ("approve not supported by this daemon") until coreAPI is an
// InteractionApprover -- the wiring half, which nothing else asserts.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/claude"
	"github.com/Nathandela/swarm/internal/protocol"
	swarmmobile "github.com/Nathandela/swarm/mobile"
)

// awaitApplied blocks until the daemon has recorded that it typed a phone answer into this
// session's dialog. It is the join point for a test whose approve travels asynchronously (over
// the relay and a gateway process): driving the "dialog left the screen" observation before the
// injection has happened would resolve the card as answered_locally, which is a different path.
func awaitApplied(t *testing.T, sk *Daemon, session string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		sk.itemMu.Lock()
		ap := sk.approvals[session]
		applied := ap != nil && ap.applied != ""
		sk.itemMu.Unlock()
		if applied {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the daemon never applied a phone answer to session %s", session)
}

// TestApproveRoundTripE2E_APhoneTapAnswersTheMachinesApproval.
func TestApproveRoundTripE2E_APhoneTapAnswersTheMachinesApproval(t *testing.T) {
	rig := newS19Rig(t)
	rig.Pair()
	rig.StartGateway()
	rig.Eventually("the machine's reconcile record reached the phone", func() bool {
		return rig.Summary().Reconciled
	})

	// Since M1.2 a phone answer is APPLIED and not merely recorded: the daemon types the
	// dialog's own keys into this PTY (mirror-program.md section 3). So the session PAINTS a
	// recorded claude permission dialog and blocks on it, and the adapter resolver is the real
	// claude one -- the party that holds the recorded key map.
	dialog, cols, rows := gridScript(t, bashDialogGrid)
	sessionID := rig.LaunchOnMachineSized(dialog, cols, rows)
	rig.sk.adapterFor = func(string) (adapter.Adapter, bool) { return claude.New(), true }
	rig.Eventually("the phone's roster shows the session the machine launched", func() bool {
		return rig.RosterHas(sessionID)
	})
	_, localID, ok := protocol.ParseID(sessionID)
	if !ok {
		t.Fatalf("owner Launch returned %q, which is not a namespaced id", sessionID)
	}

	// The machine raises a real pending permission through the production capture path.
	replayClaudeCorpus(t, rig.sk, localID, "claude-edit-permissionrequest-run1.json")

	var card swarmmobile.TranscriptItem
	rig.Eventually("the card the machine is blocked on reached the phone", func() bool {
		p := readPendingApprovals(t, rig)
		if len(p) == 1 {
			card = p[0]
			return true
		}
		return false
	})
	// ---- the tap -------------------------------------------------------------
	// Three flat strings, which is everything a button knows: the session, the card and the id
	// of the decision the user chose. The binding tuple is NOT passed -- IS-APR-2 makes the
	// phone echo content_hash and expires_at off the card it holds, and a signature that
	// accepted them from a caller would be a signature that invited computing them.
	op, err := rig.App().Approve(sessionID, card.ItemID, "allow")
	if err != nil {
		t.Fatalf("App.Approve was refused: %v%s", err, rig.gatewayTail())
	}
	if op == nil || op.OperationID == "" {
		t.Fatalf("App.Approve returned %+v; an approve must carry the phone-minted operation_id "+
			"its resolution is attributed to (§3.6)", op)
	}
	if op.OperationID == card.ItemID {
		t.Errorf("the approve's operation_id equals the interaction id; IS-APR-1 says they are "+
			"separate ids and SHALL NEVER be equal (%q)", op.OperationID)
	}
	if op.Action != protocol.ActionApprove {
		t.Errorf("App.Approve authored action %q, want %q -- IS-LIFE-4 adds no new signed action",
			op.Action, protocol.ActionApprove)
	}

	// ---- the answer comes back ----------------------------------------------
	// The op crosses the relay and a separate gateway process, so the daemon applies it whenever
	// it arrives; wait for the injection, THEN let the machine observe the dialog leave. Driving
	// the observation first would resolve the card as answered_locally -- a different path, and
	// a green test for the wrong reason.
	awaitApplied(t, rig.sk, localID)
	dialogLeaves(rig.sk, localID)

	resolved := awaitFacadeResolution(t, rig, sessionID, card.ItemID)
	if resolved["decision"] != "allowed" {
		t.Errorf("the resolution's decision = %v, want `allowed`. The chosen id `allow` carries "+
			"Verdict=allow from the adapter's own capture, and §3.6's split is read off exactly that "+
			"(IS-RES-1)%s", resolved["decision"], rig.gatewayTail())
	}
	if resolved["by"] != "phone" {
		t.Errorf("the resolution is attributed by=%v, want `phone` -- the whole claim of this test "+
			"is that the PHONE answered it, not the owner at the machine (§3.6)", resolved["by"])
	}
	if resolved["operation_id"] != op.OperationID {
		t.Errorf("the resolution echoes operation_id %v, want the phone's own %q. Without the echo a "+
			"screen cannot tell its own tap from somebody else's answer arriving at the same moment",
			resolved["operation_id"], op.OperationID)
	}

	// IS-LIFE-3's retention exemption lifts only when the resolution FOLDS on the phone.
	rig.Eventually("the answered card left the phone's pending set", func() bool {
		return len(readPendingApprovals(t, rig)) == 0
	})
	if got := findItem(readTranscript(t, rig, sessionID), card.ItemID); got == nil || !got.Resolved {
		t.Errorf("the answered approval_request is %v on the phone; a resolution marks the request "+
			"Resolved -- which ends its IS-LIFE-3 exemption -- and must NOT delete it, because a "+
			"transcript that erases what it answered cannot show what was decided", got)
	}

	// ---- and the card cannot be answered twice -------------------------------
	// The phone refuses locally now that its own copy is resolved (IS-LIFE-2 spends exactly one
	// resolution), which is the check that keeps a double tap off the wire entirely.
	if _, err := rig.App().Approve(sessionID, card.ItemID, "allow"); err == nil {
		t.Error("a second Approve on the same card was accepted; IS-LIFE-2 gives every request " +
			"exactly one resolution")
	}

	// ---- the decision vocabulary is the CLI's, and the daemon owns it --------
	// A second recorded request, answered with an id its own card never offered. It must be
	// refused BY THE MACHINE (the phone cannot know the offered set is closed) with
	// CodeInvalidField, and the card must stay pending -- an unoffered id was never rendered to
	// anybody, so it is a bug or a tampering gateway, and neither is an owner's answer.
	replayClaudeCorpus(t, rig.sk, localID, "claude-bash-permissionrequest-run1.json")
	var second swarmmobile.TranscriptItem
	rig.Eventually("the second recorded request raised a new card on the phone", func() bool {
		p := readPendingApprovals(t, rig)
		if len(p) == 1 && p[0].ItemID != card.ItemID {
			second = p[0]
			return true
		}
		return false
	})
	if _, err := rig.App().Approve(sessionID, second.ItemID, "definitely-not-offered"); err != nil {
		t.Fatalf("the phone refused an unoffered decision locally: %v. The offered set is the "+
			"MACHINE's -- §3.5 keeps the ids the CLI's own -- so this must reach the daemon and be "+
			"refused there", err)
	}
	rig.Eventually("the daemon refused the unoffered decision and left the card pending", func() bool {
		p := readPendingApprovals(t, rig)
		return len(p) == 1 && p[0].ItemID == second.ItemID
	})
}
