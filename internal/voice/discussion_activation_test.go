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
