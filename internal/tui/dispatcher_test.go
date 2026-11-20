package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

func TestDispatcherOnMainBoardUsesItsOwnSupervisor(t *testing.T) {
	local := sWorking("home/local", "codex", "/repo", "local work", time.Minute)
	dispatcher := sWorking("discord/session", "codex", "/dispatcher", "worker dispatch", time.Second)
	dispatcher.Role = "manager"
	dispatcher.ManagerConversationID = "discord-teammate-thread"
	dispatcher.Tag = "some-other-tag"
	dispatcher.EndpointID = "discord"
	dispatcher.Supersedes = []string{"discord/former"}
	worker := sWorking("discord/worker", "codex", "/repo", "dispatched work", time.Second)
	worker.EndpointID = "discord"
	worker.SpawnedFrom = "former"
	foreign := local
	foreign.ID = "discord/unrelated"
	primary, secondary := &recordingRunner{}, &recordingRunner{}
	var killed, deleted string
	m := New(newFakeClient(local), detectMixed(), WithAttachRunner(primary.run), WithLinkedDispatchers(func() ([]protocol.SessionView, error) {
		return []protocol.SessionView{dispatcher, worker, foreign}, nil
	}, secondary.run, func(id string, kill bool) error {
		if kill {
			killed = id
		} else {
			deleted = id
		}
		return nil
	})).(rootModel)
	model, _ := m.Update(tea.WindowSizeMsg{Width: testCols, Height: testRows})
	m = model.(rootModel)
	model, _ = m.Update(m.loadDispatchers())
	m = model.(rootModel)
	m.reconcileRoster([]protocol.SessionView{local})
	for _, group := range []groupingMode{groupByStatus, groupByRepo, groupByTag} {
		m.general.setLayout(group, orderByName)
		if len(m.general.sessions) != 3 || m.general.sectionOrder()[0] != dispatcherSection || !strings.Contains(m.boardView(), "DISPATCHER") {
			t.Fatalf("missing or mixed dispatcher in grouping %v: %s", group, m.boardView())
		}
	}
	if row, ok := m.general.sessionByID(worker.ID); !ok || row.Tag != "DSP" {
		t.Fatal("historical worker missing DSP tag")
	}
	m.general.commandID = dispatcher.ID
	if _, ok := m.boardCommandSession(); ok {
		t.Fatal("foreign manager accepted a primary-daemon mutation")
	}
	_, open := m.openSelected()
	if open == nil {
		t.Fatal("manager not attachable from the main board")
	}
	open()
	if len(secondary.calls) != 1 || len(primary.calls) != 0 {
		t.Fatal("wrong attach endpoint")
	}
	model, _ = m.updateGeneral(actionKey("x"))
	confirmed := model.(rootModel)
	if !confirmed.general.confirm {
		t.Fatal("missing linked confirmation")
	}
	_, cmd := confirmed.updateConfirm(tea.KeyPressMsg{Text: "y"})
	cmd()
	if killed != dispatcher.ID {
		t.Fatalf("wrong linked target %q", killed)
	}
	dispatcher.Status.Process = status.ProcessExited
	model, _ = m.Update(m.loadDispatchers())
	m = model.(rootModel)
	m.general.commandID = dispatcher.ID
	model, _ = m.updateGeneral(actionKey("x"))
	confirmed = model.(rootModel)
	_, cmd = confirmed.updateConfirm(tea.KeyPressMsg{Text: "y"})
	if cmd == nil {
		t.Fatal("missing linked delete")
	}
	cmd()
	if deleted != dispatcher.ID {
		t.Fatalf("wrong delete target %q", deleted)
	}
	model, _ = m.Update(dispatcherRows(nil))
	m = model.(rootModel)
	if len(m.general.sessions) != 1 || m.general.sessions[0].ID != local.ID {
		t.Fatal("lost primary roster after removing secondary")
	}
}
