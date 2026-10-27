package persist

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestClaudeChatFixtureSixHasNoManagedCustody(t *testing.T) {
	s, root := newTestStore(t)
	ordinary := Meta{SchemaVersion: 1, ID: "ordinary-fake", AgentType: "fake", Cwd: root}
	if err := s.Save(ordinary); err != nil {
		t.Fatal("ordinary fake1 Save changed", err)
	}
	if got, err := s.Load(ordinary.ID); err != nil || got.SchemaVersion != 1 || got.RequiredRuntime != "" {
		t.Fatal("ordinary fake1 shape changed", err)
	}
	if err := s.Delete(ordinary.ID); err != nil {
		t.Fatal("ordinary fake1 Delete changed", err)
	}
	m := Meta{SchemaVersion: 6, ID: "fake-chat", AgentType: "fake", Cwd: root, RequiredRuntime: "claude-chat"}
	if err := s.Save(m); err != nil {
		t.Fatalf("explicit unmanaged fake6 refused: %v", err)
	}
	got, err := s.Load(m.ID)
	if err != nil || got.SchemaVersion != 6 || got.RequiredRuntime != "claude-chat" || got.LoginRuntime != nil || got.CLIIdentity != nil || got.ClaudeRetirement != nil {
		t.Fatalf("fixture became managed or lost mode: %+v %v", got, err)
	}
	original := syncMetadataDirectory
	syncs := 0
	syncMetadataDirectory = func(dir string) error { syncs++; return original(dir) }
	t.Cleanup(func() { syncMetadataDirectory = original })
	m.SchemaVersion, m.Name = 1, "updated fake chat"
	if err := s.Save(m); err != nil {
		t.Fatal(err)
	}
	got, err = s.Load(m.ID)
	if err != nil || got.SchemaVersion != 6 || got.Name != m.Name || syncs == 0 {
		t.Fatalf("fixture writer lost mode/durability: %+v syncs=%d err=%v", got, syncs, err)
	}
}

func TestClaudeChatMalformedFixtureCannotBecomeDisposable(t *testing.T) {
	for _, kind := range []string{"wrong-provider", "missing-mode", "other-mode", "mode-alias", "embargo", "forged-login", "cli", "retirement", "opencode", "native-claim", "duplicate-mode", "wrong-id"} {
		t.Run(kind, func(t *testing.T) {
			s, root := newTestStore(t)
			fields := map[string]any{"schema_version": 6, "id": "fake-chat", "agent_type": "fake", "cwd": root, "required_runtime": "claude-chat"}
			switch kind {
			case "wrong-provider":
				fields["agent_type"] = "claude"
			case "missing-mode":
				delete(fields, "required_runtime")
			case "other-mode":
				fields["required_runtime"] = "other"
			case "cli":
				fields["cli_identity"] = map[string]any{"path": "/synthetic/claude"}
			case "retirement":
				fields["claude_retirement"] = map[string]any{"version": 1}
			case "opencode":
				fields["opencode_binding"] = map[string]any{"revision": 1}
			case "native-claim":
				fields["native_provider"] = "claude"
			case "wrong-id":
				fields["id"] = "other-fake"
			case "mode-alias":
				fields["Required_Runtime"] = fields["required_runtime"]
				delete(fields, "required_runtime")
			case "embargo":
				fields["input_embargo"] = "pending"
			case "forged-login":
				fields["login_runtime"] = map[string]any{"provider": "claude", "epoch": 1, "identity": "synthetic", "home": "/synthetic"}
			}
			raw, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "duplicate-mode" {
				raw = append(bytes.TrimSuffix(raw, []byte("}")), []byte(`,"required_runtime":"claude-chat"}`)...)
			}
			dir := filepath.Join(root, "fake-chat")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "meta.json")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			payload := filepath.Join(dir, "hooks.spool")
			if err := os.WriteFile(payload, []byte("retained synthetic evidence"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Load("fake-chat"); err == nil {
				t.Error("malformed fake6 loaded")
			}
			rows, err := s.Scan()
			if err != nil || len(rows) != 0 {
				t.Fatalf("malformed fixture scan: %v %v", rows, err)
			}
			if err := s.Delete("fake-chat"); err == nil {
				t.Error("omitted malformed fake6 became deletable")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(raw, after) {
				t.Fatalf("malformed fixture lost metadata: %v", err)
			}
			after, err = os.ReadFile(payload)
			if err != nil || string(after) != "retained synthetic evidence" {
				t.Fatalf("malformed fixture lost capture: %v", err)
			}
		})
	}
}
