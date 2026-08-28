package journal

// Byte-identity NEGATIVE CONTROL for phone-refit-playbook W7.1's additive Record.LastActivity
// (W7 ruling, constraint 2): a record carrying no stamp encodes to exactly the line main@1a0e7b29
// wrote, measured there (docs/verification/phone-refit-w7.md). The `ts` zero value in that line is
// the pre-existing TS field's own behaviour, unchanged; what this pins is that `last_activity`
// is ABSENT, so a rollback daemon reads every line this build writes for an unstamped session
// exactly as it read its own.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/status"
)

func TestRecordUnstampedLineIsUnchanged(t *testing.T) {
	got := string(EncodeRecord(Record{SchemaVersion: 1, Cursor: 3, SessionID: "m/s1", Type: TypeLaunched, Group: status.Group("working"), Agent: "claude", Name: "api"}))
	const before = `{"schema_version":1,"cursor":3,"ts":"0001-01-01T00:00:00Z","session_id":"m/s1","type":"launched","group":"working","agent":"claude","name":"api"}`
	if got != before {
		t.Errorf("an unstamped record encodes as\n  %s\nwant the line main@1a0e7b29 wrote\n  %s", got, before)
	}
}
