package skeleton

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHermesIdentityAcceptsNativeStableVersionPrefix(t *testing.T) {
	program := filepath.Join(t.TempDir(), "hermes")
	if err := os.WriteFile(program, []byte("#!/bin/sh\nprintf 'Hermes Agent v0.22.5 (2026.10.22)\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	identity, err := probeCLIIdentity("hermes", program, []string{"PATH=" + os.Getenv("PATH")}, t.TempDir())
	if err != nil || identity == nil || identity.Version != "0.22.5" {
		t.Fatalf("stable Hermes native identity unavailable: %+v %v", identity, err)
	}
}
