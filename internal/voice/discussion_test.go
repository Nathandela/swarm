package voice

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func TestDiscussionAdmissionKeepsAuthorAndDeduplicates(t *testing.T) {
	c, _, runtime, _ := newControllerFixture(t)
	c.discussion = &DiscussionScope{InstallationID: "install", GuildID: "guild", ThreadID: "thread"}
	origin := schema.MessageOrigin{Kind: "external", InstallationID: "install", GuildID: "guild", ChannelID: "thread", RequestID: "op1", MessageID: "msg1", ActorID: "member1", RequesterID: "invoker"}
	first, err := c.submitDiscussion(origin, "check the build", "", "")
	if err != nil {
		t.Fatal(err)
	}
	repeat, err := c.submitDiscussion(origin, "check the build", "", "")
	if err != nil || first.Sequence != repeat.Sequence {
		t.Fatalf("repeat %+v: %v", repeat, err)
	}
	origin.ActorID = "member2"
	if _, err = c.submitDiscussion(origin, "check the build", "", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed actor: %v", err)
	}
	if err = c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runtime.sent) != 1 || runtime.sent[0].Origin == nil || runtime.sent[0].Origin.ActorID != "member1" || runtime.sent[0].Origin.RequesterID != "invoker" {
		t.Fatalf("native sends %+v", runtime.sent)
	}
}

type discussionTestRuntime struct {
	*controllerRuntimeFixture
	bootstrapFixture
	retirements []bool
	retirement  protocol.ManagerRetireReceipt
}

func (r *discussionTestRuntime) ManagerRuntime(session, instance string) (protocol.ManagerRuntimeStatus, error) {
	return r.controllerRuntimeFixture.ManagerRuntime(session, instance)
}

func (r *discussionTestRuntime) ManagerRetire(_ context.Context, req protocol.ManagerRetireReq, statusOnly bool) (protocol.ManagerRetireReceipt, error) {
	r.retirements = append(r.retirements, statusOnly)
	r.retirement.OperationID = protocol.ManagerRetireOperationID(req)
	return r.retirement, nil
}

func TestDiscussionRetirementWaitsForGraceAndExactExit(t *testing.T) {
	ctx := context.Background()
	c, s, runtime, binding := newControllerFixture(t)
	now := time.Date(2026, time.November, 19, 12, 0, 0, 0, time.UTC)
	db := &discussionTestRuntime{controllerRuntimeFixture: runtime}
	d := &DiscussionController{controller: c, store: s, runtime: db, now: func() time.Time { return now }}
	if err := s.ReconcileJournal(binding, JournalCursor{Binding: "daemon", Sequence: 9}, JournalSnapshot{Metadata: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	d.journalReady.Store(true)
	if err := d.maybeRetire(ctx); err != nil {
		t.Fatal(err)
	}
	now = now.Add(DiscussionIdleGrace - time.Second)
	if err := d.maybeRetire(ctx); err != nil || len(db.retirements) != 0 {
		t.Fatalf("early retirement: %v %v", db.retirements, err)
	}
	now = now.Add(time.Second)
	db.retirement.State = "uncertain"
	if err := d.maybeRetire(ctx); !errors.Is(err, ErrControllerUncertain) || len(db.retirements) != 1 || db.retirements[0] {
		t.Fatalf("first retirement: %v %v", db.retirements, err)
	}
	st, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	conv := st.Conversations[binding.ConversationID]
	if conv.Retirement == nil || conv.Parked || conv.Retirement.ExpectedTurn.State != "idle" {
		t.Fatalf("expected persisted retiring state: %+v", conv)
	}
	db.retirement.State = "retired"
	if err := d.finishRetirement(ctx, *conv.Retirement, true); !errors.Is(err, ErrControllerUncertain) {
		t.Fatalf("missing process counted as exact exit: %v", err)
	}
	db.rows = []protocol.SessionView{{ID: binding.SessionID, Role: "manager", ManagerConversationID: binding.ConversationID,
		Status: status.Status{Process: status.ProcessExited}}}
	if err := d.finishRetirement(ctx, *conv.Retirement, true); err != nil {
		t.Fatal(err)
	}
	st, _ = s.Snapshot()
	if !st.Conversations[binding.ConversationID].Parked {
		t.Fatal("corroborated retirement did not park")
	}
}

func TestDiscussionRejectsUntrackedWorkersAndNativeApprovalTools(t *testing.T) {
	c, _, _, binding := provenControllerFixture(t)
	c.discussion = &DiscussionScope{InstallationID: "install", GuildID: "guild", ThreadID: "thread"}
	c.workers = &controllerWorkerFixture{}
	if _, err := c.workerTarget(SessionInput{Session: "machine/created", SessionInstance: "created-instance"}, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unrelated worker target: %v", err)
	}
	page, err := c.workerPage(ListInput{})
	if err != nil || string(page) != `{"workers":[]}` {
		t.Fatalf("unrelated worker roster: %s %v", page, err)
	}
	if _, err := c.ManagerCall(context.Background(), ManagerToolCall{Name: "swarm_current_request", Arguments: json.RawMessage(`{"session":"machine/created","session_instance":"created-instance"}`)}); !errors.Is(err, ErrControllerUnsupported) {
		t.Fatalf("manager %s could access native request: %v", binding.SessionID, err)
	}
}

func TestDiscussionQuestionExactChoiceRequiresOnePendingQuestion(t *testing.T) {
	now := time.Now()
	question := Request{ID: "q", Hash: "proof", Kind: "manager", State: "pending", Exported: true, ExpiresAt: now.Add(time.Hour), Payload: json.RawMessage(`{"question":"Which project?", "choices":["Service","Web"]}`)}
	st := State{Requests: map[string]Request{"q": question}}
	if id, hash := matchingDiscussionQuestion(st, " Service ", now); id != "q" || hash != "proof" {
		t.Fatalf("exact choice did not match: %q %q", id, hash)
	}
	if id, _ := matchingDiscussionQuestion(st, "I asked Service to run it", now); id != "" {
		t.Fatalf("ordinary turn matched question %q", id)
	}
	question.ID = "second"
	st.Requests["second"] = question
	if id, _ := matchingDiscussionQuestion(st, "Service", now); id != "" {
		t.Fatalf("competing question matched %q", id)
	}
}

func TestDiscussionBootstrapAllowsSlowObservationWithinCallerDeadline(t *testing.T) {
	cfg := testConfig()
	if managerObservationWindow(cfg) != 5*time.Second {
		t.Fatal("private voice observation window changed")
	}
	cfg.ConversationID = DiscussionConversationPrefix + "test"
	if managerObservationWindow(cfg) < 45*time.Second {
		t.Fatal("discussion proof timed out during measured cold startup")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	refs := ManagerBridgeRefs{Socket: "/private/bridge.sock", AuthFile: "/private/bridge.key"}
	native := bootstrapNativeFixture(cfg, refs)
	if _, err := findBootstrapManager(ctx, cfg, refs, "endpoint", "endpoint/manager", nil, native); !errors.Is(err, context.Canceled) {
		t.Fatalf("caller cancellation lost: %v", err)
	}
}

func TestDiscussionJournalGapsWithoutWorkersNeverWakeOrDowngradeHuman(t *testing.T) {
	c, s, _, binding := newControllerFixture(t)
	c.discussion = &DiscussionScope{InstallationID: "install", GuildID: "guild", ThreadID: "thread"}
	origin := schema.MessageOrigin{Kind: "external", InstallationID: "install", GuildID: "guild", ChannelID: "thread", RequestID: "op", MessageID: "msg", ActorID: "member", RequesterID: "member"}
	admission, err := c.submitDiscussion(origin, "check the build", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, cursor := range []uint64{23, 30, 50} {
		source := &controllerJournalFixture{
			resume:  protocol.JournalResume{SourceID: fixtureJournalSource, Cursor: cursor, FullResync: true, HistoryGap: true},
			records: closedControllerJournal(),
		}
		if err := c.consumeDaemonJournal(context.Background(), source); !errors.Is(err, ErrControllerJournalResync) {
			t.Fatal(err)
		}
	}
	st, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Wakes) != 0 || st.Admissions[admissionKey(binding.ConversationID, admission.Sequence)].RecoveryOnly {
		t.Fatalf("unrelated journal resync created %d recovery wakes or revoked the verified Discord admission", len(st.Wakes))
	}
	if st.Journal[binding.ConversationID].Sequence != 50 {
		t.Fatal("did not keep durable journal boundary")
	}
}

func discussionFixtureOrigin(id string) schema.MessageOrigin {
	return schema.MessageOrigin{Kind: "external", InstallationID: "install", GuildID: "guild", ChannelID: "thread", RequestID: id, MessageID: id, ActorID: "member", RequesterID: "member"}
}

func TestDiscussionRevalidatesQueuedHumanBeforeNativeSend(t *testing.T) {
	c, s, runtime, binding := newControllerFixture(t)
	c.discussion = &DiscussionScope{InstallationID: "install", GuildID: "guild", ThreadID: "thread",
		VerifyOrigin: func(_ context.Context, origin schema.MessageOrigin) error {
			if origin.RequestID == "changed" {
				return ErrDiscussionSourceChanged
			}
			return nil
		}}
	changed, err := c.submitDiscussionInProject(discussionFixtureOrigin("changed"), "switch project", "", "", "", "/withdrawn")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.submitDiscussion(discussionFixtureOrigin("fresh"), "new request", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, _ := s.Snapshot()
	if len(runtime.sent) != 1 || runtime.sent[0].Origin.RequestID != "fresh" ||
		st.Admissions[admissionKey(binding.ConversationID, changed.Sequence)].Phase != "rejected" || st.Conversations[binding.ConversationID].Project != "" {
		t.Fatalf("changed source took effect: %+v %+v", runtime.sent, st.Admissions)
	}
}

func TestDiscussionObserverRebindPreservesExactExecutingInputAndReconstructionKeepsQueue(t *testing.T) {
	c, s, runtime, prior := newControllerFixture(t)
	c.discussion = &DiscussionScope{InstallationID: "install", GuildID: "guild", ThreadID: "thread"}
	for _, id := range []string{"sent", "queued"} {
		if _, err := c.submitDiscussion(discussionFixtureOrigin(id), id, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, _ := s.Snapshot()
	first := st.Admissions[admissionKey(prior.ConversationID, 1)]
	if first.OperationID == "" || len(runtime.sent) != 1 {
		t.Fatal("missing original transport receipt")
	}
	d := &DiscussionController{store: s, now: time.Now}
	next := prior
	next.ObservationGeneration = "rejoined-observer"
	rejoined, err := d.recoverBinding(prior, next, protocol.ManagerNativeTurn{State: "idle"}, false)
	if err != nil {
		t.Fatal(err)
	}
	st, _ = s.Snapshot()
	if st.Admissions[admissionKey(prior.ConversationID, 1)].OperationID != first.OperationID ||
		!st.Admissions[admissionKey(prior.ConversationID, 1)].SettledAt.IsZero() || len(st.Publications) != 0 {
		t.Fatal("observer rejoin retired/replayed the in-flight operation or disclosed a nonexistent continuity gap")
	}
	replacement := rejoined
	replacement.SessionID, replacement.SessionInstance, replacement.NativeSessionID = "machine/rebuilt", "replacement", "native-rebuilt"
	if _, err := d.recoverBinding(rejoined, replacement, protocol.ManagerNativeTurn{State: "idle"}, true); err != nil {
		t.Fatal(err)
	}
	st, _ = s.Snapshot()
	old := st.Admissions[admissionKey(prior.ConversationID, 1)]
	queued := st.Admissions[admissionKey(prior.ConversationID, 2)]
	if old.SettledAt.IsZero() || old.Phase != "uncertain" || old.OperationID != first.OperationID ||
		!queued.SettledAt.IsZero() || queued.Phase != "" || len(st.Publications) != 1 || len(runtime.sent) != 1 {
		t.Fatalf("continuity recovery changed old effect or lost new input: %+v / %+v", old, queued)
	}
}

func TestDiscussionWakePublicationsRetainOrderAndAcknowledgedRetries(t *testing.T) {
	s, original, _ := testStore(t)
	original.ConversationID = DiscussionConversationPrefix + "wake"
	binding, err := s.BindManager(original)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.update(func(st *State) error {
		a, err := allocateAdmission(st, binding, "wake:resumed-wake")
		if err != nil {
			return err
		}
		e := Event{ID: "worker-event", JobID: "job", AttemptID: "attempt"}
		putRecord(st, "events", &st.Events, key(binding.ConversationID, e.ID), e)
		w := Wake{ID: "resumed-wake", EventID: e.ID, Admission: a.Sequence}
		putRecord(st, "wakes", &st.Wakes, key(binding.ConversationID, w.ID), w)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.BindNativeInput(binding, 1, "worker-wake-input"); err != nil {
		t.Fatal(err)
	}
	permit, err := s.GrantSpeech(binding, 1, "worker-wake-input", "wake-turn", 1)
	if err != nil {
		t.Fatal(err)
	}
	first := Publication{Admission: 1, NativeTurnID: "wake-turn", ID: "z-first", Sequence: 1, Text: "I checked the worker result"}
	final := Publication{Admission: 1, NativeTurnID: "wake-turn", ID: "a-final", Sequence: 2, Text: "The integration test passed", Final: true}
	for _, p := range []Publication{first, final} {
		if _, err := s.PublishSpeech(binding, permit, p); err != nil {
			t.Fatal(err)
		}
	}
	d := &DiscussionController{store: s, cfg: Config{ConversationID: binding.ConversationID}, now: time.Now}
	st, _ := s.Snapshot()
	var received []DiscussionOutput
	emit := func(output DiscussionOutput) error {
		received = append(received, output)
		return nil
	}
	if err := d.export(st, emit); err != nil {
		t.Fatal(err)
	}
	if len(received) != 2 || received[0].ID != first.ID || received[1].ID != final.ID ||
		received[0].Sequence >= received[1].Sequence || received[1].JobID != "job" || received[1].AttemptID != "attempt" ||
		received[1].AdmissionID != "resumed-wake" {
		t.Fatalf("wrong durable wake outputs: %+v", received)
	}
	if p, err := s.PublishSpeech(binding, permit, final); err != nil || !p.Exported {
		t.Fatalf("receipt export broke exact native retry: %+v %v", p, err)
	}
	after, _ := s.Snapshot()
	if err := d.export(after, emit); err != nil || len(received) != 2 {
		t.Fatalf("acknowledged outputs sent again: %+v %v", received, err)
	}
}
