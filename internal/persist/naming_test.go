package persist

// v0.4 P2 — session naming (bd agents-tracker-4e2). The user-provided session label
// is persisted in meta.json as an additive field: it round-trips through Save/Load
// and carries a durable snake_case "name" key, and — crucially — a meta.json written
// by an older build (no "name" key) still loads, degrading to an empty Name so a
// version-skewed on-disk session is never unreadable.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Save then Load preserves Name, and the on-disk JSON carries the durable "name" key.
func TestMeta_NameRoundTrips(t *testing.T) {
	s, dir := newTestStore(t)
	m := fullMeta()
	m.Name = "backend-refactor"
	if err := s.Save(m); err != nil {
		t.Fatalf("Save: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, m.ID, "meta.json"))
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("meta.json is not a JSON object: %v", err)
	}
	if _, ok := obj["name"]; !ok {
		t.Errorf("meta.json missing durable snake_case key %q", "name")
	}

	got, err := s.Load(m.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Name != "backend-refactor" {
		t.Fatalf("Name did not round-trip: got %q", got.Name)
	}
}

// A meta.json written before the name field existed (no "name" key) must still load:
// the absent key degrades to an empty Name with no error, and every other field
// survives. Simulated by writing a committed meta and stripping the key on disk.
func TestMeta_LoadsLegacyMetaWithoutNameField(t *testing.T) {
	s, dir := newTestStore(t)
	m := fullMeta()
	m.Name = "should-be-dropped"
	if err := s.Save(m); err != nil {
		t.Fatalf("Save: %v", err)
	}
	path := filepath.Join(dir, m.ID, "meta.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("unmarshal meta.json: %v", err)
	}
	delete(obj, "name") // simulate a meta.json written before the field existed
	edited, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal edited meta.json: %v", err)
	}
	if err := os.WriteFile(path, edited, 0o600); err != nil {
		t.Fatalf("rewrite meta.json: %v", err)
	}

	got, err := s.Load(m.ID)
	if err != nil {
		t.Fatalf("a legacy meta.json without a name key must load: %v", err)
	}
	if got.Name != "" {
		t.Fatalf("absent name must decode as empty; got %q", got.Name)
	}
	if got.AgentType != m.AgentType {
		t.Fatalf("legacy fields must survive the load; agent_type = %q, want %q", got.AgentType, m.AgentType)
	}
}
