package phonecore

// FAILING-FIRST (TDD RED, GG-5) for phone-refit-playbook W7.1: the session cache folds the
// machine's last-activity stamp VERBATIM by the rule Group, Agent and Name already follow -- a
// record carrying one sets it, a record carrying none leaves it alone, and the phone derives
// nothing from its own clock.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/status"
)

func TestCacheAppliesLastActivityVerbatim(t *testing.T) {
	c := NewSessionCache()
	last := time.Date(2026, 8, 28, 9, 34, 0, 0, time.UTC)

	c.Apply(schema.JournalRecord{SessionID: "m/s1", Type: "roster", Group: status.Group("working"), LastActivity: last})
	if cs, _ := c.Get("m/s1"); !cs.LastActivity.Equal(last) {
		t.Fatalf("LastActivity after the roster = %v; want %v applied verbatim", cs.LastActivity, last)
	}

	c.Apply(schema.JournalRecord{Cursor: 12, SessionID: "m/s1", Type: "group_transition", Group: status.Group("needs_input")})
	if cs, _ := c.Get("m/s1"); !cs.LastActivity.Equal(last) {
		t.Errorf("a record carrying no stamp changed LastActivity to %v; want %v kept -- absence is not a stamp", cs.LastActivity, last)
	}

	later := last.Add(4 * time.Minute)
	c.Apply(schema.JournalRecord{Cursor: 13, SessionID: "m/s1", Type: "group_transition", Group: status.Group("working"), LastActivity: later})
	if cs, _ := c.Get("m/s1"); !cs.LastActivity.Equal(later) {
		t.Errorf("a later stamp did not replace the earlier one: %v; want %v", cs.LastActivity, later)
	}

	c.Apply(schema.JournalRecord{SessionID: "m/s2", Type: "roster", Group: status.Group("working")})
	if cs, _ := c.Get("m/s2"); !cs.LastActivity.IsZero() {
		t.Errorf("a session whose records carried no stamp has LastActivity %v; want the zero time", cs.LastActivity)
	}
}
