package phonecore

import (
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"testing"
)

func TestNativeReadRouteDoesNotGrantComposerOrTerminalControl(t *testing.T) {
	record := &schema.SessionCapabilities{Provider: "opencode", SessionInstance: "instance", NativeInteractionVersion: 2, TerminalFallback: true}
	if RouteSession(record, r8Profile()) != DestinationChat {
		t.Fatal("qualified native reads have no transcript route")
	}
	if ComposerAvailable(record, r8Profile()) || TerminalControlAvailable(record, r8Profile()) {
		t.Fatal("native read proof granted a mutation")
	}
	if RouteSession(record, schema.RemoteProfileV1{}) != DestinationStatusCard {
		t.Fatal("native read bypassed profile trust")
	}
	record.NativeInteractionVersion = 3
	if RouteSession(record, r8Profile()) == DestinationChat {
		t.Fatal("unknown native item version was treated as readable")
	}
	record.NativeInteractionVersion = 0
	record.StructuredChat = true
	record.TerminalFallback = false
	if !ComposerAvailable(record, r8Profile()) {
		t.Fatal("native read change removed a legacy qualified composer")
	}
}
