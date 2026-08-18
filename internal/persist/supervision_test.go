package persist

// FAILING-FIRST test for ADR-010 Amendment 3, persistence hop: session meta gains the
// supervision mode of a handoff child, the shape spawn_intent already follows.
//
// FROZEN API:
//
//	type Meta struct {
//	    ...
//	    Supervision string `json:"supervision"` // "", passive, manual, none
//	}
//
// Not omitempty: the on-disk key set is the durable contract, so the key is always
// written. A meta.json written before the field existed carries no key and loads
// with "" (an unsupervised session), never an error.
//
// RED today: Meta has no Supervision field, so this file fails to compile on it.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestSupervisionRoundTrips: the mode survives Save/Load and is written under its
// snake_case key even when empty.
func TestSupervisionRoundTrips(t *testing.T) {
	for _, mode := range []string{"passive", ""} {
		t.Run("mode="+mode, func(t *testing.T) {
			s, dir := newTestStore(t)
			m := fullMeta()
			m.Supervision = mode
			if err := s.Save(m); err != nil {
				t.Fatalf("Save error: %v", err)
			}
			got, err := s.Load(m.ID)
			if err != nil {
				t.Fatalf("Load error: %v", err)
			}
			if got.Supervision != mode {
				t.Errorf("supervision = %q, want %q", got.Supervision, mode)
			}
			data, err := os.ReadFile(filepath.Join(dir, m.ID, metaFile))
			if err != nil {
				t.Fatalf("read meta.json: %v", err)
			}
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(data, &raw); err != nil {
				t.Fatalf("meta.json is not a JSON object: %v", err)
			}
			if _, ok := raw["supervision"]; !ok {
				t.Errorf("meta.json missing key %q (the on-disk key set is the durable contract)", "supervision")
			}
		})
	}
}

// TestSupervisionAbsentInOlderMetaLoadsEmpty: a meta.json from before the field
// existed loads with an empty mode -- the sessions of a pre-upgrade daemon are simply
// unsupervised, not unreadable.
func TestSupervisionAbsentInOlderMetaLoadsEmpty(t *testing.T) {
	s, dir := newTestStore(t)
	const id = "older"
	olderDir := filepath.Join(dir, id)
	if err := os.MkdirAll(olderDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `{"schema_version":1,"id":"older","agent_type":"claude","cwd":"/tmp","spawned_from":"p1","spawn_intent":"handoff"}`
	if err := os.WriteFile(filepath.Join(olderDir, metaFile), []byte(body), 0o600); err != nil {
		t.Fatalf("write meta: %v", err)
	}
	got, err := s.Load(id)
	if err != nil {
		t.Fatalf("Load of a meta.json without the supervision key must succeed: %v", err)
	}
	if got.Supervision != "" {
		t.Errorf("supervision = %q, want empty for a meta written before the field existed", got.Supervision)
	}
	if got.SpawnIntent != "handoff" {
		t.Errorf("spawn_intent = %q, want %q (carried over untouched)", got.SpawnIntent, "handoff")
	}
}
