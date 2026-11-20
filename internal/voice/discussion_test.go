package voice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/workermail"

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
	cancelState           string
	retirements           []bool
	retirement            protocol.ManagerRetireReceipt
	managerErr, retireErr error
}

func (r *discussionTestRuntime) ManagerRuntime(session, instance string) (protocol.ManagerRuntimeStatus, error) {
	if r.managerErr != nil {
		return protocol.ManagerRuntimeStatus{}, r.managerErr
	}
	return r.controllerRuntimeFixture.ManagerRuntime(session, instance)
}

func (r *discussionTestRuntime) ManagerRetire(_ context.Context, req protocol.ManagerRetireReq, statusOnly bool) (protocol.ManagerRetireReceipt, error) {
	r.retirements = append(r.retirements, statusOnly)
	r.retirement.OperationID = protocol.ManagerRetireOperationID(req)
	return r.retirement, r.retireErr
}

func (r *discussionTestRuntime) CancelManagerRetire(_ context.Context, req protocol.ManagerRetireReq) (protocol.ManagerRetireReceipt, error) {
	state := r.cancelState
	if state == "" {
		state = "cancelled"
	}
	return protocol.ManagerRetireReceipt{OperationID: protocol.ManagerRetireOperationID(req), State: state}, nil
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
	for range 2 {
		if err := d.finishRetirement(ctx, *conv.Retirement, true); !errors.Is(err, ErrControllerUncertain) {
			t.Fatal(err)
		}
	}
	if len(db.retirements) != 4 || !db.retirements[1] || db.retirements[2] || !db.retirements[3] {
		t.Fatalf("repeated tick re-drove stop before backoff: %v", db.retirements)
	}
	now = now.Add(time.Second)
	if err := d.finishRetirement(ctx, *conv.Retirement, true); !errors.Is(err, ErrControllerUncertain) || len(db.retirements) != 6 || db.retirements[5] {
		t.Fatalf("missing retry after backoff: %v %v", db.retirements, err)
	}
	db.retirement.State = "retired"
	if err := d.finishRetirement(ctx, *conv.Retirement, true); !errors.Is(err, ErrControllerUncertain) {
		t.Fatalf("missing process counted as exact exit: %v", err)
	}
	db.rows = []protocol.SessionView{{ID: binding.SessionID, Role: "manager", Agent: binding.Provider, ManagerConversationID: binding.ConversationID,
		Status: status.Status{Process: status.ProcessExited}}}
	if err := d.finishRetirement(ctx, *conv.Retirement, true); err != nil {
		t.Fatal(err)
	}
	st, _ = s.Snapshot()
	if !st.Conversations[binding.ConversationID].Parked {
		t.Fatal("corroborated retirement did not park")
	}
}

func TestDiscussionNoteStatusCorrelatesIngressAdmissionAndPublication(t *testing.T) {
	dir := t.TempDir()
	at := time.Now().UTC().Add(-2 * time.Hour)
	d := DiscussionController{cfg: Config{ConversationID: "discussion"}, scope: DiscussionScope{VMProjectAccess: true, ManagerSettingsDir: dir}}
	b := workermail.Binding{Discussion: "discussion", Session: "vm/worker", Instance: "inc", Parent: "parent", Attempt: "attempt"}
	if _, err := workermail.Submit(dir, b, "new", "needs-input", "private question", true); err != nil {
		t.Fatal(err)
	}
	if _, err := workermail.Submit(dir, b, "recorded", "complete", "private result", true); err != nil {
		t.Fatal(err)
	}
	meta, err := json.Marshal(workerNoteMetadata{Session: b.Session, Instance: b.Instance, NoteID: "recorded", Created: &at})
	if err != nil {
		t.Fatal(err)
	}
	st := State{Wakes: map[string]Wake{"w": {ID: "w", ConversationID: b.Discussion, State: "settled", EventID: "ev", Admission: 2, WorkerReply: "private answer", WorkerReplyExported: true}},
		Events:       map[string]Event{key(b.Discussion, "ev"): {WorkerMessage: true, AttemptID: b.Attempt, JobID: "job", Metadata: meta}},
		Admissions:   map[string]Admission{admissionKey(b.Discussion, 2): {Phase: "accepted"}},
		Publications: map[string]Publication{"pub": {ID: "pub", ConversationID: b.Discussion, DiscussionSource: DiscussionOutputSource{AdmissionID: "w"}}}}
	result := DiscussionNativeStatus{State: "parked"}
	if err := d.noteStatus(st, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Notes) != 2 || result.Notes[0].ID != "new" || result.Notes[0].State != "queued" || result.OldestPendingNote != result.Notes[0].Created || result.Diagnostic == "" {
		t.Fatalf("pending status: %+v", result)
	}
	n := result.Notes[1]
	if n.ID != "recorded" || n.AttemptID != "attempt" || n.Source != "wake:w" || !n.Answered || n.Admission != 2 || n.AdmissionPhase != "accepted" || len(n.Publications) != 1 || n.Publications[0].ID != "pub" || n.Publications[0].State != "awaiting_delivery_receipt" {
		t.Fatalf("recorded note: %+v", n)
	}
}

func TestDiscussionParksLiveVMWorkerOnlyWithIndependentMailbox(t *testing.T) {
	_, store, _, b := newControllerFixture(t)
	if err := store.ReconcileJournal(b, JournalCursor{Binding: "daemon", Sequence: 9}, JournalSnapshot{Metadata: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	st, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ mailbox, reader, ended, park bool }{
		{true, true, false, true}, {true, false, false, false}, {false, true, false, false}, {false, false, true, true},
	} {
		st.Workers["worker"] = WorkerAssociation{VMChild: true, Ended: tc.ended, Mailbox: tc.mailbox}
		d := DiscussionController{scope: DiscussionScope{VMProjectAccess: true}}
		if tc.reader {
			d.scope.ManagerSettingsDir = t.TempDir()
		}
		if got := d.canPark(st, time.Now()); got != tc.park {
			t.Fatalf("%+v: park=%v", tc, got)
		}
	}
}

func TestDiscussionPendingWakeClearsOnlyAfterDurableRetirementCancellation(t *testing.T) {
	for _, state := range []string{"cancelled", "uncertain"} {
		t.Run(state, func(t *testing.T) {
			c, s, rt, b := newControllerFixture(t)
			db := &discussionTestRuntime{controllerRuntimeFixture: rt, cancelState: state, retirement: protocol.ManagerRetireReceipt{State: "unknown"}}
			d := &DiscussionController{controller: c, store: s, runtime: db, now: time.Now}
			trackControllerFixtureWorker(t, s, b)
			event := Event{ID: "worker", Cursor: JournalCursor{Binding: "journal", Sequence: 1}, Metadata: json.RawMessage(`{"done":true}`)}
			if _, err := c.AcceptWorkerEvent("machine/worker", "instance", event); err != nil {
				t.Fatal(err)
			}
			req := protocol.ManagerRetireReq{Binding: runtimeBinding(b), Generation: 1, Checkpoint: strings.Repeat("a", 64)}
			if err := s.update(func(st *State) error {
				conv := st.Conversations[b.ConversationID]
				conv.Retirement = &req
				putRecord(st, "conversations", &st.Conversations, b.ConversationID, conv)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			err := d.finishRetirement(t.Context(), req, true)
			st, snapErr := s.Snapshot()
			if snapErr != nil {
				t.Fatal(snapErr)
			}
			if (state == "cancelled") != (err == nil) || (state == "cancelled") != (st.Conversations[b.ConversationID].Retirement == nil) ||
				!discussionPending(st) || len(db.retirements) != 1 || !db.retirements[0] {
				t.Fatalf("state=%s pending=%v retirements=%v err=%v", state, discussionPending(st), db.retirements, err)
			}
		})
	}
}

func TestDiscussionUnclaimedRetirementClearsOnlyForExactExitedManager(t *testing.T) {
	for _, tc := range []struct {
		name, receipt string
		process       status.Process
		instance      string
		transport     error
		clear         bool
	}{
		{name: "exact exit", receipt: "unknown", process: status.ProcessExited, clear: true},
		{name: "missing", receipt: "unknown"},
		{name: "lost", receipt: "unknown", process: status.ProcessLost},
		{name: "replacement", receipt: "unknown", process: status.ProcessExited, instance: "another-incarnation"},
		{name: "ambiguous daemon claim", receipt: "uncertain", process: status.ProcessExited},
		{name: "unavailable transport", receipt: "unknown", process: status.ProcessExited, transport: errors.New("lost transport")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, s, runtime, b := newControllerFixture(t)
			db := &discussionTestRuntime{controllerRuntimeFixture: runtime, managerErr: managerproof.ErrUnavailable, retireErr: tc.transport, retirement: protocol.ManagerRetireReceipt{State: tc.receipt}}
			c.runtime = db
			d := &DiscussionController{controller: c, store: s, runtime: db, now: time.Now}
			req := protocol.ManagerRetireReq{Binding: runtimeBinding(b), Generation: 1, Checkpoint: strings.Repeat("a", 64)}
			if err := s.update(func(st *State) error {
				conv := st.Conversations[b.ConversationID]
				conv.Retirement = &req
				putRecord(st, "conversations", &st.Conversations, b.ConversationID, conv)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if tc.process != "" {
				row := protocol.SessionView{ID: b.SessionID, Role: "manager", Agent: b.Provider, ManagerConversationID: b.ConversationID, Status: status.Status{Process: tc.process}}
				if tc.instance != "" {
					row.Capabilities = &schema.SessionCapabilities{SessionInstance: tc.instance}
				}
				db.rows = []protocol.SessionView{row}
			}
			err := d.finishRetirement(context.Background(), req, true)
			st, snapErr := s.Snapshot()
			if snapErr != nil {
				t.Fatal(snapErr)
			}
			conv := st.Conversations[b.ConversationID]
			if (err == nil) != tc.clear || (conv.Retirement == nil) != tc.clear || conv.Parked || db.launches != 0 {
				t.Fatalf("retire=%+v parked=%v launched=%d err=%v", conv.Retirement, conv.Parked, db.launches, err)
			}
		})
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

func TestDiscussionRevalidatesAndReplacesQueuedRevisionInOriginalSlot(t *testing.T) {
	c, s, runtime, binding := newControllerFixture(t)
	scope := &DiscussionScope{DiscussionID: "discussion", InstallationID: "install", GuildID: "guild", ThreadID: "thread",
		VerifyOrigin: func(_ context.Context, origin schema.MessageOrigin) error {
			if origin.Revision == "old" {
				return ErrDiscussionSourceChanged
			}
			return nil
		}}
	c.discussion = scope
	d := &DiscussionController{scope: *scope, controller: c, store: s}
	origin := discussionFixtureOrigin("changed")
	origin.DiscussionID, origin.Revision = "discussion", "old"
	changed, err := c.submitDiscussionInProject(origin, "switch project", "", "", "", "/withdrawn", true)
	if err != nil {
		t.Fatal(err)
	}
	fresh := discussionFixtureOrigin("fresh")
	fresh.DiscussionID, fresh.Revision = "discussion", "one"
	if _, err := c.submitDiscussion(fresh, "new request", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state, err := d.AdmissionState(origin); err != nil || state != "superseded" || len(runtime.sent) != 0 {
		t.Fatalf("stale revision bypassed its reserved queue slot: %q %v sends=%d", state, err, len(runtime.sent))
	}
	origin.Revision = "new"
	revised, err := c.submitDiscussion(origin, "edited request", "", "")
	if err != nil || revised.Sequence != changed.Sequence {
		t.Fatalf("edited revision lost original slot: %+v %v", revised, err)
	}
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, _ := s.Snapshot()
	if len(runtime.sent) != 1 || runtime.sent[0].Origin.Revision != "new" ||
		st.Conversations[binding.ConversationID].Project != "" {
		t.Fatalf("stale source or newer message overtook the edited source: %+v %+v", runtime.sent, st.Admissions)
	}
	if state, err := d.AdmissionState(origin); err != nil || state != "cutover" {
		t.Fatalf("cutoff not persisted: %q %v", state, err)
	}
	origin.Revision = "too-late"
	if _, err := c.submitDiscussion(origin, "again", "", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("post-cutoff edit reran input: %v", err)
	}
}

func TestDiscussionSourceWithdrawalCancelsWithoutNativeSend(t *testing.T) {
	c, s, runtime, _ := newControllerFixture(t)
	scope := &DiscussionScope{InstallationID: "install", GuildID: "guild", ThreadID: "thread", VerifyOrigin: func(context.Context, schema.MessageOrigin) error { return ErrDiscussionSourceChanged }}
	c.discussion = scope
	origin := discussionFixtureOrigin("withdrawn")
	if _, err := c.submitDiscussion(origin, "tag removed", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	d := &DiscussionController{scope: *scope, store: s, controller: c}
	if err := d.CancelAdmission(origin); err != nil {
		t.Fatal(err)
	}
	if state, err := d.AdmissionState(origin); state != "cancelled" || err != nil || len(runtime.sent) != 0 {
		t.Fatalf("state=%q err=%v sends=%d", state, err, len(runtime.sent))
	}
}

func TestDiscussionQueuedProjectSwitchAppliesToNextParticipant(t *testing.T) {
	c, s, runtime, binding := newControllerFixture(t)
	c.discussion = &DiscussionScope{InstallationID: "install", GuildID: "guild", ThreadID: "thread"}
	project := func() string {
		root, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return root
	}
	alpha, beta := project(), project()
	if err := s.update(func(st *State) error {
		conv := st.Conversations[binding.ConversationID]
		conv.Project = alpha
		putRecord(st, "conversations", &st.Conversations, binding.ConversationID, conv)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	previous := discussionFixtureOrigin("working")
	if _, err := c.submitDiscussion(previous, "working on alpha", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.submitDiscussionInProject(discussionFixtureOrigin("switch"), "switch to beta", "switch", "", "", beta, true); err != nil {
		t.Fatal(err)
	}
	followup := discussionFixtureOrigin("go-ahead")
	followup.ActorID = "new-participant"
	if _, err := c.submitDiscussionInProject(followup, "go ahead", "go ahead", "", "", alpha, false); err != nil {
		t.Fatal(err)
	}
	for idx := range 2 {
		runtime.status.Turn = protocol.ManagerNativeTurn{State: "completed", NativeInputID: runtime.sent[idx].ManagerAdmission.Nonce, NativeTurnID: fmt.Sprintf("turn-%d", idx)}
		if err := c.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	st, _ := s.Snapshot()
	if st.Conversations[binding.ConversationID].Project != beta || len(runtime.sent) != 3 ||
		!strings.Contains(runtime.sent[1].Text, beta) || !strings.Contains(runtime.sent[2].Text, beta) ||
		strings.Contains(runtime.sent[2].Text, alpha) || runtime.sent[2].Origin.ActorID != "new-participant" {
		t.Fatalf("queued follow-up restored stale project: %q sends=%+v", st.Conversations[binding.ConversationID].Project, runtime.sent)
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
	rejoined, err := d.recoverBinding(prior, next, protocol.ManagerNativeTurn{State: "idle"}, false, nil)
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
	if _, err := d.recoverBinding(rejoined, replacement, protocol.ManagerNativeTurn{State: "idle"}, true, nil); err != nil {
		t.Fatal(err)
	}
	st, _ = s.Snapshot()
	old := st.Admissions[admissionKey(prior.ConversationID, 1)]
	queued := st.Admissions[admissionKey(prior.ConversationID, 2)]
	if old.SettledAt.IsZero() || old.Phase != "uncertain" || old.OperationID != first.OperationID ||
		!queued.SettledAt.IsZero() || queued.Phase != "" || len(st.Publications) != 1 || len(runtime.sent) != 1 {
		t.Fatalf("continuity recovery changed old effect or lost new input: %+v / %+v", old, queued)
	}
	d.cfg.ConversationID = prior.ConversationID
	var notice DiscussionOutput
	if err := d.export(st, func(out DiscussionOutput) error { notice = out; return nil }); err != nil || notice.SystemCause != "manager_continuity_gap" || notice.AdmissionID != "" {
		t.Fatalf("unset or human-misattributed recovery notice: %+v %v", notice, err)
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
	if _, err := s.PublishSpeech(binding, permit, first); err != nil {
		t.Fatal(err)
	}
	d := &DiscussionController{store: s, cfg: Config{ConversationID: binding.ConversationID}, now: time.Now}
	st, _ := s.Snapshot()
	var uncertain DiscussionOutput
	lost := errors.New("outbox acknowledgment lost")
	if err := d.export(st, func(output DiscussionOutput) error { uncertain = output; return lost }); !errors.Is(err, lost) {
		t.Fatalf("torn outbox acknowledgment was ignored: %v", err)
	}
	if uncertain.ID != first.ID || uncertain.Text != first.Text {
		t.Fatalf("non-final publication was unavailable during the turn: %+v", uncertain)
	}
	if _, err := s.PublishSpeech(binding, permit, final); err != nil {
		t.Fatal(err)
	}
	st, err = s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	var received []DiscussionOutput
	emit := func(output DiscussionOutput) error {
		received = append(received, output)
		return nil
	}
	if err := d.export(st, emit); err != nil {
		t.Fatal(err)
	}
	if len(received) != 2 || received[0] != uncertain || received[0].ID != first.ID || received[1].ID != final.ID ||
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

func TestDiscussionWakeAuthorityUsesExactOriginalGrantAndCannotCreate(t *testing.T) {
	c, _, _, b := newControllerFixture(t)
	c.discussion = &DiscussionScope{InstallationID: "install", GuildID: "guild", ThreadID: "thread"}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	origin := discussionFixtureOrigin("original")
	rootAdmission := Admission{SourceID: "discussion:original", ConversationID: b.ConversationID, Origin: &origin}
	worker := WorkerAssociation{Session: "machine/worker", SessionInstance: "instance", InitialSourceID: rootAdmission.SourceID, JobID: "job", AttemptID: "attempt", ActionID: "action", Project: root}
	target := key(worker.Session, worker.SessionInstance)
	event := Event{ID: discussionEvidenceID(worker), JobID: worker.JobID, AttemptID: worker.AttemptID, WorkerProof: true, NativeTurnID: "native-turn",
		Metadata: json.RawMessage(`{"session":"machine/worker","instance":"instance"}`)}
	wake := Wake{ID: "wake", EventID: event.ID}
	trigger := Admission{SourceID: "wake:" + wake.ID, Sequence: 2}
	st := State{
		Admissions: map[string]Admission{"original": rootAdmission},
		Workers:    map[string]WorkerAssociation{key(b.ConversationID, target): worker},
		Events:     map[string]Event{key(b.ConversationID, event.ID): event},
		Wakes:      map[string]Wake{key(b.ConversationID, wake.ID): wake},
	}
	if grant, wakeID, err := c.discussionContinuation(st, trigger, target, "action"); err != nil || grant == nil || *grant != origin || wakeID != wake.ID {
		t.Fatalf("exact continuation did not preserve original actor: %+v %q %v", grant, wakeID, err)
	}
	if err := c.discussionEffect(trigger, "swarm_create"); !errors.Is(err, ErrExpired) {
		t.Fatalf("wake could create: %v", err)
	}
	worker.ActionID = "newer-directive"
	st.Workers[key(b.ConversationID, target)] = worker
	if _, _, err := c.discussionContinuation(st, trigger, target, "action"); !errors.Is(err, ErrExpired) {
		t.Fatalf("stale event could mutate after newer directive: %v", err)
	}
}

func TestDiscussionRejectsUnqualifiedWorkerProviderBeforeEffect(t *testing.T) {
	c, _, _, _ := newControllerFixture(t)
	c.discussion = &DiscussionScope{InstallationID: "install", GuildID: "guild", ThreadID: "thread"}
	workers := &controllerWorkerFixture{}
	c.workers = workers
	origin := discussionFixtureOrigin("worker")
	a := Admission{Origin: &origin, Project: "/work"}
	args := json.RawMessage(`{"action_id":"delegation","agent":"opencode","cwd":"/work","prompt":"inspect the test"}`)
	if _, err := c.managerEffect(context.Background(), "swarm_create", args, a); !errors.Is(err, ErrControllerUnsupported) || workers.launches != 0 {
		t.Fatalf("unsupported worker reached launch: %v launches=%d", err, workers.launches)
	}
}

func TestDiscussionJournalGapWithTrackedJobUsesPassiveRecovery(t *testing.T) {
	c, s, runtime, b := newControllerFixture(t)
	c.discussion = &DiscussionScope{}
	trackControllerFixtureWorker(t, s, b)
	workerKey := key(b.ConversationID, key("machine/worker", "instance"))
	if err := s.update(func(st *State) error {
		worker := st.Workers[workerKey]
		worker.JobID, worker.AttemptID = "job", "attempt"
		putRecord(st, "workers", &st.Workers, workerKey, worker)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	source := &controllerJournalFixture{
		resume:  protocol.JournalResume{SourceID: fixtureJournalSource, Cursor: 21, FullResync: true, HistoryGap: true},
		records: closedControllerJournal(),
	}
	if err := c.consumeDaemonJournal(context.Background(), source); !errors.Is(err, ErrControllerJournalResync) {
		t.Fatal(err)
	}
	st, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Wakes) != 0 || len(st.Admissions) != 0 || len(runtime.sent) != 0 || st.Workers[workerKey].RecoveryRequired ||
		st.Journal[b.ConversationID].Sequence != 21 {
		t.Fatalf("journal recovery became a manager turn or altered the exact worker grant: wakes=%d admissions=%d worker=%+v",
			len(st.Wakes), len(st.Admissions), st.Workers[workerKey])
	}
}

func TestDiscussionPersistsHumanIngressBeforeFencingRetirement(t *testing.T) {
	for _, tc := range []struct {
		name, retirement string
		managerErr, hold error
	}{
		{"unclaimed with draft", "unknown", managerproof.ErrDirectInputUnresolved, ErrDiscussionDirectInputHeld},
		{"unclaimed with no native proof", "unknown", managerproof.ErrUnavailable, ErrDiscussionNativeProofHeld},
		{"claimed", "uncertain", managerproof.ErrDirectInputUnresolved, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, store, fixture, b := newControllerFixture(t)
			req := protocol.ManagerRetireReq{Binding: runtimeBinding(b), Generation: 1, Checkpoint: strings.Repeat("a", 64)}
			if err := store.update(func(st *State) error {
				c := st.Conversations[b.ConversationID]
				c.Retirement = &req
				putRecord(st, "conversations", &st.Conversations, b.ConversationID, c)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			runtime := &discussionTestRuntime{controllerRuntimeFixture: fixture, managerErr: tc.managerErr,
				retirement: protocol.ManagerRetireReceipt{State: tc.retirement}}
			scope := DiscussionScope{InstallationID: "install", DiscussionID: "discord-discussion", ThreadID: "thread", DMOwnerID: "owner"}
			scope.VerifyOrigin = func(_ context.Context, origin schema.MessageOrigin) error {
				if origin.RequestID == "withdrawn" {
					return ErrDiscussionSourceChanged
				}
				return nil
			}
			d := &DiscussionController{store: store, runtime: runtime, scope: scope, cfg: Config{ConversationID: b.ConversationID},
				now: time.Now, ctx: t.Context(), dir: filepath.Dir(fixture.status.AuthFile), bridge: &privateHTTP{socket: fixture.status.BridgeSocket},
				handler: newManagerHandler(b.ConversationID, "fixture")}
			t.Cleanup(d.stopFollowing)
			for _, id := range []string{"withdrawn", "first", "second", "first"} {
				origin := schema.MessageOrigin{Kind: "owner_direct", InstallationID: "install", DiscussionID: "discord-discussion",
					ChannelID: "thread", RequestID: id, MessageID: id, ActorID: "owner", RequesterID: "owner", Revision: "current"}
				err := d.Admit(t.Context(), DiscussionInput{Origin: origin, Text: "message " + id})
				if id == "withdrawn" && !errors.Is(err, ErrDiscussionSourceChanged) || id != "withdrawn" && err != nil {
					t.Fatalf("admit %s: %v", id, err)
				}
			}
			before, err := store.Snapshot()
			if err != nil || len(before.Admissions) != 2 || len(runtime.retirements) != 0 || d.controller != nil {
				t.Fatalf("native or retirement consultation preceded persistence: %d admissions, retire=%v, %v", len(before.Admissions), runtime.retirements, err)
			}
			err = d.Sync(t.Context(), nil)
			after, snapErr := store.Snapshot()
			if snapErr != nil || len(after.Admissions) != 2 || len(fixture.sent) != 0 {
				t.Fatalf("preservation: admissions=%d sends=%d %v", len(after.Admissions), len(fixture.sent), snapErr)
			}
			if tc.retirement == "uncertain" {
				if !errors.Is(err, ErrControllerUncertain) || after.Conversations[b.ConversationID].Retirement == nil || d.controller != nil {
					t.Fatalf("claimed retirement lost: %v", err)
				}
				return
			}
			var hold *DiscussionInputHold
			if !errors.Is(err, tc.hold) || !errors.As(err, &hold) || hold.AdmissionID != "first" ||
				after.Conversations[b.ConversationID].Retirement != nil {
				t.Fatalf("unclaimed retirement did not clear before reporting the exact draft hold: %v", err)
			}
		})
	}
}
