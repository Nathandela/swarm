package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// composerIdempotencyStub combines the protocol test store's cached-outcome contract with a
// ComposerSender whose calls are observable. Composer sends are not self-idempotent: repeating
// the same operation can steer twice or submit the same PTY line twice, so a prepared/executing
// replay must be fenced rather than treated as safe to redrive.
type composerIdempotencyStub struct {
	*idempotentStub
	sendMu sync.Mutex
	sends  int
}

func newComposerIdempotencyStub() *composerIdempotencyStub {
	return &composerIdempotencyStub{idempotentStub: newIdempotentStub()}
}

func (s *composerIdempotencyStub) ClaimOperation(operationID, _, _ string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.ops[operationID]; ok {
		return true, nil
	}
	s.ops[operationID] = &idemRec{}
	return false, nil
}

func (s *composerIdempotencyStub) ComposerSend(_, _ string, _ ComposerSendReq) (ErrorCode, error) {
	s.sendMu.Lock()
	s.sends++
	s.sendMu.Unlock()
	return "", nil
}

func (s *composerIdempotencyStub) sendCount() int {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return s.sends
}

var _ ComposerSender = (*composerIdempotencyStub)(nil)
var _ IdempotentExecutor = (*composerIdempotencyStub)(nil)
var _ OperationClaimer = (*composerIdempotencyStub)(nil)

func TestComposerSend_ReplayedCompletedOperationReturnsCachedOKWithoutRedelivery(t *testing.T) {
	stub := newComposerIdempotencyStub()
	sock := serveRemoteAPI(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	body := &ComposerSendReq{Session: rep.EndpointID + "/sess1", ExpectedTurn: "old-context", Text: "only once"}
	frame := r6ComposerFrame(rep, "devA:01JCOMPOSERREPLAY0000000", body)

	rc.writeControl(frame)
	if got := rc.readControl(); got.Op != OpOK {
		t.Fatalf("first composer_send = op %q code %q error %q, want ok", got.Op, got.ErrorCode, got.Error)
	}
	rc.writeControl(frame)
	if got := rc.readControl(); got.Op != OpOK {
		t.Fatalf("replayed composer_send = op %q code %q error %q, want cached ok", got.Op, got.ErrorCode, got.Error)
	}
	if got := stub.sendCount(); got != 1 {
		t.Fatalf("ComposerSend executed %d times for one operation_id, want exactly once", got)
	}
}

func TestComposerSend_ReplayedPreparedOperationIsNotBlindlyRedelivered(t *testing.T) {
	stub := newComposerIdempotencyStub()
	const opID = "devA:01JCOMPOSERPENDING000000"
	stub.mu.Lock()
	stub.ops[opID] = &idemRec{} // durable crash-shaped prepared record, outcome unknown
	stub.mu.Unlock()

	sock := serveRemoteAPI(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	body := &ComposerSendReq{Session: rep.EndpointID + "/sess1", Text: "do not duplicate"}
	rc.writeControl(r6ComposerFrame(rep, opID, body))
	if got := rc.readControl(); got.Op != OpError {
		t.Fatalf("prepared composer replay = op %q, want error/outcome-unknown without redelivery", got.Op)
	}
	if got := stub.sendCount(); got != 0 {
		t.Fatalf("prepared composer replay executed %d sends, want zero", got)
	}
}

// durableComposerStub models the production prepared -> executing -> exact-terminal
// lifecycle and exposes the transactional callback used to place Begin at the actual send
// boundary. The older composerIdempotencyStub above deliberately keeps covering the legacy
// compatibility branch.
type durableComposerStub struct {
	*stubDaemon
	mu            sync.Mutex
	records       map[string]durableComposerRecord
	sends         int
	sendCode      ErrorCode
	sendErr       error
	commitErr     error
	claims        int
	commitEntered chan struct{}
	releaseCommit chan struct{}
	commitOnce    sync.Once
}

type durableComposerRecord struct {
	action      string
	session     string
	instance    string
	requestHash string
	phase       string
	outcome     []byte
}

func newDurableComposerStub() *durableComposerStub {
	return &durableComposerStub{stubDaemon: newStubDaemon(), records: make(map[string]durableComposerRecord)}
}

func (s *durableComposerStub) ClaimComposerOperation(op, action, session, instance, requestHash string) (string, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claims++
	if rec, ok := s.records[op]; ok {
		if rec.action != action || rec.session != session || rec.instance != instance || rec.requestHash != requestHash {
			return "", nil, errors.New("operation binding collision")
		}
		return rec.phase, append([]byte(nil), rec.outcome...), nil
	}
	rec := durableComposerRecord{action: action, session: session, instance: instance,
		requestHash: requestHash, phase: composerPhasePrepared}
	s.records[op] = rec
	return rec.phase, nil, nil
}

func (s *durableComposerStub) BeginComposerOperation(op string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[op]
	if !ok || rec.phase != composerPhasePrepared {
		return fmt.Errorf("operation %q is not prepared", op)
	}
	rec.phase = composerPhaseExecuting
	s.records[op] = rec
	return nil
}

func (s *durableComposerStub) CommitComposerOperation(op string, outcome []byte, success bool) error {
	if s.commitEntered != nil {
		s.commitOnce.Do(func() { close(s.commitEntered) })
		<-s.releaseCommit
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.commitErr != nil {
		return s.commitErr
	}
	rec := s.records[op]
	if success {
		rec.phase = composerPhaseCompleted
	} else {
		rec.phase = composerPhaseFailed
	}
	rec.outcome = append([]byte(nil), outcome...)
	s.records[op] = rec
	return nil
}

func TestComposerDurable_ConcurrentSameOperationWaitsThroughTerminalCommit(t *testing.T) {
	stub := newDurableComposerStub()
	stub.commitEntered = make(chan struct{})
	stub.releaseCommit = make(chan struct{})
	sock, srv := serveRemoteAPIStableSrv(t, stub, "mach1")
	rc1 := rawDial(t, sock)
	rep1 := rc1.hello(Version, []string{CapRemoteGateway})
	rc2 := rawDial(t, sock)
	rep2 := rc2.hello(Version, []string{CapRemoteGateway})
	const opID = "devA:01JCONCURRENTSAMEOP0000"
	body1 := &ComposerSendReq{Session: rep1.EndpointID + "/sess1", Text: "exactly once"}
	body2 := &ComposerSendReq{Session: rep2.EndpointID + "/sess1", Text: "exactly once"}
	frame1 := r6ComposerFrame(rep1, opID, body1)
	frame2 := r6ComposerFrame(rep2, opID, body2)

	firstReply := make(chan Control, 1)
	go func() {
		rc1.writeControl(frame1)
		firstReply <- rc1.readControl()
	}()
	select {
	case <-stub.commitEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("first attempt never reached terminal commit")
	}
	secondReply := make(chan Control, 1)
	go func() {
		rc2.writeControl(frame2)
		secondReply <- rc2.readControl()
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		srv.composerOpMu.Lock()
		entry := srv.composerOpLocks[opID]
		refs := 0
		if entry != nil {
			refs = entry.refs
		}
		srv.composerOpMu.Unlock()
		if refs == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("same-op replay never joined singleflight; refs=%d", refs)
		}
		time.Sleep(time.Millisecond)
	}
	_, sends, claims := stub.snapshot(opID)
	if sends != 1 || claims != 1 {
		t.Fatalf("while first commit blocked: sends %d claims %d, want 1/1", sends, claims)
	}

	close(stub.releaseCommit)
	for i, reply := range []<-chan Control{firstReply, secondReply} {
		select {
		case got := <-reply:
			if got.Op != OpOK {
				t.Fatalf("reply %d = %+v, want OK", i+1, got)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("reply %d remained blocked", i+1)
		}
	}
	_, sends, claims = stub.snapshot(opID)
	if sends != 1 || claims != 2 {
		t.Fatalf("terminal replay state: sends %d claims %d, want 1/2", sends, claims)
	}
}

func (s *durableComposerStub) ComposerSend(_, _ string, _ ComposerSendReq) (ErrorCode, error) {
	panic("durable composer handler bypassed transactional boundary")
}

func (s *durableComposerStub) ComposerSendTransactional(_, _ string, _ ComposerSendReq, begin func() error) (ErrorCode, error) {
	if err := begin(); err != nil {
		return CodeOutcomeUnknown, fmt.Errorf("%w: %v", ErrComposerOutcomeUnknown, err)
	}
	s.mu.Lock()
	s.sends++
	code, err := s.sendCode, s.sendErr
	s.mu.Unlock()
	return code, err
}

func (s *durableComposerStub) snapshot(op string) (durableComposerRecord, int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.records[op], s.sends, s.claims
}

var _ ComposerSender = (*durableComposerStub)(nil)
var _ ComposerOperationExecutor = (*durableComposerStub)(nil)
var _ TransactionalComposerSender = (*durableComposerStub)(nil)

func TestComposerDurable_ReplayReturnsExactTerminalOutcomeWithoutRedelivery(t *testing.T) {
	stub := newDurableComposerStub()
	stub.sendCode = CodeInputBusy
	stub.sendErr = errors.New("nothing was typed because the input line was busy")
	sock := serveRemoteAPI(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	const opID = "devA:01JDURABLEEXACTREPLAY000"
	frame := r6ComposerFrame(rep, opID, &ComposerSendReq{Session: rep.EndpointID + "/sess1", Text: "one message"})

	rc.writeControl(frame)
	if got := rc.readControl(); got.Op != OpError || got.ErrorCode != CodeInputBusy {
		t.Fatalf("first durable refusal = %+v, want input_busy", got)
	}
	rc.writeControl(frame)
	if got := rc.readControl(); got.Op != OpError || got.ErrorCode != CodeInputBusy {
		t.Fatalf("replayed durable refusal = %+v, want exact input_busy", got)
	}
	rec, sends, _ := stub.snapshot(opID)
	if sends != 1 || rec.phase != composerPhaseFailed {
		t.Fatalf("durable state = sends %d phase %q, want one send and failed", sends, rec.phase)
	}
	var outcome composerCachedOutcome
	if err := json.Unmarshal(rec.outcome, &outcome); err != nil || outcome.Code != CodeInputBusy {
		t.Fatalf("cached exact outcome = %+v (err %v), want input_busy", outcome, err)
	}
}

// TestComposerDurable_OperationIDCannotReplayDifferentBodyAsCachedSuccess is the other half
// of exact replay: an idempotency key identifies one request, not merely one session
// incarnation. Returning the first request's cached OK for different text is a false success
// for words that were never delivered.
func TestComposerDurable_OperationIDCannotReplayDifferentBodyAsCachedSuccess(t *testing.T) {
	stub := newDurableComposerStub()
	sock := serveRemoteAPI(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	const opID = "devA:01JDURABLEBODYCOLLIDE00"

	rc.writeControl(r6ComposerFrame(rep, opID, &ComposerSendReq{
		Session: rep.EndpointID + "/sess1", Text: "the request that really landed",
	}))
	if got := rc.readControl(); got.Op != OpOK {
		t.Fatalf("first composer_send = %+v, want OK", got)
	}
	rc.writeControl(r6ComposerFrame(rep, opID, &ComposerSendReq{
		Session: rep.EndpointID + "/sess1", Text: "different words that never landed",
	}))
	if got := rc.readControl(); got.Op != OpError {
		t.Fatalf("same operation_id with a different body = %+v, want binding collision; cached OK would falsely claim the second text was delivered", got)
	}
	_, sends, _ := stub.snapshot(opID)
	if sends != 1 {
		t.Fatalf("same operation_id with different body delivered %d times, want exactly one", sends)
	}
}

func TestComposerDurable_ExecutingReplayIsFencedWithoutDelivery(t *testing.T) {
	stub := newDurableComposerStub()
	const opID = "devA:01JDURABLEEXECUTING0000"
	body := &ComposerSendReq{Session: "sess1", SessionInstance: "test-session-instance", Text: "never duplicate"}
	sock := serveRemoteAPI(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	body.Session = rep.EndpointID + "/sess1"
	stub.records[opID] = durableComposerRecord{
		action: ActionComposerSend, session: "sess1", instance: "test-session-instance",
		requestHash: composerRequestHash(body), phase: composerPhaseExecuting,
	}
	rc.writeControl(r6ComposerFrame(rep, opID, body))
	if got := rc.readControl(); got.Op != OpError || got.ErrorCode != CodeOutcomeUnknown {
		t.Fatalf("executing replay = %+v, want outcome_unknown", got)
	}
	_, sends, _ := stub.snapshot(opID)
	if sends != 0 {
		t.Fatalf("executing replay delivered %d times, want zero", sends)
	}
}

func TestComposerDurable_TerminalCommitFailureNeverRepliesOK(t *testing.T) {
	stub := newDurableComposerStub()
	stub.commitErr = errors.New("durable operation log unavailable")
	sock := serveRemoteAPI(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	rc.writeControl(r6ComposerFrame(rep, "devA:01JDURABLECOMMITFAIL000", &ComposerSendReq{
		Session: rep.EndpointID + "/sess1", Text: "provider accepted this",
	}))
	if got := rc.readControl(); got.Op == OpOK || got.ErrorCode != CodeOutcomeUnknown {
		t.Fatalf("commit failure = %+v, want outcome_unknown and never OK", got)
	}
}

func TestComposerDurable_InvalidForeignSessionIsRefusedBeforeClaim(t *testing.T) {
	stub := newDurableComposerStub()
	sock := serveRemoteAPI(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	rc.writeControl(r6ComposerFrame(rep, "devA:01JFOREIGNBEFORECLAIM00", &ComposerSendReq{
		Session: "foreign-endpoint/sess1", Text: "must not reach durable state",
	}))
	if got := rc.readControl(); got.Op != OpError {
		t.Fatalf("foreign session = %+v, want refusal", got)
	}
	_, sends, claims := stub.snapshot("devA:01JFOREIGNBEFORECLAIM00")
	if claims != 0 || sends != 0 {
		t.Fatalf("foreign session reached durable/send seam: claims %d sends %d", claims, sends)
	}
}
