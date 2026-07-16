package persist

// Orchestrator-pinned contract per ADR-004: ids are path-safe by validation.
// These are NEW tests; the frozen designer contract in persist_test.go is not
// touched. Session ids must match ^[A-Za-z0-9._-]{1,128}$ and must not be ".",
// "..", or start with "-"; Save/Load/Delete reject invalid ids and nothing is
// ever created outside the store root.

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func invalidIDs() []string {
	return []string{
		"",                       // empty
		".",                      // current dir
		"..",                     // parent dir
		"../escape",              // path traversal
		"a/b",                    // separator
		"nested/../escape",       // separator + traversal
		"-leading",               // leading dash (flag-like)
		"bad char",               // space
		"tab\tchar",              // control char
		"nul\x00byte",            // NUL
		strings.Repeat("a", 129), // one over the length cap
	}
}

func sortedDirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// Save, Load and Delete must each reject every invalid id, and rejecting them
// must not create anything inside the store root.
func TestInvalidIDsRejected(t *testing.T) {
	s, root := newTestStore(t)
	for _, id := range invalidIDs() {
		m := fullMeta()
		m.ID = id
		if err := s.Save(m); err == nil {
			t.Errorf("Save(id=%q) = nil error; want rejection", id)
		}
		if _, err := s.Load(id); err == nil {
			t.Errorf("Load(%q) = nil error; want rejection", id)
		}
		if err := s.Delete(id); err == nil {
			t.Errorf("Delete(%q) = nil error; want rejection", id)
		}
	}
	if names := sortedDirNames(t, root); len(names) != 0 {
		t.Errorf("store root should be empty after only invalid-id operations, got %v", names)
	}
}

// A traversal id must not create or touch anything outside the store root.
func TestTraversalCreatesNothingOutsideRoot(t *testing.T) {
	s, root := newTestStore(t)
	parent := filepath.Dir(root)
	before := sortedDirNames(t, parent)

	m := fullMeta()
	m.ID = "../escape"
	if err := s.Save(m); err == nil {
		t.Fatal("Save with traversal id returned nil error; must reject")
	}
	if _, err := os.Stat(filepath.Join(parent, "escape")); !os.IsNotExist(err) {
		t.Errorf("traversal created a path outside root: stat err = %v", err)
	}
	if after := sortedDirNames(t, parent); !reflect.DeepEqual(before, after) {
		t.Errorf("parent of root changed after rejected traversal Save:\n before: %v\n after:  %v", before, after)
	}
}

// Boundary-valid ids (single char, dots/underscores/dashes not leading, the
// 128-char cap) round-trip through Save/Load.
func TestValidBoundaryIDsAccepted(t *testing.T) {
	s, _ := newTestStore(t)
	for _, id := range []string{"a", "A0", "a.b_c-d", "with.dots", strings.Repeat("z", 128)} {
		m := fullMeta()
		m.ID = id
		if err := s.Save(m); err != nil {
			t.Errorf("Save valid id %q: %v", id, err)
			continue
		}
		got, err := s.Load(id)
		if err != nil {
			t.Errorf("Load valid id %q: %v", id, err)
			continue
		}
		if got.ID != id {
			t.Errorf("round-trip id = %q, want %q", got.ID, id)
		}
	}
}
