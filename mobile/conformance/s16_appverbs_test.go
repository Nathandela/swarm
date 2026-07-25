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

// TestPBAPP3_StopIsTheInterruptKeystrokeOnTheLiveLease.
//
// THE RESOLUTION THIS ENCODES, recorded 2026-07-25 and not obvious from the requirement text.
// PB-APP-3's "persistent Stop" had no wire verb: the signed action set defines launch, kill,
// delete, approve, device_revoke, take_control, terminal_watch, terminal_unwatch and
// push_prefs, and there is no interrupt anywhere. Minting one was rejected -- a new signed
// action changes what requireRemoteAuthz accepts, needs its own authz tuple, its own biometric
// tier and its own replay story, all to duplicate a capability the input plane already
// delivers. An interrupt IS a keystroke: Ctrl-C is byte 0x03 and a PTY in its default ISIG
// mode turns it into SIGINT for the foreground process group, which is exactly how a human
// stops a running agent. So Stop is: hold the lease, send 0x03, with kill as the escalation.
//
// S8's facade correctly refused to INVENT a verb and records a durable local refusal instead
// (App.Interrupt -> a.refuse). That was right while no resolution existed and is wrong now:
// the button is on the screen and it does nothing.
func TestPBAPP3_StopIsTheInterruptKeystrokeOnTheLiveLease(t *testing.T) {
	h := s16ReconciledHarness(t)

	if _, err := h.App.TakeControl(testSession); err != nil {
		t.Fatalf("TakeControl: %v", err)
	}
	h.AwaitLease(testSession)

	if _, err := h.App.Interrupt(testSession); err != nil {
		t.Fatalf("Interrupt on a held lease: %v", err)
	}

	// The 125 ms coalescing window holds a keystroke briefly; the drain timer releases it.
	deadline := time.Now().Add(5 * time.Second)
	found := false
	for time.Now().Before(deadline) && !found {
		h.Drain()
		h.mu.Lock()
		for _, in := range h.Inputs {
			if in.Kind == "data" && bytes.Contains(in.Data, []byte{0x03}) {
				found = true
				break
			}
		}
		h.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	if !found {
		t.Fatalf("PB-APP-3: Stop sent no 0x03 to the machine.\n" +
			"App.Interrupt records a local refusal -- \"interrupt has no signed wire action; the " +
			"verb is owed by another slice\" -- which was correct while no resolution existed and " +
			"is stale now: an interrupt IS a keystroke (Ctrl-C, 0x03, through a PTY in ISIG mode), " +
			"the phone already has take_control -> data_in, and minting a new signed action to " +
			"duplicate that would change the signed action set for nothing. Today the persistent " +
			"Stop button is on the screen and does nothing.")
	}

	// AND NO NEW SIGNED ACTION WAS MINTED. The whole point of the resolution is that the
	// signed set does not change; a command bearing an invented action would be refused by
	// the daemon's closed capability switch while looking, to the app, exactly like one that
	// was delivered.
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.Commands {
		if strings.Contains(c.Action, "interrupt") {
			t.Errorf("PB-APP-3: a %q command reached the machine. actionClass is a closed "+
				"fail-closed switch (internal/skeleton/deviceauth.go); an action it does not know "+
				"is refused one hop short of the daemon, and a refused action seals no reply, so "+
				"the op never resolves and Stop hangs forever", c.Action)
		}
	}
}

// TestPBAPP3_StopWithoutALeaseDoesNotSilentlyDoNothing.
//
// PB-INPUT-2 refuses every keystroke until the machine has CONFIRMED a lease, so an ungated
// Stop is a button that appears to work and changes nothing. The screen's answer is to show
// the take-control step (the Kotlin half asserts that); the facade's answer is to REFUSE
// legibly rather than to return success.
func TestPBAPP3_StopWithoutALeaseDoesNotSilentlyDoNothing(t *testing.T) {
	h := s16ReconciledHarness(t)

	_, err := h.App.Interrupt(testSession)
	if err == nil {
		t.Fatalf("PB-APP-3/PB-INPUT-2: Stop with no confirmed lease returned success. The " +
			"gateway drops an input frame from a device holding no lease SILENTLY, so the user " +
			"watched a Stop button work and the agent kept running")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "lease") {
		t.Errorf("PB-APP-3: Stop without a lease failed with %v; the refusal must name the "+
			"lease, because the screen's remedy is to offer take-control rather than to retry", err)
	}
}

// TestPBAPP3_AnOfflineStopResolvesAsUndeliveredAndIsNeverReplayed.
//
// ADR-007 D7: input is live-only, never queued, never replayed. A Stop is the case where that
// rule has teeth -- a keystroke that arrives ten minutes late types a character, and a Stop
// that arrives ten minutes late interrupts whatever the agent is doing THEN, after the user
// gave up and started something else.
func TestPBAPP3_AnOfflineStopResolvesAsUndeliveredAndIsNeverReplayed(t *testing.T) {
	h := s16ReconciledHarness(t)
	if _, err := h.App.TakeControl(testSession); err != nil {
		t.Fatalf("TakeControl: %v", err)
	}
	h.AwaitLease(testSession)

	// The link goes away. Stop is pressed. Nothing may be held for the reconnection.
	if err := h.App.Stop(); err != nil {
		t.Fatalf("App.Stop: %v", err)
	}

	// MEASURED AS A DELTA, and it has to be. App.Stop itself calls suspendInput, which empties
	// the coalescer and resolves whatever it held as undelivered -- including the empty probe
	// AwaitLease types. A test that read the ledger's absolute count would therefore be green
	// today, for a Stop button that records nothing at all: the pass would come from the
	// teardown of the previous step. This is standing defect class (iii) in miniature.
	before := s16UndeliveredCount(t, h)
	_, _ = h.App.Interrupt(testSession)
	if s16UndeliveredCount(t, h) == before {
		t.Errorf("PB-INPUT-1/PB-APP-3: a Stop pressed with no connection left NO NEW undelivered "+
			"record (ledger stayed at %d). The user is entitled to be told the agent was not "+
			"stopped; silence here is the silent drop PB-INPUT-1 forbids, on the one control "+
			"where it matters most", before)
	}

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
	after := h.Inputs[sentBefore:]
	h.mu.Unlock()
	for _, in := range after {
		if in.Kind == "data" && bytes.Contains(in.Data, []byte{0x03}) {
			t.Errorf("ADR-007 D7: a Stop pressed while offline arrived on the RECONNECTED link. " +
				"It interrupts whatever the agent is doing now, which is not what the user asked " +
				"for and not what they are watching")
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
