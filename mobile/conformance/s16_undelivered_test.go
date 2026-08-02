package conformance_test

// Slice S16 -- the second residual it inherits, recorded in
// docs/verification/remote-phaseB-progress.md and owned here because it needs a facade verb:
//
//	"The undelivered-input ledger is unbounded. coalesce.go:56/170/180 append forever,
//	 Undelivered() is a read and not a drain, and the facade has no clear verb. A minute of
//	 autorepeat against a dead lease retains roughly 1800 entries, and UndeliveredInputs()
//	 copies the whole slice per call."
//
// It travels with PB-APP-8 because it is the same shape of defect: a UX state the phone
// records and the screen can never resolve. PB-INPUT-1 requires the user be TOLD what did
// not reach the machine; a list that only ever grows is not a notice, it is a leak with a
// renderer attached -- and the copy-per-call makes the settings screen quadratic in the
// number of keystrokes the user has lost.
//
// A dead lease is the ordinary way to get here, not a contrived one: PB-INPUT-2 refuses
// every keystroke until the machine confirms a lease, and every refusal is one ledger entry.
// Autorepeat on a held key is ~30 events/s.

import (
	"testing"
)

// s16LedgerCeiling is the largest ledger this test will accept. It is not a number S16
// invents: journalLogSize already bounds the facade's other unbounded-by-nature read model
// at 1024 for the same reason ("the DURABLE model is the core's; this is the page cache, so
// it is bounded rather than grown for the life of the process"). Any bound is acceptable as
// long as there IS one; this is the ceiling, not the value.
const s16LedgerCeiling = 1024

// s16LostKeystrokes is comfortably past any plausible bound and is what four seconds of
// autorepeat against a dead lease produces.
const s16LostKeystrokes = 4000

func TestPBINPUT1_TheUndeliveredLedgerIsBoundedAndSaysWhatItDropped(t *testing.T) {
	h := newHarness(t)

	// No lease was ever confirmed, so every one of these is refused at the gate and recorded.
	for i := 0; i < s16LostKeystrokes; i++ {
		_ = h.App.SendInput(testSession, []byte("a"))
	}

	list, err := h.App.UndeliveredInputs()
	if err != nil {
		t.Fatalf("UndeliveredInputs: %v", err)
	}
	n, err := list.Count()
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n == 0 {
		t.Fatalf("precondition: %d refused keystrokes produced an EMPTY ledger. PB-INPUT-1 "+
			"requires the user be told what did not reach the machine; if this is empty the "+
			"bound below is measuring nothing", s16LostKeystrokes)
	}
	if n > s16LedgerCeiling {
		t.Errorf("PB-INPUT-1: the undelivered ledger holds %d entries after %d refused "+
			"keystrokes and is therefore unbounded. A held key against a dead lease is ~30 "+
			"events/s, so a minute of it retains ~1800 entries -- and UndeliveredInputs copies "+
			"the whole slice on every call, so the screen that renders them gets slower the "+
			"worse the problem is. journalLogSize bounds the journal page cache at %d for "+
			"exactly this reason", n, s16LostKeystrokes, s16LedgerCeiling)
	}

	// A bound that silently discards is a second defect wearing the first one's clothes: the
	// user is told about the last N things they lost and never told that there were more.
	// Event.Dropped is the precedent this mirrors -- the callback plane already counts what
	// its overflow discarded rather than dropping quietly.
	dropped, derr := s16IntVerb(t, list, "Dropped", "PB-INPUT-1",
		"How many older entries the bound discarded. Without it a bounded ledger tells the "+
			"user about the last N keystrokes they lost and silently forgets that there were "+
			"thousands -- which understates the failure at exactly the moment it is worst. "+
			"swarmmobile.Event.Dropped is the same contract on the callback plane.")
	if derr != nil {
		t.Fatalf("UndeliveredList.Dropped: %v", derr)
	}
	if dropped <= 0 {
		t.Errorf("PB-INPUT-1: %d entries were refused, the ledger holds %d, and Dropped reports "+
			"%d. The discarded entries are invisible", s16LostKeystrokes, n, dropped)
	}
}

// TestPBINPUT1_TheUndeliveredLedgerCanBeCleared.
//
// Undelivered() is a READ and not a drain, deliberately -- "the state must survive the call
// that produced it, and a screen that opens after the failure must still see it" -- and that
// is right. What is missing is the other half: nothing anywhere clears it, so the notice the
// user has read and acknowledged stays on screen for the life of the process, and the next
// genuine loss arrives underneath a wall of ones they already dealt with.
func TestPBINPUT1_TheUndeliveredLedgerCanBeCleared(t *testing.T) {
	h := newHarness(t)

	for i := 0; i < 8; i++ {
		_ = h.App.SendInput(testSession, []byte("x"))
	}
	list, err := h.App.UndeliveredInputs()
	if err != nil {
		t.Fatalf("UndeliveredInputs: %v", err)
	}
	if n, _ := list.Count(); n == 0 {
		t.Fatalf("precondition: nothing was recorded as undelivered")
	}

	out := s16Verb(t, h.App, "ClearUndeliveredInputs", "() error", "PB-INPUT-1",
		"The acknowledgement half of the ledger. It is a separate verb rather than a draining "+
			"read because the two callers are different: a screen that OPENS must see the "+
			"backlog (UndeliveredInputs stays a read), and a user who DISMISSES it must be able "+
			"to say so once, for every screen.")
	if err := s16Err(t, out); err != nil {
		t.Fatalf("ClearUndeliveredInputs: %v", err)
	}

	list, err = h.App.UndeliveredInputs()
	if err != nil {
		t.Fatalf("UndeliveredInputs after clear: %v", err)
	}
	if n, _ := list.Count(); n != 0 {
		t.Errorf("PB-INPUT-1: the ledger still holds %d entries after being cleared", n)
	}

	// And it must keep WORKING. A clear that disabled the ledger would satisfy the assertion
	// above and silently drop every future loss -- PB-INPUT-1's forbidden silent drop, arrived
	// at through its own remedy.
	_ = h.App.SendInput(testSession, []byte("y"))
	list, err = h.App.UndeliveredInputs()
	if err != nil {
		t.Fatalf("UndeliveredInputs after a post-clear loss: %v", err)
	}
	if n, _ := list.Count(); n == 0 {
		t.Errorf("PB-INPUT-1: a keystroke lost AFTER the ledger was cleared was not recorded. " +
			"Clearing disabled the notice rather than acknowledging it")
	}
}
