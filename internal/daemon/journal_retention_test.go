package daemon

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/journal"
)

// TestDaemon_TheJournalIsOpenedWithBoundedRetention pins R-JRN.6 ("retention rotates segments to
// bound disk") at the daemon: journal.Open's zero Options disable both rotation and retention,
// so the live journal grew without bound (90 segments / 603 MB, all mirrored in memory). The
// segment count bounds growth, not a single start's load (the total-size cap does that). Every
// a legacy journal is compacted into the small control budget when live-only opens it.
func TestDaemon_TheJournalIsOpenedWithBoundedRetention(t *testing.T) {
	cfg := daemonConfig(t)
	dir := filepath.Join(cfg.StateDir, "journal")
	for i := 0; i < 100; i++ {
		j, err := journal.Open(dir, journal.Options{}) // each open starts a fresh segment, as each daemon start does
		if err != nil {
			t.Fatal(err)
		}
		if _, err := j.Append(journal.Record{SessionID: "seed", Type: journal.TypeGroupTransition}); err != nil {
			t.Fatal(err)
		}
		_ = j.Close()
	}

	d := openDaemon(t, cfg)
	if _, err := d.journal.Append(journal.Record{SessionID: "probe", Type: journal.TypeGroupTransition}); err != nil {
		t.Fatalf("append: %v", err)
	}
	segs, _ := filepath.Glob(filepath.Join(dir, "seg-*.log"))
	if len(segs) > journalMaxFiles {
		t.Errorf("journal holds %d segments after an append, max %d", len(segs), journalMaxFiles)
	}
}

func TestDaemon_ConversationJournalIsLiveOnly(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)
	if err := d.RecordInteraction("session", InteractionItem{V: InteractionSchemaVersion, ItemID: "message", Kind: "agent_message", Status: "completed", Fields: json.RawMessage(`{"text":"host-live-only-body"}`)}); err != nil {
		t.Fatal(err)
	}
	res, err := d.JournalReadFrom(0)
	if err != nil || len(res.Events) != 1 || res.Events[0].Type != journal.TypeInteraction {
		t.Fatalf("live records = %+v, %v", res.Events, err)
	}
	paths, err := filepath.Glob(filepath.Join(cfg.StateDir, "journal", "seg-*.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte("host-live-only-body")) {
			t.Fatal("production daemon persisted a conversation")
		}
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d = openDaemon(t, cfg)
	res, err = d.JournalReadFrom(0)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range res.Events {
		if record.Type == journal.TypeInteraction {
			t.Fatal("production daemon replayed archived conversation payload")
		}
	}
}
