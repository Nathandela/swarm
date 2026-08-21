package remotegw

// Bead agents-tracker-hggx.4.4 -- FAILING-FIRST (TDD RED, GG-5) tests for the PG-OBL-2
// deferred-wake crash gap (docs/specifications/push-gateway-api.md section 6.1, ADR-010
// section 4(b), PB-PUSH-8).
//
// THE GAP, restated from the 2026-08-15 residual in
// docs/verification/r3-red/obligations-red.txt: wouldWakeNow (push.go) deliberately
// reports false for a window-suppressed interaction wake, so preAppendObligation records
// nothing for it -- the mailbox record is published immediately and the obligation is
// appended only when the single deferral timer fires, up to 30s later. A crash inside
// that window publishes an event with no durable obligation announcing it: exactly
// PG-OBL-2's "a crash in that gap publishes an event the phone is never told about",
// narrower than the immediate-wake case agents-tracker-hggx.4.2 closed.
//
// THE RULING these tests pin (orchestrator, option (a) of the bead): the deferred case
// pre-appends a PROVISIONAL durable obligation at trigger time, and the timer's send()
// SUPERSEDES it -- durably and honestly -- if the preference re-read at send (PB-PUSH-8)
// has flipped off. "Honestly" means the record must never claim a send that did not
// happen: it must not read `delivered`/`provider_accepted`, and it must not stay
// non-terminal (a later redrive would then submit a wake the preference forbids).
//
// These tests exercise ONLY symbols that already exist (PushNotifier, TransportRouter,
// WakeObligationMachine, the file-backed stores); they fail at ASSERTION level against
// the current implementation because the provisional pre-append does not exist yet.
// This file contains NO implementation.

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
)

// deferredGapHarness wires the real gateway-transport push path -- PushNotifier ->
// TransportRouter -> WakeObligationMachine -- over FILE-BACKED obligation and wake_seq
// stores, so a crash can be simulated the way the crash-matrix tests model it: abandon
// every live object and reopen the same durable files.
type deferredGapHarness struct {
	obPath  string
	seqPath string
	addr    PushAddress
	clk     *testClock
	ft      *fakeTimer
	sub     *fakeSubmitter
	prefs   *stubPrefs
	sink    *recordingSink
	store   ObligationStore
	n       *PushNotifier
}

func newDeferredGapHarness(t *testing.T) *deferredGapHarness {
	t.Helper()
	dir := t.TempDir()
	h := &deferredGapHarness{
		obPath:  filepath.Join(dir, "wake-obligations.json"),
		seqPath: filepath.Join(dir, "wake-seq.json"),
		addr:    testPushAddress(0x4A),
		clk:     newTestClock(),
		ft:      &fakeTimer{},
		sub:     &fakeSubmitter{},
		prefs:   &stubPrefs{prefs: PushPrefs{Version: 1, NeedsInput: true, Finished: true}},
		sink:    &recordingSink{},
	}
	store, err := OpenObligationStore(h.obPath)
	if err != nil {
		t.Fatalf("OpenObligationStore: %v", err)
	}
	h.store = store
	wakeSeq, err := OpenSeqSource(h.seqPath)
	if err != nil {
		t.Fatalf("OpenSeqSource(wake): %v", err)
	}
	machine := NewWakeObligationMachine(WakeObligationConfig{
		Store: store, Submitter: h.sub, WakeKey: testWakeKey(), Address: h.addr,
		Seq: wakeSeq, Now: h.clk.Now,
	})
	ts, err := OpenTransportStore("")
	if err != nil {
		t.Fatalf("OpenTransportStore: %v", err)
	}
	if err := ts.SetTransport(TransportGateway); err != nil {
		t.Fatalf("SetTransport(gateway): %v", err)
	}
	pushSeq, err := OpenSeqSource("")
	if err != nil {
		t.Fatalf("OpenSeqSource(push): %v", err)
	}
	h.n = NewPushNotifier(h.sink, PushConfig{
		Pusher: &TransportRouter{Transport: ts, Gateway: machine},
		Target: "phone-routing-id", WakeKey: testWakeKey(), EpochID: 7,
		Now: h.clk.Now, Seq: pushSeq, Prefs: h.prefs, After: h.ft.after,
	})
	return h
}

// interaction delivers one bare interaction journal record, exactly as the ADR-010
// approval tests do: no Group, coarse wire type `interaction`.
func (h *deferredGapHarness) interaction(t *testing.T, cursor uint64, s string) {
	t.Helper()
	if err := h.n.Event(protocol.JournalRecord{
		Cursor: cursor, SessionID: s, Type: "interaction",
		Item: []byte(`{"v":1,"item_id":"ap` + string(rune('0'+cursor)) + `","ts":"2026-08-16T12:00:00Z","kind":"approval_request","summary":"Bash: rm -rf build"}`),
	}); err != nil {
		t.Fatalf("Event(interaction, cursor %d): %v", cursor, err)
	}
}

// deferOneInteractionWake drives the harness to the state every test in this file
// starts from: interaction #1 wakes immediately (claiming the session's 30s window and
// delivering an obligation), and interaction #2 five seconds later is window-suppressed
// and DEFERRED (ADR-010 section 4(b)) -- one timer armed for the window's remaining 25s,
// its mailbox record already published. It returns the delivered first obligation's
// wake_seq so a test can tell the provisional record apart from it.
func (h *deferredGapHarness) deferOneInteractionWake(t *testing.T) (firstSeq uint64) {
	t.Helper()
	h.interaction(t, 1, "m/s1")
	if got := len(h.sub.all()); got != 1 {
		t.Fatalf("setup: interaction #1 produced %d gateway submits, want 1 (the immediate wake)", got)
	}
	first, ok, err := h.store.Get(h.addr)
	if err != nil || !ok {
		t.Fatalf("setup: no durable obligation after the immediate wake: ok=%v err=%v", ok, err)
	}
	if first.State != ObligationDelivered {
		t.Fatalf("setup: first obligation state = %q, want %q", first.State, ObligationDelivered)
	}
	h.clk.advance(5 * time.Second)
	h.interaction(t, 2, "m/s1")
	if got := len(h.sub.all()); got != 1 {
		t.Fatalf("setup: interaction #2 inside the window produced a submit (total %d), want it suppressed and deferred", got)
	}
	if got := len(h.ft.scheduled()); got != 1 {
		t.Fatalf("setup: %d deferral timers armed, want exactly 1 (ADR-010 section 4(b))", got)
	}
	if got := h.sink.eventCount(); got != 2 {
		t.Fatalf("setup: %d mailbox records published, want 2 -- the deferred wake's record is published NOW, ahead of its wake", got)
	}
	return first.WakeSeq
}

// TestOBL2_DeferredInteractionWakePreAppendsADurableProvisionalObligation is the trigger
// half of the ruling: the moment a window-suppressed interaction wake is deferred, a
// PROVISIONAL obligation for the address must already be durable -- fresh wake_seq
// (the prior record is terminal and PG-OBL-5's coalescing cannot reach it), sealed
// 74-byte WakeV1 envelope (PG-WAKE-12: sealed once, at obligation creation), non-terminal
// state. Today wouldWakeNow reports false for the deferred case and nothing is recorded
// until the timer fires up to 30s later.
func TestOBL2_DeferredInteractionWakePreAppendsADurableProvisionalObligation(t *testing.T) {
	h := newDeferredGapHarness(t)
	firstSeq := h.deferOneInteractionWake(t)

	ob, ok, err := h.store.Get(h.addr)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if !ok || (ob.State != ObligationPending && ob.State != ObligationInFlight) {
		t.Fatalf("durable obligation after a DEFERRED interaction wake: ok=%v state=%q, want a non-terminal "+
			"PROVISIONAL record -- PG-OBL-2 requires the obligation recorded before or atomically with the "+
			"publish, and the deferred path records nothing until the timer fires (bd agents-tracker-hggx.4.4)",
			ok, ob.State)
	}
	if ob.WakeSeq == firstSeq {
		t.Fatalf("provisional obligation reports wake_seq %d, the already-delivered first wake's: it must be a "+
			"FRESH mint (the prior record is terminal, so PG-OBL-5's coalescing cannot apply)", ob.WakeSeq)
	}
	if len(ob.Envelope) != WakeV1Size {
		t.Fatalf("provisional obligation's sealed envelope is %d bytes, want the pinned WakeV1Size %d (PG-WAKE-12: "+
			"sealed once, at obligation creation)", len(ob.Envelope), WakeV1Size)
	}
}

// TestOBL2_CrashInsideTheDeferralWindowStillWakesThePhoneAfterRestart is the crash gap
// itself, actually simulated: the process dies BETWEEN the mailbox publish and the
// deferral timer's fire (the timer is simply never fired; every live object is
// abandoned), a fresh machine is reopened over the SAME durable files, and the restart
// re-drive (PG-OBL-8) must deliver the provisional wake -- so the phone learns of the
// published event. Today nothing durable exists for the deferred wake, so the restart
// finds nothing to re-drive and the event is silently lost.
func TestOBL2_CrashInsideTheDeferralWindowStillWakesThePhoneAfterRestart(t *testing.T) {
	h := newDeferredGapHarness(t)
	h.deferOneInteractionWake(t)

	// CRASH. The deferral timer never fires; h's notifier, router and machine are never
	// touched again. Only the durable files survive.
	store2, err := OpenObligationStore(h.obPath)
	if err != nil {
		t.Fatalf("reopen ObligationStore after the crash: %v", err)
	}
	pending, err := store2.Pending()
	if err != nil {
		t.Fatalf("Pending after the crash: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("crash gap reproduced: %d non-terminal obligations survived a crash inside the deferral "+
			"window, want 1 -- the published interaction record (cursor 2) is an event the phone is never "+
			"told about (PG-OBL-2, bd agents-tracker-hggx.4.4)", len(pending))
	}

	seq2, err := OpenSeqSource(h.seqPath)
	if err != nil {
		t.Fatalf("reopen SeqSource after the crash: %v", err)
	}
	sub2 := &fakeSubmitter{}
	machine2 := NewWakeObligationMachine(WakeObligationConfig{
		Store: store2, Submitter: sub2, WakeKey: testWakeKey(), Address: h.addr,
		Seq: seq2, Now: h.clk.Now,
	})
	if err := machine2.Drive(context.Background()); err != nil {
		t.Fatalf("restart re-drive: %v", err)
	}
	sent := sub2.all()
	if len(sent) != 1 {
		t.Fatalf("restart re-drive submitted %d wakes, want 1: the phone must learn of the event the crash orphaned", len(sent))
	}
	if !bytes.Equal(sent[0], pending[0].Envelope) {
		t.Fatalf("the re-driven wake is not the byte-identical stored envelope (PG-WAKE-12): a retry replays the sealed bytes verbatim")
	}
	if len(sent[0]) != WakeV1Size || !bytes.Equal(sent[0][2:18], h.addr[:]) {
		t.Fatalf("the re-driven wake is not a WakeV1 for the configured address: len=%d", len(sent[0]))
	}
}

// TestOBL2_PreferenceFlipDuringDeferralSupersedesTheProvisionalObligationHonestly is the
// supersede half of the ruling. The deferral timer re-reads the push preference AT SEND
// (ADR-010 section 4(b), PB-PUSH-8); when it has flipped off, no wake may leave the
// machine (zero provider calls is what "disabled" means) -- and the provisional
// obligation must then be superseded DURABLY and HONESTLY: same record (same wake_seq),
// terminal (a later redrive must not resurrect a send the preference forbids), and never
// claiming a delivery that did not happen.
func TestOBL2_PreferenceFlipDuringDeferralSupersedesTheProvisionalObligationHonestly(t *testing.T) {
	h := newDeferredGapHarness(t)
	h.deferOneInteractionWake(t)

	prov, ok, err := h.store.Get(h.addr)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if !ok || (prov.State != ObligationPending && prov.State != ObligationInFlight) {
		t.Fatalf("precondition missing: no non-terminal PROVISIONAL obligation was pre-appended for the "+
			"deferred wake (ok=%v state=%q) -- the deferred path records nothing at trigger time yet "+
			"(bd agents-tracker-hggx.4.4)", ok, prov.State)
	}

	// The user flips needs_input OFF while the deferral is pending.
	if err := h.prefs.SavePrefs(PushPrefs{Version: 1, NeedsInput: false, Finished: true}); err != nil {
		t.Fatalf("SavePrefs: %v", err)
	}
	h.clk.advance(DefaultPushWindow - 5*time.Second)
	h.ft.fire(t, 0)

	if got := len(h.sub.all()); got != 1 {
		t.Fatalf("gateway submits after the preference flipped off = %d, want 1 (only the first wake): "+
			"PB-PUSH-8 means ZERO calls for a disabled category, verified at the sender", got)
	}
	cur, ok, err := h.store.Get(h.addr)
	if err != nil {
		t.Fatalf("store.Get after the timer fired: %v", err)
	}
	if !ok {
		t.Fatal("the provisional obligation vanished: superseding must be a durable, honest record, never a delete")
	}
	if cur.WakeSeq != prov.WakeSeq {
		t.Fatalf("record for the address is wake_seq %d, want the provisional %d superseded IN PLACE: the "+
			"supersede cancels the provisional record, it does not mint a replacement", cur.WakeSeq, prov.WakeSeq)
	}
	if cur.State == ObligationPending || cur.State == ObligationInFlight {
		t.Fatalf("provisional obligation is still %q after the send was cancelled: a later redrive would "+
			"submit a wake the preference forbids -- the supersede must land it in a terminal state", cur.State)
	}
	if cur.State == ObligationDelivered || cur.LastOutcome == "provider_accepted" {
		t.Fatalf("superseded obligation claims a send that never happened (state=%q outcome=%q): the record "+
			"must stay honest", cur.State, cur.LastOutcome)
	}
	if cur.LastOutcome == "" {
		t.Fatalf("superseded obligation carries no last-outcome word at all: the record must SAY why it " +
			"ended (PG-OBL-1 persists the last outcome code)")
	}

	// The supersede must itself be durable: a restart over the same store finds nothing
	// to re-drive and submits nothing.
	store2, err := OpenObligationStore(h.obPath)
	if err != nil {
		t.Fatalf("reopen ObligationStore after the supersede: %v", err)
	}
	pending, err := store2.Pending()
	if err != nil {
		t.Fatalf("Pending after the supersede: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("%d non-terminal obligations survive the supersede on disk, want 0: the cancellation must be durable", len(pending))
	}
	seq2, err := OpenSeqSource(h.seqPath)
	if err != nil {
		t.Fatalf("reopen SeqSource: %v", err)
	}
	sub2 := &fakeSubmitter{}
	machine2 := NewWakeObligationMachine(WakeObligationConfig{
		Store: store2, Submitter: sub2, WakeKey: testWakeKey(), Address: h.addr,
		Seq: seq2, Now: h.clk.Now,
	})
	if err := machine2.Drive(context.Background()); err != nil {
		t.Fatalf("Drive after restart: %v", err)
	}
	if got := len(sub2.all()); got != 0 {
		t.Fatalf("a restart re-drove the superseded obligation (%d submits, want 0): the cancellation did not hold durably", got)
	}
}

// TestOBL2_TimerFireWithPreferenceStillOnDrivesTheProvisionalObligation is the
// positive control that keeps the supersede test honest: when the preference has NOT
// flipped, the timer's send() must deliver exactly the provisional obligation's sealed
// bytes -- the deferred send coalesces into the provisional record (PG-OBL-5), it does
// not mint a second wake for the same hand-off.
func TestOBL2_TimerFireWithPreferenceStillOnDrivesTheProvisionalObligation(t *testing.T) {
	h := newDeferredGapHarness(t)
	h.deferOneInteractionWake(t)

	prov, ok, err := h.store.Get(h.addr)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if !ok || (prov.State != ObligationPending && prov.State != ObligationInFlight) {
		t.Fatalf("precondition missing: no non-terminal PROVISIONAL obligation was pre-appended for the "+
			"deferred wake (ok=%v state=%q) (bd agents-tracker-hggx.4.4)", ok, prov.State)
	}

	h.clk.advance(DefaultPushWindow - 5*time.Second)
	h.ft.fire(t, 0)

	sent := h.sub.all()
	if len(sent) != 2 {
		t.Fatalf("gateway submits after the deferred send = %d, want 2 (the immediate wake plus the deferred one)", len(sent))
	}
	if !bytes.Equal(sent[1], prov.Envelope) {
		t.Fatalf("the deferred send is not the provisional obligation's sealed bytes: it must coalesce into " +
			"the provisional record (PG-OBL-5) and replay its envelope verbatim (PG-WAKE-12), not mint a second wake")
	}
	cur, ok, err := h.store.Get(h.addr)
	if err != nil || !ok {
		t.Fatalf("store.Get after the deferred send: ok=%v err=%v", ok, err)
	}
	if cur.State != ObligationDelivered {
		t.Fatalf("provisional obligation after the deferred send = %q, want %q", cur.State, ObligationDelivered)
	}
}
