package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/shim"
)

func TestManagedOpenCodeShimRequiresExecutionCriticalFlag(t *testing.T) {
	dir := t.TempDir()
	lc := shimLaunchConfig{SessionDir: dir, Argv: []string{"/native/opencode"}, OpenCode: &shim.OpenCodeConfig{RootID: "ses_" + strings.Repeat("a", 32), Generation: strings.Repeat("b", 32), BindingFile: filepath.Join(dir, shim.OpenCodeBindingFile), EmbargoToken: strings.Repeat("c", 64)}}
	if validateShimRuntime(lc, false) == nil {
		t.Fatal("runtime state silently accepted without required flag")
	}
	if validateShimRuntime(shimLaunchConfig{}, true) == nil {
		t.Fatal("required flag accepted without runtime state")
	}
	if err := validateShimRuntime(lc, true); err != nil {
		t.Fatal(err)
	}
	if err := validateShimRuntime(shimLaunchConfig{}, false); err != nil {
		t.Fatal("ordinary shim rejected")
	}
	var stderr bytes.Buffer
	if code := runShim([]string{"--require-opencode-tui"}, &bytes.Buffer{}, &stderr); code != 2 {
		t.Fatalf("missing config exited %d", code)
	}
	if strings.Contains(stderr.String(), "flag provided but not defined") {
		t.Fatal("new required flag is not wired")
	}
}
