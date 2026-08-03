package main

// Main-level tests for the swarm-publish binary: argv parsing and the checks that must
// happen BEFORE anything touches the network or reads a credential. Nothing here contacts
// Google -- the flow itself is covered in-package by internal/play.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunRequiresEveryFlag asserts the binary refuses to run on a partial invocation
// rather than defaulting its way to the wrong app, the wrong track, or a nil credential.
// Publishing is irreversible from this side, so every target must be stated explicitly.
func TestRunRequiresEveryFlag(t *testing.T) {
	dir := t.TempDir()
	aab := filepath.Join(dir, "app.aab")
	key := filepath.Join(dir, "key.json")
	for _, f := range []string{aab, key} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
	}

	for name, args := range map[string][]string{
		"no flags at all": nil,
		"no aab":          {"--key", key, "--package", "dev.swarm.phone", "--track", "internal"},
		"no key":          {"--aab", aab, "--package", "dev.swarm.phone", "--track", "internal"},
		"no package":      {"--aab", aab, "--key", key, "--track", "internal"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(context.Background(), args); err == nil {
				t.Fatal("run returned nil for an incomplete invocation")
			}
		})
	}
}

// TestRunRejectsAnUnknownTrack pins that a mistyped track fails locally with the valid
// values named. Google's own rejection for a bad track arrives four API calls later, after
// an edit has been opened and a bundle uploaded.
func TestRunRejectsAnUnknownTrack(t *testing.T) {
	dir := t.TempDir()
	aab := filepath.Join(dir, "app.aab")
	key := filepath.Join(dir, "key.json")
	for _, f := range []string{aab, key} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
	}

	err := run(context.Background(), []string{
		"--aab", aab, "--key", key, "--package", "dev.swarm.phone", "--track", "prodcution",
	})
	if err == nil {
		t.Fatal("run accepted the track \"prodcution\"")
	}
	if !strings.Contains(err.Error(), "production") {
		t.Errorf("error does not name the valid tracks: %v", err)
	}
}

// TestRunRejectsABrokenCredentialWithoutLeakingIt covers the two things that matter about
// a bad --key: it fails, and its failure says nothing about the file's contents. A
// credential quoted into an error lands in the terminal transcript and in CI logs.
func TestRunRejectsABrokenCredentialWithoutLeakingIt(t *testing.T) {
	dir := t.TempDir()
	aab := filepath.Join(dir, "app.aab")
	if err := os.WriteFile(aab, []byte("x"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	const secret = "SUPER-SECRET-KEY-MATERIAL"
	key := filepath.Join(dir, "key.json")
	if err := os.WriteFile(key, []byte(`{"private_key":"`+secret+`"`), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	err := run(context.Background(), []string{
		"--aab", aab, "--key", key, "--package", "dev.swarm.phone", "--track", "internal",
	})
	if err == nil {
		t.Fatal("run accepted an unparseable credential")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error quotes the credential file's contents: %v", err)
	}
}

// TestRunRejectsAMissingCredentialFile pins the common operator mistake -- a wrong path --
// as a clean error rather than a nil-pointer panic further down.
func TestRunRejectsAMissingCredentialFile(t *testing.T) {
	dir := t.TempDir()
	aab := filepath.Join(dir, "app.aab")
	if err := os.WriteFile(aab, []byte("x"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	err := run(context.Background(), []string{
		"--aab", aab, "--key", filepath.Join(dir, "absent.json"),
		"--package", "dev.swarm.phone", "--track", "internal",
	})
	if err == nil {
		t.Fatal("run returned nil for a missing credential file")
	}
}
