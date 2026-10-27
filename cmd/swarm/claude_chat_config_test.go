package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// decodeShimLaunchConfig is the exact future production entry, not a test parser.
// The unchecked RED overlay remains historical; this candidate uses the actual decoder.
func TestClaudeChatConfigMarkerAndCriticalKeys(t *testing.T) {
	const id = "10000000-0000-4000-8000-000000000001"
	base := `{"session_id":"fake-chat","required_runtime":"claude-chat","conversation_id":"` + id + `","claude_chat_fixture":"/fixture/fake","argv":["/fixture/fake","--claude-primary-fixture","--session-id=` + id + `"],"cwd":"/fixture/project","session_dir":"/fixture/state/fake-chat","socket_path":"/fixture/state/fake-chat/shim.sock","hook_socket_path":"/fixture/state/fake-chat/hook.sock","hook_drain_token":"` + strings.Repeat("a", 64) + `","env":[]}`
	for _, tc := range []struct {
		name, raw, marker string
		want              bool
	}{
		{"exact fixture", base, "claude-chat", true},
		{"missing marker", base, "", false},
		{"wrong marker", base, "opencode-tui", false},
		{"marker missing runtime", strings.Replace(base, `"required_runtime":"claude-chat",`, "", 1), "claude-chat", false},
		{"duplicate runtime", strings.Replace(base, `"required_runtime":"claude-chat"`, `"required_runtime":"claude-chat","required_runtime":"claude-chat"`, 1), "claude-chat", false},
		{"runtime case alias", strings.Replace(base, `"required_runtime"`, `"Required_Runtime"`, 1), "claude-chat", false},
		{"duplicate UUID", strings.Replace(base, `"conversation_id":"`+id+`"`, `"conversation_id":"`+id+`","conversation_id":"`+id+`"`, 1), "claude-chat", false},
		{"UUID case alias", strings.Replace(base, `"conversation_id"`, `"Conversation_ID"`, 1), "claude-chat", false},
		{"fixture path mismatch", strings.Replace(base, `"claude_chat_fixture":"/fixture/fake"`, `"claude_chat_fixture":"/fixture/other"`, 1), "claude-chat", false},
		{"backend command selector", strings.TrimSuffix(base, "}") + `,"backend_agent_command_args":["--must-not-run"]}`, "claude-chat", false},
		{"backend agent selector alias", strings.TrimSuffix(base, "}") + `,"Backend_Agent_Args":["--must-not-run"]}`, "claude-chat", false},
		{"fixture native claim", strings.TrimSuffix(base, "}") + `,"native_provider":"claude"}`, "claude-chat", false},
		{"fixture managed runtime", strings.TrimSuffix(base, "}") + `,"login_runtime":{"provider":"claude","epoch":1,"identity":"synthetic","home":"/fixture/profile"}}`, "claude-chat", false},
		{"missing native UUID", strings.Replace(base, `"conversation_id":"`+id+`",`, "", 1), "claude-chat", false},
		{"bounded config overflow", strings.TrimSuffix(base, "}") + `,"padding":"` + strings.Repeat("x", 1<<20) + `"}`, "claude-chat", false},
		{"ordinary unchanged", `{"session_id":"ordinary","argv":["/bin/true"],"cwd":"/project"}`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeShimLaunchConfig([]byte(tc.raw), tc.marker)
			if (err == nil) != tc.want {
				t.Fatalf("decode admission got err=%v wantAccepted=%t", err, tc.want)
			}
			if tc.want && tc.marker == "claude-chat" {
				encoded, err := json.Marshal(got)
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(encoded, &fields); err != nil {
					t.Fatal(err)
				}
				if string(fields["required_runtime"]) != `"claude-chat"` || string(fields["conversation_id"]) != `"`+id+`"` || string(fields["claude_chat_fixture"]) != `"/fixture/fake"` {
					t.Fatal("critical launch fields were ignored/lost")
				}
			}
		})
	}
}

func TestClaudeChatConfigReadBoundAndLegacyPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	for _, size := range []int{claudeChatLaunchConfigMaxBytes, claudeChatLaunchConfigMaxBytes + 1} {
		if err := os.WriteFile(path, []byte(strings.Repeat(" ", size)), 0600); err != nil {
			t.Fatal(err)
		}
		raw, err := readShimLaunchConfigBytes(path, "claude-chat")
		if size == claudeChatLaunchConfigMaxBytes && (err != nil || len(raw) != size) {
			t.Fatal("max-fit marked read refused", err)
		}
		if size > claudeChatLaunchConfigMaxBytes && err == nil {
			t.Fatal("marked cap+1 read accepted")
		}
	}
	if raw, err := readShimLaunchConfigBytes(path, ""); err != nil || len(raw) != claudeChatLaunchConfigMaxBytes+1 {
		t.Fatal("legacy read policy changed", err)
	}
}

func TestClaudeChatMarkedConfigRefusesFIFOBeforeBlockingOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.fifo")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := readShimLaunchConfigBytes(path, "claude-chat"); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("marked FIFO was accepted")
		}
	case <-time.After(time.Second):
		// Release a regressed blocking read-open before failing; never leave its goroutine behind.
		peer, err := os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK, 0)
		if err != nil {
			t.Fatalf("FIFO reader blocked and backstop open failed: %v", err)
		}
		defer func() { _ = peer.Close() }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("blocked FIFO reader did not join after backstop")
		}
		t.Fatal("marked config open blocked before inode validation")
	}
}

func TestClaudeChatMarkedConfigRejectsSymlinkButLegacyReadRemains(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "regular.json")
	link := filepath.Join(dir, "link.json")
	content := []byte(`{"session_id":"ordinary"}`)
	if err := os.WriteFile(target, content, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readShimLaunchConfigBytes(link, "claude-chat"); err == nil {
		t.Fatal("marked config followed a symlink")
	}
	if got, err := readShimLaunchConfigBytes(link, ""); err != nil || string(got) != string(content) {
		t.Fatal("legacy symlink read policy changed", err)
	}
}
