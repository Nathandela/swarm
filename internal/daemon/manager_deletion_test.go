package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/journal"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/status"
)

func managerDeletionBinding(local string) schema.ManagerRuntimeBinding {
	return schema.ManagerRuntimeBinding{Session: local, SessionInstance: strings.Repeat("a", 32), Provider: "codex",
		ConversationID: "discord-teammate-discussion", NativeSessionID: "thread", NativeGeneration: strings.Repeat("b", 32),
		ObservationGeneration: "observer", LoginEpoch: "login"}
}

func seedManagerToDelete(t *testing.T, d *Daemon, b schema.ManagerRuntimeBinding) {
	t.Helper()
	m := persist.Meta{ID: b.Session, Cwd: t.TempDir(), Role: "manager", AgentType: b.Provider, ManagerConversationID: b.ConversationID,
		ConversationID: b.NativeSessionID, ShimPID: 111, ShimStartTime: 222, Status: status.Status{Process: status.ProcessExited}}
	if err := d.saveMeta(m); err != nil {
		t.Fatal(err)
	}
	raw := fmt.Sprintf("%s %d %d\n", b.SessionInstance, m.ShimPID, m.ShimStartTime)
	if err := os.WriteFile(filepath.Join(d.sessionDir(m.ID), "session-instance"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestManagerDeletionExactWitnessSurvivesRestartAndJournalRotation(t *testing.T) {
	b := managerDeletionBinding("newly-deleted")
	cfg := daemonConfig(t)
	cfg.ManagerDeletionGeneration = func(persist.Meta) string { return b.NativeGeneration }
	d := openDaemon(t, cfg)
	seedManagerToDelete(t, d, b)
	if d.ManagerDeletionStatus(b, true) != "" {
		t.Fatal("existing manager treated as deleted")
	}
	if err := d.Delete(b.Session); err != nil {
		t.Fatal(err)
	}
	if d.ManagerDeletionStatus(b, false) != schema.ManagerRetireDeleted {
		t.Fatal("verified deletion unavailable before restart")
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(cfg.StateDir, "journal")); err != nil {
		t.Fatal(err)
	}
	d = openDaemon(t, cfg)
	if d.ManagerDeletionStatus(b, false) != schema.ManagerRetireDeleted {
		t.Fatal("durable deletion proof required an unrotated journal")
	}
	for _, change := range []string{"instance", "provider", "conversation", "native_id", "native_generation"} {
		wrong := b
		switch change {
		case "instance":
			wrong.SessionInstance = strings.Repeat("c", 32)
		case "provider":
			wrong.Provider = "claude"
		case "conversation":
			wrong.ConversationID += "-other"
		case "native_id":
			wrong.NativeSessionID += "-other"
		case "native_generation":
			wrong.NativeGeneration = strings.Repeat("d", 32)
		}
		if d.ManagerDeletionStatus(wrong, true) != "" {
			t.Fatalf("mismatched %s inherited deletion proof", change)
		}
	}
	// A state object, even an unreadable one, defeats the positive claim.
	if err := os.Mkdir(d.sessionDir(b.Session), 0700); err != nil {
		t.Fatal(err)
	}
	if d.ManagerDeletionStatus(b, true) != "" {
		t.Fatal("deletion evidence hid a conflicting state directory")
	}
}

func TestManagerDeletionDoesNotConfirmFailedDeleteOrModernUnprovedStop(t *testing.T) {
	b := managerDeletionBinding("unproved-deletion")
	cfg := daemonConfig(t)
	var generation string
	cfg.ManagerDeletionGeneration = func(persist.Meta) string { return generation }
	d := openDaemon(t, cfg)
	seedManagerToDelete(t, d, b)
	if _, err := d.appendJournal(journal.Record{SessionID: b.Session, SessionInstance: b.SessionInstance, Role: "manager", Agent: b.Provider, Type: journal.TypeExited}); err != nil {
		t.Fatal(err)
	}
	generation = b.NativeGeneration
	original := deleteSessionDirFn
	cleanupErr := errors.New("test partial deletion")
	deleteSessionDirFn = func(*Daemon, string) error { return cleanupErr }
	t.Cleanup(func() { deleteSessionDirFn = original })
	if err := d.Delete(b.Session); !errors.Is(err, cleanupErr) || d.ManagerDeletionStatus(b, true) != "" {
		t.Fatalf("failed deletion became positive evidence: %v", err)
	}
	deleteSessionDirFn = original
	generation = ""
	if err := d.Delete(b.Session); err != nil {
		t.Fatal(err)
	}
	if d.ManagerDeletionStatus(b, true) != "" {
		t.Fatal("unproved native stop inherited a previous positive witness")
	}
	// Even when the new sidecar is absent, its journal marker cannot fall back
	// to the weaker proof reserved for deletions made before this version.
	if err := os.Remove(filepath.Join(cfg.StateDir, managerDeletionDirectory, b.Session+".json")); err != nil {
		t.Fatal(err)
	}
	if d.ManagerDeletionStatus(b, true) != "" {
		t.Fatal("modern unproved stop fell back to legacy journal proof")
	}
}

func TestManagerDeletionLegacyExactExitAndExplicitDelete(t *testing.T) {
	b := managerDeletionBinding("legacy-deleted")
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)
	exited := journal.Record{SessionID: b.Session, SessionInstance: b.SessionInstance, Role: "manager", Agent: b.Provider, Type: journal.TypeExited}
	deleted := journal.Record{SessionID: b.Session, SessionInstance: b.SessionInstance, Role: "manager", Type: journal.TypeDeleted}
	if d.ManagerDeletionStatus(b, true) != "" {
		t.Fatal("bare absence became deletion")
	}
	if _, err := d.appendJournal(exited); err != nil {
		t.Fatal(err)
	}
	if d.ManagerDeletionStatus(b, true) != "" {
		t.Fatal("exit without explicit deletion was accepted")
	}
	if _, err := d.appendJournal(deleted); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d = openDaemon(t, cfg)
	if d.ManagerDeletionStatus(b, true) != schema.ManagerRetireDeletedParked {
		t.Fatal("v0.29.15 exact exit and deletion did not survive restart for a parked manager")
	}
	if d.ManagerDeletionStatus(b, false) != "" {
		t.Fatal("legacy exit and deletion became full writer-stop proof")
	}
	wrong := b
	wrong.SessionInstance += "wrong"
	if d.ManagerDeletionStatus(wrong, true) != "" {
		t.Fatal("wrong legacy incarnation was accepted")
	}
	restored := exited
	restored.Type = journal.TypeSessionState
	if _, err := d.appendJournal(restored); err != nil {
		t.Fatal(err)
	}
	if d.ManagerDeletionStatus(b, true) != "" {
		t.Fatal("newer restored row did not invalidate legacy deletion")
	}
}

func TestManagerDeletionLegacyProofRequiresBothEventsAboveRetainedFloor(t *testing.T) {
	dir := t.TempDir()
	j, err := journal.Open(filepath.Join(dir, "journal"), journal.Options{
		MaxBytes: 1024, MaxFiles: 4, MaxTotalBytes: 4096, LiveBytes: 2048,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	d := &Daemon{cfg: Config{StateDir: dir}, journal: j}
	appendEvent := func(r journal.Record) {
		t.Helper()
		if _, err := j.Append(r); err != nil {
			t.Fatal(err)
		}
	}
	b := managerDeletionBinding("retained-legacy-deleted")
	exited := journal.Record{SessionID: b.Session, SessionInstance: b.SessionInstance, Role: "manager", Agent: b.Provider, Type: journal.TypeExited}
	deleted := journal.Record{SessionID: b.Session, SessionInstance: b.SessionInstance, Role: "manager", Type: journal.TypeDeleted}
	advanceFloor := func() {
		for range 16 {
			appendEvent(journal.Record{Type: journal.TypePresence, Payload: []byte(`"` + strings.Repeat("x", 600) + `"`)})
		}
	}
	advanceFloor()
	resync, err := j.ReadFrom(1)
	if err != nil || !resync.FullResync {
		t.Fatalf("fixture did not move the retained floor: %+v %v", resync, err)
	}
	appendEvent(exited)
	appendEvent(deleted)
	if d.ManagerDeletionStatus(b, true) != schema.ManagerRetireDeletedParked {
		t.Fatal("old retained floor hid the complete exact parked proof")
	}
	advanceFloor()
	appendEvent(deleted)
	if d.ManagerDeletionStatus(b, true) != "" {
		t.Fatal("a surviving deletion without its compacted exit became proof")
	}
}
