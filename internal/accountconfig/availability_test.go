package accountconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func availabilityFixture(t *testing.T) (projectionFixture, string, []string) {
	t.Helper()
	f := fixture(t, "claude")
	f.candidate = filepath.Join(f.root, "accounts", "profile")
	if err := os.MkdirAll(f.candidate, 0o700); err != nil {
		t.Fatal(err)
	}
	policy := filepath.Join(f.root, "synthetic-system-policy")
	env := []string{"HOME=" + f.home, "PATH=" + os.Getenv("PATH"), "CLAUDE_CONFIG_DIR=" + f.candidate, "CLAUDE_SECURESTORAGE_CONFIG_DIR=" + f.candidate,
		"CLAUDE_CODE_MAX_RETRIES=0", "CLAUDE_CODE_NONSTREAMING_TIMEOUT_RETRIES=0", "CLAUDE_CODE_MAX_OUTPUT_TOKENS=16"}
	return f, policy, env
}

func TestAvailabilityCheck_DoesNotReadCredentialsOrCreateConfiguration(t *testing.T) {
	f, policy, env := availabilityFixture(t)
	put(t, filepath.Join(f.candidate, "settings.json"), `{"model":"sonnet","permissions":{"deny":["Bash(*)"]}}`)
	// Credential reading and auth-status belong to the caller, never this gate.
	credentials := filepath.Join(f.candidate, ".credentials.json")
	put(t, credentials, "synthetic unreadable bearer data")
	if err := os.Chmod(credentials, 0); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(f.source, "settings.json"), `{"hooks":{"Stop":[{"hooks":[{"command":"must never execute"}]}]}}`)
	before, err := os.ReadDir(f.candidate)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateAvailabilityCheck(f.root, f.candidate, f.cwd, env, policy); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadDir(f.candidate)
	if err != nil || len(before) != len(after) {
		t.Fatal("availability gate changed the selected profile")
	}
	entries, _ := os.ReadDir(f.cwd)
	if len(entries) != 0 {
		t.Fatal("availability gate wrote into the scratch directory")
	}
}

func TestAvailabilityCheck_RefusesManagedAndCustomConfiguration(t *testing.T) {
	for _, tc := range []struct{ path, content, reason string }{
		{"policy/managed-settings.json", `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"synthetic-danger"}]}]}}`, "availability-managed-policy"},
		{"policy/managed-settings.d/20-hooks.json", `{"hooks":{"SessionStart":[]}}`, "availability-managed-policy"},
		{"policy/managed-mcp.json", `{"mcpServers":{"custom":{"command":"synthetic-danger"}}}`, "availability-managed-policy"},
		{"profile/settings.json", `{"hooks":{"SessionStart":[]}}`, "executable-hooks-not-characterized"},
		{"profile/settings.json", `{"apiKeyHelper":"synthetic-danger"}`, "credential-or-provider-settings"},
		{"profile/settings.json", `{"fallbackModel":"alternate"}`, "unsupported-claude-setting"},
		{"profile/plugins/custom/plugin.json", `{}`, "availability-native-customization"},
		{"profile/skills/custom/SKILL.md", "synthetic customization", "availability-native-customization"},
		{"profile/remote-settings.json", `{"hooks":{}}`, "availability-native-customization"},
		{"profile/remote-settings-consent.json", `{}`, "availability-native-customization"},
		{"profile/remote-settings-helper-consent", "synthetic managed helper consent", "availability-native-customization"},
		{"profile/unknown-policy-source.json", `{}`, "availability-unknown-profile-source"},
		{"profile/.claude.json", `{"mcpServers":{"custom":{"command":"synthetic-danger"}}}`, "availability-native-mcp-or-trust"},
	} {
		t.Run(tc.path+tc.reason, func(t *testing.T) {
			f, policy, env := availabilityFixture(t)
			root, relative := f.candidate, strings.TrimPrefix(tc.path, "profile/")
			if strings.HasPrefix(tc.path, "policy/") {
				root, relative = policy, strings.TrimPrefix(tc.path, "policy/")
			}
			put(t, filepath.Join(root, relative), tc.content)
			err := validateAvailabilityCheck(f.root, f.candidate, f.cwd, env, policy)
			if err == nil || !strings.Contains(err.Error(), tc.reason) || strings.Contains(err.Error(), "synthetic-danger") || strings.Contains(err.Error(), f.home) {
				t.Fatalf("unsafe availability source accepted or exposed: %v", err)
			}
		})
	}
}

func TestAvailabilityCheck_RejectsAlternateRuntimeAndAuthControls(t *testing.T) {
	for _, value := range []string{"CLAUDE_SECURESTORAGE_CONFIG_DIR=/alternate", "CLAUDE_CODE_PLUGIN_DIRS=/alternate", "CLAUDE_CODE_USE_BEDROCK=1", "CLAUDE_CODE_RETRY_WATCHDOG=1", "ANTHROPIC_API_KEY=synthetic-bearer", "ANTHROPIC_BASE_URL=https://alternate.invalid", "ANTHROPIC_MODEL=alternate", "NODE_OPTIONS=--require=/alternate", "CLAUDE_CODE_MAX_RETRIES=1", "CLAUDE_CODE_MAX_OUTPUT_TOKENS=17"} {
		t.Run(strings.SplitN(value, "=", 2)[0], func(t *testing.T) {
			f, policy, env := availabilityFixture(t)
			key, _, _ := strings.Cut(value, "=")
			for i, entry := range env {
				if strings.HasPrefix(entry, key+"=") {
					env = append(env[:i], env[i+1:]...)
					break
				}
			}
			env = append(env, value)
			if err := validateAvailabilityCheck(f.root, f.candidate, f.cwd, env, policy); err == nil || strings.Contains(err.Error(), value) {
				t.Fatal("alternate runtime/auth control accepted or exposed")
			}
		})
	}
}

func TestAvailabilityCheck_RejectsAmbientOAuthProfileStore(t *testing.T) {
	f, policy, env := availabilityFixture(t)
	put(t, filepath.Join(f.home, ".config", "anthropic", "credentials", "default.json"), "synthetic unreadable bearer")
	if err := validateAvailabilityCheck(f.root, f.candidate, f.cwd, env, policy); err == nil || !strings.Contains(err.Error(), "alternate-native-profile-store") {
		t.Fatal("ordinary HOME's native OAuth profile could activate remote policy or another account")
	}
}

func TestAvailabilityCheck_UsedProfileHistoryIsInertAndBounded(t *testing.T) {
	f, policy, env := availabilityFixture(t)
	projects := filepath.Join(f.candidate, "projects")
	if err := os.Mkdir(projects, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := validateAvailabilityCheck(f.root, f.candidate, f.cwd, env, policy); err != nil {
		t.Fatalf("empty native history directory refused: %v", err)
	}
	transcript := filepath.Join(projects, "-synthetic-project", "00000000-0000-4000-8000-000000000000.jsonl")
	put(t, transcript, "synthetic transcript data must not be read")
	// No content is needed to prove history cannot execute in the fresh check.
	if err := os.Chmod(transcript, 0); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(projects, "-synthetic-project", "sessions-index.json"), `{}`)
	if err := validateAvailabilityCheck(f.root, f.candidate, f.cwd, env, policy); err != nil {
		t.Fatalf("inert native transcript/index data refused: %v", err)
	}
	unsafe := filepath.Join(projects, "-synthetic-project", "startup.sh")
	put(t, unsafe, "synthetic must not execute")
	if err := validateAvailabilityCheck(f.root, f.candidate, f.cwd, env, policy); err == nil || !strings.Contains(err.Error(), "unknown-runtime-artifact") {
		t.Fatal("unknown executable configuration accepted as runtime data")
	}
	if err := os.Remove(unsafe); err != nil {
		t.Fatal(err)
	}
	large := filepath.Join(projects, "-synthetic-project", "too-large.jsonl")
	file, err := os.OpenFile(large, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(65 << 20); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if err := validateAvailabilityCheck(f.root, f.candidate, f.cwd, env, policy); err == nil || !strings.Contains(err.Error(), "inventory-too-large") {
		t.Fatal("native runtime inventory was not bounded")
	}
	if err := os.Remove(large); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.home, filepath.Join(projects, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := validateAvailabilityCheck(f.root, f.candidate, f.cwd, env, policy); err == nil || !strings.Contains(err.Error(), "unsafe-runtime-artifact") {
		t.Fatal("runtime artifact symlink was followed")
	}
}

func TestAvailabilityCheck_NativeMetadataBackupsRemainInert(t *testing.T) {
	f, policy, env := availabilityFixture(t)
	backups := filepath.Join(f.candidate, "backups")
	if err := os.Mkdir(backups, 0o775); err != nil {
		t.Fatal(err)
	}
	// Native mkdir uses the process umask. The containing profile is 0700,
	// so its genuine 0775 subdirectory grants no access to another owner.
	if err := os.Chmod(backups, 0o775); err != nil {
		t.Fatal(err)
	}
	for _, stamp := range []string{"1791058214651", "1791058274651", "1791058334651", "1791058394651", "1791058454651"} {
		path := filepath.Join(backups, ".claude.json.backup."+stamp)
		put(t, path, "synthetic obsolete metadata must not be read")
		if err := os.Chmod(path, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := validateAvailabilityCheck(f.root, f.candidate, f.cwd, env, policy); err != nil {
		t.Fatalf("native auth-status metadata backups refused: %v", err)
	}
	info, err := os.Stat(backups)
	if err != nil || info.Mode().Perm() != 0o775 {
		t.Fatal("availability gate changed native backup permissions")
	}
}

func TestAvailabilityCheck_RejectsUnsafeOrUnknownMetadataBackups(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "directory", "unknown-name", "public-file", "world-writable-directory", "large-file", "too-many", "nonprivate-profile"} {
		t.Run(kind, func(t *testing.T) {
			f, policy, env := availabilityFixture(t)
			backups := filepath.Join(f.candidate, "backups")
			if err := os.Mkdir(backups, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(backups, ".claude.json.backup.1791058214651")
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink(f.home, path)
			case "hardlink":
				original := filepath.Join(f.home, "synthetic-backup")
				put(t, original, "synthetic metadata")
				err = os.Link(original, path)
			case "directory":
				err = os.Mkdir(path, 0o700)
			case "unknown-name":
				put(t, filepath.Join(backups, ".claude.json.corrupted.1791058214651"), "synthetic metadata")
			case "public-file":
				put(t, path, "synthetic metadata")
				err = os.Chmod(path, 0o644)
			case "world-writable-directory":
				err = os.Chmod(backups, 0o777)
			case "large-file":
				file, openErr := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
				if openErr != nil {
					t.Fatal(openErr)
				}
				err = file.Truncate(maxSourceBytes + 1)
				_ = file.Close()
			case "too-many":
				for _, stamp := range []string{"1791058214651", "1791058274651", "1791058334651", "1791058394651", "1791058454651", "1791058514651"} {
					put(t, filepath.Join(backups, ".claude.json.backup."+stamp), "synthetic metadata")
				}
			case "nonprivate-profile":
				err = os.Chmod(f.candidate, 0o750)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := validateAvailabilityCheck(f.root, f.candidate, f.cwd, env, policy); err == nil {
				t.Fatal("uncharacterized or unsafe backup artifact accepted")
			}
		})
	}
}
