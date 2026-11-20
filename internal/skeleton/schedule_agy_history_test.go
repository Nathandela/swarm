package skeleton

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/agyacp"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

func TestScheduledAgyHistorySurvivesRetag(t *testing.T) {
	dir := t.TempDir()
	store, err := persist.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := persist.Meta{ID: "seed", AgentType: "agy", ScheduleProjected: true, ScheduledCompleted: true, Tag: "automation", ScheduleAttempt: "attempt", ConversationID: "360f65e0-35d0-43ef-9039-7f33f3379081", Status: status.Status{Process: status.ProcessExited}}
	if err := store.Save(m); err != nil {
		t.Fatal(err)
	}
	core, err := daemon.Open(daemon.Config{StateDir: dir, SocketPath: filepath.Join(dir, "d.sock"), LockPath: filepath.Join(dir, "d.lock"), LogPath: filepath.Join(dir, "d.log")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.Close() })
	api := coreAPI{core: core, endpointID: "ep", scheduleStateDir: dir}
	session := protocol.NamespacedID("ep", m.ID)
	evidence := agyacp.Evidence{Attempt: m.ScheduleAttempt, Conversation: m.ConversationID, EndTurn: true, Text: "Final native answer."}
	save := func() {
		t.Helper()
		raw, err := json.Marshal(evidence)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, m.ID, "schedule-agy-evidence.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	save()
	for _, tag := range []string{"automation", "reviewed"} {
		if err := core.SetTag(m.ID, tag); err != nil {
			t.Fatal(err)
		}
		records, floor, code, err := api.InteractionHistory(session, "", 1)
		if err != nil || code != "" || !floor || len(records) != 1 {
			t.Fatalf("tag %q: records=%v floor=%v code=%q err=%v", tag, records, floor, code, err)
		}
		var item struct{ Text string }
		if err := json.Unmarshal(records[0].Item, &item); err != nil || item.Text != evidence.Text {
			t.Fatalf("item=%v err=%v", item, err)
		}
	}
	if rows, floor, _, err := api.InteractionHistory(session, "agy-schedule-closing", 1); err != nil || len(rows) != 0 || !floor {
		t.Fatalf("older=%v floor=%v err=%v", rows, floor, err)
	}
	if _, _, code, err := api.InteractionHistory(session, "", 0); err == nil || code != protocol.CodeInvalidField {
		t.Fatalf("limit 0: code=%v err=%v", code, err)
	}
	evidence.Attempt = "other-attempt"
	save()
	if _, ok := api.scheduledAgyHistory(session, ""); ok {
		t.Fatal("exposed evidence from a different attempt")
	}
	evidence.Attempt = m.ScheduleAttempt
	evidence.Conversation = "other-conversation"
	save()
	if _, ok := api.scheduledAgyHistory(session, ""); ok {
		t.Fatal("exposed evidence from a different conversation")
	}
}
