package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestClaudeNativeControlConfigRequiresExactSentinel(t *testing.T) {
	identity := strings.Repeat("a", 64)
	base := `{"session_id":"control","required_runtime":"claude-native-control","argv":["/fixture/claude"],"cwd":"/fixture/project","session_dir":"/fixture/state/control","socket_path":"/fixture/state/control/shim.sock","hook_socket_path":"/fixture/state/control/hook.sock","native_provider":"claude","login_state_root":"/fixture/state","login_runtime":{"provider":"claude","epoch":1,"identity":"` + identity + `","home":"/fixture/state/logins/provider/claude/profiles/` + identity + `"},"cli_identity":{"path":"/fixture/claude","version":"2.1.310","fingerprint":"` + strings.Repeat("b", 64) + `"},"env":[]}`
	for _, tc := range []struct {
		name, raw, marker string
		want              bool
	}{
		{"exact", base, "claude-native-control", true},
		{"missing-marker", base, "", false},
		{"chat-marker", base, "claude-chat", false},
		{"missing-runtime", strings.Replace(base, `"required_runtime":"claude-native-control",`, "", 1), "claude-native-control", false},
		{"null-runtime", strings.Replace(base, `"required_runtime":"claude-native-control"`, `"required_runtime":null`, 1), "claude-native-control", false},
		{"alias-runtime", strings.Replace(base, `"required_runtime"`, `"Required_Runtime"`, 1), "claude-native-control", false},
		{"duplicate-runtime", strings.Replace(base, `"required_runtime":"claude-native-control"`, `"required_runtime":"claude-native-control","required_runtime":"claude-native-control"`, 1), "claude-native-control", false},
		{"wrong-provider", strings.Replace(base, `"native_provider":"claude"`, `"native_provider":"codex"`, 1), "claude-native-control", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeShimLaunchConfig([]byte(tc.raw), tc.marker)
			if (err == nil) != tc.want {
				t.Fatalf("native config accepted=%t want=%t: %v", err == nil, tc.want, err)
			}
			if tc.want && shimConfigFromLaunch(got).RequiredRuntime != "claude-native-control" {
				t.Fatal("runtime lost in shim projection")
			}
		})
	}
	var stderr bytes.Buffer
	if code := runShim([]string{"--require-claude-native-control"}, &bytes.Buffer{}, &stderr); code != 2 || strings.Contains(stderr.String(), "flag provided but not defined") {
		t.Fatalf("new sentinel not recognized before config/provider effects: %d %s", code, stderr.String())
	}
}
