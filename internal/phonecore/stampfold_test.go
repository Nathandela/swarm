package phonecore

// FAILING-FIRST (TDD RED, GG-5) for phone-refit-playbook W7.1: the session cache folds the
// machine's state-since stamp VERBATIM by the rule Group, Agent and Name already follow -- a
// record carrying one sets it, a record carrying none leaves it alone, and the phone derives
// nothing from its own clock.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/status"
)

func TestCacheAppliesStateSinceVerbatim(t *testing.T) {
	c := NewSessionCache()
	last := time.Date(2026, 8, 28, 9, 34, 0, 0, time.UTC)

	c.Apply(schema.JournalRecord{SessionID: "m/s1", Type: "roster", Group: status.Group("working"), StateSince: last})
	if cs, _ := c.Get("m/s1"); !cs.StateSince.Equal(last) {
		t.Fatalf("StateSince after the roster = %v; want %v applied verbatim", cs.StateSince, last)
	}

	c.Apply(schema.JournalRecord{Cursor: 12, SessionID: "m/s1", Type: "group_transition", Group: status.Group("needs_input")})
	if cs, _ := c.Get("m/s1"); !cs.StateSince.Equal(last) {
		t.Errorf("a record carrying no stamp changed StateSince to %v; want %v kept -- absence is not a stamp", cs.StateSince, last)
	}

	later := last.Add(4 * time.Minute)
	c.Apply(schema.JournalRecord{Cursor: 13, SessionID: "m/s1", Type: "group_transition", Group: status.Group("working"), StateSince: later})
	if cs, _ := c.Get("m/s1"); !cs.StateSince.Equal(later) {
		t.Errorf("a later stamp did not replace the earlier one: %v; want %v", cs.StateSince, later)
	}

	c.Apply(schema.JournalRecord{SessionID: "m/s2", Type: "roster", Group: status.Group("working")})
	if cs, _ := c.Get("m/s2"); !cs.StateSince.IsZero() {
		t.Errorf("a session whose records carried no stamp has StateSince %v; want the zero time", cs.StateSince)
	}
}

// TestCachedSessionUnstampedBytesAreUnchanged is the durable-state half of the byte-identity
// NEGATIVE CONTROL (W7 ruling, constraint 2): the literal is what main@1a0e7b29 persisted for this
// session, measured there. A session no record has stamped must persist to exactly those bytes,
// so a rollback build reads this build's state as its own; drop `omitzero` and this fails.
func TestCachedSessionUnstampedBytesAreUnchanged(t *testing.T) {
	b, err := json.Marshal(CachedSession{SessionID: "m/s1", Group: status.Group("working"), Agent: "claude", Name: "api", Present: true})
	if err != nil {
		t.Fatal(err)
	}
	const before = `{"SessionID":"m/s1","Group":"working","Agent":"claude","Name":"api","Present":true,"Capabilities":null}`
	if string(b) != before {
		t.Errorf("an unstamped cached session persists as\n  %s\nwant the bytes main@1a0e7b29 wrote\n  %s", b, before)
	}
}

// TestPersistedCacheWithoutTheStampStillLoads: a durable state written before the field existed
// (the literal is main@1a0e7b29's own output, inside the container that carries sessions) loads
// with every field it had and a zero stamp -- the additive-field rule in the loading direction.
func TestPersistedCacheWithoutTheStampStillLoads(t *testing.T) {
	const persisted = `{"sessions":[{"SessionID":"m/s1","Group":"working","Agent":"claude","Name":"api","Present":true,"Capabilities":null}]}`
	var c purgeableContainer
	if err := json.Unmarshal([]byte(persisted), &c); err != nil {
		t.Fatalf("a pre-stamp durable state no longer loads: %v", err)
	}
	if len(c.Sessions) != 1 {
		t.Fatalf("sessions = %d; want 1", len(c.Sessions))
	}
	cs := c.Sessions[0]
	if cs.SessionID != "m/s1" || cs.Group != status.Group("working") || cs.Agent != "claude" || cs.Name != "api" || !cs.Present {
		t.Errorf("a pre-stamp session lost a field on load: %+v", cs)
	}
	if !cs.StateSince.IsZero() {
		t.Errorf("StateSince = %v for a session persisted before the field existed; want the zero time", cs.StateSince)
	}
}
