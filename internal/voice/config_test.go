package voice

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func testConfig() Config {
	return Config{Version: 2, PlaceID: "place-1", Name: "My computer", Provider: "codex", ConversationID: "conversation-1", WorkDir: "/workspace", DaemonSocket: "/state/daemon.sock", Language: "en", VoiceID: "marin", Mode: "conversation"}
}

func privateConfigDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "voice")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestConfigRoundTripAndPrivateCustody(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "voice")
	want := testConfig()
	if err := SaveConfig(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig(dir)
	if err != nil || got != want {
		t.Fatalf("config mismatch: %+v %v", got, err)
	}
	for path, mode := range map[string]os.FileMode{dir: 0700, filepath.Join(dir, "config.json"): 0600} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("private mode: %v %v", info, err)
		}
	}
	if err := SaveOpenAIKey(dir, strings.NewReader("test-secret\n")); err != nil {
		t.Fatal(err)
	}
	key, err := LoadOpenAIKey(dir)
	if err != nil || key != "test-secret" {
		t.Fatal("key round trip failed", err)
	}
	info, err := os.Stat(filepath.Join(dir, "openai.key"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("key is not private")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil || strings.Contains(string(raw), key) {
		t.Fatal("config contains key", err)
	}
}

func TestConfigRejectsMalformedAndFutureWithoutReset(t *testing.T) {
	raw, _ := json.Marshal(testConfig())
	for name, bad := range map[string]string{
		"future":    strings.Replace(string(raw), `"version":2`, `"version":3`, 1),
		"duplicate": strings.Replace(string(raw), `"version":2`, `"version":2,"version":2`, 1),
		"case":      strings.Replace(string(raw), `"provider"`, `"Provider"`, 1),
		"unknown":   strings.Replace(string(raw), `"version":2`, `"unknown":1,"version":1`, 1),
		"surrogate": strings.Replace(string(raw), `"My computer"`, `"\ud800"`, 1),
		"trailing":  string(raw) + `{}`,
		"oversized": strings.Repeat(" ", 16<<10) + string(raw),
	} {
		t.Run(name, func(t *testing.T) {
			dir := privateConfigDir(t)
			path := filepath.Join(dir, "config.json")
			if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadConfig(dir); err == nil {
				t.Fatal("accepted malformed config")
			}
			after, _ := os.ReadFile(path)
			if string(after) != bad {
				t.Fatal("bad config was changed")
			}
		})
	}
}

func TestConfigValidation(t *testing.T) {
	for _, mutate := range []func(*Config){
		func(c *Config) { c.Provider = "other" }, func(c *Config) { c.PlaceID = "" },
		func(c *Config) { c.ConversationID = "" }, func(c *Config) { c.WorkDir = "relative" },
		func(c *Config) { c.DaemonSocket = "/tmp/a/../b" }, func(c *Config) { c.Region = "other" },
		func(c *Config) { c.Language = "unsupported" }, func(c *Config) { c.Mode = "always" },
		func(c *Config) { c.Name = "a\nb" }, func(c *Config) { c.VoiceID = "" },
	} {
		c := testConfig()
		mutate(&c)
		if err := SaveConfig(privateConfigDir(t), c); err == nil {
			t.Fatalf("accepted invalid config: %+v", c)
		}
	}
}

func TestPrivateVoiceFilesRejectLinksAndPublicSecrets(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "public", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			dir := privateConfigDir(t)
			path := filepath.Join(dir, "openai.key")
			target := filepath.Join(t.TempDir(), "key")
			if err := os.WriteFile(target, []byte("test-secret"), 0600); err != nil {
				t.Fatal(err)
			}
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink(target, path)
			case "hardlink":
				err = os.Link(target, path)
			case "public":
				err = os.WriteFile(path, []byte("test-secret"), 0644)
			case "fifo":
				err = syscall.Mkfifo(path, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := LoadOpenAIKey(dir); err == nil {
				t.Fatal("accepted unsafe secret")
			}
			if err := SaveOpenAIKey(dir, strings.NewReader("replacement")); err == nil {
				t.Fatal("replaced unsafe secret")
			}
			got, _ := os.ReadFile(target)
			if string(got) != "test-secret" {
				t.Fatal("changed link target")
			}
		})
	}
}

func TestInvalidKeyPreservesStoredKeyAndRedactsErrors(t *testing.T) {
	dir := privateConfigDir(t)
	if err := SaveOpenAIKey(dir, strings.NewReader("original")); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"", "secret\nsecond", "secret\x00", "secreté", strings.Repeat("x", 4097)} {
		err := SaveOpenAIKey(dir, strings.NewReader(key))
		if err == nil {
			t.Fatal("accepted invalid key")
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatal("error exposes input")
		}
		got, err := LoadOpenAIKey(dir)
		if err != nil || got != "original" {
			t.Fatal("invalid import changed stored key", err)
		}
	}
}

func TestOldSpeechConfigRequiresExplicitSetup(t *testing.T) {
	dir := privateConfigDir(t)
	old := testConfig()
	old.Version, old.Region, old.VoiceID = 1, "eu", "gradium-voice"
	raw, _ := json.Marshal(old)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(dir); err == nil || !strings.Contains(err.Error(), "swarm voice setup") {
		t.Fatalf("legacy configuration: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gradium.key"), []byte("old-key"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOpenAIKey(dir); !os.IsNotExist(err) {
		t.Fatalf("old provider key reused: %v", err)
	}
	if err := SetupOpenAISpeech(dir, strings.NewReader("new-key")); err != nil {
		t.Fatal(err)
	}
	want := old
	want.Version, want.Region, want.VoiceID = 2, "", "marin"
	got, err := LoadConfig(dir)
	if err != nil || got != want {
		t.Fatalf("migration got %+v: %v", got, err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "gradium.key")); string(got) != "old-key" {
		t.Fatal("legacy key was modified")
	}
}
