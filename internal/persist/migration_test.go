package persist

// FIX 5 — migration registry primitive. These exercise the unexported chain
// directly (same-package test); nothing new is exported.

import (
	"reflect"
	"strings"
	"testing"
)

// The real registry must carry a named v0->v1 entry so the chain is a genuine
// primitive, not a bare version stamp.
func TestMigrationsHasV0Entry(t *testing.T) {
	if _, ok := migrations[0]; !ok {
		t.Error("migrations registry missing the v0->v1 entry")
	}
}

// applyMigrations must apply steps in ascending version order and stamp the
// final version. A synthetic v0->v1->v2 chain that each append to a field proves
// ordering: sequential application yields "base-v1v2", reverse would yield
// "base-v2v1".
func TestApplyMigrationsRunsInAscendingVersionOrder(t *testing.T) {
	var order []int
	chain := map[int]func(*Meta){
		0: func(m *Meta) { order = append(order, 0); m.AgentType += "v1" },
		1: func(m *Meta) { order = append(order, 1); m.AgentType += "v2" },
	}

	m := Meta{SchemaVersion: 0, AgentType: "base-"}
	if err := applyMigrations(&m, 2, chain); err != nil {
		t.Fatalf("applyMigrations with a complete chain errored: %v", err)
	}

	if m.SchemaVersion != 2 {
		t.Errorf("SchemaVersion after migration = %d, want 2", m.SchemaVersion)
	}
	if !reflect.DeepEqual(order, []int{0, 1}) {
		t.Errorf("migration order = %v, want [0 1]", order)
	}
	if m.AgentType != "base-v1v2" {
		t.Errorf("AgentType = %q, want %q (proves v0->v1->v2 sequential order)", m.AgentType, "base-v1v2")
	}
}

// A gap in the chain between the file's version and the target must ERROR, never
// silently advance the stamp: an unmigratable file is a loud failure, not a
// half-upgraded meta.
func TestApplyMigrationsMissingStepErrors(t *testing.T) {
	m := Meta{SchemaVersion: 0, AgentType: "unchanged"}
	err := applyMigrations(&m, 3, map[int]func(*Meta){}) // empty chain: gap at v0
	if err == nil {
		t.Fatal("applyMigrations with a missing step returned nil error; must reject")
	}
	if !strings.Contains(err.Error(), "no migration registered from v0") {
		t.Errorf("error = %v, want it to mention %q", err, "no migration registered from v0")
	}
}
