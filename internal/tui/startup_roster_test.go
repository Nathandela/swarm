package tui

import (
	"errors"
	"testing"
	"time"

	tea "github.com/Nathandela/swarm/internal/thirdparty/bubbletea"
	"github.com/Nathandela/swarm/internal/protocol"
)

func startupResults(m rootModel) <-chan tea.Msg {
	results := make(chan tea.Msg, 16)
	for _, cmd := range m.Init()().(tea.BatchMsg) {
		go func(c tea.Cmd) { results <- c() }(cmd)
	}
	return results
}

func TestStartup_LateRosterArrivesWithoutStatusEvents(t *testing.T) {
	row := protocol.SessionView{ID: "endpoint/task", Name: "Still running"}
	c := &reconnectTestClient{fakeClient: newFakeClient(row), listStarted: make(chan struct{}), listRelease: make(chan struct{})}
	m := New(c, nil).(rootModel)
	defer func() { _ = m.Close(); close(c.events) }()
	results := startupResults(m)
	close(c.listRelease)
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case msg := <-results:
			next, _ := m.Update(msg)
			m = next.(rootModel)
			if got, ok := m.general.sessionByID(row.ID); ok && got.Name == row.Name {
				return
			}
		case <-deadline.C:
			t.Fatal("the late initial roster was discarded; no status event should be required to display existing agents")
		}
	}
}

func TestStartup_FailedInitialReadStartsRecovery(t *testing.T) {
	c := &reconnectTestClient{fakeClient: newFakeClient()}
	c.listErr = errors.New("initial roster unavailable")
	m := New(c, nil, WithDaemonReconnector(func() (Client, error) { return newFakeClient(), nil })).(rootModel)
	defer func() { _ = m.Close(); close(c.events) }()
	results := startupResults(m)
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case msg := <-results:
			next, _ := m.Update(msg)
			m = next.(rootModel)
			if m.connectionLost && m.reconnecting {
				return
			}
		case <-deadline.C:
			t.Fatal("failed initial List left a connected, empty board without recovery")
		}
	}
}

func TestReconnect_SlowRosterIsAccepted(t *testing.T) {
	row := protocol.SessionView{ID: "endpoint/task", Name: "Recovered"}
	c := &reconnectTestClient{fakeClient: newFakeClient(row), listStarted: make(chan struct{}), listRelease: make(chan struct{})}
	lifetime := newClientLifetime(newFakeClient())
	defer func() { _ = (rootModel{connections: lifetime}).Close(); close(c.events) }()
	result := make(chan daemonReconnectedMsg, 1)
	go func() { result <- prepareDaemonConnection(lifetime, 1, c, nil) }()
	<-c.listStarted
	time.Sleep(listDialTimeout + 100*time.Millisecond)
	close(c.listRelease)
	got := <-result
	if got.err != nil || len(got.sessions) != 1 || got.sessions[0].ID != row.ID {
		t.Fatalf("a slow successful reconnect roster was rejected: %v", got.err)
	}
}

func TestStartup_LateRosterCannotReplaceRecoveredConnection(t *testing.T) {
	row := protocol.SessionView{ID: "endpoint/task", Name: "Obsolete"}
	old := &reconnectTestClient{fakeClient: newFakeClient(row), listStarted: make(chan struct{}), listRelease: make(chan struct{})}
	fresh := newFakeClient(protocol.SessionView{ID: row.ID, Name: "Fresh"})
	m := New(old, nil, WithDaemonReconnector(func() (Client, error) { return fresh, nil })).(rootModel)
	defer func() { _ = m.Close(); close(old.events); close(fresh.events) }()
	results := startupResults(m)
	next, _ := m.Update(connectionLostMsg{from: m.events})
	m = next.(rootModel)
	next, _ = m.applyDaemonReconnected(daemonReconnectedMsg{generation: m.reconnectGeneration, client: fresh, events: fresh.events, sessions: fresh.sessions})
	m = next.(rootModel)
	close(old.listRelease)
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case msg := <-results:
			if _, ok := msg.(rosterLoadedMsg); !ok {
				continue
			}
			next, _ := m.Update(msg)
			m = next.(rootModel)
			got, ok := m.general.sessionByID(row.ID)
			if !ok || got.Name != "Fresh" || m.client != fresh {
				t.Fatal("late startup read displaced the recovered connection")
			}
			return
		case <-deadline.C:
			t.Fatal("late roster completion never arrived")
		}
	}
}

func TestStartup_CloseCancelsPendingRosterCommand(t *testing.T) {
	c := &reconnectTestClient{fakeClient: newFakeClient(), listStarted: make(chan struct{}), listRelease: make(chan struct{})}
	m := New(c, nil).(rootModel)
	defer func() { close(c.listRelease); close(c.events) }()
	results := startupResults(m)
	_ = m.Close()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case msg := <-results:
			if msg == nil {
				return
			}
		case <-deadline.C:
			t.Fatal("closing the TUI left its initial roster command blocked")
		}
	}
}
