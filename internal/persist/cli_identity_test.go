package persist

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIIdentityRoundTrip(t *testing.T) {
	id := &CLIIdentity{Path: "/bin/provider", Version: "2.3.4", Fingerprint: "observed"}
	data, err := json.Marshal(Meta{SchemaVersion: SchemaVersion, CLIIdentity: id})
	if err != nil {
		t.Fatal(err)
	}
	var got Meta
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.CLIIdentity == nil || *got.CLIIdentity != *id || got.SchemaVersion != 1 {
		t.Fatalf("bad roundtrip: %+v", got)
	}
	data, err = json.Marshal(Meta{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "cli_identity") {
		t.Fatal("unknown baseline should be omitted")
	}
}

func TestCLIFingerprintAtomicReplacement(t *testing.T) {
	dir := t.TempDir()
	path, tmp := filepath.Join(dir, "cli"), filepath.Join(dir, "next")
	const content = "#!/bin/sh\nexit 0\n"
	if err := os.WriteFile(path, []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
	first, err := CLIFingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmp, []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(tmp, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	second, err := CLIFingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("same-size, same-mtime atomic replacement missed")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := CLIFingerprint(path); err == nil {
		t.Fatal("accepted non-executable")
	}
}
