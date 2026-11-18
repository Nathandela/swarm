package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/journal"
	"github.com/Nathandela/swarm/internal/status"
)

func TestJournalInstanceIsCapturedBeforeQueueAndSurvivesReplacementReplay(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)
	m := metaWith("worker", "codex", status.ProcessRunning, status.InteractionNone)
	m.ShimPID = 101
	m.ShimStartTime = 202
	m.Role = "manager"
	if err := d.store.Save(m); err != nil {
		t.Fatal(err)
	}
	d.putMem(m)
	instancePath := filepath.Join(cfg.StateDir, m.ID, "session-instance")
	if err := os.WriteFile(instancePath, []byte("old-instance 101 202"), 0600); err != nil {
		t.Fatal(err)
	}
	live, cancel := d.JournalSubscribe()
	defer cancel()
	m.Status.Process = status.ProcessExited
	if err := d.saveMeta(m); err != nil {
		t.Fatal(err)
	}
	queued := <-live
	m.ShimPID = 303
	m.ShimStartTime = 404
	m.Role = ""
	if err := d.store.Save(m); err != nil {
		t.Fatal(err)
	}
	d.putMem(m)
	if err := os.WriteFile(instancePath, []byte("new-instance 303 404"), 0600); err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"provider":"codex","session_instance":"new-instance"}`)
	if _, err := d.RecordSessionStateForIncarnation(m.ID, m.ShimPID, m.ShimStartTime, func() (json.RawMessage, error) { return payload, nil }); err != nil {
		t.Fatal(err)
	}
	if queued.SessionInstance != "old-instance" || queued.Role != "manager" {
		t.Fatalf("queued event lost original identity: %+v", queued)
	}
	before, err := d.JournalReadFrom(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Events) != 2 || before.Events[0].SessionInstance != "old-instance" || before.Events[1].SessionInstance != "new-instance" || before.Events[1].Role != "" {
		t.Fatalf("publication identity is not exact: %+v", before.Events)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	after, err := reopened.JournalReadFrom(0)
	if err != nil || after.SourceID != before.SourceID || len(after.Events) != 2 || after.Events[0].SessionInstance != "old-instance" || after.Events[0].Role != "manager" {
		t.Fatalf("replay borrowed replacement identity: %+v %v", after, err)
	}
}

func TestJournalLegacyOrMismatchedIncarnationRemainsUnknown(t *testing.T) {
	for _, content := range []string{"legacy-bare-instance", "other-instance 999 202"} {
		t.Run(content, func(t *testing.T) {
			d := openDaemon(t, daemonConfig(t))
			m := metaWith("legacy", "codex", status.ProcessRunning, status.InteractionNone)
			m.ShimPID = 101
			m.ShimStartTime = 202
			if err := d.store.Save(m); err != nil {
				t.Fatal(err)
			}
			d.putMem(m)
			if err := os.WriteFile(filepath.Join(d.cfg.StateDir, m.ID, "session-instance"), []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			m.Status.Process = status.ProcessExited
			if err := d.saveMeta(m); err != nil {
				t.Fatal(err)
			}
			res, err := d.JournalReadFrom(0)
			if err != nil || len(res.Events) != 1 || res.Events[0].SessionInstance != "" || res.Events[0].Type != journal.TypeExited {
				t.Fatalf("invented historical instance: %+v %v", res, err)
			}
		})
	}
}
