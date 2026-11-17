package voice

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

const fixtureJournalSource = "d21dfd1a-72ed-44b2-9f38-e050e6630546"

type controllerJournalFixture struct {
	resume  protocol.JournalResume
	records <-chan protocol.JournalRecord
	from    uint64
}

func (f *controllerJournalFixture) EndpointID() string { return "machine" }
func (f *controllerJournalFixture) Capabilities() []string {
	return append([]string(nil), controllerJournalCaps...)
}
func (f *controllerJournalFixture) JournalSubscribeFrom(_ context.Context, from uint64) (protocol.JournalResume, <-chan protocol.JournalRecord, error) {
	f.from = from
	return f.resume, f.records, nil
}
func closedControllerJournal(records ...protocol.JournalRecord) <-chan protocol.JournalRecord {
	ch := make(chan protocol.JournalRecord, len(records))
	for _, r := range records {
		ch <- r
	}
	close(ch)
	return ch
}
func TestControllerQualifiedJournalReplayWakesOnlyTrackedTerminalAndCoalescesOthers(t *testing.T) {
	c, s, r, b := newControllerFixture(t)
	trackControllerFixtureWorker(t, s, b)
	if err := c.ReconcileJournal(JournalCursor{Binding: fixtureJournalSource, Sequence: 10}, JournalSnapshot{Metadata: json.RawMessage(`{"history_available":true}`)}); err != nil {
		t.Fatal(err)
	}
	replay := []protocol.JournalRecord{{Cursor: 11, SessionID: "worker", SessionInstance: "instance", Type: "interaction", Item: json.RawMessage(`{"kind":"agent_message","status":"in_progress","text":"token-sentinel"}`)}, {Cursor: 12, SessionID: "worker", SessionInstance: "instance", Type: "group_transition", Group: status.GroupReadyForReview}}
	source := &controllerJournalFixture{resume: protocol.JournalResume{SourceID: fixtureJournalSource, Cursor: 12, Events: replay}, records: closedControllerJournal(protocol.JournalRecord{Cursor: 13, SessionID: "foreign", SessionInstance: "foreign", Type: "group_transition", Group: status.GroupNeedsInput}, protocol.JournalRecord{Cursor: 14, SessionID: "session", SessionInstance: b.SessionInstance, Role: "manager", Type: "group_transition", Group: status.GroupNeedsInput})}
	if err := c.consumeDaemonJournal(context.Background(), source); !errors.Is(err, ErrControllerJournalResync) {
		t.Fatal(err)
	}
	st, _ := s.Snapshot()
	if source.from != 10 || st.Journal[b.ConversationID].Sequence != 14 || len(st.Wakes) != 1 || len(st.Admissions) != 1 || len(r.sent) != 0 {
		t.Fatal("journal replay lost cursor/wake separation or woke self/untracked peers")
	}
	raw, _ := json.Marshal(st)
	if strings.Contains(string(raw), "token-sentinel") {
		t.Fatal("per-token worker history was copied into controller storage")
	}
	for _, wake := range st.Wakes {
		if wake.RecoveryOnly {
			t.Fatal("qualified exact-instance retained transition became guessed recovery")
		}
	}
}
func TestControllerJournalGapUsesAtomicRecoveryWithoutBorrowingCurrentRosterInstance(t *testing.T) {
	c, s, _, b := newControllerFixture(t)
	trackControllerFixtureWorker(t, s, b)
	if err := c.ReconcileJournal(JournalCursor{Binding: fixtureJournalSource, Sequence: 10}, JournalSnapshot{Metadata: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	source := &controllerJournalFixture{resume: protocol.JournalResume{SourceID: fixtureJournalSource, Cursor: 20, HistoryGap: true, Events: []protocol.JournalRecord{{Cursor: 20, SessionID: "worker", Type: "group_transition", Group: status.GroupReadyForReview}}, Roster: []protocol.JournalRecord{{SessionID: "worker", SessionInstance: "new-current-instance"}}}, records: closedControllerJournal()}
	if err := c.consumeDaemonJournal(context.Background(), source); !errors.Is(err, ErrControllerJournalResync) {
		t.Fatal(err)
	}
	st, _ := s.Snapshot()
	if st.Journal[b.ConversationID].Sequence != 20 || len(st.Wakes) != 1 {
		t.Fatal("atomic gap checkpoint was not retained")
	}
	for _, wake := range st.Wakes {
		if !wake.RecoveryOnly {
			t.Fatal("unknown historical instance became ordinary task authority")
		}
	}
	worker := st.Workers[key(b.ConversationID, key("machine/worker", "instance"))]
	if !worker.RecoveryRequired {
		t.Fatal("source gap forgot durable unmanaged task uncertainty")
	}
	if _, ok := st.Workers[key(b.ConversationID, key("machine/worker", "new-current-instance"))]; ok {
		t.Fatal("current roster instance retargeted an old task")
	}
}
func TestControllerLiveJournalGapDoesNotAdvancePastUnexaminedPosition(t *testing.T) {
	c, s, _, b := newControllerFixture(t)
	trackControllerFixtureWorker(t, s, b)
	if err := c.ReconcileJournal(JournalCursor{Binding: fixtureJournalSource, Sequence: 10}, JournalSnapshot{Metadata: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	source := &controllerJournalFixture{resume: protocol.JournalResume{SourceID: fixtureJournalSource, Cursor: 10}, records: closedControllerJournal(protocol.JournalRecord{Cursor: 12, SessionID: "worker", SessionInstance: "instance", Type: "group_transition", Group: status.GroupReadyForReview})}
	if err := c.consumeDaemonJournal(context.Background(), source); !errors.Is(err, ErrControllerJournalResync) {
		t.Fatal(err)
	}
	st, _ := s.Snapshot()
	if st.Journal[b.ConversationID].Sequence != 10 || len(st.Wakes) != 0 {
		t.Fatal("missing source position was declared irrelevant")
	}
}
func TestControllerDedicatedJournalConnectionConsumesVerifiedWireNamespace(t *testing.T) {
	c, s, runtime, b := newControllerFixture(t)
	socket := controllerWireServer(t, "machine", func(req protocol.Control) protocol.Control {
		return protocol.Control{Op: protocol.OpJournalSubscribeFrom, EndpointID: "machine", Cursor: 0, JournalSourceID: fixtureJournalSource}
	})
	if err := c.FollowDaemonJournal(context.Background(), socket); !errors.Is(err, ErrControllerJournalResync) {
		t.Fatal(err)
	}
	st, _ := s.Snapshot()
	if st.Journal[b.ConversationID].Binding != fixtureJournalSource || len(st.Wakes) != 0 {
		t.Fatal("actual source-aware protocol consumer did not preserve namespace")
	}
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runtime.sent) != 0 {
		t.Fatal("empty initial snapshot caused a polling-only model call")
	}
}
