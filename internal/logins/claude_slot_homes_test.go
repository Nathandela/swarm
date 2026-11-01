package logins

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeSlotProtectedHomes(t *testing.T) {
	t.Run("actual and promotion refs with desired C", func(t *testing.T) {
		s, accounts := claudeSlotLogins(t)
		doc := claudeSlotDocument(t, s, accounts)
		doc["claude_profiles"] = map[string]any{accounts[0].Identity: map[string]any{"provisioned": true, "cwds": []string{"/work"}}}
		writeClaudeSlotDocument(t, s, doc)

		homes, err := s.ProtectedHomes(ProviderClaude)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{s.Home(ProviderClaude), filepath.Join(s.path, "provider", "claude", "profiles", accounts[0].Identity), s.savedNativeHome(accounts[2].ID), s.savedNativeHome(accounts[0].ID), s.savedNativeHome(accounts[1].ID)}
		if strings.Join(homes, "\n") != strings.Join(want, "\n") {
			t.Fatalf("protected homes = %v, want %v", homes, want)
		}
	})

	t.Run("promotion source stays held after actual becomes target", func(t *testing.T) {
		s, accounts := claudeSlotLogins(t)
		doc := claudeSlotDocument(t, s, accounts)
		slot := doc["claude_slot"].(map[string]any)
		slot["actual"] = slot["promotion"].(map[string]any)["target"]
		writeClaudeSlotDocument(t, s, doc)

		homes, err := s.ProtectedHomes(ProviderClaude)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{s.Home(ProviderClaude), s.savedNativeHome(accounts[2].ID), s.savedNativeHome(accounts[1].ID), s.savedNativeHome(accounts[0].ID)}
		if strings.Join(homes, "\n") != strings.Join(want, "\n") {
			t.Fatalf("protected homes = %v, want %v", homes, want)
		}
	})

	t.Run("unreserved parked login excluded without promotion", func(t *testing.T) {
		s, accounts := claudeSlotLogins(t)
		doc := claudeSlotDocument(t, s, accounts)
		delete(doc["claude_slot"].(map[string]any), "promotion")
		writeClaudeSlotDocument(t, s, doc)

		homes, err := s.ProtectedHomes(ProviderClaude)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{s.Home(ProviderClaude), s.savedNativeHome(accounts[2].ID), s.savedNativeHome(accounts[0].ID)}
		if strings.Join(homes, "\n") != strings.Join(want, "\n") {
			t.Fatalf("protected homes = %v, want %v", homes, want)
		}
	})

	t.Run("deduplicates desired actual and target", func(t *testing.T) {
		s, accounts := claudeSlotLogins(t)
		doc := claudeSlotDocument(t, s, accounts)
		doc["providers"].(map[string]any)[ProviderClaude].(map[string]any)["active_id"] = accounts[1].ID
		slot := doc["claude_slot"].(map[string]any)
		slot["actual"] = slot["promotion"].(map[string]any)["target"]
		writeClaudeSlotDocument(t, s, doc)

		homes, err := s.ProtectedHomes(ProviderClaude)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{s.Home(ProviderClaude), s.savedNativeHome(accounts[1].ID), s.savedNativeHome(accounts[0].ID)}
		if strings.Join(homes, "\n") != strings.Join(want, "\n") {
			t.Fatalf("protected homes = %v, want %v", homes, want)
		}
	})

	t.Run("malformed or foreign actual refuses", func(t *testing.T) {
		for _, name := range []string{"malformed", "foreign"} {
			t.Run(name, func(t *testing.T) {
				s, accounts := claudeSlotLogins(t)
				doc := claudeSlotDocument(t, s, accounts)
				slot := doc["claude_slot"].(map[string]any)
				if name == "malformed" {
					slot["actual"] = "unknown"
				} else {
					slot["actual"].(map[string]any)["login_id"] = strings.Repeat("f", 32)
				}
				writeClaudeSlotDocument(t, s, doc)
				if _, err := s.ProtectedHomes(ProviderClaude); !errors.Is(err, ErrIneligible) {
					t.Fatalf("invalid slot ownership fell back to desired: %v", err)
				}
			})
		}
	})
}
