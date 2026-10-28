package skeleton

import (
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
)

func TestClaudeChatNativeCallerSelectorIsFreshLocalOnly(t *testing.T) {
	for _, kind := range []string{"exact", "prompt", "remote-preset", "provider", "resume", "resume-empty", "import", "handoff", "lineage", "intent", "argv", "runtime", "profile", "cli", "backend", "supervision", "invalid-utf8", "oversized-prompt"} {
		t.Run(kind, func(t *testing.T) {
			api := &coreAPI{}
			spec := daemon.LaunchSpec{AgentType: "claude", Cwd: "/work", ClientEnv: []string{"PATH=/usr/bin"}, Options: map[string]string{"claude_interface": "swarm-chat"}}
			switch kind {
			case "invalid-utf8":
				spec.InitialPrompt = string([]byte{0xff})
			case "oversized-prompt":
				spec.InitialPrompt = strings.Repeat("x", 16<<10+1)
			case "prompt":
				spec.InitialPrompt = "semantic owner text 🐝"
			case "remote-preset":
				spec.ClientEnv = nil
			case "provider":
				spec.AgentType = "codex"
			case "resume":
				spec.Options[protocol.OptionResumeFrom] = "endpoint/source"
			case "resume-empty":
				spec.Options[protocol.OptionResumeFrom] = ""
			case "import":
				spec.Options[protocol.OptionResumeConversationID] = "1389ef09-4c19-4d50-8fdd-1fc95bdcfd4a"
			case "handoff":
				spec.Options[protocol.OptionHandoffFrom] = ""
			case "lineage":
				spec.SpawnedFrom = "source"
			case "intent":
				spec.SpawnIntent = protocol.SpawnIntentDelegate
			case "argv":
				spec.Argv = []string{"/foreign/claude"}
			case "runtime":
				spec.RequiredRuntime = persist.RequiredRuntimeClaudeChat
			case "profile":
				spec.LoginRuntime = &logins.Runtime{Provider: "claude"}
			case "cli":
				spec.CLIIdentity = &persist.CLIIdentity{Path: "/foreign/claude"}
			case "backend":
				spec.Backend = &daemon.BackendSpec{Program: "/foreign/backend"}
			case "supervision":
				spec.Supervision = "hands-off"
			}
			before := spec
			err := api.prepareClaudeChatFixtureLaunch(&spec)
			allowed := runtime.GOOS == "linux" && (kind == "exact" || kind == "prompt")
			if allowed {
				if err != nil || spec.RequiredRuntime != persist.RequiredRuntimeClaudeChat || spec.ClaudeChatFixture != "" || len(spec.Argv) != 0 || spec.ConversationID != "" || spec.InitialPrompt != before.InitialPrompt {
					t.Fatalf("explicit fresh native selector not derived safely: %+v %v", spec, err)
				}
			} else if err == nil || !reflect.DeepEqual(spec, before) {
				t.Fatalf("invalid/prequalified native selector gained effects: %s %+v %v", kind, spec, err)
			}
		})
	}
}

func TestClaudeChatNativeCompositionPreservesPromptForSemanticDispatch(t *testing.T) {
	const prompt = "OWNER_INITIAL_NEVER_POSITIONAL 🐝\nsecond line"
	spec := daemon.LaunchSpec{AgentType: "claude", Cwd: "/work", RequiredRuntime: persist.RequiredRuntimeClaudeChat, ClientEnv: []string{"PATH=/usr/bin"}, InitialPrompt: prompt, Options: map[string]string{"claude_interface": "swarm-chat", "model": "opus"}}
	got, err := composeLaunchSpec(spec, testEndpoint, "", srcGetter("", persist.Meta{}), stubLookPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.InitialPrompt != prompt || slices.Contains(got.Argv, prompt) || got.Argv[0] != "/abs/claude" || !slices.Contains(got.Argv, "--permission-prompt-tool") {
		t.Fatalf("prompt lost or leaked to native argv: %+v", got)
	}
	if got.Backend != nil || got.ClaudeChatFixture != "" || got.RequiredRuntime != persist.RequiredRuntimeClaudeChat {
		t.Fatal("native chat acquired fixture/backend")
	}
}
