// FAILING-FIRST (TDD RED, GG-5) tests for the gateway runtime (agents-tracker-6rn):
// the supervised Service that composes the journal-OUT bridge (RunJournal ->
// RelaySink) and the command-IN loop (CommandBridge) over one relay Mailbox, with
// journal reconnect and clean ctx-cancel shutdown.
//
// THE CONTRACT these tests freeze (undefined symbols -> compile-fail RED):
//   - type Service; func NewService(ServiceConfig) *Service; (*Service).Run(ctx) error
//   - ServiceConfig{ DaemonSocket; Relay Mailbox; PhoneTarget; Key; EpochID;
//     PollInterval; ReconnectDelay; ... }
//
// Run drives both loops until ctx is cancelled. Because the Service depends only on
// the Mailbox seam and a daemon socket, this unit test uses a scripted fake Mailbox
// and a fake daemon-less Forwarder is NOT needed (Run builds its own Gateway); the
// journal side is exercised in the skeleton integration test against a real daemon.
// Here we assert the command-IN half drains a queued command and the runtime stops
// on cancel.
package remotegw

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// scriptedMailbox serves a fixed inbox and records appends; MailboxRead honours the
// cursor so a drained item is not re-served.
type scriptedMailbox struct {
	mu      sync.Mutex
	inbox   []relay.Item
	appends [][]byte
}

func (m *scriptedMailbox) MailboxRead(_ context.Context, cursor uint64) ([]relay.Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []relay.Item
	for _, it := range m.inbox {
		if it.Cursor > cursor {
			out = append(out, it)
		}
	}
	return out, nil
}

func (m *scriptedMailbox) MailboxAppend(_ context.Context, _ string, env []byte) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.appends = append(m.appends, env)
	return uint64(len(m.appends)), nil
}

// MailboxAck is a no-op: this fake does not model durable purge. It exists only
// to satisfy the Mailbox interface so this fake keeps compiling.
func (m *scriptedMailbox) MailboxAck(_ context.Context, _ uint64) error {
	return nil
}

func (m *scriptedMailbox) appendCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.appends)
}

// TestService_RunStopsOnCancel proves the runtime returns promptly when ctx is
// cancelled, with an unreachable daemon socket (the journal loop keeps retrying but
// must not block shutdown). The command loop polls the scripted mailbox meanwhile.
func TestService_RunStopsOnCancel(t *testing.T) {
	mb := &scriptedMailbox{}
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 2)
	}
	svc := NewService(ServiceConfig{
		DaemonSocket:   "/nonexistent/remote.sock", // journal RunJournal will fail + retry
		Relay:          mb,
		PhoneTarget:    "phone",
		Key:            key,
		EpochID:        1,
		PollInterval:   10 * time.Millisecond,
		ReconnectDelay: 10 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()

	time.Sleep(60 * time.Millisecond) // let both loops spin a few times
	cancel()
	select {
	case err := <-done:
		if err == nil || err != context.Canceled {
			t.Fatalf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop within 2s of cancel (a loop is not honouring ctx)")
	}
}

// TestService_CommandLoopDrainsQueuedCommand proves the runtime's command-IN half
// polls the mailbox and processes a queued command even while the journal side is
// failing (unreachable daemon): the two loops are independent. It uses a Service
// whose Forwarder is injected so no real daemon is needed.
func TestService_CommandLoopDrainsQueuedCommand(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 2)
	}
	mb := &scriptedMailbox{inbox: []relay.Item{
		{Cursor: 1, Envelope: sealedCmd(t, key, 1, protocol.DeviceCommandAuth{Action: protocol.ActionKill, Session: "m/s1", OperationID: "op-1", DeviceID: "d", Sig: "s"})},
	}}
	fwd := &fakeForwarder{}
	svc := NewService(ServiceConfig{
		DaemonSocket:   "/nonexistent/remote.sock",
		Relay:          mb,
		Forwarder:      fwd, // injected: bypass the built-in Gateway forwarder
		PhoneTarget:    "phone",
		Key:            key,
		EpochID:        1,
		PollInterval:   10 * time.Millisecond,
		ReconnectDelay: time.Hour, // keep the journal side quiet
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = svc.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		fwd.mu.Lock()
		n := len(fwd.seen)
		fwd.mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	fwd.mu.Lock()
	seen := len(fwd.seen)
	fwd.mu.Unlock()
	if seen != 1 {
		t.Fatalf("command loop forwarded %d commands, want 1", seen)
	}
	// A sealed reply was appended back to the phone mailbox.
	if mb.appendCount() == 0 {
		t.Fatal("no reply appended for the drained command")
	}
	// Sanity: the reply opens under the key to a control.
	e, err := crypto.ParseEnvelope(mb.appends[len(mb.appends)-1])
	if err != nil {
		t.Fatalf("reply parse: %v", err)
	}
	plain, err := crypto.OpenMailbox(key, e)
	if err != nil {
		t.Fatalf("reply open: %v", err)
	}
	var ctrl protocol.Control
	if err := json.Unmarshal(plain, &ctrl); err != nil {
		t.Fatalf("reply decode: %v", err)
	}
}
