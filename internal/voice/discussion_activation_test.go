package voice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
)

func TestPendingWakeReportsParkedManagerActivationRefusal(t *testing.T) {
	c, s, rt, b := newControllerFixture(t)
	cfg := testConfig()
	cfg.ConversationID = b.ConversationID
	dir := privateConfigDir(t)
	refused := protocol.ErrManagerLaunchPrerequisite
	runtime := &discussionTestRuntime{controllerRuntimeFixture: rt, bootstrapFixture: bootstrapFixture{refusal: refused}}
	d := &DiscussionController{cfg: cfg, store: s, controller: c, runtime: runtime, dir: dir, endpoint: "endpoint", bridge: &privateHTTP{socket: dir + "/bridge.sock"}, now: time.Now}
	if err := s.update(func(st *State) error {
		conv := st.Conversations[b.ConversationID]
		conv.Parked = true
		conv.Retirement = &protocol.ManagerRetireReq{Binding: runtimeBinding(b), Generation: b.Generation, Checkpoint: hashBytes([]byte("test-checkpoint"))}
		putRecord(st, "conversations", &st.Conversations, b.ConversationID, conv)
		a, err := allocateAdmission(st, b, "wake:verified-job")
		if err != nil {
			return err
		}
		putRecord(st, "admissions", &st.Admissions, admissionKey(b.ConversationID, a.Sequence), a)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	err := d.Sync(context.Background(), func(DiscussionOutput) error { return nil })
	if !errors.Is(err, ErrDiscussionActivationUnavailable) || !errors.Is(err, refused) {
		t.Fatalf("parked wake: %v", err)
	}
}

func TestPendingDiscussionReportsSupervisorSocketOutage(t *testing.T) {
	ctx := t.Context()
	c, s, _, b := newControllerFixture(t)
	cfg := testConfig()
	cfg.ConversationID = b.ConversationID
	dir := privateConfigDir(t)
	client, err := NewProtocolControllerClient(ctx, dir+"/absent.sock", "endpoint")
	if err != nil {
		t.Fatal(err)
	}
	if err = discussionActivationError(client.discussionSupported(ctx)); !errors.Is(err, ErrDiscussionActivationUnavailable) || !errors.Is(err, errControllerSupervisorOffline) {
		t.Fatalf("missing cold-start notice: %v", err)
	}
	c.runtime = client
	d := &DiscussionController{cfg: cfg, store: s, controller: c, runtime: client, dir: dir, endpoint: "endpoint", now: time.Now}
	if err = d.Sync(ctx, nil); errors.Is(err, ErrDiscussionActivationUnavailable) {
		t.Fatalf("idle outage requested owner notice: %v", err)
	}
	if err = s.update(func(st *State) error {
		a, err := allocateAdmission(st, b, "wake:existing-worker")
		if err != nil {
			return err
		}
		putRecord(st, "admissions", &st.Admissions, admissionKey(b.ConversationID, a.Sequence), a)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	err = d.Sync(ctx, nil)
	if !errors.Is(err, ErrDiscussionActivationUnavailable) || !errors.Is(err, errControllerSupervisorOffline) {
		t.Fatalf("pending worker completion had no actionable outage: %v", err)
	}
}
