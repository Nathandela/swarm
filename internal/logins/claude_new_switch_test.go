package logins

import (
	"errors"
	"testing"
)

func TestClaudeNewFleetJournalIsRejectedBeforeOwnedMode(t *testing.T) {
	s := testStore(t)
	a := add(t, s, ProviderClaude, "A", claudeCredential("a", "grant"))
	r := snapshot(t, s)
	if _, err := s.BeginSwitch(r.Revision, ProviderClaude, a.ID, "new action", nil); !errors.Is(err, ErrPending) {
		t.Fatalf("new Claude fleet journal: %v", err)
	}
	if len(snapshot(t, s).Switches) != 0 {
		t.Fatal("new Claude journal persisted")
	}
}
