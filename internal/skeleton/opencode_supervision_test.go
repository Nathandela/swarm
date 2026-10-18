package skeleton

import (
	"errors"
	"testing"

	"github.com/Nathandela/swarm/internal/idempotency"
	"github.com/Nathandela/swarm/internal/protocol"
)

func TestOpenCodeSupervisionUsesNativeAdmissionOnce(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	req := protocol.SendInputReq{Text: "[swarm child attention]", Submit: true}
	for i := 0; i < 2; i++ {
		if err := f.d.sendSupervision(f.binding.WorkerID, req, "supervision:exact-event"); err != nil {
			t.Fatal(err)
		}
	}
	rec, ok := f.d.core.ComposerOperation("supervision:exact-event")
	if !ok || rec.Phase != idempotency.PhaseCompleted || len(rec.Evidence) == 0 || f.calls.Load() != 1 {
		t.Fatalf("notification lacked durable native admission: %+v calls=%d", rec, f.calls.Load())
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
