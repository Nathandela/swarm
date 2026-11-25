package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/status"
)

func TestDiscussionLaunchChecksGrantBeforePreparationAndDispatch(t *testing.T) {
	for _, tc := range []struct {
		name, tool  string
		deny        int
		wantActions int
	}{
		{"create before preparation", "swarm_create", 1, 0},
		{"create before dispatch", "swarm_create", 2, 1},
		{"resume before preparation", "swarm_resume", 1, 0},
		{"resume before dispatch", "swarm_resume", 2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, s, _, _ := provenControllerFixture(t)
			project, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			origin := discussionFixtureOrigin("new")
			if err := s.update(func(st *State) error {
				a := st.Admissions[admissionKey(c.binding.ConversationID, 1)]
				a.Origin = &origin
				a.Project = project
				putRecord(st, "admissions", &st.Admissions, admissionKey(c.binding.ConversationID, 1), a)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			workers := &controllerWorkerFixture{}
			c.workers = workers
			checks := 0
			c.discussion = &DiscussionScope{InstallationID: "install", GuildID: "guild", ThreadID: "thread",
				VerifyAudience: func(context.Context) error { return nil },
				VerifyGrant: func(_ context.Context, selected schema.MessageOrigin) error {
					if selected != origin {
						t.Fatalf("unexpected origin %+v", selected)
					}
					checks++
					if checks == tc.deny {
						return ErrDiscussionSourceChanged
					}
					return nil
				}}
			args := json.RawMessage(fmt.Sprintf(`{"action_id":"fresh","agent":"codex","cwd":%q,"prompt":"check the test"}`, project))
			if tc.tool == "swarm_resume" {
				worker := WorkerAssociation{Session: "machine/worker", SessionInstance: "instance", JobID: "job", AttemptID: "attempt", Project: project}
				if err := s.update(func(st *State) error {
					putRecord(st, "workers", &st.Workers, key(c.binding.ConversationID, key(worker.Session, worker.SessionInstance)), worker)
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				workers.rows = []protocol.SessionView{{ID: worker.Session, Agent: "codex", Cwd: project, Status: status.Status{Process: status.ProcessExited}, Capabilities: &schema.SessionCapabilities{SessionInstance: worker.SessionInstance}}}
				args = json.RawMessage(`{"action_id":"fresh","session":"machine/worker","session_instance":"instance"}`)
			}
			_, err = c.ManagerCall(context.Background(), ManagerToolCall{Name: tc.tool, Arguments: args})
			st, _ := s.Snapshot()
			if !errors.Is(err, ErrDiscussionSourceChanged) || workers.launches != 0 || len(st.Actions) != tc.wantActions {
				t.Fatalf("revoked grant: checks=%d launches=%d actions=%d err=%v", checks, workers.launches, len(st.Actions), err)
			}
		})
	}
}

func TestDiscussionJobPublicationRequiresExactExportAndEnrolledProject(t *testing.T) {
	_, s, _, binding := newControllerFixture(t)
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	origin := discussionFixtureOrigin("work")
	raw, _ := json.Marshal(controllerActionRequest{Tool: "swarm_create", Origin: &origin, Project: project})
	scope := DiscussionScope{InstallationID: "install", GuildID: "guild", ThreadID: "thread"}
	enrolled := true
	scope.ProjectEnrolled = func(path string) bool { return enrolled && path == project }
	d := &DiscussionController{store: s, cfg: Config{ConversationID: binding.ConversationID}, scope: scope}
	source := DiscussionOutputSource{AdmissionID: "work", JobID: "job", AttemptID: "attempt"}
	out := DiscussionOutput{DiscussionOutputSource: source, ID: "pub", Sequence: 3}
	if err := s.update(func(st *State) error {
		p := Publication{DiscussionSource: source, ID: out.ID, Text: "verified", OutputSequence: out.Sequence, Exported: true}
		action := Action{ID: "attempt", ConversationID: binding.ConversationID, State: "settled", Request: raw, Receipt: json.RawMessage(`{"state":"created","session":"machine/worker","session_instance":"physical"}`)}
		putRecord(st, "publications", &st.Publications, key(binding.ConversationID, p.ID), p)
		putRecord(st, "actions", &st.Actions, key(binding.ConversationID, action.ID), action)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.ValidatePublication(context.Background(), out, hashValue("verified")); err != nil {
		t.Fatal(err)
	}
	if err := d.ValidatePublication(context.Background(), out, hashValue("substituted")); !errors.Is(err, ErrStale) {
		t.Fatalf("body changed: %v", err)
	}
	forged := out
	forged.AttemptID = "other"
	if err := d.ValidatePublication(context.Background(), forged, hashValue("verified")); !errors.Is(err, ErrStale) {
		t.Fatalf("attempt changed: %v", err)
	}
	enrolled = false
	if err := d.ValidatePublication(context.Background(), out, hashValue("verified")); !errors.Is(err, ErrExpired) {
		t.Fatalf("withdrawn project published: %v", err)
	}
}

func TestDiscussionEvidenceLimitRecordsSafeNoticeOnceAndExplainsStatus(t *testing.T) {
	_, s, _, b := newControllerFixture(t)
	worker := WorkerAssociation{JobID: "job", AttemptID: "attempt", ActionID: "attempt", Session: "machine/worker", SessionInstance: "instance", ConversationID: b.ConversationID}
	if err := s.update(func(st *State) error {
		putRecord(st, "workers", &st.Workers, key(b.ConversationID, key(worker.Session, worker.SessionInstance)), worker)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.noteDiscussionWorkerEvidenceLimit(b, worker); err != nil {
			t.Fatal(err)
		}
	}
	st, _ := s.Snapshot()
	d := &DiscussionController{store: s, cfg: Config{ConversationID: b.ConversationID}}
	current, err := d.Status()
	if err != nil || len(current.Jobs) != 1 || current.Jobs[0].State != "evidence_limited" || current.Jobs[0].Diagnostic == "" {
		t.Fatalf("bad job status: %+v, %v", current, err)
	}
	if len(st.Publications) != 1 {
		t.Fatalf("expected one durable notice; got %d", len(st.Publications))
	}
	for _, p := range st.Publications {
		if p.Text != discussionEvidenceLimitNotice || p.DiscussionSource.SystemCause != "worker_evidence_limited" || p.DiscussionSource.JobID != worker.JobID || p.DiscussionSource.AttemptID != worker.AttemptID {
			t.Fatalf("limit notice held unexpected evidence: %+v", p)
		}
	}
}

func TestDiscussionSettledLaunchReceiptSurvivesGrantRevocation(t *testing.T) {
	c, s, _, b := provenControllerFixture(t)
	origin := discussionFixtureOrigin("worker")
	st, _ := s.Snapshot()
	a := st.Admissions[admissionKey(b.ConversationID, 1)]
	a.Origin = &origin
	a.Project = "/withdrawn"
	args := json.RawMessage(`{"action_id":"old","agent":"codex","cwd":"/withdrawn","prompt":"already finished"}`)
	normalized, err := withoutBusinessID(args, "action_id")
	if err != nil {
		t.Fatal(err)
	}
	id := c.businessReference("action", 1, "", "old")
	request, _ := json.Marshal(controllerActionRequest{Tool: "swarm_create", Arguments: normalized, Origin: &origin, Project: a.Project})
	receipt := json.RawMessage(`{"state":"created","session":"machine/worker","session_instance":"instance"}`)
	if err := s.update(func(current *State) error {
		effect := Action{ID: id, ConversationID: b.ConversationID, Admission: 1, NativeInputID: a.NativeInputID, State: "settled", Request: request, Receipt: receipt}
		putRecord(current, "actions", &current.Actions, key(b.ConversationID, id), effect)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	c.workers = &controllerWorkerFixture{}
	c.discussion = &DiscussionScope{InstallationID: "install", GuildID: "guild", ThreadID: "thread",
		ProjectEnrolled: func(string) bool { return false },
		VerifyAudience:  func(context.Context) error { t.Fatal("receipt lookup contacted Discord"); return nil }}
	got, err := c.managerEffect(context.Background(), "swarm_create", args, a)
	if err != nil || !bytes.Contains(got, []byte(`"state":"created"`)) {
		t.Fatalf("past receipt blocked: %s, %v", got, err)
	}
}

func TestDiscussionHumanFollowupCarriesEveryReadProjectGrant(t *testing.T) {
	s, b, _ := testStore(t)
	b.ConversationID = DiscussionConversationPrefix + "summary"
	b, err := s.BindManager(b)
	if err != nil {
		t.Fatal(err)
	}
	orig := discussionFixtureOrigin("follow-up")
	roots := make([]string, 3)
	for i := range roots {
		roots[i], err = filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
	}
	withdrawn := ""
	scope := DiscussionScope{InstallationID: "install", GuildID: "guild", ThreadID: "thread", VerifyGrant: func(context.Context, schema.MessageOrigin) error { return nil }, VerifyAudience: func(context.Context) error { return nil }, ProjectEnrolled: func(p string) bool { return p != withdrawn }}
	c := &Controller{binding: b, store: s, discussion: &scope}
	a, err := c.submitDiscussionInProject(orig, "what did the workers find?", "", "", "", roots[0], false)
	if err != nil {
		t.Fatal(err)
	}
	a.NativeInputID, a.NativeTurnID, a.Phase = "input", "turn", "running"
	if err := s.update(func(st *State) error {
		putRecord(st, "admissions", &st.Admissions, admissionKey(b.ConversationID, a.Sequence), a)
		for i, project := range roots[1:] {
			w := WorkerAssociation{ConversationID: b.ConversationID, Project: project, Session: fmt.Sprintf("machine/w%d", i), SessionInstance: "instance", JobID: fmt.Sprintf("job-%d", i), AttemptID: fmt.Sprintf("attempt-%d", i), ActionID: "verified", Admission: 42}
			putRecord(st, "workers", &st.Workers, key(b.ConversationID, key(w.Session, w.SessionInstance)), w)
			intent, _ := json.Marshal(controllerActionRequest{Tool: "swarm_create", Origin: &orig, Project: project, Launch: &protocol.LaunchReq{Cwd: project}})
			receipt, _ := json.Marshal(createdWorkerResult{State: "created", Session: w.Session, SessionInstance: w.SessionInstance})
			effect := Action{ID: w.AttemptID, ConversationID: b.ConversationID, State: "settled", Request: intent, Receipt: receipt}
			putRecord(st, "actions", &st.Actions, key(b.ConversationID, effect.ID), effect)
			e := Event{ID: discussionEvidenceID(w), WorkerProof: true, NativeTurnID: "exact-turn", JobID: w.JobID, AttemptID: w.AttemptID, Metadata: json.RawMessage(fmt.Sprintf(`{"session":%q,"instance":%q,"data":{"turn_id":"exact-turn","state":"completed","text":"verified result"}}`, w.Session, w.SessionInstance))}
			putRecord(st, "events", &st.Events, key(b.ConversationID, e.ID), e)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		raw, err := c.readDiscussionWorker(context.Background(), ReadInput{SessionInput: SessionInput{fmt.Sprintf("machine/w%d", i), "instance"}}, a)
		var read struct{ Evidence *schema.WorkerTurnEvidence }
		if err != nil || json.Unmarshal(raw, &read) != nil || read.Evidence == nil || read.Evidence.Text != "verified result" {
			t.Fatalf("worker %d evidence missing: %s, %v", i, raw, err)
		}
	}
	permit, err := s.GrantSpeech(b, a.Sequence, a.NativeInputID, a.NativeTurnID, 1)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.PublishSpeech(b, permit, Publication{ID: "summary", Admission: a.Sequence, NativeTurnID: a.NativeTurnID, Sequence: 1, Text: "both passed", Final: true})
	if err != nil {
		t.Fatal(err)
	}
	d := &DiscussionController{store: s, cfg: Config{ConversationID: b.ConversationID}, scope: scope}
	st, _ := s.Snapshot()
	var out DiscussionOutput
	if err := d.export(st, func(p DiscussionOutput) error { out = p; return nil }); err != nil {
		t.Fatal(err)
	}
	if out.JobID != "" || out.ID != p.ID {
		t.Fatalf("expected no-job summary: %+v", out)
	}
	if err := d.ValidatePublication(context.Background(), out, hashValue(out.Text)); err != nil {
		t.Fatal(err)
	}
	for _, root := range roots {
		withdrawn = root
		if err := d.ValidatePublication(context.Background(), out, hashValue(out.Text)); !errors.Is(err, ErrExpired) {
			t.Fatalf("missing grant fence for %q: %v", root, err)
		}
	}
}

func TestDiscussionNeutralClarificationPublishesBeforeProjectSelection(t *testing.T) {
	s, b, _ := testStore(t)
	b.ConversationID = DiscussionConversationPrefix + "neutral"
	b, err := s.BindManager(b)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.PutRequest(b, Request{ID: "clarify", Kind: "manager", Payload: json.RawMessage(`{"question":"Which project?","choices":["A","B"]}`), ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	d := &DiscussionController{store: s, cfg: Config{ConversationID: b.ConversationID}, now: time.Now, scope: DiscussionScope{ProjectEnrolled: func(string) bool { return false }}}
	st, _ := s.Snapshot()
	var out DiscussionOutput
	if err := d.export(st, func(p DiscussionOutput) error { out = p; return nil }); err != nil {
		t.Fatal(err)
	}
	if out.QuestionID != r.ID || out.JobID != "" {
		t.Fatalf("not a neutral question: %+v", out)
	}
	if err := d.ValidatePublication(context.Background(), out, hashValue(out.Text)); err != nil {
		t.Fatal(err)
	}
}

func TestTicketDiscussionGrantsOneWorkerToTheVerifiedMachineOrigin(t *testing.T) {
	c, _, runtime, _ := newControllerFixture(t)
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	origin := discussionFixtureOrigin("ta_fixed")
	origin.Kind, origin.ActorID, origin.RequesterID, origin.DiscussionID, origin.Revision = "ticket_assignment", "cerbere", "cerbere", "discussion", "revision"
	scope := &DiscussionScope{InstallationID: origin.InstallationID, GuildID: origin.GuildID, ThreadID: origin.ChannelID, DiscussionID: origin.DiscussionID}
	if scope.validateOrigin(origin) {
		t.Fatal("unenrolled scope accepted a machine grant")
	}
	scope.TicketRequestID, scope.TicketProvider = origin.RequestID, "codex"
	if !scope.validateOrigin(origin) {
		t.Fatal("enrolled scope refused the machine grant")
	}
	c.discussion = scope
	scope.ProjectEnrolled = func(path string) bool { return path == project }
	scope.VerifyGrant = func(ctx context.Context, grant schema.MessageOrigin) error {
		if grant != origin {
			return ErrDiscussionSourceChanged
		}
		return nil
	}
	worker := &controllerWorkerFixture{
		rows: []protocol.SessionView{{
			ID: "machine/created", Agent: "codex", Cwd: project, SpawnedFrom: "session", SpawnIntent: schema.SpawnIntentDelegate,
			Capabilities: &schema.SessionCapabilities{SessionInstance: "created-instance"},
		}},
	}
	c.workers = worker
	if _, err := c.submitDiscussionInProject(origin, "check the regression", "", "", "", project, true); err != nil {
		t.Fatal(err)
	}
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runtime.sent) != 1 || runtime.sent[0].Origin == nil || *runtime.sent[0].Origin != origin {
		t.Fatalf("native submission lost machine origin: %+v", runtime.sent)
	}
	nonce := runtime.sent[0].ManagerAdmission.Nonce
	runtime.status.ReadyForInput = false
	runtime.status.Turn = protocol.ManagerNativeTurn{State: "running", NativeInputID: nonce, NativeTurnID: "ticket-turn"}
	runtime.proof = protocol.ManagerCallProof{Main: true, NativeInputID: nonce, NativeTurnID: "ticket-turn", NativeCallID: "native-call"}
	launch := func(actionID, agent string) (json.RawMessage, error) {
		args := fmt.Appendf(nil, `{"action_id":%q,"agent":%q,"cwd":%q,"prompt":"check the regression"}`, actionID, agent, project)
		return c.ManagerCall(context.Background(), ManagerToolCall{Name: "swarm_create", Arguments: args})
	}
	if _, err := launch("wrong-provider", "claude"); !errors.Is(err, ErrControllerUnsupported) || worker.launches != 0 {
		t.Fatalf("a non-enrolled provider launched a worker: launches=%d %v", worker.launches, err)
	}
	if _, err := launch("first", "codex"); err != nil {
		t.Fatal(err)
	}
	if _, err := launch("second", "codex"); !errors.Is(err, ErrConflict) || worker.launches != 1 {
		t.Fatalf("a second action launched a worker: launches=%d %v", worker.launches, err)
	}
	if _, err := launch("first", "codex"); err != nil || worker.launches != 1 {
		t.Fatalf("same operation repeated its launch: launches=%d %v", worker.launches, err)
	}
	human := discussionFixtureOrigin("human")
	human.DiscussionID, human.Revision = origin.DiscussionID, "human-revision"
	if err := c.discussionEffect(Admission{Origin: &human}, "swarm_create"); !errors.Is(err, ErrControllerUnsupported) {
		t.Fatalf("human follow-up could independently create a worker: %v", err)
	}
	if err := c.discussionEffect(Admission{Origin: &human}, "swarm_send"); err != nil {
		t.Fatalf("human follow-up could not steer the existing worker: %v", err)
	}
}
