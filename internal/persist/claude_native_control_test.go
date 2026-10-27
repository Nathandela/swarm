package persist

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func nativeControlMetaFixture(t *testing.T, retired bool) (*Store, Meta, []byte) {
	t.Helper()
	s, m, raw := claudeRetainedMetaFixture(t, retired)
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m.RequiredRuntime = "claude-native-control"
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.root, m.ID, metaFile), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return s, m, raw
}

func TestClaudeNativeControlMetadataRoundTrip(t *testing.T) {
	// The same sanitized row is exercised against the retained old reader sources.
	compatibility, err := os.ReadFile("testdata/claude-native-control-meta.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeMeta(compatibility, "retained"); err != nil {
		t.Fatalf("compatibility fixture is not valid current metadata: %v", err)
	}
	for _, retired := range []bool{false, true} {
		s, m, _ := nativeControlMetaFixture(t, retired)
		got, err := s.Load(m.ID)
		if err != nil {
			t.Fatalf("exact native control schema5 refused: %v", err)
		}
		if got.SchemaVersion != 5 || got.RequiredRuntime != m.RequiredRuntime || IsClaudeChatRuntime(got) {
			t.Fatal("native control confused with chat/Hermes")
		}
		m.SchemaVersion, m.Name = 1, "ordinary label update"
		if err := s.Save(m); err != nil {
			t.Fatal(err)
		}
		got, err = s.Load(m.ID)
		if err != nil || got.SchemaVersion != 5 || got.RequiredRuntime != m.RequiredRuntime || got.Name != m.Name {
			t.Fatalf("native runtime carriage lost: %+v %v", got, err)
		}
	}
}

func TestClaudeNativeControlMetadataCannotConvertOrStrip(t *testing.T) {
	for _, kind := range []string{"strip", "chat", "future", "drop-owner", "convert-existing"} {
		t.Run(kind, func(t *testing.T) {
			s, m, before := nativeControlMetaFixture(t, false)
			if kind == "convert-existing" {
				m.RequiredRuntime = ""
				before, _ = json.Marshal(m)
				if err := os.WriteFile(filepath.Join(s.root, m.ID, metaFile), before, 0600); err != nil {
					t.Fatal(err)
				}
				m.RequiredRuntime = "claude-native-control"
			}
			if kind == "strip" {
				m.RequiredRuntime = ""
			}
			if kind == "chat" {
				m.SchemaVersion, m.RequiredRuntime = 6, "claude-chat"
			}
			if kind == "future" {
				m.RequiredRuntime = "claude-native-control-future"
			}
			if kind == "drop-owner" {
				m.LoginRuntime, m.CLIIdentity = nil, nil
			}
			if err := s.Save(m); err == nil {
				t.Fatal("existing metadata converted or stripped")
			}
			after, err := os.ReadFile(filepath.Join(s.root, m.ID, metaFile))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("rejected save changed retained row", err)
			}
		})
	}
}

func TestClaudeNativeControlMetadataStrictPair(t *testing.T) {
	_, m, raw := nativeControlMetaFixture(t, false)
	for _, kind := range []string{"six", "unmanaged", "wrong-agent", "hermes", "null", "alias", "duplicate", "cli-version", "cli-fingerprint"} {
		t.Run(kind, func(t *testing.T) {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "six":
				fields["schema_version"] = json.RawMessage(`6`)
			case "unmanaged":
				delete(fields, "login_runtime")
			case "wrong-agent":
				fields["agent_type"] = json.RawMessage(`"fake"`)
			case "hermes":
				fields["hermes_native"] = json.RawMessage(`true`)
			case "null":
				fields["required_runtime"] = json.RawMessage(`null`)
			case "alias":
				fields["Required_Runtime"] = fields["required_runtime"]
				delete(fields, "required_runtime")
			case "cli-version", "cli-fingerprint":
				cli := *m.CLIIdentity
				if kind == "cli-version" {
					cli.Version = "2.1.311"
				} else {
					cli.Fingerprint = "invalid"
				}
				fields["cli_identity"], _ = json.Marshal(cli)
			}
			bad, _ := json.Marshal(fields)
			if kind == "duplicate" {
				bad = append([]byte(`{"required_runtime":"claude-native-control",`), bad[1:]...)
			}
			if _, err := decodeMeta(bad, m.ID); err == nil {
				t.Fatal("invalid native control pair accepted", kind)
			}
		})
	}
}
