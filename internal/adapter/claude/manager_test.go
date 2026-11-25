package claude

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
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

func TestVMManagerKeepsClaudePermissions(t *testing.T) {
	manager := &adapter.ManagerSpec{NativeCLI: true, Command: "/private/swarm"}
	for _, resume := range []bool{false, true} {
		var argv []string
		var err error
		if resume {
			argv, err = New().Resume(adapter.ResumeSpec{Manager: manager, ConversationID: "019a056e-f192-7961-84cc-b4fa60e47aee"})
		} else {
			argv, err = New().Command(adapter.LaunchSpec{Manager: manager})
		}
		if err != nil {
			t.Fatal(err)
		}
		idx := slices.Index(argv, "--mcp-config")
		if !slices.Contains(argv, "--allowedTools=mcp__swarm_voice__publish,mcp__swarm_voice__ask,mcp__swarm_voice__swarm_discussion_history,mcp__swarm_voice__swarm_discussion_answer,mcp__swarm_voice__swarm_reply,mcp__swarm_voice__swarm_read_note,mcp__swarm_voice__swarm_discussion_yield") || slices.Contains(argv, "--dangerously-skip-permissions") || idx < 0 || !strings.Contains(argv[idx+1], `"env":{"SWARM_MANAGER_VM_CLI":"1"}`) {
			t.Fatalf("unexpected VM policy: %#v", argv)
		}
	}
}

func TestClaudeAttachmentSettingsPreserveHooksAndProtectInputs(t *testing.T) {
	const dir = "/private/Operators (test [team] *)/attachments"
	ad := New()
	for _, resume := range []bool{false, true} {
		var argv []string
		var err error
		if resume {
			argv, err = ad.Resume(adapter.ResumeSpec{ConversationID: "conv", AttachmentReadDir: dir})
		} else {
			argv, err = ad.Command(adapter.LaunchSpec{AttachmentReadDir: dir})
		}
		if err != nil {
			t.Fatal(err)
		}
		raw, ok := settingsValue(argv)
		if !ok || slices.Contains(argv, "--add-dir") || slices.Contains(argv, "--dangerously-skip-permissions") {
			t.Fatalf("unexpected Claude launch settings: %q", argv)
		}
		var settings struct {
			Hooks       map[string]json.RawMessage
			Permissions struct {
				AdditionalDirectories []string
				Deny                  []string
			}
		}
		if err := json.Unmarshal([]byte(raw), &settings); err != nil {
			t.Fatal(err)
		}
		if len(settings.Hooks) != len(hookEvents) || !slices.Equal(settings.Permissions.AdditionalDirectories, []string{dir}) ||
			!slices.Equal(settings.Permissions.Deny, []string{`Edit(//private/Operators (test \[team\] \*)/attachments)`, `Edit(//private/Operators (test \[team\] \*)/attachments/**)`}) {
			t.Fatalf("unexpected Claude settings: %s", raw)
		}
	}
	for _, unsupported := range []string{"", "/private/Operators?/attachments", `/private/Operators\team/attachments`} {
		argv, err := ad.Command(adapter.LaunchSpec{AttachmentReadDir: unsupported})
		if err != nil {
			t.Fatal(err)
		}
		if raw, ok := settingsValue(argv); !ok || strings.Contains(raw, `"permissions"`) {
			t.Fatalf("unscoped launch for %q gained file permissions: %s", unsupported, raw)
		}
	}
}
