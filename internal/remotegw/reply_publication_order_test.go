package remotegw

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// publicationMailbox can stop the first append before it reaches relay custody. That
// makes the otherwise microscopic window between reply-seq allocation and append fully
// deterministic: a concurrent reconcile either waits behind the reply publication or
// overtakes it and publishes a ceiling that makes the reply stale on the phone.
type publicationMailbox struct {
	mu sync.Mutex

	calls              int
	firstEntered       chan struct{}
	releaseFirst       chan struct{}
	failFirst          error
	storeFirstThenFail bool
	attempts           [][]byte
	stored             [][]byte
	firstEnterOnce     sync.Once
}

func newPublicationMailbox() *publicationMailbox {
	return &publicationMailbox{
		firstEntered: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
}

func (m *publicationMailbox) MailboxRead(context.Context, uint64) ([]relay.Item, error) {
	return nil, nil
}

func (m *publicationMailbox) MailboxWait(context.Context, uint64) ([]relay.Item, bool, error) {
	return nil, false, nil
}

func (m *publicationMailbox) MailboxAck(context.Context, uint64) error { return nil }

func (m *publicationMailbox) MailboxAppend(ctx context.Context, _ string, env []byte) (uint64, error) {
	raw := append([]byte(nil), env...)
	m.mu.Lock()
	m.calls++
	call := m.calls
	m.attempts = append(m.attempts, raw)
	m.mu.Unlock()
	if call == 1 {
		m.firstEnterOnce.Do(func() { close(m.firstEntered) })
		select {
		case <-m.releaseFirst:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
		if m.failFirst != nil {
			if m.storeFirstThenFail {
				m.mu.Lock()
				m.stored = append(m.stored, raw)
				m.mu.Unlock()
			}
			return 0, m.failFirst
		}
	}
	m.mu.Lock()
	m.stored = append(m.stored, raw)
	cursor := uint64(len(m.stored))
	m.mu.Unlock()
	return cursor, nil
}

func (m *publicationMailbox) snapshot() (attempts, stored [][]byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, raw := range m.attempts {
		attempts = append(attempts, append([]byte(nil), raw...))
	}
	for _, raw := range m.stored {
		stored = append(stored, append([]byte(nil), raw...))
	}
	return attempts, stored
}

func publicationService(t *testing.T, mb *publicationMailbox) (*Service, crypto.ContentKey) {
	t.Helper()
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 91)
	}
	return NewService(ServiceConfig{
		DaemonSocket: "/nonexistent/remote.sock",
		Relay:        mb,
		PhoneTarget:  "phone",
		Machine:      "machine",
		Key:          key,
		EpochID:      7,
		SenderKeyID:  [8]byte{9, 8, 7, 6, 5, 4, 3, 2},
	}), key
}

func publicationFrame(t *testing.T, key crypto.ContentKey, raw []byte) (kind, operation string, seq, replyCeiling uint64) {
	t.Helper()
	env, err := crypto.ParseEnvelope(raw)
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	plain, err := crypto.OpenMailbox(key, env)
	if err != nil {
		t.Fatalf("OpenMailbox: %v", err)
	}
	var frame struct {
		Kind        string `json:"kind"`
		OperationID string `json:"operation_id"`
		protocol.ReconcileRecord
	}
	if err := json.Unmarshal(plain, &frame); err != nil {
		t.Fatalf("decode publication: %v", err)
	}
	return frame.Kind, frame.OperationID, env.Header.Seq, frame.ReplyCeiling
}

func assertStillBlocked(t *testing.T, done <-chan error, what string) {
	t.Helper()
	select {
	case err := <-done:
		t.Fatalf("%s overtook the publication fence: %v", what, err)
	case <-time.After(75 * time.Millisecond):
	}
}

func awaitFirstAppend(t *testing.T, mb *publicationMailbox) {
	t.Helper()
	select {
	case <-mb.firstEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first append to enter")
	}
}

func awaitPublication(t *testing.T, done <-chan error, what string) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestService_ReplyAppendPrecedesAReconcilePublishingItsSequence(t *testing.T) {
	mb := newPublicationMailbox()
	svc, key := publicationService(t, mb)
	replyDone := make(chan error, 1)
	go func() {
		replyDone <- svc.bridge.sealReply(context.Background(), protocol.Control{
			Op: protocol.OpError, OperationID: "op-busy", ErrorCode: protocol.CodeInputBusy,
		})
	}()
	awaitFirstAppend(t, mb) // reply seq 1 is issued, but its append has not reached custody

	reconcileDone := make(chan error, 1)
	go func() { reconcileDone <- svc.sink.Reconcile() }()
	assertStillBlocked(t, reconcileDone, "reconcile")
	close(mb.releaseFirst)
	awaitPublication(t, replyDone, "reply")
	awaitPublication(t, reconcileDone, "reconcile")

	_, stored := mb.snapshot()
	if len(stored) != 2 {
		t.Fatalf("stored publications = %d, want reply then reconcile", len(stored))
	}
	kind0, op0, seq0, _ := publicationFrame(t, key, stored[0])
	kind1, _, _, ceiling1 := publicationFrame(t, key, stored[1])
	if kind0 != kindCommandReply || op0 != "op-busy" || seq0 != 1 {
		t.Fatalf("first publication = kind %q op %q seq %d, want reply op-busy at seq 1", kind0, op0, seq0)
	}
	if kind1 != kindReconcile || ceiling1 != seq0 {
		t.Fatalf("second publication = kind %q reply ceiling %d, want reconcile after reply seq %d", kind1, ceiling1, seq0)
	}
}

func TestService_ReconcileAppendPrecedesAnyReplyAboveItsPublishedCeiling(t *testing.T) {
	mb := newPublicationMailbox()
	svc, key := publicationService(t, mb)
	reconcileDone := make(chan error, 1)
	go func() { reconcileDone <- svc.sink.Reconcile() }()
	awaitFirstAppend(t, mb) // reconcile has read the old reply ceiling but is not stored yet

	replyDone := make(chan error, 1)
	go func() {
		replyDone <- svc.bridge.sealReply(context.Background(), protocol.Control{
			Op: protocol.OpOK, OperationID: "op-after-reconcile",
		})
	}()
	assertStillBlocked(t, replyDone, "reply")
	close(mb.releaseFirst)
	awaitPublication(t, reconcileDone, "reconcile")
	awaitPublication(t, replyDone, "reply")

	_, stored := mb.snapshot()
	if len(stored) != 2 {
		t.Fatalf("stored publications = %d, want reconcile then reply", len(stored))
	}
	kind0, _, _, ceiling0 := publicationFrame(t, key, stored[0])
	kind1, op1, seq1, _ := publicationFrame(t, key, stored[1])
	if kind0 != kindReconcile || ceiling0 != 0 {
		t.Fatalf("first publication = kind %q reply ceiling %d, want reconcile at old ceiling 0", kind0, ceiling0)
	}
	if kind1 != kindCommandReply || op1 != "op-after-reconcile" || seq1 <= ceiling0 {
		t.Fatalf("second publication = kind %q op %q seq %d, want reply strictly above ceiling %d", kind1, op1, seq1, ceiling0)
	}
}

func TestService_ReconcileRedrivesDeliveryUnknownReplyBeforePublishingItsCeiling(t *testing.T) {
	mb := newPublicationMailbox()
	mb.failFirst = errors.New("reply delivery unknown")
	svc, key := publicationService(t, mb)

	replyDone := make(chan error, 1)
	go func() {
		replyDone <- svc.bridge.sealReply(context.Background(), protocol.Control{
			Op: protocol.OpError, OperationID: "op-unknown", ErrorCode: protocol.CodeInputBusy,
		})
	}()
	awaitFirstAppend(t, mb)
	close(mb.releaseFirst)
	if err := <-replyDone; err == nil {
		t.Fatal("first reply append succeeded; want delivery-unknown failure")
	}
	if err := svc.sink.Reconcile(); err != nil {
		t.Fatalf("reconcile must redrive the pending reply itself: %v", err)
	}
	// The retained command can now be consumed without appending the reply again: the
	// reconcile path already put its exact bytes in custody before publishing the ceiling.
	if ok, err := svc.bridge.redrivePendingReply(context.Background(), "op-unknown"); err != nil || !ok {
		t.Fatalf("bridge acknowledgement after reconcile redrive = (%v, %v), want delivered", ok, err)
	}

	attempts, stored := mb.snapshot()
	if len(attempts) != 3 || !bytes.Equal(attempts[0], attempts[1]) {
		t.Fatalf("reply redrive was not byte-identical: attempts=%d equal=%v", len(attempts), len(attempts) >= 2 && bytes.Equal(attempts[0], attempts[1]))
	}
	if len(stored) != 2 {
		t.Fatalf("stored publications after recovery = %d, want reply then reconcile", len(stored))
	}
	_, op0, seq0, _ := publicationFrame(t, key, stored[0])
	kind1, _, _, ceiling1 := publicationFrame(t, key, stored[1])
	if op0 != "op-unknown" || kind1 != kindReconcile || ceiling1 != seq0 {
		t.Fatalf("recovered order = reply(op=%q seq=%d), reconcile(kind=%q ceiling=%d)", op0, seq0, kind1, ceiling1)
	}
}

func TestService_StoredButErroredReplyRemainsDuplicateSafeBeforeReconcile(t *testing.T) {
	mb := newPublicationMailbox()
	mb.failFirst = errors.New("reply stored but response lost")
	mb.storeFirstThenFail = true
	svc, key := publicationService(t, mb)

	replyDone := make(chan error, 1)
	go func() {
		replyDone <- svc.bridge.sealReply(context.Background(), protocol.Control{
			Op: protocol.OpError, OperationID: "op-stored-unknown", ErrorCode: protocol.CodeInputBusy,
		})
	}()
	awaitFirstAppend(t, mb)
	close(mb.releaseFirst)
	if err := <-replyDone; err == nil {
		t.Fatal("first reply append succeeded; want a lost-response result")
	}
	if err := svc.sink.Reconcile(); err != nil {
		t.Fatalf("reconcile must redrive the stored-but-unacknowledged reply itself: %v", err)
	}
	if ok, err := svc.bridge.redrivePendingReply(context.Background(), "op-stored-unknown"); err != nil || !ok {
		t.Fatalf("bridge acknowledgement after reconcile redrive = (%v, %v), want delivered", ok, err)
	}

	attempts, stored := mb.snapshot()
	if len(attempts) != 3 || !bytes.Equal(attempts[0], attempts[1]) {
		t.Fatalf("stored reply redrive was not byte-identical: attempts=%d", len(attempts))
	}
	if len(stored) != 3 || !bytes.Equal(stored[0], stored[1]) {
		t.Fatalf("stored publications = %d, want identical reply, duplicate, reconcile", len(stored))
	}
	recv := crypto.NewMailboxReceiver()
	first, err := crypto.ParseEnvelope(stored[0])
	if err != nil {
		t.Fatalf("parse first reply: %v", err)
	}
	if _, err := recv.Accept(key, first); err != nil {
		t.Fatalf("accept first reply: %v", err)
	}
	duplicate, err := crypto.ParseEnvelope(stored[1])
	if err != nil {
		t.Fatalf("parse duplicate reply: %v", err)
	}
	if _, err := recv.Accept(key, duplicate); !errors.Is(err, crypto.ErrStaleSeq) {
		t.Fatalf("accept exact duplicate = %v, want ErrStaleSeq", err)
	}
	reconcile, err := crypto.ParseEnvelope(stored[2])
	if err != nil {
		t.Fatalf("parse reconcile: %v", err)
	}
	if _, err := recv.Accept(key, reconcile); err != nil {
		t.Fatalf("accept reconcile after duplicate: %v", err)
	}
	_, _, replySeq, _ := publicationFrame(t, key, stored[0])
	kind, _, _, ceiling := publicationFrame(t, key, stored[2])
	if kind != kindReconcile || ceiling != replySeq {
		t.Fatalf("final publication = kind %q ceiling %d, want reconcile covering reply %d", kind, ceiling, replySeq)
	}
}
