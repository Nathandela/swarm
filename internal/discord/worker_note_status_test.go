package discord

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/workermail"
	bolt "go.etcd.io/bbolt"
)

type parkedStatusManager struct {
	*teammateTestManager
	view TeammateNativeStatus
}

func (*parkedStatusManager) Dormant(TeammateDiscussion) (bool, error) { return true, nil }
func (m *parkedStatusManager) Status(TeammateDiscussion) (TeammateNativeStatus, error) {
	return m.view, nil
}

func TestWorkerPublicationReceiptsSurviveCompaction(t *testing.T) {
	c, manager := teammateTestConnector(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	c.service.Config.ProjectAccess = ProjectAccessEnrolled
	s := c.service.Store
	d := TeammateDiscussion{InstallationID: "install", ID: "discussion", Route: Route{ChannelID: "201"}}
	k := teammateOutputKey(d.InstallationID, d.ID, "publication")
	out := teammateOutput{Route: d.Route, Chunks: []Chunk{{State: StateSent, MessageID: "301"}, {State: StateUncertain}}}
	check := func(state string, ids ...string) {
		t.Helper()
		notes := []workermail.NoteStatus{{Publications: []workermail.PublicationStatus{{ID: "publication"}}}}
		if err := s.workerPublicationStatus(d, &TeammateNativeStatus{Notes: notes}); err != nil {
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
	c.manager = &parkedStatusManager{teammateTestManager: manager, view: TeammateNativeStatus{State: "parked", Notes: notes, Jobs: []TeammateJobStatus{{State: "turn_completed", RecoveryRequired: true, NativeTurnPublication: &workermail.PublicationStatus{ID: "publication"}}}}}
	var tasks sync.WaitGroup
	done := make(chan teammateTaskResult, 1)
	if err := c.schedule(t.Context(), 1, map[string]bool{}, map[string]bool{}, map[string]time.Time{}, map[string]time.Time{}, done, &tasks); err != nil {
		t.Fatal(err)
	}
	tasks.Wait()
	if result := <-done; result.retry {
		t.Fatal("enrolled discussion could not park or retain its worker result")
	}
	status, err := s.teammateStatus(d.InstallationID, func(TeammateDiscussion) (TeammateNativeStatus, error) {
		t.Fatal("status reopened a parked manager")
		return TeammateNativeStatus{}, nil
	})
	if err != nil || len(status.Discussions) != 1 || status.Discussions[0].Native == nil || len(status.Discussions[0].Native.Notes) != 1 {
		t.Fatalf("parked status %+v, %v", status, err)
	}
	jobs := status.Discussions[0].Native.Jobs
	if len(jobs) != 1 || jobs[0].NativeTurnPublication.State != StateSent || !reflect.DeepEqual(jobs[0].NativeTurnPublication.MessageIDs, []string{"301", "302"}) {
		t.Fatalf("parked job result: %+v", jobs)
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
		if err := s.teammatePark(last, 0, &TeammateNativeStatus{State: "parked", ManagerSession: id, RetirementOperation: "retire-" + id}); err != nil {
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
		if d.ID == "116" && (d.Native == nil || d.Native.RetirementOperation != "retire-116" || d.Native.ManagerSession != "116") {
			t.Fatalf("missing outgoing retirement proof %+v", d.Native)
		}
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

func TestAnchoredDeliveryRequiresExactPublicationAndEveryChunk(t *testing.T) {
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	d := TeammateDiscussion{InstallationID: "I", ID: "D", Route: Route{ChannelID: "201"}}
	p := TeammatePublication{ID: "pub", AdmissionID: "op", Sequence: 7, Body: strings.Repeat("a", 1901)}
	key := teammateOutputKey(d.InstallationID, d.ID, p.ID)
	metadata := p
	metadata.Body = ""
	out := teammateOutput{DiscussionID: d.ID, Route: d.Route, BodyHash: PublicationBodyHash(p.Body), Publication: metadata, Chunks: []Chunk{{State: StateSent, MessageID: "301", GatewaySeen: true}, {State: StateUncertain}}}
	put := func() {
		t.Helper()
		if err := s.db.Update(func(tx *bolt.Tx) error { return putJSON(tx, teammateOutboxBucket, key, out) }); err != nil {
			t.Fatal(err)
		}
	}
	check := func(q TeammatePublication, want []string, wantErr error) {
		t.Helper()
		ids, err := s.confirmedPublication(d, q)
		if !reflect.DeepEqual(ids, want) || !errors.Is(err, wantErr) {
			t.Fatalf("delivery %v %v, wanted %v %v", ids, err, want, wantErr)
		}
	}
	put()
	check(p, nil, nil)
	out.Chunks[1] = Chunk{State: StateSent, MessageID: "302", RESTAcknowledged: true}
	put()
	for i := 0; i < 2; i++ {
		check(p, []string{"301", "302"}, nil)
		wrong := p
		wrong.Body = "edited"
		check(wrong, nil, ErrConflict)
		wrong = p
		wrong.AdmissionID = "other"
		check(wrong, nil, ErrConflict)
		if i == 0 {
			if err := s.db.Update(func(tx *bolt.Tx) error { return teammateCompactOutput(tx, key, d, out) }); err != nil {
				t.Fatal(err)
			}
		}
	}
	d.Route.ChannelID = "other"
	check(p, nil, nil)
}

func TestLateReplyReceiptUpdatesActiveAndParkedStatus(t *testing.T) {
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	d := TeammateDiscussion{InstallationID: "I", ID: "D", Route: Route{ChannelID: "201"}}
	if err := s.teammateBind(teammateBinding{TeammateDiscussion: d, Opened: true}); err != nil {
		t.Fatal(err)
	}
	p := TeammatePublication{ID: "result", AdmissionID: "ask", Sequence: 1, Body: "answer"}
	proof := p.Proof()
	p.Body = ""
	k := teammateOutputKey(d.InstallationID, d.ID, p.ID)
	out := teammateOutput{DiscussionID: d.ID, Route: d.Route, Publication: p, BodyHash: proof.BodyHash, SettledAt: time.Now(), Chunks: []Chunk{{State: StateSent, MessageID: "301"}}}
	save := func() {
		t.Helper()
		if err := s.db.Update(func(tx *bolt.Tx) error { return putJSON(tx, teammateOutboxBucket, k, out) }); err != nil {
			t.Fatal(err)
		}
	}
	save()
	view := TeammateNativeStatus{State: "parked", UnconfirmedReplies: 2, ReplyReceipts: [][]PublicationProof{{proof}, {proof, {ID: "another"}}}}
	check := func(want int, parked bool) {
		t.Helper()
		v, err := s.teammateStatus(d.InstallationID, func(TeammateDiscussion) (TeammateNativeStatus, error) {
			if parked {
				t.Fatal("parked dispatcher was reopened")
			}
			return view, nil
		})
		if err != nil || len(v.Discussions) != 1 || v.Discussions[0].Native == nil || v.Discussions[0].Native.UnconfirmedReplies != want {
			t.Fatalf("want %d, status %+v err %v", want, v, err)
		}
	}
	check(2, false) // A historical sent label with no acknowledgement bit is insufficient.
	out.Chunks[0].GatewaySeen = true
	save()
	check(1, false)
	out.Chunks[0].GatewaySeen = false
	save()
	if err = s.teammatePark(d, 0, &view); err != nil {
		t.Fatal(err)
	}
	check(2, true)
	out.Chunks[0].RESTAcknowledged = true
	save()
	check(1, true)
	if err = s.db.Update(func(tx *bolt.Tx) error { return teammateCompactOutput(tx, k, d, out) }); err != nil {
		t.Fatal(err)
	}
	check(1, true) // Legacy compacted receipts retain route, IDs and both immutable hashes.
	out.Chunks[0].RESTAcknowledged = false
	if err = s.db.Update(func(tx *bolt.Tx) error { return teammateCompactOutput(tx, k, d, out) }); err != nil {
		t.Fatal(err)
	}
	check(2, true) // Compaction cannot promote a bare historical sent label into proof.
	if err = s.db.View(func(tx *bolt.Tx) error {
		var legacy struct{ DiscordMessageIDs []string }
		if err := teammateGet(tx.Bucket([]byte(teammateReceiptBucket)), k, &legacy); err != nil {
			return err
		}
		if len(legacy.DiscordMessageIDs) != 0 {
			t.Fatalf("downgrade would promote partial evidence: %+v", legacy)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
