package skeleton

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/agyinput"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

func TestAgyStatusLineBinaryStripsLinuxDeletedSuffix(t *testing.T) {
	argv := agyStatusLineArgsWithBinary([]string{"/usr/local/bin/agy"}, []string{"HOME=" + t.TempDir()}, "/usr/local/bin/swarm (deleted)")
	if len(argv) < 3 || strings.Contains(argv[2], "(deleted)") || !strings.Contains(argv[2], `'/usr/local/bin/swarm' hook AgyStatusLine`) {
		t.Fatalf("statusLine args retained (deleted) suffix: %v", argv)
	}
}

func TestReadAgySettingsBounded_HandlesUTF8BOMAndRejectsOversizedFile(t *testing.T) {
	home := t.TempDir()
	settingsPath := filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}

	// UTF-8 BOM prefix must still chain user's statusLine command.
	bomJSON := "\xef\xbb\xbf" + `{"statusLine":{"type":"command","command":"printf ok"}}`
	if err := os.WriteFile(settingsPath, []byte(bomJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	argv := agyStatusLineArgsWithBinary([]string{"/usr/local/bin/agy"}, []string{"HOME=" + home}, "/opt/bin/swarm")
	if len(argv) < 3 || !strings.Contains(argv[2], "printf ok") {
		t.Fatalf("UTF-8 BOM settings failed to chain custom statusLine: %v", argv)
	}

	// Oversized settings (> maxAgySettingsBytes) must return nil rather than reading a truncated prefix.
	oversized := `{"statusLine":{"type":"command","command":"printf ok"},"pad":"` + strings.Repeat("a", maxAgySettingsBytes+64) + `"}`
	if err := os.WriteFile(settingsPath, []byte(oversized), 0o600); err != nil {
		t.Fatal(err)
	}
	if raw := readAgySettingsBounded(settingsPath); raw != nil {
		t.Fatalf("oversized settings returned %d bytes, want nil", len(raw))
	}
}

func TestCLIRefreshSkipsAgySessionWithRegisteredNativeInput(t *testing.T) {
	oldID := &persist.CLIIdentity{
		Path:        "/usr/local/bin/agy",
		Version:     "1.4.7",
		Fingerprint: "sha256:old",
	}
	newID := &persist.CLIIdentity{
		Path:        "/usr/local/bin/agy",
		Version:     "1.5.10",
		Fingerprint: "sha256:new",
	}

	f := newAuthFake("")
	f.add(persist.Meta{
		ID:             "discordagy000001",
		AgentType:      "agy",
		Cwd:            "/work/agy",
		Env:            []string{"PATH=/usr/local/bin"},
		ConversationID: "50000000-0000-4000-8000-000000000099",
		CLIIdentity:    oldID,
		Status:         status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone},
	})

	w := testWatcher(t, f)
	w.agents = []string{"agy"}
	sessDir := filepath.Join(w.stateDir, "discordagy000001")
	if err := os.MkdirAll(sessDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessDir, agyinput.DescriptorFile), []byte(`{"session_id":"discordagy000001"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	w.resolve = func(string, []string) (string, error) { return "/usr/local/bin/agy", nil }
	w.cliObserve = func(string) *persist.CLIIdentity { c := *oldID; return &c }
	w.cliProbe = func(string, string, []string, string) (*persist.CLIIdentity, error) { c := *newID; return &c, nil }

	for range 5 {
		w.tick()
	}
	if len(f.killed) != 0 || len(f.launched) != 0 {
		t.Fatalf("cli-refresh killed or relaunched agy session owning native-input broker: killed=%v launched=%d", f.killed, len(f.launched))
	}
}
