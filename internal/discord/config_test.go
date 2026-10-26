package discord

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleConfig(dir string) Config {
	return Config{Version: 1, InstallationID: "install-123", Name: "Atlas 🌍", ApplicationID: "123", OwnerID: "456", TokenFile: filepath.Join(dir, "token"), DaemonSocket: filepath.Join(dir, "daemon.sock"), GuildIDs: []string{"789"}, UserIDs: []string{"456"}, ReceptionistCLI: "codex", WorkDir: dir}
}
func TestConfigRoundTripAndPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "place")
	c := sampleConfig(dir)
	if err := SaveConfig(dir, c); err != nil {
		t.Fatal(err)
	}
	os.Chmod(dir, 0755)
	os.Chmod(ConfigPath(dir), 0644)
	got, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != c.Name || got.InstallationID != c.InstallationID {
		t.Fatalf("got %+v", got)
	}
	for _, p := range []string{dir, ConfigPath(dir)} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0600)
		if p == dir {
			want = 0700
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s mode %o", p, info.Mode().Perm())
		}
	}
	got.Name = "別の場所"
	if err := SaveConfig(dir, got); err != nil {
		t.Fatal(err)
	}
	renamed, err := LoadConfig(dir)
	if err != nil || renamed.InstallationID != c.InstallationID {
		t.Fatalf("rename: %+v %v", renamed, err)
	}
	other := sampleConfig(filepath.Join(t.TempDir(), "other"))
	other.InstallationID = "other"
	if err := SaveConfig(filepath.Dir(other.TokenFile), other); err != nil {
		t.Fatal(err)
	}
}
func TestConfigRejectsMalformedAndSecretDiagnostics(t *testing.T) {
	dir := t.TempDir()
	path := ConfigPath(dir)
	for _, raw := range []string{`{"version":99}`, `{"version":1,"token":"sensitive-value"}`, `{broken sensitive-value`} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := LoadConfig(dir)
		if err == nil || strings.Contains(err.Error(), "sensitive-value") {
			t.Fatalf("error %v", err)
		}
	}
	c := sampleConfig(dir)
	c.Name = "bad\nname"
	if err := SaveConfig(dir, c); err == nil {
		t.Fatal("accepted control character")
	}
}
func TestLoadToken(t *testing.T) {
	p := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(p, []byte("secret\n"), 0644); err != nil {
		t.Fatal(err)
	}
	s, err := LoadToken(p)
	if err != nil || s != "secret" {
		t.Fatalf("%q %v", s, err)
	}
	info, _ := os.Stat(p)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
}

func TestLoadTokenRejectsSymlinkAndHidesPath(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "sensitive-value")
	if err := os.WriteFile(target, []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadToken(link); err == nil || strings.Contains(err.Error(), "sensitive-value") {
		t.Fatalf("error %v", err)
	}
	info, _ := os.Stat(target)
	if info.Mode().Perm() != 0644 {
		t.Fatal("changed symlink target")
	}
	if _, err := LoadToken(filepath.Join(dir, "missing-sensitive-value")); err == nil || strings.Contains(err.Error(), "sensitive-value") {
		t.Fatalf("error %v", err)
	}
}
