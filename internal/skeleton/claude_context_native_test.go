//go:build linux

package skeleton

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/accountconfig"
	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/claude"
)

// Opt-in local native contract probe. No turn is submitted and all service
// routes point to loopback. The real adapter and managed observer argv are used;
// a private inert swarm executable prevents hook callbacks reaching a daemon.
func TestManagedClaudeNative289PreservesOwnerAndObserverHooks(t *testing.T) {
	native := os.Getenv("SWARM_TEST_CLAUDE_NATIVE_PATH")
	if native == "" {
		t.Skip("explicit native contract executable required")
	}
	root := t.TempDir()
	home, source, profile, cwd, bin := filepath.Join(root, "home"), filepath.Join(root, "home", ".claude"), filepath.Join(root, "account"), filepath.Join(root, "project"), filepath.Join(root, "bin")
	for _, path := range []string{home, source, profile, cwd, bin, filepath.Join(cwd, ".claude")} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	put := func(path, value string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	put(filepath.Join(source, "settings.json"), `{"effortLevel":"high","hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"exit 0"}]}]}}`)
	put(filepath.Join(cwd, ".claude", "settings.json"), `{"effortLevel":"low","hooks":{"PreCompact":[{"hooks":[{"type":"command","command":"exit 0"}]}]}}`)
	put(filepath.Join(cwd, ".claude", "settings.local.json"), `{"language":"French"}`)
	put(filepath.Join(profile, ".claude.json"), `{"hasCompletedOnboarding":true,"oauthAccount":{"accountUuid":"synthetic-selected","organizationUuid":"synthetic-org","emailAddress":"selected@example.invalid"}}`)
	put(filepath.Join(profile, ".credentials.json"), `{"claudeAiOauth":{"accessToken":"synthetic-not-real","refreshToken":"synthetic-not-real-refresh","expiresAt":4102444800000,"scopes":["user:profile","user:inference"],"subscriptionType":"max"}}`)
	put(filepath.Join(bin, "swarm"), "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(filepath.Join(bin, "swarm"), 0700); err != nil {
		t.Fatal(err)
	}
	sourceEnv := []string{"HOME=" + home, "CLAUDE_CONFIG_DIR=" + source}
	if _, err := accountconfig.PrepareClaudeContext(profile, cwd, sourceEnv, func(_ string, install func() error) error { return install() }); err != nil {
		t.Fatal(err)
	}
	argv, err := claude.New().Command(adapter.LaunchSpec{})
	if err != nil {
		t.Fatal(err)
	}
	argv, err = managedClaudeObserverArgs(argv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accountconfig.ValidateClaudeInvocationSettings(argv); err != nil {
		t.Fatal(err)
	}
	args := append(append([]string(nil), argv[1:]...), "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--no-session-persistence", "--tools", "", "--strict-mcp-config", "--disable-slash-commands")
	var input strings.Builder
	for i, subtype := range []string{"initialize", "get_settings", "get_hooks_listing"} {
		raw, _ := json.Marshal(map[string]any{"type": "control_request", "request_id": string(rune('0' + i)), "request": map[string]any{"subtype": subtype}})
		input.Write(raw)
		input.WriteByte('\n')
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, native, args...)
	cmd.Dir, cmd.Stdin = cwd, strings.NewReader(input.String())
	cmd.Env = []string{"HOME=" + home, "CLAUDE_CONFIG_DIR=" + profile, "CLAUDE_SECURESTORAGE_CONFIG_DIR=" + profile, "PATH=" + bin + ":/usr/bin:/bin", "TMPDIR=" + root, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "DISABLE_AUTOUPDATER=1", "DISABLE_ERROR_REPORTING=1", "HTTP_PROXY=http://127.0.0.1:9", "HTTPS_PROXY=http://127.0.0.1:9", "ALL_PROXY=http://127.0.0.1:9", "ANTHROPIC_BASE_URL=http://127.0.0.1:9", "CLAUDE_CODE_REMOTE_SETTINGS_PATH=" + filepath.Join(root, "absent-policy")}
	raw, err := cmd.Output()
	if err != nil {
		t.Fatal("synthetic native configuration control failed")
	}
	scan := bufio.NewScanner(bytes.NewReader(raw))
	scan.Buffer(make([]byte, 4096), 2<<20)
	settingsSeen := false
	hooks := map[string]bool{}
	for scan.Scan() {
		var message struct {
			Type     string `json:"type"`
			Response struct {
				Subtype string          `json:"subtype"`
				ID      string          `json:"request_id"`
				Result  json.RawMessage `json:"response"`
			} `json:"response"`
		}
		if json.Unmarshal(scan.Bytes(), &message) != nil || message.Type != "control_response" {
			continue
		}
		if message.Response.Subtype != "success" {
			t.Fatal("native configuration control refused")
		}
		switch message.Response.ID {
		case "1":
			var result struct{ Effective map[string]any }
			if json.Unmarshal(message.Response.Result, &result) != nil || result.Effective["effortLevel"] != "low" || result.Effective["language"] != "French" {
				t.Fatal("assembled managed argv changed project/local precedence")
			}
			settingsSeen = true
		case "2":
			var result struct {
				Hooks []struct{ Event, Source string }
			}
			if json.Unmarshal(message.Response.Result, &result) != nil {
				t.Fatal("invalid native hook listing")
			}
			for _, hook := range result.Hooks {
				hooks[hook.Event+"/"+hook.Source] = true
			}
		}
	}
	for _, expected := range []string{"UserPromptSubmit/userSettings", "PreCompact/projectSettings", "UserPromptSubmit/flagSettings", "SessionStart/flagSettings", "StopFailure/flagSettings"} {
		if !hooks[expected] {
			t.Errorf("missing native merged hook %s", expected)
		}
	}
	if scan.Err() != nil || !settingsSeen {
		t.Fatal("native settings result missing")
	}
}
