package remotegw

// FAILING-FIRST (TDD RED, GG-5) for codex round-3 blocker 1 / bead agents-tracker-10ar:
// the GATEWAY's command loop still probed MailboxWait blind. The phone negotiates the
// "wait" capability per connection (mobile/relay.go negotiateWaitSupport, the final-audit
// committee's M1/M2/M3 fix); the machine hop got only a residual note. Against a relay
// whose hello does not advertise "wait" -- a pre-wait build -- the loop parks a wait the
// relay answers with an uncorrelated MsgError the client's pump discards as unsolicited,
// so every wait ends as a swallowed timeout: commands stop flowing FOREVER while the
// relay is, by its own contract, perfectly usable through mailbox_read.
//
// The fix mirrors the phone: CommandBridge.Run negotiates capabilities once per
// connection (a Service is one relay generation, so Run entry IS the connection's
// start), and a non-advertising relay gets the documented compatibility fallback --
// MailboxRead polling at the same 500 ms cadence the phone's drainPoll uses (playbook
// section 10; internal/remote/transport doc.go) -- never an eternal refused-wait loop.
// A Mailbox seam that offers no hello at all (unit fakes) keeps the wait it implements.

import (
	"context"
	"crypto/ed25519"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// dialShippedRelay stands up the real relay.Server and dials it authenticated, so the
// caps fence below asserts against what production actually grants.
func dialShippedRelay(t *testing.T, ctx context.Context) *relay.Client {
	t.Helper()
	cfg := relay.DefaultConfig()
	cfg.DBPath = filepath.Join(t.TempDir(), "relay.db")
	srv, err := relay.New(cfg)
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("relay start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	cl, err := relay.Dial(ctx, srv.URL(), relayAuthFor(pub, priv))
	if err != nil {
		t.Fatalf("relay dial: %v", err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	return cl
}

// helloMailbox is a fakeMailbox that ALSO speaks r_hello, with a configurable
// capability set. Its MailboxWait models the pre-wait relay AS SEEN THROUGH THE REAL
// CLIENT: the refusal frame is uncorrelated, the pump drops it, and the caller sees
// nothing but its own deadline -- so the fake parks until the caller's context ends.
// When the capability set advertises "wait", MailboxWait answers from the inbox like
// fakeMailbox does (arrival-driven, no blocking modelled).
type helloMailbox struct {
	fakeMailbox
	caps []string

	mu        sync.Mutex
	hellos    int
	waitCalls int
	readCalls int
}

func (m *helloMailbox) Hello(_ context.Context, version int, asked []string) (int, []string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hellos++
	// The real r_hello returns the INTERSECTION of what the client asked and what the
	// server registered (relay server.go handleHello); modelling that here is what
	// makes a capability dropped from gatewayHelloCaps fail the advertising arm
	// instead of passing on a fake that volunteers caps nobody requested.
	agreed := make([]string, 0, len(m.caps))
	for _, c := range asked {
		for _, s := range m.caps {
			if c == s {
				agreed = append(agreed, c)
				break
			}
		}
	}
	return version, agreed, nil
}

func (m *helloMailbox) advertisesWait() bool {
	for _, c := range m.caps {
		if c == "wait" {
			return true
		}
	}
	return false
}

func (m *helloMailbox) MailboxWait(ctx context.Context, cursor uint64) ([]relay.Item, bool, error) {
	m.mu.Lock()
	m.waitCalls++
	m.mu.Unlock()
	if !m.advertisesWait() {
		// The pre-wait relay through the real client: the MsgError refusal is dropped
		// as unsolicited by the pump, and only the caller's own deadline ends the wait.
		<-ctx.Done()
		return nil, false, ctx.Err()
	}
	return m.fakeMailbox.MailboxWait(ctx, cursor)
}

func (m *helloMailbox) MailboxRead(ctx context.Context, cursor uint64) ([]relay.Item, error) {
	m.mu.Lock()
	m.readCalls++
	m.mu.Unlock()
	return m.fakeMailbox.MailboxRead(ctx, cursor)
}

func (m *helloMailbox) counts() (hellos, waitCalls, readCalls int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hellos, m.waitCalls, m.readCalls
}

func r4helloBridge(t *testing.T, mb Mailbox, fwd *fakeForwarder, key crypto.ContentKey) *CommandBridge {
	t.Helper()
	return NewCommandBridge(CommandBridgeConfig{
		Mailbox:     mb,
		Forwarder:   fwd,
		Key:         key,
		EpochID:     1,
		ReplyTarget: "phone-routing-id",
		// Short, so a tree still probing the wait blind cycles its refused-wait loop
		// inside this test's bounds instead of sitting out the 35 s production budget.
		WaitTimeout: 300 * time.Millisecond,
	})
}

func r4awaitForward(t *testing.T, fwd *fakeForwarder, want int, msg string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		fwd.mu.Lock()
		n := len(fwd.ops)
		fwd.mu.Unlock()
		if n >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(msg)
}

// TestCommitteeR4_ANonAdvertisingRelayGetsThePollFallbackNotAnEternalRefusedWaitLoop is
// the old-relay arm: hello omits "wait", so Run must select MailboxRead polling and the
// command still reaches the daemon -- with mailbox_wait never issued at all.
func TestCommitteeR4_ANonAdvertisingRelayGetsThePollFallbackNotAnEternalRefusedWaitLoop(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 3)
	}
	mb := &helloMailbox{caps: []string{"mailbox", "push"}}
	mb.inbox = []relay.Item{{Cursor: 1, Envelope: sealedCmd(t, key, 1,
		protocol.DeviceCommandAuth{Action: protocol.ActionKill, Session: "m/s1", OperationID: "op-1", DeviceID: "d1", Sig: "s1"})}}
	fwd := &fakeForwarder{}
	b := r4helloBridge(t, mb, fwd, key)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = b.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	r4awaitForward(t, fwd, 1,
		"the command never reached the daemon through a relay that does not advertise \"wait\": "+
			"the loop is parked in the eternal refused-wait cycle instead of the documented "+
			"MailboxRead poll fallback (codex round-3 blocker 1, bead agents-tracker-10ar)")

	hellos, waitCalls, readCalls := mb.counts()
	if hellos == 0 {
		t.Error("the bridge never negotiated hello; wait support must be derived from the " +
			"relay's advertised capability set, per connection")
	}
	if waitCalls != 0 {
		t.Errorf("the bridge issued %d mailbox_wait ops to a relay whose hello did not "+
			"advertise \"wait\"; an unclaimed capability must never be probed", waitCalls)
	}
	if readCalls == 0 {
		t.Error("no mailbox_read was issued: the command arrived outside the poll fallback")
	}
}

// TestCommitteeR4_AnAdvertisingRelayKeepsTheWaitTail is the modern arm: hello advertises
// "wait", the loop drives MailboxWait exactly as before, and the negotiation actually
// happened rather than being skipped.
func TestCommitteeR4_AnAdvertisingRelayKeepsTheWaitTail(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 5)
	}
	mb := &helloMailbox{caps: []string{"mailbox", "push", "wait"}}
	mb.inbox = []relay.Item{{Cursor: 1, Envelope: sealedCmd(t, key, 1,
		protocol.DeviceCommandAuth{Action: protocol.ActionKill, Session: "m/s2", OperationID: "op-2", DeviceID: "d1", Sig: "s2"})}}
	fwd := &fakeForwarder{}
	b := r4helloBridge(t, mb, fwd, key)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = b.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	r4awaitForward(t, fwd, 1, "the command never reached the daemon through the wait tail")

	hellos, waitCalls, _ := mb.counts()
	if hellos == 0 {
		t.Error("the bridge never negotiated hello with a wait-advertising relay; the verdict " +
			"must come from the capability exchange, not from a default")
	}
	if waitCalls == 0 {
		t.Error("no mailbox_wait was issued against a relay that advertises \"wait\": the " +
			"negotiation demoted a modern relay to the compatibility poll")
	}
}

// TestCommitteeR4_GatewayHelloCapsAreServedByTheShippedRelay is the cross-package drift
// fence, the machine-hop sibling of mobile's TestCommitteeR3_PhoneHelloCapsAreServedByTheRelay:
// every capability the gateway's hello asks for must be granted by the shipped relay, or
// the sidecar would silently run degraded against its OWN relay.
func TestCommitteeR4_GatewayHelloCapsAreServedByTheShippedRelay(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cl := dialShippedRelay(t, ctx)
	_, agreed, err := cl.Hello(ctx, relay.ProtocolVersion, gatewayHelloCaps)
	if err != nil {
		t.Fatalf("Hello: %v", err)
	}
	granted := map[string]bool{}
	for _, c := range agreed {
		granted[c] = true
	}
	for _, want := range gatewayHelloCaps {
		if !granted[want] {
			t.Errorf("the shipped relay did not grant %q, which gatewayHelloCaps requests; "+
				"the gateway would silently degrade against its own relay", want)
		}
	}
}
