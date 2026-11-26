package voice

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/status"
)

type failingDiscussionRuntime struct {
	parkedRecoveryRuntime
	old         string
	failure     error
	unavailable bool
	launchCalls int
}

func (r *failingDiscussionRuntime) LaunchOperation(op string, req protocol.LaunchReq) (string, string, error) {
	r.launchCalls++
	if r.unavailable {
		return "", "", protocol.ErrManagerLaunchPrerequisite
	}
	return r.parkedRecoveryRuntime.LaunchOperation(op, req)
}

func (r *failingDiscussionRuntime) ManagerRuntime(session, instance string) (protocol.ManagerRuntimeStatus, error) {
	if session == r.old && r.failure != nil {
		return protocol.ManagerRuntimeStatus{}, r.failure
	}
	s := r.status
	if session != r.old {
		s.ProviderExhausted = false
		s.ReadyForInput = true
	}
	return s, nil
}

func TestDiscussionReplacesFailedOwnerAcrossRestart(t *testing.T) {
	for _, cause := range []string{schema.ManagerReplaceQuota, schema.ManagerReplaceCustody, schema.ManagerReplaceExited} {
		t.Run(cause, func(t *testing.T) {
			c, s, native, old := newControllerFixture(t)
			r := &failingDiscussionRuntime{parkedRecoveryRuntime: parkedRecoveryRuntime{discussionTestRuntime: discussionTestRuntime{controllerRuntimeFixture: native}, receipts: map[string]schema.OperationOutcomeView{}}, old: old.SessionID}
			cfg := testConfig()
			cfg.ConversationID = old.ConversationID
			dir := privateConfigDir(t)
			d := &DiscussionController{store: s, controller: c, runtime: r, dir: dir, endpoint: "machine", cfg: cfg, bridge: &privateHTTP{socket: dir + "/bridge.sock"}, ctx: t.Context(), handler: newManagerHandler(old.ConversationID, "test"), now: time.Now}
			t.Cleanup(d.stopFollowing)
			r.status.BridgeSocket, r.status.AuthFile = d.refs().Socket, d.refs().AuthFile
			c.refs, c.runtime = d.refs(), r
			c.discussion = &DiscussionScope{InstallationID: "install", GuildID: "guild", ThreadID: "thread", VMProjectAccess: true}
			first, err := c.submitDiscussion(discussionFixtureOrigin("original"), "execute original exactly once", "", "")
			if err != nil {
				t.Fatal(err)
			}
			if err = c.Tick(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err = s.update(func(st *State) error {
				w := WorkerAssociation{ConversationID: old.ConversationID, JobID: "android", AttemptID: "android-original", Session: "machine/worker", SessionInstance: "worker-original", Admission: first.Sequence, Project: "/android", VMChild: true}
				putRecord(st, "workers", &st.Workers, key(old.ConversationID, key(w.Session, w.SessionInstance)), w)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			r.rows = []protocol.SessionView{{ID: old.SessionID, Agent: old.Provider, Role: "manager", ManagerConversationID: old.ConversationID, Status: status.Status{Process: status.ProcessRunning}, Capabilities: &protocol.SessionCapabilities{SessionInstance: old.SessionInstance}}}
			r.retirement.State = "uncertain"
			if cause == schema.ManagerReplaceQuota {
				r.status.ProviderExhausted = true
				r.status.ReadyForInput = false
				r.status.Turn = protocol.ManagerNativeTurn{NativeTurnID: "quota-failed", State: "completed", Reason: "error"}
			} else {
				r.failure = managerproof.ErrQuarantined
			}
			if cause == schema.ManagerReplaceExited {
				r.rows[0].Status.Process = status.ProcessExited
			}
			if err = d.Sync(t.Context(), nil); !errors.Is(err, ErrControllerUncertain) {
				t.Fatalf("first failure: %v", err)
			}
			st, _ := s.Snapshot()
			if len(r.requests) != 0 || len(r.sent) != 1 {
				t.Fatal("replacement or resend before physical exit")
			}
			if cause != schema.ManagerReplaceExited {
				req := st.Conversations[old.ConversationID].Retirement
				if req == nil || req.Replacement != cause {
					t.Fatalf("missing durable retirement: %+v", req)
				}
			}
			// Rebuild the connector controller from durable storage with the old session still
			// listed. An exit receipt alone must not cause concurrent native writers.
			d.controller = nil
			r.retirement.State = "retired"
			if cause != schema.ManagerReplaceExited {
				if err = d.Sync(t.Context(), nil); !errors.Is(err, ErrControllerUncertain) || len(r.requests) != 0 {
					t.Fatalf("old owner still running: %v", err)
				}
			}
			r.rows[0].Status.Process = status.ProcessExited
			now := time.Now()
			d.now = func() time.Time { return now }
			r.unavailable = true
			for range 3 {
				err = d.Sync(t.Context(), func(DiscussionOutput) error { return nil })
				if errors.Is(err, ErrDiscussionActivationUnavailable) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if !errors.Is(err, ErrDiscussionActivationUnavailable) || r.launchCalls != 1 {
				t.Fatalf("missing unavailable pool diagnostic: %v calls %d", err, r.launchCalls)
			}
			view, err := d.Status()
			if err != nil || view.State != "waiting" || !strings.Contains(view.Diagnostic, "retry at") {
				t.Fatalf("missing retry status: %+v %v", view, err)
			}
			r.unavailable = false
			d.controller = nil
			for range 3 {
				if err = d.Sync(t.Context(), func(DiscussionOutput) error { return nil }); !errors.Is(err, ErrDiscussionActivationUnavailable) || r.launchCalls != 1 {
					t.Fatalf("restart bypassed rate limit: %v", err)
				}
			}
			now = now.Add(discussionReplacementInterval)
			for range 3 {
				err = d.Sync(t.Context(), func(DiscussionOutput) error { return nil })
				if d.controller != nil && d.controller.binding.SessionID != old.SessionID {
					break
				}
				if err != nil && !errors.Is(err, ErrControllerJournalResync) {
					t.Fatal(err)
				}
			}
			if d.controller == nil || d.controller.binding.SessionID == old.SessionID || len(r.requests) != 1 {
				t.Fatalf("replacement launches %d: %v", len(r.requests), err)
			}
			st, _ = s.Snapshot()
			recovered := st.Admissions[admissionKey(old.ConversationID, first.Sequence+1)]
			if !recovered.RecoveryOnly || recovered.RecoveryOutput == nil || recovered.RecoveryOutput.AdmissionID != "original" || len(st.Workers) != 1 || len(r.sent) != 1 {
				t.Fatalf("lost recovery attribution, duplicated work: %+v", recovered)
			}
			if err = c.Tick(t.Context()); !errors.Is(err, ErrStale) {
				t.Fatalf("old owner returned and regained authority: %v", err)
			}
			prompt := discussionHandoff(st, recovered, recovered.InputText, old.Provider, strings.Repeat("a", 64), true)
			if !strings.Contains(prompt, "android-original") || !strings.Contains(prompt, "machine/worker") || strings.Contains(prompt, "execute original exactly once") {
				t.Fatalf("incorrect recovery handoff: %q", prompt)
			}
		})
	}
}

func TestQueuedExplicitMailboxNoteRetainsItsAdmissionAcrossOwnerCustodyLoss(t *testing.T) {
	c, s, _, b := newControllerFixture(t)
	c.discussion = &DiscussionScope{VMProjectAccess: true}
	w := WorkerAssociation{ConversationID: b.ConversationID, Session: "machine/original-worker", SessionInstance: "original-instance", Parent: "old-owner", VMChild: true, Mailbox: true, JobID: "job", AttemptID: "attempt"}
	src := workerNoteMetadata{Session: w.Session, Instance: w.SessionInstance, Agent: "codex", NoteID: "complete", Kind: "complete", Sequence: 1, Text: "Original report is complete"}
	if _, err := c.queueWorkerNote(w, workerMessageInput{ID: src.NoteID, Kind: src.Kind, Text: src.Text}, src); err != nil {
		t.Fatal(err)
	}
	st, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	var old Wake
	for _, wake := range st.Wakes {
		old = wake
	}
	if err = s.noteControllerCustodyLost(b); err != nil {
		t.Fatal(err)
	}
	updated, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	note := updated.Wakes[key(b.ConversationID, old.ID)]
	a := updated.Admissions[admissionKey(b.ConversationID, note.Admission)]
	if note.ID != old.ID || note.OperationID != old.OperationID || note.RequestHash != old.RequestHash || note.RecoveryOnly || a.RecoveryOnly || updated.Workers[key(b.ConversationID, key(w.Session, w.SessionInstance))].RecoveryRequired || automaticRecoveryRequired(updated, b.ConversationID, a, nil) {
		t.Fatal("native owner failure downgraded the independently acknowledged worker completion")
	}
	// An explicit incident against this worker still fences its own attempts.
	if err = s.update(func(st *State) error { return quarantineControllerState(st, b, w.Session, w.SessionInstance) }); err != nil {
		t.Fatal(err)
	}
	updated, _ = s.Snapshot()
	if !automaticRecoveryRequired(updated, b.ConversationID, a, nil) {
		t.Fatal("worker-specific quarantine was bypassed")
	}
}
