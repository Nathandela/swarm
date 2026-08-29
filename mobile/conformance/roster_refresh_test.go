package conformance_test

import (
	"testing"

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
	if cmd.ResyncCursor != 0 {
		t.Fatalf("RefreshRoster cursor = %d, want the phone's prior zero cursor", cmd.ResyncCursor)
	}
}
