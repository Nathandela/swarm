package journal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSourceIdentityStableAcrossOpenAndNewDirectoryIsDistinct(t *testing.T) {
	dir := t.TempDir()
	j, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	first, err := j.ReadFrom(0)
	if err != nil || first.SourceID == "" {
		t.Fatalf("missing namespace: %+v %v", first, err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := j.ReadFrom(0)
	if err != nil || second.SourceID != first.SourceID {
		t.Fatalf("restart changed namespace: %+v %v", second, err)
	}
	_ = j.Close()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	j, err = Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	third, err := j.ReadFrom(0)
	if err != nil || third.SourceID == first.SourceID || third.SourceID == "" {
		t.Fatalf("new directory inherited namespace: %+v %v", third, err)
	}
}

func TestSourceIdentityLegacyMigrationAndInterruptedMarkerPreserveCursor(t *testing.T) {
	dir := t.TempDir()
	record := EncodeRecord(Record{SchemaVersion: 1, Cursor: 77, SessionID: "legacy", Type: TypeExited})
	if err := os.WriteFile(filepath.Join(dir, segName(1)), append(record, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	id, err := newJournalSourceID()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(journalSourceDocument{Version: 1, SourceID: id})
	if err := os.WriteFile(filepath.Join(dir, journalSourceFile), raw, 0600); err != nil {
		t.Fatal(err)
	}
	j, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	res, err := j.ReadFrom(0)
	if err != nil || res.SourceID != id || res.Cursor != 77 || len(res.Events) != 1 {
		t.Fatalf("migration changed source/cursor: %+v %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(dir, journalSourceMarker)); err != nil {
		t.Fatal("interrupted creation marker was not completed")
	}
}

func TestSourceIdentityMutationAndDirectoryReplacementRefuseOldHandle(t *testing.T) {
	for _, mode := range []struct {
		name string
		opts Options
	}{
		{"legacy", Options{}},
		{"live", liveOptions()},
	} {
		for _, replacement := range []string{"identity", "directory"} {
			t.Run(mode.name+"/"+replacement, func(t *testing.T) {
				dir := t.TempDir()
				j, err := Open(dir, mode.opts)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = j.Close() }()
				mustAppend(t, j, Record{Type: TypePresence})
				if replacement == "directory" {
					moved := dir + "-old"
					if err := os.Rename(dir, moved); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.RemoveAll(moved) })
					if err := os.Mkdir(dir, 0700); err != nil {
						t.Fatal(err)
					}
				}
				id, err := newJournalSourceID()
				if err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(journalSourceDocument{Version: 1, SourceID: id})
				for _, name := range []string{journalSourceFile, journalSourceMarker} {
					if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := j.Append(Record{Type: TypePresence}); err == nil {
					t.Fatal("old source handle appended after custody changed")
				}
				if err := j.Recover(); err == nil {
					t.Fatal("Recover cleared storage failure despite tampered journal source custody")
				}
				if _, err := j.Append(Record{Type: TypePresence}); err == nil {
					t.Fatal("old source handle appended after refused Recover")
				}
				if j.Cursor() != 1 {
					t.Fatal("refused source mutation consumed a cursor")
				}
				if _, err := j.ReadFrom(0); err == nil {
					t.Fatal("old handle returned a trusted snapshot after custody changed")
				}
			})
		}
	}
}

func TestSourceIdentityRejectsHardlinkAndWritableDirectory(t *testing.T) {
	for _, unsafe := range []string{"hardlink", "directory"} {
		t.Run(unsafe, func(t *testing.T) {
			dir := t.TempDir()
			j, err := Open(dir, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			if unsafe == "directory" {
				if err := os.Chmod(dir, 0777); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Link(filepath.Join(dir, journalSourceFile), filepath.Join(t.TempDir(), "alias")); err != nil {
					t.Fatal(err)
				}
			}
			if reopened, err := Open(dir, Options{}); err == nil {
				_ = reopened.Close()
				t.Fatal("unsafe namespace custody was accepted")
			}
		})
	}
}

func TestSourceIdentityMalformedUnsafeAndFutureFailBeforeCleanup(t *testing.T) {
	for _, kind := range []string{"malformed", "future", "symlink", "public", "missing"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			j, err := Open(dir, Options{})
			if err != nil {
				t.Fatal(err)
			}
			mustAppend(t, j, Record{Type: TypeLaunched, SessionID: "old"})
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "source.json")
			switch kind {
			case "malformed":
				err = os.WriteFile(path, []byte(`{"version":1,"source_id":"broken"}`), 0600)
			case "future":
				err = os.WriteFile(path, []byte(`{"version":2,"source_id":"00000000-0000-4000-8000-000000000001"}`), 0600)
			case "symlink":
				_ = os.Remove(path)
				target := filepath.Join(t.TempDir(), "identity")
				if err = os.WriteFile(target, []byte(`{"version":1,"source_id":"00000000-0000-4000-8000-000000000001"}`), 0600); err == nil {
					err = os.Symlink(target, path)
				}
			case "public":
				if _, e := os.Stat(path); os.IsNotExist(e) {
					err = os.WriteFile(path, []byte(`{}`), 0600)
				}
				if err == nil {
					err = os.Chmod(path, 0644)
				}
			case "missing":
				err = os.Remove(path)
				if os.IsNotExist(err) {
					err = nil
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadDir(dir)
			opened, err := Open(dir, Options{MaxFiles: 1, MaxTotalBytes: 1})
			if err == nil {
				_ = opened.Close()
				t.Fatal("damaged journal identity was silently reset/ignored")
			}
			after, _ := os.ReadDir(dir)
			if len(after) != len(before) {
				t.Fatal("identity refusal changed journal retention files")
			}
		})
	}
}
