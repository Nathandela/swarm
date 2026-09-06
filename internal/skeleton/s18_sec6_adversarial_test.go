package skeleton

// The S18 revoke tests need the same real daemon, gateway, phone core, and
// relay generation.  This fixture deliberately speaks relay-v2 directly: the
// retired relay-v1 client is not a test transport.

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remote/relayv2"
	"github.com/Nathandela/swarm/internal/remotegw"
)

const s18ArrivalWindow = 8 * time.Second
const s18SilenceWindow = 3 * time.Second

type s18Sealer struct{ aead cipher.AEAD }

func s18NewSealer(t *testing.T) *s18Sealer {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	return &s18Sealer{aead: aead}
}

func (s *s18Sealer) Seal(plain []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return s.aead.Seal(nonce, nonce, plain, nil), nil
}

func (s *s18Sealer) Open(sealed []byte) ([]byte, error) {
	n := s.aead.NonceSize()
	if len(sealed) < n {
		return nil, errors.New("s18: short sealed state")
	}
	return s.aead.Open(nil, sealed[:n], sealed[n:], nil)
}

type s18PhoneRelay struct {
	conn    *relayv2.Conn
	binding relayv2.Binding
	mu      sync.Mutex
	last    uint64
}

// s18ObservedMailbox only observes the real machine subscription at the command
// bridge seam. A stale envelope is deliberately retained (and therefore not ACKed),
// so the durable checkpoint must not be used as evidence it reached the gateway.
type s18ObservedMailbox struct {
	*relayv2.MachineMailbox
	mu       sync.Mutex
	observed map[uint64]bool
}

func (m *s18ObservedMailbox) note(items []relayv2.Item) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, item := range items {
		m.observed[item.Cursor] = true
	}
}

func (m *s18ObservedMailbox) MailboxRead(ctx context.Context, cursor uint64) ([]relayv2.Item, error) {
	items, err := m.MachineMailbox.MailboxRead(ctx, cursor)
	m.note(items)
	return items, err
}

func (m *s18ObservedMailbox) MailboxWait(ctx context.Context, cursor uint64) ([]relayv2.Item, bool, error) {
	items, timedOut, err := m.MachineMailbox.MailboxWait(ctx, cursor)
	m.note(items)
	return items, timedOut, err
}

func (m *s18ObservedMailbox) saw(cursor uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.observed[cursor]
}

func (p *s18PhoneRelay) MailboxAppend(ctx context.Context, target string, ciphertext []byte) (uint64, error) {
	if target != p.binding.MachineRID {
		return 0, errors.New("s18: append target is not the paired machine")
	}
	digest := sha256.Sum256(ciphertext)
	result, err := p.conn.Append(ctx, p.binding, base64.RawURLEncoding.EncodeToString(digest[:]), ciphertext)
	if err == nil {
		p.mu.Lock()
		p.last = result.Cursor
		p.mu.Unlock()
	}
	return result.Cursor, err
}

// hostileRedelivery uses a new relay id for captured bytes.  Normal callers use
// the stable ciphertext digest so uncertain appends dedupe; this one models the
// relay delivering the same valid envelope twice, which must reach the gateway
// and be rejected by its durable sequence gate.
func (p *s18PhoneRelay) hostileRedelivery(ctx context.Context, target string, ciphertext []byte) (uint64, error) {
	if target != p.binding.MachineRID {
		return 0, errors.New("s18: redelivery target is not the paired machine")
	}
	idInput := append([]byte("s18-hostile-redelivery:"), ciphertext...)
	digest := sha256.Sum256(idInput)
	result, err := p.conn.Append(ctx, p.binding, base64.RawURLEncoding.EncodeToString(digest[:]), ciphertext)
	if err != nil {
		return 0, err
	}
	if result.Deduped {
		return 0, errors.New("s18: hostile redelivery was deduped by relay message id")
	}
	return result.Cursor, nil
}

func (p *s18PhoneRelay) lastAppendCursor() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.last
}

type s18Rig struct {
	t          *testing.T
	ctx        context.Context
	sk         *Daemon
	core       *phonecore.Core
	keys       crypto.EpochKeys
	epochID    uint32
	phoneRelay *s18PhoneRelay
	phoneSub   *relayv2.Subscription
	mailbox    *s18ObservedMailbox
	machineTgt string
	machine    string
	deviceID   string
	localID    string
	namespaced string
	watcher    *s18Tap

	gatewayDone     <-chan error
	stopGatewayWait func()
}

func s18NewRig(t *testing.T, capability device.Capability) *s18Rig {
	t.Helper()
	baseURL := os.Getenv("SKELETON_RELAY_V2_HTTP")
	if baseURL == "" {
		t.Skip("SKELETON_RELAY_V2_HTTP is set by services/relay/test/session.sh")
	}

	sk, remoteSocket := assembleWithRemote(t)
	identity := writeAllowedRelayV2Identity(t, sk.api.stateDir, "s18-rig.local")
	profile := relayv2.Profile{
		RelayURL: baseURL, MachineRID: relayv2.RoutingID(identity.RelayAuthPublic()),
		OperatorNamespace: "local-test", Security: relay.Security{AllowLoopbackCleartext: true},
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	core, err := phonecore.Resume(phonecore.Config{
		Dir: t.TempDir(), Machine: sk.api.endpointID,
		WakeSealer: s18NewSealer(t), ContentSealer: s18NewSealer(t),
	})
	if err != nil {
		t.Fatalf("phonecore.Resume: %v", err)
	}
	ks := core.KeyStore()
	deviceID := device.DeviceIDFor(ks.CommandSigningPublic())
	if err := sk.api.devices.Add(device.Record{
		DeviceID: deviceID, Name: "s18-adversary", NoiseStaticPub: make([]byte, 32),
		RelayAuthPub: ks.RelayAuthPublic(), CommandSignPub: ks.CommandSigningPublic(),
		RecipientPub: make([]byte, 32), Capability: capability, PairedAt: time.Now(),
		GrantedEpoch: identity.EpochID(),
	}); err != nil {
		t.Fatalf("register phone: %v", err)
	}

	machineAuth := relayv2.Auth{PublicKey: identity.RelayAuthPublic(), Role: relayv2.RoleMachine,
		Purpose: relayv2.PurposeControl, Sign: func(message []byte) ([]byte, error) { return identity.RelayAuthSign(message), nil }}
	control, err := relayv2.Dial(ctx, profile, machineAuth)
	if err != nil {
		t.Fatalf("machine control dial: %v", err)
	}
	t.Cleanup(func() { control.Close() })
	ceremony := s18Ceremony(t)
	consentMessage := relayv2.ConsentMessage(ceremony, profile.MachineRID)
	consentSignature, err := ks.SignRelayAuth(consentMessage)
	if err != nil {
		t.Fatalf("sign relay consent: %v", err)
	}
	binding, err := control.Authorize(ctx, ks.RelayAuthPublic(), relayv2.MarshalConsent(ceremony, consentSignature))
	if err != nil {
		t.Fatalf("authorize phone generation: %v", err)
	}

	machineAuth.Purpose = relayv2.PurposeStream
	machineConn, err := relayv2.Dial(ctx, profile, machineAuth)
	if err != nil {
		t.Fatalf("machine stream dial: %v", err)
	}
	t.Cleanup(func() { machineConn.Close() })
	machineSub, err := machineConn.Subscribe(ctx, binding, relayv2.Checkpoint{})
	if err != nil {
		t.Fatalf("machine subscribe: %v", err)
	}
	machineMailbox, err := relayv2.NewMachineMailbox(machineSub)
	if err != nil {
		t.Fatalf("machine mailbox: %v", err)
	}
	observedMailbox := &s18ObservedMailbox{MachineMailbox: machineMailbox, observed: make(map[uint64]bool)}

	phoneConn, err := relayv2.Dial(ctx, profile, relayv2.Auth{PublicKey: ks.RelayAuthPublic(), Sign: ks.SignRelayAuth,
		Role: relayv2.RolePhone, Purpose: relayv2.PurposeStream})
	if err != nil {
		t.Fatalf("phone stream dial: %v", err)
	}
	t.Cleanup(func() { phoneConn.Close() })
	phoneBinding, err := phoneConn.PhoneBinding()
	if err != nil || phoneBinding != binding {
		t.Fatalf("phone binding = (%+v, %v), want %+v", phoneBinding, err, binding)
	}
	phoneSub, err := phoneConn.Subscribe(ctx, phoneBinding, relayv2.Checkpoint{})
	if err != nil {
		t.Fatalf("phone subscribe: %v", err)
	}

	state := core.State()
	state.Machine = sk.api.endpointID
	state.EpochID = identity.EpochID()
	state.Keys = identity.EpochKeys()
	state.RoutingID = binding.PeerRID
	if err := core.Save(state); err != nil {
		t.Fatalf("seed phone state: %v", err)
	}
	inbound, err := remotegw.OpenInboundState(filepath.Join(t.TempDir(), "inbound.json"), sk.api.endpointID)
	if err != nil {
		t.Fatalf("open inbound state: %v", err)
	}
	service := remotegw.NewService(remotegw.ServiceConfig{
		DaemonSocket: remoteSocket, Relay: observedMailbox, PhoneTarget: binding.PeerRID,
		Machine: sk.api.endpointID, Key: identity.EpochKeys().ContentKey, EpochID: identity.EpochID(),
		GrantSeq: identity.GrantSeq(), Inbound: inbound, StateDir: sk.api.stateDir, DeviceID: deviceID,
		ReconnectDelay: 50 * time.Millisecond,
	})
	serviceCtx, stopService := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- service.Run(serviceCtx) }()
	stopped := false
	t.Cleanup(func() {
		stopService()
		if stopped {
			return
		}
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("gateway did not stop after cancel")
		}
	})

	meta := launchFake(t, sk, "idle 600s\n")
	r := &s18Rig{
		t: t, ctx: ctx, sk: sk, core: core, keys: identity.EpochKeys(), epochID: identity.EpochID(),
		phoneRelay: &s18PhoneRelay{conn: phoneConn, binding: binding}, phoneSub: phoneSub,
		machineTgt: binding.MachineRID, machine: sk.api.endpointID, deviceID: deviceID,
		mailbox: observedMailbox,
		localID: meta.ID, namespaced: protocol.NamespacedID(sk.api.endpointID, meta.ID),
		gatewayDone: done, stopGatewayWait: func() { stopped = true },
	}
	r.watcher = s18WatchPTY(t, sk, meta.ID)
	return r
}

func s18Ceremony(t *testing.T) string {
	t.Helper()
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(raw[:])
}

func (r *s18Rig) takeControl(session, operationID string, signedFor time.Duration, ttlSeconds int) {
	r.t.Helper()
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		r.t.Fatal(err)
	}
	cmd, err := phonecore.SignTakeControl(r.core.KeyStore(), phonecore.TakeControlInput{
		Machine: r.machine, Session: session, OperationID: operationID, ExpiresAt: time.Now().Add(signedFor), GateToken: hex.EncodeToString(raw[:]),
	})
	if err != nil {
		r.t.Fatalf("sign take_control: %v", err)
	}
	seq, err := r.core.Seq().NextCommand()
	if err != nil {
		r.t.Fatalf("take_control sequence: %v", err)
	}
	env, err := phonecore.SealTakeControlEnvelope(r.keys.ContentKey, r.epochID, seq, cmd, hex.EncodeToString(raw[:]), ttlSeconds)
	if err != nil {
		r.t.Fatalf("seal take_control: %v", err)
	}
	if _, err := r.phoneRelay.MailboxAppend(r.ctx, r.machineTgt, env); err != nil {
		r.t.Fatalf("append take_control: %v", err)
	}
}

func (r *s18Rig) typeWithTheClientGuardRemoved(session string, data []byte) []byte {
	r.t.Helper()
	seq, err := r.core.Seq().NextInput()
	if err != nil {
		r.t.Fatalf("input sequence: %v", err)
	}
	env, err := phonecore.SealInputData(r.keys.ContentKey, r.epochID, seq, session, data)
	if err != nil {
		r.t.Fatalf("seal input: %v", err)
	}
	if _, err := r.phoneRelay.MailboxAppend(r.ctx, r.machineTgt, env); err != nil {
		r.t.Fatalf("append input: %v", err)
	}
	return env
}

func (r *s18Rig) requireTheClientGuardWouldHaveRefused(session string) {
	r.t.Helper()
	err := r.core.Leases().Require(session, time.Now())
	if err == nil {
		r.t.Fatalf("client lease gate accepted %q; the adversarial append bypasses nothing", session)
	}
	if !errors.Is(err, phonecore.ErrNoLease) && !errors.Is(err, phonecore.ErrLeaseExpired) {
		r.t.Fatalf("client lease gate refused %q with %v, not a lease refusal", session, err)
	}
}

func (r *s18Rig) awaitSealedReply(operationID string, within time.Duration) (schema.Control, bool) {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(r.ctx, within)
	defer cancel()
	for {
		delivery, err := r.phoneSub.Recv(ctx)
		if err != nil {
			return schema.Control{}, false
		}
		if err := r.phoneSub.Ack(ctx, delivery.Cursor); err != nil {
			r.t.Fatalf("phone reply ACK: %v", err)
		}
		env, err := crypto.ParseEnvelope(delivery.Ciphertext)
		if err != nil {
			continue
		}
		plain, err := crypto.OpenMailbox(r.keys.ContentKey, env)
		if err != nil {
			continue
		}
		var control schema.Control
		if json.Unmarshal(plain, &control) == nil && control.OperationID == operationID {
			return control, true
		}
	}
}

func s18Marker(what string) string {
	var raw [6]byte
	_, _ = rand.Read(raw[:])
	return fmt.Sprintf("S18-%s-%s", what, hex.EncodeToString(raw[:]))
}

type s18Tap struct {
	mu sync.Mutex
	b  strings.Builder
}

func s18WatchPTY(t *testing.T, sk *Daemon, local string) *s18Tap {
	t.Helper()
	stream, err := sk.api.TerminalTap(local)
	if err != nil {
		t.Fatalf("TerminalTap: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	tap := &s18Tap{}
	go func() {
		for frame := range stream.Frames() {
			tap.mu.Lock()
			tap.b.Write(frame)
			tap.mu.Unlock()
		}
	}()
	return tap
}

func (t *s18Tap) count(marker string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Count(t.b.String(), marker)
}

func (t *s18Tap) awaitArrival(marker string, n int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if t.count(marker) >= n {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func (t *s18Tap) requireNeverArrives(tb testing.TB, marker, why string) {
	tb.Helper()
	deadline := time.Now().Add(s18SilenceWindow)
	for time.Now().Before(deadline) {
		if n := t.count(marker); n > 0 {
			tb.Fatalf("%s: marker arrived %d time(s)", why, n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPBSEC6_ServerSideControlsHoldAgainstAPhoneWithItsGuardsRemoved(t *testing.T) {
	r := s18NewRig(t, device.CapFull)

	t.Run("positive_control_a_leased_keystroke_reaches_the_pty", func(t *testing.T) {
		r.takeControl(r.namespaced, "op-s18-lease-1", 15*time.Minute, 3600)
		r.requireTheClientGuardWouldHaveRefused(r.namespaced)
		marker := s18Marker("live")
		r.typeWithTheClientGuardRemoved(r.namespaced, []byte(marker+"\n"))
		if !r.watcher.awaitArrival(marker, 1, s18ArrivalWindow) {
			t.Fatalf("a leased adversarial keystroke did not reach the PTY within %v", s18ArrivalWindow)
		}
	})

	t.Run("no_lease_the_server_refuses_the_keystroke", func(t *testing.T) {
		other := launchFake(t, r.sk, "idle 600s\n")
		otherSession := protocol.NamespacedID(r.machine, other.ID)
		otherTap := s18WatchPTY(t, r.sk, other.ID)
		r.requireTheClientGuardWouldHaveRefused(otherSession)
		canary := s18Marker("nolease-canary")
		r.typeWithTheClientGuardRemoved(r.namespaced, []byte(canary+"\n"))
		refused := s18Marker("nolease")
		r.typeWithTheClientGuardRemoved(otherSession, []byte(refused+"\n"))
		if !r.watcher.awaitArrival(canary, 1, s18ArrivalWindow) {
			t.Fatal("the leased canary did not arrive; the no-lease refusal would be vacuous")
		}
		otherTap.requireNeverArrives(t, refused, "an unleased device typed into a live session")
	})

	t.Run("an_expired_lease_stops_accepting_keystrokes", func(t *testing.T) {
		session := launchFake(t, r.sk, "idle 600s\n")
		namespaced := protocol.NamespacedID(r.machine, session.ID)
		tap := s18WatchPTY(t, r.sk, session.ID)
		r.takeControl(namespaced, "op-s18-expiring", 2*time.Second, 2)
		early := s18Marker("preexpiry")
		r.typeWithTheClientGuardRemoved(namespaced, []byte(early+"\n"))
		if !tap.awaitArrival(early, 1, s18ArrivalWindow) {
			t.Fatal("the short-lived lease never delivered its positive control")
		}
		time.Sleep(3 * time.Second)
		late := s18Marker("postexpiry")
		r.typeWithTheClientGuardRemoved(namespaced, []byte(late+"\n"))
		tap.requireNeverArrives(t, late, "an expired lease kept accepting input")
	})

	t.Run("a_replayed_input_envelope_does_not_type_twice", func(t *testing.T) {
		session := launchFake(t, r.sk, "idle 600s\n")
		namespaced := protocol.NamespacedID(r.machine, session.ID)
		tap := s18WatchPTY(t, r.sk, session.ID)
		r.takeControl(namespaced, "op-s18-replay", 15*time.Minute, 3600)
		marker := s18Marker("replay")
		envelope := r.typeWithTheClientGuardRemoved(namespaced, []byte(marker+"\n"))
		if !tap.awaitArrival(marker, 1, s18ArrivalWindow) {
			t.Fatal("the replay positive control never arrived")
		}
		firstCursor := r.phoneRelay.lastAppendCursor()
		secondCursor, err := r.phoneRelay.hostileRedelivery(r.ctx, r.machineTgt, envelope)
		if err != nil {
			t.Fatalf("replay append: %v", err)
		}
		if secondCursor <= firstCursor {
			t.Fatalf("hostile redelivery cursor = %d, want a new cursor after %d", secondCursor, firstCursor)
		}
		deadline := time.Now().Add(s18ArrivalWindow)
		for time.Now().Before(deadline) {
			if r.mailbox.saw(secondCursor) {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !r.mailbox.saw(secondCursor) {
			t.Fatalf("gateway never received hostile redelivery cursor %d", secondCursor)
		}
		deadline = time.Now().Add(s18SilenceWindow)
		for time.Now().Before(deadline) {
			if n := tap.count(marker); n > 1 {
				t.Fatalf("replayed input reached the PTY %d times", n)
			}
			time.Sleep(10 * time.Millisecond)
		}
	})

	t.Run("the_kill_switch_halts_a_lease_that_is_already_live", func(t *testing.T) {
		session := launchFake(t, r.sk, "idle 600s\n")
		namespaced := protocol.NamespacedID(r.machine, session.ID)
		tap := s18WatchPTY(t, r.sk, session.ID)
		r.takeControl(namespaced, "op-s18-killswitch", 15*time.Minute, 3600)
		before := s18Marker("ksbefore")
		r.typeWithTheClientGuardRemoved(namespaced, []byte(before+"\n"))
		if !tap.awaitArrival(before, 1, s18ArrivalWindow) {
			t.Fatal("the kill-switch positive control never arrived")
		}
		if err := r.sk.api.SetRemoteControl(false); err != nil {
			t.Fatalf("engage kill switch: %v", err)
		}
		if r.sk.api.RemoteControlEnabled() {
			t.Fatal("kill switch remained enabled")
		}
		after := s18Marker("ksafter")
		r.typeWithTheClientGuardRemoved(namespaced, []byte(after+"\n"))
		tap.requireNeverArrives(t, after, "the kill switch did not halt the existing lease")
		r.takeControl(namespaced, "op-s18-killswitch-retake", 15*time.Minute, 3600)
		retake := s18Marker("ksretake")
		r.typeWithTheClientGuardRemoved(namespaced, []byte(retake+"\n"))
		tap.requireNeverArrives(t, retake, "the kill switch allowed a fresh take_control")
	})
}

func TestPBSEC6_ACapabilityBelowFullCannotOpenALeaseOrType(t *testing.T) {
	r := s18NewRig(t, device.CapReadApprove)
	const operationID = "op-s18-cap"
	r.takeControl(r.namespaced, operationID, 15*time.Minute, 3600)
	reply, ok := r.awaitSealedReply(operationID, s18ArrivalWindow)
	if !ok {
		t.Fatalf("no sealed refusal for take_control %q", operationID)
	}
	if reply.Op != protocol.OpError || reply.Generation != 0 {
		t.Fatalf("read_approve take_control reply = %+v, want refusal without generation", reply)
	}
	r.requireTheClientGuardWouldHaveRefused(r.namespaced)
	marker := s18Marker("cap")
	r.typeWithTheClientGuardRemoved(r.namespaced, []byte(marker+"\n"))
	r.watcher.requireNeverArrives(t, marker, "a read_approve device opened a lease and typed")
}
