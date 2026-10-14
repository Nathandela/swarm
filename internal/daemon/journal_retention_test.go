package daemon

import (
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/journal"
)

// TestDaemon_TheJournalIsOpenedWithBoundedRetention pins R-JRN.6 ("retention rotates segments to
// bound disk") at the daemon: journal.Open's zero Options disable both rotation and retention,
// so the live journal grew to 90 segments / 603 MB, all loaded into memory at every start
// (daemon RSS 1.46 GB). Every daemon start adds a segment, so of 100 pre-existing ones only the
// owner's chosen 64 may remain after the first append.
func TestDaemon_TheJournalIsOpenedWithBoundedRetention(t *testing.T) {
	cfg := daemonConfig(t)
	dir := filepath.Join(cfg.StateDir, "journal")
	for i := 0; i < 100; i++ {
		j, err := journal.Open(dir) // each open starts a fresh segment, as each daemon start does
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
	if len(segs) != 64 {
		t.Errorf("journal holds %d segments after an append, want the bounded 64", len(segs))
	}
}
