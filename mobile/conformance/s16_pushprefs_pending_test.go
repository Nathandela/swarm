package conformance_test

// PB-APP-7's FAILURE path: the half that left this suite when two cases were removed from
// TestS8_SurfacesWithNoWireVerbFailVisiblyAndLeakNoPendingOps.
//
// WHY THOSE CASES WENT, and what went with them. That test asserted two properties of three
// verbs with no wire verb: the call fails VISIBLY, and it leaks no pending op. S16 wired two of
// the three, so their cases asserted the product was still broken and were removed --
// SetPushPreference now seals a real signed push_prefs, and Interrupt is a keystroke on the live
// input plane. But the SECOND property did not stop applying to SetPushPreference: it still
// returns a.signedCommand, so it still issues an op, and the hazard the original test named is
// unchanged -- an op no reply can ever resolve raises PendingOpCount for the life of the
// process, which makes every REAL pending op invisible behind it.
//
// Interrupt is owed nothing here and that is not an omission: input is never acknowledged by the
// machine, so it creates no op at all and the property is structurally inapplicable.
//
// THE ORDERING THIS FILE ALSO MEASURES. SetPushPreference persists the preference and advances
// its durable version BEFORE sealing the command, so a send that then fails -- and ADR-007 B16
// makes a backgrounded, disconnected handset the normal case -- burns a version the machine
// never sees. The second test is the one that decides whether that is a defect or a cost: a
// burned version is harmless precisely because the machine's rule is STRICTLY ADVANCING rather
// than consecutive, so the next successful update still wins. What it is not is free; see the
// slice evidence for the residual it leaves on the screen.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/status"
	swarmmobile "github.com/Nathandela/swarm/mobile"
)

// TestPBAPP7_AFailedPreferenceSendLeaksNoPendingOp.
//
// The failure is produced the way a handset produces it -- the app is stopped, which is what
// Android's lifecycle does when the user leaves the screen -- rather than by breaking a seam.
func TestPBAPP7_AFailedPreferenceSendLeaksNoPendingOp(t *testing.T) {
	h := s16ReconciledHarness(t)

	before, err := h.App.PendingOpCount()
	if err != nil {
		t.Fatalf("PendingOpCount: %v", err)
	}

	if err := h.App.Stop(); err != nil {
		t.Fatalf("App.Stop: %v", err)
	}

	op, err := h.App.SetPushPreference(&swarmmobile.PushPreference{Alerts: true})
	if err == nil {
		t.Fatalf("PB-APP-7: SetPushPreference returned success with no connection (op %+v). The "+
			"machine never heard the preference, and the settings screen has been told it did", op)
	}

	after, err := h.App.PendingOpCount()
	if err != nil {
		t.Fatalf("PendingOpCount: %v", err)
	}
	if after != before {
		t.Errorf("PB-APP-7: a push_prefs that was never appended left %d pending op(s) behind "+
			"(%d -> %d). An op no reply can resolve raises the count for the life of the process, "+
			"and every genuinely in-flight op is then hidden behind one that can never land",
			after-before, before, after)
	}
}

// TestPBAPP7_APreferenceBurnedByAFailedSendDoesNotPoisonTheNextOne.
//
// This is the question the persist-then-deliver ordering actually turns on. The local Save runs
// first, so a failed send advances the phone's counter past a version the machine never stored.
// That is safe ONLY because remotegw.filePushPrefs.SavePrefs refuses anything that does not
// STRICTLY exceed what it holds, rather than requiring the next value: a gap is fine, a
// repeat is not.
//
// If the rule were consecutive -- or if the phone reused a burned version -- the first
// backgrounded toggle would silently deafen the machine to every later one, which is the same
// brick PB-PUSH-10's durable counter exists to prevent, arriving through the retry path instead
// of through a process death.
func TestPBAPP7_APreferenceBurnedByAFailedSendDoesNotPoisonTheNextOne(t *testing.T) {
	h := s16ReconciledHarness(t)
	m := s16NewMachine(t, h)

	if err := h.App.Stop(); err != nil {
		t.Fatalf("App.Stop: %v", err)
	}
	if _, err := h.App.SetPushPreference(&swarmmobile.PushPreference{Alerts: true}); err == nil {
		t.Fatalf("precondition: the disconnected send succeeded, so no version was burned and " +
			"this test measures nothing")
	}

	if err := h.App.Start(); err != nil {
		t.Fatalf("App.Start: %v", err)
	}

	// The user tries again, which is the only thing the product offers them.
	off := swarmmobile.PushPreference{}
	if _, err := h.App.SetPushPreference(&off); err != nil {
		t.Fatalf("PB-APP-7: the retry after a failed send was refused locally: %v", err)
	}
	m.apply(t, h, off)

	if n := m.wake(t, "sess-after-burned-version", status.GroupNeedsInput); n != 0 {
		t.Errorf("PB-APP-7/PB-PUSH-10: %d push(es) were sent after the user turned notifications "+
			"off on the SECOND attempt. The machine refused the update, which means the version "+
			"the phone burned on the failed send was not strictly exceeded -- so one backgrounded "+
			"toggle deafens the machine to every later one, with the settings screen showing the "+
			"user's choice throughout", n)
	}
}
