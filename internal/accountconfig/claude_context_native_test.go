package accountconfig

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

	"github.com/creack/pty"
)

// Explicitly opt in with an exact installed native executable. This checks only
// synthetic native local settings/status; it never submits a turn or authenticates.
func TestClaudeContextNative289(t *testing.T) {
	native := os.Getenv("SWARM_TEST_CLAUDE_NATIVE_PATH")
	if native == "" {
		t.Skip("explicit native contract executable required")
	}
	f := fixture(t, "claude")
	put(t, filepath.Join(f.source, "settings.json"), `{"model":"claude-sonnet-4-6","effortLevel":"high","permissions":{"deny":["Bash(*)"]},"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"exit 0"}]}]}}`)
	put(t, filepath.Join(f.home, ".claude.json"), `{"oauthAccount":{"accountUuid":"synthetic-owner","organizationUuid":"synthetic-owner-org","emailAddress":"owner@example.invalid"}}`)
	put(t, filepath.Join(f.candidate, ".claude.json"), `{"hasCompletedOnboarding":true,"oauthAccount":{"accountUuid":"synthetic-selected","organizationUuid":"synthetic-selected-org","emailAddress":"selected@example.invalid"}}`)
	put(t, filepath.Join(f.candidate, ".credentials.json"), `{"claudeAiOauth":{"accessToken":"synthetic-not-real","refreshToken":"synthetic-not-real-refresh","expiresAt":4102444800000,"scopes":["user:profile","user:inference"],"subscriptionType":"max"}}`)
	put(t, filepath.Join(f.cwd, ".claude", "settings.json"), `{"effortLevel":"low","permissions":{"deny":["WebFetch"]}}`)
	put(t, filepath.Join(f.cwd, ".claude", "settings.local.json"), `{"language":"French"}`)
	if _, err := PrepareClaudeContext(f.candidate, f.cwd, f.env, func(g string, install func() error) error { return install() }); err != nil {
		t.Fatal(err)
	}
	env := []string{"HOME=" + f.home, "CLAUDE_CONFIG_DIR=" + f.candidate, "CLAUDE_SECURESTORAGE_CONFIG_DIR=" + f.candidate, "PATH=/usr/bin:/bin", "TMPDIR=" + t.TempDir(), "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "DISABLE_AUTOUPDATER=1", "DISABLE_ERROR_REPORTING=1", "HTTP_PROXY=http://127.0.0.1:9", "HTTPS_PROXY=http://127.0.0.1:9", "ALL_PROXY=http://127.0.0.1:9", "ANTHROPIC_BASE_URL=http://127.0.0.1:9", "CLAUDE_CODE_REMOTE_SETTINGS_PATH=" + filepath.Join(f.root, "no-remote-policy")}
	nativeCwd := f.cwd
	run := func(input string, args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, native, args...)
		cmd.Env = env
		cmd.Dir = nativeCwd
		cmd.Stdin = strings.NewReader(input)
		raw, err := cmd.Output()
		if err != nil {
			t.Fatal("synthetic native command failed")
		}
		return raw
	}
	if string(run("", "--version")) != "2.1.289 (Claude Code)\n" {
		t.Fatal("native version differs from contract")
	}
	var status struct {
		LoggedIn   bool   `json:"loggedIn"`
		AuthMethod string `json:"authMethod"`
		Email      string `json:"email"`
	}
	if json.Unmarshal(run("", "auth", "status", "--json"), &status) != nil || !status.LoggedIn || status.AuthMethod != "claude.ai" || status.Email != "selected@example.invalid" {
		t.Fatal("native local status did not retain selected account metadata")
	}
	input := ""
	for i, subtype := range []string{"initialize", "get_settings", "get_hooks_listing"} {
		raw, _ := json.Marshal(map[string]any{"type": "control_request", "request_id": string(rune('0' + i)), "request": map[string]any{"subtype": subtype}})
		input += string(raw) + "\n"
	}
	raw := run(input, "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--no-session-persistence", "--tools", "", "--strict-mcp-config")
	scan := bufio.NewScanner(bytes.NewReader(raw))
	scan.Buffer(make([]byte, 4096), 2<<20)
	settingsSeen, hooksSeen := false, false
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
			t.Fatal("synthetic native control failed")
		}
		switch message.Response.ID {
		case "1":
			var result struct {
				Effective map[string]any `json:"effective"`
				Sources   []struct {
					Source string `json:"source"`
				} `json:"sources"`
			}
			if json.Unmarshal(message.Response.Result, &result) != nil {
				t.Fatal("invalid settings result")
			}
			if result.Effective["effortLevel"] != "low" || result.Effective["language"] != "French" || len(result.Sources) != 3 || result.Sources[0].Source != "userSettings" || result.Sources[1].Source != "projectSettings" || result.Sources[2].Source != "localSettings" {
				t.Fatal("native configuration tiers changed")
			}
			settingsSeen = true
		case "2":
			var result struct {
				Hooks []struct {
					Event  string `json:"event"`
					Source string `json:"source"`
				} `json:"hooks"`
			}
			if json.Unmarshal(message.Response.Result, &result) != nil {
				t.Fatal("invalid hooks result")
			}
			for _, hook := range result.Hooks {
				if hook.Event == "UserPromptSubmit" && hook.Source == "userSettings" {
					hooksSeen = true
				}
			}
		}
	}
	if scan.Err() != nil || !settingsSeen || !hooksSeen {
		t.Fatal("native settings or hook listing missing")
	}
	configProfile := filepath.Join(f.root, "restricted-config")
	nativeCwd = filepath.Join(f.root, "restricted-cwd")
	for _, path := range []string{configProfile, nativeCwd} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := PrepareRestrictedClaudeConfig(f.candidate, configProfile); err != nil {
		t.Fatal(err)
	}
	for i, entry := range env {
		if strings.HasPrefix(entry, "CLAUDE_CONFIG_DIR=") {
			env[i] = "CLAUDE_CONFIG_DIR=" + configProfile
		}
	}
	if json.Unmarshal(run("", "auth", "status", "--json"), &status) != nil || !status.LoggedIn || status.Email != "selected@example.invalid" {
		t.Fatal("restricted native local status lost selected secure store")
	}
	raw = run(input, "-p", "--safe-mode", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--no-session-persistence", "--tools", "", "--strict-mcp-config", "--disable-slash-commands")
	scan = bufio.NewScanner(bytes.NewReader(raw))
	scan.Buffer(make([]byte, 4096), 2<<20)
	emptyHooks := false
	for scan.Scan() {
		var message struct {
			Type     string `json:"type"`
			Response struct {
				ID     string `json:"request_id"`
				Result struct {
					Hooks []json.RawMessage `json:"hooks"`
				} `json:"response"`
			} `json:"response"`
		}
		if json.Unmarshal(scan.Bytes(), &message) == nil && message.Type == "control_response" && message.Response.ID == "2" {
			emptyHooks = len(message.Response.Result.Hooks) == 0
		}
	}
	if scan.Err() != nil || !emptyHooks {
		t.Fatal("restricted native context loaded interactive hooks")
	}
}

// This preserves original native workspace trust and observes private native
// startup writes. It submits no prompt or synthetic confirmation input.
func TestClaudeContextNative289InteractiveStateWrite(t *testing.T) {
	native := os.Getenv("SWARM_TEST_CLAUDE_NATIVE_PATH")
	if native == "" {
		t.Skip("explicit native contract executable required")
	}
	f := fixture(t, "claude")
	put(t, filepath.Join(f.source, "settings.json"), `{"effortLevel":"high"}`)
	owner, _ := json.Marshal(map[string]any{"projects": map[string]any{f.cwd: map[string]any{"hasTrustDialogAccepted": true}}})
	put(t, filepath.Join(f.home, ".claude.json"), string(owner))
	put(t, filepath.Join(f.candidate, ".claude.json"), `{"hasCompletedOnboarding":true,"oauthAccount":{"accountUuid":"synthetic-selected","organizationUuid":"synthetic-org","emailAddress":"selected@example.invalid"}}`)
	put(t, filepath.Join(f.candidate, ".credentials.json"), `{"claudeAiOauth":{"accessToken":"synthetic-not-real","refreshToken":"synthetic-not-real-refresh","expiresAt":4102444800000,"scopes":["user:profile","user:inference"],"subscriptionType":"max"}}`)
	lease := func(_ string, install func() error) error { return install() }
	c, err := PrepareClaudeContext(f.candidate, f.cwd, f.env, lease)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(f.candidate, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, native, "--tools", "", "--strict-mcp-config", "--disable-slash-commands", "--settings", `{"disableAllHooks":true}`)
	cmd.Dir = f.cwd
	cmd.Env = []string{"HOME=" + f.home, "CLAUDE_CONFIG_DIR=" + f.candidate, "CLAUDE_SECURESTORAGE_CONFIG_DIR=" + f.candidate, "PATH=/usr/bin:/bin", "TERM=xterm-256color", "TMPDIR=" + t.TempDir(), "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "DISABLE_AUTOUPDATER=1", "DISABLE_ERROR_REPORTING=1", "HTTP_PROXY=http://127.0.0.1:9", "HTTPS_PROXY=http://127.0.0.1:9", "ALL_PROXY=http://127.0.0.1:9", "ANTHROPIC_BASE_URL=http://127.0.0.1:9", "CLAUDE_CODE_REMOTE_SETTINGS_PATH=" + filepath.Join(f.root, "absent-policy")}
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 30, Cols: 100})
	if err != nil {
		t.Fatal("synthetic native terminal failed")
	}
	defer func() {
		_ = terminal.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()
	chunks := make(chan []byte, 16)
	go func() {
		defer close(chunks)
		buffer := make([]byte, 8192)
		for {
			n, err := terminal.Read(buffer)
			if n > 0 {
				select {
				case chunks <- append([]byte(nil), buffer[:n]...):
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	var output []byte
	changed := false
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for !changed {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				t.Fatal("native terminal exited before private native state write")
			}
			output = append(output, chunk...)
			if len(output) > 1<<20 {
				t.Fatal("native terminal output bound exceeded")
			}
		case <-tick.C:
			after, err := os.ReadFile(filepath.Join(f.candidate, ".claude.json"))
			changed = err == nil && !bytes.Equal(before, after)
		case <-ctx.Done():
			t.Fatal("synthetic native startup state write was not observed")
		}
	}
	if err := RevalidateClaudeContext(c, f.candidate); err != nil {
		t.Fatal("actual native trust write held account", err)
	}
	if _, err := PrepareClaudeContext(f.candidate, f.cwd, f.env, lease); err != nil {
		t.Fatal("actual native trust write held new launch", err)
	}
}
