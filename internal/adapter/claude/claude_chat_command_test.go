package claude

import (
	"encoding/json"
	"github.com/Nathandela/swarm/internal/adapter"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestClaudeChatCommandRetainsNativeAssetsAndUsesSemanticPrompt(t *testing.T) {
	const prompt = "OWNER_TEXT_NEVER_ARGV 🐝\n--permission-mode bypassPermissions"
	argv, err := New().Command(adapter.LaunchSpec{Cwd: "/work", Name: "owner chat", InitialPrompt: prompt, Options: map[string]string{"claude_interface": "swarm-chat", "model": "opus"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"-p", "--replay-user-messages", "--verbose"} {
		if !slices.Contains(argv, flag) {
			t.Errorf("missing structured flag %s: %v", flag, argv)
		}
	}
	for _, pair := range [][2]string{{"--input-format", "stream-json"}, {"--output-format", "stream-json"}, {"--permission-prompt-tool", "stdio"}, {"--permission-prompts", "host"}, {"--model", "opus"}, {"--name", "owner chat"}} {
		i := slices.Index(argv, pair[0])
		if i < 0 || i+1 >= len(argv) || argv[i+1] != pair[1] {
			t.Errorf("missing native pair %v: %v", pair, argv)
		}
	}
	for _, forbidden := range []string{prompt, "--system-prompt", "--append-system-prompt", "--settings-sources", "--mcp-config", "--strict-mcp-config", "--no-session-persistence", "--dangerously-skip-permissions", "--include-partial-messages"} {
		if slices.Contains(argv, forbidden) {
			t.Errorf("chat rewrote native assets or leaked prompt via %q", forbidden)
		}
	}
	if strings.Contains(strings.Join(argv, "\n"), "OWNER_TEXT_NEVER_ARGV") {
		t.Fatal("initial prompt entered argv")
	}
	var settings map[string]json.RawMessage
	count := 0
	for i, arg := range argv {
		if arg == "--settings" {
			count++
			if i+1 >= len(argv) || json.Unmarshal([]byte(argv[i+1]), &settings) != nil {
				t.Fatal("invalid merged settings")
			}
		}
	}
	if count != 1 || string(settings["autoContinueAtUsageLimit"]) != "false" {
		t.Fatalf("usage guard not merged into one normal settings object: %v", argv)
	}
	var hooks map[string]json.RawMessage
	if json.Unmarshal(settings["hooks"], &hooks) != nil {
		t.Fatal("normal hooks missing")
	}
	for _, hook := range []string{"UserPromptSubmit", "PreToolUse", "PostToolUse", "Notification", "Stop", "SubagentStart", "SubagentStop", "PermissionRequest"} {
		if len(hooks[hook]) == 0 {
			t.Errorf("lost native Swarm hook %s", hook)
		}
	}
}

func TestClaudeChatInterfacePickerDefaultsToNativeAndOrdinaryArgvUnchanged(t *testing.T) {
	var picker *adapter.OptionSpec
	for _, option := range New().Options() {
		if option.Key == "claude_interface" {
			copy := option
			picker = &copy
		}
	}
	if picker == nil || picker.Type != "choice" || picker.Default != "native" || !slices.Contains(picker.Choices, "swarm-chat") {
		t.Fatal("missing explicit native-default interface choice")
	}
	ordinary, err := New().Command(adapter.LaunchSpec{InitialPrompt: "ordinary prompt", Options: map[string]string{"model": "sonnet"}})
	if err != nil {
		t.Fatal(err)
	}
	native, err := New().Command(adapter.LaunchSpec{InitialPrompt: "ordinary prompt", Options: map[string]string{"model": "sonnet", "claude_interface": "native"}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ordinary, native) || !slices.Contains(ordinary, "ordinary prompt") || slices.Contains(ordinary, "-p") {
		t.Fatal("default native terminal argv changed")
	}
}

func TestClaudeChatCommandCannotResumeNativeChat(t *testing.T) {
	if argv, err := New().Resume(adapter.ResumeSpec{ConversationID: "1389ef09-4c19-4d50-8fdd-1fc95bdcfd4a", Options: map[string]string{"claude_interface": "swarm-chat"}}); err == nil || len(argv) != 0 {
		t.Fatal("chat adapter composed a resumed conversation")
	}
}
