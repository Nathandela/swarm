package discord

import (
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
	"testing"
)

func TestWorkerProjectionIncludesEveryProviderAndKeepsIdentityAcrossNames(t *testing.T) {
	var rows []protocol.SessionView
	for _, agent := range []string{"claude", "codex", "agy", "hermes", "opencode"} {
		rows = append(rows, protocol.SessionView{ID: "local/" + agent, Agent: agent, Name: "Same name", Cwd: "/private/project", Status: status.Status{Process: status.ProcessRunning}, Capabilities: &protocol.SessionCapabilities{SessionInstance: agent + "-instance"}})
	}
	rows = append(rows, protocol.SessionView{ID: "local/old", RosterHidden: true})
	got := projectWorkers(rows)
	if len(got) != 5 {
		t.Fatalf("whole roster=%+v", got)
	}
	for i, w := range got {
		if w.ID != rows[i].ID || w.Instance != rows[i].Capabilities.SessionInstance || w.Name != "Same name" || w.Project != "project" {
			t.Fatalf("projection=%+v", w)
		}
	}
	rows[0].Name = "Renamed"
	if next := projectWorkers(rows)[0]; next.ID != got[0].ID || next.Instance != got[0].Instance {
		t.Fatal("rename changed identity")
	}
}
