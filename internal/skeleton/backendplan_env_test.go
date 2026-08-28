package skeleton

// The differential this file pins was first observed live (2026-08-28, VM samoa): a daemon
// started outside a login shell carried PATH=/usr/bin:/bin, and every codex session it
// launched came up as a working PTY with NO backend -- "no session backend planned: backend
// Program \"codex\" does not resolve on PATH" -- even when the SPAWNING CLIENT's env resolved
// codex fine. The agent's argv0 resolves against the launch env (lookPathIn); the backend
// program resolved against the daemon's own (launchEnv). One binary, two resolutions, two
// different answers.
//
// The fix threads the launch's resolved agent env through daemon.BackendPlanner into
// planSessionBackend, so both resolutions read the same PATH. These tests are the S-6-style
// differential (daemon env A vs launch env B) for that seam.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFakeCodex places an executable file NAMED codex in a fresh temp dir and returns the
// dir. Backend planning only LookPaths the program (backendProber.Run refuses to run
// anything), so an empty executable is a sufficient witness.
func writeFakeCodex(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestBackendPlanResolvesAgainstTheLaunchEnvNotTheDaemons is the fix's primary witness: the
// daemon's own PATH sees no codex, the launch env does, and the backend MUST plan -- with the
// program resolved to the launch env's binary.
//
// MUTATION: revert planSessionBackend to backendProber{env: d.launchEnv()} and this fails
// exactly as the field failure read: "not found on the agent PATH".
func TestBackendPlanResolvesAgainstTheLaunchEnvNotTheDaemons(t *testing.T) {
	bin := writeFakeCodex(t)
	t.Setenv("PATH", filepath.Join(t.TempDir(), "empty")) // the daemon's own PATH: no codex anywhere

	d := &Daemon{}
	sessionDir := t.TempDir()
	sock := filepath.Join(sessionDir, "codex.sock")
	agentEnv := []string{"PATH=" + bin, "HOME=" + t.TempDir()}

	spec, err := d.planSessionBackend("codex", sessionDir, sock, agentEnv)
	if err != nil {
		t.Fatalf("planSessionBackend with a resolving launch env: %v", err)
	}
	if spec == nil {
		t.Fatal("planSessionBackend planned no backend although the launch env resolves codex")
	}
	if want := filepath.Join(bin, "codex"); spec.Program != want {
		t.Fatalf("backend program = %q, want the launch env's %q", spec.Program, want)
	}
}

// TestBackendPlanFailsWhenTheLaunchEnvCannotResolve is the differential's other arm: a
// supplied launch env is AUTHORITATIVE. When it cannot resolve the program, a daemon PATH
// that could is not consulted -- the agent's own argv0 would fail on the same env, and a
// backend the agent cannot be launched against serves nobody.
func TestBackendPlanFailsWhenTheLaunchEnvCannotResolve(t *testing.T) {
	bin := writeFakeCodex(t)
	t.Setenv("PATH", bin) // the daemon's own PATH WOULD resolve codex

	d := &Daemon{}
	sessionDir := t.TempDir()
	sock := filepath.Join(sessionDir, "codex.sock")
	agentEnv := []string{"PATH=" + filepath.Join(t.TempDir(), "empty")}

	_, err := d.planSessionBackend("codex", sessionDir, sock, agentEnv)
	if err == nil {
		t.Fatal("planSessionBackend consulted an env other than the supplied launch env")
	}
	if !strings.Contains(err.Error(), "not found on the agent PATH") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestBackendPlanWithNoLaunchEnvFallsBackToDaemonPolicy pins the nil contract the seam
// documents: a caller that supplies no env gets the pre-fix behavior, daemon policy.
func TestBackendPlanWithNoLaunchEnvFallsBackToDaemonPolicy(t *testing.T) {
	bin := writeFakeCodex(t)
	t.Setenv("PATH", bin)

	d := &Daemon{}
	sessionDir := t.TempDir()
	sock := filepath.Join(sessionDir, "codex.sock")

	spec, err := d.planSessionBackend("codex", sessionDir, sock, nil)
	if err != nil {
		t.Fatalf("planSessionBackend with nil env against a resolving daemon PATH: %v", err)
	}
	if spec == nil {
		t.Fatal("nil launch env must fall back to daemon policy, which resolves codex here")
	}
}
