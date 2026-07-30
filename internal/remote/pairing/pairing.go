// Package pairing orchestrates the swarm remote-control device<->machine pairing
// slice (R-PAIR.1-.9). It composes the frozen crypto foundation
// (internal/remote/crypto: Noise XXpsk0 + SAS) and treats the relay rendezvous
// (internal/remote/relay, R-PAIR.6) as an opaque two-party byte transport seam.
//
// Flow (device = XXpsk0 initiator, machine = responder):
//
//	device scans QR -> claims rendezvous -> msg1 (e)
//	machine msg2 (e,ee,s,es + MachinePayload)   // hostname, routing, relay-auth, recipient, epoch
//	device  msg3 (s,se + DevicePayload)         // name, routing, relay-auth, recipient, ConsentDeferred
//	both derive SAS from the Noise channel binding
//	machine shows SAS + Allow? [y/N] (mandatory desktop confirm, fail-closed)
//	device  shows SAS to its own operator (DeviceSAS gate, fail-closed)
//	device  msg4 (transport frame)              // the relay-route consent, or an empty ABORT
//	machine msg5 (transport frame)              // the accept/decline decision
//	device  msg6 (transport frame)              // acknowledgement of an ACCEPTANCE only
//	on affirmative confirm AND a non-empty msg4 ONLY: the machine sends its acceptance;
//	the device pins the machine static on that acceptance and acknowledges it; the
//	machine pins the device static + records routing ONLY on that acknowledgement, and
//	then burns the rendezvous.
//
// msg4 EXISTS BECAUSE THE SAS CANNOT PRECEDE msg3 (ADR-007 B52). Writing msg3 is what
// creates the channel binding, so anything carried in msg3 is inside the transcript the
// SAS attests and cannot be chosen after the operator has compared it. The consent is the
// one field that must be: a phone whose operator REJECTS the SAS must not have already
// released a standing grant over its own relay route. Neither end commits before it has
// the other's answer — the machine holds the consent before it accepts, the device holds
// the acceptance before it pins.
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
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

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
	// ErrAcceptUnacknowledged is the machine-side result when the acceptance was sent and
	// the device never acknowledged it: the machine cannot know its "yes" arrived, so it
	// claims NO device (PB-PAIR-4, ADR-007 B81(2)).
	//
	// It is not the same failure as a decline. A decline is an answer; this is the absence
	// of one, and the only honest thing either name can carry is that nothing was pinned.
	ErrAcceptUnacknowledged = errors.New("pairing: the device never acknowledged the acceptance; no device claimed (PB-PAIR-4)")
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

// DeviceVerifyFunc is the device's check on the machine payload it has just
// authenticated, run the instant msg2 is decoded — BEFORE msg3 is written, before the SAS
// is shown, and before any consent exists. A non-nil error fails the pairing CLOSED with
// nothing sent and nothing pinned; a nil func is a no-op.
//
// IT EXISTS FOR ADR-007 B48. The pairing rendezvous dials under unverified TLS, because it
// is the dial that fetches the pin that would verify it (B45). B48 amends that ruling:
// leaving the certificate unchecked also lowered the cost of B46's consent harvest from
// "hold a certificate valid for the operator's relay" to "be on the path". The remedy is
// not to verify the dial — it cannot be — but to compare what it presented against
// MachinePayload.RelaySPKIPin, which the REAL machine authored and which reaches the phone
// only inside this authenticated frame. A network attacker terminating that TLS cannot
// make the two agree.
//
// It runs at msg2 rather than later because that is the first moment the comparison is
// possible and the last moment before the operator is asked to do anything: an operator
// should never be shown a SAS for a connection already known to be terminated.
//
// It does NOT cover a QR-holder, who is a legitimate party to the ceremony and presents
// the relay's real certificate. That case belongs to the SAS gate and the deferred
// consent (B52).
type DeviceVerifyFunc func(machine MachinePayload) error

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
	//
	// IT NO LONGER RIDES IN msg3 (ADR-007 B52). msg3 is written before the SAS exists —
	// the channel binding is created BY writing it — so a signature carried here is one
	// the phone released before its operator could compare anything, which is B46's
	// harvest. RunDevice sends it in msg4 instead, and the machine fills this field in
	// from that frame. A msg3 that still carries one is a pre-B52 build and is REFUSED:
	// its credential is already out.
	ConsentSig []byte
	// ConsentDeferred is the device stating in msg3 that its relay-route consent will
	// follow in msg4, once its operator has compared the SAS.
	//
	// IT CONVEYS NO AUTHORITY, WHICH IS THE POINT. It exists so the machine's fail-closed
	// refusal can keep the position it had when the signature was here — BEFORE the
	// operator is prompted — and it draws the distinction that refusal was always about:
	// a build that WILL grant a route once its operator confirms, versus one that grants
	// none (an older build, or a hostile one). A marker that granted anything would be a
	// thing released before the SAS gate, and this whole argument would restart one level
	// down.
	ConsentDeferred bool
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
	VerifyMachine    DeviceVerifyFunc    // optional; checks the authenticated msg2 payload (ADR-007 B48)
	Consent          DeviceConsentFunc   // MANDATORY (ADR-007 B38); signs the route consent carried in msg4
}

// MachineOutcome is the machine's result on an affirmatively-confirmed pairing
// (R-PAIR.7): the SAS shown to the operator, the pinned device Noise-static, and
// the device's exchanged routing payload.
type MachineOutcome struct {
	SAS          [6]string
	DeviceStatic []byte // pinned device Noise-static public key
	Device       DevicePayload
	// RendezvousUnburned records a rendezvous burn that did not finish on a pairing that
	// otherwise SUCCEEDED, and it is nil on every healthy pairing.
	//
	// IT IS A FIELD RATHER THAN AN ERROR BECAUSE A BURN THAT CANNOT FINISH IS HOUSEKEEPING,
	// NOT CONSENT (ADR-007 B82(1)). The burn runs after the acceptance has been forwarded
	// AND acknowledged, so folding its failure into the pairing's failure orphaned a phone
	// this machine had already told yes, with every frame delivered intact and only a
	// loaded or distant relay to blame.
	//
	// It is not nothing, which is why it is carried rather than dropped: an unburned
	// rendezvous id survives until the relay purges it at its own TTL, and past that lapse
	// the id can be re-created (ADR-007 B47b). The residual is BOUNDED by that TTL and by
	// the pairing secret already being spent, so it cannot be replayed into a second
	// handshake -- but it is a fact about relay state that this outcome is the only place
	// that knows.
	RendezvousUnburned error
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
	// Fail CLOSED on a device that granted no relay route (ADR-007 B38, B52). The machine
	// refuses BEFORE the operator is prompted, so a confirm is never spent on a pairing
	// whose grant delivery would then fail fatally at gateway start
	// (cmd/swarm-remote/deliver.go, whose failure is fatal in main.go).
	//
	// The check is on the DEFERRAL MARKER, because the signature itself now arrives in
	// msg4, after the phone's own SAS gate. What is being distinguished is unchanged: a
	// build that will grant a route once its operator confirms, versus one that grants
	// none (an older build, or a hostile one). Presence is all this package can check —
	// it does not import relay and cannot verify a signature, and the relay is the
	// authority that must; a check here would be advisory and a check there is the fence.
	//
	// A msg3 that CARRIES a signature is refused too, and that is not belt-and-braces: it
	// is a pre-B52 device whose standing credential is already out on the wire, released
	// before its operator could compare anything. This machine cannot repair that phone,
	// so it refuses the pairing rather than completing one whose credential is harvested.
	//
	// It DECLINES rather than merely returning, for the same reason the confirm gate below
	// does: the device is parked on rt.Recv waiting for this machine's decision, and a
	// machine that walks away silently leaves it there until its own deadline elapses —
	// reporting a timeout for what is actually a refusal, and holding the rendezvous
	// unburned meanwhile. Found by TestB38_AMachineRefusesAMsg3WithNoConsentBeforeTheConfirm,
	// which hung before this line existed.
	if !devPayload.ConsentDeferred || len(devPayload.ConsentSig) != 0 {
		declineAndBurn(ctx, sess, rt, label)
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
		declineAndBurn(ctx, sess, rt, label)
		switch {
		case cErr != nil && errors.Is(cErr, ErrConfirmTimeout):
			return nil, ErrConfirmTimeout
		case cErr != nil:
			return nil, cErr
		default:
			return nil, ErrConfirmDeclined
		}
	}

	// msg4: the consent the device released after ITS operator's SAS gate — read AFTER the
	// confirm above and BEFORE any acceptance below (ADR-007 B52).
	//
	// THAT ORDER IS THE WHOLE OF THE PARTIAL-FAILURE ARGUMENT. The machine commits to
	// nothing until it holds the consent, and the device pins nothing until it holds the
	// acceptance, so there is no ordering in which one side is enrolled and the other is
	// not. Reading it before the confirm would instead block the desktop prompt on the
	// phone — leaving the operator nothing to compare the phone's SAS against — and
	// sending acceptance first would strand a pinned device against a machine that then
	// failed.
	//
	// It also closes a defect that has nothing to do with the consent: before msg4 existed
	// NOTHING after msg3 reached this machine, so it accepted and enrolled devices whose
	// operator had REFUSED the SAS, spending PB-STATE-10's single-device slot on a pairing
	// the user declined.
	consent, err := recvConsent(ctx, sess, rt)
	if err != nil || len(consent) == 0 {
		declineAndBurn(ctx, sess, rt, label)
		if err != nil {
			return nil, fmt.Errorf("pairing: recv relay-route consent: %w", err)
		}
		// A zero-length msg4 is the device's well-formed ABORT: its operator refused the
		// SAS, or it could not sign. Same meaning, same path, and answered rather than
		// left to a timeout.
		return nil, ErrNoConsent
	}
	devPayload.ConsentSig = consent

	// Affirmative confirm (R-PAIR.7): send the acceptance over the authenticated channel.
	// Up to this instant a dead ctx must fail the pairing CLOSED, and it does so
	// symmetrically -- no acceptance, no pin, no enrollment.
	if err := sendDecision(ctx, sess, rt, decisionAccept); err != nil {
		_ = rt.Complete(ctx, label)
		return nil, fmt.Errorf("pairing: send acceptance: %w", err)
	}

	// THE MACHINE USED TO COMMIT ON THE STRENGTH OF HAVING *SENT* THIS FRAME, AND NOTHING
	// ACKNOWLEDGED IT (PB-PAIR-4 / PB-SAS-4; ADR-007 B81(2), B86(2)). "Sent" was therefore
	// all it could ever know, and three ordinary things -- the handset's own 60 s TTL
	// elapsing between msg4 and the answer, a relay flipping one bit, a relay accepting the
	// frame and delivering nothing because the peer detached or its 16-slot inbox was full
	// -- all ended the same way: machine enrolled, phone holding nothing, remote control on
	// for a device that does not exist, the single-device slot spent, and every further
	// pairing refused until someone found the device id and ran a desktop revoke. The relay
	// is the declared adversary and chose that at will, so the revoke was a treadmill.
	//
	// So the machine waits to be told. The doubt does not disappear -- this is two generals,
	// and the fifth frame only MOVES the residual -- but it moves it to the harmless side:
	// an acknowledgement lost in transit leaves the phone pinned and this machine claiming
	// NOTHING, which spends no slot, enables no kill switch, and is recovered by pairing
	// again rather than by revoking.
	//
	// THE WAIT IS DETACHED, AND THAT IS THE SAME RULING AS B69(2), one frame later. Before
	// the acceptance leaves, the pairing deadline is correct. Once it has left, the phone
	// may pin at any moment, so cancelling the machine's chance to LEARN that re-creates
	// precisely the half-pair B69(2) fixed -- phone pinned, machine enrolling nothing --
	// from an ordinary clock. It is bounded for the reason B64 exists: an unbounded park is
	// the defect, not the remedy.
	ackCtx, cancelAck := context.WithTimeout(context.WithoutCancel(ctx), acceptAckWindow)
	defer cancelAck()
	if err := recvAck(ackCtx, sess, rt); err != nil {
		_ = burnSlot(ctx, rt, label)
		return nil, fmt.Errorf("%w (%v)", ErrAcceptUnacknowledged, err)
	}

	// Pin the device static + record its routing (R-PAIR.7), and burn the rendezvous. The
	// burn's failure is NOT the pairing's failure: see MachineOutcome.RendezvousUnburned.
	return &MachineOutcome{
		SAS:                sas,
		DeviceStatic:       deviceStatic,
		Device:             devPayload,
		RendezvousUnburned: burnSlot(ctx, rt, label),
	}, nil
}

// sendConsent writes msg4 over the established Noise transport: the device's relay-route
// consent, or a ZERO-LENGTH frame meaning "no route granted". The empty frame is a
// well-formed ABORT rather than silence — the device sends it when its operator refuses
// the SAS and when signing fails — so the machine never parks on a receive reporting a
// timeout for a question that was settled minutes earlier.
func sendConsent(ctx context.Context, sess *crypto.NoiseSession, rt RendezvousTransport, consent []byte) error {
	frame, err := sess.Encrypt(consent)
	if err != nil {
		return err
	}
	return rt.Send(ctx, frame)
}

// abortSendWindow bounds the detached abort send below. It is a LIVENESS bound, not a
// protocol timeout: one small frame on a socket that is already open needs milliseconds,
// and the only reason to wait at all is that the peer may be gone in a way the kernel has
// not noticed yet, where a write buffers rather than failing. Past this the device stops
// trying and returns its operator's answer, which is what they are waiting on.
const abortSendWindow = 2 * time.Second

// abortConsent sends the zero-length msg4 abort on a context DETACHED from the caller's
// (ADR-007 B64).
//
// The frame exists to unpark a machine sitting in recvConsent, and on a shipped phone the
// ONLY way this device reaches an abort is that very ctx being cancelled: mobile's
// DeviceSAS closure returns nil or ctx.Err() and nothing else, RejectSAS and Cancel both
// go through cancelHandshake(), and the 60 s pairingTTL is a deadline on the same ctx.
// Sending on it therefore sent nothing — a relay Send passes the caller's ctx straight to
// ws.Write, which fails outright once it is done — so the one frame that answers the
// machine was the one frame that could never leave.
//
// WithoutCancel rather than Background: the values on the pairing ctx stay, only the
// cancellation is dropped. And a TIMEOUT on top, because the usual reason that ctx is dead
// is that the peer went away — against a socket whose peer is gone but whose FIN never
// arrived, a write blocks until the send buffer fills, and an abort that hangs the phone
// is no better than one that is never sent.
//
// Best-effort by construction: the machine's own deadline is the fence that does not
// depend on this frame arriving, because on the reject path cancelHandshake() has already
// CloseNow()'d the socket underneath us and no context can revive it.
func abortConsent(ctx context.Context, sess *crypto.NoiseSession, rt RendezvousTransport) {
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), abortSendWindow)
	defer cancel()
	_ = sendConsent(sendCtx, sess, rt, nil)
}

// recvConsent reads msg4 and opens it under the authenticated transport. A frame that
// does not decrypt is an error; a frame that decrypts to nothing is the device's abort,
// which the caller distinguishes by length.
func recvConsent(ctx context.Context, sess *crypto.NoiseSession, rt RendezvousTransport) ([]byte, error) {
	frame, err := rt.Recv(ctx)
	if err != nil {
		return nil, err
	}
	return sess.Decrypt(frame)
}

// acceptCommitWindow bounds the detached rendezvous burn, and acceptAckWindow the detached
// wait for the device's acknowledgement of an acceptance. Like abortSendWindow both are
// LIVENESS bounds rather than protocol timeouts: one roundtrip on a socket that is already
// open needs milliseconds, and the only reason to wait at all is a peer that is gone in a
// way the kernel has not noticed. They exist so detaching from the pairing deadline cannot
// re-create B64 -- an unbounded park is the defect that deadline was added to fix.
//
// THE COST OF acceptAckWindow BEING SHORT IS A RETRY, NOT A HALF-PAIR, which is the whole
// difference from ADR-007 B82(1). B82(1) is the observation that a flat 2 s on the BURN
// manufactures a half-pair on a loaded or distant relay; that is fixed below by the burn no
// longer being able to fail the pairing at all. Here a relay too slow to carry the
// acknowledgement inside the window leaves the machine claiming nothing while the phone
// pinned -- a pairing the owner runs again, with the single-device slot free and no revoke
// in the way.
const (
	acceptCommitWindow = 2 * time.Second
	acceptAckWindow    = 2 * time.Second
)

// ackLabel domain-separates the acknowledgement below from every other use of the channel
// binding -- the SAS above all, which is derived from the same value.
const ackLabel = "swarm/pairing/decision-ack/v1"

// acceptAck is what the device sends back for a decision it has received and acted on: a
// domain-separated hash over the Noise CHANNEL BINDING and the decision byte.
//
// IT IS KEYED ON THE CHANNEL BINDING BECAUSE THAT IS THE VALUE THE TWO OPERATORS COMPARED
// (PB-SAS-4, ADR-007 B86(2)). Until this frame existed the SAS attested only the handshake
// that PRECEDED the decision: msg4 and the acceptance ride outside the transcript, so six
// matching emoji said nothing whatever about whether the two sides ended up agreeing, which
// is exactly why a half-pair was invisible to both people looking straight at it. A machine
// that commits only on a frame no party without this binding can produce makes the
// comparison those operators performed reach the accept/decline exchange too.
//
// The frame already rides the Noise transport, so the relay can neither read nor forge it
// and the binding adds no secrecy. What it adds is that the property is STATED: an
// acknowledgement that echoed a byte would be satisfied by any frame the peer emitted,
// including one from a session whose transcript diverged.
func acceptAck(sess *crypto.NoiseSession, decision byte) []byte {
	h := sha256.New()
	h.Write([]byte(ackLabel))
	h.Write(sess.ChannelBinding())
	h.Write([]byte{decision})
	return h.Sum(nil)
}

// sendDecision encrypts the machine's accept/decline signal over the established Noise
// transport and sends it. It is authenticated (both statics are pinned by now), so the
// device knows the decision came from the real machine.
//
// IT NO LONGER BURNS THE RENDEZVOUS. Burning is housekeeping and is separated from the
// decision precisely so it can never be mistaken for it (see burnSlot).
func sendDecision(ctx context.Context, sess *crypto.NoiseSession, rt RendezvousTransport, decision byte) error {
	frame, err := sess.Encrypt([]byte{decision})
	if err != nil {
		return err
	}
	return rt.Send(ctx, frame)
}

// recvAck reads the device's acknowledgement of the acceptance and checks it against this
// session's channel binding. A frame that does not decrypt, or that decrypts to anything
// other than the expected value, is NOT an acknowledgement: the machine claims no device.
func recvAck(ctx context.Context, sess *crypto.NoiseSession, rt RendezvousTransport) error {
	frame, err := rt.Recv(ctx)
	if err != nil {
		return err
	}
	got, err := sess.Decrypt(frame)
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare(got, acceptAck(sess, decisionAccept)) != 1 {
		return errors.New("acknowledgement does not match this session's channel binding")
	}
	return nil
}

// sendAck is the device half of the frame above: it acknowledges an acceptance it has
// received and is about to pin on.
func sendAck(ctx context.Context, sess *crypto.NoiseSession, rt RendezvousTransport) error {
	frame, err := sess.Encrypt(acceptAck(sess, decisionAccept))
	if err != nil {
		return err
	}
	return rt.Send(ctx, frame)
}

// burnSlot completes (burns) the rendezvous after the acceptance has left, on a
// context DETACHED from the caller's and bounded by acceptCommitWindow.
//
// IT RETURNS ITS ERROR RATHER THAN IMPOSING IT (ADR-007 B82(1)). sendDecision used to run
// Send and Complete back to back and fold a Complete failure into the decision's failure,
// so a burn that could not finish -- on a relay merely loaded or distant, AFTER the
// acceptance had already been forwarded and every frame delivered intact -- orphaned a
// phone this machine had already told yes. A burn that cannot finish is housekeeping, not
// consent, and the caller records it as MachineOutcome.RendezvousUnburned.
//
// DETACHING IS B69(2)'s RULING and it is unchanged: past the commit point the pairing
// deadline no longer gets to reverse what the two legs settled. It works because the relay
// conn outlives this call -- relayRendezvousFactory's watcher closes it when the CONNECTION
// ctx is done (internal/skeleton/pairing.go), not the pairing deadline derived from it.
func burnSlot(ctx context.Context, rt RendezvousTransport, label string) error {
	detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), acceptCommitWindow)
	defer cancel()
	return rt.Complete(detached, label)
}

// declineAndBurn answers the device with a decline and burns the rendezvous, both on the
// caller's context and both best-effort: the pairing has already failed for a reason the
// caller holds and returns.
//
// The device is ANSWERED rather than left in silence because it is parked on rt.Recv
// waiting for this machine's decision, and a machine that walks away leaves it there until
// its own deadline elapses -- reporting a timeout for what is actually a refusal, and
// holding the rendezvous unburned meanwhile (ADR-007 B38).
//
// The caller's context is right here, unlike on the accept path: nothing has been committed
// on either side, so a dead ctx costs only the courtesy frame.
func declineAndBurn(ctx context.Context, sess *crypto.NoiseSession, rt RendezvousTransport, label string) {
	_ = sendDecision(ctx, sess, rt, decisionDecline)
	_ = rt.Complete(ctx, label)
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
	// The device's own check on the authenticated machine payload (ADR-007 B48): the relay
	// certificate this dial accepted unverified, against the pin the real machine authored
	// and put in this frame. It runs before anything is written, shown, or signed.
	if p.VerifyMachine != nil {
		if err := p.VerifyMachine(machPayload); err != nil {
			return nil, err
		}
	}
	// A device that cannot produce a consent at all fails CLOSED here, BEFORE msg3, so
	// the machine is never handed a handshake it will have to refuse: a pairing that
	// completes without a route grants the phone a machine that can never deliver it the
	// epoch grant, which is a pairing that looks successful and does nothing.
	if p.Consent == nil {
		return nil, ErrNoConsent
	}
	// msg3 (s, se + device payload): device -> machine. Completes the handshake, and
	// carries NO SIGNATURE (ADR-007 B52) — only the device's statement that one will
	// follow once its operator has compared the SAS. See DevicePayload.ConsentDeferred,
	// and TestB52_NoSharedSASExistsBeforeMsg3 for why the signature cannot stay here:
	// writing msg3 is what CREATES the channel binding, so a payload field is inside the
	// very transcript the SAS attests and cannot be chosen after the SAS is shown.
	//
	// msg3 is SENT here rather than withheld until the gate, which would look strictly
	// safer. It is not: the machine derives its half of the SAS from msg3, so a withheld
	// msg3 leaves the desktop blank and the phone operator comparing against nothing.
	p.Payload.ConsentSig = nil
	p.Payload.ConsentDeferred = true
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
	//
	// AND BEFORE THE CONSENT IS PRODUCED (ADR-007 B46/B52). This gate is the only thing
	// that ever tells this phone it is talking to the wrong machine, so anything released
	// above it is released to whoever is on the wire. A rejecting operator used to have
	// already handed an interceptor a STANDING grant over this phone's relay route —
	// enough to authorize itself and then permanently ban the phone, whose relay-auth key
	// is minted once per install.
	if p.DeviceSAS != nil {
		if err := p.DeviceSAS(ctx, sas); err != nil {
			abortConsent(ctx, sess, rt) // an ANSWERED refusal, not silence
			return nil, err
		}
	}

	// msg4: the relay-route consent (ADR-007 B27/B38), signed over the machine the device
	// AUTHENTICATED in msg2 — the consent names the routing id derived from that frame's
	// relay-auth key, so a consent built before the handshake would name whatever machine
	// the device was configured for, which on a photographed QR is not the one on the wire.
	consent, cErr := p.Consent(machPayload)
	if cErr != nil || len(consent) == 0 {
		abortConsent(ctx, sess, rt)
		if cErr != nil {
			return nil, fmt.Errorf("pairing: sign relay-route consent: %w", cErr)
		}
		return nil, ErrNoConsent
	}
	// A failed send is REMEMBERED, not returned: the machine may have declined and burned
	// the rendezvous while the operator was still comparing, in which case its decision is
	// already waiting below and ErrPairingDeclined is the honest cause, not a transport
	// error naming a symptom.
	sendErr := sendConsent(ctx, sess, rt, consent)

	// Wait for the machine's authenticated decision (R-PAIR.5). No machine static
	// is pinned unless the machine affirmatively accepts; a decline / timeout on
	// the machine side surfaces here as ErrPairingDeclined with no pin.
	frame, err := rt.Recv(ctx)
	if err != nil {
		if sendErr != nil {
			return nil, fmt.Errorf("pairing: send relay-route consent: %w", sendErr)
		}
		return nil, fmt.Errorf("pairing: recv decision: %w", err)
	}
	decision, err := sess.Decrypt(frame)
	if err != nil {
		return nil, fmt.Errorf("pairing: decrypt decision: %w", err)
	}
	if len(decision) != 1 || decision[0] != decisionAccept {
		return nil, ErrPairingDeclined
	}

	// ACKNOWLEDGE THE ACCEPTANCE BEFORE PINNING ON IT (PB-PAIR-4 / PB-SAS-4). The machine
	// holds no device until this frame reaches it, so a phone that pins without sending one
	// would leave the machine claiming a device the phone does not know about -- the
	// orientation that spends the single-device slot and needs a desktop revoke.
	//
	// A FAILED SEND FAILS THE PAIRING CLOSED, and that is the cheap half of the two-generals
	// split: where this device knows its acknowledgement did not leave, the machine will not
	// have it either, so declining to pin is the two legs agreeing rather than a loss. Where
	// the send REPORTS success and the frame is dropped anyway -- the relay does exactly that
	// when the peer has detached or its inbox is full -- this phone pins and the machine
	// claims nothing. That residual is irreducible, and it is the harmless orientation:
	// re-pairing needs nothing from the desktop.
	if err := sendAck(ctx, sess, rt); err != nil {
		return nil, fmt.Errorf("pairing: acknowledge acceptance: %w", err)
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
// ADR-007 2026-07-20 + B38's route consent + B52's deferral marker): six
// length-prefixed byte fields followed by the one-byte marker. The marker rides
// last, so the six-field prefix is byte-identical to the previous encoding and a
// truncated frame still fails as a malformed payload rather than as a false
// deferral.
func encodeDevicePayload(p DevicePayload) []byte {
	var b []byte
	b = appendField(b, []byte(p.DeviceName))
	b = appendField(b, p.DeviceRoutingID)
	b = appendField(b, p.DeviceRelayAuthPub)
	b = appendField(b, p.RecipientPub)
	b = appendField(b, p.DeviceCommandSignPub)
	b = appendField(b, p.ConsentSig)
	var marker byte
	if p.ConsentDeferred {
		marker = 1
	}
	return append(b, marker)
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
	if len(b) != 1 {
		return DevicePayload{}, errMalformedPayload
	}
	p.ConsentDeferred = b[0] == 1
	return p, nil
}
