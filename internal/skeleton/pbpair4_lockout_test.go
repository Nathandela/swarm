package skeleton

// PB-PAIR-4 / PB-SAS-4 -- A DECISION THE PHONE NEVER LEARNS LEAVES THE MACHINE HOLDING
// AUTHORITY FOR A HANDSET THAT HOLDS NOTHING, AND LOCKS THE OWNER OUT OF PAIRING.
//
// The machine's accept/decline decision is the last frame of the ceremony. Nothing
// acknowledges it. So the machine commits on the strength of having SENT it, and three
// entirely ordinary things -- a phone whose context dies, a relay that flips a bit, a relay
// that accepts the frame and delivers nothing -- all end the same way:
//
//	machineEnrolled = true, devicePinned = false
//
// measured by the round-5 crypto review as deterministic for the bit-flip, every attempt
// (ADR-007 B81(2)). The machine then refuses every further pairing -- "a device is already
// paired (single-device v1)" -- so the owner's only exit is `swarm remote revoke <device-id>`
// at the desktop. That is a relay-triggered lockout, and the relay is the declared adversary.
//
// THE PROPERTY, STATED SO THAT ANY CORRECT REMEDY PASSES.
//
// Perfect agreement between the two legs is unobtainable -- this is the two-generals problem,
// and a fifth frame only moves which side holds the residual uncertainty. What is obtainable,
// and what these tests demand, is that the uncertainty fall on the harmless side:
//
//	(1) NO ORPHAN AUTHORITY. If the phone never learned the machine's decision, the machine
//	    must hold no device authority: nothing paired, remote control not enabled. A machine
//	    that cannot know whether its "yes" arrived must resolve that doubt by not claiming a
//	    device.
//	(2) RECOVERABLE WITHOUT A DESKTOP REVOKE. Whatever state the failed ceremony leaves, a
//	    fresh pairing must run to completion -- no `swarm remote revoke`, no device id the
//	    owner has to go and find.
//
// Both clauses are satisfied by every remedy the record names. A FIFTH FRAME: the machine
// commits on the phone's acknowledgement, which never arrives, so it commits nothing.
// DEFERRED ENROLMENT: the machine mints authority when the phone is first seen, and it never
// is. A DURABLE PREPARE/COMMIT: the prepared record is not authority and does not block the
// next pairing. Nothing here names a frame, a field, a record or an order.
//
// WHY (2) IS "NO REVOKE" RATHER THAN "REVOKE IS THE ACCEPTED REMEDY". The lockout is chosen by
// the adversary at zero cost and repeats on every attempt, so a desktop revoke is not a
// recovery, it is a treadmill: revoke, pair, the relay flips the bit again, locked out again.
// A recovery step the adversary can invalidate for free is not one. Clause (1) is the
// independent half: even where the owner does revoke, the phantom device spent PB-STATE-10's
// single-device slot and turned the kill switch on for a handset that never existed.
//
// WHAT THIS DOES NOT ASSERT, DELIBERATELY. Nothing here says the owner's terminal must report
// the failure. Where a frame is genuinely lost the machine cannot know, so a pair_result that
// says "paired" is two generals, not a defect -- and forbidding it would block the very
// remedies above. PB-SAS-4 is fenced here by its CONSEQUENCE (a tampered decision must not
// leave the two sides disagreeing) rather than by its mechanism: a test asserting that the
// channel binding covers the decision would block a fifth-frame remedy that reaches agreement
// without extending the transcript.
//
// Reused (same package): assemble (serve_test.go), memRendezvous / rendezvousPair
// (enroll_e2e_test.go), dialRemote / rawRemote (remote_journal_test.go), awaitControl /
// recvDeviceEnd / phoneConsentFor / devLegResult (pairing_integration_test.go). No existing
// test is modified.

import (
	"context"
	"crypto/ed25519"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/pairing"
)

// decisionFault is what the relay does to the machine's decision frame.
type decisionFault int

const (
	faultNone decisionFault = iota
	// faultCorruptDecision flips one bit of the sealed decision frame: the relay is a
	// forwarder, so it cannot read or forge the frame, but it can damage it. ADR-007 B81(2)
	// measured the result -- device="decrypt decision: message authentication failed".
	faultCorruptDecision
	// faultDropDecision accepts the frame and delivers nothing, reporting success.
	// handleRendezvousSend does exactly this when the peer has detached or the 16-slot inbox
	// is full: measured at 25 frames accepted and 9 silently dropped. No malice required.
	faultDropDecision
)

// consentRecvOrdinal is how many frames the MACHINE has received once the device's consent
// (msg4) is in hand: msg1, msg3, msg4. The machine's next send is therefore its accept/decline
// decision -- the frame under test.
//
// It is expressed as "the first machine send after the consent has arrived" rather than as a
// send ordinal so that it keeps naming the decision under a remedy that adds frames: anything
// a fix appends to the ceremony comes after this point, not before it.
const consentRecvOrdinal = 3

// consentSendOrdinal is the device's msg4 among its own sends: msg1, msg3, msg4. A remedy that
// adds an acknowledgement adds a FOURTH device send, so killing the phone here still means it
// never acknowledged anything.
const consentSendOrdinal = 3

// phoneTTL is mobile/pairing.go's 60 s pairingTTL, scaled down. It is the HANDSET's own clock
// and is handed only to the handset leg -- the machine leg's window is the one BeginPairing
// derives from pairWindow, production's own value, and nothing here touches it. Injecting a
// deadline into the machine's Pair is what made b52_consent_release_test.go:223 vacuous.
//
// It exists because a shipped phone whose decision frame is DROPPED has nothing to react to:
// it parks on the receive until its own clock runs out. That park is the honest behaviour, so
// the test supplies the clock the real app has rather than pretending the phone gives up.
const phoneTTL = 5 * time.Second

// decisionFaultRendezvous is the MACHINE end of a rendezvous whose relay damages or drops the
// one frame nothing acknowledges. Every other frame, in both directions, is forwarded verbatim.
type decisionFaultRendezvous struct {
	*memRendezvous

	fault decisionFault

	mu      sync.Mutex
	recvs   int
	applied bool
}

var _ pairing.RendezvousTransport = (*decisionFaultRendezvous)(nil)

func (r *decisionFaultRendezvous) Recv(ctx context.Context) ([]byte, error) {
	msg, err := r.memRendezvous.Recv(ctx)
	if err == nil {
		r.mu.Lock()
		r.recvs++
		r.mu.Unlock()
	}
	return msg, err
}

func (r *decisionFaultRendezvous) Send(ctx context.Context, msg []byte) error {
	r.mu.Lock()
	isDecision := !r.applied && r.recvs >= consentRecvOrdinal
	if isDecision {
		r.applied = true
	}
	r.mu.Unlock()
	if !isDecision {
		return r.memRendezvous.Send(ctx, msg)
	}
	switch r.fault {
	case faultCorruptDecision:
		damaged := append([]byte(nil), msg...)
		damaged[len(damaged)-1] ^= 0x01
		return r.memRendezvous.Send(ctx, damaged)
	case faultDropDecision:
		return nil // the relay reports success and delivers nothing
	default:
		return r.memRendezvous.Send(ctx, msg)
	}
}

func (r *decisionFaultRendezvous) faultLanded() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.applied
}

// phoneDeathRendezvous is the DEVICE end of a rendezvous belonging to a phone whose context
// dies the instant its consent is on the wire -- its own 60 s pairingTTL elapsing, the app
// being killed, the network going away. The frame IS delivered first; only then does the phone
// stop existing.
//
// Recv is ctx-faithful the way relay.Conn is (a read on a dead ctx fails outright), which is
// what makes the death deterministic rather than a coin flip between a buffered frame and a
// cancelled context.
type phoneDeathRendezvous struct {
	pairing.RendezvousTransport

	cancel context.CancelFunc

	mu    sync.Mutex
	sends int
	died  bool
}

var _ pairing.RendezvousTransport = (*phoneDeathRendezvous)(nil)

func (p *phoneDeathRendezvous) Send(ctx context.Context, msg []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := p.RendezvousTransport.Send(ctx, msg); err != nil {
		return err
	}
	p.mu.Lock()
	p.sends++
	n := p.sends
	p.mu.Unlock()
	if n == consentSendOrdinal {
		p.mu.Lock()
		p.died = true
		p.mu.Unlock()
		p.cancel()
	}
	return nil
}

func (p *phoneDeathRendezvous) Recv(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return p.RendezvousTransport.Recv(ctx)
}

func (p *phoneDeathRendezvous) phoneDied() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.died
}

// injectFaultedPairing wires the coreAPI pairing seam exactly as injectPairing does, except
// that the FIRST rendezvous it hands out carries the named fault and every later one is
// healthy. The recovery pairing therefore runs over an ordinary relay, which is the point: the
// fault is transient (a bit, a full inbox, a dead handset), and the question is whether the
// machine is still usable afterwards.
func injectFaultedPairing(t *testing.T, sk *Daemon, fault decisionFault) (chan *memRendezvous, chan *decisionFaultRendezvous) {
	t.Helper()
	machineID, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("machine identity: %v", err)
	}
	signPub, signPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("machine grant-signing key: %v", err)
	}
	keys, err := crypto.NewEpochKeys()
	if err != nil {
		t.Fatalf("epoch keys: %v", err)
	}

	deviceEnds := make(chan *memRendezvous, 2)
	faultedEnds := make(chan *decisionFaultRendezvous, 1)
	var mu sync.Mutex
	handedOut := 0
	sk.api.pairing = &pairingConfig{
		Static:            machineID.NoiseStatic(),
		RecipientPub:      machineID.RecipientPublic(),
		SignPub:           signPub,
		SignPriv:          signPriv,
		EpochID:           1,
		GrantSeq:          1,
		EpochKeys:         keys,
		Hostname:          "test-machine.local",
		OperatorNamespace: "owner",
		RoutingID:         []byte("machine-routing-id-0001"),
		RelayAuthPub:      make([]byte, 32),
		RelayURL:          "ws://rendezvous.test:9999",
		NewRendezvous: func(context.Context, [16]byte) (pairing.RendezvousTransport, error) {
			mEnd, dEnd := rendezvousPair()
			deviceEnds <- dEnd
			mu.Lock()
			first := handedOut == 0
			handedOut++
			mu.Unlock()
			if !first || fault == faultNone {
				return mEnd, nil
			}
			faulted := &decisionFaultRendezvous{memRendezvous: mEnd, fault: fault}
			faultedEnds <- faulted
			return faulted, nil
		},
	}
	// The second channel carries the faulted machine end back to the test so it can assert the
	// fault actually landed on the frame it was aimed at.
	return deviceEnds, faultedEnds
}

// matchedPhoneSAS is mobile/pairing.go's DeviceSAS closure for the operator who compared the
// two screens and found them equal: it returns nil, or ctx.Err() if the handset's own clock
// runs out first. There is no third return, which is why an abort a real phone can produce
// always arrives with the ctx already dead (ADR-007 B64).
func matchedPhoneSAS(ctx context.Context, _ [6]string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

// runPhoneLeg scripts the handset on the shared rendezvous from the pair_start QR, with the
// SAS gate a shipped phone installs. It mirrors runDeviceLeg and adds that gate, so msg4
// leaves only after this operator has compared the codes.
func runPhoneLeg(ctx context.Context, ks crypto.KeyStore, dEnd pairing.RendezvousTransport, qp pairing.QRPayload) chan devLegResult {
	ch := make(chan devLegResult, 1)
	static, err := ks.NoiseStatic()
	if err != nil {
		ch <- devLegResult{err: err}
		return ch
	}
	dp := pairing.DeviceParams{
		Static:       static,
		Secret:       qp.PairingSecret,
		RendezvousID: qp.RendezvousID,
		Payload: pairing.DevicePayload{
			DeviceName:           "Test iPhone",
			DeviceRoutingID:      []byte("device-routing-id-0001"),
			DeviceRelayAuthPub:   ks.RelayAuthPublic(),
			RecipientPub:         ks.RecipientPublic(),
			DeviceCommandSignPub: ks.CommandSigningPublic(),
		},
		DeviceSAS: matchedPhoneSAS,
		Consent:   phoneConsentFor(ks, qp.RendezvousID),
	}
	go func() {
		do, err := pairing.RunDevice(ctx, dp, dEnd)
		ch <- devLegResult{outcome: do, err: err}
	}()
	return ch
}

// awaitPairStart reads until the daemon answers a pair_start, returning EITHER the pair_start
// reply or the refusal. Unlike awaitControl it does not fatal on a refusal, because the
// refusal is exactly what the lockout looks like on the wire.
func awaitPairStart(t *testing.T, rc *rawRemote) protocol.Control {
	t.Helper()
	for i := 0; i < 8; i++ {
		c, err := rc.readTry(5 * time.Second)
		if err != nil {
			t.Fatalf("waiting for a pair_start answer: %v", err)
		}
		if c.Op == protocol.OpPairStart || c.Op == protocol.OpError {
			return c
		}
	}
	t.Fatal("the daemon neither replied to nor refused pair_start within the frame budget")
	return protocol.Control{}
}

// completePairing runs one whole ordinary ceremony over the wire on a fresh owner connection
// and requires it to succeed on BOTH legs. It is the recovery assertion and the control, which
// is deliberate: the same helper that proves a healthy machine pairs is the one that proves a
// machine is still usable after a failed ceremony.
func completePairing(t *testing.T, sk *Daemon, deviceEnds chan *memRendezvous, phoneKeyDir string, why string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	rc := dialRemote(t, sk.SocketPath(), protocol.CapPairing)
	rc.write(protocol.Control{Op: protocol.OpPairStart, EndpointID: rc.endpointID,
		Pairing: &protocol.PairingControl{Capability: "full"}})

	reply := awaitPairStart(t, rc)
	if reply.Op == protocol.OpError {
		t.Fatalf("%s: pair_start was REFUSED -- %q.\n"+
			"  PB-PAIR-4 clause (2): the machine committed to a device on the strength of a decision\n"+
			"  frame the handset never received, so it now holds the single-device slot for a phone\n"+
			"  that holds nothing, and the only exit is `swarm remote revoke <device-id>` at the\n"+
			"  desktop. The relay chooses this at will and repeats it on the next attempt, so a\n"+
			"  desktop revoke is a treadmill, not a recovery (ADR-007 B81(2)).", why, reply.Error)
	}
	if reply.Pairing == nil || reply.Pairing.QR == "" {
		t.Fatalf("%s: pair_start reply missing QR: %+v", why, reply.Pairing)
	}
	qp, err := pairing.DecodeQR(reply.Pairing.QR)
	if err != nil {
		t.Fatalf("%s: pair_start QR undecodable: %v", why, err)
	}

	dEnd := recvDeviceEnd(t, deviceEnds)
	ks, err := crypto.NewFileKeyStore(phoneKeyDir)
	if err != nil {
		t.Fatalf("%s: phone keystore: %v", why, err)
	}
	devDone := runPhoneLeg(ctx, ks, dEnd, qp)

	pending := awaitControl(t, rc, protocol.OpPairPending)
	if pending.Pairing == nil || len(pending.Pairing.SAS) != 6 {
		t.Fatalf("%s: pair_pending missing the 6-word SAS gate: %+v", why, pending.Pairing)
	}
	rc.write(protocol.Control{Op: protocol.OpPairConfirm, EndpointID: rc.endpointID,
		Pairing: &protocol.PairingControl{Allow: true}})

	res := awaitControl(t, rc, protocol.OpPairResult)
	if res.Pairing == nil || res.Pairing.DeviceID == "" {
		t.Fatalf("%s: pair_result = %+v; want a completed pairing carrying the new DeviceID", why, res.Pairing)
	}
	select {
	case r := <-devDone:
		if r.err != nil || r.outcome == nil {
			t.Fatalf("%s: the handset did not pin (outcome=%v err=%v)", why, r.outcome, r.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("%s: the handset leg never resolved", why)
	}
}

// TestPBPAIR4_ADecisionThePhoneNeverLearnsMustNotLockTheMachine drives one ceremony to the
// exact instant the record keeps returning to -- the machine has decided, the phone has not
// heard -- by three routes that share nothing but their ending, and then asks the two questions
// PB-PAIR-4 is about: does the machine hold authority for a handset that holds nothing, and can
// the owner pair again without a desktop revoke.
//
// The three routes are chosen so that no single mechanism explains them all. The first has no
// relay fault at all and no attacker: it is a clock. The second is the declared adversary,
// deterministic on every attempt. The third is a relay that is merely BUSY -- the 16-slot inbox
// full, or the peer already detached -- and reports success while dropping the frame.
func TestPBPAIR4_ADecisionThePhoneNeverLearnsMustNotLockTheMachine(t *testing.T) {
	for _, tc := range []struct {
		name      string
		fault     decisionFault
		killPhone bool
		whatItWas string
	}{
		{
			name:      "the phone's context dies the instant its consent is on the wire",
			fault:     faultNone,
			killPhone: true,
			whatItWas: "an ordinary clock: the handset's own 60 s pairingTTL, or the app going away, " +
				"between sending msg4 and reading the answer. No relay fault and no attacker",
		},
		{
			name:      "the relay flips one bit in the decision frame",
			fault:     faultCorruptDecision,
			killPhone: false,
			whatItWas: "the declared adversary, deterministic on every attempt (ADR-007 B81(2) measured " +
				"machine=<nil>, device=\"decrypt decision: message authentication failed\")",
		},
		{
			name:      "the relay accepts the decision frame and delivers nothing",
			fault:     faultDropDecision,
			killPhone: false,
			whatItWas: "a relay that is merely busy: handleRendezvousSend reports success when the peer " +
				"has detached or the 16-slot inbox is full",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sk := assemble(t)
			deviceEnds, faultedEnds := injectFaultedPairing(t, sk, tc.fault)

			ttlCtx, cancelTTL := context.WithTimeout(context.Background(), phoneTTL)
			t.Cleanup(cancelTTL)
			phoneCtx, killPhone := context.WithCancel(ttlCtx)
			t.Cleanup(killPhone)

			rc := dialRemote(t, sk.SocketPath(), protocol.CapPairing)
			rc.write(protocol.Control{Op: protocol.OpPairStart, EndpointID: rc.endpointID,
				Pairing: &protocol.PairingControl{Capability: "full"}})

			reply := awaitPairStart(t, rc)
			if reply.Op != protocol.OpPairStart || reply.Pairing == nil || reply.Pairing.QR == "" {
				t.Fatalf("the FIRST pair_start was not served (%+v); nothing is being measured", reply)
			}
			qp, err := pairing.DecodeQR(reply.Pairing.QR)
			if err != nil {
				t.Fatalf("pair_start QR undecodable: %v", err)
			}

			var dEnd pairing.RendezvousTransport = recvDeviceEnd(t, deviceEnds)
			var dying *phoneDeathRendezvous
			if tc.killPhone {
				dying = &phoneDeathRendezvous{RendezvousTransport: dEnd, cancel: killPhone}
				dEnd = dying
			}
			ks, err := crypto.NewFileKeyStore(t.TempDir())
			if err != nil {
				t.Fatalf("phone keystore: %v", err)
			}
			devDone := runPhoneLeg(phoneCtx, ks, dEnd, qp)

			pending := awaitControl(t, rc, protocol.OpPairPending)
			if pending.Pairing == nil || len(pending.Pairing.SAS) != 6 {
				t.Fatalf("pair_pending missing the 6-word SAS gate: %+v", pending.Pairing)
			}
			// The desktop operator compares the codes, sees them match, and allows. So does the
			// handset's operator. Both people did exactly what the ceremony asks of them.
			rc.write(protocol.Control{Op: protocol.OpPairConfirm, EndpointID: rc.endpointID,
				Pairing: &protocol.PairingControl{Allow: true}})

			var handset devLegResult
			select {
			case handset = <-devDone:
			case <-time.After(15 * time.Second):
				t.Fatal("the handset leg never resolved; the scripted fault did not produce the " +
					"ordering under test")
			}
			if tc.killPhone && !dying.phoneDied() {
				t.Fatal("the handset never reached the death point (its consent was never sent)")
			}
			if tc.fault != faultNone {
				select {
				case faulted := <-faultedEnds:
					if !faulted.faultLanded() {
						t.Fatal("the relay never touched the decision frame; the ordering under test " +
							"never happened")
					}
				case <-time.After(5 * time.Second):
					t.Fatal("the faulted rendezvous was never handed out")
				}
			}

			// Let the machine leg settle: it is a background goroutine, and pair_result is the
			// only observable that says it has finished.
			_ = awaitControl(t, rc, protocol.OpPairResult)

			// THE FORK. A remedy is entirely free to RESCUE this ceremony -- retransmit the
			// decision, repair the frame, wait for an acknowledgement and try again. If the
			// handset ends up pinned, the two sides agree and there is no half-pair to fence;
			// demanding a failure here would forbid a fix that simply works. The clauses below
			// apply only to the state the record keeps arriving at: the handset holds nothing.
			if handset.err == nil && handset.outcome != nil {
				t.Logf("the handset pinned despite %s: the ceremony was rescued rather than "+
					"half-completed, which satisfies PB-PAIR-4 by agreement", tc.whatItWas)
				return
			}
			t.Logf("handset leg failed as scripted: %v", handset.err)

			// CLAUSE (1) -- NO ORPHAN AUTHORITY. Reported as an Error rather than a Fatal so that
			// clause (2) is measured in the same run: the two are independent halves and a GREEN
			// implementer needs to see both.
			settle := time.Now().Add(2 * time.Second)
			for sk.api.devices.Count() != 0 && time.Now().Before(settle) {
				time.Sleep(50 * time.Millisecond)
			}
			if got := sk.api.devices.Count(); got != 0 {
				t.Errorf("HALF-PAIR (PB-PAIR-4, clause 1): the machine holds authority for %d device(s) "+
					"while the handset holds nothing.\n"+
					"  Cause: %s.\n"+
					"  The machine committed on the strength of having SENT its decision. Nothing "+
					"acknowledges that frame, so \"sent\" is all it can ever know -- and the doubt must "+
					"resolve toward claiming no device, not toward claiming one.", got, tc.whatItWas)
			}
			if sk.api.RemoteControlEnabled() {
				t.Errorf("HALF-PAIR (PB-PAIR-4, clause 1): remote control is ENABLED after a ceremony "+
					"whose handset pinned nothing (%s). The kill switch reports a live remote-control "+
					"device that does not exist.", tc.whatItWas)
			}

			// CLAUSE (2) -- RECOVERABLE WITHOUT A DESKTOP REVOKE. A DIFFERENT handset key store, so
			// this cannot be satisfied by special-casing a repeat of the same device.
			completePairing(t, sk, deviceEnds, t.TempDir(),
				"recovery after "+tc.name)
		})
	}
}

// TestPBPAIR4_ControlAnUnfaultedCeremonyStillEnrolls is the control the fence above needs: on
// the SAME wiring with no fault scripted, an ordinary pairing completes on both legs and mints
// real authority. Without it, "never enroll anything" satisfies every assertion above.
func TestPBPAIR4_ControlAnUnfaultedCeremonyStillEnrolls(t *testing.T) {
	sk := assemble(t)
	deviceEnds, _ := injectFaultedPairing(t, sk, faultNone)

	completePairing(t, sk, deviceEnds, t.TempDir(), "control: an unfaulted ceremony")

	if got := sk.api.devices.Count(); got != 1 {
		t.Fatalf("registry Count = %d after an ordinary pairing; want exactly 1", got)
	}
	if !sk.api.RemoteControlEnabled() {
		t.Fatal("RemoteControlEnabled() is false after an ordinary pairing; want true")
	}
}

// TestPBPAIR4_ControlAGenuineDeclineStillDeclinesAndStaysPairable is the second control, and
// the one that stops the fence being satisfiable by enrolling on every ceremony regardless of
// what the operator said. A desktop decline must still enroll nothing -- and must still leave
// the machine pairable, because a decline is not a lockout either.
func TestPBPAIR4_ControlAGenuineDeclineStillDeclinesAndStaysPairable(t *testing.T) {
	sk := assemble(t)
	deviceEnds, _ := injectFaultedPairing(t, sk, faultNone)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	rc := dialRemote(t, sk.SocketPath(), protocol.CapPairing)
	rc.write(protocol.Control{Op: protocol.OpPairStart, EndpointID: rc.endpointID,
		Pairing: &protocol.PairingControl{Capability: "full"}})

	reply := awaitPairStart(t, rc)
	if reply.Op != protocol.OpPairStart || reply.Pairing == nil || reply.Pairing.QR == "" {
		t.Fatalf("pair_start was not served: %+v", reply)
	}
	qp, err := pairing.DecodeQR(reply.Pairing.QR)
	if err != nil {
		t.Fatalf("pair_start QR undecodable: %v", err)
	}

	dEnd := recvDeviceEnd(t, deviceEnds)
	ks, err := crypto.NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatalf("phone keystore: %v", err)
	}
	devDone := runPhoneLeg(ctx, ks, dEnd, qp)

	if pending := awaitControl(t, rc, protocol.OpPairPending); pending.Pairing == nil {
		t.Fatalf("pair_pending missing its payload: %+v", pending)
	}
	// The desktop operator says NO.
	rc.write(protocol.Control{Op: protocol.OpPairConfirm, EndpointID: rc.endpointID,
		Pairing: &protocol.PairingControl{Allow: false}})

	res := awaitControl(t, rc, protocol.OpPairResult)
	if res.Pairing != nil && res.Pairing.DeviceID != "" {
		t.Fatalf("pair_result carried DeviceID %q on a DECLINED pairing", res.Pairing.DeviceID)
	}
	select {
	case r := <-devDone:
		if r.outcome != nil {
			t.Fatal("the handset pinned a machine whose operator declined")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the handset leg never resolved after a decline")
	}
	if got := sk.api.devices.Count(); got != 0 {
		t.Fatalf("registry Count = %d after a DECLINED pairing; want 0", got)
	}

	// A decline leaves the machine pairable, exactly as a lost decision must.
	completePairing(t, sk, deviceEnds, t.TempDir(), "after a genuine decline")
}
