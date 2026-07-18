package persist

// R3.1.1 (agents-tracker-tid, perf plan item 3.1): Save must marshal compactly
// (json.Marshal, not MarshalIndent) — the indentation cost is pure overhead on
// the hot per-status-change write path. A pre-existing pretty-printed meta.json
// (written by an older build) must still load unchanged: JSON decoding is
// whitespace-agnostic, so this is a compatibility pin, not a behavior change.

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestSaveWritesCompactJSON fails under json.MarshalIndent (which always emits
// newlines for a struct with this many fields) and passes once Save switches to
// json.Marshal.
func TestSaveWritesCompactJSON(t *testing.T) {
	s, dir := newTestStore(t)
	m := fullMeta()
	if err := s.Save(m); err != nil {
		t.Fatalf("Save error: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, m.ID, "meta.json"))
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}
	if bytes.Contains(raw, []byte("\n")) {
		t.Errorf("meta.json is not compact (contains a newline), want single-line json.Marshal output:\n%s", raw)
	}
}

// TestLoadOldPrettyPrintedMeta pins backward compatibility: a meta.json written
// by an older build with json.MarshalIndent must still load correctly once Save
// switches to compact json.Marshal.
func TestLoadOldPrettyPrintedMeta(t *testing.T) {
	s, dir := newTestStore(t)
	const id = "pretty-legacy"
	legacyDir := filepath.Join(dir, id)
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}
	pretty := `{
  "schema_version": 1,
  "id": "pretty-legacy",
  "agent_type": "claude",
  "cwd": "/home/nathan/project",
  "launch_options": {
    "worktree": "false"
  },
  "env": [
    "PATH=/usr/bin:/bin"
  ],
  "created_at": "2026-07-16T12:30:45.123456789Z",
  "status": {
    "process": "running",
    "turn": "idle",
    "interaction": "none"
  },
  "last_activity": "2026-07-16T13:00:00.987654321Z",
  "shim_pid": 4242,
  "shim_start_time": 1700000000,
  "conversation_id": "conv-xyz-789",
  "exit_code": null,
  "resumed_from": ""
}`
	if err := os.WriteFile(filepath.Join(legacyDir, "meta.json"), []byte(pretty), 0o600); err != nil {
		t.Fatalf("write pretty legacy meta: %v", err)
	}
	got, err := s.Load(id)
	if err != nil {
		t.Fatalf("Load of pretty-printed meta.json must succeed: %v", err)
	}
	if got.ID != id || got.AgentType != "claude" || got.Cwd != "/home/nathan/project" {
		t.Errorf("pretty-printed meta decoded wrong: %+v", got)
	}
	if len(got.Env) != 1 || got.Env[0] != "PATH=/usr/bin:/bin" {
		t.Errorf("pretty-printed meta env decoded wrong: %v", got.Env)
	}

	// Scan must also decode it.
	scanned, err := s.Scan()
	if err != nil {
		t.Fatalf("Scan error: %v", err)
	}
	if len(scanned) != 1 || scanned[0].ID != id {
		t.Errorf("Scan did not pick up the pretty-printed legacy session: %+v", scanned)
	}
}
