package persist

// ADR-024: session meta gains the provider-account identity the agent was
// launched under (the auth watcher's comparison stamp). Additive without a
// schema-version bump for the AgentCwd reason -- a rolled-back daemon merely
// drops the stamp, which degrades to "unknown: never auto-recycled" -- and
// omitempty like ShimWireVersion/BackendPlanError: absence IS the pre-feature
// record.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthIdentityRoundTrips(t *testing.T) {
	for _, identity := range []string{"a3f2b4c5d6e7f8091a2b3c4d5e6f70812345678900aabbccddeeff0011223344", ""} {
		t.Run("auth_identity="+identity, func(t *testing.T) {
			s, _ := newTestStore(t)
			m := fullMeta()
			m.AuthIdentity = identity
			if err := s.Save(m); err != nil {
				t.Fatalf("Save error: %v", err)
			}
			got, err := s.Load(m.ID)
			if err != nil {
				t.Fatalf("Load error: %v", err)
			}
			if got.AuthIdentity != identity {
				t.Errorf("auth_identity = %q, want %q", got.AuthIdentity, identity)
			}
		})
	}
}

// TestAuthIdentityIsOmittedWhenEmpty: the key stays off pre-feature-shaped
// records (omitempty), so an older daemon reading the file sees exactly the key
// set it wrote.
func TestAuthIdentityIsOmittedWhenEmpty(t *testing.T) {
	s, dir := newTestStore(t)
	m := fullMeta()
	m.AuthIdentity = ""
	if err := s.Save(m); err != nil {
		t.Fatalf("Save error: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, m.ID, "meta.json"))
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}
	if strings.Contains(string(raw), "auth_identity") {
		t.Errorf("empty auth_identity was written: %s", raw)
	}
}

// TestAuthIdentityDidNotBumpSchema: a meta written WITHOUT the field (a
// pre-ADR-024 record) still loads, with the zero value meaning "unknown".
func TestAuthIdentityDidNotBumpSchema(t *testing.T) {
	s, dir := newTestStore(t)
	m := fullMeta()
	if err := s.Save(m); err != nil {
		t.Fatalf("Save error: %v", err)
	}
	path := filepath.Join(dir, m.ID, "meta.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}
	var onDisk map[string]any
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("unmarshal meta.json: %v", err)
	}
	delete(onDisk, "auth_identity")
	pruned, err := json.Marshal(onDisk)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	if err := os.WriteFile(path, pruned, 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	got, err := s.Load(m.ID)
	if err != nil {
		t.Fatalf("a pre-ADR-024 record failed to load: %v", err)
	}
	if got.AuthIdentity != "" {
		t.Errorf("auth_identity = %q on a record that never had one; want \"\"", got.AuthIdentity)
	}
}
