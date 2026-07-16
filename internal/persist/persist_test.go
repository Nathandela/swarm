package persist

// Failing-first tests for the persist package (Epic 1, invariant S8: atomic-durable state).
// These tests ARE the acceptance criteria E1.4, E1.4b, E1.5, E1.6, E1.7, E1.8 and the
// DefaultDir requirement of R-1. No implementation lives here; a separate agent makes them pass.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/status"
)

// newTestStore builds a Store rooted at a fresh, not-yet-existing "sessions" dir so that
// NewStore exercises its create-if-missing path (relied on by the permissions test).
func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "sessions")
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore(%q) error: %v", dir, err)
	}
	return s, dir
}

// fullMeta returns a Meta with every S-2 field set to a distinct non-zero value, including the
// ExitCode pointer and all three status dimensions. Times are constructed in UTC with
// nanosecond precision so they survive a JSON RFC3339Nano round-trip under reflect.DeepEqual.
func fullMeta() Meta {
	code := 137
	return Meta{
		SchemaVersion:  SchemaVersion,
		ID:             "sess-abc123",
		AgentType:      "claude",
		Cwd:            "/home/nathan/project",
		LaunchOptions:  map[string]string{"model": "opus", "worktree": "false"},
		Env:            []string{"PATH=/usr/bin:/bin", "ANTHROPIC_API_KEY=sk-ant-test"},
		CreatedAt:      time.Date(2026, 7, 16, 12, 30, 45, 123456789, time.UTC),
		Status:         status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionPermission},
		LastActivity:   time.Date(2026, 7, 16, 13, 0, 0, 987654321, time.UTC),
		ShimPID:        4242,
		ShimStartTime:  1_700_000_000,
		ConversationID: "conv-xyz-789",
		ExitCode:       &code,
		ResumedFrom:    "sess-parent-000",
	}
}

func checkPerm(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s: mode = %04o, want %04o", path, got, want)
	}
}

// E1.4b — Save then Load preserves EVERY S-2 field exactly, and the on-disk JSON carries every
// snake_case key (the JSON key set is the durable data contract).
func TestSaveLoadRoundTripAllFields(t *testing.T) {
	s, dir := newTestStore(t)
	want := fullMeta()
	if err := s.Save(want); err != nil {
		t.Fatalf("Save error: %v", err)
	}
	got, err := s.Load(want.ID)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip mismatch:\n got: %#v\nwant: %#v", got, want)
	}

	raw, err := os.ReadFile(filepath.Join(dir, want.ID, "meta.json"))
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("meta.json is not a JSON object: %v", err)
	}
	for _, key := range []string{
		"schema_version", "id", "agent_type", "cwd", "launch_options",
		"env", "created_at", "status", "last_activity", "shim_pid",
		"shim_start_time", "conversation_id", "exit_code", "resumed_from",
	} {
		if _, ok := m[key]; !ok {
			t.Errorf("meta.json missing snake_case key %q", key)
		}
	}
}

// E1.4 — atomic temp+rename leaves only the committed meta.json; no temp file remains observable
// after a successful Save.
func TestSaveLeavesNoTempFile(t *testing.T) {
	s, dir := newTestStore(t)
	m := fullMeta()
	if err := s.Save(m); err != nil {
		t.Fatalf("Save error: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, m.ID))
	if err != nil {
		t.Fatalf("read session dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 1 || names[0] != "meta.json" {
		t.Errorf("session dir should contain only meta.json after Save, got %v", names)
	}
}

// E1.4 / S8 (atomicity half) — a crash after temp-create but before rename leaves the previous
// meta.json intact: Load and Scan observe old-or-new, never a torn file, and a stray temp file
// never masks the committed session.
func TestCrashDuringWriteLeavesOldIntact(t *testing.T) {
	s, dir := newTestStore(t)
	old := fullMeta()
	if err := s.Save(old); err != nil {
		t.Fatalf("Save error: %v", err)
	}
	stray := filepath.Join(dir, old.ID, "meta.json.tmp1234")
	if err := os.WriteFile(stray, []byte(`{"schema_version":1,"id":"sess-ab`), 0o600); err != nil {
		t.Fatalf("write stray temp: %v", err)
	}
	got, err := s.Load(old.ID)
	if err != nil {
		t.Fatalf("Load after simulated crash error: %v", err)
	}
	if !reflect.DeepEqual(got, old) {
		t.Errorf("Load returned torn/changed data after simulated crash:\n got: %#v\nwant: %#v", got, old)
	}
	sessions, err := s.Scan()
	if err != nil {
		t.Fatalf("Scan error: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != old.ID {
		t.Errorf("Scan should return exactly the intact session, got %+v", sessions)
	}
}

// E1.4 / S8 (isolation half) — one corrupt session dir never prevents scanning the rest.
func TestScanIsolatesCorruptSession(t *testing.T) {
	s, dir := newTestStore(t)
	good := fullMeta()
	good.ID = "good"
	if err := s.Save(good); err != nil {
		t.Fatalf("Save error: %v", err)
	}
	badDir := filepath.Join(dir, "bad")
	if err := os.MkdirAll(badDir, 0o700); err != nil {
		t.Fatalf("mkdir bad: %v", err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "meta.json"), []byte(`{ this is not valid json`), 0o600); err != nil {
		t.Fatalf("write corrupt meta: %v", err)
	}
	sessions, err := s.Scan()
	if err != nil {
		t.Fatalf("Scan must not error on a corrupt session: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != "good" {
		t.Errorf("Scan should return only the intact session, got %+v", sessions)
	}
}

// E1.4 — Load of a corrupt session returns an error, never a zero-value Meta with nil error.
func TestLoadCorruptReturnsError(t *testing.T) {
	s, dir := newTestStore(t)
	badDir := filepath.Join(dir, "bad")
	if err := os.MkdirAll(badDir, 0o700); err != nil {
		t.Fatalf("mkdir bad: %v", err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "meta.json"), []byte(`{ truncated`), 0o600); err != nil {
		t.Fatalf("write corrupt meta: %v", err)
	}
	if _, err := s.Load("bad"); err == nil {
		t.Fatal("Load of corrupt meta.json returned nil error; must report corruption")
	}
}

// E1.4 (perms) — sessions root 0700, session subdir 0700, meta.json 0600, verified under a
// permissive umask so the modes come from the code, not an inherited umask.
func TestPermissionsUnderPermissiveUmask(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)

	dir := filepath.Join(t.TempDir(), "sessions")
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore error: %v", err)
	}
	m := fullMeta()
	if err := s.Save(m); err != nil {
		t.Fatalf("Save error: %v", err)
	}
	checkPerm(t, dir, 0o700)
	checkPerm(t, filepath.Join(dir, m.ID), 0o700)
	checkPerm(t, filepath.Join(dir, m.ID, "meta.json"), 0o600)
}

// E1.5 — an older-schema meta.json (schema_version 0, only a subset of fields) is migrated to the
// current schema on Load rather than erroring; carried-over fields survive.
func TestMigrationFromOlderSchema(t *testing.T) {
	s, dir := newTestStore(t)
	const id = "legacy"
	legacyDir := filepath.Join(dir, id)
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}
	legacy := `{"schema_version":0,"id":"legacy","agent_type":"codex","cwd":"/tmp/work"}`
	if err := os.WriteFile(filepath.Join(legacyDir, "meta.json"), []byte(legacy), 0o600); err != nil {
		t.Fatalf("write legacy meta: %v", err)
	}
	got, err := s.Load(id)
	if err != nil {
		t.Fatalf("Load of older-schema meta must migrate, not error: %v", err)
	}
	if got.SchemaVersion != SchemaVersion {
		t.Errorf("migrated schema_version = %d, want %d", got.SchemaVersion, SchemaVersion)
	}
	if got.ID != "legacy" || got.AgentType != "codex" || got.Cwd != "/tmp/work" {
		t.Errorf("migration dropped carried-over fields: %+v", got)
	}
}

// E1.5 — a meta.json whose schema_version is newer than this build's must fail loudly with an
// explicit error mentioning the version, never silently misparse.
func TestLoadRejectsFutureSchemaVersion(t *testing.T) {
	s, dir := newTestStore(t)
	const id = "future"
	futureDir := filepath.Join(dir, id)
	if err := os.MkdirAll(futureDir, 0o700); err != nil {
		t.Fatalf("mkdir future: %v", err)
	}
	body := `{"schema_version":999,"id":"future","agent_type":"claude"}`
	if err := os.WriteFile(filepath.Join(futureDir, "meta.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write future meta: %v", err)
	}
	_, err := s.Load(id)
	if err == nil {
		t.Fatal("Load of a future schema_version returned nil error; must reject")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "version") {
		t.Errorf("error should mention the schema version, got: %v", err)
	}
}

// E1.6 — the roster is rebuildable purely by directory scan; removing any roster.json index must
// not change Scan results (test passes whether or not the implementation keeps an index file).
func TestRosterRebuildableByScan(t *testing.T) {
	s, dir := newTestStore(t)
	const n = 5
	ids := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		m := fullMeta()
		m.ID = "sess-" + strconv.Itoa(i)
		ids[m.ID] = true
		if err := s.Save(m); err != nil {
			t.Fatalf("Save %s: %v", m.ID, err)
		}
	}
	_ = os.Remove(filepath.Join(dir, "roster.json"))

	sessions, err := s.Scan()
	if err != nil {
		t.Fatalf("Scan error: %v", err)
	}
	if len(sessions) != n {
		t.Fatalf("Scan returned %d sessions, want %d", len(sessions), n)
	}
	for _, m := range sessions {
		if !ids[m.ID] {
			t.Errorf("Scan returned unexpected session id %q", m.ID)
		}
		delete(ids, m.ID)
	}
	if len(ids) != 0 {
		t.Errorf("Scan missing sessions: %v", ids)
	}
}

// E1.7 — resumed_from round-trips through Save/Load.
func TestResumedFromRoundTrips(t *testing.T) {
	s, _ := newTestStore(t)
	m := fullMeta()
	m.ResumedFrom = "sess-origin-42"
	if err := s.Save(m); err != nil {
		t.Fatalf("Save error: %v", err)
	}
	got, err := s.Load(m.ID)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if got.ResumedFrom != "sess-origin-42" {
		t.Errorf("resumed_from = %q, want %q", got.ResumedFrom, "sess-origin-42")
	}
}

// E1.7 — Delete removes the whole session dir; Load afterwards errors and the dir is gone.
func TestDeleteRemovesSessionDir(t *testing.T) {
	s, dir := newTestStore(t)
	m := fullMeta()
	if err := s.Save(m); err != nil {
		t.Fatalf("Save error: %v", err)
	}
	sessionDir := filepath.Join(dir, m.ID)
	if _, err := os.Stat(sessionDir); err != nil {
		t.Fatalf("session dir should exist before Delete: %v", err)
	}
	if err := s.Delete(m.ID); err != nil {
		t.Fatalf("Delete error: %v", err)
	}
	if _, err := s.Load(m.ID); err == nil {
		t.Error("Load after Delete returned nil error; session should be gone")
	}
	if _, err := os.Stat(sessionDir); !os.IsNotExist(err) {
		t.Errorf("session dir should be removed after Delete, stat err = %v", err)
	}
}

// E1.8 — FilterEnv drops injection vectors (LD_PRELOAD, DYLD_INSERT_LIBRARIES) and unrelated
// secrets (AWS_SECRET_ACCESS_KEY, arbitrary FOO), while passing through the normative allowlist
// exactly: order-preserving, KEY=VALUE intact including values that themselves contain '='.
// Provider credentials (ANTHROPIC_API_KEY, OPENAI_API_KEY) are allowed — agent CLIs need them.
func TestFilterEnvAllowlist(t *testing.T) {
	in := []string{
		"PATH=/usr/bin:/bin",
		"AWS_SECRET_ACCESS_KEY=topsecret",
		"HOME=/home/nathan",
		"LD_PRELOAD=/evil.so",
		"SHELL=/bin/bash",
		"DYLD_INSERT_LIBRARIES=/evil.dylib",
		"TERM=xterm-256color",
		"FOO=bar",
		"LANG=en_US.UTF-8",
		"PATHOLOGICAL=not-path", // prefix of PATH must not be mistaken for it
		"LC_ALL=en_US.UTF-8",
		"VIRTUAL_ENV=/home/nathan/.venv",
		"CONDA_PREFIX=/opt/conda",
		"ANTHROPIC_API_KEY=sk-ant-123",
		"OPENAI_API_KEY=key=with=equals", // value contains '=' — must pass through verbatim
	}
	want := []string{
		"PATH=/usr/bin:/bin",
		"HOME=/home/nathan",
		"SHELL=/bin/bash",
		"TERM=xterm-256color",
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
		"VIRTUAL_ENV=/home/nathan/.venv",
		"CONDA_PREFIX=/opt/conda",
		"ANTHROPIC_API_KEY=sk-ant-123",
		"OPENAI_API_KEY=key=with=equals",
	}
	got := FilterEnv(in)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FilterEnv mismatch:\n got: %v\nwant: %v", got, want)
	}
}

// R-1 (DefaultDir) — with XDG_STATE_HOME set, state lives under $XDG_STATE_HOME/swarm/sessions.
func TestDefaultDirUsesXDGStateHome(t *testing.T) {
	xdg := filepath.Join(t.TempDir(), "xdgstate")
	t.Setenv("XDG_STATE_HOME", xdg)
	got, err := DefaultDir()
	if err != nil {
		t.Fatalf("DefaultDir error: %v", err)
	}
	want := filepath.Join(xdg, "swarm", "sessions")
	if got != want {
		t.Errorf("DefaultDir = %q, want %q", got, want)
	}
}

// R-1 (DefaultDir) — without XDG_STATE_HOME, falls back to $HOME/.local/state/swarm/sessions.
func TestDefaultDirFallsBackWithoutXDG(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "") // registers restore of the original value
	os.Unsetenv("XDG_STATE_HOME")  // genuinely unset for this test; cleanup still restores
	got, err := DefaultDir()
	if err != nil {
		t.Fatalf("DefaultDir error: %v", err)
	}
	want := filepath.Join(home, ".local", "state", "swarm", "sessions")
	if got != want {
		t.Errorf("DefaultDir = %q, want %q", got, want)
	}
}
