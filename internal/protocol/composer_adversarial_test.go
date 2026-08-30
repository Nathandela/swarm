package protocol

import (
	"errors"
	"sync"
	"testing"
)

// adversarialComposerBackend keeps the existing durable-stub semantics observable while
// letting each test inject the two outcomes the composer protocol must preserve: a failed
// terminal commit, and a coded refusal whose reply was lost and therefore has to be replayed.
type adversarialComposerBackend struct {
	*idempotentStub
	mu          sync.Mutex
	sends       int
	sendCode    ErrorCode
	sendErr     error
	commitError error
}

func newAdversarialComposerBackend() *adversarialComposerBackend {
	return &adversarialComposerBackend{idempotentStub: newIdempotentStub()}
}

func (s *adversarialComposerBackend) ClaimOperation(operationID, _, _ string) (bool, error) {
	s.idempotentStub.mu.Lock()
	defer s.idempotentStub.mu.Unlock()
	if _, ok := s.ops[operationID]; ok {
		return true, nil
	}
	s.ops[operationID] = &idemRec{}
	return false, nil
}

func (s *adversarialComposerBackend) ComposerSend(_, _ string, _ ComposerSendReq) (ErrorCode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sends++
	return s.sendCode, s.sendErr
}

func (s *adversarialComposerBackend) CommitIdempotentOp(operationID string, ok bool) error {
	if s.commitError != nil {
		return s.commitError
	}
	return s.idempotentStub.CommitIdempotentOp(operationID, ok)
}

var _ ComposerSender = (*adversarialComposerBackend)(nil)
var _ OperationClaimer = (*adversarialComposerBackend)(nil)
var _ IdempotentExecutor = (*adversarialComposerBackend)(nil)

func TestComposerIdempotency_DoesNotReplyOKWhenTerminalCommitFailed(t *testing.T) {
	stub := newAdversarialComposerBackend()
	stub.commitError = errors.New("durable operation log unavailable")
	sock := serveRemoteAPI(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	body := &ComposerSendReq{Session: rep.EndpointID + "/sess1", Text: "accepted by provider"}

	rc.writeControl(r6ComposerFrame(rep, "devA:01JCOMMITFAIL00000000000", body))
	if got := rc.readControl(); got.Op == OpOK {
		t.Fatalf("composer_send replied OK after its durable terminal commit failed: %+v; a lost reply/retry would now resolve as outcome_unknown", got)
	}
}

func TestComposerIdempotency_ReplayPreservesOriginalCodedRefusal(t *testing.T) {
	stub := newAdversarialComposerBackend()
	stub.sendCode = CodeInputBusy
	stub.sendErr = errors.New("nothing was typed because the input line was busy")
	sock := serveRemoteAPI(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	body := &ComposerSendReq{Session: rep.EndpointID + "/sess1", Text: "retryable words"}
	frame := r6ComposerFrame(rep, "devA:01JCODEDREPLAY000000000", body)

	rc.writeControl(frame)
	if got := rc.readControl(); got.Op != OpError || got.ErrorCode != CodeInputBusy {
		t.Fatalf("first coded refusal = %+v, want input_busy", got)
	}
	rc.writeControl(frame)
	if got := rc.readControl(); got.Op != OpError || got.ErrorCode != CodeInputBusy {
		t.Fatalf("replayed coded refusal = %+v, want the original input_busy outcome", got)
	}
}
