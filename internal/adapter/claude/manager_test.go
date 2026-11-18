package claude

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
)

func TestManagerClaudeMCPPreservesHooksAndNativeToolsForFreshAndResume(t *testing.T) {
	manager := &adapter.ManagerSpec{Command: "/private/space 'quote;$()/swarm", Args: []string{"voice", "mcp", "--socket", "/private/s 'x;$/voice.sock", "--auth-file", "/private/s 'x;$/auth", "--conversation", "conversation-1"}}
	for _, resume := range []bool{false, true} {
		var argv []string
		var err error
		if resume {
			argv, err = New().Resume(adapter.ResumeSpec{ConversationID: "019a056e-f192-7961-84cc-b4fa60e47aee", Manager: manager})
		} else {
			argv, err = New().Command(adapter.LaunchSpec{Manager: manager, InitialPrompt: "inspect repo"})
		}
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(argv, "--settings") || !slices.Contains(argv, "--dangerously-skip-permissions") {
			t.Fatalf("manager lost hooks/full native tools: %q", argv)
		}
		if slices.Contains(argv, "--strict-mcp-config") || slices.Contains(argv, "--tools") || slices.Contains(argv, "-p") || slices.Contains(argv, "--bare") {
			t.Fatalf("manager changed native tool/login path: %q", argv)
		}
		idx := slices.Index(argv, "--mcp-config")
		if idx < 0 || idx+1 >= len(argv) {
			t.Fatal("manager MCP config absent")
		}
		var config struct {
			Servers map[string]struct {
				Command string   `json:"command"`
				Args    []string `json:"args"`
			} `json:"mcpServers"`
		}
		if err := json.Unmarshal([]byte(argv[idx+1]), &config); err != nil {
			t.Fatal(err)
		}
		got := config.Servers["swarm_voice"]
		if got.Command != manager.Command || !reflect.DeepEqual(got.Args, manager.Args) {
			t.Fatalf("MCP arguments changed: %+v", got)
		}
	}
}
