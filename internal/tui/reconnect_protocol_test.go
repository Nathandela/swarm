package tui

import (
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

type reconnectProtocolBackend struct {
	protocol.DaemonAPI
	mu     sync.Mutex
	row    persist.Meta
	events chan persist.Meta
}

func (b *reconnectProtocolBackend) List() []persist.Meta {
	b.mu.Lock()
	defer b.mu.Unlock()
	return []persist.Meta{b.row}
}
func (b *reconnectProtocolBackend) Events() <-chan persist.Meta { return b.events }
func serveReconnectProtocol(t *testing.T, b *reconnectProtocolBackend, socket string) func() {
	t.Helper()
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := protocol.NewServer(b, "owner")
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go server.ServeConn(conn)
		}
	}()
	var once sync.Once
	closeServer := func() { once.Do(func() { _ = listener.Close(); _ = server.Close() }) }
	t.Cleanup(closeServer)
	return closeServer
}
func TestReconnect_RealOwnerProtocolSocketReplacement(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "sw-rec-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(root) }()
	socket := filepath.Join(root, "owner.sock")
	backend := &reconnectProtocolBackend{row: persist.Meta{ID: "task", AgentType: "codex", Name: "Original", Status: status.Status{Process: status.ProcessRunning, Turn: status.TurnActive}}, events: make(chan persist.Meta, 32)}
	closeFirst := serveReconnectProtocol(t, backend, socket)
	dial := func() (Client, error) { return protocol.Dial(socket, []string{"subscribe"}) }
	client, err := dial()
	if err != nil {
		t.Fatal(err)
	}
	m := New(client, nil, WithDaemonReconnector(dial)).(rootModel)
	defer func() { _ = m.Close() }()
	m.general.restoreSel("owner/task")
	closeFirst()
	loss := waitForEvent(m.events)()
	next, _ := m.Update(loss)
	m = next.(rootModel)
	if !m.connectionLost {
		t.Fatal("socket close was not observed")
	}
	backend.mu.Lock()
	backend.row.Name = "Fresh"
	backend.mu.Unlock()
	closeSecond := serveReconnectProtocol(t, backend, socket)
	defer closeSecond()
	next, cmd := m.reconnectDaemon(daemonReconnectTickMsg{generation: m.reconnectGeneration})
	m = next.(rootModel)
	next, _ = m.Update(cmd())
	m = next.(rootModel)
	row, ok := m.general.sessionByID("owner/task")
	if m.connectionLost || !ok || row.Name != "Fresh" || m.general.selectedID() != "owner/task" {
		t.Fatal("replacement did not restore the same session and selected identity")
	}
	// An older producer snapshot arriving after hydration must be only an invalidation.
	backend.events <- persist.Meta{ID: "task", AgentType: "codex", Name: "Obsolete", Status: status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle}}
	event := waitForEvent(m.events)()
	next, _ = m.Update(event)
	m = next.(rootModel)
	row, _ = m.general.sessionByID("owner/task")
	if row.Name != "Fresh" {
		t.Fatal("old producer event overwrote fresh hydration")
	}
	// Refresh through the real RPC connection; later attention is still observable.
	backend.mu.Lock()
	backend.row.Status.Turn = status.TurnIdle
	backend.row.Status.Interaction = status.InteractionError
	backend.mu.Unlock()
	m.rosterRefreshing = false
	next, refresh := m.refreshRoster()
	m = next.(rootModel)
	next, _ = m.Update(refresh())
	m = next.(rootModel)
	row, _ = m.general.sessionByID("owner/task")
	if row.Group != status.GroupNeedsInput || row.Status.Interaction != status.InteractionError {
		t.Fatal("recovered protocol stream could not refresh attention")
	}
}
