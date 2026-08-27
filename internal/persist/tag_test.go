package persist

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestTagPersistsAndLegacyMetadataRemainsCompatible(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(root)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	want := Meta{ID: "tagged", AgentType: "codex", Tag: "frontend"}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Load(want.ID)
	if err != nil || got.Tag != want.Tag {
		t.Fatalf("Load tag = %q, err=%v", got.Tag, err)
	}

	raw, err := os.ReadFile(filepath.Join(root, want.ID, metaFile))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	delete(obj, "tag")
	legacy, _ := json.Marshal(obj)
	if err := os.WriteFile(filepath.Join(root, want.ID, metaFile), legacy, 0o600); err != nil {
		t.Fatalf("write legacy meta: %v", err)
	}
	got, err = store.Load(want.ID)
	if err != nil || got.Tag != "" {
		t.Fatalf("legacy Load tag = %q, err=%v; want empty", got.Tag, err)
	}
}

// The on-disk key set is the durable contract (Meta doc comment): an untagged
// session still writes the "tag" key, like agent_cwd / spawned_from / supervision.
func TestTagKeyIsAlwaysWritten(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(root)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Save(Meta{ID: "untagged", AgentType: "codex"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "untagged", metaFile))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := obj["tag"]; !ok {
		t.Fatalf("meta.json lacks the tag key when untagged: %s", raw)
	}
}
