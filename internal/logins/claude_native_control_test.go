package logins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestClaudeNativeControlCustodyKeepsExactPair(t *testing.T) {
	s, _, meta, native := claudeCustodyFixture(t)
	meta["required_runtime"] = "claude-native-control"
	raw, _ := json.Marshal(meta)
	parsed, err := parseClaudeSessionMeta(raw, s.StateRoot(), "source")
	if err != nil || !claudeNativeMatchesMeta(native, parsed) {
		t.Fatalf("native control changed existing custody binding: %v", err)
	}
	writeClaudeCustodyJSON(t, s.StateRoot(), "source", "meta.json", meta)
	meta["name"] = "renamed"
	raw, _ = json.Marshal(meta)
	called := false
	if err := WithClaudeMetadataSave(s.StateRoot(), "source", raw, func() error {
		called = true
		return os.WriteFile(filepath.Join(s.StateRoot(), "source", "meta.json"), raw, 0600)
	}); err != nil || !called {
		t.Fatalf("exact owned update rejected: %v", err)
	}
	delete(meta, "required_runtime")
	raw, _ = json.Marshal(meta)
	called = false
	if err := WithClaudeMetadataSave(s.StateRoot(), "source", raw, func() error { called = true; return nil }); err == nil || called {
		t.Fatal("runtime stripped through custody writer")
	}
}

func TestClaudeNativeControlTagCannotEscapeOwnedRouting(t *testing.T) {
	for _, schema := range []int{1, 4} {
		s, _, _, _ := claudeCustodyFixture(t)
		raw, _ := json.Marshal(map[string]any{"id": "foreign", "schema_version": schema, "agent_type": "claude", "cwd": "/project", "required_runtime": "claude-native-control"})
		if _, err := parseClaudeSessionMeta(raw, s.StateRoot(), "foreign"); err == nil {
			t.Errorf("native tag escaped owned parser through schema%d/no owner", schema)
		}
		if err := os.Mkdir(filepath.Join(s.StateRoot(), "foreign"), 0700); err != nil {
			t.Fatal(err)
		}
		called := false
		err := WithClaudeMetadataSave(s.StateRoot(), "foreign", raw, func() error { called = true; return nil })
		if err == nil || called {
			t.Errorf("native tag escaped direct writer through schema%d/no owner: called=%t err=%v", schema, called, err)
		}
	}
}

func TestClaudeNativeControlUninitializedRootTagCannotPersist(t *testing.T) {
	for _, schema := range []int{1, 4} {
		root := t.TempDir()
		raw, _ := json.Marshal(map[string]any{"id": "foreign", "schema_version": schema, "agent_type": "claude", "required_runtime": "claude-native-control"})
		called := false
		err := WithClaudeMetadataSave(root, "foreign", raw, func() error { called = true; return nil })
		if err == nil || called {
			t.Errorf("tagged schema%d escaped uninitialized-root guard: called=%t err=%v", schema, called, err)
		}
	}
}
