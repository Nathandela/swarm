package skeleton

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/enroll"
	"github.com/Nathandela/swarm/internal/remote/pairing"
)

// defaultPairTTL bounds a rendezvous when the request carries no explicit TTL. It is
// advisory (the pair_start ExpiresAt the phone displays); the daemon's real gate is
// the mandatory SAS confirm, not a wall clock.
const defaultPairTTL = 3 * time.Minute

// pairingConfig carries the machine-side pairing identity + enrollment material and
// the rendezvous seam BeginPairing drives one pairing on. It is nil until provisioned
// (a LATER slice: `swarm remote init`), mirroring how the assembly wires
// coreAPI.devices / launchPolicy / stateDir; a nil config makes BeginPairing fail
// closed ("pairing not configured on this daemon"). In production Static/SignPriv/
// EpochKeys come from the daemon keystore; in tests they are generated as
// enroll_e2e_test.go does and NewRendezvous is an in-memory transport.
type pairingConfig struct {
	Static       *crypto.NoiseStatic // machine Noise-static handle (msg2 identity)
	RecipientPub []byte              // machine sealed-box recipient X25519 pub (A14)
	SignPub      []byte              // machine Ed25519 grant-signing pub (phone pins it)
	SignPriv     ed25519.PrivateKey  // machine Ed25519 grant-signing priv (signs the epoch grant)
	EpochID      uint32              // the granted epoch id
	GrantSeq     uint64              // the epoch grant sequence
	EpochKeys    crypto.EpochKeys    // wake/content keys sealed to the paired device
	Hostname     string              // MachinePayload.Hostname
	RoutingID    []byte              // MachinePayload.MachineRoutingID
	RelayAuthPub []byte              // MachinePayload.MachineRelayAuthPub

	// NewRendezvous returns the machine-side RendezvousTransport for a freshly minted
	// rendezvous id. BeginPairing mints the id + single-use secret + QR, then asks this
	// for the transport it drives the machine leg on (a relay adapter in prod; an
	// in-memory transport in tests).
	NewRendezvous func(ctx context.Context, id [16]byte) (pairing.RendezvousTransport, error)
}

// BeginPairing makes coreAPI a protocol.PairingHost (slice A3.3-d): it hosts a REAL
// Noise pairing behind the owner-tier pair_start/pair_confirm wire. It SYNCHRONOUSLY
// mints a rendezvous id + single-use secret + decodable QR, opens the machine-side
// transport, and returns the PairView; it runs the handshake in a background goroutine
// whose SAS gate is the passed-in confirm. Device authority is minted ONLY on an
// affirmative confirm: enroll.Enroll -> devices.Add, then a success result. On decline,
// disconnect (ctx cancel -> confirm returns a non-nil error), or any error, it reports
// a failure result and enrolls NOTHING (fail closed).
func (a *coreAPI) BeginPairing(ctx context.Context, req protocol.PairStartReq,
	confirm func(sas []string, deviceName string) (bool, error),
	result func(protocol.PairResult)) (protocol.PairView, error) {

	cfg := a.pairing
	if cfg == nil {
		return protocol.PairView{}, errors.New("pairing not configured on this daemon")
	}

	// The capability the new device is granted (fail-closed: an unknown or empty tier
	// aborts the pairing before any transport work rather than defaulting to authority).
	var capTier device.Capability
	if err := capTier.UnmarshalText([]byte(req.Capability)); err != nil {
		return protocol.PairView{}, err
	}

	// Mint the rendezvous id + single-use pairing secret (crypto/rand). They are
	// INDEPENDENT: the relay only ever sees the id; the secret is the out-of-band
	// camera channel (the QR), never on the wire.
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return protocol.PairView{}, fmt.Errorf("mint rendezvous id: %w", err)
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return protocol.PairView{}, fmt.Errorf("mint pairing secret: %w", err)
	}

	// The QR a real phone scans to recover BOTH the rendezvous id and the single-use
	// secret it drives the device leg with (R-PAIR.2).
	qr, err := pairing.EncodeQR(pairing.QRPayload{RendezvousID: id, PairingSecret: secret})
	if err != nil {
		return protocol.PairView{}, fmt.Errorf("encode pairing qr: %w", err)
	}

	transport, err := cfg.NewRendezvous(ctx, id)
	if err != nil {
		return protocol.PairView{}, fmt.Errorf("open rendezvous: %w", err)
	}

	mp := pairing.MachineParams{
		Static:       cfg.Static,
		Secret:       secret,
		RendezvousID: id,
		LocalConsole: true,
		// The machine-side SAS gate: adapt the host confirm to pairing.ConfirmFunc. The
		// server's confirm closure selects on the connection-derived ctx, so a disconnect
		// makes this return (false, non-nil err) -> Machine.Pair declines and errors ->
		// enroll/Add never run (fail closed).
		Confirm: func(_ context.Context, sas [6]string, deviceName string) (bool, error) {
			return confirm(sas[:], deviceName)
		},
		Payload: pairing.MachinePayload{
			Hostname:            cfg.Hostname,
			MachineRoutingID:    cfg.RoutingID,
			MachineRelayAuthPub: cfg.RelayAuthPub,
			RecipientPub:        cfg.RecipientPub,
			MachineSignPub:      cfg.SignPub,
			EpochID:             cfg.EpochID,
		},
	}

	now := a.now()
	go func() {
		outcome, err := pairing.NewMachine(mp).Pair(ctx, transport)
		if err != nil {
			result(protocol.PairResult{Err: err})
			return
		}
		res, err := enroll.Enroll(outcome, capTier, cfg.SignPriv, cfg.EpochID, cfg.GrantSeq, cfg.EpochKeys, now)
		if err != nil {
			result(protocol.PairResult{Err: err})
			return
		}
		if err := a.devices.Add(res.Record); err != nil {
			result(protocol.PairResult{Err: err})
			return
		}
		result(protocol.PairResult{
			DeviceID:   res.Record.DeviceID,
			Name:       res.Record.Name,
			Capability: req.Capability,
		})
	}()

	ttl := time.Duration(req.TTLSeconds) * time.Second
	if ttl <= 0 {
		ttl = defaultPairTTL
	}
	expiresAt := now.Add(ttl)
	return protocol.PairView{
		QR:           qr,
		RendezvousID: hex.EncodeToString(id[:]),
		ExpiresAt:    &expiresAt,
	}, nil
}

// coreAPI ALSO satisfies protocol.PairingHost so an assembled owner-tier Server can host
// a real pairing (slice A3.3-d). A nil pairingConfig makes BeginPairing fail closed.
var _ protocol.PairingHost = (*coreAPI)(nil)
