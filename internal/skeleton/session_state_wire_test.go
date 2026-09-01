package skeleton

import (
	"encoding/json"
	"testing"

	"github.com/Nathandela/swarm/internal/journal"
	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/status"
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

func TestSessionStateWire_InvalidCapabilityPayloadExplicitlyRevokesPriorAuthority(t *testing.T) {
	cache := phonecore.NewSessionCache()
	valid := &schema.SessionCapabilities{Provider: "claude", SessionInstance: "i", StructuredChat: true}
	cache.Apply(schema.JournalRecord{
		Cursor: 1, SessionID: "s", Type: phonecore.RecordTypeSessionState,
		Group: status.GroupWorking, Capabilities: valid,
	})

	rec := journal.Record{
		SessionID: "s", Type: journal.TypeSessionState, Group: status.GroupWorking,
		Payload: []byte(`{"provider":"claude","session_instance":"i","structured_chat":true,"terminal_fallback":true}`),
	}
	got := toWireJournalRecord(rec)
	if got.Capabilities == nil || got.Capabilities.Validate() == nil {
		t.Fatalf("invalid session-state payload = %#v, want explicit fail-closed sentinel", got.Capabilities)
	}
	got.Cursor = 2
	body, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var crossed schema.JournalRecord
	if err := json.Unmarshal(body, &crossed); err != nil {
		t.Fatal(err)
	}
	if crossed.Capabilities == nil || crossed.Capabilities.Validate() == nil {
		t.Fatalf("fail-closed sentinel did not survive the wire: %s", body)
	}
	cache.Apply(crossed)
	state, ok := cache.Get("s")
	if !ok || state.Group != status.GroupWorking {
		t.Fatalf("invalid capability discarded display state: %#v, %v", state, ok)
	}
	if state.Capabilities != nil {
		t.Fatalf("invalid session-state retained prior authority: %#v", state.Capabilities)
	}
}

func TestSessionStateWire_RenameOnlyNilPayloadPreservesPriorAuthority(t *testing.T) {
	cache := phonecore.NewSessionCache()
	valid := &schema.SessionCapabilities{Provider: "claude", SessionInstance: "i", StructuredChat: true}
	cache.Apply(schema.JournalRecord{
		Cursor: 1, SessionID: "s", Type: phonecore.RecordTypeSessionState,
		Group: status.GroupWorking, Name: "before", Capabilities: valid,
	})

	rename := toWireJournalRecord(journal.Record{
		Cursor: 2, SessionID: "s", Type: journal.TypeSessionState,
		Group: status.GroupWorking, Name: "after", Payload: nil,
	})
	if rename.Capabilities != nil {
		t.Fatalf("rename-only session-state invented capability authority: %#v", rename.Capabilities)
	}
	cache.Apply(rename)
	state, ok := cache.Get("s")
	if !ok || state.Name != "after" {
		t.Fatalf("rename-only session-state was not folded: %#v, %v", state, ok)
	}
	if state.Capabilities == nil || *state.Capabilities != *valid {
		t.Fatalf("rename-only nil payload changed prior capability: %#v", state.Capabilities)
	}
}
