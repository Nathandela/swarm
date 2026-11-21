package voice

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/status"
)

type recoveryWorkerProbe struct {
	*controllerWorkerFixture
	req                  schema.InteractionHistoryReq
	unavailable, limited bool
}

func (w *recoveryWorkerProbe) NativeHistoryPage(req schema.InteractionHistoryReq) (schema.NativeHistoryPage, error) {
	w.req = req
	if w.unavailable {
		return schema.NativeHistoryPage{}, ErrControllerNativeUnavailable
	}
	return schema.NativeHistoryPage{Scope: "authorized_worker_turn", WorkerEvidenceLimited: w.limited}, nil
}

func TestEnrolledBridgeAfterCustodyLoss(t *testing.T) {
	c, store, _, binding := newControllerFixture(t)
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	origin := discussionFixtureOrigin("request")
	origin.DiscussionID, origin.Revision = "discussion", "revision"
	var grantErr error
	c.discussion = &DiscussionScope{
		DiscussionID: origin.DiscussionID, InstallationID: origin.InstallationID, GuildID: origin.GuildID, ThreadID: origin.ChannelID,
		ProjectEnrolled: func(s string) bool { return s == project },
		VerifyAudience:  func(context.Context) error { return nil },
		VerifyGrant: func(_ context.Context, o schema.MessageOrigin) error {
			if o != origin {
				t.Fatal("revalidated wrong original grant")
			}
			return grantErr
		},
	}
	token := strings.Repeat("ab", 32)
	ep, _, _ := protocol.ParseID(binding.SessionID)
	w := WorkerAssociation{ConversationID: binding.ConversationID, Project: project, Parent: "old-manager", Session: ep + "/worker", SessionInstance: "exact", JobID: "job", ActionID: "job", AttemptID: "job"}
	k := key(binding.ConversationID, key(w.Session, w.SessionInstance))
	act := Action{ID: w.ActionID, ConversationID: binding.ConversationID, State: "settled"}
	act.Request, _ = json.Marshal(controllerActionRequest{Tool: "swarm_create", Project: project, Origin: &origin, Launch: &protocol.LaunchReq{Cwd: project, SpawnedFrom: w.Parent, Agent: "codex", InitialPrompt: "read the release file", WorkerBridge: "/bridge:" + token}})
	act.Receipt, _ = json.Marshal(createdWorkerResult{State: "created", Session: w.Session, SessionInstance: w.SessionInstance})
	if err := store.update(func(st *State) error {
		putRecord(st, "workers", &st.Workers, k, w)
		putRecord(st, "actions", &st.Actions, key(binding.ConversationID, act.ID), act)
		return quarantineControllerState(st, binding, "", "")
	}); err != nil {
		t.Fatal(err)
	}
	roster := &controllerWorkerFixture{rows: []protocol.SessionView{{ID: w.Session, Agent: "codex", Cwd: project, SpawnedFrom: w.Parent, SpawnIntent: schema.SpawnIntentDelegate, Status: status.Status{Process: status.ProcessRunning}, Capabilities: &schema.SessionCapabilities{SessionInstance: w.SessionInstance}}}}
	proofRuntime := &recoveryWorkerProbe{controllerWorkerFixture: roster}
	c.workers = proofRuntime
	question, _ := protocol.NewSemanticOperationID()
	in := workerMessageInput{ID: question, Kind: "needs-input", Text: "Which tree?"}
	send := func(input workerMessageInput) (workerMessageReceipt, error) {
		return c.acceptDiscussionWorkerMessage(t.Context(), "worker", token, input)
	}
	if _, err := c.acceptDiscussionWorkerMessage(t.Context(), "worker", strings.Repeat("cd", 32), in); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign grant", err)
	}
	storeAction := func(action Action) {
		t.Helper()
		if err := store.update(func(st *State) error {
			putRecord(st, "actions", &st.Actions, key(binding.ConversationID, action.ID), action)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	foreignReceipt := act
	foreignReceipt.Receipt, _ = json.Marshal(createdWorkerResult{State: "created", Session: w.Session, SessionInstance: "old"})
	storeAction(foreignReceipt)
	if _, err := send(in); !errors.Is(err, ErrStale) {
		t.Fatal("accepted another launch receipt", err)
	}
	storeAction(act)
	pending := Action{ConversationID: binding.ConversationID, ID: "steer", Target: key(w.Session, w.SessionInstance), State: "executing"}
	storeAction(pending)
	if _, err := send(in); !errors.Is(err, ErrControllerUncertain) {
		t.Fatal("accepted note during unresolved worker steering", err)
	}
	pending.State = "settled"
	storeAction(pending)
	verify := c.discussion.VerifyGrant
	c.discussion.VerifyGrant = func(ctx context.Context, o schema.MessageOrigin) error {
		pending.State = "executing"
		storeAction(pending)
		return verify(ctx, o)
	}
	if _, err := send(in); !errors.Is(err, ErrControllerUncertain) {
		t.Fatal("grant check raced a new steering action", err)
	}
	c.discussion.VerifyGrant = verify
	pending.State = "settled"
	storeAction(pending)
	row := roster.rows[0]
	for _, change := range []func(*protocol.SessionView){
		func(r *protocol.SessionView) {
			r.Capabilities = &schema.SessionCapabilities{SessionInstance: "successor"}
		},
		func(r *protocol.SessionView) { r.SpawnedFrom = "other" },
		func(r *protocol.SessionView) { r.Status.Process = status.ProcessExited },
		func(r *protocol.SessionView) { r.Cwd = "/another" },
		func(r *protocol.SessionView) { r.RosterHidden = true },
	} {
		roster.rows[0] = row
		change(&roster.rows[0])
		if _, err := send(in); err == nil {
			t.Fatal("accepted stale roster", roster.rows)
		}
	}
	roster.rows[0] = row
	grantErr = ErrDiscussionSourceChanged
	if _, err := send(in); !errors.Is(err, ErrDiscussionSourceChanged) {
		t.Fatal("revoked original message", err)
	}
	grantErr = nil
	roster.rows[0].Capabilities = nil
	proofRuntime.unavailable = true
	if _, err := send(in); err == nil {
		t.Fatal("accepted cold roster without independent native proof")
	}
	proofRuntime.unavailable = false
	if _, err := send(workerMessageInput{ID: question, Text: "untyped"}); !errors.Is(err, ErrStale) {
		t.Fatal("automatic steering authority escaped", err)
	}
	for range 2 {
		if _, err := send(in); err != nil {
			t.Fatal(err)
		}
	}
	if proofRuntime.req.Worker == nil || proofRuntime.req.Worker.SessionInstance != w.SessionInstance || proofRuntime.req.Worker.Parent != w.Parent ||
		proofRuntime.req.Worker.ManagerConversation != w.ConversationID || proofRuntime.req.Worker.PromptHash != hashBytes([]byte("read the release file")) {
		t.Fatal("cold roster fallback was not restricted to the exact original launch", proofRuntime.req)
	}
	changed := in
	changed.Text = "different"
	if _, err := send(changed); !errors.Is(err, ErrConflict) {
		t.Fatal("changed note replay", err)
	}
	st, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Events) != 1 || len(st.Wakes) != 1 || len(st.Admissions) != 1 || !st.Workers[k].RecoveryRequired {
		t.Fatal("lost quarantine or duplicate admission")
	}
	var a Admission
	for _, wake := range st.Wakes {
		a = st.Admissions[admissionKey(binding.ConversationID, wake.Admission)]
		event := st.Events[key(binding.ConversationID, wake.EventID)]
		if !a.RecoveryOnly || !wake.RecoveryOnly || !event.RecoveryOnly || !event.WorkerMessage {
			t.Fatal("recovered note conferred ordinary authority")
		}
	}
	// Switching to VM policy cannot convert a previously enrolled worker note into a VM mailbox grant.
	c.discussion.VMProjectAccess = true
	grantErr = ErrDiscussionSourceChanged
	read := SessionInput{Session: w.Session, SessionInstance: w.SessionInstance}
	if _, err := c.readDiscussionWorkerNote(t.Context(), read, a); !errors.Is(err, ErrDiscussionSourceChanged) {
		t.Fatal("VM policy exposed withdrawn enrolled note", err)
	}
	grantErr = nil
	if _, err := c.readDiscussionWorkerNote(t.Context(), read, a); err != nil {
		t.Fatal("exact original grant did not authorize retained note", err)
	}
	c.discussion.VMProjectAccess = false
	if _, _, err := c.discussionContinuation(st, a, key(w.Session, w.SessionInstance), "new-action"); !errors.Is(err, ErrExpired) {
		t.Fatal("allowed new input to uncertain worker", err)
	}
	a.Phase, a.NativeInputID, a.NativeTurnID = "running", "input", "turn"
	if err := store.update(func(st *State) error {
		putRecord(st, "admissions", &st.Admissions, admissionKey(binding.ConversationID, a.Sequence), a)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.replyToDiscussionWorker(t.Context(), a, "larch"); err != nil {
		t.Fatal(err)
	}
	if err := c.replyToDiscussionWorker(t.Context(), a, "larch"); err != nil {
		t.Fatal("exact reply retry", err)
	}
	if err := c.replyToDiscussionWorker(t.Context(), a, "cedar"); !errors.Is(err, ErrConflict) {
		t.Fatal("overwrote answer", err)
	}
	pull := workerMessageInput{ID: question, ReadReply: true}
	grantErr = ErrDiscussionSourceChanged
	if _, err := send(pull); !errors.Is(err, ErrDiscussionSourceChanged) {
		t.Fatal("answer disclosed after revocation", err)
	}
	grantErr = nil
	got, err := send(pull)
	if err != nil || got.Text != "larch" {
		t.Fatal("persisted point-to-point answer", got, err)
	}
	// A reply committed before retirement remains available on that same ID and grant.
	a.NativeBindingRetired = true
	if err := store.update(func(st *State) error {
		putRecord(st, "admissions", &st.Admissions, admissionKey(w.ConversationID, a.Sequence), a)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := send(pull); err != nil || got.Text != "larch" {
		t.Fatal("settled answer lost after retirement", got, err)
	}
	proofRuntime.limited = true // The search limit concerns result text, not the supervisor-verified incarnation.
	id, _ := protocol.NewSemanticOperationID()
	completion := workerMessageInput{ID: id, Kind: "complete", Text: "larch 713"}
	for range 2 {
		if _, err := send(completion); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := send(workerMessageInput{ReadReply: true, ID: id}); !errors.Is(err, ErrNotFound) {
		t.Fatal("completion could consume an answer", err)
	}
	st, err = store.Snapshot()
	if err != nil || len(st.Wakes) != 2 || len(st.Actions) != 2 || !st.Workers[k].RecoveryRequired || roster.launches != 0 {
		t.Fatal("bridge replayed work or dropped quarantine", err)
	}
	// A later manager cannot answer this retired native admission. The existing receipt must end the wait.
	interruptedID, _ := protocol.NewSemanticOperationID()
	interrupted := workerMessageInput{ID: interruptedID, Kind: "needs-input", Text: "Still needed?"}
	if _, err := send(interrupted); err != nil {
		t.Fatal(err)
	}
	eventID := "worker-message-" + hashValue([]string{w.Session, w.SessionInstance, w.AttemptID, interruptedID})
	if err := store.update(func(st *State) error {
		wake := st.Wakes[key(w.ConversationID, eventID)]
		ak := admissionKey(w.ConversationID, wake.Admission)
		admission := st.Admissions[ak]
		admission.NativeBindingRetired = true
		putRecord(st, "admissions", &st.Admissions, ak, admission)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, input := range []workerMessageInput{interrupted, {ID: interruptedID, ReadReply: true}, {ID: interruptedID, ReadReceipt: true}} {
		receipt, err := send(input)
		if err != nil || receipt.State != "superseded" {
			t.Fatal("retired unanswered note reopened", receipt, err)
		}
	}

}
