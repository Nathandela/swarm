package phonecore

import (
	"testing"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/status"
)

func TestSessionState_EstablishesExactInstanceAndClearsMetadata(t *testing.T) {
	cache := NewSessionCache()
	cache.Apply(schema.JournalRecord{
		Cursor: 0, SessionID: "m/s", Type: "roster", Group: status.GroupWorking,
		Agent: "claude", Name: "old",
	})
	caps := &schema.SessionCapabilities{
		Provider: "claude", SessionInstance: "instance-2", StructuredChat: true,
	}
	cache.Apply(schema.JournalRecord{
		Cursor: 2, SessionID: "m/s", Type: RecordTypeSessionState,
		Group: status.GroupCompleted, Agent: "codex", Name: "", Capabilities: caps,
	})

	got, ok := cache.Get("m/s")
	if !ok {
		t.Fatal("session state did not keep the row present")
	}
	if got.Group != status.GroupCompleted || got.Agent != "codex" || got.Name != "" {
		t.Fatalf("session state did not replace complete metadata: %#v", got)
	}
	if got.Capabilities == nil || got.Capabilities.SessionInstance != "instance-2" || !got.Capabilities.StructuredChat {
		t.Fatalf("session state did not establish exact-instance capabilities: %#v", got.Capabilities)
	}
}

func TestSessionState_InvalidCapabilitiesFailClosedWithoutDiscardingState(t *testing.T) {
	cache := NewSessionCache()
	cache.Apply(schema.JournalRecord{
		Cursor: 1, SessionID: "m/s", Type: RecordTypeSessionState,
		Group: status.GroupWorking, Agent: "claude",
		Capabilities: &schema.SessionCapabilities{
			Provider: "claude", SessionInstance: "i", StructuredChat: true, TerminalFallback: true,
		},
	})
	got, ok := cache.Get("m/s")
	if !ok || got.Group != status.GroupWorking {
		t.Fatalf("valid display state was discarded: %#v, %v", got, ok)
	}
	if got.Capabilities != nil {
		t.Fatalf("invalid capabilities were accepted: %#v", got.Capabilities)
	}
}
