package remotegw

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

var errReplyDeliveryUnknown = errors.New("reply delivery unknown")

// replyRetryMailbox models the real delivery-unknown boundary: the first append may have
// reached the relay, but its response did not reach the gateway. Every attempted envelope
// is retained so the test can require byte-for-byte replay rather than a fresh seq/nonce.
type replyRetryMailbox struct {
	fakeMailbox
	muAttempts sync.Mutex
	attempts   [][]byte
	acks       []uint64
	failFirst  bool
}

type retainedRunMailbox struct {
	fakeMailbox
	muRun       sync.Mutex
	appendCalls int
}

func (m *retainedRunMailbox) MailboxAppend(context.Context, string, []byte) (uint64, error) {
	m.muRun.Lock()
	m.appendCalls++
	m.muRun.Unlock()
	return 0, errReplyDeliveryUnknown
}

func (m *retainedRunMailbox) appends() int {
	m.muRun.Lock()
	defer m.muRun.Unlock()
	return m.appendCalls
}

type retainedRunForwarder struct {
	mu    sync.Mutex
	calls int
}

func (f *retainedRunForwarder) ForwardCommand(_ string, rc protocol.RemoteCommand) (protocol.Control, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return protocol.Control{Op: protocol.OpOK, SessionID: rc.Session, OperationID: rc.OperationID}, nil
}

func (f *retainedRunForwarder) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (m *replyRetryMailbox) MailboxAppend(_ context.Context, target string, env []byte) (uint64, error) {
	m.muAttempts.Lock()
	m.attempts = append(m.attempts, append([]byte(nil), env...))
	call := len(m.attempts)
	m.muAttempts.Unlock()
	if call == 1 && m.failFirst {
		return 0, errReplyDeliveryUnknown
	}
	return m.fakeMailbox.MailboxAppend(context.Background(), target, env)
}

func (m *replyRetryMailbox) MailboxAck(_ context.Context, cursor uint64) error {
	m.muAttempts.Lock()
	defer m.muAttempts.Unlock()
	m.acks = append(m.acks, cursor)
	return nil
}

func TestCommandBridge_ReplyAppendFailureRetriesExactEnvelopeBeforeLaterCommand(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 31)
	}
	mb := &replyRetryMailbox{
		fakeMailbox: fakeMailbox{inbox: []relay.Item{
			{Cursor: 1, Envelope: sealedCmd(t, key, 1, protocol.DeviceCommandAuth{Action: protocol.ActionKill, Session: "m/one", OperationID: "op-1", DeviceID: "d1", Sig: "s1"})},
			{Cursor: 2, Envelope: sealedCmd(t, key, 2, protocol.DeviceCommandAuth{Action: protocol.ActionDelete, Session: "m/two", OperationID: "op-2", DeviceID: "d1", Sig: "s2"})},
		}},
		failFirst: true,
	}
	fwd := &fakeForwarder{}
	b := NewCommandBridge(CommandBridgeConfig{
		Mailbox: mb, Forwarder: fwd, Key: key, EpochID: 1, ReplyTarget: "phone",
	})

	if n, err := b.PollOnce(context.Background()); n != 0 || !errors.Is(err, errReplyDeliveryUnknown) {
		t.Fatalf("first PollOnce = (%d, %v), want (0, delivery-unknown error)", n, err)
	}
	if got := b.Cursor(); got != 0 {
		t.Fatalf("cursor advanced to %d past an authenticated command whose terminal reply is unpublished", got)
	}
	mb.muAttempts.Lock()
	firstAcks := append([]uint64(nil), mb.acks...)
	mb.muAttempts.Unlock()
	if len(firstAcks) != 0 {
		t.Fatalf("relay ACKs after failed outcome publication = %v, want none", firstAcks)
	}
	fwd.mu.Lock()
	firstSeen := append([]protocol.DeviceCommandAuth(nil), fwd.seen...)
	fwd.mu.Unlock()
	if len(firstSeen) != 1 || firstSeen[0].OperationID != "op-1" {
		t.Fatalf("first pass forwarded operations %+v, want only op-1; op-2 must wait behind its unpublished outcome", firstSeen)
	}

	if n, err := b.PollOnce(context.Background()); err != nil || n != 2 {
		t.Fatalf("retry PollOnce = (%d, %v), want both retained commands consumed after reply recovery", n, err)
	}
	mb.muAttempts.Lock()
	attempts := append([][]byte(nil), mb.attempts...)
	mb.muAttempts.Unlock()
	if len(attempts) != 3 {
		t.Fatalf("reply append attempts = %d, want failed op-1 + exact retry + op-2", len(attempts))
	}
	if !bytes.Equal(attempts[0], attempts[1]) {
		t.Fatal("delivery-unknown reply was re-sealed instead of re-appended byte-for-byte")
	}
	if bytes.Equal(attempts[1], attempts[2]) {
		t.Fatal("the later command reused the preceding command's reply envelope")
	}
	if got := b.Cursor(); got != 2 {
		t.Fatalf("cursor after recovered replies = %d, want 2", got)
	}
}

func TestCommandBridge_ReplyAppendFailureRestartsFromUnchangedCheckpoint(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 47)
	}
	mb := &replyRetryMailbox{
		fakeMailbox: fakeMailbox{inbox: []relay.Item{{
			Cursor: 1,
			Envelope: sealedCmd(t, key, 1, protocol.DeviceCommandAuth{
				Action: protocol.ActionKill, Session: "m/one", OperationID: "op-restart", DeviceID: "d1", Sig: "s1",
			}),
		}}},
		failFirst: true,
	}
	inbound, err := OpenInboundState("", "")
	if err != nil {
		t.Fatalf("OpenInboundState: %v", err)
	}
	replySeq, err := OpenSeqSource("")
	if err != nil {
		t.Fatalf("OpenSeqSource: %v", err)
	}
	newBridge := func(fwd CommandForwarder) *CommandBridge {
		return NewCommandBridge(CommandBridgeConfig{
			Mailbox: mb, Forwarder: fwd, Key: key, EpochID: 1, ReplyTarget: "phone",
			Inbound: inbound, ReplySeq: replySeq,
		})
	}
	firstForwarder := &fakeForwarder{}
	if n, err := newBridge(firstForwarder).PollOnce(context.Background()); n != 0 || !errors.Is(err, errReplyDeliveryUnknown) {
		t.Fatalf("pre-restart PollOnce = (%d, %v), want retained delivery-unknown command", n, err)
	}
	if got := inbound.Load().Cursor; got != 0 {
		t.Fatalf("durable checkpoint after failed reply = %d, want 0", got)
	}

	secondForwarder := &fakeForwarder{}
	restarted := newBridge(secondForwarder)
	if n, err := restarted.PollOnce(context.Background()); n != 1 || err != nil {
		t.Fatalf("post-restart PollOnce = (%d, %v), want retained command re-served and consumed", n, err)
	}
	secondForwarder.mu.Lock()
	seen := append([]protocol.DeviceCommandAuth(nil), secondForwarder.seen...)
	secondForwarder.mu.Unlock()
	if len(seen) != 1 || seen[0].OperationID != "op-restart" {
		t.Fatalf("post-restart daemon replay = %+v, want op-restart for daemon idempotency", seen)
	}
	if got := inbound.Load().Cursor; got != 1 {
		t.Fatalf("durable checkpoint after recovered reply = %d, want 1", got)
	}
}

func TestCommandBridge_ConcurrentProducerRedrivesPendingReplyWithoutDuplicatingItsCommandRetry(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 59)
	}
	mb := &replyRetryMailbox{failFirst: true}
	b := NewCommandBridge(CommandBridgeConfig{Mailbox: mb, Key: key, EpochID: 1, ReplyTarget: "phone"})
	commandReply := protocol.Control{Op: protocol.OpOK, SessionID: "m/one", OperationID: "op-command"}
	watcherReply := protocol.Control{Op: protocol.OpDetach, SessionID: "m/two", OperationID: "op-watcher"}

	if err := b.sealReply(context.Background(), commandReply); !errors.Is(err, errReplyDeliveryUnknown) {
		t.Fatalf("initial command reply = %v, want delivery-unknown", err)
	}
	if err := b.sealReply(context.Background(), watcherReply); err != nil {
		t.Fatalf("concurrent watcher reply did not first redrive the pending command reply: %v", err)
	}
	if err := b.sealReply(context.Background(), commandReply); err != nil {
		t.Fatalf("retained command did not recognize its reply was already redriven: %v", err)
	}
	mb.muAttempts.Lock()
	attempts := append([][]byte(nil), mb.attempts...)
	mb.muAttempts.Unlock()
	if len(attempts) != 3 {
		t.Fatalf("append attempts = %d, want failed command + exact redrive + watcher; command retry must add none", len(attempts))
	}
	if !bytes.Equal(attempts[0], attempts[1]) {
		t.Fatal("concurrent producer re-sealed the pending command reply")
	}
}

func TestCommandBridge_RunBacksOffAndRedrivesPendingReplyWithoutReforwarding(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 71)
	}
	mb := &retainedRunMailbox{fakeMailbox: fakeMailbox{inbox: []relay.Item{{
		Cursor: 1,
		Envelope: sealedCmd(t, key, 1, protocol.DeviceCommandAuth{
			Action: protocol.ActionKill, Session: "m/one", OperationID: "op-run", DeviceID: "d1", Sig: "s1",
		}),
	}}}}
	fwd := &retainedRunForwarder{}
	waits := make(chan int, 4)
	release := make(chan struct{}, 1)
	b := NewCommandBridge(CommandBridgeConfig{
		Mailbox: mb, Forwarder: fwd, Key: key, EpochID: 1, ReplyTarget: "phone",
		RetainedRetryWait: func(ctx context.Context, attempt int) error {
			select {
			case waits <- attempt:
			case <-ctx.Done():
				return ctx.Err()
			}
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	select {
	case attempt := <-waits:
		if attempt != 1 {
			t.Fatalf("first retained backoff attempt = %d, want 1", attempt)
		}
	case <-time.After(time.Second):
		t.Fatal("Run never entered retained-command backoff")
	}
	if got := fwd.count(); got != 1 {
		t.Fatalf("daemon forwards before first backoff = %d, want 1", got)
	}
	if got := mb.appends(); got != 1 {
		t.Fatalf("reply appends before first backoff = %d, want 1", got)
	}

	release <- struct{}{}
	select {
	case attempt := <-waits:
		if attempt != 2 {
			t.Fatalf("second retained backoff attempt = %d, want 2", attempt)
		}
	case <-time.After(time.Second):
		t.Fatal("Run never retried the retained reply")
	}
	if got := fwd.count(); got != 1 {
		t.Fatalf("daemon command was re-forwarded during reply-only redrive: %d calls, want 1", got)
	}
	if got := mb.appends(); got != 2 {
		t.Fatalf("reply append attempts after one released backoff = %d, want 2", got)
	}

	cancelledAt := time.Now()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run after cancellation = %v, want context.Canceled", err)
		}
		if elapsed := time.Since(cancelledAt); elapsed > 250*time.Millisecond {
			t.Fatalf("Run took %v to cancel while in retained backoff", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not cancel promptly from retained-command backoff")
	}
}
