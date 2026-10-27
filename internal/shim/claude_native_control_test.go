package shim

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestClaudeNativeControlShimRemainsDormantBeforeCustody(t *testing.T) {
	cfg, _, _ := claudeClaimFixture(t)
	cfg.RequiredRuntime = "claude-native-control"
	if _, err := Run(cfg); !errors.Is(err, ErrClaudeNativeControlDormant) {
		t.Fatalf("native birth was not held at dormant boundary: %v", err)
	}
	entries, err := os.ReadDir(cfg.SessionDir)
	if err != nil || len(entries) != 0 {
		t.Fatal("dormant runtime performed custody/provider setup", err)
	}
}

func TestClaudeNativeControlClaimRequiresMatchingImmutableRuntime(t *testing.T) {
	for _, kind := range []string{"exact", "stripped", "other", "old-config"} {
		t.Run(kind, func(t *testing.T) {
			cfg, scope, meta := claudeClaimFixture(t)
			cfg.RequiredRuntime, meta.RequiredRuntime = "claude-native-control", "claude-native-control"
			if kind == "stripped" {
				meta.RequiredRuntime = ""
			}
			if kind == "other" {
				meta.RequiredRuntime = "claude-chat"
			}
			if kind == "old-config" {
				cfg.RequiredRuntime = ""
			}
			raw, _ := json.Marshal(meta)
			if err := os.WriteFile(filepath.Join(cfg.SessionDir, "meta.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			err := scope.claimClaudeReservation(cfg)
			if (err == nil) != (kind == "exact") || scope.ownPreclaimDurable != (kind == "exact") {
				t.Fatalf("runtime claim accepted=%t durable=%t case=%s: %v", err == nil, scope.ownPreclaimDurable, kind, err)
			}
		})
	}
}
