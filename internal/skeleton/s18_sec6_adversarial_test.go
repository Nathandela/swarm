package skeleton

// Slice S18 -- PB-SEC-6:
//
//	"The app cannot bypass any server-side control: kill switch, lease, capability,
//	 expiry, seq gating stay authoritative server-side."
//	Criterion: "Adversarial test through the real transport: no typing without a lease
//	 or while the kill switch is engaged."
//
// ============================================================================
// THIS FILE IS GREEN AT HEAD, AND SAYS SO RATHER THAN CLAIMING A RED IT DOES NOT HAVE.
// ============================================================================
//
// PB-SEC-6 is a RE-ASSERTION requirement: its criterion asks for an adversarial test, not for
// a change. Every assertion below passed on the first run against the tree as it stood, which
// is the correct outcome -- the machine's refusals are already in place (protocol/server.go
// controlGateOpen's five clauses, remotegw LeaseManager.Input's drop, the gateway's durable
// inbound high-water). Labelling this file "FAILING-FIRST" would be a false claim, and a
// header asserting a property the file does not have is the exact defect class this project
// keeps finding.
//
// WHAT WAS DONE INSTEAD, because a green suite proves nothing on its own. Every refusal
// assertion here was run against a deliberately BROKEN version of the control it guards, and
// every one failed. The mutations, run at RED time:
//
//	no lease      -> grant a lease for that session      -> the keystroke arrives, test fails
//	expiry        -> do not wait past the signed horizon -> the keystroke arrives, test fails
//	seq gating    -> re-seal at a fresh seq, not replay  -> the marker types twice, test fails
//	kill switch   -> do not engage it                    -> the keystroke arrives, test fails
//	capability    -> CapFull instead of CapReadApprove   -> the keystroke arrives, test fails
//	client guard  -> apply a lease to the phone's state  -> the anti-vacuity check fires
//
// A further probe stubbed the whole surface out (the gateway Service constructed but never
// started) and ran the file against it: all eight tests fail. There is no assertion here that
// a dead machine would satisfy.
//
// THE ADVERSARY MODEL, stated first because it is what makes or breaks this file.
//
// The phone is the thing we are trying to make misbehave. Every check on the handset is a
// COURTESY: an attacker holding the device holds the whole client, so a test that drives the
// facade and observes a client-side refusal proves nothing at all about this requirement --
// it proves the courtesy is still installed. What PB-SEC-6 asks is whether the MACHINE still
// refuses once the courtesy is gone.
//
// So the attacker here is a REAL phonecore.Core -- real sealed device keys, a real epoch
// content key, a real durable send-sequencer, and a real relay connection -- whose
// client-side lease gate has been REMOVED. The gate is a single call in the bound facade:
//
//	mobile/commands.go:238,270,303,364 -> core.Leases().Require(session, time.Now())
//
// and everything below it is the seal-and-append this file performs directly. Bypassing it
// is therefore not a simulation of an attack; it is the same code path the facade takes with
// four lines deleted, which is precisely the modification an attacker makes.
//
// WHY NOT internal/phonesim. phonesim never constructs a phonecore.Core -- it holds a
// crypto.KeyStore and a bare Sequencer and calls the package-level seal functions (see
// phonesim.go Type/TakeControl). It therefore has NO lease gate to remove, and a "bypass"
// through it would be a bypass of nothing: the test could not distinguish a server that
// refuses from a client that never checked. It would also be unable to make the
// anti-vacuity assertion below, which is the half that gives the rest of the file meaning.
//
// EVERY REFUSAL HERE CARRIES ITS OWN POSITIVE CONTROL. Standing defect class (iii) -- a test
// that passes because its subject became unreachable -- is the live hazard for a "the bytes
// must NOT arrive" assertion: a rig with a broken relay, an unstarted gateway or a dead PTY
// passes every one of them.
//
// A SHARED positive control is not enough, and that is not a hypothetical. The vacuous-pass
// probe run while writing this file stubbed the surface out (the gateway Service constructed
// but never started) and ran everything against it. Four subtests failed on their own positive
// controls, as intended -- and TWO PASSED: the no-lease arm, which is the criterion's first
// named control, and the capability arm. Both had been leaning on a positive control
// established earlier on the same rig. Each now proves the path is live at the moment of its
// own attack:
//
//   - the no-lease arm sends a CANARY keystroke on the leased session at the same time as the
//     attack on the unleased one, so both cross the same relay, gateway and daemon within the
//     same second and the only difference between them is the lease;
//   - the capability arm cannot use a keystroke at all -- no lease may ever be granted to it --
//     so it asserts on the machine-sealed REFUSAL coming back, which is positive evidence that
//     the command reached the daemon and was judged there.
//
// WHAT IS NOT CLAIMED. Nothing here touches a handset, a real biometric, or hardware key
// custody: PB-E2E-5 stays deferred and no assertion in this file may be read as covering any
// part of it. This file is about the MACHINE's refusals, observed through the real relay,
// the real gateway and the real daemon on one host.

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/Nathandela/swarm/internal/remotegw"
)

// s18ArrivalWindow is how long a keystroke is given to travel phone -> relay -> gateway ->
// daemon -> PTY before it is called lost. It is deliberately far above §6.0's p99 of 800 ms:
// this file measures REFUSALS, and a window tuned to the latency budget would turn a loaded
// box into a false "the server refused" on the positive controls.
const s18ArrivalWindow = 8 * time.Second

// s18SilenceWindow is how long a REFUSED keystroke is watched for before the refusal is
// believed. It must be comfortably longer than a legitimate delivery takes, or the file
// would report "the server refused" whenever the box is slow -- the strongest possible form
// of a guard that cannot fail, since every assertion here is a negative one.
const s18SilenceWindow = 3 * time.Second

// ---------------------------------------------------------------------------
// The rig: the production chain with a REAL phone core at the phone end.
// ---------------------------------------------------------------------------

// s18Sealer is a real AES-GCM KEK standing in for the Android Keystore, so the Core under
// test is one that actually seals its device keys and its state blob at rest (PB-KEY-9).
// phonecore.InsecureCleartextSealer would also compile, and is deliberately NOT used: this
// file's Core must be shaped like the shipped one, and "the attacker's own client" is a
// weaker object if it is a client no user could have.
type s18Sealer struct{ aead cipher.AEAD }

func s18NewSealer(t *testing.T) *s18Sealer {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("sealer key: %v", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("aes: %v", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("gcm: %v", err)
	}
	return &s18Sealer{aead: aead}
}

func (s *s18Sealer) Seal(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return s.aead.Seal(nonce, nonce, plaintext, nil), nil
}

func (s *s18Sealer) Open(sealed []byte) ([]byte, error) {
	n := s.aead.NonceSize()
	if len(sealed) < n {
		return nil, errors.New("s18: sealed blob too short")
	}
	return s.aead.Open(nil, sealed[:n], sealed[n:], nil)
}

// s18Rig is the whole remote wire: a real in-process relay, a real daemon with a remote
// socket, a real gateway Service, and a real phonecore.Core as the phone.
type s18Rig struct {
	t   *testing.T
	ctx context.Context

	sk         *Daemon
	core       *phonecore.Core
	keys       crypto.EpochKeys
	epochID    uint32
	phoneRelay *relay.Client
	machineTgt string
	machine    string // daemon endpoint id
	deviceID   string

	localID    string // the fake session's LOCAL id (what TerminalTap takes)
	namespaced string // the same session as the phone addresses it
	watcher    *s18Tap

	// gatewayDone carries remotegw.Service.Run's return value. PB-SEC-7 asserts on it: the
	// requirement's chain ends "gateway severs and exits", and an exit is only observable
	// here. stopGatewayWait tells the rig's cleanup that Run has already been collected.
	gatewayDone     <-chan error
	stopGatewayWait func()
}

// s18NewRig builds the chain. It duplicates the bootstrap in s6b_input_latency_test.go
// rather than calling it because that rig's phone is a phonesim and this file's whole
// argument rests on the phone being a real Core (see the header).
//
// The pairing/enrolment handshake is deliberately NOT run: PB-SEC-6 is about the machine's
// refusals once a device is paired, and the device record is therefore seeded directly into
// the daemon registry the way registerPhone already does for the command E2Es. What must be
// real is the Core, the transport and the daemon -- and all three are.
func s18NewRig(t *testing.T, cap device.Capability) *s18Rig {
	t.Helper()

	rcfg := relay.DefaultConfig()
	rcfg.DBPath = filepath.Join(t.TempDir(), "relay.db")
	relaySrv, err := relay.New(rcfg)
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := relaySrv.Start(ctx); err != nil {
		t.Fatalf("relay start: %v", err)
	}
	t.Cleanup(func() { _ = relaySrv.Close() })

	sk, rsock := assembleWithRemote(t)

	// The machine identity is what HOLDS the epoch, and coreAPI.rotateEpoch is a documented
	// NO-OP when the file is absent ("pairing unprovisioned: no epoch to rotate"). A rig
	// without one would make PB-SEC-7's rotation assertion fail for a reason that is a
	// property of the harness rather than of the product -- and, worse, would let a future
	// implementation that never rotates look identical. So it is provisioned here, for every
	// rig, and PB-SEC-7 reads the epoch back out of this same file.
	writeTestIdentity(t, sk.api.stateDir, "s18-rig.local")

	const epochID = uint32(1)
	keys, err := crypto.NewEpochKeys()
	if err != nil {
		t.Fatalf("epoch keys: %v", err)
	}

	// The phone: a REAL Core with REAL sealed custody.
	core, err := phonecore.Resume(phonecore.Config{
		Dir:           t.TempDir(),
		Machine:       sk.api.endpointID,
		WakeSealer:    s18NewSealer(t),
		ContentSealer: s18NewSealer(t),
	})
	if err != nil {
		t.Fatalf("phonecore.Resume: %v", err)
	}
	ks := core.KeyStore()

	deviceID := device.DeviceIDFor(ks.CommandSigningPublic())
	if err := sk.api.devices.Add(device.Record{
		DeviceID:       deviceID,
		Name:           "s18-adversary",
		NoiseStaticPub: make([]byte, 32),
		RelayAuthPub:   ks.RelayAuthPublic(),
		CommandSignPub: ks.CommandSigningPublic(),
		RecipientPub:   make([]byte, 32),
		Capability:     cap,
		PairedAt:       time.Now(),
		GrantedEpoch:   epochID,
	}); err != nil {
		t.Fatalf("register the phone with the daemon: %v", err)
	}

	mPub, mPriv, _ := ed25519.GenerateKey(nil)
	pPub, pPriv, _ := ed25519.GenerateKey(nil)
	machineRelay, err := relay.Dial(ctx, relaySrv.URL(), relayAuth(mPub, mPriv))
	if err != nil {
		t.Fatalf("machine dial: %v", err)
	}
	t.Cleanup(func() { _ = machineRelay.Close() })
	phoneRelay, err := relay.Dial(ctx, relaySrv.URL(), relayAuth(pPub, pPriv))
	if err != nil {
		t.Fatalf("phone dial: %v", err)
	}
	t.Cleanup(func() { _ = phoneRelay.Close() })
	if err := machineRelay.AuthorizeDevice(ctx, pPub,
		e2eConsent(pPriv, relay.RoutingID(mPub))); err != nil {
		t.Fatalf("machine authorize phone: %v", err)
	}
	if err := phoneRelay.AuthorizeDevice(ctx, mPub,
		e2eConsent(mPriv, relay.RoutingID(pPub))); err != nil {
		t.Fatalf("phone authorize machine: %v", err)
	}

	// Give the Core the epoch coordinates a grant would have installed, so its durable
	// sequencer is bound to the epoch and its content key is the one the gateway holds.
	st := core.State()
	st.Machine = sk.api.endpointID
	st.EpochID = epochID
	st.Keys = keys
	st.RoutingID = phoneRelay.RoutingID()
	if err := core.Save(st); err != nil {
		t.Fatalf("seed the phone's epoch coordinates: %v", err)
	}

	inbound, err := remotegw.OpenInboundState(filepath.Join(t.TempDir(), "inbound.json"), sk.api.endpointID)
	if err != nil {
		t.Fatalf("OpenInboundState: %v", err)
	}
	svc := remotegw.NewService(remotegw.ServiceConfig{
		DaemonSocket: rsock,
		Relay:        machineRelay,
		PhoneTarget:  phoneRelay.RoutingID(),
		Key:          keys.ContentKey,
		EpochID:      epochID,
		Inbound:      inbound,
		// StateDir + DeviceID arm the post-revocation exit (service.go deviceRevoked). They
		// are set for EVERY rig, not only PB-SEC-7's: a gateway assembled without them keeps
		// reconnecting after a revoke, which is the configuration PB-SEC-7 exists to forbid,
		// and a rig that quietly used it would make the S18 evidence describe a gateway
		// production does not run.
		StateDir:       sk.api.stateDir,
		DeviceID:       deviceID,
		ReconnectDelay: 50 * time.Millisecond,
	})
	svcCtx, svcCancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- svc.Run(svcCtx) }()
	stopped := false
	t.Cleanup(func() {
		svcCancel()
		if stopped {
			return // PB-SEC-7 already observed Run returning
		}
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("gateway service did not stop within 2s of cancel")
		}
	})

	meta := launchFake(t, sk, "idle 600s\n")
	r := &s18Rig{
		t: t, ctx: ctx,
		sk: sk, core: core, keys: keys, epochID: epochID,
		phoneRelay: phoneRelay, machineTgt: machineRelay.RoutingID(),
		machine: sk.api.endpointID, deviceID: deviceID,
		localID:         meta.ID,
		namespaced:      protocol.NamespacedID(sk.api.endpointID, meta.ID),
		gatewayDone:     done,
		stopGatewayWait: func() { stopped = true },
	}
	r.watcher = s18WatchPTY(t, sk, meta.ID)
	return r
}

// takeControl signs and seals a REAL take_control and appends it to the machine's mailbox.
// It is the legitimate path, used to establish the positive controls; signedFor bounds the
// device-signed horizon so the expiry subtest can ask for a short-lived lease.
func (r *s18Rig) takeControl(session, operationID string, signedFor time.Duration, ttlSeconds int) {
	r.t.Helper()
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		r.t.Fatalf("gate token: %v", err)
	}
	gateToken := hex.EncodeToString(raw)
	cmd, err := phonecore.SignTakeControl(r.core.KeyStore(), phonecore.TakeControlInput{
		Machine:     r.machine,
		Session:     session,
		OperationID: operationID,
		ExpiresAt:   time.Now().Add(signedFor),
		GateToken:   gateToken,
	})
	if err != nil {
		r.t.Fatalf("sign take_control: %v", err)
	}
	seq, err := r.core.Seq().NextCommand()
	if err != nil {
		r.t.Fatalf("allocate command seq: %v", err)
	}
	env, err := phonecore.SealTakeControlEnvelope(r.keys.ContentKey, r.epochID, seq, cmd, gateToken, ttlSeconds)
	if err != nil {
		r.t.Fatalf("seal take_control: %v", err)
	}
	if _, err := r.phoneRelay.MailboxAppend(r.ctx, r.machineTgt, env); err != nil {
		r.t.Fatalf("append take_control: %v", err)
	}
}

// typeWithTheClientGuardRemoved IS THE ATTACK. It seals a keystroke burst under the phone's
// own epoch content key, with the phone's own durable sequencer, and appends it to the
// machine's mailbox -- exactly what the facade does AFTER core.Leases().Require has returned
// nil, with that call deleted.
//
// It returns the wire bytes so a caller can re-append them (the seq-gating subtest).
func (r *s18Rig) typeWithTheClientGuardRemoved(session string, data []byte) []byte {
	r.t.Helper()
	seq, err := r.core.Seq().NextInput()
	if err != nil {
		r.t.Fatalf("allocate input seq: %v", err)
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

// requireTheClientGuardWouldHaveRefused is the ANTI-VACUITY half, and without it the whole
// file is worthless: if the phone's own gate were already open, "the bytes did not arrive"
// would say nothing about the server, because nothing would have been bypassed.
//
// It asserts the guard the facade calls is live and closed for this session RIGHT NOW.
//
// THE GATE IS SHUT FOR THE WHOLE RIG, DELIBERATELY. phonecore.LeaseState is fed from the
// phone's inbound drain (MailboxRouter -> LeaseState.Apply), and this rig never runs one:
// an attacker who has deleted the Require call has no reason to keep tracking leases either,
// and wiring the drain would only re-supply the state the attack discards. The consequence is
// the argument this file makes -- the client's gate is shut in EVERY subtest, including the
// positive control where the keystroke arrives anyway. So the difference between "arrived"
// and "refused" is never the client; it is only ever the machine.
//
// It is not a check that can pass for the wrong reason: TestMUTATION_ClientGuardCheck (the
// probe run at RED time) opened the gate by applying a lease and this helper fired.
func (r *s18Rig) requireTheClientGuardWouldHaveRefused(session string) {
	r.t.Helper()
	err := r.core.Leases().Require(session, time.Now())
	if err == nil {
		r.t.Fatalf("PB-SEC-6: the phone's own lease gate (phonecore.LeaseState.Require, the call "+
			"mobile/commands.go makes before every keystroke) reports session %q as typeable. "+
			"Nothing is being bypassed, so every 'the server refused' assertion below would be "+
			"vacuous -- they would pass against a machine with no controls at all", session)
	}
	if !errors.Is(err, phonecore.ErrNoLease) && !errors.Is(err, phonecore.ErrLeaseExpired) {
		r.t.Fatalf("PB-SEC-6: the phone's lease gate refused session %q with an unrecognised "+
			"error %v; the bypass below may not be bypassing the lease gate at all", session, err)
	}
}

// awaitSealedReply drains the phone's own relay mailbox and returns the machine-sealed reply
// carrying operationID, if one lands inside the window.
//
// It exists because ABSENCE IS A WEAK POSITIVE CONTROL. For the capability arm there is no
// keystroke that may legitimately arrive -- the whole point is that no lease is ever granted --
// so "nothing reached the PTY" cannot be distinguished from "nothing was running" by watching
// the PTY. The sealed refusal can: it is proof that the command travelled phone -> relay ->
// gateway -> daemon, was REFUSED there, and came back. That is a statement about the server.
//
// Envelopes are opened directly rather than through a crypto.MailboxReceiver: a receiver
// enforces the per-stream seq discipline, and this is an observer sharing the phone's mailbox
// with the app's own inbound path, not a second consumer of it.
func (r *s18Rig) awaitSealedReply(operationID string, within time.Duration) (schema.Control, bool) {
	r.t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		items, err := r.phoneRelay.MailboxRead(r.ctx, 0)
		if err != nil {
			r.t.Fatalf("phone mailbox read: %v", err)
		}
		for _, it := range items {
			env, err := crypto.ParseEnvelope(it.Envelope)
			if err != nil {
				continue
			}
			plain, err := crypto.OpenMailbox(r.keys.ContentKey, env)
			if err != nil {
				continue // a journal frame on another bucket, or not ours
			}
			var ctrl schema.Control
			if json.Unmarshal(plain, &ctrl) != nil {
				continue
			}
			if ctrl.OperationID == operationID {
				return ctrl, true
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return schema.Control{}, false
}

// marker mints a keystroke that is unique in this process, so its arrival at the PTY is
// attributable to exactly one send.
func s18Marker(what string) string {
	raw := make([]byte, 6)
	_, _ = rand.Read(raw)
	return fmt.Sprintf("SEC6-%s-%s", what, hex.EncodeToString(raw))
}

// ---------------------------------------------------------------------------
// The PTY tap. It COUNTS occurrences rather than latching first-seen, because the seq-gating
// subtest's whole question is whether a replayed frame types the same bytes a SECOND time.
// ---------------------------------------------------------------------------

type s18Tap struct {
	mu  sync.Mutex
	buf strings.Builder
}

func s18WatchPTY(t *testing.T, sk *Daemon, local string) *s18Tap {
	t.Helper()
	stream, err := sk.api.TerminalTap(local)
	if err != nil {
		t.Fatalf("TerminalTap(%s): %v", local, err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	w := &s18Tap{}
	go func() {
		for f := range stream.Frames() {
			w.mu.Lock()
			w.buf.Write(f)
			w.mu.Unlock()
		}
	}()
	return w
}

func (w *s18Tap) count(marker string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.Count(w.buf.String(), marker)
}

// awaitArrival blocks until the marker has been seen at least n times, or the window closes.
func (w *s18Tap) awaitArrival(marker string, n int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if w.count(marker) >= n {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// requireNeverArrives watches for the FULL silence window and fails if the marker ever shows
// up. It never returns early on success: a refusal believed after 10 ms is a refusal that
// would also be "observed" from a server which simply had not got round to the write yet.
func (w *s18Tap) requireNeverArrives(t *testing.T, marker, why string) {
	t.Helper()
	deadline := time.Now().Add(s18SilenceWindow)
	for time.Now().Before(deadline) {
		if n := w.count(marker); n > 0 {
			t.Fatalf("PB-SEC-6: %s -- the keystroke reached the session's PTY %d time(s). "+
				"The control is not authoritative server-side: a phone with its client-side "+
				"gate removed typed into a live shell", why, n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// PB-SEC-6.
// ---------------------------------------------------------------------------

// TestPBSEC6_ServerSideControlsHoldAgainstAPhoneWithItsGuardsRemoved is the requirement's
// headline. The subtests are ORDERED and share one rig: the kill-switch arm is last because
// engaging it is a machine-wide, durable act that severs every live lease, so a later subtest
// would be measuring the kill switch rather than its own control.
func TestPBSEC6_ServerSideControlsHoldAgainstAPhoneWithItsGuardsRemoved(t *testing.T) {
	r := s18NewRig(t, device.CapFull)

	// --- POSITIVE CONTROL ---------------------------------------------------
	//
	// Everything below asserts that bytes do NOT arrive. That family of assertion passes
	// against a rig where nothing works at all, so the path is demonstrated FIRST.
	t.Run("positive_control_a_leased_keystroke_reaches_the_pty", func(t *testing.T) {
		r.takeControl(r.namespaced, "op-s18-lease-1", 15*time.Minute, 3600)
		// The phone's own gate is SHUT here too -- see requireTheClientGuardWouldHaveRefused's
		// doc for why it is shut for the whole rig. That is what makes this the proof that the
		// bypass WORKS: identical client state, identical call path, and the bytes arrive.
		// Every refusal below is therefore attributable to the server and to nothing else.
		r.requireTheClientGuardWouldHaveRefused(r.namespaced)
		mk := s18Marker("live")
		r.typeWithTheClientGuardRemoved(r.namespaced, []byte(mk+"\n"))
		if !r.watcher.awaitArrival(mk, 1, s18ArrivalWindow) {
			t.Fatalf("PB-SEC-6: with a granted lease, a keystroke did NOT reach the PTY within %v. "+
				"The rig is not carrying traffic, so every refusal assertion in this file would "+
				"pass vacuously (standing defect class iii)", s18ArrivalWindow)
		}
	})

	// --- LEASE (the criterion's first named control) -------------------------
	//
	// The attack: a session the phone never took control of. The lease conn does not exist
	// on the gateway and the daemon's control gate is closed, so the ONLY thing that could
	// let these bytes through is a client-side check -- which is the thing being removed.
	t.Run("no_lease_the_server_refuses_the_keystroke", func(t *testing.T) {
		other := launchFake(t, r.sk, "idle 600s\n")
		otherNS := protocol.NamespacedID(r.machine, other.ID)
		tap := s18WatchPTY(t, r.sk, other.ID)

		r.requireTheClientGuardWouldHaveRefused(otherNS)

		// ITS OWN POSITIVE CONTROL, and it must be its own: relying on the earlier
		// positive_control subtest is not enough, and the vacuous-pass probe run at RED time
		// proved it -- with the gateway stubbed out entirely, every other subtest failed and
		// THIS ONE PASSED, because a dead rig delivers no keystroke to an unleased session
		// exactly as a working one does. That is standing defect class (iii) on the single
		// most important assertion in the file.
		//
		// The canary rides the LEASED session concurrently, so the two frames cross the same
		// relay, the same gateway and the same daemon within the same second, and the only
		// difference between them is the lease.
		canary := s18Marker("nolease-canary")
		r.typeWithTheClientGuardRemoved(r.namespaced, []byte(canary+"\n"))

		mk := s18Marker("nolease")
		r.typeWithTheClientGuardRemoved(otherNS, []byte(mk+"\n"))

		if !r.watcher.awaitArrival(canary, 1, s18ArrivalWindow) {
			t.Fatalf("PB-SEC-6: the canary keystroke on the LEASED session did not arrive "+
				"within %v, so the rig is not carrying traffic and the refusal below would be "+
				"observed from a dead machine as readily as from a refusing one", s18ArrivalWindow)
		}
		tap.requireNeverArrives(t, mk,
			"a phone that never took control of this session typed into it, while an "+
				"identically-sent keystroke on a LEASED session arrived")
	})

	// --- EXPIRY --------------------------------------------------------------
	//
	// The daemon's deadline is the EARLIEST of the device-signed ExpiresAt, now+TTLSeconds
	// and a 30-minute cap (protocol/server.go), and controlGateOpen re-checks it per
	// keystroke on the SERVER clock. The phone here signs a two-second horizon and then
	// keeps typing past it -- with no client-side expiry check in the way, because the call
	// that would have made one is the call being removed.
	t.Run("an_expired_lease_stops_accepting_keystrokes", func(t *testing.T) {
		sess := launchFake(t, r.sk, "idle 600s\n")
		ns := protocol.NamespacedID(r.machine, sess.ID)
		tap := s18WatchPTY(t, r.sk, sess.ID)

		r.takeControl(ns, "op-s18-expiring", 2*time.Second, 2)
		early := s18Marker("preexpiry")
		r.typeWithTheClientGuardRemoved(ns, []byte(early+"\n"))
		if !tap.awaitArrival(early, 1, s18ArrivalWindow) {
			t.Fatalf("PB-SEC-6: the short-lived lease never carried a keystroke at all, so the "+
				"post-expiry refusal below would prove nothing about expiry (it would hold for a "+
				"lease that was never granted). Waited %v", s18ArrivalWindow)
		}

		time.Sleep(3 * time.Second) // past the signed horizon and the TTL

		late := s18Marker("postexpiry")
		r.typeWithTheClientGuardRemoved(ns, []byte(late+"\n"))
		tap.requireNeverArrives(t, late,
			"a phone kept typing past the horizon its own take_control signed")
	})

	// --- SEQ GATING ----------------------------------------------------------
	//
	// The relay is untrusted and may redeliver. A captured, perfectly valid, correctly
	// signed input envelope re-appended must NOT type its bytes a second time: the gateway's
	// durable inbound high-water (PB-GW-1) is what stops it, and it is server-side.
	t.Run("a_replayed_input_envelope_does_not_type_twice", func(t *testing.T) {
		sess := launchFake(t, r.sk, "idle 600s\n")
		ns := protocol.NamespacedID(r.machine, sess.ID)
		tap := s18WatchPTY(t, r.sk, sess.ID)

		r.takeControl(ns, "op-s18-replay", 15*time.Minute, 3600)
		mk := s18Marker("replay")
		env := r.typeWithTheClientGuardRemoved(ns, []byte(mk+"\n"))
		if !tap.awaitArrival(mk, 1, s18ArrivalWindow) {
			t.Fatalf("PB-SEC-6: the frame being replayed never arrived the FIRST time, so " +
				"'it did not arrive twice' is not a statement about the replay guard")
		}

		// Re-append the very same sealed bytes, as a hostile relay would.
		if _, err := r.phoneRelay.MailboxAppend(r.ctx, r.machineTgt, env); err != nil {
			t.Fatalf("replay append: %v", err)
		}
		deadline := time.Now().Add(s18SilenceWindow)
		for time.Now().Before(deadline) {
			if n := tap.count(mk); n > 1 {
				t.Fatalf("PB-SEC-6: a replayed input envelope typed its bytes %d times. The "+
					"inbound seq gate is not authoritative server-side, so anything the relay "+
					"captured can be re-executed against a live shell", n)
			}
			time.Sleep(10 * time.Millisecond)
		}
	})

	// --- KILL SWITCH (the criterion's second named control) -------------------
	//
	// LAST, because SetRemoteControl(false) proactively severs every live lease machine-wide
	// (skeleton/killswitch.go), which would make any subsequent subtest measure this rather
	// than its own control.
	t.Run("the_kill_switch_halts_a_lease_that_is_already_live", func(t *testing.T) {
		sess := launchFake(t, r.sk, "idle 600s\n")
		ns := protocol.NamespacedID(r.machine, sess.ID)
		tap := s18WatchPTY(t, r.sk, sess.ID)

		r.takeControl(ns, "op-s18-killswitch", 15*time.Minute, 3600)
		before := s18Marker("ksbefore")
		r.typeWithTheClientGuardRemoved(ns, []byte(before+"\n"))
		if !tap.awaitArrival(before, 1, s18ArrivalWindow) {
			t.Fatalf("PB-SEC-6: no keystroke arrived while the kill switch was ON, so the " +
				"post-disable silence below would prove nothing about the kill switch")
		}

		if err := r.sk.api.SetRemoteControl(false); err != nil {
			t.Fatalf("engage the kill switch: %v", err)
		}
		if r.sk.api.RemoteControlEnabled() {
			t.Fatalf("PB-SEC-6: SetRemoteControl(false) returned nil but RemoteControlEnabled() " +
				"is still true; the switch under test is not engaged and the assertion below " +
				"would be measuring nothing")
		}

		after := s18Marker("ksafter")
		r.typeWithTheClientGuardRemoved(ns, []byte(after+"\n"))
		tap.requireNeverArrives(t, after,
			"a phone kept typing after the owner engaged the kill switch")

		// And a FRESH take_control while the switch is off must not reopen the path: a
		// mid-session halt that could be undone by re-taking control is not a kill switch.
		r.takeControl(ns, "op-s18-killswitch-retake", 15*time.Minute, 3600)
		retake := s18Marker("ksretake")
		r.typeWithTheClientGuardRemoved(ns, []byte(retake+"\n"))
		tap.requireNeverArrives(t, retake,
			"a phone re-took control while the kill switch was engaged and typed again")
	})
}

// TestPBSEC6_ACapabilityBelowFullCannotOpenALeaseOrType covers the requirement's third named
// control on its own rig, because the capability is a property of the registered device and
// cannot be changed mid-test without becoming a test of the registry instead.
//
// device.CapReadApprove maps ActionTakeControl (ActionControl) to false, and the daemon's
// requireRemoteAuthz is the choke point. The phone signs a perfectly valid take_control -- the
// signature verifies, the device is registered, the kill switch is on -- and is refused purely
// on its tier. With no lease granted, the bypassed keystroke has nothing to ride.
// ITS POSITIVE CONTROL IS THE SEALED REFUSAL, not an arriving keystroke. No keystroke may
// legitimately arrive on this rig at any point -- the device can never hold a lease -- so
// watching the PTY cannot tell a refusing machine from an absent one. The vacuous-pass probe
// run at RED time confirmed that: with the gateway stubbed out, this test PASSED.
//
// The refusal the gateway seals back (lease_confirm.go: "a refused lease that produces no
// reply is indistinguishable from one that is merely slow") is the proof that the command
// reached the daemon, was judged there, and was denied.
func TestPBSEC6_ACapabilityBelowFullCannotOpenALeaseOrType(t *testing.T) {
	r := s18NewRig(t, device.CapReadApprove)

	const opID = "op-s18-cap"
	r.takeControl(r.namespaced, opID, 15*time.Minute, 3600)

	reply, ok := r.awaitSealedReply(opID, s18ArrivalWindow)
	if !ok {
		t.Fatalf("PB-SEC-6: no sealed reply for take_control %q arrived within %v. Without it "+
			"there is no evidence the command reached the daemon at all, and the silence "+
			"asserted below would be the silence of a machine that never heard the request "+
			"rather than one that refused it", opID, s18ArrivalWindow)
	}
	if reply.Op != "error" {
		t.Fatalf("PB-SEC-6: the machine answered take_control from a read_approve device with "+
			"op=%q generation=%d, not a refusal. device.Capability.Allows maps take_control to "+
			"ActionControl, which read_approve does not permit", reply.Op, reply.Generation)
	}
	if reply.Generation != 0 {
		t.Errorf("PB-SEC-6: the refusal carries generation %d. A refusal naming a lease "+
			"generation is one the phone's LeaseState could open the gate on", reply.Generation)
	}

	r.requireTheClientGuardWouldHaveRefused(r.namespaced)

	mk := s18Marker("cap")
	r.typeWithTheClientGuardRemoved(r.namespaced, []byte(mk+"\n"))
	r.watcher.requireNeverArrives(t, mk,
		"a device holding read_approve took control and typed")
}
