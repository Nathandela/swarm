package persist

// FAILING-FIRST test for ADR-010 Phase 2 PIECE 1, persistence hop: session meta
// gains the spawn lineage (D4), mirroring the ResumedFrom precedent exactly
// (TestResumedFromRoundTrips above is the shape this follows).
//
// FROZEN API:
//
//	type Meta struct {
//	    ...
//	    SpawnedFrom string `json:"spawned_from"`
//	    SpawnIntent string `json:"spawn_intent"`
//	}
//
// Both tags are deliberately NOT omitempty: the on-disk key set is the durable data
// contract (see the Meta doc comment), so the keys are always present, exactly as
// `resumed_from` is. The lineage must survive Save/Load so it is crash-safe — the
// roster rebuilds every SessionView from these bytes after a daemon restart.
//
// RED today: Meta has neither field, so this file fails to compile on them.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestSpawnLineageRoundTrips: spawned_from + spawn_intent survive Save/Load and are
// written under their snake_case keys (the durable contract).
func TestSpawnLineageRoundTrips(t *testing.T) {
	s, _ := newTestStore(t)
	m := fullMeta()
	m.SpawnedFrom = "sess-parent-000"
	m.SpawnIntent = "handoff"
	if err := s.Save(m); err != nil {
		t.Fatalf("Save error: %v", err)
	}
	got, err := s.Load(m.ID)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if got.SpawnedFrom != "sess-parent-000" {
		t.Errorf("spawned_from = %q, want %q", got.SpawnedFrom, "sess-parent-000")
	}
	if got.SpawnIntent != "handoff" {
		t.Errorf("spawn_intent = %q, want %q", got.SpawnIntent, "handoff")
	}
}

// TestSpawnLineageKeysAlwaysPresent: an UNLINEAGED session still writes both keys —
// the durable key set does not vary with the value (the same rule resumed_from
// follows), so a reader never has to distinguish "absent" from "empty".
func TestSpawnLineageKeysAlwaysPresent(t *testing.T) {
	s, dir := newTestStore(t)
	m := fullMeta()
	m.SpawnedFrom, m.SpawnIntent = "", ""
	if err := s.Save(m); err != nil {
		t.Fatalf("Save error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, m.ID, metaFile))
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("meta.json is not a JSON object: %v", err)
	}
	for _, key := range []string{"spawned_from", "spawn_intent"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("meta.json missing snake_case key %q (the on-disk key set is the durable contract)", key)
		}
	}
}
