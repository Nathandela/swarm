package skeleton

import (
	"encoding/json"
	"testing"

	"github.com/Nathandela/swarm/internal/journal"
	"github.com/Nathandela/swarm/internal/protocol"
)

func TestJournalIdentityConversionNeverBorrowsFreshRosterForHistory(t *testing.T) {
	lookups := 0
	caps := func(string) (protocol.SessionCapabilities, bool) {
		lookups++
		return protocol.SessionCapabilities{Provider: "codex", ProviderVersion: "0.167.0", AdapterRevision: "fixture", SessionInstance: "fresh-instance", TerminalFallback: true, TerminalControl: true}, true
	}
	old := journal.Record{Cursor: 7, SessionID: "worker", SessionInstance: "old-instance", Role: "manager", Type: journal.TypeExited}
	got := toWireJournalRecordWith(old, caps)
	if got.SessionInstance != "old-instance" || got.Role != "manager" || lookups != 0 {
		t.Fatalf("historical event borrowed current identity: %+v lookups=%d", got, lookups)
	}
	legacy := old
	legacy.SessionInstance = ""
	legacy.Role = ""
	got = toWireJournalRecordWith(legacy, caps)
	if got.SessionInstance != "" || lookups != 0 {
		t.Fatalf("legacy identity was invented: %+v lookups=%d", got, lookups)
	}
	roster := journal.Record{SessionID: "worker", Type: journal.TypeRoster}
	got = toWireJournalRecordWith(roster, caps)
	if got.SessionInstance != "fresh-instance" || lookups != 1 {
		t.Fatalf("atomic roster omitted authoritative current instance: %+v lookups=%d", got, lookups)
	}
	capability, _ := caps("worker")
	capability.SessionInstance = "historic-payload-instance"
	payload, _ := json.Marshal(capability)
	for _, typ := range []journal.RecordType{journal.TypeSessionState, journal.TypeCapabilityTransition} {
		rec := journal.Record{SessionID: "worker", Type: typ, Payload: payload}
		got := toWireJournalRecordWith(rec, caps)
		if got.SessionInstance != "historic-payload-instance" {
			t.Fatalf("existing typed historical proof was lost: %+v", got)
		}
	}
}
