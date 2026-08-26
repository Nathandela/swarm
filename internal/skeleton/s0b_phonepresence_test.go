package skeleton

// FAILING-FIRST (TDD RED, GG-5) for the fact the lease is about to stop supplying.
// Plan: docs/specifications/chat-surface-plan.md §9 (Wave G) and §8's prerequisite.
// Beads: agents-tracker-tbpm.9, and the blocker the audit committee found on tbpm.7.
//
// WHY THIS EXISTS BEFORE THE DELETION IT SERVES. R1 removes "take control" from the
// product, so no phone will ever hold a lease again. Three things read that lease today
// and only one of them is cosmetic:
//
//	the board's "phone control" marker            (internal/tui/general.go) -- cosmetic
//	the grid-tap skip                             (serve.go)                -- an optimisation
//	anyControlled, ADR-010 Amendment 3 C3's gate  (serve.go:618-629)        -- LOAD BEARING
//
// C3 is "the supervisor never types into a session someone is driving". Delete the lease
// naively and that gate answers false for every phone, so the passive supervisor starts
// typing notifications into a session somebody is actively chatting in -- through the same
// PTY the phone is sending to. The lease has to be replaced as a FACT before it is removed
// as a control.
//
// THE REPLACEMENT IS WEAKER AND HONEST. There is no presence protocol on this wire: the
// watch channel is fallback-only, a chat phone reads a machine-wide journal, and
// foreground_only is a push-transport class rather than a presence fact. What the daemon
// DOES observe is a message arriving. So the fact is "this phone sent recently", which is
// what the terminal will say in as many words -- never "the phone is here", which would
// name something nobody measured.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
)

// TestPhonePresence_ASendRecordsThatSomebodyIsDriving. No lease is ever taken here --
// composer_send needs none, which is the whole of R1 -- so at HEAD there is nothing on this
// session to tell the supervisor that a person is mid-conversation with it.
func TestPhonePresence_ASendRecordsThatSomebodyIsDriving(t *testing.T) {
	r := newKeystrokeRig(t)

	if r.sk.phoneRecentlyActive(r.local) {
		t.Fatalf("a session nobody has sent to already reads as driven from a phone")
	}

	code, err := r.sendBare("", "still here", "devA:01JPRESENCE0000000000000")
	if err != nil || code != "" {
		t.Fatalf("send refused: code %q err %v", code, err)
	}

	if !r.sk.phoneRecentlyActive(r.local) {
		t.Fatalf("a session the phone just sent a message into does not read as driven.\n" +
			"ADR-010 Amendment 3 C3 says the supervisor never types into a session someone is " +
			"driving, and it reads this. composer_send takes NO LEASE, so once take-control " +
			"leaves the product the old gate answers false for every phone on earth and the " +
			"supervisor types its notification into somebody's open conversation.")
	}
}

// TestPhonePresence_TheGateConsultsIt is C3's own predicate, away from the rig's attached
// owner (an owner attach is a controller lease, and would make this vacuous).
func TestPhonePresence_TheGateConsultsIt(t *testing.T) {
	sk := assemble(t)
	const id = "no-such-session"

	if sk.anyControlled(id) {
		t.Fatalf("anyControlled on a session with no lease and no phone = true")
	}
	sk.setPhoneActivityForTest(id, time.Now())
	if !sk.anyControlled(id) {
		t.Fatalf("anyControlled ignores a phone that just sent. The lease is being deleted as a " +
			"control; if this gate does not learn the replacement fact, C3 silently stops holding")
	}
}

// TestPhonePresence_ItExpires keeps the fact honest at the other end. "Sent recently" is a
// claim with a horizon; a phone that sent an hour ago is not driving anything, and a gate
// that never expired would mute the supervisor for the life of the session.
func TestPhonePresence_ItExpires(t *testing.T) {
	sk := assemble(t)
	const id = "no-such-session"

	sk.setPhoneActivityForTest(id, time.Now())
	if !sk.anyControlled(id) {
		t.Fatalf("the record did not register at all")
	}

	sk.setPhoneActivityForTest(id, time.Now().Add(-phoneActiveHorizon-time.Second))
	if sk.anyControlled(id) {
		t.Fatalf("a phone that sent %v ago still counts as driving the session. The marker the "+
			"terminal draws from this says \"phone sent HH:mm\", which is a fact with a time on "+
			"it; the gate must age out the same way", phoneActiveHorizon)
	}
	if sk.phoneRecentlyActive(id) {
		t.Fatalf("phoneRecentlyActive disagrees with anyControlled about the same record")
	}
}

// TestPhonePresence_ARefusedSendRecordsNothing. The fact is "a message was DELIVERED", not
// "a message was attempted": a send the machine turned away is not somebody driving
// anything, and recording it would silence the supervisor on the strength of a refusal.
func TestPhonePresence_ARefusedSendRecordsNothing(t *testing.T) {
	// The rig's default adapter is Codex-shaped: it proves no keystroke seam, so the send
	// is refused structured_unsupported having typed nothing.
	r := newR7ComposerRig(t, false)

	code, _ := r.sendBare("", "never arrives", "devA:01JPRESENCEREF00000000000")
	if code != protocol.CodeStructuredUnsupported {
		t.Fatalf("expected the send to be refused, got code %q", code)
	}
	if r.sk.phoneRecentlyActive(r.local) {
		t.Fatalf("a REFUSED send marked the session as driven from a phone")
	}
	if at := r.sk.phoneActivityAt(r.local); !at.IsZero() {
		t.Fatalf("phoneActivityAt after a refusal = %v, want the zero time", at)
	}
}

// TestPhonePresence_TheTerminalReportsTheSendAndNotAPresence pins the shape at its source.
// The board marker and the attach row both render this, and the difference between what was
// measured and what we would like to have is the whole point: a send is observed, a presence
// is not. So the record is an INSTANT -- "phone sent 09:41" -- and never a boolean.
func TestPhonePresence_TheTerminalReportsTheSendAndNotAPresence(t *testing.T) {
	r := newKeystrokeRig(t)
	if got := r.sk.phoneActivityAt(r.local); !got.IsZero() {
		t.Fatalf("phoneActivityAt on an untouched session = %v, want the zero time", got)
	}

	before := time.Now()
	code, err := r.sendBare("", "mark me", "devA:01JPRESENCEMARK0000000000")
	if err != nil || code != "" {
		t.Fatalf("send refused: code %q err %v", code, err)
	}

	at := r.sk.phoneActivityAt(r.local)
	if at.Before(before) || time.Since(at) > time.Minute {
		t.Fatalf("phoneActivityAt = %v, want the instant of the send", at)
	}
}
