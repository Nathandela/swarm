package tui

import (
	tea "charm.land/bubbletea/v2"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

type reconnectTestClient struct {
	*fakeClient
	closed      atomic.Int32
	subErr      error
	nilEvents   bool
	listStarted chan struct{}
	listRelease chan struct{}
}

func (c *reconnectTestClient) Close() error { c.closed.Add(1); return nil }
func (c *reconnectTestClient) Subscribe() (<-chan protocol.Event, error) {
	if c.nilEvents {
		return nil, c.subErr
	}
	return c.events, c.subErr
}
func (c *reconnectTestClient) List() ([]protocol.SessionView, error) {
	if c.listStarted != nil {
		close(c.listStarted)
		<-c.listRelease
	}
	return c.fakeClient.List()
}

func disconnectedModel(t *testing.T, c Client, r DaemonReconnector) rootModel {
	t.Helper()
	m := New(c, nil, WithDaemonReconnector(r)).(rootModel)
	next, _ := m.Update(connectionLostMsg{from: m.events})
	return next.(rootModel)
}

func TestReconnect_RetriesPastSixFailures(t *testing.T) {
	m := disconnectedModel(t, newFakeClient(), func() (Client, error) { return nil, errors.New("offline") })
	for range 9 {
		next, cmd := m.applyDaemonReconnected(daemonReconnectedMsg{generation: m.reconnectGeneration, err: errors.New("offline")})
		m = next.(rootModel)
		if !m.reconnecting || cmd == nil {
			t.Fatal("automatic recovery stopped before the daemon returned")
		}
	}
}

func TestReconnect_RequiresRosterAndSubscription(t *testing.T) {
	for _, kind := range []string{"roster", "subscription", "nil events", "dial error"} {
		t.Run(kind, func(t *testing.T) {
			c := &reconnectTestClient{fakeClient: newFakeClient()}
			switch kind {
			case "roster":
				c.listErr = errors.New("roster unavailable")
			case "subscription":
				c.subErr = errors.New("subscription unavailable")
			case "nil events":
				c.nilEvents = true
			}
			m := disconnectedModel(t, newFakeClient(), func() (Client, error) {
				if kind == "dial error" {
					return c, errors.New("dial failed after allocation")
				}
				return c, nil
			})
			_, cmd := m.reconnectDaemon(daemonReconnectTickMsg{generation: m.reconnectGeneration})
			result := cmd().(daemonReconnectedMsg)
			if result.err == nil || result.client != nil {
				t.Fatal("incomplete candidate reported successful recovery")
			}
			if c.closed.Load() != 1 {
				t.Fatalf("failed candidate closed %d times", c.closed.Load())
			}
		})
	}
}

func TestReconnect_DrainsFloodDuringHydration(t *testing.T) {
	c := &reconnectTestClient{fakeClient: newFakeClient(), listStarted: make(chan struct{}), listRelease: make(chan struct{})}
	m := disconnectedModel(t, newFakeClient(), func() (Client, error) { return c, nil })
	_, cmd := m.reconnectDaemon(daemonReconnectTickMsg{generation: m.reconnectGeneration})
	result := make(chan daemonReconnectedMsg, 1)
	go func() { result <- cmd().(daemonReconnectedMsg) }()
	<-c.listStarted
	pushed := make(chan struct{})
	go func() {
		for range 1024 {
			c.events <- protocol.Event{}
		}
		close(pushed)
	}()
	select {
	case <-pushed:
	case <-time.After(200 * time.Millisecond):
		close(c.listRelease)
		t.Fatal("event flood blocked the subscription while List was running")
	}
	close(c.listRelease)
	got := <-result
	close(c.events)
	if got.err != nil {
		t.Fatal(got.err)
	}
}

func TestReconnect_ClosesDisplacedClientAndIgnoresStalePayloads(t *testing.T) {
	old := &reconnectTestClient{fakeClient: newFakeClient()}
	fresh := newFakeClient(protocol.SessionView{ID: "endpoint/kept", Name: "Fresh", Tag: "current"})
	m := disconnectedModel(t, old, func() (Client, error) { return fresh, nil })
	_, dial := m.reconnectDaemon(daemonReconnectTickMsg{generation: m.reconnectGeneration})
	next, _ := m.applyDaemonReconnected(dial().(daemonReconnectedMsg))
	m = next.(rootModel)
	defer close(fresh.events)
	if old.closed.Load() != 1 {
		t.Fatal("replacement did not close the displaced client")
	}
	for _, stale := range []protocol.SessionView{{ID: "endpoint/kept", Name: "Old", Tag: "old"}, {ID: "endpoint/deleted", Name: "Deleted"}} {
		m = send(m, eventMsg{from: m.events, ev: protocol.Event{Session: stale}}).(rootModel)
	}
	kept, _ := m.general.sessionByID("endpoint/kept")
	if kept.Name != "Fresh" || kept.Tag != "current" || len(m.general.sessions) != 1 {
		t.Fatal("unversioned event regressed or resurrected authoritative roster rows")
	}
}

func TestReconnect_OfflineConfirmAndRenameKeepTheirDrafts(t *testing.T) {
	c := newFakeClient(protocol.SessionView{ID: "endpoint/task"})
	m := disconnectedModel(t, c, nil)
	m.general.confirm, m.general.confirmID = true, "endpoint/task"
	next, cmd := m.updateGeneral(keyRune('y'))
	if cmd != nil {
		cmd()
	}
	if len(c.deletedIDs()) != 0 || !next.(rootModel).general.confirm {
		t.Fatal("offline confirmation submitted or consumed")
	}
	m.general.confirm = false
	m.general.editing, m.general.editID = true, "endpoint/task"
	m.general.edit.set("unsent rename")
	next, cmd = m.updateGeneral(keyEnter)
	if cmd != nil {
		cmd()
	}
	if len(c.renamedCalls()) != 0 || !next.(rootModel).general.editing || next.(rootModel).general.edit.text != "unsent rename" {
		t.Fatal("offline rename submitted or lost its draft")
	}
	if m.boardTakesMouse() {
		t.Fatal("offline board accepted mouse attachment")
	}
}

func TestReconnect_CandidateArrivingAfterCloseIsReleased(t *testing.T) {
	c := &reconnectTestClient{fakeClient: newFakeClient()}
	release := make(chan struct{})
	started := make(chan struct{})
	m := disconnectedModel(t, newFakeClient(), func() (Client, error) { close(started); <-release; return c, nil })
	closer, ok := any(m).(interface{ Close() error })
	if !ok {
		t.Fatal("TUI has no shared recovery cleanup")
	}
	_, cmd := m.reconnectDaemon(daemonReconnectTickMsg{generation: m.reconnectGeneration})
	done := make(chan struct{})
	go func() { cmd(); close(done) }()
	<-started
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	close(release)
	<-done
	if c.closed.Load() != 1 {
		t.Fatal("late candidate leaked after TUI shutdown")
	}
}

func TestReconnect_UpgradeSupersedesOlderCandidate(t *testing.T) {
	old, fresh, obsolete := newFakeClient(), newFakeClient(), &reconnectTestClient{fakeClient: newFakeClient()}
	m := disconnectedModel(t, old, func() (Client, error) { return obsolete, nil })
	previousGeneration := m.reconnectGeneration
	next, _ := m.Update(daemonRestartedMsg{client: fresh})
	m = next.(rootModel)
	next, _ = m.applyDaemonReconnected(daemonReconnectedMsg{generation: previousGeneration, client: obsolete, events: obsolete.events, sessions: []protocol.SessionView{}})
	if next.(rootModel).client != fresh || obsolete.closed.Load() != 1 {
		t.Fatal("older reconnect displaced the completed upgrade")
	}
}

func TestReconnect_FailedUpgradeSubscriptionStaysStale(t *testing.T) {
	old := newFakeClient()
	candidate := &reconnectTestClient{fakeClient: newFakeClient(), subErr: errors.New("subscription refused")}
	m := New(old, nil, WithDaemonReconnector(func() (Client, error) { return nil, errors.New("offline") })).(rootModel)
	next, _ := m.Update(daemonRestartedMsg{client: candidate})
	if !next.(rootModel).connectionLost || next.(rootModel).client != old || candidate.closed.Load() != 1 {
		t.Fatal("incomplete upgrade replaced the last known client or reported live data")
	}
}

func TestReconnect_StaleStateVisibleAcrossScreens(t *testing.T) {
	for _, screen := range []screen{screenGeneral, screenLaunch, screenHandoff, screenOptions} {
		m := disconnectedModel(t, newFakeClient(), func() (Client, error) { return nil, errors.New("offline") })
		m.width, m.height, m.screen = 120, 30, screen
		if !strings.Contains(stripANSI(m.View().Content), "daemon connection lost") {
			t.Fatalf("screen %d hides its disconnected state", screen)
		}
	}
}

func TestReconnect_RefreshCannotUndoCompletedRename(t *testing.T) {
	c := newFakeClient(protocol.SessionView{ID: "endpoint/task", Name: "Original"})
	m := New(c, nil).(rootModel)
	m.rosterInvalidations = true
	next, _ := m.refreshRoster()
	m = next.(rootModel)
	stale := rosterLoadedMsg{generation: m.reconnectGeneration, from: m.events, sessions: []protocol.SessionView{{ID: "endpoint/task", Name: "Original"}}}
	m = send(m, renameDoneMsg{id: "endpoint/task", name: "Renamed", generation: m.reconnectGeneration}).(rootModel)
	next, cmd := m.applyRoster(stale)
	got, _ := next.(rootModel).general.sessionByID("endpoint/task")
	if got.Name != "Renamed" || cmd == nil {
		t.Fatal("an earlier List undid the completed rename instead of refreshing")
	}
}

func TestReconnect_HydratedBannerExpires(t *testing.T) {
	old := newFakeClient(sWorking("endpoint/task", "codex", "/tmp", "before reconnect", time.Second))
	question := protocol.SessionView{ID: "endpoint/task", Agent: "codex", Summary: "fresh question", Group: status.GroupNeedsInput, Status: status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionPrompt}}
	fresh := newFakeClient(question)
	model := New(old, nil, WithDaemonReconnector(func() (Client, error) { return fresh, nil }))
	defer func() { _ = model.(interface{ Close() error }).Close() }()
	tm := startTM(t, model)
	waitContains(t, tm, "before reconnect")
	close(old.events)
	waitContains(t, tm, "codex needs input (question)")
	time.Sleep(bannerDuration + 100*time.Millisecond)
	quitTM(t, tm)
	if strings.Contains(finalView(t, tm), "codex needs input (question)") {
		t.Fatal("hydrated attention banner did not expire")
	}
	close(fresh.events)
}

func TestReconnect_OptionsReloadPreservesPolicyDraft(t *testing.T) {
	for _, activeScreen := range []screen{screenOptions, screenAccounts} {
		t.Run(string(rune('0'+activeScreen)), func(t *testing.T) {
			old, fresh := newContextGuardOptionsClient(), newContextGuardOptionsClient()
			m := New(old, nil).(rootModel)
			m.screen = activeScreen
			var load tea.Cmd
			m.options, load = newOptionsModel(groupByRepo, orderByName, old, 1)
			m = send(m, load()).(rootModel)
			m.options.focus = optionsFocusThreshold
			m.options.contextGuard.threshold.set("87")
			m = send(m, connectionLostMsg{from: m.events}).(rootModel)
			m.reconnecting = true
			fresh.settings.Revision = 9
			next, cmd := m.applyDaemonReconnected(daemonReconnectedMsg{generation: m.reconnectGeneration, client: fresh, events: fresh.events, sessions: []protocol.SessionView{}})
			m = next.(rootModel)
			if !m.options.contextGuard.loading {
				t.Fatal("open options did not reload its replacement connection")
			}
			results := make(chan tea.Msg, 16)
			for _, child := range cmd().(tea.BatchMsg) {
				if child != nil {
					go func(c tea.Cmd) { results <- c() }(child)
				}
			}
			defer close(fresh.events)
			timer := time.NewTimer(time.Second)
			defer timer.Stop()
			for {
				select {
				case result := <-results:
					if loaded, ok := result.(contextGuardSettingsLoadedMsg); ok {
						m = send(m, loaded).(rootModel)
						if m.options.contextGuard.revision != 9 || m.options.contextGuard.threshold.text != "87" || m.options.focus != optionsFocusThreshold || m.options.grouping != groupByRepo {
							t.Fatal("reload lost policy draft/focus or retained stale revision")
						}
						return
					}
				case <-timer.C:
					t.Fatal("replacement settings were not fetched")
				}
			}
		})
	}
}

func TestReconnect_AuthoritativeRosterCanRestoreAnAbsentSession(t *testing.T) {
	row := protocol.SessionView{ID: "endpoint/task", Name: "Restored"}
	m := New(newFakeClient(row), nil).(rootModel)
	m.reconcileRoster([]protocol.SessionView{})
	m.reconcileRoster([]protocol.SessionView{row})
	if got, ok := m.general.sessionByID(row.ID); !ok || got.Name != row.Name {
		t.Fatal("authoritative roster suppressed a session restored by the daemon")
	}
}

func TestReconnect_OldMutationRepliesCannotChangeTheRecoveredView(t *testing.T) {
	row := protocol.SessionView{ID: "endpoint/task", Name: "Fresh"}
	m := New(newFakeClient(row), nil).(rootModel)
	m.reconnectGeneration = 2
	for _, result := range []tea.Msg{
		launchResultMsg{generation: 1}, deleteDoneMsg{id: row.ID, generation: 1},
		killDoneMsg{id: row.ID, generation: 1}, renameDoneMsg{id: row.ID, name: "Obsolete", generation: 1},
		tagDoneMsg{id: row.ID, tag: "Obsolete", generation: 1}, handoffDoneMsg{generation: 1},
	} {
		next, cmd := m.Update(result)
		got := next.(rootModel)
		if cmd != nil || got.rosterRevision != 0 || len(got.general.sessions) != 1 || got.general.sessions[0].Name != "Fresh" || got.general.sessions[0].Tag != "" {
			t.Fatalf("old %T changed recovered state", result)
		}
	}
}

func TestReconnect_DuplicateCompletionCannotCloseTheAdoptedClient(t *testing.T) {
	fresh := &reconnectTestClient{fakeClient: newFakeClient()}
	m := disconnectedModel(t, newFakeClient(), func() (Client, error) { return fresh, nil })
	_, dial := m.reconnectDaemon(daemonReconnectTickMsg{generation: m.reconnectGeneration})
	result := dial().(daemonReconnectedMsg)
	next, _ := m.applyDaemonReconnected(result)
	m = next.(rootModel)
	_, _ = m.applyDaemonReconnected(result)
	if fresh.closed.Load() != 0 {
		t.Fatal("duplicate completion closed the adopted client")
	}
	_ = m.Close()
	if fresh.closed.Load() != 1 {
		t.Fatal("adopted client was not closed exactly once on shutdown")
	}
}
