package conformance_test

// Slice S16 -- PB-APP-3's Stop and PB-APP-4's peek, over the real relay and the real gateway
// opener.
//
// PB-APP-2 (roster + Group verbatim), PB-APP-5 (presence, kill switch read-only) and PB-APP-6
// (launch and its content hash) are exercised against this same backend by S8's
// conformance_test.go and verbs_test.go, and are deliberately not duplicated here: S16 owns
// their SCREENS, and the screen half is in
// android/app/src/test/kotlin/dev/swarm/phone/ui. What is here is the behaviour S16 changes.

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// TestPBAPP3_StopIsTheSignedTurnInterruptOp.
//
// SUPERSESSION EXECUTED (Wave R6, Mirror M2.4 "Stop becomes a signed interrupt op";
// pre-recorded in docs/verification/r6-red/chat-red.txt and mobile/r6_chatverbs_test.go).
// This test was TestPBAPP3_StopIsTheInterruptKeystrokeOnTheLiveLease and pinned the
// 2026-07-25 resolution verbatim: "An interrupt IS a keystroke: Ctrl-C is byte 0x03 ...
// So Stop is: hold the lease, send 0x03", and its second half asserted "NO NEW SIGNED
// ACTION WAS MINTED ... a command bearing an invented action would be refused by the
// daemon's closed capability switch while looking, to the app, exactly like one that was
// delivered". That premise was dissolved by Wave R1 (ActionTurnInterrupt is mapped at
// every hop: actionClass, opForAction, handleControl) and retired by Wave R6's real
// handler, so the pin is retargeted to the NEW contract: Stop seals the signed
// turn_interrupt command -- no lease, no 0x03, and the op resolves, which is what gives
// the button a visible success AND a visible refusal.
func TestPBAPP3_StopIsTheSignedTurnInterruptOp(t *testing.T) {
	h := s16ReconciledHarness(t)

	if _, err := h.App.Interrupt(testSession, "01JTURN"); err != nil {
		t.Fatalf("Interrupt: %v -- Stop rides the signed turn_interrupt op now (M2.4) and "+
			"needs no lease; a refusal here is a Stop button that does nothing", err)
	}

	cmd := h.AwaitCommand("turn_interrupt")
	if cmd.Session != testSession {
		t.Errorf("PB-APP-3: the turn_interrupt command targeted %q, want %q", cmd.Session, testSession)
	}
	if cmd.Sig == "" || cmd.DeviceID == "" {
		t.Errorf("PB-APP-3/M2.4: the turn_interrupt command carries no device signature "+
			"(sig %q, device %q); an unsigned interrupt is a raw keystroke with extra steps", cmd.Sig, cmd.DeviceID)
	}

	// AND NO 0x03 RIDES THE INPUT PLANE: the keystroke ride is retired, not duplicated. A
	// Stop that both sealed the op and typed the byte would interrupt twice.
	time.Sleep(300 * time.Millisecond)
	h.Drain()
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, in := range h.Inputs {
		if in.Kind == "data" && bytes.Contains(in.Data, []byte{0x03}) {
			t.Errorf("PB-APP-3: Stop still sent 0x03 on the input plane beside the signed op; " +
				"the input-plane ride is superseded (Wave R6, M2.4)")
		}
	}
}

// TestPBAPP3_StopNeedsNoLeaseAndSaysSoWhenItCannotReachTheMachine.
//
// SUPERSESSION EXECUTED (Wave R6, same record). This was
// TestPBAPP3_StopWithoutALeaseDoesNotSilentlyDoNothing, pinning "PB-INPUT-2 refuses every
// keystroke until the machine has CONFIRMED a lease ... the refusal must name the lease,
// because the screen's remedy is to offer take-control rather than to retry". The signed op
// needs no lease -- the tuple's own signature is the authorization -- so the old refusal
// would now be a Stop refused for a precondition the op does not have. What is KEPT is the
// property the old test protected: a Stop that cannot act never silently succeeds.
func TestPBAPP3_StopNeedsNoLeaseAndSaysSoWhenItCannotReachTheMachine(t *testing.T) {
	h := s16ReconciledHarness(t)

	// No TakeControl anywhere: the signed op authorizes itself.
	if _, err := h.App.Interrupt(testSession, "01JTURN"); err != nil {
		t.Fatalf("Interrupt with no lease: %v -- the signed turn_interrupt op needs none", err)
	}
	if cmd := h.AwaitCommand("turn_interrupt"); cmd.Session != testSession {
		t.Errorf("turn_interrupt targeted %q, want %q", cmd.Session, testSession)
	}
}

// TestPBAPP3_AnOfflineStopRefusesLegiblyAndIsNeverReplayed.
//
// SUPERSESSION EXECUTED (Wave R6, same record). This was
// TestPBAPP3_AnOfflineStopResolvesAsUndeliveredAndIsNeverReplayed, whose middle pinned the
// input-plane ledger: "a Stop pressed with no connection left NO NEW undelivered record ...
// silence here is the silent drop PB-INPUT-1 forbids". The signed op is NOT a keystroke, so
// the undelivered-INPUT ledger is exactly where its failure must NOT land
// (mobile/r6_chatverbs_test.go pins the ledger stays empty); its refusal surfaces on the op
// itself, as a classed error the screen renders -- the 4lta pin (an offline Stop SAYS SO)
// preserved on the op plane. What is KEPT UNCHANGED is live-only: nothing is queued, and
// nothing arrives on the reconnected link (ADR-007 D7's teeth -- a Stop that lands ten
// minutes late interrupts whatever the agent is doing THEN).
func TestPBAPP3_AnOfflineStopRefusesLegiblyAndIsNeverReplayed(t *testing.T) {
	h := s16ReconciledHarness(t)

	// The link goes away. Stop is pressed. Nothing may be held for the reconnection.
	if err := h.App.Stop(); err != nil {
		t.Fatalf("App.Stop: %v", err)
	}

	before := s16UndeliveredCount(t, h)
	_, err := h.App.Interrupt(testSession, "01JTURN")
	if err == nil {
		t.Fatalf("PB-APP-3: an offline Stop returned success; the user watched a Stop button " +
			"work while the machine never heard it")
	}
	class, cerr := h.App.ErrorClass(err.Error())
	if cerr != nil {
		t.Fatalf("ErrorClass: %v", cerr)
	}
	if class == "" || class == "swarm/unknown" || class == "swarm/internal" {
		t.Errorf("PB-APP-3/PB-APP-9: the offline Stop's refusal classified as %q (%v); the user "+
			"is entitled to a legible state, not a bug report", class, err)
	}
	if got := s16UndeliveredCount(t, h); got != before {
		t.Errorf("Wave R6/M2.4: an offline Stop left %d new undelivered-INPUT record(s); the "+
			"signed op is not a keystroke and reports on itself, not on the keystroke ledger",
			got-before)
	}

	commandsBefore := len(h.Commands)
	sentBefore := len(h.Inputs)
	if err := h.App.Start(); err != nil {
		t.Fatalf("App.Start: %v", err)
	}
	if _, ok := awaitConnState(t, h.App, "online", 10*time.Second); !ok {
		t.Fatalf("the phone never reconnected")
	}
	time.Sleep(500 * time.Millisecond)
	h.Drain()
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.Commands[commandsBefore:] {
		if c.Action == "turn_interrupt" {
			t.Errorf("ADR-007 D7's spirit: a Stop pressed while offline arrived on the " +
				"RECONNECTED link as a signed op. It interrupts whatever the agent is doing now, " +
				"which is not what the user asked for and not what they are watching")
		}
	}
	for _, in := range h.Inputs[sentBefore:] {
		if in.Kind == "data" && bytes.Contains(in.Data, []byte{0x03}) {
			t.Errorf("a Stop pressed while offline arrived on the reconnected link as 0x03; " +
				"the input-plane ride is retired AND nothing may replay")
		}
	}
}

// PASSES TODAY, AND THAT IS NOT COVERAGE -- labelled so no evidence line can count it as
// earned. The facade already joins the daemon's lines with newlines and adds nothing, so this
// is a REGRESSION fence rather than a RED: it is here because PB-APP-4's criterion is a
// negative ("only sanitized text is rendered"), and the shape that would break it -- a
// re-wrap, a trim, a second sanitizer -- is one a screen author reaches for naturally once the
// grid is on a 6-inch display.
//
// TestPBAPP4_ThePeekRendersTheDaemonsBytesAndNothingElse.
//
// PB-APP-4's criterion is "asserts only sanitized text is rendered", and the honest reading is
// the one ADR-007 D2 forces: there is no VT emulator on the device, so the phone's obligation
// is to show what the daemon rendered, byte for byte. A phone that re-interpreted the bytes
// would be a second emulator; a phone that re-SANITIZED them would be silently editing a grid
// the daemon has already declared safe, and the two ends would disagree about what the user
// saw with nothing failing.
//
// The Kotlin half (SessionScreensTest) asserts the screen renders Snapshot.Text unchanged, and
// android/gate/s16_ui_test.go fences the absence of an escape parser on that side.
func TestPBAPP4_ThePeekRendersTheDaemonsBytesAndNothingElse(t *testing.T) {
	h := newHarness(t)

	lines := []string{
		"$ swarm status",
		"3 sessions, 1 needs input",
		"tab\tseparated and  double  spaced",
	}
	h.PushTerminal(testSession, lines, 80, 24)

	var snap struct {
		text string
		cols int
		rows int
	}
	eventually(t, "the phone never received the terminal snapshot", func() bool {
		s, err := h.App.Peek(testSession)
		if err != nil {
			return false
		}
		snap.text, snap.cols, snap.rows = s.Text, s.Cols, s.Rows
		return s.Text != ""
	})

	if want := strings.Join(lines, "\n"); snap.text != want {
		t.Errorf("PB-APP-4: Peek rendered\n\t%q\nwant the daemon's own lines joined by newlines\n\t%q\n"+
			"The phone holds no VT emulator (ADR-007 D2): re-wrapping, trimming or re-sanitizing "+
			"here makes the two ends disagree about what the user saw, with nothing failing",
			snap.text, want)
	}
	if snap.cols != 80 || snap.rows != 24 {
		t.Errorf("PB-APP-4: the grid geometry arrived as %dx%d, want 80x24. The screen sizes the "+
			"peek from these, so a wrong pair renders a correct grid at the wrong shape",
			snap.cols, snap.rows)
	}

	// The frame kind the phone accepted was a terminal snapshot and nothing else: PB-APP-4's
	// live tail is the peek plane, and a journal record rendered into the grid would be the
	// same class of confusion one channel over.
	page, err := h.App.ReadJournal(0, 0)
	if err != nil {
		t.Fatalf("ReadJournal: %v", err)
	}
	for i := 0; ; i++ {
		e, err := page.At(i)
		if err != nil {
			break
		}
		if strings.Contains(e.Type, "terminal") {
			t.Errorf("PB-APP-4: a terminal snapshot reached the JOURNAL read model as %q", e.Type)
		}
	}
}

// s16UndeliveredCount is the ledger depth right now.
func s16UndeliveredCount(t *testing.T, h *harness) int {
	t.Helper()
	list, err := h.App.UndeliveredInputs()
	if err != nil {
		t.Fatalf("UndeliveredInputs: %v", err)
	}
	n, err := list.Count()
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	return n
}
