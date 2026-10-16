package daemon

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/journal"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

func TestDelete_PreDeleteFailureKeepsSessionRetryableAcrossRestart(t *testing.T) {
	cfg := daemonConfig(t)
	cleanupErr := errors.New("worktree is still in use")
	fail := true
	calls := 0
	cfg.PreDelete = func(persist.Meta) error {
		calls++
		if fail {
			return cleanupErr
		}
		return nil
	}
	d := openDaemon(t, cfg)
	m := persist.Meta{ID: "retry-predelete", AgentType: "fake", Cwd: t.TempDir(), Status: status.Status{Process: status.ProcessRunning}}
	if err := d.saveMeta(m); err != nil {
		t.Fatal(err)
	}
	if err := d.Delete(m.ID); !errors.Is(err, cleanupErr) {
		t.Fatalf("Delete error = %v; want cleanup failure", err)
	}
	assertDeleteRetryable(t, d, m.ID, false)
	if got, _ := d.Get(m.ID); got.Status.Process != status.ProcessLost {
		t.Fatalf("failed deletion retained process status %q; want lost for the absent shim", got.Status.Process)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	fail = false
	d = openDaemon(t, cfg)
	if err := d.Delete(m.ID); err != nil {
		t.Fatalf("retry after restart: %v", err)
	}
	if calls != 2 {
		t.Fatalf("cleanup calls = %d; want failed attempt and retry", calls)
	}
	if _, ok := d.Get(m.ID); ok {
		t.Fatal("successful retry left the session in the registry")
	}
	if _, err := os.Stat(filepath.Join(cfg.StateDir, m.ID)); !os.IsNotExist(err) {
		t.Fatalf("successful retry left the directory: %v", err)
	}
}

func TestDelete_PartialDirectoryFailureKeepsSessionRetryable(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)
	m := persist.Meta{ID: "retry-directory", AgentType: "fake", Cwd: t.TempDir(), Status: status.Status{Process: status.ProcessExited}}
	if err := d.saveMeta(m); err != nil {
		t.Fatal(err)
	}
	removeErr := errors.New("session directory removal failed")
	originalRemove := deleteSessionDirFn
	deleteSessionDirFn = func(d *Daemon, id string) error {
		if err := os.Remove(filepath.Join(d.sessionDir(id), "meta.json")); err != nil {
			t.Fatal(err)
		}
		return removeErr
	}
	t.Cleanup(func() { deleteSessionDirFn = originalRemove })
	if err := d.Delete(m.ID); !errors.Is(err, removeErr) {
		t.Fatalf("Delete error = %v; want removal failure", err)
	}
	assertDeleteRetryable(t, d, m.ID, true)
	deleteSessionDirFn = originalRemove
	if err := d.Delete(m.ID); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if _, ok := d.Get(m.ID); ok {
		t.Fatal("successful retry left the session in the registry")
	}
}

func TestDelete_JournalFailureLeavesFilesUntouched(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)
	m := persist.Meta{ID: "retry-journal", AgentType: "fake", Cwd: t.TempDir(), Status: status.Status{Process: status.ProcessExited}}
	if err := d.saveMeta(m); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(d.sessionDir(m.ID), "transcript.log")
	if err := os.WriteFile(payload, []byte("retained output"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.journal.Close(); err != nil {
		t.Fatal(err)
	}
	if err := d.Delete(m.ID); err == nil {
		t.Fatal("Delete succeeded without a durable deletion record")
	}
	assertDeleteRetryable(t, d, m.ID, false)
	if raw, err := os.ReadFile(payload); err != nil || string(raw) != "retained output" {
		t.Fatalf("journal failure changed session payload: %q, %v", raw, err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d = openDaemon(t, cfg)
	if err := d.Delete(m.ID); err != nil {
		t.Fatalf("retry after restart: %v", err)
	}
}

func TestDelete_JournalsBeforeRemovingFiles(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)
	m := persist.Meta{ID: "delete-wal", AgentType: "fake", Cwd: t.TempDir(), Status: status.Status{Process: status.ProcessExited}}
	if err := d.saveMeta(m); err != nil {
		t.Fatal(err)
	}
	originalRemove := deleteSessionDirFn
	deleteSessionDirFn = func(d *Daemon, id string) error {
		files, err := filepath.Glob(filepath.Join(cfg.StateDir, "journal", "seg-*.log"))
		if err != nil || len(files) == 0 {
			t.Fatalf("read durable journal files: %v", err)
		}
		raw, err := os.ReadFile(files[len(files)-1])
		if err != nil {
			t.Fatal(err)
		}
		lines := bytes.Split(bytes.TrimSpace(raw), []byte{'\n'})
		record, err := journal.DecodeRecord(lines[len(lines)-1])
		if err != nil || record.SessionID != id || record.Type != journal.TypeDeleted {
			t.Fatalf("directory removal began before deletion was journaled: %+v, %v", record, err)
		}
		return originalRemove(d, id)
	}
	t.Cleanup(func() { deleteSessionDirFn = originalRemove })
	if err := d.Delete(m.ID); err != nil {
		t.Fatal(err)
	}
}

func assertDeleteRetryable(t *testing.T, d *Daemon, id string, compensated bool) {
	t.Helper()
	if _, ok := d.Get(id); !ok {
		t.Fatal("failed deletion removed the session from the registry")
	}
	if d.isDeleted(id) {
		t.Fatal("failed deletion tombstoned the session")
	}
	d.mu.Lock()
	stop := d.sessions[id].stop
	d.mu.Unlock()
	select {
	case <-stop:
		t.Fatal("failed deletion permanently stopped the retained session monitor")
	default:
	}
	if _, err := d.store.Load(id); err != nil {
		t.Fatalf("failed deletion lost durable retry metadata: %v", err)
	}
	res, err := d.JournalReadFrom(0)
	if err != nil {
		t.Fatal(err)
	}
	deleted, restored := false, false
	for _, rec := range res.Events {
		if rec.SessionID != id {
			continue
		}
		switch rec.Type {
		case journal.TypeDeleted:
			deleted, restored = true, false
		case journal.TypeSessionState:
			if deleted {
				restored = rec.Agent != "" && rec.Group != ""
			}
		}
	}
	if deleted != compensated || (deleted && !restored) {
		t.Fatalf("failed deletion journal: deleted=%v, restored=%v; want compensated=%v", deleted, restored, compensated)
	}
}
