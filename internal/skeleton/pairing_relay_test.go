package skeleton

// FAILING-FIRST tests for remote slice A3.3-e: the LIVE-RELAY rendezvous adapter
// that makes REAL relay-backed pairing work end to end. Slice A3.3-d proved the
// daemon hosts a real Noise pairing over an in-memory memRendezvous; A4-1b loaded
// the machine identity into coreAPI.pairing. What is STILL missing is the seam that
// turns a configured relay URL into a live rendezvous: pairingConfig.NewRendezvous
// is left nil by loadPairingConfig today (pairing_config.go), so coreAPI.BeginPairing
// cannot open a rendezvous against a real relay. This slice wires that seam.
//
// INTENDED PRODUCTION (RED — none of this exists yet; GREEN implements EXACTLY this):
//
//	1. A relay-URL config. `swarm remote init` accepts a `--relay-url <url>` flag and
//	   writes it to <stateDir>/remote/relay.json as {"relay_url":"..."} (0600). The
//	   reader half lives in loadPairingConfig:
//	     - identity present + relay.json present -> pairingConfig.NewRendezvous != nil
//	       (a closure that relay.DialRaw's the configured URL and returns a
//	       pairing.RendezvousTransport bound to the rendezvous id).
//	     - identity present + NO relay.json     -> pairingConfig.NewRendezvous == nil
//	       (pairing without a relay URL stays cleanly unsupported; BeginPairing already
//	       fails closed on a nil rendezvous seam — it must never panic).
//
//	2. A RendezvousTransport adapter (new code in internal/skeleton, or a small new
//	   internal package — NOT modifying the frozen relay/pairing packages). relay.Conn
//	   does NOT satisfy pairing.RendezvousTransport directly: the method NAMES differ
//	   (Conn.RendezvousCreate/Claim/Send/Recv/Complete vs the interface's
//	   Create/Claim/Send/Recv/Complete), and Conn.RendezvousSend / .RendezvousComplete
//	   carry an `id string` argument the interface's Send/Complete do not (the
//	   transport is BOUND to one rendezvous). So a thin wrapper is required: it holds a
//	   *relay.Conn plus the bound rendezvous label and forwards each interface method
//	   to the matching Conn.Rendezvous* call. loadPairingConfig sets NewRendezvous to a
//	   closure that DialRaw's the configured relay URL and returns this wrapper bound to
//	   hex(id) — only when a relay URL is configured (nil otherwise).
//
// RED today: loadPairingConfig always leaves NewRendezvous nil, so the headline test
// fails on the `cfg.NewRendezvous != nil` assertion (the relay-URL wiring is absent).
// This file COMPILES (every symbol it references already exists — relay.*, pairing.*,
// crypto.*, device.*, protocol.*, and the package's own test helpers), so the RED is a
// clean assertion failure, not an undefined-symbol compile error.
//
// Reused from sibling test files (same package `skeleton`, so NOT replicated):
// assemble / dialClient (serve_test.go), dialRemote / rawRemote (remote_journal_test.go),
// awaitControl / runDeviceLeg (pairing_integration_test.go), writeTestIdentity /
// loadPairingConfig (pairing_config_test.go), the pairingConfig type (pairing.go).

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/pairing"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// startPairingRelay boots an in-process relay over plain ws:// on an ephemeral
// localhost port — the same shape internal/remote/relay's own harness uses
// (startTestRelay), reconstructed here because that helper is a relay-package test
// symbol this package cannot import. Pairing only ever drives the rendezvous ops,
// which never touch APNs or the persistence sweeps, so no push sink is wired and the
// default real clock is fine (the 60s rendezvous TTL dwarfs the test's runtime).
// Cleanup closes the server.
func startPairingRelay(t *testing.T) *relay.Server {
	t.Helper()
	cfg := relay.DefaultConfig()
	cfg.Listen = "127.0.0.1:0"
	cfg.TLSMode = "off"
	cfg.DBPath = filepath.Join(t.TempDir(), "relay.db")
	srv, err := relay.New(cfg)
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("relay Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

// writeRelayURL writes the relay-URL config `swarm remote init --relay-url` is
// intended to persist, at the EXACT path loadPairingConfig must read:
// <stateDir>/remote/relay.json, a small JSON object {"relay_url":"..."}. This pins
// the frozen A3.3-e config contract for both halves — the CLI writer and the
// loadPairingConfig reader must agree on this filename + shape.
func writeRelayURL(t *testing.T, stateDir, url string) {
	t.Helper()
	remoteDir := filepath.Join(stateDir, "remote")
	if err := os.MkdirAll(remoteDir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", remoteDir, err)
	}
	b, err := json.Marshal(map[string]string{"relay_url": url})
	if err != nil {
		t.Fatalf("marshal relay.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(remoteDir, "relay.json"), b, 0o600); err != nil {
		t.Fatalf("write relay.json: %v", err)
	}
}

// relayDeviceRendezvous is the TEST's stand-in for the phone leg: a thin
// pairing.RendezvousTransport over a raw relay.Conn, bound to one rendezvous label.
// The device in production is an iOS app (no production Go device adapter), so this
// wrapper lives in the test; it mirrors the exact method mapping the MACHINE-side
// production adapter needs, precisely because relay.Conn's Rendezvous* methods do NOT
// satisfy pairing.RendezvousTransport as-is (different names; Send/Complete take an
// id the interface binds instead).
type relayDeviceRendezvous struct {
	conn  *relay.Conn
	label string // hex(rendezvous id) this transport is bound to
}

func (r *relayDeviceRendezvous) Create(ctx context.Context, id string) error {
	return r.conn.RendezvousCreate(ctx, id)
}

// Claim retries while the relay still reports the id as not-yet-created. The machine
// leg creates the rendezvous ASYNCHRONOUSLY (BeginPairing's background goroutine,
// after the synchronous pair_start reply is already on the wire), so a tight test can
// race the device's claim ahead of the create; the relay answers that window with
// ErrRendezvousExpired (a claim of an unknown id maps to codeRendezvousTTL). A real
// phone scans the QR only after the desktop displays it — strictly after the machine
// created the rendezvous — so this race cannot occur in production. The retry is a
// test-timing seam only, bounded far under the 60s rendezvous TTL so a genuine expiry
// still surfaces as a real error.
func (r *relayDeviceRendezvous) Claim(ctx context.Context, id string) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := r.conn.RendezvousClaim(ctx, id)
		if err == nil || !errors.Is(err, relay.ErrRendezvousExpired) || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (r *relayDeviceRendezvous) Send(ctx context.Context, msg []byte) error {
	return r.conn.RendezvousSend(ctx, r.label, msg)
}

func (r *relayDeviceRendezvous) Recv(ctx context.Context) ([]byte, error) {
	return r.conn.RendezvousRecv(ctx)
}

func (r *relayDeviceRendezvous) Complete(ctx context.Context, id string) error {
	return r.conn.RendezvousComplete(ctx, id)
}

// Compile-time proof the wrapper shape is a valid RendezvousTransport — and, by
// mirroring it, evidence the machine-side production adapter is a small wrapper, not
// a direct relay.Conn (which does not satisfy the interface).
var _ pairing.RendezvousTransport = (*relayDeviceRendezvous)(nil)

// TestPairing_OverRealRelayEnrollsDevice is the headline A3.3-e proof: a REAL
// device<->machine pairing over an ACTUAL relay (in-process, but a genuine websocket
// rendezvous — not the memRendezvous stand-in). The machine leg runs on the
// PRODUCTION relay adapter that loadPairingConfig wires from a configured relay URL;
// the device leg connects to the SAME relay with its own raw client. An affirmative
// SAS confirm enrolls the device.
//
// RED today: loadPairingConfig leaves NewRendezvous nil (no relay-URL config, no
// adapter), so this fails at the `cfg.NewRendezvous != nil` gate — the exact missing
// A3.3-e wiring. After GREEN, the full relay-backed flow must complete.
func TestPairing_OverRealRelayEnrollsDevice(t *testing.T) {
	// 1. A real, in-process relay: the SINGLE rendezvous both legs meet on.
	srv := startPairingRelay(t)

	// 2. Provision the machine identity AND the relay-URL config, then load the pairing
	// config through the PRODUCTION loader. With a relay URL configured, loadPairingConfig
	// must wire a relay-backed NewRendezvous — nil today, which is the RED below.
	stateDir := t.TempDir()
	writeTestIdentity(t, stateDir, "relay-machine.local")
	writeRelayURL(t, stateDir, srv.URL())

	cfg, err := loadPairingConfig(stateDir)
	if err != nil {
		t.Fatalf("loadPairingConfig with a machine identity + relay.json: %v", err)
	}
	if cfg == nil {
		t.Fatal("loadPairingConfig returned nil despite a present identity + relay.json")
	}
	if cfg.NewRendezvous == nil {
		t.Fatal("loadPairingConfig left NewRendezvous nil despite a configured relay URL; " +
			"A3.3-e must wire a relay-backed rendezvous adapter (relay.DialRaw(relay_url) -> " +
			"pairing.RendezvousTransport bound to hex(id)) when <stateDir>/remote/relay.json is present")
	}

	// 3. Assemble a live daemon and wire the REAL relay-backed pairing config onto it
	// (mirrors injectPairing, but with the production relay adapter, not a memRendezvous).
	sk := assemble(t)
	sk.api.pairing = cfg

	if got := sk.api.devices.List(); len(got) != 0 {
		t.Fatalf("registry not empty before pairing: %d devices", len(got))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	// 4. Drive the owner-tier pair_start; the reply carries the QR a real phone scans.
	rc := dialRemote(t, sk.SocketPath(), protocol.CapPairing)
	rc.write(protocol.Control{Op: protocol.OpPairStart, EndpointID: rc.endpointID,
		Pairing: &protocol.PairingControl{Capability: "full"}})

	reply := awaitControl(t, rc, protocol.OpPairStart)
	if reply.Pairing == nil || reply.Pairing.QR == "" || reply.Pairing.RendezvousID == "" {
		t.Fatalf("pair_start reply missing QR/RendezvousID: %+v", reply.Pairing)
	}
	qp, err := pairing.DecodeQR(reply.Pairing.QR)
	if err != nil {
		t.Fatalf("pair_start QR is not a decodable pairing QR: %v", err)
	}

	// 5. The DEVICE leg: connect to the SAME relay with its OWN raw client, claim the
	// rendezvous id recovered from the QR, and drive the XXpsk0 handshake as initiator.
	// This is the real phone side, over the real relay.
	devConn, err := relay.DialRaw(ctx, srv.URL())
	if err != nil {
		t.Fatalf("device DialRaw to the relay: %v", err)
	}
	t.Cleanup(func() { _ = devConn.Close() })
	dEnd := &relayDeviceRendezvous{conn: devConn, label: hex.EncodeToString(qp.RendezvousID[:])}

	ks, err := crypto.NewFileKeyStore(t.TempDir())
	if err != nil {
		t.Fatalf("device keystore: %v", err)
	}
	devDone := runDeviceLeg(ctx, ks, dEnd, qp)

	// 6. With the handshake complete over the real relay, the host pushes the SAS gate.
	pending := awaitControl(t, rc, protocol.OpPairPending)
	if pending.Pairing == nil || len(pending.Pairing.SAS) != 6 {
		t.Fatalf("pair_pending missing the 6-word SAS gate: %+v", pending.Pairing)
	}
	if pending.Pairing.DeviceName != "Test iPhone" {
		t.Fatalf("pair_pending DeviceName = %q; want %q", pending.Pairing.DeviceName, "Test iPhone")
	}

	// 7. Approve at the SAS gate: this — and only this — mints the device authority.
	rc.write(protocol.Control{Op: protocol.OpPairConfirm, EndpointID: rc.endpointID,
		Pairing: &protocol.PairingControl{Allow: true}})

	res := awaitControl(t, rc, protocol.OpPairResult)
	if res.Pairing == nil || res.Pairing.DeviceID == "" {
		t.Fatalf("pair_result = %+v; want success carrying the new DeviceID", res.Pairing)
	}

	// 8. The device leg pinned the machine over the real relay (the handshake truly
	// completed end to end, not just the machine half).
	select {
	case r := <-devDone:
		if r.err != nil {
			t.Fatalf("device leg failed over the real relay: %v", r.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("device leg never completed over the real relay")
	}

	// 9. The registry gained exactly the enrolled device, bound to its pinned
	// command-signing key, at the requested tier — proving REAL relay-backed pairing.
	recs := sk.api.devices.List()
	if len(recs) != 1 {
		t.Fatalf("registry has %d devices after pairing; want exactly 1 enrolled device", len(recs))
	}
	wantID := device.DeviceIDFor(ks.CommandSigningPublic())
	if recs[0].DeviceID != wantID {
		t.Fatalf("enrolled DeviceID = %q; want %q (bound to the pinned command-signing key)", recs[0].DeviceID, wantID)
	}
	if res.Pairing.DeviceID != wantID {
		t.Fatalf("pair_result DeviceID = %q; want %q (must match the enrolled record)", res.Pairing.DeviceID, wantID)
	}
	if recs[0].Capability != device.CapFull {
		t.Fatalf("enrolled Capability = %v; want CapFull", recs[0].Capability)
	}
	if !sk.api.RemoteControlEnabled() {
		t.Fatal("RemoteControlEnabled() is false after a device was enrolled over the real relay; want true")
	}
}

// TestLoadPairingConfig_NoRelayURLLeavesRendezvousNil pins the other arm of the
// relay-URL config: a present, valid machine identity but NO relay.json must still
// load a pairing config (the identity is valid) yet leave NewRendezvous nil, so
// pairing-without-a-relay-URL is cleanly unsupported — BeginPairing fails closed
// rather than panicking on a nil rendezvous seam.
//
// This is a regression guard for the fail-closed default the A3.3-e wiring must
// preserve: only a CONFIGURED relay URL turns NewRendezvous non-nil.
func TestLoadPairingConfig_NoRelayURLLeavesRendezvousNil(t *testing.T) {
	stateDir := t.TempDir()
	writeTestIdentity(t, stateDir, "no-relay-host") // identity, but deliberately NO relay.json

	cfg, err := loadPairingConfig(stateDir)
	if err != nil {
		t.Fatalf("loadPairingConfig with an identity but no relay.json: %v", err)
	}
	if cfg == nil {
		t.Fatal("loadPairingConfig returned nil for a present, valid identity; want a config with nil NewRendezvous")
	}
	if cfg.NewRendezvous != nil {
		t.Fatal("loadPairingConfig set NewRendezvous despite NO relay URL configured; " +
			"without a relay URL pairing must stay cleanly unsupported (nil NewRendezvous)")
	}
}
