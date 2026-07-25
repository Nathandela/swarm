package conformance_test

// Slice S10 at the FACADE: PB-SYNC-1 (a shared-bucket gap stales both channels it
// carries), PB-SYNC-3 (Resync is not an optimistic local clear) and PB-SYNC-6 (resync is
// bounded and non-amplifying, at §6.0's STATED rate).
//
// WHAT App.Resync DOES TODAY, in full:
//
//	func (a *App) Resync(stream string) error {
//	    a.mu.Lock(); delete(a.staleStrm, stream); a.mu.Unlock(); return nil
//	}
//
// It clears the flag, asks the machine for nothing, waits for nothing, and is free to call
// at any rate. Every criterion below is therefore a statement about a verb that currently
// only forgets.
//
// These tests DO use newHarness, and that is correct here rather than a fixture smell: the
// subject is staleness and repair, not key delivery, and the seeded epoch key is the
// premise the machine-sealed frames need. PB-KEY-10's fence -- which is exactly about the
// seeding -- lives in s10_bootstrap_test.go and shares none of this scaffolding.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// s10GapTheSharedBucket leaves the phone's shared (sender, epoch) bucket with a hole: two
// frames arrive from the real sink, then one arrives at a seq two ahead of them, so the seq
// in between is one the relay lost.
//
// THE DELIVERED FRAME IS A TERMINAL SNAPSHOT, DELIBERATELY. The skipped seq could equally
// have carried the journal record saying a session exited -- crypto.MailboxResult is
// {Plaintext, Gap bool} and nothing more, so the phone cannot know which. An implementation
// that reads the gap off the frame it arrived on therefore stales the wrong channel, and
// that is the whole of PB-SYNC-1.
func s10GapTheSharedBucket(t *testing.T, h *harness) {
	t.Helper()
	// Snapshot() publishes the reconcile record (seq 1) then the roster record (seq 2), so
	// the phone's high-water on this bucket is 2.
	h.PushRoster(schema.JournalRecord{SessionID: testMachineID + "/sess-gap", Type: "roster"})
	eventually(t, "the phone never received the pre-gap roster", func() bool {
		list, err := h.App.Roster()
		if err != nil {
			return false
		}
		n, _ := list.Count()
		return n > 0
	})

	// seq 3 is the frame the relay lost. seq 4 lands, on the same bucket, carrying a grid.
	plain, err := json.Marshal(struct {
		Kind string `json:"kind"`
		schema.TerminalSnapshot
	}{
		Kind: "terminal_snapshot",
		TerminalSnapshot: schema.TerminalSnapshot{
			Session: testMachineID + "/sess-gap", Lines: []string{"$ "}, Cols: 80, Rows: 24,
		},
	})
	if err != nil {
		t.Fatalf("marshal the post-gap snapshot: %v", err)
	}
	env, err := crypto.SealMailbox(h.Keys.ContentKey, crypto.EnvelopeHeader{
		Version:     crypto.VersionV1,
		EpochID:     h.EpochID,
		Seq:         4,
		SenderKeyID: h.senderKeyID,
		IssuedAt:    time.Now().UnixMilli(),
	}, plain)
	if err != nil {
		t.Fatalf("seal the post-gap snapshot: %v", err)
	}
	if _, err := h.machineRelay.MailboxAppend(h.ctx, h.phoneTarget, env.Marshal()); err != nil {
		t.Fatalf("append the post-gap snapshot: %v", err)
	}

	eventually(t, "the phone never observed the gap at all; every assertion resting on it is "+
		"vacuous", func() bool {
		state, err := h.App.StreamState("terminal")
		return err == nil && state == "stale"
	})
}

// TestS10_TheFacadeStalesBothChannelsOfAGappedBucket is PB-SYNC-1 at the surface. The gap
// was observed on a terminal snapshot, so mobile/relay.go's accept() marks only "terminal"
// -- but the frame the relay actually lost was a journal record, and the phone cannot know
// which it was. A roster shown as live over a hole is a killed session still on the screen.
func TestS10_TheFacadeStalesBothChannelsOfAGappedBucket(t *testing.T) {
	h := newHarness(t)
	s10GapTheSharedBucket(t, h)

	journal, err := h.App.StreamState("journal")
	if err != nil {
		t.Fatalf("StreamState(journal): %v", err)
	}
	if journal != "stale" {
		t.Errorf("after a gap in the SHARED bucket StreamState(%q) = %q, want \"stale\". Journal "+
			"and terminal ride ONE (sender, epoch) seq space and MailboxResult carries a bare Gap "+
			"bool with no frame kind, so attributing the hole to terminal -- because terminal is "+
			"what happened to arrive after it -- is exactly what PB-SYNC-1 calls a failing "+
			"implementation. mobile/relay.go accept() marks the stream of the frame it is holding",
			"journal", journal)
	}
	reply, err := h.App.StreamState("reply")
	if err != nil {
		t.Fatalf("StreamState(reply): %v", err)
	}
	if reply == "stale" {
		t.Errorf("a gap in the shared bucket staled the command-reply stream too. That is a " +
			"separate seq space (the deliberate SenderKeyID=0 split); staling it makes the " +
			"per-bucket tracking indistinguishable from one global flag")
	}
}

// TestS10_ResyncDoesNotClearStalenessBeforeTheRepairLands is PB-SYNC-3's optimistic-clearing
// hazard at the surface, and it is the sharpest one: the current implementation IS the
// defect, in one statement.
func TestS10_ResyncDoesNotClearStalenessBeforeTheRepairLands(t *testing.T) {
	h := newHarness(t)
	s10GapTheSharedBucket(t, h)

	if err := h.App.Resync("journal"); err != nil {
		t.Fatalf("App.Resync(journal): %v", err)
	}
	// Nothing has been republished. The machine has not been asked, or has been asked and
	// has not answered; either way the phone still has a hole it cannot see through.
	state, err := h.App.StreamState("journal")
	if err != nil {
		t.Fatalf("StreamState(journal): %v", err)
	}
	if state != "stale" {
		t.Errorf("StreamState(journal) = %q immediately after Resync, before any repair landed. "+
			"PB-SYNC-3: the flag clears only after a SUCCESSFUL reseed of that stream, committed "+
			"atomically with the matching transport watermark. Clearing on the request turns "+
			"'resync' into 'forget', and the user is then shown a hole as live -- which is the "+
			"one thing PB-APP-8 forbids", state)
	}
}

// TestS10_ResyncAsksTheMachine is the other half of the same defect: a repair that does not
// leave the device is not a repair. The verb is UNSIGNED (PB-SYNC-5's decision, recorded on
// schema.ActionJournalResync), so the machine sees it as an ordinary read command on the
// phone->machine bucket.
func TestS10_ResyncAsksTheMachine(t *testing.T) {
	h := newHarness(t)
	s10GapTheSharedBucket(t, h)

	if err := h.App.Resync("journal"); err != nil {
		t.Fatalf("App.Resync(journal): %v", err)
	}
	h.AwaitCommand(schema.ActionJournalResync)
}

// TestS10_ResyncIsRateBounded is PB-SYNC-6. The relay is the declared adversary and it
// controls exactly the input that triggers a resync -- it can withhold a frame whenever it
// likes -- so an unbounded resync is an amplifier the adversary holds the lever on. §6.0
// binds the number: <= 1 per stream per 5 s and <= 12 per 5 min. It is a STATED bound
// someone chose, not an emergent one, and it must be enforced rather than documented.
//
// The relay's own quota is not the backstop: mailbox_read and mailbox_ack meter against
// OpsPerMin 600 while mailbox_append does not, so a resync storm spends the budget the
// phone needs to RECEIVE the repair, and the connection dies with codeQuotaExceeded.
func TestS10_ResyncIsRateBounded(t *testing.T) {
	h := newHarness(t)
	s10GapTheSharedBucket(t, h)

	if err := h.App.Resync("journal"); err != nil {
		t.Fatalf("the first Resync: %v", err)
	}
	// Immediately again, well inside the 5-second per-stream window.
	second := h.App.Resync("journal")
	if second == nil {
		t.Errorf("a second Resync(journal) issued immediately after the first was accepted. " +
			"§6.0 binds the resync rate at <= 1 per stream per 5 s and <= 12 per 5 min; without " +
			"enforcement a relay that withholds one frame can drive the phone to republish the " +
			"whole roster as fast as the UI can call, and mailbox_read/mailbox_ack meter against " +
			"the same 600/min budget the repair itself has to arrive on")
	}

	// A DIFFERENT stream is a different budget: the bound is per stream, so repairing the
	// terminal must not be blocked by having just asked about the journal.
	if err := h.App.Resync("terminal"); err != nil {
		t.Errorf("Resync(terminal) was refused right after Resync(journal): %v. The bound is PER "+
			"STREAM; one shared budget makes the two channels able to starve each other, and a "+
			"gap stales BOTH of them at once", err)
	}
}

// TestS10_ANonAdvancingPageTerminates is PB-SYNC-6's other clause: a hostile relay must not
// be able to hold the drain on one page forever.
//
// LEGITIMATELY PASSING TODAY, and recorded as such rather than counted as earned. mobile/
// relay.go already processes every item in a page even when an earlier one cannot be opened,
// and the read cursor advances on the first frame that DOES commit -- so one planted
// undecodable item costs one re-read, not a permanent wedge. That is the property, and this
// is its REGRESSION FENCE: an implementation that stopped the sweep at the first
// unopenable item (which is what internal/phonesim's drain does, deliberately, for a
// GAPPED frame) would hand the relay exactly the denial lever PB-SYNC-6 forbids.
//
// It is NOT evidence for the bootstrap-frame stall, which is a different failure: there the
// unopenable item is at the HEAD and nothing behind it can commit either, so no frame ever
// advances the cursor. That case is fenced in s10_bootstrap_test.go.
func TestS10_ANonAdvancingPageTerminates(t *testing.T) {
	h := newHarness(t)

	// An item the phone can never open: it advances no cursor, so every subsequent read
	// re-serves it at the head of the page, forever.
	if _, err := h.machineRelay.MailboxAppend(h.ctx, h.phoneTarget, []byte("not an envelope at all")); err != nil {
		t.Fatalf("append the undecodable item: %v", err)
	}

	h.PushReconcile()
	h.PushRoster(schema.JournalRecord{SessionID: testMachineID + "/sess-behind", Type: "roster"})

	eventually(t, "the frames behind an undecodable mailbox item never reached the phone. A "+
		"relay that plants ONE item the phone cannot open pins the read cursor at it and every "+
		"later frame is unreachable for the whole 7-day retention window -- unbounded denial "+
		"driven by the party PB-SYNC-6 declares hostile, with nothing on the screen to say so",
		func() bool {
			list, err := h.App.Roster()
			if err != nil {
				return false
			}
			n, _ := list.Count()
			return n > 0
		})
}
