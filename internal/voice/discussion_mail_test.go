package voice

import (
	"context"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/workermail"
)

func TestWorkerMailboxIngestionOrdersNotesAndReturnsHeldTurnAnswer(t *testing.T) {
	for _, agent := range []string{"codex", "claude"} {
		t.Run(agent, func(t *testing.T) {
			c, s, _, binding := newControllerFixture(t)
			c.managerSettingsDir = t.TempDir()
			c.discussion = &DiscussionScope{VMProjectAccess: true}
			endpoint, _, _ := protocol.ParseID(binding.SessionID)
			b := workermail.Binding{Discussion: binding.ConversationID, Parent: "initial-manager", Session: endpoint + "/worker", Instance: "first", Attempt: "vm-child-launch-1", Project: t.TempDir(), Agent: agent}
			lost, err := workermail.Submit(c.managerSettingsDir, b, "early", "needs-input", "which fixture?", true)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = workermail.Submit(c.managerSettingsDir, b, "late", "complete", "finished 17", true); err != nil {
				t.Fatal(err)
			}
			st, err := s.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			// The first receipt can be lost for longer than the native semantic replay window.
			if _, err = workermail.Submit(c.managerSettingsDir, b, "early", "needs-input", "which fixture?", false); err != nil {
				t.Fatal(err)
			}
			changed, err := c.ingestWorkerMail(context.Background(), st)
			if err != nil || !changed {
				t.Fatalf("ingest: changed=%v %v", changed, err)
			}
			st, err = s.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if len(st.Workers) != 1 || len(st.Admissions) != 2 {
				t.Fatalf("expected one child and two admissions: %d %d", len(st.Workers), len(st.Admissions))
			}
			first := st.Admissions[admissionKey(b.Discussion, 1)]
			second := st.Admissions[admissionKey(b.Discussion, 2)]
			if first.SourceID != "wake:worker-message-"+hashValue([]string{b.Session, b.Instance, b.Attempt, "early"}) || second.SourceID != "wake:worker-message-"+hashValue([]string{b.Session, b.Instance, b.Attempt, "late"}) {
				t.Fatal("notes admitted out of order")
			}
			completion := st.Wakes[key(b.Discussion, strings.TrimPrefix(second.SourceID, "wake:"))]
			if !strings.Contains(string(completion.Request), "publish with final=true during this turn") {
				t.Fatal("completed worker note omitted the Discord publication step")
			}
			first.Phase, first.NativeInputID, first.NativeTurnID = "running", "input", "turn"
			if err = s.update(func(st *State) error {
				putRecord(st, "admissions", &st.Admissions, admissionKey(b.Discussion, 1), first)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err = c.replyToDiscussionWorker(context.Background(), first, "fixture 17"); err != nil {
				t.Fatal(err)
			}
			got, err := workermail.Lookup(c.managerSettingsDir, b, lost.ID)
			if err != nil || got.Answer != "fixture 17" {
				t.Fatalf("durable answer %+v %v", got, err)
			}
			if _, err = c.ingestWorkerMail(context.Background(), st); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestSupersededMailboxNoteDoesNotBlockOtherAttempts(t *testing.T) {
	c, s, _, binding := newControllerFixture(t)
	c.managerSettingsDir = t.TempDir()
	c.discussion = &DiscussionScope{VMProjectAccess: true}
	endpoint, _, _ := protocol.ParseID(binding.SessionID)
	b := workermail.Binding{Discussion: binding.ConversationID, Parent: "manager", Session: endpoint + "/old", Instance: "one", Attempt: "old", Project: t.TempDir(), Agent: "codex"}
	_, err := workermail.Submit(c.managerSettingsDir, b, "final", "complete", "old final", true)
	if err != nil {
		t.Fatal(err)
	}
	w := WorkerAssociation{VMChild: true, Mailbox: true, ConversationID: b.Discussion, Session: b.Session, SessionInstance: b.Instance, Parent: b.Parent, AttemptID: b.Attempt, RecoveryRequired: true}
	if err = s.update(func(st *State) error {
		putRecord(st, "workers", &st.Workers, key(b.Discussion, key(b.Session, b.Instance)), w)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	fresh := b
	fresh.Session = endpoint + "/new"
	fresh.Attempt = "new"
	if _, err = workermail.Submit(c.managerSettingsDir, fresh, "final", "complete", "new final", true); err != nil {
		t.Fatal(err)
	}
	st, _ := s.Snapshot()
	if _, err = c.ingestWorkerMail(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	old, _ := workermail.Lookup(c.managerSettingsDir, b, "final")
	live, _ := workermail.Lookup(c.managerSettingsDir, fresh, "final")
	if old.State != "superseded" || live.State != "ingested" {
		t.Fatalf("receipts %s %s", old.State, live.State)
	}
	st, _ = s.Snapshot()
	if len(st.Admissions) != 1 {
		t.Fatal("superseded note reached manager")
	}
}
