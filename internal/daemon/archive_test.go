package daemon

import (
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
	"testing"
)

func TestArchivePersistsHistoryAndRefusesLiveActors(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	if err := d.saveMeta(persist.Meta{ID: "ended", AgentType: "codex", Status: status.Status{Process: status.ProcessExited}}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetRosterHidden("ended", true); err != nil {
		t.Fatal(err)
	}
	disk, err := d.store.Load("ended")
	if err != nil || !disk.RosterHidden {
		t.Fatalf("archive not durable: %+v %v", disk, err)
	}
	if err := d.saveMeta(persist.Meta{ID: "live", AgentType: "codex", Status: status.Status{Process: status.ProcessRunning}}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetRosterHidden("live", true); err == nil {
		t.Fatal("live actor hidden")
	}
	live, ok := d.Get("live")
	if !ok || live.RosterHidden {
		t.Fatal("refused archive changed live actor")
	}
}
