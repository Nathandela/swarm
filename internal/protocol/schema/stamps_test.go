package schema

// FAILING-FIRST (TDD RED, GG-5) for phone-refit-playbook W7.1 / W7.4: JournalRecord gains `ts`
// and `state_since`. Both are additive and omitted when zero, so every record type that
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
	for _, key := range []string{`"ts"`, `"state_since"`} {
		if strings.Contains(string(b), key) {
			t.Errorf("a record with no stamp serialised %s: %s -- a zero stamp must be absent, never the epoch", key, b)
		}
	}
}

func TestJournalRecordRoundTripsStamps(t *testing.T) {
	ts := time.Date(2026, 8, 28, 9, 38, 0, 0, time.UTC)
	last := time.Date(2026, 8, 28, 9, 34, 0, 0, time.UTC)
	b, err := json.Marshal(JournalRecord{SessionID: "m/s1", Type: "roster", TS: ts, StateSince: last})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"ts":`, `"state_since":`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("a stamped record did not serialise %s: %s", key, b)
		}
	}
	var back JournalRecord
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !back.TS.Equal(ts) || !back.StateSince.Equal(last) {
		t.Errorf("round trip = ts %v state_since %v; want %v and %v", back.TS, back.StateSince, ts, last)
	}
}

// TestJournalRecordUnstampedBytesAreUnchanged is the byte-identity NEGATIVE CONTROL the
// orchestrator asked for (W7 ruling, constraint 2): the literal is what main@1a0e7b29 produced for
// this record, measured there (docs/verification/phone-refit-w7.md), not typed from memory. A
// record with zero stamps must serialise to exactly those bytes; drop `omitzero` from either new
// field and this fails.
func TestJournalRecordUnstampedBytesAreUnchanged(t *testing.T) {
	b, err := json.Marshal(JournalRecord{Cursor: 3, SessionID: "m/s1", Type: "launched", Group: status.Group("working"), Agent: "claude", Name: "api"})
	if err != nil {
		t.Fatal(err)
	}
	const before = `{"cursor":3,"session_id":"m/s1","type":"launched","group":"working","agent":"claude","name":"api"}`
	if string(b) != before {
		t.Errorf("an unstamped record serialises as\n  %s\nwant the bytes main@1a0e7b29 wrote\n  %s", b, before)
	}
}
