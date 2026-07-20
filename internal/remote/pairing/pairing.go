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
//	type ConfirmFunc func(ctx, sas [4]string, deviceName string) (bool, error)  // desktop confirm (R-PAIR.5)
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
	"errors"

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
type ConfirmFunc func(ctx context.Context, sas [4]string, deviceName string) (bool, error)

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
	EpochID             uint32
}

// DevicePayload is the device's authenticated msg3 handshake payload (R-PAIR.3;
// RecipientPub added by D.0-A14).
type DevicePayload struct {
	DeviceName         string
	DeviceRoutingID    []byte
	DeviceRelayAuthPub []byte
	RecipientPub       []byte // A14: device sealed-box recipient X25519 pub, pinned at pairing
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
}

// MachineOutcome is the machine's result on an affirmatively-confirmed pairing
// (R-PAIR.7): the SAS shown to the operator, the pinned device Noise-static, and
// the device's exchanged routing payload.
type MachineOutcome struct {
	SAS          [4]string
	DeviceStatic []byte // pinned device Noise-static public key
	Device       DevicePayload
}

// DeviceOutcome is the device's result on a completed pairing (R-PAIR.7): the
// SAS, the pinned machine Noise-static, and the machine's routing payload
// (including the initial EpochID).
type DeviceOutcome struct {
	SAS           [4]string
	MachineStatic []byte // pinned machine Noise-static public key
	Machine       MachinePayload
}

// Machine is the machine-side pairing endpoint for a SINGLE `swarm remote pair`
// invocation. Its secret is single-use (R-PAIR.1) and it listens only while Pair
// runs (R-PAIR.8) — no standing listener between invocations.
type Machine struct {
	// Fields are the implementer's; the stub carries none.
}

// NewMachine builds a machine-side pairing endpoint. It opens NO listener and
// touches NO transport until Pair is called (R-PAIR.8).
func NewMachine(p MachineParams) *Machine { return &Machine{} }

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
	return nil, ErrUnimplemented
}

// Listening reports whether a pairing listener is currently active. It is false
// before Pair starts and after it returns (R-PAIR.8: no standing listener).
func (m *Machine) Listening() bool { return false }

// RunDevice runs one device-side pairing attempt over rt: claim the rendezvous,
// drive the XXpsk0 handshake as initiator, derive the SAS, and finalize by
// pinning the machine static + recording its routing (R-PAIR.7) once the machine
// affirmatively accepts. Returns ErrPairingDeclined if the machine declines or
// times out.
func RunDevice(ctx context.Context, p DeviceParams, rt RendezvousTransport) (*DeviceOutcome, error) {
	return nil, ErrUnimplemented
}
