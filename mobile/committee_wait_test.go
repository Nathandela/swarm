package swarmmobile

// FAILING-FIRST (TDD RED, GG-5) for the final-audit committee's wait-negotiation findings
// (Opus M1/M2/M3, codex quota HIGH), the phone half:
//
//   - Wait support is a PER-CONNECTION verdict derived from the r_hello capability set,
//     never a process-sticky one: a phone that fell back to the poll against an old relay
//     re-evaluates on its next connection, so an upgraded relay gets the wait tail for
//     free (bead agents-tracker-zphd), and a network stall on one connection cannot pin a
//     modern relay to the 500 ms poll for the process lifetime.
//   - The wait drain acks OFF the delivery path through transport.AckBatcher: one metered
//     ack per second at most, instead of one per delivered page -- the synchronous shape
//     spends the relay's own OpsPerMin (600) window in ~40 s at the specified 8 frames/s.
//
// The rig is the r9_waitfallback one: a real relay.Server, a real machine-side RelaySink,
// and a proxy in front of the phone that either plays a pre-wait relay for the FIRST
// connection or forwards verbatim while counting the phone's metered mailbox ops.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remotegw"
)

// committeeProxy fronts the real relay for the PHONE. With oldFirst set, the first
// accepted connection is played as a pre-wait relay: hello is answered locally with the
// "wait" capability stripped (the intersection an old serverCaps would produce), and any
// mailbox_wait/_cancel that still arrives is refused with the old dispatch's in-order
// MsgError. Every other frame, and every later connection, crosses verbatim. All counters
// observe what the PHONE put on the wire.
type committeeProxy struct {
	srv      *httptest.Server
	upstream string
	oldFirst bool

	mu            sync.Mutex
	conns         int
	helloStripped int // hellos answered as the old relay (no "wait")
	waitRefused   int // wait ops refused old-relay-style (must be 0 under negotiation)
	waitForwarded int // mailbox_wait ops that crossed to the real relay
	readForwarded int // mailbox_read ops
	ackForwarded  int // mailbox_ack ops
	discardProbed int // mailbox_discard ops put on the wire (must stay zero for an old relay)
	kill          []func()
}

func newCommitteeProxy(t *testing.T, upstream string, oldFirst bool) *committeeProxy {
	t.Helper()
	p := &committeeProxy{upstream: upstream, oldFirst: oldFirst}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		idx := p.conns
		p.conns++
		p.mu.Unlock()
		oldRelay := p.oldFirst && idx == 0

		down, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = down.CloseNow() }()
		down.SetReadLimit(relay.MaxFrame + 64)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		up, _, err := websocket.Dial(ctx, p.upstream, nil)
		if err != nil {
			return
		}
		defer func() { _ = up.CloseNow() }()
		up.SetReadLimit(relay.MaxFrame + 64)

		p.mu.Lock()
		p.kill = append(p.kill, func() {
			cancel()
			_ = down.CloseNow()
			_ = up.CloseNow()
		})
		p.mu.Unlock()

		var wmu sync.Mutex
		write := func(c *websocket.Conn, mt websocket.MessageType, b []byte) error {
			wmu.Lock()
			defer wmu.Unlock()
			return c.Write(ctx, mt, b)
		}

		done := make(chan struct{}, 2)
		go func() { // relay -> phone, verbatim
			defer func() { done <- struct{}{} }()
			for {
				mt, data, err := up.Read(ctx)
				if err != nil {
					return
				}
				if err := write(down, mt, data); err != nil {
					return
				}
			}
		}()
		go func() { // phone -> relay: count, and play the old relay when told to
			defer func() { done <- struct{}{} }()
			for {
				mt, data, err := down.Read(ctx)
				if err != nil {
					return
				}
				if reply, forward := p.classify(data, oldRelay); !forward {
					if err := write(down, websocket.MessageBinary, reply); err != nil {
						return
					}
					continue
				}
				if err := write(up, mt, data); err != nil {
					return
				}
			}
		}()
		<-done
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *committeeProxy) URL() string { return "ws" + strings.TrimPrefix(p.srv.URL, "http") }

// classify counts one phone->relay frame and, in old-relay mode, answers hello and the
// wait ops the way a pre-wait dispatch would. It returns (reply, false) to answer locally.
func (p *committeeProxy) classify(data []byte, oldRelay bool) (reply []byte, forward bool) {
	tag, payload, err := relay.ReadFrame(bytes.NewReader(data))
	if err != nil || tag != relay.MsgRelay {
		return nil, true
	}
	var env struct {
		Op      string   `json:"op"`
		Version int      `json:"version"`
		Caps    []string `json:"caps"`
	}
	if json.Unmarshal(payload, &env) != nil {
		return nil, true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	switch env.Op {
	case "hello":
		if !oldRelay {
			return nil, true
		}
		// The old serverCaps intersection: everything the client asked for except
		// capabilities introduced after this simulated relay generation.
		agreed := make([]string, 0, len(env.Caps))
		for _, c := range env.Caps {
			if c != "wait" && c != relay.CapabilityMailboxRecovery {
				agreed = append(agreed, c)
			}
		}
		p.helloStripped++
		body, _ := json.Marshal(map[string]any{"version": env.Version, "caps": agreed})
		var buf bytes.Buffer
		if relay.WriteFrame(&buf, relay.MsgOK, body) != nil {
			return nil, true
		}
		return buf.Bytes(), false
	case "mailbox_wait", "mailbox_wait_cancel":
		if !oldRelay {
			p.waitForwarded++
			return nil, true
		}
		p.waitRefused++
		body, _ := json.Marshal(map[string]string{"code": "bad_request"})
		var buf bytes.Buffer
		if relay.WriteFrame(&buf, relay.MsgError, body) != nil {
			return nil, true
		}
		return buf.Bytes(), false
	case "mailbox_read":
		p.readForwarded++
	case "mailbox_ack":
		p.ackForwarded++
	case "mailbox_discard":
		p.discardProbed++
		if oldRelay {
			body, _ := json.Marshal(map[string]string{"code": "bad_request"})
			var buf bytes.Buffer
			if relay.WriteFrame(&buf, relay.MsgError, body) != nil {
				return nil, true
			}
			return buf.Bytes(), false
		}
	}
	return nil, true
}

// Kill severs every live proxied connection; the phone's next dial builds a new one.
func (p *committeeProxy) Kill() {
	p.mu.Lock()
	fns := p.kill
	p.kill = nil
	p.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

func (p *committeeProxy) counts() (helloStripped, waitRefused, waitForwarded, readForwarded, ackForwarded int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.helloStripped, p.waitRefused, p.waitForwarded, p.readForwarded, p.ackForwarded
}

func (p *committeeProxy) mailboxDiscardProbes() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.discardProbed
}

// committeeRig is the r9_waitfallback seeding, factored for this file: a real relay, a
// paired phone state directory, an authorized machine-side RelaySink, and the phone app
// started against the given (usually proxied) relay URL.
func committeeRig(t *testing.T, ctx context.Context, phoneRelayURL func(realURL string) string) (*App, *remotegw.RelaySink) {
	t.Helper()

	rcfg := relay.DefaultConfig()
	rcfg.DBPath = filepath.Join(t.TempDir(), "relay.db")
	srv, err := relay.New(rcfg)
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("relay start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	const machine = "committee-machine-endpoint"
	const epoch = uint32(7)
	dir := t.TempDir()
	custody := r4r3Custody{}
	wake := custodySealer{tier: "wake", fetch: custody.WakeKEK}
	content := custodySealer{tier: "content", fetch: custody.ContentKEK}
	reg, err := phonecore.NewMachineRegistry(dir)
	if err != nil {
		t.Fatalf("NewMachineRegistry: %v", err)
	}
	stateDir, err := reg.AddMachine(phonecore.MachineDescriptor{ID: machine})
	if err != nil {
		t.Fatalf("AddMachine: %v", err)
	}
	provision, err := phonecore.Resume(phonecore.Config{
		Dir: stateDir, Machine: machine, WakeSealer: wake, ContentSealer: content,
	})
	if err != nil {
		t.Fatalf("phonecore.Resume: %v", err)
	}
	ks := provision.KeyStore()
	phoneTarget := relay.RoutingID(ks.RelayAuthPublic())

	keys, err := crypto.NewEpochKeys()
	if err != nil {
		t.Fatalf("epoch keys: %v", err)
	}
	signPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("machine sign key: %v", err)
	}
	machineID, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("machine identity: %v", err)
	}
	mPub, mPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("machine relay-auth key: %v", err)
	}
	machineRelay, err := relay.Dial(ctx, srv.URL(), relay.ClientAuth{
		RelayAuthPub: mPub,
		Sign:         func(c []byte) ([]byte, error) { return ed25519.Sign(mPriv, c), nil },
	})
	if err != nil {
		t.Fatalf("machine dial: %v", err)
	}
	t.Cleanup(func() { _ = machineRelay.Close() })

	var ceremony [16]byte
	if _, err := rand.Read(ceremony[:]); err != nil {
		t.Fatalf("crypto/rand: %v", err)
	}
	sig, err := ks.SignRelayAuth(relay.ConsentMessage(hex.EncodeToString(ceremony[:]), relay.RoutingID(mPub)))
	if err != nil {
		t.Fatalf("phone signs its route consent: %v", err)
	}
	if err := machineRelay.AuthorizeDevice(ctx, ks.RelayAuthPublic(),
		relay.MarshalConsent(hex.EncodeToString(ceremony[:]), sig)); err != nil {
		t.Fatalf("authorize phone: %v", err)
	}

	store, err := phonecore.OpenStore(filepath.Join(stateDir, phonecore.StateFileName), machine, wake, content)
	if err != nil {
		t.Fatalf("open phone state: %v", err)
	}
	if err := store.Save(phonecore.State{
		Machine:             machine,
		MachineSignPub:      signPub,
		MachineRelayAuthPub: mPub,
		OperatorNamespace:   "owner",
		RoutingID:           phoneTarget,
		EpochID:             epoch,
		Keys:                keys,
	}); err != nil {
		t.Fatalf("seed phone state: %v", err)
	}

	sink := remotegw.NewRelaySink(remotegw.RelayConfig{
		Appender:       machineRelay,
		Target:         phoneTarget,
		Machine:        machine,
		EpochID:        epoch,
		Key:            keys.ContentKey,
		RecipientKeyID: crypto.KeyID(ks.RecipientPublic()),
		SenderKeyID:    crypto.KeyID(machineID.RecipientPublic()),
	})

	app, err := NewApp(&Config{StateDir: dir, MachineID: machine, RelayURL: phoneRelayURL(srv.URL())}, custody)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	if err := app.Start(); err != nil {
		t.Fatalf("App.Start: %v", err)
	}
	return app, sink
}

func committeeEvent(t *testing.T, sink *remotegw.RelaySink, machine string, cursor uint64) {
	t.Helper()
	if err := sink.Event(schema.JournalRecord{
		Cursor: cursor, SessionID: machine + "/committee", Type: "launched", Group: "working",
	}); err != nil {
		t.Fatalf("sink.Event(%d): %v", cursor, err)
	}
}

// TestCommittee_WaitSupportIsRenegotiatedPerConnection: the phone lands in the poll
// fallback against a pre-wait relay -- selected by the hello capability set, with ZERO
// blind mailbox_wait probes -- and, once the relay is upgraded (the next connection's
// hello advertises "wait"), the SAME process drains through the wait tail again. The
// verdict belongs to the connection, not to the process (bead agents-tracker-zphd).
func TestCommittee_WaitSupportIsRenegotiatedPerConnection(t *testing.T) {
	// Shorten the per-wait bound the way r9_waitfallback_test does: on a tree where the
	// verdict still comes from a blind probe's timeout, this keeps the RED run from
	// sitting out the full 35 s production bound. Under negotiation it is never reached.
	oldTimeout := waitTimeout
	waitTimeout = 1 * time.Second
	t.Cleanup(func() { waitTimeout = oldTimeout })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	proxyHolder := &struct{ p *committeeProxy }{}
	app, sink := committeeRig(t, ctx, func(realURL string) string {
		proxyHolder.p = newCommitteeProxy(t, realURL, true)
		return proxyHolder.p.URL()
	})
	proxy := proxyHolder.p
	const machine = "committee-machine-endpoint"

	// GENERATION 1: the old relay. Delivery must arrive through the poll, with the wait
	// op never so much as attempted.
	committeeEvent(t, sink, machine, 1)
	r9Eventually(t, "the journal event never reached the phone through the old relay's poll fallback",
		func() bool { return r9SawCursor(t, app, 1) })

	helloStripped, waitRefused, _, readForwarded, _ := proxy.counts()
	if helloStripped == 0 {
		t.Errorf("the phone never negotiated hello with the old relay; wait support must be " +
			"derived from the advertised capability set, not probed blind")
	}
	if waitRefused != 0 {
		t.Errorf("the phone sent %d mailbox_wait ops to a relay whose hello did not advertise "+
			"\"wait\"; a capability the relay does not claim must never be probed -- the probe's "+
			"MsgError refusal is exactly the uncorrelated frame that desynchronises the reply "+
			"stream (H1)", waitRefused)
	}
	if readForwarded == 0 {
		t.Error("no mailbox_read crossed the proxy: the event arrived outside the compatibility poll")
	}
	if got := app.waitSupport.Load(); got != waitUnsupported {
		t.Errorf("waitSupport = %d against the old relay, want waitUnsupported (%d)", got, waitUnsupported)
	}

	// THE RELAY UPGRADES: every connection after the first is forwarded verbatim to the
	// real (wait-supporting) relay. Sever the old-relay connection; the phone reconnects.
	proxy.Kill()

	// GENERATION 2: same process, upgraded relay. The hello advertises "wait", so the
	// drain must park the wait tail again -- a process-sticky verdict polls forever.
	committeeEvent(t, sink, machine, 2)
	r9Eventually(t, "the post-upgrade event never reached the phone", func() bool {
		return r9SawCursor(t, app, 2)
	})
	r9Eventually(t, "no mailbox_wait crossed the proxy after the relay upgrade: the poll verdict "+
		"stuck to the process, so the upgraded relay's wait tail is never used until the app is "+
		"killed (bead agents-tracker-zphd)", func() bool {
		_, _, waitForwarded, _, _ := proxy.counts()
		return waitForwarded > 0
	})
	_, waitRefusedAfter, _, _, _ := proxy.counts()
	if waitRefusedAfter != waitRefused {
		t.Errorf("wait ops were refused after the upgrade (%d -> %d); the verbatim connection "+
			"forwards them, so these reached the OLD-relay connection", waitRefused, waitRefusedAfter)
	}
}

// TestCommittee_AnOldRelayIsNeverProbedWithMailboxDiscard pins the destructive
// operation to the CURRENT connection's hello verdict. A pre-recovery relay answers an
// unknown op with an ordinary in-order error; sending the op anyway would both violate
// negotiation and risk shifting the connection's request/reply pairing. The nil-like
// alternative is also the safe compatibility behavior: keep the durable pending marker
// and retry only after a connection advertises recovery support.
func TestCommittee_AnOldRelayIsNeverProbedWithMailboxDiscard(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	proxyHolder := &struct{ p *committeeProxy }{}
	app, _ := committeeRig(t, ctx, func(realURL string) string {
		proxyHolder.p = newCommitteeProxy(t, realURL, true)
		return proxyHolder.p.URL()
	})
	proxy := proxyHolder.p
	r9Eventually(t, "the phone never completed capability negotiation with the old relay", func() bool {
		state, err := app.ConnectionState()
		return err == nil && state == connOnline
	})
	if app.mailboxRecoverySupported.Load() {
		t.Fatal("old relay connection retained mailbox recovery support absent from hello")
	}

	token, err := app.core.BeginRelayDiscardRecovery()
	if err != nil || token == "" {
		t.Fatalf("seed durable pending recovery = %q, %v", token, err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := app.requestMailboxDiscard()
		result <- err
	}()
	select {
	case err := <-result:
		if !errors.Is(err, relay.ErrPeerCapabilityUnavailable) {
			t.Fatalf("old-relay recovery = %v, want ErrPeerCapabilityUnavailable", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("old-relay recovery did not refuse promptly from the negotiated verdict")
	}
	if got := proxy.mailboxDiscardProbes(); got != 0 {
		t.Fatalf("phone sent %d mailbox_discard ops despite the old relay omitting recovery support", got)
	}
	if got := app.core.DiscardRecoveryToken(); got != token {
		t.Fatalf("old-relay refusal changed durable pending token %q to %q", token, got)
	}
}

// TestCommittee_TheWaitDrainAcksOffTheDeliveryPath: across a quiet-then-burst transition
// at the specified 8 frames/s, the drain's metered mailbox ops must stay under the
// relay's OpsPerMin window (600, relay.DefaultConfig), which requires the acks to ride
// transport.AckBatcher's 1/s cadence instead of one synchronous ack per delivered page.
func TestCommittee_TheWaitDrainAcksOffTheDeliveryPath(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	proxyHolder := &struct{ p *committeeProxy }{}
	app, sink := committeeRig(t, ctx, func(realURL string) string {
		proxyHolder.p = newCommitteeProxy(t, realURL, false)
		return proxyHolder.p.URL()
	})
	proxy := proxyHolder.p
	const machine = "committee-machine-endpoint"

	r9Eventually(t, "the phone never came online through the counting proxy", func() bool {
		st, err := app.ConnectionState()
		return err == nil && st == "online"
	})

	// QUIET: the drain parks its wait and banks pacer tokens -- the regime in which a
	// following burst is admitted at arrival rate rather than spaced.
	time.Sleep(2 * time.Second)

	_, _, waits0, reads0, acks0 := proxy.counts()
	start := time.Now()

	// BURST: 8 frames/s for 5 s, the specified arrival rate the codex probe measured the
	// synchronous ack path against.
	const frames = 40
	for i := 1; i <= frames; i++ {
		committeeEvent(t, sink, machine, uint64(i))
		time.Sleep(125 * time.Millisecond)
	}
	r9Eventually(t, "the burst never fully reached the phone", func() bool {
		return r9SawCursor(t, app, frames)
	})
	elapsed := time.Since(start)

	_, _, waits1, reads1, acks1 := proxy.counts()
	waits, reads, acks := waits1-waits0, reads1-reads0, acks1-acks0

	if reads != 0 {
		t.Errorf("the phone issued %d mailbox_read ops during the burst; the wait tail must not "+
			"fall back to the poll under load", reads)
	}
	// The batcher's ceiling is 1 ack/s (transport.MaxDrainAcksPerSec); +2 absorbs the
	// tick straddling both ends of the window. The synchronous path acks once per
	// delivered page -- ~one per frame at this arrival rate -- and lands far above.
	if maxAcks := int(elapsed/time.Second) + 2; acks > maxAcks {
		t.Errorf("the drain issued %d mailbox_ack ops in %.1fs (one per delivered page); batched "+
			"acking (transport.AckBatcher) is bounded by %d in this window. At 8 frames/s the "+
			"synchronous shape spends the relay's whole OpsPerMin quota (600) in ~40 s -- the relay "+
			"then refuses the very ops the live tail runs on", acks, elapsed.Seconds(), maxAcks)
	}
	// The quota statement itself, §6.0's window: the burst's metered op rate, held for a
	// full minute, must stay under the relay's default OpsPerMin.
	quota := relay.DefaultConfig().Quotas.OpsPerMin
	if perMin := float64(waits+reads+acks) * 60 / elapsed.Seconds(); perMin >= float64(quota) {
		t.Errorf("the drain's metered ops extrapolate to %.0f/min at the specified 8 frames/s "+
			"(%d waits + %d reads + %d acks in %.1fs); the relay's OpsPerMin default is %d, and a "+
			"drain that meters past it is knocked offline by the relay it is draining",
			perMin, waits, reads, acks, elapsed.Seconds(), quota)
	}
}
