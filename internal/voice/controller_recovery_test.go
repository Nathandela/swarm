package voice

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"syscall"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func TestControllerExplicitUncertaintyAckAllowsNewHumanWorkWithoutRetiringGuards(t *testing.T) {
	c, s, r, b := provenControllerFixture(t)
	c.workers = &controllerWorkerFixture{launchErr: errors.New("lost reply"), outcome: schema.OperationOutcomeView{State: schema.OutcomeUnknown}}
	if _, err := c.ManagerCall(context.Background(), ManagerToolCall{Name: "swarm_create", Arguments: json.RawMessage(`{"action_id":"task1","agent":"codex","cwd":"/tmp","prompt":"uncertain task"}`)}); !errors.Is(err, ErrControllerUncertain) {
		t.Fatal(err)
	}
	if _, err := c.SubmitText(Utterance{ID: "unrelated", DeviceID: "device", VoiceSessionID: "voice", MediaEpoch: 1}, "new unrelated human work"); err != nil {
		t.Fatal(err)
	}
	r.status.Turn.State = "completed"
	r.status.ReadyForInput = true
	if err := c.Tick(context.Background()); !errors.Is(err, ErrControllerUncertain) {
		t.Fatal(err)
	}
	st, _ := s.Snapshot()
	old := st.Admissions[admissionKey(b.ConversationID, 1)]
	ack := InputRecoveryAck{Admission: 1, OperationID: old.OperationID, NativeTurnID: old.NativeTurnID, ExternalInput: 0}
	stale := ack
	stale.NativeTurnID = "wrong"
	if err := c.AcknowledgeUncertainty(context.Background(), stale); !errors.Is(err, ErrStale) {
		t.Fatal("stale acknowledgement authorized recovery")
	}
	if err := c.AcknowledgeUncertainty(context.Background(), ack); err != nil {
		t.Fatal(err)
	}
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, _ = s.Snapshot()
	if len(r.sent) != 2 || st.Conversations[b.ConversationID].RetiredThrough != 0 || !st.Admissions[admissionKey(b.ConversationID, 1)].SettledAt.IsZero() || !st.Admissions[admissionKey(b.ConversationID, 1)].RecoveryAcknowledged {
		t.Fatal("explicit continuation lost the old unresolved authority or blocked unrelated new work")
	}
	for _, a := range st.Actions {
		if a.State != "executing" {
			t.Fatal("acknowledgement fabricated an old effect outcome")
		}
	}
}
func TestControllerTrustedRecoveryInputCanReportAndAskButCannotMutate(t *testing.T) {
	c, s, r, b := newControllerFixture(t)
	trackControllerFixtureWorker(t, s, b)
	wake, err := c.AcceptWorkerEvent("machine/worker", "instance", Event{ID: "lost", Cursor: JournalCursor{Binding: "journal", Sequence: 1}, RecoveryOnly: true, Metadata: json.RawMessage(`{"recovery_only":false,"state":"lost"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(r.sent[0].Text, recoveryInputNotice) {
		t.Fatal("recovery context was not identified to the native manager")
	}
	nonce := r.sent[0].ManagerAdmission.Nonce
	r.status.Turn = protocol.ManagerNativeTurn{State: "running", NativeInputID: nonce, NativeTurnID: "recovery"}
	r.status.ReadyForInput = false
	r.proof = protocol.ManagerCallProof{Main: true, NativeInputID: nonce, NativeTurnID: "recovery", NativeCallID: "call"}
	c.workers = &controllerWorkerFixture{}
	if _, err = c.ManagerCall(context.Background(), ManagerToolCall{Name: "swarm_create", Arguments: json.RawMessage(`{"action_id":"retry","agent":"codex","cwd":"/tmp","prompt":"rerun uncertain work"}`)}); !errors.Is(err, ErrControllerUncertain) {
		t.Fatal("automatic recovery reran work", err)
	}
	raw, err := c.ManagerCall(context.Background(), ManagerToolCall{Name: "ask", Arguments: json.RawMessage(`{"request_id":"continue","task_id":"uncertain","question":"Continue despite the unknown prior effect?"}`)})
	if err != nil {
		t.Fatal(err)
	}
	var question Request
	if err = json.Unmarshal(raw, &question); err != nil {
		t.Fatal(err)
	}
	answer, err := c.AnswerQuestion(question.ID, question.Hash, "human-continuation", json.RawMessage(`"Yes, continue"`))
	if err != nil {
		t.Fatal(err)
	}
	st, _ := s.Snapshot()
	if !st.Admissions[admissionKey(b.ConversationID, wake.Admission)].RecoveryOnly || st.Admissions[admissionKey(b.ConversationID, answer.Sequence)].RecoveryOnly || len(st.Actions) != 0 {
		t.Fatal("model arguments changed recovery authority, or explicit human continuation stayed restricted")
	}
}
func TestControllerRecoverySnapshotCheckpointWakeAndAdmissionAreAtomic(t *testing.T) {
	c, s, _, b := newControllerFixture(t)
	cursor := JournalCursor{Binding: "d21dfd1a-72ed-44b2-9f38-e050e6630546", Sequence: 0}
	snapshot := JournalSnapshot{Metadata: json.RawMessage(`{"history_available":false,"reason":"initial_snapshot"}`)}
	s.beforeCommit = func() error { return syscall.ENOSPC }
	if _, err := c.AcceptRecoverySnapshot(cursor, snapshot); !errors.Is(err, syscall.ENOSPC) {
		t.Fatal(err)
	}
	s.beforeCommit = nil
	st, _ := s.Snapshot()
	if len(st.Journal) != 0 || len(st.Wakes) != 0 || len(st.Admissions) != 0 {
		t.Fatal("snapshot checkpoint escaped failed recovery acceptance")
	}
	first, err := c.AcceptRecoverySnapshot(cursor, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	repeat, err := c.AcceptRecoverySnapshot(cursor, snapshot)
	if err != nil || repeat.Admission != first.Admission || repeat.OperationID != first.OperationID {
		t.Fatal("snapshot retry allocated another model wake", err)
	}
	st, _ = s.Snapshot()
	if st.Journal[b.ConversationID] != cursor || !st.Admissions[admissionKey(b.ConversationID, first.Admission)].RecoveryOnly || len(st.Events) != 1 {
		t.Fatal("initial/gap snapshot was promoted to ordinary task authorization")
	}
}
