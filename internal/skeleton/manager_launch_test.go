package skeleton

import (
	"slices"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
)

func TestManagerLaunchCompositionBindsTrustedBridgeForBothNativeRuntimes(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		spec := daemon.LaunchSpec{AgentType: provider, Cwd: t.TempDir(), Manager: &daemon.ManagerLaunchSpec{ConversationID: "voice-conversation", BridgeSocket: "/private/service/bridge.sock", AuthFile: "/private/service/auth", Command: "/trusted/repo/swarm"}}
		got, err := composeLaunchSpec(spec, testEndpoint, "", srcGetter("", persist.Meta{}), stubLookPath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.Join(got.Argv, "\n"), "/trusted/repo/swarm") {
			t.Fatalf("%s bridge executable lost: %q", provider, got.Argv)
		}
		if provider == "claude" && !slices.Contains(got.Argv, "--settings") {
			t.Fatal("native hooks lost")
		}
		source := endedCodexSource("source", nil)
		if provider == "claude" {
			source = endedClaudeSource("source")
		}
		source.Cwd = spec.Cwd
		source.Role = "manager"
		source.ManagerConversationID = "voice-conversation"
		spec.Options = map[string]string{protocol.OptionResumeFrom: testEndpoint + "/source"}
		if _, err := composeLaunchSpec(spec, testEndpoint, "", srcGetter(source.ID, source), stubLookPath); err != nil {
			t.Fatal(err)
		}
		spec.Manager = nil
		if _, err := composeLaunchSpec(spec, testEndpoint, "", srcGetter(source.ID, source), stubLookPath); err == nil {
			t.Fatal("manager silently resumed without current bridge binding")
		}
	}
}
