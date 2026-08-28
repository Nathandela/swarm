package schema

// FAILING-FIRST (TDD RED, GG-5) for phone-refit-playbook W7.1 / W7.4: JournalRecord gains `ts`
// and `last_activity`. Both are additive and omitted when zero, so every record type that
// predates them serialises byte-identically to what earlier builds wrote (the rule the type's
// own doc states for every field but cursor, session_id and type).

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/status"
)

func TestJournalRecordOmitsZeroStamps(t *testing.T) {
	b, err := json.Marshal(JournalRecord{Cursor: 3, SessionID: "m/s1", Type: "launched", Group: status.Group("working")})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"ts"`, `"last_activity"`} {
		if strings.Contains(string(b), key) {
			t.Errorf("a record with no stamp serialised %s: %s -- a zero stamp must be absent, never the epoch", key, b)
		}
	}
}

func TestJournalRecordRoundTripsStamps(t *testing.T) {
	ts := time.Date(2026, 8, 28, 9, 38, 0, 0, time.UTC)
	last := time.Date(2026, 8, 28, 9, 34, 0, 0, time.UTC)
	b, err := json.Marshal(JournalRecord{SessionID: "m/s1", Type: "roster", TS: ts, LastActivity: last})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"ts":`, `"last_activity":`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("a stamped record did not serialise %s: %s", key, b)
		}
	}
	var back JournalRecord
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !back.TS.Equal(ts) || !back.LastActivity.Equal(last) {
		t.Errorf("round trip = ts %v last_activity %v; want %v and %v", back.TS, back.LastActivity, ts, last)
	}
}
