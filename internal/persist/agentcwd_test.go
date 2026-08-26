package persist

// FAILING-FIRST (TDD RED, GG-5) for the worktree half of the hands-off handoff sweep:
// session meta gains the working directory the AGENT actually ran in.
//
// FROZEN API:
//
//	type Meta struct {
//	    ...
//	    AgentCwd string `json:"agent_cwd"` // "" unless a pre-launch hook overrode the cwd
//	}
//
//	func (m Meta) ProviderCwd() string // AgentCwd, falling back to Cwd when it is empty
//
// WHY IT IS ADDITIVE AND Cwd DOES NOT MOVE. For a worktree-isolated session (Epic 12)
// the daemon's PreLaunch hook returns <repo>/.swarm/worktrees/<id> and the agent runs
// THERE, while Meta.Cwd keeps the launch cwd -- the repo root. Teardown depends on that
// exact meaning: internal/skeleton's preDeleteWorktree calls worktree.Remove(m.Cwd, m.ID),
// and a Remove anchored at the worktree instead of the repo tears down nothing. So the
// resolved directory arrives as a NEW field and Cwd keeps its meaning untouched.
//
// A provider files its own history under an encoding of the directory the agent ran in,
// so every provider-facing derivation must read ProviderCwd; that is why the fallback is
// written once, on the type, instead of at each call site.
//
// Not omitempty, matching spawned_from / spawn_intent / supervision and the Meta doc
// comment: the on-disk key set is the durable contract, so the key is always written and
// a meta.json from before the field existed loads with "" rather than an error.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// preAgentCwdSchemaVersion is the constant shipped by the last build that had no AgentCwd
// field. It is hardcoded because it is history, not configuration.
const preAgentCwdSchemaVersion = 1

// TestAgentCwdRoundTrips: the resolved directory survives Save/Load under its snake_case
// key, and the key is written even when the session never had an override.
func TestAgentCwdRoundTrips(t *testing.T) {
	for _, agentCwd := range []string{"/home/nathan/project/.swarm/worktrees/sess-abc123", ""} {
		t.Run("agent_cwd="+agentCwd, func(t *testing.T) {
			s, dir := newTestStore(t)
			m := fullMeta()
			m.AgentCwd = agentCwd
			if err := s.Save(m); err != nil {
				t.Fatalf("Save error: %v", err)
			}
			got, err := s.Load(m.ID)
			if err != nil {
				t.Fatalf("Load error: %v", err)
			}
			if got.AgentCwd != agentCwd {
				t.Errorf("agent_cwd = %q, want %q", got.AgentCwd, agentCwd)
			}
			if got.Cwd != m.Cwd {
				t.Errorf("cwd = %q, want %q -- the launch cwd must not move", got.Cwd, m.Cwd)
			}
			data, err := os.ReadFile(filepath.Join(dir, m.ID, metaFile))
			if err != nil {
				t.Fatalf("read meta.json: %v", err)
			}
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(data, &raw); err != nil {
				t.Fatalf("meta.json is not a JSON object: %v", err)
			}
			if _, ok := raw["agent_cwd"]; !ok {
				t.Errorf("meta.json missing key %q (the on-disk key set is the durable contract)", "agent_cwd")
			}
		})
	}
}

// TestProviderCwdPrefersTheResolvedAgentCwd pins the one rule every provider-facing path
// derivation depends on: the agent's own directory when there was an override, the launch
// directory otherwise.
func TestProviderCwdPrefersTheResolvedAgentCwd(t *testing.T) {
	const repo = "/home/nathan/project"
	const worktree = "/home/nathan/project/.swarm/worktrees/sess-abc123"

	if got := (Meta{Cwd: repo}).ProviderCwd(); got != repo {
		t.Errorf("ProviderCwd() with no override = %q, want the launch cwd %q", got, repo)
	}
	if got := (Meta{Cwd: repo, AgentCwd: worktree}).ProviderCwd(); got != worktree {
		t.Errorf("ProviderCwd() with an override = %q, want the agent cwd %q", got, worktree)
	}
}

// TestAgentCwdAbsentInOlderMetaLoadsEmpty is the schema-compat half: a meta.json written
// before the field existed must load, carry an empty AgentCwd, and fall back to its Cwd --
// which is exactly right, because a session launched by that build had no override.
func TestAgentCwdAbsentInOlderMetaLoadsEmpty(t *testing.T) {
	s, dir := newTestStore(t)
	const id = "older"
	olderDir := filepath.Join(dir, id)
	if err := os.MkdirAll(olderDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// A literal line as builds before the field wrote it, not something re-encoded by this
	// build: a fixture produced by the code under test proves nothing about the code before it.
	body := `{"schema_version":1,"id":"older","agent_type":"claude","cwd":"/home/nathan/project","launch_options":{"worktree":"true"},"spawned_from":"p1","spawn_intent":"handoff"}`
	if err := os.WriteFile(filepath.Join(olderDir, metaFile), []byte(body), 0o600); err != nil {
		t.Fatalf("write meta: %v", err)
	}
	got, err := s.Load(id)
	if err != nil {
		t.Fatalf("Load of a meta.json without the agent_cwd key must succeed: %v", err)
	}
	if got.AgentCwd != "" {
		t.Errorf("agent_cwd = %q, want empty for a meta written before the field existed", got.AgentCwd)
	}
	if got.ProviderCwd() != "/home/nathan/project" {
		t.Errorf("ProviderCwd() = %q, want the stored cwd %q", got.ProviderCwd(), "/home/nathan/project")
	}
	if got.SpawnIntent != "handoff" {
		t.Errorf("spawn_intent = %q, want %q (carried over untouched)", got.SpawnIntent, "handoff")
	}
}

// TestAgentCwdAdditionDidNotBumpTheSchema records the decision, the way
// internal/journal/agentfield_test.go records journal's. decodeMeta REJECTS a
// schema_version above the build's own, so a bump would make every pre-bump daemon
// refuse every meta.json this build writes -- the whole session, not just the new
// field -- to gain nothing: encoding/json already ignores an unknown key, and an
// absent one already decodes to "", which is exactly this field's meaning for "this
// session's cwd was never overridden". If a future change bumps SchemaVersion this
// test fails ON PURPOSE, so the bump is a deliberate decision rather than something
// that rides along with a field addition.
func TestAgentCwdAdditionDidNotBumpTheSchema(t *testing.T) {
	if SchemaVersion != preAgentCwdSchemaVersion {
		t.Fatalf("SchemaVersion is %d, was %d before the AgentCwd field. A bump makes every older "+
			"daemon reject every meta.json this build writes; this test is where that decision is recorded",
			SchemaVersion, preAgentCwdSchemaVersion)
	}
	// The downgrade direction, which no amount of running this build exercises: decode a meta
	// carrying the new key into the Meta shape the previous build had, and apply that build's
	// version gate.
	m := fullMeta()
	m.AgentCwd = "/home/nathan/project/.swarm/worktrees/sess-abc123"
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var old struct {
		SchemaVersion int    `json:"schema_version"`
		ID            string `json:"id"`
		Cwd           string `json:"cwd"`
	}
	if err := json.Unmarshal(data, &old); err != nil {
		t.Fatalf("a build without AgentCwd failed to decode a meta carrying one: %v", err)
	}
	if old.SchemaVersion > preAgentCwdSchemaVersion {
		t.Fatalf("schema_version %d exceeds the %d an older daemon accepts; it would reject this "+
			"session entirely, not just the agent cwd", old.SchemaVersion, preAgentCwdSchemaVersion)
	}
	if old.Cwd != m.Cwd {
		t.Fatalf("older-shape cwd = %q, want %q -- the field an older daemon relies on must be untouched", old.Cwd, m.Cwd)
	}
}
