package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestClaudeChatNativeConfigCarriageRetainsManagedRuntimeAndRefusesMixing(t *testing.T) {
	const conversation = "1389ef09-4c19-4d50-8fdd-1fc95bdcfd4a"
	identity := strings.Repeat("a", 64)
	for _, kind := range []string{"exact", "wrong-version", "external-auth", "common-home", "backend", "fixture", "raw-input", "alias-runtime", "unicode-version", "missing-marker"} {
		t.Run(kind, func(t *testing.T) {
			runtime := map[string]any{"provider": "claude", "epoch": 1, "identity": identity, "home": "/fixture/state/logins/provider/claude/profiles/" + identity}
			cli := map[string]any{"path": "/fixture/claude", "version": "2.1.310", "fingerprint": strings.Repeat("b", 64)}
			fields := map[string]any{"required_runtime": "claude-chat", "conversation_id": conversation, "session_id": "native-chat", "argv": []string{"/fixture/claude", "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--replay-user-messages", "--permission-prompt-tool", "stdio", "--permission-prompts", "host", "--session-id=" + conversation, "--settings", `{"autoContinueAtUsageLimit":false}`}, "cwd": "/fixture/project", "session_dir": "/fixture/state/native-chat", "socket_path": "/fixture/state/native-chat/shim.sock", "hook_socket_path": "/fixture/state/native-chat/hook.sock", "hook_drain_token": strings.Repeat("c", 64), "env": []string{}, "login_runtime": runtime, "login_state_root": "/fixture/state", "native_provider": "claude", "cli_identity": cli}
			marker := "claude-chat"
			switch kind {
			case "wrong-version":
				cli["version"] = "2.1.309"
			case "external-auth":
				runtime["external_auth"] = true
			case "common-home":
				runtime["home"] = "/fixture/state/logins/provider/claude/native"
			case "backend":
				fields["backend_program"] = "/fixture/backend"
			case "fixture":
				fields["claude_chat_fixture"] = "/fixture/fake"
			case "raw-input":
				fields["claude_input"] = true
			case "alias-runtime":
				delete(fields, "required_runtime")
				fields["Required_Runtime"] = "claude-chat"
			case "unicode-version":
				delete(cli, "version")
				cli["verſion"] = "2.1.310"
			case "missing-marker":
				marker = ""
			}
			raw, _ := json.Marshal(fields)
			got, err := decodeShimLaunchConfig(raw, marker)
			if kind == "exact" {
				if err != nil {
					t.Fatal("exact native carriage refused", err)
				}
				cfg := shimConfigFromLaunch(got)
				if cfg.RequiredRuntime != "claude-chat" || cfg.NativeProvider != "claude" || cfg.ClaudeChatFixture != "" || cfg.LoginRuntime == nil || cfg.LoginRuntime.Home != runtime["home"] || cfg.CLIIdentity == nil || cfg.CLIIdentity.Version != "2.1.310" || cfg.Backend != nil {
					t.Fatal("native custody fields lost in cmd projection")
				}
			} else if err == nil {
				t.Fatalf("mixed/foreign native config accepted: %s", kind)
			}
		})
	}
}

func TestClaudeChatConfigUnicodeAliasMatchesStandardJSONFold(t *testing.T) {
	var value struct {
		Version string `json:"version"`
	}
	raw := []byte(`{"verſion":"2.1.310"}`)
	if err := json.Unmarshal(raw, &value); err != nil || value.Version != "2.1.310" {
		t.Fatal("standard JSON Unicode-fold counterexample changed", value, err)
	}
	if err := validateClaudeChatLaunchJSON([]byte(`{"cli_identity":{"verſion":"2.1.310"}}`)); err == nil {
		t.Fatal("Unicode SimpleFold alias escaped native custody guard")
	}
}
