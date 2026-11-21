package voice

import (
	"bytes"
	"context"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

type busyRuntime struct {
	*controllerRuntimeFixture
	interrupts []protocol.ManagerInterruptReq
}

func (r *busyRuntime) ManagerInterrupt(_ context.Context, q protocol.ManagerInterruptReq, _ bool) (protocol.ManagerInterruptReceipt, error) {
	r.interrupts = append(r.interrupts, q)
	r.status.Turn.State = "completed"
	r.status.Turn.Reason = "aborted"
	r.status.ReadyForInput = true
	return protocol.ManagerInterruptReceipt{State: "interrupted"}, nil
}

func TestBusyDispatcherDistinctUnansweredMessages(t *testing.T) {
	c, s, rt, b := newControllerFixture(t)
	r := &busyRuntime{controllerRuntimeFixture: rt}
	c.runtime = r
	c.discussion = &DiscussionScope{DiscussionID: "discussion", InstallationID: "install", GuildID: "guild", ThreadID: "thread", VerifyOrigin: func(context.Context, schema.MessageOrigin) error { return nil }}
	submit := func(id, sender string) Admission {
		t.Helper()
		o := discussionFixtureOrigin(id)
		o.DiscussionID, o.Revision, o.ActorID, o.RequesterID = "discussion", "one", sender, sender
		a, err := c.submitDiscussion(o, id, "", "")
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	tick := func() {
		t.Helper()
		if err := c.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	original := submit("original", "member")
	tick()
	r.status.Turn = protocol.ManagerNativeTurn{NativeInputID: r.sent[0].ManagerAdmission.Nonce, NativeTurnID: "busy", State: "running"}
	r.status.ReadyForInput = false
	tick()
	st, _ := s.Snapshot()
	running := st.Admissions[admissionKey(b.ConversationID, original.Sequence)]
	first := submit("first", "member")
	if submit("first", "member").Sequence != first.Sequence {
		t.Fatal("replayed first took another slot")
	}
	tick()
	if len(r.interrupts) != 0 || len(r.sent) != 1 {
		t.Fatal("first ping disrupted the active turn")
	}
	r.proof = protocol.ManagerCallProof{Main: true, NativeInputID: running.NativeInputID, NativeTurnID: "busy", NativeCallID: "authenticated-tool"}
	call := ManagerToolCall{Name: "swarm_discussion_answer", Arguments: []byte("{}")}
	notified, err := c.ManagerCall(t.Context(), call)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := c.ManagerCall(t.Context(), call)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(notified, []byte("dispatcher_notice")) || bytes.Contains(replay, []byte("dispatcher_notice")) {
		t.Fatal("expected one gentle notification")
	}
	submit("other", "other-member")
	tick()
	if len(r.interrupts) != 0 {
		t.Fatal("different member caused escalation")
	}
	second := submit("second", "member")
	submit("second", "member")
	changed := r.status
	changed.Turn.NativeTurnID = "later-turn"
	if err := c.interruptForWaitingMessages(t.Context(), running, changed); err != nil || len(r.interrupts) != 0 {
		t.Fatal("retargeted the interrupt", err)
	}
	check := c.discussion.VerifyOrigin
	c.discussion.VerifyOrigin = func(context.Context, schema.MessageOrigin) error { return ErrDiscussionSourceChanged }
	tick()
	if len(r.interrupts) != 0 {
		t.Fatal("withdrawn ping interrupted")
	}
	c.discussion.VerifyOrigin = check
	tick()
	if len(r.interrupts) != 1 || r.interrupts[0].NativeInputID != running.NativeInputID || r.interrupts[0].NativeTurnID != "busy" {
		t.Fatalf("wrong interrupt: %+v", r.interrupts)
	}
	tick()
	for _, expected := range []string{"first", "other", "second"} {
		last := r.sent[len(r.sent)-1]
		if last.Origin.MessageID != expected {
			t.Fatalf("wanted %s, got %s", expected, last.Origin.MessageID)
		}
		r.status.Turn = protocol.ManagerNativeTurn{NativeInputID: last.ManagerAdmission.Nonce, NativeTurnID: expected, State: "completed"}
		tick()
	}
	tick()
	if len(r.sent) != 4 || len(r.interrupts) != 1 || submit("second", "member").Sequence != second.Sequence {
		t.Fatalf("duplicate dispatch: sends=%d interrupts=%d", len(r.sent), len(r.interrupts))
	}
}

func TestBusyDispatcherReplyBetweenPingsResetsEscalation(t *testing.T) {
	c, store, rt, binding := provenControllerFixture(t)
	r := &busyRuntime{controllerRuntimeFixture: rt}
	c.runtime = r
	c.discussion = &DiscussionScope{DiscussionID: "discussion", InstallationID: "install", GuildID: "guild", ThreadID: "thread", VerifyOrigin: func(context.Context, schema.MessageOrigin) error { return nil }}
	if err := c.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	ping := func(id string) {
		origin := discussionFixtureOrigin(id)
		origin.DiscussionID, origin.Revision = "discussion", "one"
		if _, err := c.submitDiscussion(origin, id, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	ping("before")
	if err := store.update(func(st *State) error {
		_, err := nextDiscussionOutput(st, binding.ConversationID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ping("after")
	if err := c.Tick(t.Context()); err != nil || len(r.interrupts) != 0 {
		t.Fatalf("an answered ping counted twice: %v, %+v", err, r.interrupts)
	}
}

func TestBusyDispatcherWithoutToolCallReadsFirstPingOnNextTurn(t *testing.T) {
	c, _, rt, _ := provenControllerFixture(t)
	r := &busyRuntime{controllerRuntimeFixture: rt}
	c.runtime = r
	c.discussion = &DiscussionScope{DiscussionID: "discussion", InstallationID: "install", GuildID: "guild", ThreadID: "thread", VerifyOrigin: func(context.Context, schema.MessageOrigin) error { return nil }}
	if err := c.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	origin := discussionFixtureOrigin("waiting")
	origin.DiscussionID, origin.Revision = "discussion", "one"
	for range 2 {
		if _, err := c.submitDiscussion(origin, "waiting message", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	rt.status.Turn.State = "completed"
	rt.status.ReadyForInput = true
	for range 2 {
		if err := c.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.interrupts) != 0 || len(r.sent) != 2 || r.sent[1].Origin.MessageID != "waiting" {
		t.Fatalf("expected one natural next-turn delivery: interrupts=%d sends=%d", len(r.interrupts), len(r.sent))
	}
}
