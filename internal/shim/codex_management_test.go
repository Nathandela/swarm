package shim

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
)

func TestCodexManagementEnvironmentRejectsUnknownAdvancedWrapper(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho 'codex-cli 0.163.0'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := Config{NativeProvider: logins.ProviderCodex, Argv: []string{path}, Env: []string{"HOME=/ordinary", "CODEX_MANAGED_PACKAGE_ROOT=/untrusted", "CODEX_MANAGED_BY_BUN=1"}, Backend: &BackendConfig{Program: path, Env: []string{"CODEX_MANAGED_BY_NPM=untrusted"}}}
	if err := resolveCodexManagementEnvironment(&cfg); err != nil {
		t.Fatalf("ordinary unknown-wrapper fallback disappeared: %v", err)
	}
	if strings.Join(cfg.Env, "\n") != "HOME=/ordinary" || len(cfg.Backend.Env) != 0 {
		t.Fatalf("inherited management selectors survived: %v %v", cfg.Env, cfg.Backend.Env)
	}
	cfg.LoginRuntime = &logins.Runtime{Provider: logins.ProviderCodex, ExternalAuth: true}
	cfg.CLIIdentity = &persist.CLIIdentity{Path: path, Version: "0.163.0", Fingerprint: "untrusted"}
	if err := resolveCodexManagementEnvironment(&cfg); err == nil {
		t.Fatal("version-only wrapper admitted external native authentication")
	}
}
