package skeleton

import (
	"encoding/json"
	"testing"

	"github.com/Nathandela/swarm/internal/journal"
	"github.com/Nathandela/swarm/internal/protocol"
)

func TestSessionStateWire_UsesHistoricalPayloadNotCurrentLookup(t *testing.T) {
	old := protocol.SessionCapabilities{Provider: "claude", SessionInstance: "i1", StructuredChat: false}
	current := protocol.SessionCapabilities{Provider: "claude", SessionInstance: "i2", StructuredChat: true}
	payload, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	rec := journal.Record{SessionID: "s", Type: journal.TypeSessionState, Payload: payload}
	got := toWireJournalRecordWith(rec, func(string) (protocol.SessionCapabilities, bool) { return current, true })
	if got.Capabilities == nil || *got.Capabilities != old {
		t.Fatalf("session-state capability = %#v, want its own historical payload %#v", got.Capabilities, old)
	}
}

func TestSessionStateWire_RejectsMalformedCapabilityPayload(t *testing.T) {
	rec := journal.Record{SessionID: "s", Type: journal.TypeSessionState, Payload: []byte(`{"provider":"claude","session_instance":"i","structured_chat":true,"terminal_fallback":true}`)}
	if got := toWireJournalRecord(rec); got.Capabilities != nil {
		t.Fatalf("malformed session-state capability crossed wire: %#v", got.Capabilities)
	}
}
