package skeleton

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/idempotency"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

func TestOpenCodeSupervisionUsesNativeAdmissionOnce(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	req := protocol.SendInputReq{Text: "[swarm child attention]", Submit: true}
	for i := 0; i < 2; i++ {
		if err := f.d.sendSupervision(f.binding.WorkerID, req, "semantic:supervision:exact-event"); err != nil {
			t.Fatal(err)
		}
	}
	rec, ok := f.d.core.ComposerOperation("semantic:supervision:exact-event")
	if !ok || rec.Phase != idempotency.PhaseCompleted || len(rec.Evidence) == 0 || f.calls.Load() != 1 {
		t.Fatalf("notification lacked durable native admission: %+v calls=%d", rec, f.calls.Load())
	}
	var intent openCodeSendIntent
	if json.Unmarshal(rec.Request, &intent) != nil || intent.Source != "supervisor" {
		t.Fatal("native notification lost machine provenance")
	}
}

func TestSupervisionAdvancesAttemptOnlyOnProvenNoEffect(t *testing.T) {
	f := newSupFakes(plainMeta("parent", stReady))
	f.setSendErr(errors.New("outcome unknown"))
	s := &supervisor{endpointID: supEndpoint, dir: t.TempDir(), get: f.get, controlled: f.isControlled, records: map[string]*supervisionRecord{}}
	var ids []string
	s.send = func(local string, req protocol.SendInputReq, operationID string) error {
		ids = append(ids, operationID)
		return f.send(local, req, operationID)
	}
	ev := supervisionEvent{Seq: 7, Group: status.GroupReadyForReview}
	s.records["kid"] = &supervisionRecord{Child: "kid", Source: "parent", Pending: &ev}
	s.deliver("kid", "parent", ev)
	s.deliver("kid", "parent", ev)
	if ids[0] != ids[1] || s.records["kid"].Pending.Attempt != 0 {
		t.Fatal("uncertain notification advanced to a new native operation")
	}
	f.setSendErr(errSupervisionRetry)
	s.deliver("kid", "parent", ev)
	raw, err := os.ReadFile(filepath.Join(s.dir, "kid.json"))
	var durable supervisionRecord
	if err != nil || json.Unmarshal(raw, &durable) != nil || durable.Pending == nil || durable.Pending.Attempt != 1 {
		t.Fatalf("safe retry attempt was not persisted: %s, %v", raw, err)
	}
	f.setSendErr(nil)
	s.deliver("kid", "parent", *durable.Pending)
	if ids[3] == ids[0] || s.records["kid"].Pending != nil {
		t.Fatal("definitely rejected notification could not make one new admitted attempt")
	}
}

func TestOpenCodeSupervisionIdleWithoutRequestedInputOutcomeIsNotReady(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	f.d.openCode.mu.Lock()
	observation := f.d.openCode.sessions[f.binding.WorkerID]
	view := cloneOpenCodeView(observation.view)
	view.Current = &OpenCodeInput{MessageID: "msg_unresolved", Classified: true}
	f.d.openCode.mu.Unlock()
	f.d.publishOpenCodeView(f.binding.WorkerID, observation, view)
	m, ok := f.d.supervisionMeta(f.binding.WorkerID)
	if !ok || m.Status.Turn != status.TurnUnknown {
		t.Fatal("native idle manufactured ready-for-review before the requested input completed")
	}
	view.Current = nil
	f.d.publishOpenCodeView(f.binding.WorkerID, observation, view)
	m, ok = f.d.supervisionMeta(f.binding.WorkerID)
	if !ok || m.Status.Turn != status.TurnIdle {
		t.Fatal("qualified idle did not restore notification eligibility")
	}
}

func TestOpenCodeSupervisionReconcilesUncertainAdmissionWithoutRepost(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	f.dropReply.Store(true)
	req := protocol.SendInputReq{Text: "[swarm child attention]", Submit: true}
	for i := 0; i < 2; i++ {
		if err := f.d.sendSupervision(f.binding.WorkerID, req, "supervision:lost-reply"); err == nil || errors.Is(err, errSupervisionRetry) {
			t.Fatalf("unconfirmed admission authorized a new attempt: %v", err)
		}
	}
	f.rowAvailable.Store(true)
	if err := f.d.sendSupervision(f.binding.WorkerID, req, "supervision:lost-reply"); err != nil {
		t.Fatalf("exact native row did not reconcile the old notification: %v", err)
	}
	if f.calls.Load() != 1 {
		t.Fatalf("notification was posted %d times", f.calls.Load())
	}
}

func TestOpenCodeSupervisionRetriesOnlyBeforeNativeExecution(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	req := protocol.SendInputReq{Text: "[swarm child attention]", Submit: true}
	if err := f.d.withOwnerSessionEnd(f.binding.WorkerID, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := f.d.sendSupervision(f.binding.WorkerID, req, "supervision:ending"); err == nil {
		t.Fatal("notification crossed owner end")
	}
	rec, ok := f.d.core.ComposerOperation("supervision:ending")
	if !ok || rec.Phase != idempotency.PhasePrepared || f.calls.Load() != 0 {
		t.Fatalf("pre-write refusal became an uncertain native execution: %+v", rec)
	}
}
