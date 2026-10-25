package logins

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/Nathandela/swarm/internal/processcontain"
)

func writeClaudeSetupSource(t *testing.T, common string, fields map[string]any) []byte {
	t.Helper()
	path := filepath.Join(common, ".claude.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err = json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	for key, value := range fields {
		object[key] = value
	}
	raw, err = json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return raw
}

func readClaudeSetupProfile(t *testing.T, s *Store, identity string) map[string]any {
	t.Helper()
	home, err := s.ClaudeProfileHome(identity)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err = json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	return object
}

func TestClaudeProfileSetupInitialInheritance(t *testing.T) {
	s, login, common := profileFixture(t)
	before := writeClaudeSetupSource(t, common, map[string]any{
		"hasCompletedOnboarding": true, "lastOnboardingVersion": "2.0.41", "theme": "light",
		"primaryApiKey": "private-api-key", "customApiKeyResponses": map[string]any{"approved": []string{"private-key"}},
		"bypassPermissionsModeAccepted": true, "enabledMcpjsonServers": []string{"private-approval"},
		"hasCompletedClaudeInChromeOnboarding": true, "unknownPreference": "preserve-only-in-source",
	})
	if _, err := s.ProvisionClaudeProfile(snapshot(t, s).Revision, login.ID, []string{"/work"}); err != nil {
		t.Fatal(err)
	}
	got := readClaudeSetupProfile(t, s, login.Identity)
	for key, want := range map[string]any{"hasCompletedOnboarding": true, "lastOnboardingVersion": "2.0.41", "theme": "light"} {
		if !reflect.DeepEqual(got[key], want) {
			t.Fatalf("initial profile %s=%v; want %v", key, got[key], want)
		}
	}
	if len(got) != 5 || got["mcpServers"] == nil || got["projects"] == nil {
		t.Fatal("profile must contain only setup allowlist and explicit MCP definitions")
	}
	project := got["projects"].(map[string]any)["/work"].(map[string]any)
	if len(project) != 1 || project["mcpServers"] == nil {
		t.Fatal("inherited project trust or unrelated project state")
	}
	after, err := os.ReadFile(filepath.Join(common, ".claude.json"))
	if err != nil || string(before) != string(after) {
		t.Fatal("source changed")
	}
	home, _ := s.ClaudeProfileHome(login.Identity)
	for _, name := range claudeProfileAssets {
		target, err := os.Readlink(filepath.Join(home, name))
		if err != nil || target != filepath.Join(common, name) {
			t.Fatalf("workflow alias changed: %s", name)
		}
	}
}

func TestClaudeProfileSetupFieldPolicy(t *testing.T) {
	for _, theme := range []string{"auto", "dark", "light", "dark-daltonized", "light-daltonized", "dark-ansi", "light-ansi"} {
		t.Run(theme, func(t *testing.T) {
			s, login, common := profileFixture(t)
			writeClaudeSetupSource(t, common, map[string]any{"theme": theme, "hasCompletedOnboarding": false, "lastOnboardingVersion": []any{"ignored-without-completion"}})
			if _, err := s.ProvisionClaudeProfile(snapshot(t, s).Revision, login.ID, nil); err != nil {
				t.Fatal(err)
			}
			got := readClaudeSetupProfile(t, s, login.Identity)
			if got["theme"] != theme || got["hasCompletedOnboarding"] != nil || got["lastOnboardingVersion"] != nil {
				t.Fatal("theme manufactured completed setup")
			}
		})
	}
	for _, body := range []string{`{}`, `{"unknownPreference":{"keep":"only source"}}`, `{"hasCompletedOnboarding":false}`, `{"hasCompletedOnboarding":true,"lastOnboardingVersion":"2.1.310"}`} {
		t.Run(body, func(t *testing.T) {
			s, login, common := profileFixture(t)
			if err := os.WriteFile(filepath.Join(common, ".claude.json"), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ProvisionClaudeProfile(snapshot(t, s).Revision, login.ID, nil); err != nil {
				t.Fatal(err)
			}
			got := readClaudeSetupProfile(t, s, login.Identity)
			if got["theme"] != nil || got["unknownPreference"] != nil || (got["hasCompletedOnboarding"] == true) != strings.Contains(body, `:true`) {
				t.Fatal("missing fields were inferred")
			}
		})
	}
	t.Run("missing-source", func(t *testing.T) {
		s, login, common := profileFixture(t)
		if err := os.Remove(filepath.Join(common, ".claude.json")); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ProvisionClaudeProfile(snapshot(t, s).Revision, login.ID, nil); err != nil {
			t.Fatal(err)
		}
		if len(readClaudeSetupProfile(t, s, login.Identity)) != 0 {
			t.Fatal("missing source manufactured setup")
		}
	})
}

func TestClaudeProfileSetupRejectsInvalidSelectedFields(t *testing.T) {
	cases := map[string]string{
		"completion-null":   `{"hasCompletedOnboarding":null}`,
		"completion-string": `{"hasCompletedOnboarding":"source-private-value"}`,
		"completion-number": `{"hasCompletedOnboarding":1}`,
		"completion-object": `{"hasCompletedOnboarding":{}}`,
		"completion-array":  `{"hasCompletedOnboarding":[]}`,
		"version-missing":   `{"hasCompletedOnboarding":true}`,
		"version-null":      `{"hasCompletedOnboarding":true,"lastOnboardingVersion":null}`,
		"version-number":    `{"hasCompletedOnboarding":true,"lastOnboardingVersion":2}`,
		"theme-null":        `{"theme":null}`,
		"theme-object":      `{"theme":{}}`,
		"theme-number":      `{"theme":1}`,
		"theme-custom":      `{"theme":"custom:source-private-value"}`,
		"theme-unknown":     `{"theme":"source-private-value"}`,
		"duplicate":         `{"hasCompletedOnboarding":true,"hasCompletedOnboarding":false}`,
		"nonobject":         `[]`,
		"trailing":          `{} {}`,
	}
	for _, version := range []string{"", "2", "2.1", "v2.1.310", " 2.1.310", "2.1.310 ", "02.1.310", "2.01.310", "2.1.0310", "2.1.310-beta", "2.1.310+build", "source-private-value", strings.Repeat("1", 33) + ".1.1"} {
		encoded, _ := json.Marshal(version)
		cases["version-"+version] = `{"hasCompletedOnboarding":true,"lastOnboardingVersion":` + string(encoded) + `}`
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			s, login, common := profileFixture(t)
			if err := os.WriteFile(filepath.Join(common, ".claude.json"), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := s.ProvisionClaudeProfile(snapshot(t, s).Revision, login.ID, nil)
			if !errors.Is(err, ErrIneligible) || strings.Contains(err.Error(), "source-private-value") {
				t.Fatalf("wrong or private error: %v", err)
			}
			if snapshot(t, s).ClaudeProfiles[login.Identity].Provisioned {
				t.Fatal("invalid setup published provisioned")
			}
			home, _ := s.ClaudeProfileHome(login.Identity)
			if _, err := os.Lstat(filepath.Join(home, ".claude.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid setup published partial config")
			}
		})
	}
}

func TestClaudeProfileSetupSourceSafety(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "fifo", "permissions", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			s, login, common := profileFixture(t)
			path := filepath.Join(common, ".claude.json")
			switch kind {
			case "symlink", "hardlink":
				other := filepath.Join(common, "setup-source")
				if err := os.Rename(path, other); err != nil {
					t.Fatal(err)
				}
				var err error
				if kind == "symlink" {
					err = os.Symlink(other, path)
				} else {
					err = os.Link(other, path)
				}
				if err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := syscall.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
			case "permissions":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "oversized":
				if err := os.WriteFile(path, []byte(`{"unknown":"`+strings.Repeat("x", maxCredentialBytes)+`"}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.ProvisionClaudeProfile(snapshot(t, s).Revision, login.ID, nil); !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("unsafe source accepted: %v", err)
			}
		})
	}
}

func TestClaudeProfileSetupRetryNeverBackfillsExistingConfig(t *testing.T) {
	for _, kind := range []string{"before-rename", "visible-rename", "empty-config", "empty-file", "native-fields"} {
		t.Run(kind, func(t *testing.T) {
			s, login, common := profileFixture(t)
			writeClaudeSetupSource(t, common, map[string]any{"hasCompletedOnboarding": true, "lastOnboardingVersion": "2.0.41", "theme": "light"})
			home, _ := s.ClaudeProfileHome(login.Identity)
			visible := false
			s.writeOps.rename = func(old, new string) error {
				if strings.HasSuffix(new, ".claude.json") {
					if kind != "visible-rename" {
						return syscall.ENOSPC
					}
					visible = true
				}
				return s.root.Rename(old, new)
			}
			s.writeOps.syncDir = func(dir string) error {
				if visible {
					return syscall.EIO
				}
				return syncRootDir(s.root, dir)
			}
			_, err := s.ProvisionClaudeProfile(snapshot(t, s).Revision, login.ID, []string{"/work"})
			if kind == "visible-rename" {
				if !errors.Is(err, ErrDurabilityUncertain) {
					t.Fatal(err)
				}
			} else if !errors.Is(err, syscall.ENOSPC) {
				t.Fatal(err)
			}
			if snapshot(t, s).ClaudeProfiles[login.Identity].Provisioned {
				t.Fatal("failed write published profile")
			}
			for path, body := range map[string]string{"empty-config": "{}", "empty-file": "", "native-fields": `{"theme":"dark","hasCompletedOnboarding":false,"nativeField":"keep"}`} {
				if kind == path {
					if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(body), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if kind == "before-rename" {
				writeClaudeSetupSource(t, common, map[string]any{"lastOnboardingVersion": "2.1.310", "theme": "dark"})
			} else {
				writeClaudeSetupSource(t, common, map[string]any{"theme": "custom:source-private-value"})
			}
			fresh, err := OpenExisting(s.StateRoot())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = fresh.Close() }()
			fresh.grantWriters = func([]string, []string) ([]processcontain.Identity, error) { return nil, nil }
			if _, err = fresh.ProvisionClaudeProfile(snapshot(t, fresh).Revision, login.ID, []string{"/work"}); err != nil {
				t.Fatal(err)
			}
			got := readClaudeSetupProfile(t, fresh, login.Identity)
			switch kind {
			case "before-rename":
				if got["theme"] != "dark" || got["lastOnboardingVersion"] != "2.1.310" {
					t.Fatal("first write did not use current valid source")
				}
			case "visible-rename":
				if got["theme"] != "light" || got["lastOnboardingVersion"] != "2.0.41" {
					t.Fatal("visible rename re-inherited")
				}
			case "native-fields":
				if got["theme"] != "dark" || got["hasCompletedOnboarding"] != false || got["nativeField"] != "keep" || got["lastOnboardingVersion"] != nil {
					t.Fatal("pending native fields overwritten")
				}
			default:
				if got["theme"] != nil || got["hasCompletedOnboarding"] != nil || got["lastOnboardingVersion"] != nil {
					t.Fatal("existing empty destination backfilled")
				}
			}
		})
	}
}

func TestClaudeProfileSetupEstablishedAndSecondProfile(t *testing.T) {
	s, login, common := profileFixture(t)
	writeClaudeSetupSource(t, common, map[string]any{"hasCompletedOnboarding": true, "lastOnboardingVersion": "2.0.41", "theme": "light"})
	if _, err := s.ProvisionClaudeProfile(snapshot(t, s).Revision, login.ID, []string{"/work"}); err != nil {
		t.Fatal(err)
	}
	home, _ := s.ClaudeProfileHome(login.Identity)
	got := readClaudeSetupProfile(t, s, login.Identity)
	got["theme"], got["hasCompletedOnboarding"], got["lastOnboardingVersion"] = "dark", false, "native-original"
	got["oauthAccount"], got["privateNative"] = map[string]any{"accountUuid": "native-a"}, "keep"
	got["projects"].(map[string]any)["/work"].(map[string]any)["hasTrustDialogAccepted"] = true
	raw, _ := json.Marshal(got)
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, theme := range []string{"auto", "custom:source-private-value"} {
		cwd := "/new-" + strings.Split(theme, ":")[0]
		newScope := map[string]any{"mcpServers": map[string]any{"added": map[string]any{"command": "added-server"}}}
		writeClaudeSetupSource(t, common, map[string]any{"theme": theme, "lastOnboardingVersion": "2.1.310", "projects": map[string]any{cwd: newScope}})
		if _, err := s.ProvisionClaudeProfile(snapshot(t, s).Revision, login.ID, []string{"/work"}); err != nil {
			t.Fatal(err)
		}
		if after, err := os.ReadFile(filepath.Join(home, ".claude.json")); err != nil || string(after) != string(raw) {
			t.Fatal("established no-op changed config")
		}
		if _, err := s.ProvisionClaudeProfile(snapshot(t, s).Revision, login.ID, []string{cwd}); err != nil {
			t.Fatal(err)
		}
		got["projects"].(map[string]any)[cwd] = newScope
		if !reflect.DeepEqual(readClaudeSetupProfile(t, s, login.Identity), got) {
			t.Fatal("CWD extension changed private native fields")
		}
		after, err := os.ReadFile(filepath.Join(home, ".claude.json"))
		if err != nil {
			t.Fatal(err)
		}
		raw = after
	}
	writeClaudeSetupSource(t, common, map[string]any{"theme": "light-ansi", "lastOnboardingVersion": "2.1.310"})
	second := add(t, s, ProviderClaude, "B", claudeCredential("b", "b"))
	if _, err := s.ProvisionClaudeProfile(snapshot(t, s).Revision, second.ID, nil); err != nil {
		t.Fatal(err)
	}
	secondConfig := readClaudeSetupProfile(t, s, second.Identity)
	if secondConfig["theme"] != "light-ansi" || secondConfig["lastOnboardingVersion"] != "2.1.310" || secondConfig["hasCompletedOnboarding"] != true || secondConfig["oauthAccount"] != nil {
		t.Fatal("second profile did not independently inherit safe current source")
	}
}
