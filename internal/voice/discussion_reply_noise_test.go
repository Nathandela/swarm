package voice

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
)

// settleVMTurns admits each message on a VM-access discussion and completes its native turn without a final reply.
func settleVMTurns(t *testing.T, ids ...string) (*Controller, *Store, ManagerBinding, []Admission) {
	t.Helper()
	c, s, r, b := newControllerFixture(t)
	c.discussion = &DiscussionScope{InstallationID: "install", GuildID: "guild", ThreadID: "thread", VMProjectAccess: true}
	ctx := context.Background()
	var out []Admission
	for i, id := range ids {
		a, err := c.admitDiscussionInput(DiscussionInput{Origin: discussionFixtureOrigin(id), Text: "Request " + id, ExpectReply: true}, "", false)
		if err != nil {
			t.Fatal(err)
		}
		r.status.Turn = protocol.ManagerNativeTurn{State: "idle"}
		r.status.ReadyForInput = true
		if err = c.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		if len(r.sent) != i+1 {
			t.Fatalf("turn %s not sent: %d sends", id, len(r.sent))
		}
		r.status.Turn = protocol.ManagerNativeTurn{State: "completed", NativeInputID: r.sent[i].ManagerAdmission.Nonce, NativeTurnID: id}
		if err = c.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	return c, s, b, out
}

func unverifiedNotices(st State) []Publication {
	var out []Publication
	for _, p := range st.Publications {
		if p.ReplyMonitor && p.Final {
			out = append(out, p)
		}
	}
	return out
}

func TestVMTurnThatPostedAReplyEndsWithoutAnUnverifiedNotice(t *testing.T) {
	_, s, b, as := settleVMTurns(t, "on-it")
	a := as[0]
	if err := s.update(func(st *State) error {
		n, err := nextDiscussionOutput(st, b.ConversationID)
		if err != nil {
			return err
		}
		p := Publication{ID: "progress", ConversationID: b.ConversationID, Admission: a.Sequence, OutputSequence: n, Text: "On it", DiscussionSource: DiscussionOutputSource{AdmissionID: a.Origin.RequestID}}
		putRecord(st, "publications", &st.Publications, key(b.ConversationID, p.ID), p)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(a.ReplyCheckAfter, 0)
	d := DiscussionController{store: s, cfg: Config{ConversationID: b.ConversationID}, now: func() time.Time { return now }, scope: DiscussionScope{VMProjectAccess: true}}
	for range 3 {
		st, _ := s.Snapshot()
		if err := d.checkDiscussionReply(context.Background(), st); err != nil {
			t.Fatal(err)
		}
		now = now.Add(discussionReplyWait)
	}
	st, _ := s.Snapshot()
	if got := unverifiedNotices(st); len(got) != 0 || hasReplyObligation(st) {
		t.Fatalf("notices %+v obligation=%v", got, hasReplyObligation(st))
	}
}

func TestVMUnverifiedNoticeIsPostedOncePerDiscussionWindow(t *testing.T) {
	_, s, b, as := settleVMTurns(t, "first", "second")
	now := time.Unix(max(as[0].ReplyCheckAfter, as[1].ReplyCheckAfter), 0).Add(time.Minute)
	d := DiscussionController{store: s, cfg: Config{ConversationID: b.ConversationID}, now: func() time.Time { return now }, scope: DiscussionScope{VMProjectAccess: true}}
	for range 4 {
		st, _ := s.Snapshot()
		if err := d.checkDiscussionReply(context.Background(), st); err != nil {
			t.Fatal(err)
		}
	}
	st, _ := s.Snapshot()
	got := unverifiedNotices(st)
	if len(got) != 1 {
		t.Fatalf("want one notice, got %d", len(got))
	}
	if strings.Contains(got[0].Text, "person running Swarm") {
		t.Fatalf("notice addresses the operator, not the thread: %q", got[0].Text)
	}
}
