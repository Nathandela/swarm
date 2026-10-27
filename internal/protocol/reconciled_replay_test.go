package protocol

import (
	"testing"
	"time"
)

type reviewRejectionBackend struct{ *durableComposerStub }

func (s *reviewRejectionBackend) SemanticOperation(local, id string) (SemanticMessageReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.records[id]
	rec.phase = composerPhaseFailed
	rec.outcome = []byte(`{"ok":false,"code":"input_busy","message":"native queue rejected"}`)
	s.records[id] = rec
	return SemanticMessageReceipt{State: "rejected"}, nil
}

func TestReviewReplayReturnsNewlyReconciledNativeRejection(t *testing.T) {
	stub := &reviewRejectionBackend{newDurableComposerStub()}
	stub.sendErr = ErrComposerOutcomeUnknown
	stub.sendCode = CodeOutcomeUnknown
	rc := rawDial(t, serveOwnerAPI(t, stub))
	rep := rc.hello(Version, []string{CapSemanticSend})
	id := semanticTestID(time.Now(), "a")
	frame := semanticTestFrame(rep, id, "uncertain original message")
	rc.writeControl(frame)
	if got := rc.readControl(); got.Op != OpError || got.ErrorCode != CodeOutcomeUnknown {
		t.Fatalf("initial send: %+v", got)
	}
	rc.writeControl(frame)
	if got := rc.readControl(); got.Op != OpError || got.ErrorCode != CodeInputBusy {
		t.Fatalf("exact reconciled rejection reported as unknown: %+v", got)
	}
	if _, sends, _ := stub.snapshot(id); sends != 1 {
		t.Fatalf("reconciliation replayed %d sends", sends)
	}
}
