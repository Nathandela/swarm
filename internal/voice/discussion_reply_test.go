package voice

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/workermail"
)

func TestMissingDiscordReplySurvivesRestartAndConfirmsFinal(t *testing.T) {
	c, s, r, b := newControllerFixture(t)
	c.discussion = &DiscussionScope{InstallationID: "install", GuildID: "guild", ThreadID: "thread"}
	origin := discussionFixtureOrigin("missing")
	input, err := c.admitDiscussionInput(DiscussionInput{Origin: origin, Text: "Check the job", ExpectReply: true}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = c.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	r.status.Turn = protocol.ManagerNativeTurn{State: "completed", NativeInputID: r.sent[0].ManagerAdmission.Nonce, NativeTurnID: "original"}
	if err = c.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(input.ReplyCheckAfter, 0)
	confirmed := false
	d := DiscussionController{store: s, cfg: Config{ConversationID: b.ConversationID}, now: func() time.Time { return now }, scope: DiscussionScope{ConfirmedDelivery: func(_ context.Context, o DiscussionOutput) ([]string, error) {
		if o.ID != replyNoticeID(input, true) {
			t.Fatalf("unexpected output: %+v", o)
		}
		if confirmed {
			return []string{"1234"}, nil
		}
		return nil, nil
	}}}
	get := func() State {
		t.Helper()
		st, err := s.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	check := func() {
		t.Helper()
		if err = d.checkDiscussionReply(ctx, get()); err != nil {
			t.Fatal(err)
		}
	}
	// Reopen the Bolt ledger as a reconnecting controller before the first deadline.
	dir := filepath.Dir(s.db.Path())
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	b.Generation = s.generation
	r.status.Binding = runtimeBinding(b)
	c.store, c.binding, d.store = s, b, s
	if dormant, err := d.Dormant(); err != nil || dormant {
		t.Fatalf("dormant=%v err=%v", dormant, err)
	}
	check()
	a := get().Admissions[admissionKey(b.ConversationID, input.Sequence)]
	follow := get().Admissions[admissionKey(b.ConversationID, a.ReplyRecovery)]
	if follow.Sequence == 0 || !follow.RecoveryOnly || follow.RecoveryOutput == nil || follow.RecoveryOutput.AdmissionID != origin.RequestID || !strings.Contains(follow.InputText, origin.MessageID) {
		t.Fatalf("missing targeted report-only recovery: %+v", follow)
	}
	check()
	if len(get().Admissions) != 2 {
		t.Fatal("duplicate recovery")
	}
	r.status.Turn.State = "idle"
	r.status.ReadyForInput = true
	if err = c.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(r.sent) != 2 || r.sent[1].Text == "Check the job" {
		t.Fatalf("replayed request: %+v", r.sent)
	}
	r.status.Turn = protocol.ManagerNativeTurn{State: "completed", NativeInputID: r.sent[1].ManagerAdmission.Nonce, NativeTurnID: "follow"}
	if err = c.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	now = now.Add(discussionReplyWait)
	check()
	st := get()
	if len(st.Publications) != 1 || len(st.Admissions) != 2 {
		t.Fatalf("missing terminal notice or extra recovery: publications=%d inputs=%d", len(st.Publications), len(st.Admissions))
	}
	now = now.Add(discussionReplyWait)
	check()
	if err = s.Compact(b, now.Add(8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(get().Admissions) != 2 {
		t.Fatal("compaction removed the original request or its pending recovery receipt")
	}
	if !hasReplyObligation(get()) {
		t.Fatal("unacknowledged Discord message closed obligation")
	}
	confirmed = true
	now = now.Add(discussionReplyWait)
	check()
	if hasReplyObligation(get()) {
		t.Fatal("delivered result remained open")
	}
}

func TestDiscordOverdueTurnPostsOneNoticeWithoutNewInput(t *testing.T) {
	c, s, r, b := newControllerFixture(t)
	c.discussion = &DiscussionScope{InstallationID: "install", GuildID: "guild", ThreadID: "thread"}
	a, err := c.admitDiscussionInput(DiscussionInput{Origin: discussionFixtureOrigin("slow"), Text: "Inspect", ExpectReply: true}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(a.ReplyCheckAfter, 0)
	d := DiscussionController{store: s, cfg: Config{ConversationID: b.ConversationID}, now: func() time.Time { return now }}
	for range 2 {
		st, _ := s.Snapshot()
		if err = d.checkDiscussionReply(context.Background(), st); err != nil {
			t.Fatal(err)
		}
		now = now.Add(discussionReplyWait)
	}
	st, _ := s.Snapshot()
	if len(st.Publications) != 1 || len(r.sent) != 1 || len(st.Admissions) != 1 {
		t.Fatalf("pubs=%d sends=%d admissions=%d phase=%s", len(st.Publications), len(r.sent), len(st.Admissions), st.Admissions[admissionKey(b.ConversationID, a.Sequence)].Phase)
	}
	now = now.Add(discussionReplyStall)
	if err = d.checkDiscussionReply(t.Context(), st); err != nil {
		t.Fatal(err)
	}
	st, _ = s.Snapshot()
	if p := st.Publications[key(b.ConversationID, replyNoticeID(a, true))]; !p.Final || !strings.Contains(p.Text, "check the dispatcher") || len(r.sent) != 1 {
		t.Fatalf("stalled native turn left no actionable result or replayed work: %+v", p)
	}
}

func TestReplyRecoveryWithdrawnGrantLeavesFIFOClear(t *testing.T) {
	c, s, r, b := newControllerFixture(t)
	c.discussion = &DiscussionScope{InstallationID: "install", GuildID: "guild", ThreadID: "thread",
		VerifyGrant: func(context.Context, schema.MessageOrigin) error { return ErrDiscussionSourceChanged }}
	a, err := c.admitDiscussionInput(DiscussionInput{Origin: discussionFixtureOrigin("edited"), Text: "Run once", ExpectReply: true}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = c.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	r.status.Turn = protocol.ManagerNativeTurn{State: "completed", NativeInputID: r.sent[0].ManagerAdmission.Nonce, NativeTurnID: "done"}
	if err = c.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(a.ReplyCheckAfter, 0)
	d := DiscussionController{store: s, cfg: Config{ConversationID: b.ConversationID}, now: func() time.Time { return now }}
	st, _ := s.Snapshot()
	if err = d.checkDiscussionReply(ctx, st); err != nil {
		t.Fatal(err)
	}
	r.status.ReadyForInput = true
	r.status.Turn.State = "idle"
	if err = c.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	st, _ = s.Snapshot()
	next := st.Admissions[admissionKey(b.ConversationID, a.Sequence)]
	report := st.Admissions[admissionKey(b.ConversationID, next.ReplyRecovery)]
	if report.Phase != "rejected" || report.SettledAt.IsZero() || len(r.sent) != 1 {
		t.Fatalf("withdrawn report %q settled=%v sends=%d", report.Phase, report.SettledAt, len(r.sent))
	}
	now = now.Add(discussionReplyWait)
	if err = d.checkDiscussionReply(ctx, st); err != nil {
		t.Fatal(err)
	}
	st, _ = s.Snapshot()
	if hasReplyObligation(st) || len(st.Publications) != 0 {
		t.Fatalf("stale obligation or public notice")
	}
}

func TestDiscordClarificationOnlyPausesAfterReceipt(t *testing.T) {
	c, s, _, b := newControllerFixture(t)
	c.discussion = &DiscussionScope{InstallationID: "install", GuildID: "guild", ThreadID: "thread"}
	a, err := c.admitDiscussionInput(DiscussionInput{Origin: discussionFixtureOrigin("clarify"), Text: "Inspect", ExpectReply: true}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(a.ReplyCheckAfter, 0)
	req := Request{ID: "question", Kind: "manager", State: "pending", ConversationID: b.ConversationID, Hash: "proof", Payload: json.RawMessage(`{"question":"Which VM?"}`), OutputSequence: 1, ExpiresAt: now.Add(24 * time.Hour), Exported: true, DiscussionSource: DiscussionOutputSource{AdmissionID: a.Origin.RequestID}}
	if err = s.update(func(st *State) error {
		putRecord(st, "requests", &st.Requests, key(b.ConversationID, req.ID), req)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	confirmed := false
	d := DiscussionController{store: s, cfg: Config{ConversationID: b.ConversationID}, now: func() time.Time { return now }, scope: DiscussionScope{ConfirmedDelivery: func(_ context.Context, o DiscussionOutput) ([]string, error) {
		if o.QuestionHash != req.Hash || o.QuestionID != req.ID {
			return nil, ErrStale
		}
		if confirmed {
			return []string{"1"}, nil
		}
		return nil, nil
	}}}
	for _, receipt := range []bool{false, true} {
		confirmed = receipt
		st, _ := s.Snapshot()
		if err = d.checkDiscussionReply(context.Background(), st); err != nil {
			t.Fatal(err)
		}
		st, _ = s.Snapshot()
		out := st.Admissions[admissionKey(b.ConversationID, a.Sequence)]
		if out.ReplyWaitingForHuman != receipt || out.ReplyCheckAfter <= 0 || len(st.Admissions) != 1 {
			t.Fatalf("receipt=%v out=%+v", receipt, out)
		}
		now = now.Add(discussionReplyWait)
	}
}

func TestVMReplyCheckNeverStartsAnUnrestrictedRecovery(t *testing.T) {
	c, s, r, b := newControllerFixture(t)
	c.discussion = &DiscussionScope{InstallationID: "install", GuildID: "guild", ThreadID: "thread", VMProjectAccess: true}
	a, err := c.admitDiscussionInput(DiscussionInput{Origin: discussionFixtureOrigin("native"), Text: "Do the job", ExpectReply: true}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	r.status.Turn = protocol.ManagerNativeTurn{State: "completed", NativeInputID: r.sent[0].ManagerAdmission.Nonce, NativeTurnID: "native"}
	if err = c.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(a.ReplyCheckAfter, 0)
	d := DiscussionController{store: s, cfg: Config{ConversationID: b.ConversationID}, scope: *c.discussion, now: func() time.Time { return now }}
	for range 5 {
		st, _ := s.Snapshot()
		if err = d.checkDiscussionReply(t.Context(), st); err != nil {
			t.Fatal(err)
		}
		now = now.Add(discussionReplyWait)
	}
	st, _ := s.Snapshot()
	current := st.Admissions[admissionKey(b.ConversationID, a.Sequence)]
	if current.ReplyCheckAfter != 0 || !current.ReplyDeliveryUnknown || len(st.Publications) != 1 || len(st.Admissions) != 1 || len(r.sent) != 1 {
		t.Fatalf("unknown=%v next=%d pubs=%d sends=%d", current.ReplyDeliveryUnknown, current.ReplyCheckAfter, len(st.Publications), len(r.sent))
	}
	status, err := d.Status()
	if err != nil || status.UnconfirmedReplies != 1 || len(status.UnconfirmedOutputs) != 1 || len(status.UnconfirmedOutputs[0]) != 1 || status.UnconfirmedOutputs[0][0].ID != replyNoticeID(a, true) {
		t.Fatalf("missing scoped output for late receipts: %+v %v", status, err)
	}
	if dormant, err := d.Dormant(); err != nil || dormant {
		t.Fatalf("unexported status was abandoned: %v %v", dormant, err)
	}
	var published DiscussionOutput
	if err = d.Sync(t.Context(), func(o DiscussionOutput) error { published = o; return nil }); err != nil {
		t.Fatal(err)
	}
	if published.SystemCause != "" || published.AdmissionID != a.Origin.RequestID || published.ID != replyNoticeID(a, true) {
		t.Fatalf("notice lost its ordinary admission-scoped publication envelope: %+v", published)
	}
	if err = d.ValidatePublication(t.Context(), published, hashValue(published.Text)); err != nil {
		t.Fatal(err)
	}
	if dormant, err := d.Dormant(); err != nil || !dormant {
		t.Fatalf("pending uncertainty pinned manager: %v %v", dormant, err)
	}
}

func TestLegacyPendingReplyClassificationIsBounded(t *testing.T) {
	c, s, _, b := newControllerFixture(t)
	c.discussion = &DiscussionScope{InstallationID: "install", GuildID: "guild", ThreadID: "thread"}
	a, err := c.submitDiscussion(discussionFixtureOrigin("legacy"), "hello", "", "")
	if err != nil {
		t.Fatal(err)
	}
	k := admissionKey(b.ConversationID, a.Sequence)
	if err = s.update(func(st *State) error {
		v := st.Admissions[k]
		v.ReplyTracked = false
		putRecord(st, "admissions", &st.Admissions, k, v)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	d := DiscussionController{store: s, cfg: Config{ConversationID: b.ConversationID}, scope: *c.discussion, now: func() time.Time { return now }}
	d.scope.ExpectsReply = func(context.Context, schema.MessageOrigin) (bool, error) { return false, errors.New("offline") }
	st, _ := s.Snapshot()
	if err = d.trackLegacyReplies(st); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		st, _ = s.Snapshot()
		if err = d.checkDiscussionReply(t.Context(), st); err != nil {
			t.Fatal(err)
		}
		now = now.Add(discussionReplyWait)
	}
	st, _ = s.Snapshot()
	v := st.Admissions[k]
	if !v.ReplyTracked || !v.ReplyDeliveryUnknown || v.ReplyCheckAfter != 0 || len(st.Publications) != 0 {
		t.Fatalf("migration guessed old message intent: %+v", v)
	}
}

func TestSharedYieldRequiresCurrentQuietHumanTurn(t *testing.T) {
	c, s, _, b := newControllerFixture(t)
	c.discussion = &DiscussionScope{InstallationID: "install", GuildID: "guild", ThreadID: "thread"}
	a, err := c.admitDiscussionInput(DiscussionInput{Origin: discussionFixtureOrigin("chat"), Text: "FYI everyone", ExpectReply: true}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	k := admissionKey(b.ConversationID, a.Sequence)
	if err = s.update(func(st *State) error {
		a = st.Admissions[k]
		a.Phase, a.NativeInputID, a.NativeTurnID = "running", "n", "t"
		putRecord(st, "admissions", &st.Admissions, k, a)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	stale := a
	stale.NativeInputID = "another"
	if err = c.yieldSharedReply(stale); !errors.Is(err, ErrExpired) {
		t.Fatalf("stale native input yielded: %v", err)
	}
	if err = c.yieldSharedReply(a); err != nil {
		t.Fatal(err)
	}
	st, err := s.Snapshot()
	if err != nil || hasReplyObligation(st) || len(st.Publications) != 0 {
		t.Fatalf("chatter produced a reply: %v", err)
	}
	if err = s.update(func(st *State) error {
		pub := Publication{ID: "progress", ConversationID: b.ConversationID, OutputSequence: 1, DiscussionSource: DiscussionOutputSource{AdmissionID: a.Origin.RequestID}}
		putRecord(st, "publications", &st.Publications, key(b.ConversationID, pub.ID), pub)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = c.yieldSharedReply(a); !errors.Is(err, ErrConflict) {
		t.Fatalf("an acknowledged turn yielded: %v", err)
	}
}

func TestSyncKeepsDelegatedReplyOpenWhileWorkerRuns(t *testing.T) {
	for _, vm := range []bool{false, true} {
		c, s, r, b := newControllerFixture(t)
		c.discussion = &DiscussionScope{InstallationID: "install", GuildID: "guild", ThreadID: "thread", VMProjectAccess: vm}
		a, err := c.admitDiscussionInput(DiscussionInput{Origin: discussionFixtureOrigin("worker"), Text: "Run the report", ExpectReply: true}, "", false)
		if err != nil {
			t.Fatal(err)
		}
		if err = c.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		r.status.Turn = protocol.ManagerNativeTurn{State: "completed", NativeInputID: r.sent[0].ManagerAdmission.Nonce, NativeTurnID: "native"}
		if err = c.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		w := WorkerAssociation{ConversationID: b.ConversationID, Session: "machine/worker", SessionInstance: "instance", Parent: "manager", AttemptID: "vm-child-attempt", JobID: "job", Admission: a.Sequence, VMChild: vm, Mailbox: vm}
		if err = s.update(func(st *State) error {
			putRecord(st, "workers", &st.Workers, key(b.ConversationID, key(w.Session, w.SessionInstance)), w)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		now := time.Unix(a.ReplyCheckAfter, 0)
		d := DiscussionController{cfg: Config{ConversationID: b.ConversationID}, store: s, scope: *c.discussion, now: func() time.Time { return now }}
		emit := func(o DiscussionOutput) error { return nil }
		if err = d.Sync(t.Context(), emit); err != nil {
			t.Fatal(err)
		}
		now = now.Add(15 * time.Minute)
		if err = d.Sync(t.Context(), emit); err != nil {
			t.Fatal(err)
		}
		st, _ := s.Snapshot()
		if st.Publications[key(b.ConversationID, replyNoticeID(a, true))].Final || !hasReplyObligation(st) {
			t.Fatalf("vm=%v: terminated a live worker", vm)
		}
	}
}

func TestSyncAcceptsAuthenticatedVMWorkerWakeFinal(t *testing.T) {
	c, s, r, b := newControllerFixture(t)
	c.discussion = &DiscussionScope{InstallationID: "install", GuildID: "guild", ThreadID: "thread", VMProjectAccess: true}
	a, err := c.admitDiscussionInput(DiscussionInput{Origin: discussionFixtureOrigin("job"), Text: "Get report", ExpectReply: true}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	r.status.Turn = protocol.ManagerNativeTurn{State: "completed", NativeInputID: r.sent[0].ManagerAdmission.Nonce, NativeTurnID: "native"}
	if err = c.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	c.managerSettingsDir = t.TempDir()
	ep, parent, _ := protocol.ParseID(b.SessionID)
	mail := workermail.Binding{Discussion: b.ConversationID, Session: ep + "/worker", Instance: "instance", Parent: parent, Attempt: "vm-child-attempt", Agent: "codex", Project: t.TempDir(), ParentInputHash: hashBytes([]byte(r.sent[0].ManagerAdmission.Nonce))}
	if _, err = workermail.Submit(c.managerSettingsDir, mail, "done", "complete", "report", true); err != nil {
		t.Fatal(err)
	}
	st, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.ingestWorkerMail(t.Context(), st); err != nil {
		t.Fatal(err)
	}
	st, err = s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	w := st.Workers[key(b.ConversationID, key(mail.Session, mail.Instance))]
	if w.Admission != a.Sequence || w.ParentInputHash != mail.ParentInputHash {
		t.Fatal("ordinary native input not linked")
	}
	wakeID := "worker-message-" + hashValue([]string{mail.Session, mail.Instance, mail.Attempt, "done"})
	wake := st.Wakes[key(b.ConversationID, wakeID)]
	wakeInput := st.Admissions[admissionKey(b.ConversationID, wake.Admission)]
	wakeInput.SettledAt = time.Now()
	wakeInput.Phase = "completed"
	wake.State = "completed"
	source := discussionOutputSource(&st, b.ConversationID, wakeInput)
	p := Publication{ConversationID: b.ConversationID, ID: "answer", Final: true, Exported: true, Admission: wake.Admission, OutputSequence: 3, Text: "report", DiscussionSource: source}
	if err = s.update(func(st *State) error {
		putRecord(st, "admissions", &st.Admissions, admissionKey(b.ConversationID, wakeInput.Sequence), wakeInput)
		putRecord(st, "wakes", &st.Wakes, key(b.ConversationID, wake.ID), wake)
		putRecord(st, "publications", &st.Publications, key(b.ConversationID, p.ID), p)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(a.ReplyCheckAfter, 0)
	d := DiscussionController{cfg: Config{ConversationID: b.ConversationID}, store: s, scope: *c.discussion, now: func() time.Time { return now }}
	d.scope.ConfirmedDelivery = func(_ context.Context, o DiscussionOutput) ([]string, error) {
		if o.ID == p.ID {
			return []string{"123"}, nil
		}
		return nil, nil
	}
	if err = d.Sync(t.Context(), func(o DiscussionOutput) error { return nil }); err != nil {
		t.Fatal(err)
	}
	st, _ = s.Snapshot()
	if st.Publications[key(b.ConversationID, replyNoticeID(a, true))].Final || hasReplyObligation(st) {
		t.Fatal("confirmed child result was ignored")
	}
	for _, alter := range []func(*State){
		func(st *State) {
			x := w
			x.ParentInputHash = hashBytes([]byte("different human turn"))
			st.Workers[key(b.ConversationID, key(w.Session, w.SessionInstance))] = x
		},
		func(st *State) {
			e := st.Events[key(b.ConversationID, wake.EventID)]
			e.WorkerMessage = false
			st.Events[key(b.ConversationID, e.ID)] = e
		},
		func(st *State) {
			x := p
			x.DiscussionSource.AttemptID = "different-attempt"
			st.Publications[key(b.ConversationID, p.ID)] = x
		},
	} {
		snapshot, err := s.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		alter(&snapshot)
		if discussionReplyEvidence(snapshot, a, now).final {
			t.Fatal("crossed a native-input, note, or worker-attempt boundary")
		}
	}

}

func TestSyncWithdrawnSourceNeverAnnouncesMonitor(t *testing.T) {
	c, s, r, b := newControllerFixture(t)
	c.discussion = &DiscussionScope{InstallationID: "install", GuildID: "guild", ThreadID: "thread", VMProjectAccess: true}
	a, err := c.admitDiscussionInput(DiscussionInput{Origin: discussionFixtureOrigin("deleted"), Text: "Inspect", ExpectReply: true}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	r.status.Turn = protocol.ManagerNativeTurn{State: "completed", NativeInputID: r.sent[0].ManagerAdmission.Nonce, NativeTurnID: "native"}
	if err = c.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	d := DiscussionController{cfg: Config{ConversationID: b.ConversationID}, store: s, scope: *c.discussion, now: func() time.Time { return time.Unix(a.ReplyCheckAfter, 0) }}
	d.scope.VerifyGrant = func(context.Context, schema.MessageOrigin) error { return ErrDiscussionSourceChanged }
	if err = d.Sync(t.Context(), func(DiscussionOutput) error { t.Fatal("posted about a withdrawn request"); return nil }); err != nil {
		t.Fatal(err)
	}
	st, _ := s.Snapshot()
	if hasReplyObligation(st) {
		t.Fatal("withdrawn request pinned the dispatcher")
	}
}
