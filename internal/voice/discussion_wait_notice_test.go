package voice

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
)

// waitNoticeFixture admits reply-tracked requests whose manager turns completed
// and links a live worker to each, as delegated requests look while they run.
func waitNoticeFixture(t *testing.T, mod func(*WorkerAssociation), ids ...string) (*Store, *DiscussionController, *time.Time) {
	t.Helper()
	if len(ids) == 0 {
		ids = []string{"wait"}
	}
	c, s, r, b := newControllerFixture(t)
	c.discussion = &DiscussionScope{InstallationID: "install", GuildID: "guild", ThreadID: "thread"}
	var last Admission
	for i, id := range ids {
		a, err := c.admitDiscussionInput(DiscussionInput{Origin: discussionFixtureOrigin(id), Text: "Run " + id, ExpectReply: true}, "", false)
		if err != nil {
			t.Fatal(err)
		}
		if err = c.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		r.status.Turn = protocol.ManagerNativeTurn{State: "completed", NativeInputID: r.sent[i].ManagerAdmission.Nonce, NativeTurnID: "native-" + id}
		if err = c.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		w := WorkerAssociation{ConversationID: b.ConversationID, Session: "machine/worker-" + id, SessionInstance: "instance-" + id, Parent: "manager", JobID: "job-" + id, AttemptID: "attempt-" + id, Admission: a.Sequence}
		if mod != nil {
			mod(&w)
		}
		if err = s.update(func(st *State) error {
			putRecord(st, "workers", &st.Workers, key(b.ConversationID, key(w.Session, w.SessionInstance)), w)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		last = a
	}
	st, _ := s.Snapshot()
	last = st.Admissions[admissionKey(b.ConversationID, last.Sequence)]
	now := time.Unix(last.ReplyCheckAfter, 0)
	d := &DiscussionController{cfg: Config{ConversationID: b.ConversationID}, store: s, scope: *c.discussion, now: func() time.Time { return now }}
	return s, d, &now
}

func waitNotices(t *testing.T, s *Store) int {
	t.Helper()
	st, _ := s.Snapshot()
	n := 0
	for _, p := range st.Publications {
		if p.ReplyMonitor && p.Text == discussionWorkerWaitNotice {
			n++
		}
	}
	return n
}

func check(t *testing.T, s *Store, d *DiscussionController) {
	t.Helper()
	st, _ := s.Snapshot()
	if err := d.checkDiscussionReply(t.Context(), st); err != nil {
		t.Fatal(err)
	}
}

func TestWaitNoticeWaitsTwoQuietMinutesForLinkedWorker(t *testing.T) {
	s, d, now := waitNoticeFixture(t, nil)
	check(t, s, d) // first observation starts the quiet clock
	if n := waitNotices(t, s); n != 0 {
		t.Fatalf("notice at first check: %d", n)
	}
	*now = now.Add(discussionReplyWait - time.Second)
	check(t, s, d)
	if n := waitNotices(t, s); n != 0 {
		t.Fatalf("notice before two quiet minutes: %d", n)
	}
	*now = now.Add(time.Second)
	check(t, s, d)
	if n := waitNotices(t, s); n != 1 {
		t.Fatalf("want one notice after two quiet minutes, got %d", n)
	}
}

func TestWaitNoticeSentAtMostOncePerOpenStretch(t *testing.T) {
	s, d, now := waitNoticeFixture(t, nil)
	for range 6 {
		check(t, s, d)
		*now = now.Add(discussionReplyWait)
	}
	if n := waitNotices(t, s); n != 1 {
		t.Fatalf("want one routine notice for the open stretch, got %d", n)
	}
}

func TestWaitNoticeBudgetSurvivesStoreReopen(t *testing.T) {
	s, d, now := waitNoticeFixture(t, nil, "first", "second")
	for range 3 {
		check(t, s, d)
		*now = now.Add(time.Minute)
	}
	if n := waitNotices(t, s); n != 1 {
		t.Fatalf("setup: want one notice, got %d", n)
	}
	dir := filepath.Dir(s.db.Path())
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	d.store = reopened
	for range 6 {
		check(t, reopened, d)
		*now = now.Add(time.Minute)
	}
	if n := waitNotices(t, reopened); n != 1 {
		t.Fatalf("restart reopened the notice budget: %d notices", n)
	}
}

func TestWaitNoticeSkippedWhenWorkerEnded(t *testing.T) {
	s, d, now := waitNoticeFixture(t, func(w *WorkerAssociation) { w.Ended = true })
	for range 3 {
		check(t, s, d)
		*now = now.Add(discussionReplyWait)
	}
	if n := waitNotices(t, s); n != 0 {
		t.Fatalf("ended worker produced %d routine notices", n)
	}
}

func TestWaitNoticeOverlappingRequestsShareOneBudget(t *testing.T) {
	s, d, now := waitNoticeFixture(t, nil, "first", "second")
	for range 8 {
		check(t, s, d)
		*now = now.Add(time.Minute)
	}
	if n := waitNotices(t, s); n != 1 {
		t.Fatalf("overlapping requests in one discussion sent %d routine notices", n)
	}
}

func stallNotice(t *testing.T, s *Store) bool {
	t.Helper()
	st, _ := s.Snapshot()
	for _, p := range st.Publications {
		if p.ReplyMonitor && p.Final && p.Text == discussionStallNotice {
			return true
		}
	}
	return false
}

func TestStalledWorkerWithoutNoticeEvidenceGetsFinalNotice(t *testing.T) {
	for name, mod := range map[string]func(*WorkerAssociation){
		"unlinked VM child": func(w *WorkerAssociation) { w.VMChild, w.Admission = true, 0 },
		"provisional":       func(w *WorkerAssociation) { w.SessionInstance = w.Session },
	} {
		s, d, now := waitNoticeFixture(t, mod)
		check(t, s, d)
		*now = now.Add(discussionReplyStall - time.Second)
		check(t, s, d)
		if stallNotice(t, s) || waitNotices(t, s) != 0 {
			t.Fatalf("%s: spoke before the stall deadline", name)
		}
		*now = now.Add(discussionReplyWait) // next scheduled check
		check(t, s, d)
		if !stallNotice(t, s) {
			t.Fatalf("%s: stalled request ended in silence", name)
		}
	}
}
