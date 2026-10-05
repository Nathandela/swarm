package persist

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCLIContentFingerprintRejectsMetadataPreservingTamperAndOldReaders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cli")
	if err := os.WriteFile(path, []byte("synthetic executable bytes"), 0o500); err != nil {
		t.Fatal(err)
	}
	metadata, err := CLIFingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	content, err := CLIContentFingerprint(path)
	if err != nil || !IsCLIContentFingerprint(content) || !MatchCLIFingerprint(path, content) {
		t.Fatalf("invalid content identity: %q %v", content, err)
	}
	// Existing actors compare their metadata-only stamp directly to the opaque
	// persisted stamp. They refuse before their environment/profile/native work.
	if metadata == content {
		t.Fatal("old metadata reader accepted retained stamp")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("Synthetic executable bytes"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	unchanged, err := CLIFingerprint(path)
	if err != nil || unchanged != metadata {
		t.Fatal("tamper fixture did not preserve metadata")
	}
	if MatchCLIFingerprint(path, content) {
		t.Fatal("content stamp accepted altered bytes")
	}
}

func TestCLIContentFingerprintBoundsAndPrivateCustody(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cli")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o500)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(1<<30 + 1); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if _, err := CLIContentFingerprint(path); err == nil {
		t.Fatal("oversized retained executable accepted")
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("private fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := CLIContentFingerprint(path); err == nil {
		t.Fatal("nonprivate retained executable accepted")
	}
}
