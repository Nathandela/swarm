package discord

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/workermail"
	bolt "go.etcd.io/bbolt"
)

func TestWorkerPublicationReceiptsSurviveCompaction(t *testing.T) {
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	d := TeammateDiscussion{InstallationID: "install", ID: "discussion", Route: Route{ChannelID: "201"}}
	k := teammateOutputKey(d.InstallationID, d.ID, "publication")
	out := teammateOutput{Route: d.Route, Chunks: []Chunk{{State: StateSent, MessageID: "301"}, {State: StateUncertain}}}
	check := func(state string, ids ...string) {
		t.Helper()
		notes := []workermail.NoteStatus{{Publications: []workermail.PublicationStatus{{ID: "publication"}}}}
		if err := s.workerPublicationStatus(d, notes); err != nil {
			t.Fatal(err)
		}
		pub := notes[0].Publications[0]
		if pub.State != state || !reflect.DeepEqual(pub.MessageIDs, ids) {
			t.Fatalf("receipt %+v", pub)
		}
	}
	if err := s.db.Update(func(tx *bolt.Tx) error { return putJSON(tx, teammateOutboxBucket, k, out) }); err != nil {
		t.Fatal(err)
	}
	check(StateUncertain, "301")
	out.Chunks[1] = Chunk{State: StateSent, MessageID: "302"}
	if err := s.db.Update(func(tx *bolt.Tx) error { return teammateCompactOutput(tx, k, d, out) }); err != nil {
		t.Fatal(err)
	}
	check(StateSent, "301", "302")
	if err := s.teammateBind(teammateBinding{TeammateDiscussion: d, Opened: true}); err != nil {
		t.Fatal(err)
	}
	notes := []workermail.NoteStatus{{ID: "note", AttemptID: "attempt", Publications: []workermail.PublicationStatus{{ID: "publication"}}}}
	if err := s.teammatePark(d, 0, notes); err != nil {
		t.Fatal(err)
	}
	status, err := s.teammateStatus(d.InstallationID, func(TeammateDiscussion) (TeammateNativeStatus, error) {
		t.Fatal("status reopened a parked manager")
		return TeammateNativeStatus{}, nil
	})
	if err != nil || len(status.Discussions) != 1 || status.Discussions[0].Native == nil || len(status.Discussions[0].Native.Notes) != 1 {
		t.Fatalf("parked status %+v, %v", status, err)
	}
	p := status.Discussions[0].Native.Notes[0].Publications[0]
	if p.State != StateSent || !reflect.DeepEqual(p.MessageIDs, []string{"301", "302"}) {
		t.Fatalf("parked publication %+v", p)
	}
	d.Route.ChannelID = "202"
	check("")
}

func TestParkedWorkerStatusRetainsBoundedRecentReceipts(t *testing.T) {
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	var last TeammateDiscussion
	for i := 1; i <= 18; i++ {
		id := fmt.Sprintf("%d", 100+i)
		last = TeammateDiscussion{InstallationID: "install", ID: id, Route: Route{ChannelID: id}}
		if err := s.teammateBind(teammateBinding{TeammateDiscussion: last, Opened: true}); err != nil {
			t.Fatal(err)
		}
		if err := s.teammatePark(last, 0, []workermail.NoteStatus{{ID: "note-" + id}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.db.Update(func(tx *bolt.Tx) error {
		recent, err := recentWorkerNotes(tx, "install")
		if err != nil {
			return err
		}
		if len(recent) != 16 {
			t.Fatalf("unexpected recent count %d", len(recent))
		}
		recent[1].At = time.Now().Add(-workermail.ReceiptRetention - time.Hour)
		recent[2].At = time.Now().Add(-GuardTTL - time.Hour)
		return putJSON(tx, teammateMetaBucket, teammateKey("install", "parked-worker-notes"), recent)
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.teammateBind(teammateBinding{TeammateDiscussion: last, Opened: true}); err != nil {
		t.Fatal(err)
	}
	status, err := s.teammateStatus("install", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, d := range status.Discussions {
		got[d.ID]++
	}
	if len(status.Discussions) != 15 || got["118"] != 1 || got["117"] != 0 || got["116"] != 1 || got["101"] != 0 || got["102"] != 0 {
		t.Fatalf("recent status: %v", got)
	}
}

type pendingWorkerTestManager struct {
	TeammateManager
	route  TeammateDiscussion
	synced chan TeammateDiscussion
}

func (m *pendingWorkerTestManager) PendingWorkerDiscussions() (func(TeammateDiscussion) bool, error) {
	return func(d TeammateDiscussion) bool { return d == m.route }, nil
}
func (m *pendingWorkerTestManager) Sync(_ context.Context, d TeammateDiscussion, _ TeammateHistoryReader, _ func(TeammatePublication) error) error {
	m.synced <- d
	return nil
}

func TestParkedWorkerNoteReactivatesConnectorWithoutHumanInput(t *testing.T) {
	c, _ := teammateTestConnector(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	d := TeammateDiscussion{InstallationID: "install", ID: "original", Route: Route{GuildID: "10", ChannelID: "201", ParentID: "20"}}
	store := c.service.Store
	if err := store.teammateBind(teammateBinding{TeammateDiscussion: d, Opened: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.teammatePark(d, 0, nil); err != nil {
		t.Fatal(err)
	}
	rows, err := store.teammateActiveBindings("install")
	if err != nil || len(rows) != 0 {
		t.Fatalf("discussion still active: %v, %v", rows, err)
	}
	m := &pendingWorkerTestManager{route: d, synced: make(chan TeammateDiscussion, 1)}
	c.manager = m
	var tasks sync.WaitGroup
	done := make(chan teammateTaskResult, 1)
	if err := c.schedule(context.Background(), 1, map[string]bool{}, map[string]bool{}, map[string]time.Time{}, map[string]time.Time{}, done, &tasks); err != nil {
		t.Fatal(err)
	}
	tasks.Wait()
	select {
	case got := <-m.synced:
		if got != d {
			t.Fatalf("wrong discussion %+v", got)
		}
	default:
		t.Fatal("parked discussion was never synced")
	}
}
