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
	"path/filepath"
	"strings"
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

// serviceHangingMailbox supplies the command-side Mailbox methods while its embedded
// hangingAppender blocks the journal/replay side until that append context is cancelled.
type serviceHangingMailbox struct{ *hangingAppender }

func (m *serviceHangingMailbox) MailboxRead(context.Context, uint64) ([]relay.Item, error) {
	return nil, nil
}

func (m *serviceHangingMailbox) MailboxWait(context.Context, uint64) ([]relay.Item, bool, error) {
	return nil, false, nil
}

func (m *serviceHangingMailbox) MailboxAck(context.Context, uint64) error { return nil }

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

// MailboxWait is the S6b low-latency seam. The scripted inbox is finite, so a wait
// that blocked would stall Service.Run's command loop after the script is drained;
// answering immediately keeps this fake's shape (serve the script, then nothing).
func (m *scriptedMailbox) MailboxWait(ctx context.Context, cursor uint64) ([]relay.Item, bool, error) {
	items, err := m.MailboxRead(ctx, cursor)
	return items, false, err
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

// Replay runs synchronously before the Service starts either producer loop. Its relay append
// must nevertheless inherit the Service generation context: otherwise cancellation during a
// lost relay reply leaves Run stuck until RelaySink's independent five-second deadline.
func TestService_RunCancelStopsAnInFlightReplayAppend(t *testing.T) {
	app := &hangingAppender{entered: make(chan struct{}, 1)}
	mb := &serviceHangingMailbox{hangingAppender: app}
	outbox, err := OpenOutbox("")
	if err != nil {
		t.Fatalf("OpenOutbox: %v", err)
	}
	if err := outbox.Reserve(1, []byte("previously sealed envelope")); err != nil {
		t.Fatalf("reserve replay entry: %v", err)
	}
	svc := NewService(ServiceConfig{
		DaemonSocket: "/nonexistent/remote.sock",
		Relay:        mb,
		PhoneTarget:  "phone",
		Outbox:       outbox,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()
	select {
	case <-app.entered:
	case <-time.After(time.Second):
		t.Fatal("replay did not enter the relay client")
	}
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not cancel its in-flight replay append")
	}
	if err := svc.Err(); err != nil {
		t.Fatalf("normal service cancellation reported degraded state: %v", err)
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

// --- ADR-015 P9/P12: PushGateway wiring at the Service seam --------------------------

// TestNewService_PushGatewayConfiguredWrapsPusherInTransportRouter pins the production
// wiring the R3 GREEN review found missing: with cfg.PushGateway set, PushConfig.Pusher
// (PushNotifier's send seam) is a *TransportRouter over the relay's own PushTriggerer,
// not the relay pusher directly -- so selection between legacy_relay and gateway becomes
// exclusive (P12) the moment a pairing migrates, rather than never becoming reachable at
// all because nothing in cmd/swarm-remote ever constructed the router.
func TestNewService_PushGatewayConfiguredWrapsPusherInTransportRouter(t *testing.T) {
	mb := &pushCapableMailbox{}
	svc := NewService(ServiceConfig{
		Relay: mb,
		PushGateway: &PushGatewayConfig{
			GatewayURL:       "https://push.example.com",
			SubmitCapability: "test-cap",
			Address:          testPushAddress(0xF0),
		},
	})
	router, ok := svc.notifier.cfg.Pusher.(*TransportRouter)
	if !ok {
		t.Fatalf("PushConfig.Pusher = %T, want *TransportRouter when PushGateway is configured", svc.notifier.cfg.Pusher)
	}
	if router.Legacy != PushTriggerer(mb) {
		t.Fatal("TransportRouter.Legacy is not the relay's own PushTriggerer -- a rollback to " +
			"legacy_relay would silently stop firing push_trigger at all")
	}
	if svc.wakeMachine == nil {
		t.Fatal("Service.wakeMachine is nil despite PushGateway being configured")
	}
}

// TestNewService_NoPushGatewayLeavesThePusherUntouched pins the other half: the default
// (PushGateway nil, every pairing until it migrates) must be BYTE-FOR-BYTE today's
// behaviour -- the relay's PushTriggerer wired directly, no TransportRouter in the way.
func TestNewService_NoPushGatewayLeavesThePusherUntouched(t *testing.T) {
	mb := &pushCapableMailbox{}
	svc := NewService(ServiceConfig{Relay: mb})
	if _, ok := svc.notifier.cfg.Pusher.(*TransportRouter); ok {
		t.Fatal("PushConfig.Pusher is a *TransportRouter with no PushGateway configured, want the legacy path untouched")
	}
	if svc.notifier.cfg.Pusher != PushTriggerer(mb) {
		t.Fatalf("PushConfig.Pusher = %v, want the relay client directly", svc.notifier.cfg.Pusher)
	}
	if svc.wakeMachine != nil {
		t.Fatal("Service.wakeMachine is non-nil with no PushGateway configured")
	}
}

// TestService_RedrivePendingWakeObligationsIsANoOpWithoutPushGateway pins that calling
// the redrive hook on an unmigrated (legacy_relay-only) service is safe and cheap, since
// cmd/swarm-remote calls it unconditionally at startup.
func TestService_RedrivePendingWakeObligationsIsANoOpWithoutPushGateway(t *testing.T) {
	svc := NewService(ServiceConfig{Relay: &pushCapableMailbox{}})
	if err := svc.RedrivePendingWakeObligations(context.Background()); err != nil {
		t.Fatalf("RedrivePendingWakeObligations with no PushGateway configured: %v", err)
	}
}

// TestService_RedrivePendingWakeObligationsSubmitsAPendingObligationAtStartup is the
// PG-OBL-8 half: an obligation left non-terminal by whatever wrote the durable stores
// (standing in for a previous process instance) is re-driven the moment this one calls
// the hook, without needing an unrelated trigger to land first.
func TestService_RedrivePendingWakeObligationsSubmitsAPendingObligationAtStartup(t *testing.T) {
	obligations := newFakeObligationStore()
	addr := testPushAddress(0xF1)
	env, err := SealWakeV1(testWakeKey(), addr, 1, time.Now())
	if err != nil {
		t.Fatalf("SealWakeV1: %v", err)
	}
	if err := obligations.Put(WakeObligation{
		Address: addr, WakeSeq: 1, Envelope: env,
		IssuedAt: time.Now(), ExpiresAt: time.Now().Add(WakeV1Expiry), State: ObligationPending,
	}); err != nil {
		t.Fatalf("seed a pending obligation: %v", err)
	}

	sub := &fakeSubmitter{}
	svc := NewService(ServiceConfig{
		Relay:   &pushCapableMailbox{},
		WakeKey: testWakeKey(),
		PushGateway: &PushGatewayConfig{
			GatewayURL: "https://push.example.com", SubmitCapability: "cap", Address: addr,
			Obligations: obligations,
		},
	})
	// The service's own machine talks HTTP; swap its Submitter for the fake so this test
	// exercises the redrive PATH, not a real network call.
	svc.wakeMachine.cfg.Submitter = sub

	if err := svc.RedrivePendingWakeObligations(context.Background()); err != nil {
		t.Fatalf("RedrivePendingWakeObligations: %v", err)
	}
	if got := len(sub.all()); got != 1 {
		t.Fatalf("wake submissions after redrive = %d, want 1: PG-OBL-8's restart re-drive must not "+
			"wait for an unrelated trigger", got)
	}
}

func TestServiceErrReportsJournalReconnectFailure(t *testing.T) {
	svc := NewService(ServiceConfig{
		DaemonSocket:   filepath.Join(t.TempDir(), "missing.sock"),
		Relay:          &scriptedMailbox{},
		ReconnectDelay: time.Hour,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		svc.runJournal(ctx)
		close(done)
	}()
	deadline := time.Now().Add(time.Second)
	for svc.Err() == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if err := svc.Err(); err == nil || !strings.Contains(err.Error(), "journal bridge") {
		t.Fatalf("Service.Err() = %v, want the journal bridge failure", err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runJournal did not stop")
	}
}
