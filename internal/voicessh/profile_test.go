package voicessh

import (
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSSHVoiceProfileRejectsReadableOrLinkedSecret(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	files, err := openProfileFiles(dir, "fixture", true)
	if err != nil {
		t.Fatal(err)
	}
	defer files.close()
	expected := profile{Version: 1, Seed: make([]byte, ed25519.SeedSize), Identity: strings.Repeat("a", 64)}
	if err = files.write(expected); err != nil {
		t.Fatal(err)
	}
	location := filepath.Join(dir, files.name)
	if err = os.Chmod(location, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = files.read(); !errors.Is(err, ErrProfile) {
		t.Fatal("public-readable device private key was accepted", err)
	}
	if err = os.Chmod(location, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Link(location, filepath.Join(dir, "other")); err != nil {
		t.Fatal(err)
	}
	if _, err = files.read(); !errors.Is(err, ErrProfile) {
		t.Fatal("multiply linked device private key was accepted", err)
	}
}
