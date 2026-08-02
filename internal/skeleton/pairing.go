package skeleton

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/enroll"
	"github.com/Nathandela/swarm/internal/remote/grant"
	"github.com/Nathandela/swarm/internal/remote/pairing"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// defaultPairTTL bounds a rendezvous when the request carries no explicit TTL, and
// pairWindow caps every announced expiry at the relay's authoritative slot lifetime.
//
// IT WAS 3 MINUTES AGAINST A 60-SECOND SLOT (ADR-007 B46). The old comment called this
// expiry "advisory... the daemon's real gate is the mandatory SAS confirm, not a wall
// clock". The SAS is indeed the gate for what a pairing MEANS, and it decides nothing
// about whether the rendezvous still exists: past relay.Config.RendezvousTTL the slot is
// purged, and this daemon went on printing an expiry two minutes into that gap with the
// QR still on the owner's screen. That gap is the interval an expired rendezvous id can
// be re-created in (B47b, fenced at the relay by burnRendezvous) -- so the announcement
// is brought back inside the thing it announces rather than left to be caught downstream.
//
// The bound is the DEFAULT relay config's TTL because that is the only value a machine
// can know: the deployed relay's own setting is not on any wire the daemon reads, and the
// phone transcribes the same 60 s constant (mobile/pairing.go). Announcing SHORTER than a
// relay that was tuned longer costs a retry; announcing longer is the defect above.
const defaultPairTTL = 3 * time.Minute

// pairWindow is the announced expiry for a requested TTL: the request, or the daemon
// default when it asks for nothing, clamped to the relay slot.
func pairWindow(requested time.Duration) time.Duration {
	if requested <= 0 {
		requested = defaultPairTTL
	}
	if slot := relay.DefaultConfig().RendezvousTTL; requested > slot {
		return slot
	}
	return requested
}

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
	// RelaySPKIPin is the SHA-256 of the relay certificate's SubjectPublicKeyInfo, read
	// from relay.json and carried VERBATIM into MachinePayload (ADR-007 B33/B34). Empty
	// means no pin is configured; this daemon neither derives nor validates one.
	RelaySPKIPin []byte

	// EndpointID is the daemon's federation endpoint id, carried into MachinePayload so
	// the paired phone can NAME the machine it just paired with (S19). Every mutating
	// command the phone authors signs over it and this daemon verifies that signature
	// against its own id, so it must be the id the daemon SERVES under -- loadPairingConfig
	// derives it from the same state directory serve.go does, from the same function.
	// Without it a completed pairing leaves the phone unable to author anything:
	// crypto.Command.Canonical refuses an empty Machine before a byte is sealed.
	EndpointID string

	// RelayURL is the configured relay endpoint, carried VERBATIM into the pairing QR
	// (PB-PAIR-7) so a phone that has only scanned the camera channel knows where to
	// dial. It is the same string NewRendezvous was built from -- the machine's own
	// dial target is the one endpoint known reachable -- and it is never rewritten or
	// normalized, because a rewritten URL is a different destination.
	RelayURL string

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

	// Snapshot the pairing pointer under pairingMu (RevokeDevice reassigns it on an epoch
	// rotation), then release BEFORE the long handshake -- cfg is an immutable snapshot.
	a.pairingMu.Lock()
	cfg := a.pairing
	a.pairingMu.Unlock()
	if cfg == nil {
		return protocol.PairView{}, errors.New("pairing not configured on this daemon")
	}

	// C6 (single-device v1, ADR-007 2026-07-24): the gateway assumes exactly one paired
	// device, so refuse a second pairing FAIL-FAST -- before minting any rendezvous
	// id/secret/QR or spawning a handshake -- and leave the existing device untouched.
	// Re-pairing is revoke-then-pair (revoke drops Count to 0). Single-owner-serial: two
	// concurrent owner pairings is out of scope (pairing is owner-tier, one in flight per
	// connection). The Registry itself stays uncapped; enforcement lives here at the
	// pairing layer so the registry's own tests are unaffected.
	//
	// THE REFUSAL NAMES ITS OWN REMEDY BY THE COMMANDS THAT PERFORM IT (PB-STATE-10). This
	// is the wall a stranded handset's owner hits: the phone told them to pair again, and
	// this is what they get. "Revoke it first" is true and unusable -- it names no verb, and
	// no way to learn the device id the verb needs -- so the recovery would be reachable only
	// by an operator who already knew it.
	if a.devices != nil && a.devices.Count() > 0 {
		return protocol.PairView{}, errors.New("a device is already paired (single-device v1); " +
			"run `swarm remote devices` to see its id, then `swarm remote revoke <device-id>` " +
			"to unregister it, and pair again")
	}

	// C7: a nil rendezvous seam means no relay is configured (relay.json absent, see
	// pairing_config.go). Guard the unconditional cfg.NewRendezvous call below, which
	// would otherwise panic on the nil func, and return a clean, actionable error.
	if cfg.NewRendezvous == nil {
		return protocol.PairView{}, errors.New("relay not configured; run `swarm remote init` with a relay URL before pairing")
	}

	// PB-PAIR-7 fail-closed: the QR must carry an endpoint. Today no path reaches here
	// without one (loadPairingConfig sets RelayURL and NewRendezvous from the same read),
	// but that invariant is COINCIDENTAL, not enforced: pairing.EncodeQR encodes an empty
	// RelayURL perfectly happily -- the field is length-prefixed, so empty is well-formed --
	// and the QR is minted BEFORE cfg.NewRendezvous is exercised, so the guard above runs
	// too late to be the one carrying it. State the precondition where it belongs rather
	// than mint a QR that leaves the scanning phone with an id, a secret and nowhere to dial.
	if strings.TrimSpace(cfg.RelayURL) == "" {
		return protocol.PairView{}, errors.New("relay endpoint not configured; run `swarm remote init` with a relay URL before pairing")
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

	// The QR a real phone scans to recover the relay endpoint, the rendezvous id, and the
	// single-use secret it drives the device leg with (R-PAIR.2, PB-PAIR-7). Without the
	// endpoint the phone holds an id and a secret and no address to dial, so it can never
	// claim the rendezvous. MachineStaticPub is deliberately NOT pinned here in v1: it
	// costs 43 more payload characters, pushing the symbol past what a standard 80x24
	// terminal can draw (PB-PAIR-1(b)), and the machine static is already pinned from
	// Noise msg2 with the SAS compare as the designed human anti-MITM check.
	qr, err := pairing.EncodeQR(pairing.QRPayload{
		RelayURL:      cfg.RelayURL,
		RendezvousID:  id,
		PairingSecret: secret,
	})
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
		//
		// IT ALSO OBSERVES THE PAIRING WINDOW, AND ONLY SINCE ADR-007 B69(3). The adapter
		// used to take the ctx as `_`. Machine.Pair observes its ctx in transport calls
		// only, so the window bounded Recv/Send/Complete and left the third leg -- the one
		// with a human on the end -- unbounded: the closure below selects on the pairing
		// session ctx, which internal/protocol/server.go builds as context.WithCancel(
		// context.Background()) and which carries no deadline. An owner who walked away
		// from the prompt reproduced B64's ENTIRE consequence chain, with a phone doing
		// everything right: no pair_result ever, and every later pair_start on the
		// connection refused "pairing already in progress".
		//
		// The prompt is CANCELLED rather than left standing with the slot released,
		// because past the window an affirmative answer cannot be honoured by anything:
		// the relay has purged the rendezvous at the slot TTL pairWindow clamps to, and
		// the handset gave up at its own 60 s pairingTTL. Cancelling it is also what
		// releases the slot -- clearPairing runs only from result, and result runs only
		// when this handshake goroutine returns.
		//
		// The abandoned confirm goroutine is bounded by the result it produces, not
		// leaked: Pair returns ErrConfirmTimeout -> result -> clearPairing -> the pairing
		// session's cancel -> the blocked closure returns ctx.Err() into a buffered
		// channel nobody reads. ErrConfirmTimeout rather than ctx.Err() because
		// ConfirmFunc owns the confirm clock by contract, and the window IS that clock now.
		Confirm: func(ctx context.Context, sas [6]string, deviceName string) (bool, error) {
			type answer struct {
				allow bool
				err   error
			}
			answered := make(chan answer, 1)
			go func() {
				allow, err := confirm(sas[:], deviceName)
				answered <- answer{allow, err}
			}()
			select {
			case a := <-answered:
				return a.allow, a.err
			case <-ctx.Done():
				return false, pairing.ErrConfirmTimeout
			}
		},
		Payload: pairing.MachinePayload{
			Hostname:            cfg.Hostname,
			MachineRoutingID:    cfg.RoutingID,
			MachineRelayAuthPub: cfg.RelayAuthPub,
			RecipientPub:        cfg.RecipientPub,
			MachineSignPub:      cfg.SignPub,
			MachineEndpointID:   cfg.EndpointID,
			RelaySPKIPin:        cfg.RelaySPKIPin,
			EpochID:             cfg.EpochID,
		},
	}

	now := a.now()

	// ENFORCE the window this call is about to announce (ADR-007 B64). pairWindow used to
	// be the ExpiresAt in the PairView and NOTHING else: the handshake ran on the
	// connection-lifetime ctx (internal/protocol/server.go's context.WithCancel(
	// context.Background())), which carries no deadline, so past the announced instant the
	// goroutine below stayed parked in recvConsent indefinitely.
	//
	// That is not merely a slow pairing, because the goroutine is the only thing that ever
	// calls result: no result means no clearPairing, so the connection's single pairing slot
	// is held forever and every later pair_start on it is refused "pairing already in
	// progress". There is no pair_cancel op; dropping the owner connection was the only exit.
	//
	// It is reached in the ORDINARY order of the ceremony, with no attacker: the owner
	// answers the desktop prompt in front of them first, then turns to the phone, and from
	// that moment anything ending the phone's leg leaves this side on a receive with no
	// clock. The phone's abort frame is sent again now (pairing.abortConsent), but it cannot
	// be the fence -- RejectSAS CloseNow()s the socket underneath it, and a phone that loses
	// the network sends nothing at all.
	//
	// The bound is the DURATION rather than the expiresAt instant below so an injected
	// a.now() cannot skew it: what is promised and what is enforced stay the same value.
	window := pairWindow(time.Duration(req.TTLSeconds) * time.Second)
	pairCtx, cancelPair := context.WithTimeout(ctx, window)

	go func() {
		defer cancelPair()
		outcome, err := pairing.NewMachine(mp).Pair(pairCtx, transport)
		if err != nil {
			result(protocol.PairResult{Err: err})
			return
		}
		// The COMMIT (epoch re-check + enroll.Enroll + AddSole + grant.Save) runs under the OUTERMOST
		// lifecycle lock -- but ONLY here, NEVER across the long handshake above -- so a concurrent
		// RevokeDevice (which holds lifecycleMu across its own rotate+remove) cannot interleave
		// between the re-check and AddSole and let AddSole commit under a stale epoch (round-4
		// finding 1, UNANIMOUS). Round-5 finding 2 (codex#5+sonnet#1): the commit computes the
		// PairResult under the lock but the lock is RELEASED (the inner closure's defer) BEFORE the
		// result(...) owner-socket write below, so a blocking notification never stalls a concurrent
		// revoke/pair behind the lock. Pairing and revoke are both rare, human-driven owner-tier ops.
		res := func() protocol.PairResult {
			a.lifecycleMu.Lock()
			defer a.lifecycleMu.Unlock()

			// cfg is the ENTRY snapshot the whole handshake ran under (its EpochID is baked into
			// MachinePayload and the grant we are about to seal). RE-VALIDATE the epoch at this commit
			// point: if a revoke rotated the machine epoch, ABORT and enroll NOTHING (fail closed); the
			// operator retries and picks up the fresh epoch. Under lifecycleMu no rotate can run
			// concurrently, so this check + enroll + AddSole + grant.Save are atomic w.r.t. rotate/remove.
			a.pairingMu.Lock()
			cur := a.pairing
			a.pairingMu.Unlock()
			if cur == nil || cur.EpochID != cfg.EpochID {
				return protocol.PairResult{Err: errors.New("pairing aborted: machine epoch rotated during the handshake; retry")}
			}
			if a.lifecycleGate != nil {
				a.lifecycleGate("pair-commit") // TEST-ONLY seam (nil in production): finding-1 window
			}
			res, err := enroll.Enroll(outcome, capTier, cfg.SignPriv, cfg.EpochID, cfg.GrantSeq, cfg.EpochKeys, now)
			if err != nil {
				return protocol.PairResult{Err: err}
			}
			// C1 (finding, re-audit): commit the enrollment ATOMICALLY. The early Count()>0
			// fast-reject above is only advisory (it races the background confirm); AddSole is
			// the real gate -- under the registry mutex it refuses a SECOND, different device,
			// so two concurrent owner pairings can never both enroll and brick the gateway.
			if err := a.devices.AddSole(res.Record); err != nil {
				return protocol.PairResult{Err: err}
			}
			// C5 (daemon half, ADR-007 2026-07-24): persist the sealed grant addressable by
			// device id so the separate gateway process can deliver it to the phone over the
			// relay mailbox (BeginPairing used to DISCARD res.Grant). Persist AFTER AddSole so a
			// confirmed+enrolled device is the precondition.
			//
			// C2 (finding, re-audit): enrollment is TRANSACTIONAL. A grant-write failure must
			// leave NOTHING enrolled -- otherwise the device sits in the registry (Count=1),
			// blocking re-pairing, yet reports failure, recoverable only by an explicit revoke.
			// Roll the device back before reporting failure so a clean retry works. Fail CLOSED:
			// the write error is the reported cause (the rollback itself is best-effort).
			if err := grant.Save(a.registryDir(), res.Record.DeviceID, res.Grant); err != nil {
				_, _ = a.devices.Remove(res.Record.DeviceID)
				return protocol.PairResult{Err: fmt.Errorf("persist epoch grant: %w", err)}
			}
			return protocol.PairResult{
				DeviceID:   res.Record.DeviceID,
				Name:       res.Record.Name,
				Capability: req.Capability,
			}
		}()
		result(res) // OUTSIDE lifecycleMu (round-5 finding 2): the owner-socket write cannot stall a revoke/pair
	}()

	expiresAt := now.Add(window)
	return protocol.PairView{
		QR:           qr,
		RendezvousID: hex.EncodeToString(id[:]),
		ExpiresAt:    &expiresAt,
	}, nil
}

// registryDir is where the device registry and its per-device sealed-grant sidecars
// live (<stateDir>/devices), matching serve.go's device.Open and the gateway's
// resolveGatewayParams. The grant sidecar (internal/remote/grant) is co-located so the
// gateway process locates it by the same convention.
func (a *coreAPI) registryDir() string { return filepath.Join(a.stateDir, "devices") }

// coreAPI ALSO satisfies protocol.PairingHost so an assembled owner-tier Server can host
// a real pairing (slice A3.3-d). A nil pairingConfig makes BeginPairing fail closed.
var _ protocol.PairingHost = (*coreAPI)(nil)
