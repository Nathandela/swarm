// Package pairing orchestrates the swarm remote-control device<->machine pairing
// slice (R-PAIR.1-.9). It composes the frozen crypto foundation
// (internal/remote/crypto: Noise XXpsk0 + SAS) and treats the relay rendezvous
// (internal/remote/relay, R-PAIR.6) as an opaque two-party byte transport seam.
//
// Flow (device = XXpsk0 initiator, machine = responder):
//
//	device scans QR -> claims rendezvous -> msg1 (e)
//	machine msg2 (e,ee,s,es + MachinePayload)   // hostname, routing, relay-auth, recipient, epoch
//	device  msg3 (s,se + DevicePayload)         // name, routing, relay-auth, recipient
//	both derive SAS from the Noise channel binding
//	machine shows SAS + Allow? [y/N] (mandatory desktop confirm, fail-closed)
//	on affirmative confirm ONLY: machine pins device static + records routing,
//	sends its acceptance, burns the rendezvous; device pins machine static.
//
// This file is the FAILING-FIRST (TDD RED, GG-5) seam: every function is an
// unimplemented stub returning ErrUnimplemented. The exported types + signatures
// are the frozen contract the implementer fills; no test is edited to pass.
//
// FROZEN CONTRACT (what the implementer must deliver):
//
//	QR codec (qr.go): EncodeQR / DecodeQR, byte-exact, <=200 bytes (R-PAIR.2).
//	type RendezvousTransport interface{ Create; Claim; Send; Recv; Complete }  // relay seam (R-PAIR.6)
//	type ConfirmFunc func(ctx, sas [6]string, deviceName string) (bool, error)  // desktop confirm (R-PAIR.5)
//	type RateLimiter interface{ Allow() bool }                                  // gateway-side limit (R-PAIR.8)
//	type MachinePayload / DevicePayload                                         // msg2 / msg3 fields (R-PAIR.3 + A14 RecipientPub)
//	type MachineParams / DeviceParams / MachineOutcome / DeviceOutcome
//	func NewMachine(MachineParams) *Machine
//	func (*Machine) Pair(ctx, RendezvousTransport) (*MachineOutcome, error)     // responder; single-use; fail-closed
//	func (*Machine) Listening() bool                                           // no standing listener (R-PAIR.8)
//	func RunDevice(ctx, DeviceParams, RendezvousTransport) (*DeviceOutcome, error) // initiator
package pairing

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// ErrUnimplemented is returned by every stub in this failing-first skeleton. It
// is deliberately DISTINCT from every behavioral sentinel below so an error-path
// test (errors.Is against a specific sentinel) cannot go green against a stub.
var ErrUnimplemented = errors.New("pairing: unimplemented")

// Behavioral error contract. Each is a stable sentinel a test pins with
// errors.Is; the implementer must return exactly these (or wrap them).
var (
	// ErrHeadlessRefused is returned when Pair runs without a local console:
	// Phase-1 pairing REQUIRES a local operator at a physical display; headless/
	// SSH-only pairing is refused (R-PAIR.9 / D.0-A12), a Phase-3 follow-up.
	ErrHeadlessRefused = errors.New("pairing: local console required; headless pairing refused (R-PAIR.9 / D.0-A12)")
	// ErrConfirmDeclined is returned when the operator answers no to the desktop
	// confirm; nothing is pinned and no acceptance is sent (R-PAIR.5).
	ErrConfirmDeclined = errors.New("pairing: operator declined the pairing (R-PAIR.5)")
	// ErrConfirmTimeout is returned when the desktop confirm elapses without an
	// affirmative answer; the pairing fails CLOSED (R-PAIR.5).
	ErrConfirmTimeout = errors.New("pairing: operator confirmation timed out; failed closed (R-PAIR.5)")
	// ErrSecretConsumed is returned by a second Pair on a Machine whose single-use
	// secret was already consumed by a completed handshake (R-PAIR.1).
	ErrSecretConsumed = errors.New("pairing: single-use pairing secret already consumed (R-PAIR.1)")
	// ErrRateLimited is returned when a pairing attempt is refused by the
	// gateway-side limiter or surfaced from a relay-side rate refusal (R-PAIR.8).
	ErrRateLimited = errors.New("pairing: pairing attempt rate limited (R-PAIR.8)")
	// ErrPairingDeclined is the device-side result when the machine does not
	// affirmatively accept (declined or timed out): no machine static is pinned.
	ErrPairingDeclined = errors.New("pairing: machine did not accept; no pin established (device side, R-PAIR.5)")
	// ErrNoConsent is returned when the device cannot produce the relay-route consent
	// msg3 must carry (DeviceParams.Consent absent or failing), or when the machine
	// receives a msg3 without one. Both are fail-closed: a pairing that completes
	// without it leaves a relay unable to tell the machine from a stranger holding the
	// phone's public key, and leaves the machine unable to deliver the epoch grant
	// that makes the pairing usable at all (ADR-007 B27/B38).
	ErrNoConsent = errors.New("pairing: device relay-route consent missing; pairing failed closed (ADR-007 B38)")
)

// RendezvousTransport is the pairing package's seam onto the relay rendezvous
// (R-PAIR.6, whose two-party / 60s-TTL / burn mechanics the relay owns and
// tests). The machine Creates, the device Claims, both Send/Recv opaque
// handshake bytes, and the machine Completes (burns) on finish. The relay only
// forwards opaque bytes; it never sees the pairing secret or handshake
// plaintext. Implementer: adapt relay.Conn's Rendezvous* methods; tests use an
// in-memory fake.
type RendezvousTransport interface {
	Create(ctx context.Context, id string) error
	Claim(ctx context.Context, id string) error
	Send(ctx context.Context, msg []byte) error
	Recv(ctx context.Context) ([]byte, error)
	Complete(ctx context.Context, id string) error
}

// ConfirmFunc is the mandatory machine-side operator confirm (R-PAIR.5): given
// the SAS to compare out-of-band and the device's self-reported name, it returns
// true only on an affirmative "Allow" answer. Returning false declines; a
// non-nil error (e.g. the prompt's own TTL elapsing -> ErrConfirmTimeout) fails
// the pairing CLOSED. The callback OWNS the confirm TTL (clock discipline), so
// the orchestrator holds no separate confirm clock.
type ConfirmFunc func(ctx context.Context, sas [6]string, deviceName string) (bool, error)

// DeviceSASFunc is the device-side mirror of the machine's ConfirmFunc seam
// (R-PAIR.4/.5): it surfaces the SAS the device derived from the Noise channel
// binding so the phone operator can compare it out-of-band against the desktop
// SAS BEFORE the device commits to the pairing decision. It is invoked exactly
// once, after the handshake completes but before RunDevice blocks on the
// machine's decision frame and before any DeviceOutcome is pinned. A non-nil
// error fails the pairing CLOSED (nothing pinned); a nil callback is a no-op.
type DeviceSASFunc func(ctx context.Context, sas [6]string) error

// DeviceConsentFunc produces the device's relay-route consent for the machine it
// has just authenticated (R-PAIR.3, ADR-007 B27/B38). RunDevice invokes it exactly
// once, AFTER msg2 has been decrypted and decoded — so the machine payload it is
// handed is authenticated under the Noise+PSK channel — and places the result in
// DevicePayload.ConsentSig before msg3 is written.
//
// IT TAKES THE WHOLE PAYLOAD, NOT THE ROUTING ID, deliberately: the relay derives a
// routing id from a relay-auth public key, so a consent signed over a routing id
// STRING the machine merely asserted would bind to whatever the machine typed there.
// Signing over a value the caller derives from MachineRelayAuthPub binds the consent
// to the key the relay will actually authenticate the grantee under.
//
// A nil func, or a non-nil error, fails the pairing CLOSED with ErrNoConsent: a
// device that completes a pairing without granting a route has paired with a machine
// that can never deliver it the epoch grant, which is a silently broken pairing.
type DeviceConsentFunc func(machine MachinePayload) ([]byte, error)

// RateLimiter bounds pairing attempts on the gateway/machine side (R-PAIR.8; the
// relay enforces its own independent limit). Allow returns false to refuse an
// attempt before any transport work; a nil RateLimiter is unlimited.
type RateLimiter interface {
	Allow() bool
}

// MachinePayload is the machine's authenticated msg2 handshake payload (R-PAIR.3;
// RecipientPub added by D.0-A14 so BOTH X25519 keys are pinned at pairing). It
// rides inside the encrypted Noise message, so the relay never sees it.
type MachinePayload struct {
	Hostname            string
	MachineRoutingID    []byte
	MachineRelayAuthPub []byte
	RecipientPub        []byte // A14: machine sealed-box recipient X25519 pub, pinned at pairing
	MachineSignPub      []byte // enrollment keystone: machine Ed25519 grant-signing pub, pinned at pairing so the phone can verify epoch grants (F3)
	// MachineEndpointID is the machine's federation endpoint id (S19). It is the machine's
	// NAME, where Hostname is only its label: every mutating command the phone authors signs
	// over it, and crypto.Command.Canonical refuses an empty one, so a phone that completes
	// this handshake without it can author nothing. It is pinned here for the same reason the
	// three keys above are -- the pairing is the one authenticated moment the phone learns who
	// the machine is -- and it must be here rather than on the gateway's later reconcile
	// record, because PB-LIFE-3 starts the gateway only AFTER pairing.
	MachineEndpointID string
	// RelaySPKIPin is the SHA-256 of the relay certificate's SubjectPublicKeyInfo
	// (ADR-007 B33's pin, B34's missing channel), OPTIONAL: empty means the machine
	// has no pin configured and the phone learns none.
	//
	// IT IS HERE BECAUSE THERE IS NOWHERE ELSE. relay.TrustRootSourceFor("android")
	// is TrustRootsPinned, so a release handset refuses every dial without a pin —
	// and the pairing QR cannot carry one: MaxRelayURLLen = 39 already puts the
	// symbol at 133 bytes of a 134-byte v6-L budget, one character of slack, while a
	// 32-byte pin is ~43 base64 characters. This payload costs zero QR bytes and the
	// pin inherits exactly the trust properties of the five keys beside it — a party
	// that could substitute the pin here could already substitute MachineSignPub.
	//
	// ITS LIMIT, STATED HERE RATHER THAN DISCOVERED LATER: it does not protect the
	// PAIRING dial that carries it, only every dial after it. The pairing dial's own
	// protection is the cleartext refusal and the consent signature below.
	RelaySPKIPin []byte
	EpochID      uint32
}

// DevicePayload is the device's authenticated msg3 handshake payload (R-PAIR.3;
// RecipientPub added by D.0-A14; DeviceCommandSignPub added by ADR-007 2026-07-20
// so the machine pins the device's Ed25519 command-signing key for R-POL.9).
type DevicePayload struct {
	DeviceName           string
	DeviceRoutingID      []byte
	DeviceRelayAuthPub   []byte
	RecipientPub         []byte // A14: device sealed-box recipient X25519 pub, pinned at pairing
	DeviceCommandSignPub []byte // R-CRY.16 / ADR-007 2026-07-20: device Ed25519 command-signing pub, pinned at pairing for R-POL.9
	// ConsentSig is the device's relay-auth signature over relay.ConsentMessage of the
	// MACHINE's routing id: the device granting this machine, and no other party,
	// authority over the device's relay route (ADR-007 B27's consent signature, made
	// mandatory by B38). It is opaque here — this package never parses it, so it does
	// not import relay — and it is produced by DeviceParams.Consent from the machine
	// payload the device just AUTHENTICATED in msg2, never from a guess.
	//
	// It is what carries the ceremony's outcome to a relay that cannot witness the
	// ceremony. Without it the relay had to infer consent from the target's state
	// ("has authorized nobody"), which any holder of the target's PUBLIC key could
	// satisfy — and that key is disclosed by msg2, by msg3, and by an unprotected
	// auth_init. See internal/remote/relay/store.go mayActOn.
	ConsentSig []byte
}

// MachineParams configures one machine-side (Noise XXpsk0 responder) pairing.
type MachineParams struct {
	Static       *crypto.NoiseStatic // machine Noise-static handle (identity)
	Secret       [32]byte            // single-use pairing secret = XXpsk0 PSK (R-PAIR.1)
	RendezvousID [16]byte            // keys the relay rendezvous; independent of Secret
	Payload      MachinePayload      // carried to the device in msg2
	LocalConsole bool                // R-PAIR.9 / D.0-A12: false => headless => refuse
	Confirm      ConfirmFunc         // R-PAIR.5 mandatory desktop confirm gate
	Limiter      RateLimiter         // R-PAIR.8 gateway-side rate limit (nil => unlimited)
}

// DeviceParams configures one device-side (Noise XXpsk0 initiator) pairing.
type DeviceParams struct {
	Static           *crypto.NoiseStatic // device Noise-static handle
	Secret           [32]byte            // pairing secret from the scanned QR (= PSK)
	RendezvousID     [16]byte            // from the scanned QR
	MachineStaticPub []byte              // optional pin from the QR (nil or 32 bytes)
	Payload          DevicePayload       // carried to the machine in msg3
	Limiter          RateLimiter         // optional device-side rate limit (nil => unlimited)
	DeviceSAS        DeviceSASFunc       // optional; surfaces the SAS before the decision (nil => no-op)
	Consent          DeviceConsentFunc   // MANDATORY (ADR-007 B38); signs the route consent carried in msg3
}

// MachineOutcome is the machine's result on an affirmatively-confirmed pairing
// (R-PAIR.7): the SAS shown to the operator, the pinned device Noise-static, and
// the device's exchanged routing payload.
type MachineOutcome struct {
	SAS          [6]string
	DeviceStatic []byte // pinned device Noise-static public key
	Device       DevicePayload
}

// DeviceOutcome is the device's result on a completed pairing (R-PAIR.7): the
// SAS, the pinned machine Noise-static, and the machine's routing payload
// (including the initial EpochID).
type DeviceOutcome struct {
	SAS           [6]string
	MachineStatic []byte // pinned machine Noise-static public key
	Machine       MachinePayload
}

// Machine is the machine-side pairing endpoint for a SINGLE `swarm remote pair`
// invocation. Its secret is single-use (R-PAIR.1) and it listens only while Pair
// runs (R-PAIR.8) — no standing listener between invocations.
type Machine struct {
	params MachineParams

	mu        sync.Mutex
	consumed  bool // set once a handshake reaches transport mode (R-PAIR.1)
	listening bool // true only while Pair drives the transport (R-PAIR.8)
}

// NewMachine builds a machine-side pairing endpoint. It opens NO listener and
// touches NO transport until Pair is called (R-PAIR.8).
func NewMachine(p MachineParams) *Machine { return &Machine{params: p} }

// Pair runs one machine-side pairing attempt over rt: create rendezvous, drive
// the XXpsk0 handshake as responder, derive the SAS, gate on the mandatory
// operator confirm (R-PAIR.5), and only then pin the device static + record its
// routing (R-PAIR.7), completing (burning) the rendezvous. The single-use secret
// is consumed on the first completed handshake; a second call returns
// ErrSecretConsumed. A machine without a local console returns ErrHeadlessRefused
// (R-PAIR.9). Fails closed (nothing pinned, no acceptance sent) on decline or
// timeout. A gateway-side rate refusal or a relay-side rate error surfaces as
// ErrRateLimited (R-PAIR.8).
func (m *Machine) Pair(ctx context.Context, rt RendezvousTransport) (*MachineOutcome, error) {
	p := m.params

	// Refuse cheaply, BEFORE any transport work, in fail-closed precedence order.
	// Gateway-side rate limit (R-PAIR.8): refuse an over-budget attempt outright.
	if p.Limiter != nil && !p.Limiter.Allow() {
		return nil, ErrRateLimited
	}
	// Headless refusal (R-PAIR.9 / D.0-A12): Phase-1 pairing needs a local operator.
	if !p.LocalConsole {
		return nil, ErrHeadlessRefused
	}
	// Single-use secret (R-PAIR.1): a spent Machine never opens a second rendezvous.
	m.mu.Lock()
	if m.consumed {
		m.mu.Unlock()
		return nil, ErrSecretConsumed
	}
	m.listening = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.listening = false
		m.mu.Unlock()
	}()

	label := rendezvousLabel(p.RendezvousID)
	if err := rt.Create(ctx, label); err != nil {
		// A relay-side rate refusal (or any create failure) surfaces verbatim so
		// errors.Is(err, ErrRateLimited) holds for the rate-limited case (R-PAIR.8).
		return nil, fmt.Errorf("pairing: create rendezvous: %w", err)
	}

	// Machine is the XXpsk0 responder; the 32-byte secret is the PSK, and the peer
	// static is learned (not pinned) on the wire — the SAS + desktop confirm are
	// the out-of-band gate. AllowUnpinnedPeer is mechanically pairing-only.
	sess, err := crypto.NewNoise(crypto.NoiseConfig{
		Initiator:         false,
		Static:            p.Static,
		AllowUnpinnedPeer: true,
		PSK:               p.Secret[:],
		Prologue:          crypto.PairPrologue(p.RendezvousID[:]),
	})
	if err != nil {
		return nil, fmt.Errorf("pairing: new noise responder: %w", err)
	}

	// msg1 (e): device -> machine.
	msg1, err := rt.Recv(ctx)
	if err != nil {
		return nil, fmt.Errorf("pairing: recv msg1: %w", err)
	}
	if _, err := sess.ReadMessage(msg1); err != nil {
		return nil, fmt.Errorf("pairing: read msg1: %w", err)
	}
	// msg2 (e, ee, s, es + machine payload): machine -> device. Carries the
	// machine's Noise static plus its routing payload, incl. the A14 RecipientPub.
	msg2, err := sess.WriteMessage(encodeMachinePayload(p.Payload))
	if err != nil {
		return nil, fmt.Errorf("pairing: write msg2: %w", err)
	}
	if err := rt.Send(ctx, msg2); err != nil {
		return nil, fmt.Errorf("pairing: send msg2: %w", err)
	}
	// msg3 (s, se + device payload): device -> machine. Completes the handshake;
	// the machine learns the device's static + routing payload.
	msg3, err := rt.Recv(ctx)
	if err != nil {
		return nil, fmt.Errorf("pairing: recv msg3: %w", err)
	}
	devPayloadBytes, err := sess.ReadMessage(msg3)
	if err != nil {
		return nil, fmt.Errorf("pairing: read msg3: %w", err)
	}
	if !sess.HandshakeComplete() {
		return nil, fmt.Errorf("pairing: handshake did not complete after msg3")
	}
	// The secret is now spent (R-PAIR.1): a completed handshake consumes it even if
	// the operator later declines — a photographed QR cannot be retried.
	m.mu.Lock()
	m.consumed = true
	m.mu.Unlock()

	devPayload, err := decodeDevicePayload(devPayloadBytes)
	if err != nil {
		return nil, fmt.Errorf("pairing: decode device payload: %w", err)
	}
	// Fail CLOSED on a device that granted no relay route (ADR-007 B38). The machine
	// refuses BEFORE the operator is prompted, so a confirm is never spent on a pairing
	// whose grant delivery would then fail fatally at gateway start
	// (cmd/swarm-remote/deliver.go, whose failure is fatal in main.go). The check is on
	// presence only: this package does not import relay and cannot verify the signature,
	// and the relay itself is the authority that must — a check here would be advisory
	// and a check there is the fence.
	//
	// It DECLINES rather than merely returning, for the same reason the confirm gate below
	// does: the device is parked on rt.Recv waiting for this machine's decision, and a
	// machine that walks away silently leaves it there until its own deadline elapses —
	// reporting a timeout for what is actually a refusal, and holding the rendezvous
	// unburned meanwhile. Found by TestB38_AMachineRefusesAMsg3WithNoConsentBeforeTheConfirm,
	// which hung before this line existed.
	if len(devPayload.ConsentSig) == 0 {
		_ = m.sendDecision(ctx, sess, rt, label, false)
		return nil, ErrNoConsent
	}
	deviceStatic := sess.PeerStatic()

	// SAS from the Noise channel binding (R-PAIR.4): on a MITM the two ends bind
	// different transcripts, so the operator's out-of-band comparison diverges.
	sas, err := crypto.SAS(sess.ChannelBinding())
	if err != nil {
		return nil, fmt.Errorf("pairing: derive sas: %w", err)
	}

	// Mandatory desktop confirm (R-PAIR.5). Nothing is pinned and no acceptance is
	// sent until the operator affirmatively allows. A decline / timeout / missing
	// callback fails CLOSED: the device is told (a decline frame so it unblocks),
	// the rendezvous is burned, and no outcome is returned.
	allow, cErr := false, error(nil)
	if p.Confirm != nil {
		allow, cErr = p.Confirm(ctx, sas, devPayload.DeviceName)
	}
	if cErr != nil || !allow {
		_ = m.sendDecision(ctx, sess, rt, label, false)
		switch {
		case cErr != nil && errors.Is(cErr, ErrConfirmTimeout):
			return nil, ErrConfirmTimeout
		case cErr != nil:
			return nil, cErr
		default:
			return nil, ErrConfirmDeclined
		}
	}

	// Affirmative confirm (R-PAIR.7): send acceptance over the authenticated
	// channel, pin the device static + record its routing, and burn the rendezvous.
	if err := m.sendDecision(ctx, sess, rt, label, true); err != nil {
		return nil, fmt.Errorf("pairing: send acceptance: %w", err)
	}
	return &MachineOutcome{
		SAS:          sas,
		DeviceStatic: deviceStatic,
		Device:       devPayload,
	}, nil
}

// sendDecision encrypts the machine's final accept/decline signal over the
// established Noise transport and burns the rendezvous. It is authenticated (both
// statics are pinned by now), so the device knows the decision came from the real
// machine. The rendezvous is completed (burned) regardless of the decision.
func (m *Machine) sendDecision(ctx context.Context, sess *crypto.NoiseSession, rt RendezvousTransport, label string, accept bool) error {
	b := decisionDecline
	if accept {
		b = decisionAccept
	}
	frame, err := sess.Encrypt([]byte{b})
	if err != nil {
		_ = rt.Complete(ctx, label)
		return err
	}
	sendErr := rt.Send(ctx, frame)
	if err := rt.Complete(ctx, label); err != nil && sendErr == nil {
		sendErr = err
	}
	return sendErr
}

// Listening reports whether a pairing listener is currently active. It is false
// before Pair starts and after it returns (R-PAIR.8: no standing listener).
func (m *Machine) Listening() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.listening
}

// RunDevice runs one device-side pairing attempt over rt: claim the rendezvous,
// drive the XXpsk0 handshake as initiator, derive the SAS, and finalize by
// pinning the machine static + recording its routing (R-PAIR.7) once the machine
// affirmatively accepts. Returns ErrPairingDeclined if the machine declines or
// times out.
func RunDevice(ctx context.Context, p DeviceParams, rt RendezvousTransport) (*DeviceOutcome, error) {
	// Optional device-side rate limit (R-PAIR.8; the relay enforces its own).
	if p.Limiter != nil && !p.Limiter.Allow() {
		return nil, ErrRateLimited
	}

	label := rendezvousLabel(p.RendezvousID)
	if err := rt.Claim(ctx, label); err != nil {
		return nil, fmt.Errorf("pairing: claim rendezvous: %w", err)
	}

	// Device is the XXpsk0 initiator; the scanned secret is the PSK. If the QR
	// carried a machine static, pin it up front; otherwise learn it on the wire
	// (the SAS + desktop confirm are the out-of-band gate).
	cfg := crypto.NoiseConfig{
		Initiator: true,
		Static:    p.Static,
		PSK:       p.Secret[:],
		Prologue:  crypto.PairPrologue(p.RendezvousID[:]),
	}
	if len(p.MachineStaticPub) == 32 {
		cfg.PeerStatic = p.MachineStaticPub
	} else {
		cfg.AllowUnpinnedPeer = true
	}
	sess, err := crypto.NewNoise(cfg)
	if err != nil {
		return nil, fmt.Errorf("pairing: new noise initiator: %w", err)
	}

	// msg1 (e): device -> machine.
	msg1, err := sess.WriteMessage(nil)
	if err != nil {
		return nil, fmt.Errorf("pairing: write msg1: %w", err)
	}
	if err := rt.Send(ctx, msg1); err != nil {
		return nil, fmt.Errorf("pairing: send msg1: %w", err)
	}
	// msg2 (e, ee, s, es + machine payload): machine -> device. The device learns
	// the machine's static + routing payload (incl. the A14 RecipientPub + epoch).
	msg2, err := rt.Recv(ctx)
	if err != nil {
		return nil, fmt.Errorf("pairing: recv msg2: %w", err)
	}
	machPayloadBytes, err := sess.ReadMessage(msg2)
	if err != nil {
		return nil, fmt.Errorf("pairing: read msg2: %w", err)
	}
	machPayload, err := decodeMachinePayload(machPayloadBytes)
	if err != nil {
		return nil, fmt.Errorf("pairing: decode machine payload: %w", err)
	}
	// THE RELAY-ROUTE CONSENT (ADR-007 B27/B38), signed HERE and not earlier, because
	// this is the first moment the device knows WHO it is consenting to: msg2 is the
	// authenticated frame that carries the machine's relay-auth key, and the consent
	// names the routing id derived from it. Signing it into the payload the device was
	// configured with, before the handshake, would be consenting to a machine the
	// device had not yet met.
	//
	// Fail CLOSED (ErrNoConsent) rather than sending an empty one: a msg3 without it
	// completes a handshake and enrolls a device the machine can never deliver the
	// epoch grant to, which is a pairing that looks successful and does nothing.
	if p.Consent == nil {
		return nil, ErrNoConsent
	}
	consent, err := p.Consent(machPayload)
	if err != nil {
		return nil, fmt.Errorf("pairing: sign relay-route consent: %w", err)
	}
	if len(consent) == 0 {
		return nil, ErrNoConsent
	}
	p.Payload.ConsentSig = consent
	// msg3 (s, se + device payload): device -> machine. Completes the handshake.
	msg3, err := sess.WriteMessage(encodeDevicePayload(p.Payload))
	if err != nil {
		return nil, fmt.Errorf("pairing: write msg3: %w", err)
	}
	if err := rt.Send(ctx, msg3); err != nil {
		return nil, fmt.Errorf("pairing: send msg3: %w", err)
	}
	if !sess.HandshakeComplete() {
		return nil, fmt.Errorf("pairing: handshake did not complete after msg3")
	}

	sas, err := crypto.SAS(sess.ChannelBinding())
	if err != nil {
		return nil, fmt.Errorf("pairing: derive sas: %w", err)
	}
	machineStatic := sess.PeerStatic()

	// Surface the SAS to the phone operator (R-PAIR.4) BEFORE blocking on the
	// machine's decision and BEFORE any pin, so the operator can compare it
	// out-of-band against the desktop SAS at the right moment. A non-nil error
	// fails the pairing CLOSED: nothing is pinned and no outcome is returned.
	if p.DeviceSAS != nil {
		if err := p.DeviceSAS(ctx, sas); err != nil {
			return nil, err
		}
	}

	// Wait for the machine's authenticated decision (R-PAIR.5). No machine static
	// is pinned unless the machine affirmatively accepts; a decline / timeout on
	// the machine side surfaces here as ErrPairingDeclined with no pin.
	frame, err := rt.Recv(ctx)
	if err != nil {
		return nil, fmt.Errorf("pairing: recv decision: %w", err)
	}
	decision, err := sess.Decrypt(frame)
	if err != nil {
		return nil, fmt.Errorf("pairing: decrypt decision: %w", err)
	}
	if len(decision) != 1 || decision[0] != decisionAccept {
		return nil, ErrPairingDeclined
	}

	// R-PAIR.7: pin the machine static + record its routing payload (incl. epoch).
	return &DeviceOutcome{
		SAS:           sas,
		MachineStatic: machineStatic,
		Machine:       machPayload,
	}, nil
}

// decisionAccept / decisionDecline are the single-byte machine-side pairing
// decision carried in the final authenticated transport frame (R-PAIR.5).
const (
	decisionDecline byte = 0x00
	decisionAccept  byte = 0x01
)

// errMalformedPayload is returned when a handshake payload cannot be decoded. It
// only fires on a truncated/garbled frame; the frame rides the authenticated
// Noise channel, so this is a defensive check, not an expected path.
var errMalformedPayload = errors.New("pairing: malformed handshake payload")

// rendezvousLabel renders the 16-byte rendezvous id as the opaque relay label.
// It is derived only from the rendezvous id (never the secret), so the secret is
// never carried in a label the relay can see (R-PAIR.1).
func rendezvousLabel(id [16]byte) string { return hex.EncodeToString(id[:]) }

// appendField appends a 4-byte big-endian length prefix then f, so no two
// distinct field sequences share an encoding (F11 — no splicing).
func appendField(b, f []byte) []byte {
	b = binary.BigEndian.AppendUint32(b, uint32(len(f)))
	return append(b, f...)
}

// readField reads one length-prefixed field from b, returning the field, the
// remaining bytes, and whether the read was well-formed.
func readField(b []byte) (field, rest []byte, ok bool) {
	if len(b) < 4 {
		return nil, nil, false
	}
	n := binary.BigEndian.Uint32(b[:4])
	b = b[4:]
	if uint32(len(b)) < n {
		return nil, nil, false
	}
	return append([]byte(nil), b[:n]...), b[n:], true
}

// encodeMachinePayload serialises the msg2 machine payload (R-PAIR.3 + A14 +
// enrollment keystone + S19's endpoint id + B34's relay SPKI pin): the seven
// length-prefixed byte fields followed by the 4-byte big-endian epoch id. Each added field rides BEFORE the
// epoch trailer, so the epoch-trailer contract is undisturbed.
func encodeMachinePayload(p MachinePayload) []byte {
	var b []byte
	b = appendField(b, []byte(p.Hostname))
	b = appendField(b, p.MachineRoutingID)
	b = appendField(b, p.MachineRelayAuthPub)
	b = appendField(b, p.RecipientPub)
	b = appendField(b, p.MachineSignPub)
	b = appendField(b, []byte(p.MachineEndpointID))
	b = appendField(b, p.RelaySPKIPin)
	b = binary.BigEndian.AppendUint32(b, p.EpochID)
	return b
}

// decodeMachinePayload is the inverse of encodeMachinePayload.
func decodeMachinePayload(b []byte) (MachinePayload, error) {
	var p MachinePayload
	var ok bool
	var host []byte
	if host, b, ok = readField(b); !ok {
		return MachinePayload{}, errMalformedPayload
	}
	p.Hostname = string(host)
	if p.MachineRoutingID, b, ok = readField(b); !ok {
		return MachinePayload{}, errMalformedPayload
	}
	if p.MachineRelayAuthPub, b, ok = readField(b); !ok {
		return MachinePayload{}, errMalformedPayload
	}
	if p.RecipientPub, b, ok = readField(b); !ok {
		return MachinePayload{}, errMalformedPayload
	}
	if p.MachineSignPub, b, ok = readField(b); !ok {
		return MachinePayload{}, errMalformedPayload
	}
	var endpoint []byte
	if endpoint, b, ok = readField(b); !ok {
		return MachinePayload{}, errMalformedPayload
	}
	p.MachineEndpointID = string(endpoint)
	if p.RelaySPKIPin, b, ok = readField(b); !ok {
		return MachinePayload{}, errMalformedPayload
	}
	if len(b) != 4 {
		return MachinePayload{}, errMalformedPayload
	}
	p.EpochID = binary.BigEndian.Uint32(b)
	return p, nil
}

// encodeDevicePayload serialises the msg3 device payload (R-PAIR.3 + A14 +
// ADR-007 2026-07-20 + B38's route consent): six length-prefixed byte fields.
func encodeDevicePayload(p DevicePayload) []byte {
	var b []byte
	b = appendField(b, []byte(p.DeviceName))
	b = appendField(b, p.DeviceRoutingID)
	b = appendField(b, p.DeviceRelayAuthPub)
	b = appendField(b, p.RecipientPub)
	b = appendField(b, p.DeviceCommandSignPub)
	b = appendField(b, p.ConsentSig)
	return b
}

// decodeDevicePayload is the inverse of encodeDevicePayload.
func decodeDevicePayload(b []byte) (DevicePayload, error) {
	var p DevicePayload
	var ok bool
	var name []byte
	if name, b, ok = readField(b); !ok {
		return DevicePayload{}, errMalformedPayload
	}
	p.DeviceName = string(name)
	if p.DeviceRoutingID, b, ok = readField(b); !ok {
		return DevicePayload{}, errMalformedPayload
	}
	if p.DeviceRelayAuthPub, b, ok = readField(b); !ok {
		return DevicePayload{}, errMalformedPayload
	}
	if p.RecipientPub, b, ok = readField(b); !ok {
		return DevicePayload{}, errMalformedPayload
	}
	if p.DeviceCommandSignPub, b, ok = readField(b); !ok {
		return DevicePayload{}, errMalformedPayload
	}
	if p.ConsentSig, b, ok = readField(b); !ok {
		return DevicePayload{}, errMalformedPayload
	}
	if len(b) != 0 {
		return DevicePayload{}, errMalformedPayload
	}
	return p, nil
}
