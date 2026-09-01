package daemon

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/journal"
	"github.com/Nathandela/swarm/internal/status"
)

func TestDaemon_JournalSubscribeFromCarriesRosterAtBoundaryThenLive(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	m := namedMeta("s1", "claude", "live", status.ProcessRunning, status.InteractionNone)
	d.putMem(m)
	res, live, cancel, err := d.JournalSubscribeFrom(0)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if len(res.Roster) != 1 || res.Roster[0].SessionID != m.ID || res.Roster[0].Type != journal.TypeRoster {
		t.Fatalf("atomic roster = %#v, want live session at boundary", res.Roster)
	}
	if err := d.RecordGatewayPresence(true); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-live:
		if got.Type != journal.TypePresence || got.Cursor <= res.Cursor {
			t.Fatalf("live record = %#v, want presence after boundary %d", got, res.Cursor)
		}
	case <-time.After(time.Second):
		t.Fatal("post-boundary append did not reach atomic feed")
	}
}
