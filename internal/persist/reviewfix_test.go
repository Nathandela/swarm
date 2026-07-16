package persist

// Review-fix round tests (Epic 1). NEW tests only; the frozen designer contract
// in persist_test.go is not touched. These cover: decode-time id validation
// (FIX 1), Save-side env enforcement (FIX 2), hardening pre-existing dir modes
// (FIX 4), and symlink-escape rejection (FIX 6). Helpers newTestStore, fullMeta
// and checkPerm are shared package-level helpers from persist_test.go.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

// FIX 1(a) — Load of a syntactically valid but empty object must error: an empty
// id is garbage that must never round-trip as a real session.
func TestLoadEmptyObjectErrors(t *testing.T) {
	s, dir := newTestStore(t)
	const id = "emptymeta"
	d := filepath.Join(dir, id)
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(d, "meta.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write meta: %v", err)
	}
	if _, err := s.Load(id); err == nil {
		t.Error("Load of {} returned nil error; must reject an empty id")
	}
}

// FIX 1(b) — Load of a meta whose id does not match its session directory name
// must error, never silently return a mislabeled session.
func TestLoadRejectsIDDirMismatch(t *testing.T) {
	s, dir := newTestStore(t)
	const id = "realdir"
	d := filepath.Join(dir, id)
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `{"schema_version":1,"id":"otherid","agent_type":"claude"}`
	if err := os.WriteFile(filepath.Join(d, "meta.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write meta: %v", err)
	}
	if _, err := s.Load(id); err == nil {
		t.Error("Load of meta whose id != directory name returned nil error; must reject")
	}
}

// FIX 1(b) — Scan silently skips a session whose meta id does not match its dir,
// exactly as it skips corrupt sessions, so one mislabeled dir never hides the rest.
func TestScanSkipsMismatchedID(t *testing.T) {
	s, dir := newTestStore(t)
	good := fullMeta()
	good.ID = "good"
	if err := s.Save(good); err != nil {
		t.Fatalf("Save good: %v", err)
	}
	badDir := filepath.Join(dir, "mismatch")
	if err := os.MkdirAll(badDir, 0o700); err != nil {
		t.Fatalf("mkdir mismatch: %v", err)
	}
	body := `{"schema_version":1,"id":"notmismatch","agent_type":"claude"}`
	if err := os.WriteFile(filepath.Join(badDir, "meta.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write mismatch meta: %v", err)
	}
	sessions, err := s.Scan()
	if err != nil {
		t.Fatalf("Scan error: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != "good" {
		t.Errorf("Scan should skip the mismatched-id session, got %+v", sessions)
	}
}

// FIX 1(c) — Save unconditionally stamps the current SchemaVersion before
// marshaling: a caller cannot persist an arbitrary (here future) version.
func TestSaveStampsCurrentSchemaVersion(t *testing.T) {
	s, dir := newTestStore(t)
	m := fullMeta()
	m.SchemaVersion = 999
	if err := s.Save(m); err != nil {
		t.Fatalf("Save error: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, m.ID, "meta.json"))
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}
	var disk struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(raw, &disk); err != nil {
		t.Fatalf("unmarshal disk meta: %v", err)
	}
	if disk.SchemaVersion != SchemaVersion {
		t.Errorf("on-disk schema_version = %d, want %d (Save must stamp current)", disk.SchemaVersion, SchemaVersion)
	}
}

// FIX 2 — Save applies FilterEnv before persisting (single choke point, ADR-004):
// a secret or injection vector in Env never reaches disk; allowlisted entries
// survive verbatim.
func TestSaveFiltersEnvBeforePersist(t *testing.T) {
	s, dir := newTestStore(t)
	m := fullMeta()
	m.Env = []string{
		"PATH=/usr/bin:/bin",
		"AWS_SECRET_ACCESS_KEY=topsecret",
		"ANTHROPIC_API_KEY=sk-ant-test",
		"LD_PRELOAD=/evil.so",
	}
	if err := s.Save(m); err != nil {
		t.Fatalf("Save error: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, m.ID, "meta.json"))
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}
	if strings.Contains(string(raw), "AWS_SECRET_ACCESS_KEY") || strings.Contains(string(raw), "LD_PRELOAD") {
		t.Errorf("on-disk meta.json still contains a dropped env key:\n%s", raw)
	}
	got, err := s.Load(m.ID)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	want := []string{"PATH=/usr/bin:/bin", "ANTHROPIC_API_KEY=sk-ant-test"}
	if !reflect.DeepEqual(got.Env, want) {
		t.Errorf("persisted env = %v, want %v (allowlisted entries survive verbatim)", got.Env, want)
	}
}

// FIX 4 — NewStore hardens a pre-existing root to 0700 and Save hardens a
// pre-existing session dir to 0700; MkdirAll alone leaves an existing dir's mode
// untouched, so the chmod must be unconditional. Verified under a permissive
// umask so the modes come from the code, not an inherited umask.
func TestHardenPreexistingDirs(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)

	root := filepath.Join(t.TempDir(), "sessions")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("pre-create root: %v", err)
	}
	m := fullMeta()
	sessionDir := filepath.Join(root, m.ID)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("pre-create session dir: %v", err)
	}

	s, err := NewStore(root)
	if err != nil {
		t.Fatalf("NewStore error: %v", err)
	}
	checkPerm(t, root, 0o700) // NewStore hardened the pre-existing root

	if err := s.Save(m); err != nil {
		t.Fatalf("Save error: %v", err)
	}
	checkPerm(t, root, 0o700)
	checkPerm(t, sessionDir, 0o700) // Save hardened the pre-existing session dir
}

// FIX 6 — a session path that is a symlink must never be followed out of the
// store root: Save/Load/Delete error, Scan skips it, and nothing is written into
// the symlink target.
func TestSymlinkSessionPathRejected(t *testing.T) {
	s, root := newTestStore(t)
	outside := t.TempDir()
	link := filepath.Join(root, "evil")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	m := fullMeta()
	m.ID = "evil"
	if err := s.Save(m); err == nil {
		t.Error("Save into a symlinked session path returned nil error; must reject")
	}
	if _, err := s.Load("evil"); err == nil {
		t.Error("Load of a symlinked session path returned nil error; must reject")
	}
	if err := s.Delete("evil"); err == nil {
		t.Error("Delete of a symlinked session path returned nil error; must reject")
	}

	sessions, err := s.Scan()
	if err != nil {
		t.Fatalf("Scan must not error on a symlinked entry: %v", err)
	}
	for _, sm := range sessions {
		if sm.ID == "evil" {
			t.Error("Scan returned a symlinked session; must skip it")
		}
	}

	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatalf("read outside dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("symlinked session path allowed writes outside the store root: %v", entries)
	}
}
