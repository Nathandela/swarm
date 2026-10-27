package skeleton

import (
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

func TestNativeUnknownDiscussionRetainsClassicAncestorWorkspace(t *testing.T) {
	root := endedCodexSource("classic-root", map[string]string{protocol.OptionWorktree: "true"})
	root.AgentType = "hermes"
	root.AgentCwd = t.TempDir()
	root.ConversationID = "local-" + strings.Repeat("a", 64)
	current := endedCodexSource("native-current", nil)
	current.AgentType = "hermes"
	current.ResumedFrom = root.ID
	current.CreatedAt = time.Now()
	current.HermesNative = true
	current.CLIIdentity = &persist.CLIIdentity{Version: "0.22.5"}
	current.ConversationID = "local-" + strings.Repeat("a", 64)
	current.Status = status.Status{Process: status.ProcessLost, NativeWorkUnknown: true}
	current.ShimPID = 123
	cleaned := false
	api := continuityAPIWithConfig(t, func(c *daemon.Config) { c.PreDelete = func(persist.Meta) error { cleaned = true; return nil } }, root, current)
	if err := api.Delete(current.ID); err == nil {
		t.Fatal("unknown native writers did not block family deletion")
	}
	if cleaned || len(api.List()) != 2 {
		t.Fatal("classic ancestor removed before native uncertainty was checked")
	}
}
