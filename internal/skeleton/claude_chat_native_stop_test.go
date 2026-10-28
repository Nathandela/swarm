package skeleton

import (
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
)

func TestClaudeNativeStopMissingAuthorityBypassesConsumerLocks(t *testing.T) {
	d := &Daemon{}
	const local = "native-stop-retained"
	d.rememberClaudeChatRuntime(persist.Meta{SchemaVersion: 6, ID: local, AgentType: "claude", RequiredRuntime: "claude-chat"})
	lane := d.composerLaneFor(local)
	lane.mu.Lock()
	lane.endMu.Lock()
	done := make(chan error, 1)
	go func() { done <- d.ownerKill(local) }()
	var err error
	blocked := false
	select {
	case err = <-done:
	case <-time.After(time.Second):
		blocked = true
	}
	lane.endMu.Unlock()
	lane.mu.Unlock()
	if blocked {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("owner Stop remained blocked after fixture release")
		}
		t.Fatal("retained native Stop waited on consumer state")
	}
	if err == nil || !strings.Contains(err.Error(), "unconfirmed") {
		t.Fatalf("missing authority must be unconfirmed without core fallback: %v", err)
	}
}
