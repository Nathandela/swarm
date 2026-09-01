package conformance_test

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// TestS10_RefreshRosterAsksForRosterOnly is the pull-refresh contract. It uses the
// existing sealed unsigned journal read but marks the answer as roster-only, so a phone
// at cursor zero does not ask the gateway to aggregate a multi-megabyte transcript into
// the relay's one-frame journal_reseed.
func TestS10_RefreshRosterAsksForRosterOnly(t *testing.T) {
	h := newHarness(t)
	if err := h.App.RefreshRoster(); err != nil {
		t.Fatalf("App.RefreshRoster: %v", err)
	}
	cmd := h.AwaitCommand(schema.ActionJournalResync)
	if !cmd.RosterOnly {
		t.Fatalf("RefreshRoster command = %#v, want roster_only=true", cmd)
	}
	if cmd.DiscardedBacklog {
		t.Fatalf("healthy RefreshRoster command = %#v, want discarded_backlog omitted", cmd)
	}
	if cmd.ResyncCursor != 0 {
		t.Fatalf("RefreshRoster cursor = %d, want the phone's prior zero cursor", cmd.ResyncCursor)
	}
}

// Automatic foreground/network anti-entropy is deliberately weaker than the visible Inbox
// Reload. A background lifecycle transition did not authorize deletion, so even an authenticated
// stale-age head must remain in place for the user-visible, generation-fenced recovery path.
func TestS10_SyncRosterNeverDiscardsAnUnreachableMailbox(t *testing.T) {
	h := newHarness(t)
	eventually(t, "the phone never came online", func() bool {
		state, err := h.App.ConnectionState()
		return err == nil && state == "online"
	})

	h.SealOffset(-phonecore.InboundMaxAge - time.Minute)
	h.PushEvent(schema.JournalRecord{Cursor: 1, SessionID: testMachineID + "/old", Type: "launched"})
	eventually(t, "the stale-age head never raised the inbound refusal", func() bool {
		state, err := h.App.ConnectionState()
		return err == nil && state == "offline"
	})

	if err := h.App.SyncRoster(); err != nil {
		t.Fatalf("App.SyncRoster: %v", err)
	}
	cmd := h.AwaitCommand(schema.ActionJournalResync)
	if !cmd.RosterOnly || cmd.DiscardedBacklog || cmd.DiscardRecoveryToken != "" {
		t.Fatalf("automatic SyncRoster command = %#v, want roster_only with no discard authority", cmd)
	}
	if state, err := h.App.ConnectionState(); err != nil || state != "offline" {
		t.Fatalf("SyncRoster changed stale retained mailbox state to %q, %v; automatic convergence must not compact it", state, err)
	}
}
