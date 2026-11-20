package voice

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/managerproof"
)

func TestDiscussionManualResumeRequiresExactReadyManagerAndCounter(t *testing.T) {
	c, store, runtime, b := newControllerFixture(t)
	d := &DiscussionController{controller: c, store: store, cfg: Config{ConversationID: b.ConversationID}}
	runtime.status.Turn.ExternalInput = 7
	if err := store.observeController(b, 7, true); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		session, instance string
		counter           uint64
	}{
		{"machine/other", b.SessionInstance, 7},
		{b.SessionID, "replacement", 7},
		{b.SessionID, b.SessionInstance, 6},
	} {
		if err := d.ResumeManual(context.Background(), tc.session, tc.instance, tc.counter); !errors.Is(err, ErrStale) {
			t.Fatalf("manual grant accepted stale coordinates: %+v %v", tc, err)
		}
	}
	st, _ := store.Snapshot()
	if !st.Conversations[b.ConversationID].SchedulerPaused {
		t.Fatal("failed recovery unpaused the scheduler")
	}
	if err := d.ResumeManual(context.Background(), b.SessionID, b.SessionInstance, 7); err != nil {
		t.Fatal(err)
	}
	st, _ = store.Snapshot()
	if st.Conversations[b.ConversationID].SchedulerPaused || len(runtime.sent) != 0 {
		t.Fatal("manual resume failed to release scheduling or injected an unadmitted turn")
	}
}

func TestDiscussionManualResumeKeepsQuarantinedManagerFenced(t *testing.T) {
	c, store, runtime, b := newControllerFixture(t)
	d := &DiscussionController{controller: c, store: store, cfg: Config{ConversationID: b.ConversationID}}
	if err := store.noteControllerCustodyLost(b); err != nil {
		t.Fatal(err)
	}
	err := d.ResumeManual(context.Background(), b.SessionID, b.SessionInstance, 0)
	if !errors.Is(err, managerproof.ErrQuarantined) || !strings.Contains(err.Error(), b.SessionID) || !strings.Contains(err.Error(), "verifies its native writers stopped") || !strings.Contains(err.Error(), "preserve any unfinished draft") {
		t.Fatalf("quarantine lacked exact actionable recovery: %v", err)
	}
	st, _ := store.Snapshot()
	if !st.Conversations[b.ConversationID].CustodyLost || len(runtime.sent) != 0 {
		t.Fatal("manual resume bypassed quarantined custody")
	}
}
