package upgrade

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/accountconfig"
	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/enrollment"
	"github.com/Nathandela/swarm/internal/persist"
)

func TestRetainedNativeCompatibilityRequiresWorker3WithoutCache(t *testing.T) {
	stamp := "sha256:" + strings.Repeat("a", 64) + ":" + strings.Repeat("b", 64)
	for _, shape := range []string{"session", "recovery", "enrollment", "refresh", "candidate"} {
		t.Run(shape, func(t *testing.T) {
			state := t.TempDir()
			_ = os.Chmod(state, 0o700)
			path := filepath.Join(state, "accounts", "native", "claude-"+accountconfig.CharacterizedClaudeVersion, "claude")
			identity := persist.CLIIdentity{Path: path, Version: accountconfig.CharacterizedClaudeVersion, Fingerprint: stamp}
			switch shape {
			case "session":
				dir := filepath.Join(state, "retained-session")
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(struct {
					CLI persist.CLIIdentity `json:"cli_identity"`
				}{identity})
				if err := os.WriteFile(filepath.Join(dir, "meta.json"), raw, 0o600); err != nil {
					t.Fatal(err)
				}
			case "recovery":
				raw, _ := json.Marshal(map[string]any{"account_schema_version": accounts.RecoverySchemaVersion, "account_rotations": map[string]any{"incident": map[string]any{"expected_cli_identity": identity}}})
				if err := os.WriteFile(filepath.Join(state, "auth-watch-state.json"), raw, 0o600); err != nil {
					t.Fatal(err)
				}
			case "refresh", "candidate":
				key, field := "cli_refresh", "target"
				if shape == "candidate" {
					key, field = "cli_candidates", "identity"
				}
				raw, _ := json.Marshal(map[string]any{key: map[string]any{"source": map[string]any{field: identity}}})
				if err := os.WriteFile(filepath.Join(state, "auth-watch-state.json"), raw, 0o600); err != nil {
					t.Fatal(err)
				}
			case "enrollment":
				store, err := accounts.Open(state)
				if err != nil {
					t.Fatal(err)
				}
				_ = store.Close()
				dir := filepath.Join(state, "accounts", "jobs", "retained-terminal-job")
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(enrollment.Config{SchemaVersion: enrollment.RetainedNativeConfigSchemaVersion, Provider: accounts.ProviderClaude, NativeVersion: identity.Version, NativePath: path, NativeFingerprint: stamp})
				if err := os.WriteFile(filepath.Join(dir, enrollment.ConfigFile), raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			card := CurrentManifest("v0.16.2")
			older := card
			older.AccountWorker = 2
			if err := accountStateGuard(state, older); err == nil {
				t.Fatal("Worker2 accepted retained authority without cache")
			}
			if err := accountStateGuard(state, card); err != nil {
				t.Fatalf("Worker3 rejected valid metadata: %v", err)
			}
		})
	}
}

func TestRetainedNativeCacheCompatibilityMetadataOnly(t *testing.T) {
	state := t.TempDir()
	_ = os.Chmod(state, 0o700)
	store, err := accounts.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	dir := filepath.Join(state, "accounts", "native", "claude-"+accountconfig.CharacterizedClaudeVersion)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "claude")
	raw, _ := json.Marshal(map[string]any{"version": accountconfig.CharacterizedClaudeVersion, "path": path, "content_sha256": strings.Repeat("a", 64), "source": persist.CLIIdentity{Path: "/owner/native/2.1.289", Version: accountconfig.CharacterizedClaudeVersion, Fingerprint: "observed"}})
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	card := CurrentManifest("v0.16.2")
	older := card
	older.AccountWorker = 2
	if err := accountStateGuard(state, older); err == nil {
		t.Fatal("Worker2 accepted cache-only authority")
	}
	// Upgrade admission reads only metadata; actual actor admission verifies bytes.
	if err := accountStateGuard(state, card); err != nil {
		t.Fatalf("metadata scan tried to execute/read missing binary: %v", err)
	}
	for index, changed := range []string{
		string(raw[:len(raw)-1]) + `,"future_security":true}`,
		string(raw[:len(raw)-1]) + `,"version":"2.1.289"}`,
		strings.Replace(string(raw), `"fingerprint":"observed"`, `"fingerprint":"observed","future_security":true`, 1),
		strings.Replace(string(raw), `"fingerprint":"observed"`, `"fingerprint":"observed","fingerprint":"observed"`, 1),
		strings.Replace(string(raw), `,"fingerprint":"observed"`, "", 1),
		strings.Replace(string(raw), `/owner/native/2.1.289`, "", 1),
	} {
		if changed == string(raw) {
			t.Fatalf("invalid manifest fixture %d did not change metadata", index)
		}
		if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(changed), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := accountStateGuard(state, card); err == nil {
			t.Fatalf("target ignored invalid cache manifest metadata control %d", index)
		}
	}
}

func TestRetainedNativeIncompleteStageCustody(t *testing.T) {
	for _, shape := range []string{"empty", "partial", "unknown", "symlink", "public", "bad-name"} {
		t.Run(shape, func(t *testing.T) {
			state := t.TempDir()
			_ = os.Chmod(state, 0o700)
			store, err := accounts.Open(state)
			if err != nil {
				t.Fatal(err)
			}
			_ = store.Close()
			name := ".stage-01K00000000000000000000000"
			if shape == "bad-name" {
				name = ".stage-unknown"
			}
			dir := filepath.Join(state, "accounts", "native", name)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "claude")
			switch shape {
			case "partial":
				err = os.WriteFile(path, []byte("partial"), 0o600)
			case "unknown":
				err = os.WriteFile(filepath.Join(dir, "unknown"), []byte("preserve"), 0o600)
			case "symlink":
				err = os.Symlink("/bin/true", path)
			case "public":
				err = os.WriteFile(path, []byte("partial"), 0o644)
				if err == nil {
					err = os.Chmod(path, 0o644)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			card := CurrentManifest("v0.16.2")
			older := card
			older.AccountWorker = 2
			if err := accountStateGuard(state, older); err == nil {
				t.Fatal("old target accepted stage custody")
			}
			err = accountStateGuard(state, card)
			valid := shape == "empty" || shape == "partial"
			if valid && err != nil {
				t.Fatalf("valid crash stage refused: %v", err)
			}
			if !valid && err == nil {
				t.Fatal("unsafe crash stage accepted")
			}
		})
	}
}

func TestCodexRetentionAndEnrollmentRequireWorker4(t *testing.T) {
	for _, shape := range []string{"cache", "enrollment", "running-enrollment"} {
		t.Run(shape, func(t *testing.T) {
			state := t.TempDir()
			_ = os.Chmod(state, 0700)
			store, err := accounts.Open(state)
			if err != nil {
				t.Fatal(err)
			}
			_ = store.Close()
			version := accountconfig.CharacterizedCodexVersion
			path := filepath.Join(state, "accounts", "native", "codex-"+version, "codex")
			stamp := "sha256:" + strings.Repeat("a", 64) + ":" + strings.Repeat("b", 64)
			var file string
			var value any
			switch shape {
			case "cache":
				file = filepath.Join(filepath.Dir(path), "manifest.json")
				value = map[string]any{"version": version, "path": path, "content_sha256": strings.Repeat("a", 64), "source": persist.CLIIdentity{Path: "/owner/codex/0.160.0", Version: version, Fingerprint: "observed"}}
			case "enrollment":
				file = filepath.Join(state, "accounts", "jobs", "codex-job", enrollment.ConfigFile)
				value = enrollment.Config{SchemaVersion: enrollment.RetainedNativeConfigSchemaVersion, Provider: accounts.ProviderCodex, NativeVersion: version, NativePath: path, NativeFingerprint: stamp}
			case "running-enrollment":
				file = filepath.Join(state, "auth-watch-state.json")
				value = map[string]any{"cli_refresh": map[string]any{"source": map[string]any{"account_enrollment": map[string]any{"binding": "retained-plan"}}}}
			}
			if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(value)
			if err := os.WriteFile(file, raw, 0600); err != nil {
				t.Fatal(err)
			}
			card := CurrentManifest("v0.17.6")
			older := card
			older.AccountWorker = 3
			if err := accountStateGuard(state, older); err == nil {
				t.Fatal("older build accepted Codex/enrollment authority")
			}
			if err := accountStateGuard(state, card); err != nil {
				t.Fatal("current build rejected metadata", err)
			}
		})
	}
}
