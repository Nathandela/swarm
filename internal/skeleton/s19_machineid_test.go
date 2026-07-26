package skeleton

// FAILING-FIRST (TDD RED, GG-5) tests for the MACHINE half of slice S19's first production
// hole: the daemon never told the phone its own endpoint id, so a paired phone could name no
// machine and crypto.Command.Canonical refused every signed verb it authored.
//
// TWO LINKS, TWO TESTS, and neither one alone is the property:
//
//	(a) loadPairingConfig must carry the endpoint id the daemon SERVES UNDER, not some other
//	    value. A config field populated from a second, independently-derived id would satisfy
//	    any assertion about non-emptiness and still address the phone's commands to a machine
//	    that does not answer to them.
//	(b) BeginPairing must PUBLISH it. A payload field the codec round-trips (the pairing
//	    package's own S19 test) and the host never fills reaches no phone at all.
//
// WHY THE SECOND TEST STILL INJECTS A pairingConfig. injectPairing replaces the whole config,
// which is exactly the kind of fixture that hid this hole -- so it is deliberately NOT the
// subject here: test (a) is what ties the config's value to the daemon's, over the production
// loader, and (b) only asks what BeginPairing does with the value it is handed.

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/pairing"
)

// TestS19_PairingConfigCarriesTheEndpointIdTheDaemonServesUnder is link (a).
//
// The assertion is against a LIVE daemon's client-visible endpoint id rather than against
// endpointID(dir), because the latter is the implementation restated: it would pass on a
// loader that computed the id correctly and a Serve that published a different one.
func TestS19_PairingConfigCarriesTheEndpointIdTheDaemonServesUnder(t *testing.T) {
	sk := assemble(t)
	dir := sk.stateDir

	// Provision the machine identity the production loader reads. assemble's state dir has
	// none (pairing is simply unconfigured there), so this is `swarm remote init`'s durable
	// half, exactly as the S19 exit demonstration writes it.
	writeTestIdentity(t, dir, "s19-config.local")

	cfg, err := loadPairingConfig(dir)
	if err != nil {
		t.Fatalf("loadPairingConfig: %v", err)
	}
	if cfg == nil {
		t.Fatal("loadPairingConfig returned nil for a present, valid identity")
	}

	owner, err := protocol.Dial(sk.SocketPath(), nil)
	if err != nil {
		t.Fatalf("protocol.Dial: %v", err)
	}
	t.Cleanup(func() { _ = owner.Close() })

	want := owner.EndpointID()
	if want == "" {
		t.Fatal("the daemon reported no endpoint id at hello; there is nothing for a phone to name")
	}
	if cfg.EndpointID != want {
		t.Fatalf("loadPairingConfig carries endpoint id %q while the daemon serves under %q. "+
			"Every mutating command the phone authors signs over this value and the daemon "+
			"verifies the signature against its own, so a divergence refuses every verb -- and "+
			"an empty one is refused by crypto.Command.Canonical before a byte is sealed",
			cfg.EndpointID, want)
	}
}

// TestS19_BeginPairingPublishesTheMachineEndpointID is link (b): the id on the config
// reaches the DEVICE, through the real Noise handshake the host drives.
func TestS19_BeginPairingPublishesTheMachineEndpointID(t *testing.T) {
	sk := assemble(t)
	deviceEnds := injectPairing(t, sk)
	const machineEndpoint = "ep-s19cfg01"
	sk.api.pairing.EndpointID = machineEndpoint

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	view, err := sk.api.BeginPairing(ctx, protocol.PairStartReq{Capability: "full"},
		func(sas []string, name string) (bool, error) { return true, nil },
		func(protocol.PairResult) {})
	if err != nil {
		t.Fatalf("BeginPairing: %v", err)
	}
	qp, err := pairing.DecodeQR(view.QR)
	if err != nil {
		t.Fatalf("DecodeQR: %v", err)
	}

	ks, err := crypto.NewFileKeyStore(filepath.Join(t.TempDir(), "phone"))
	if err != nil {
		t.Fatalf("phone keystore: %v", err)
	}
	dEnd := recvDeviceEnd(t, deviceEnds)
	res := <-runDeviceLeg(ctx, ks, dEnd, qp)
	if res.err != nil {
		t.Fatalf("device leg: %v", res.err)
	}
	if res.outcome == nil {
		t.Fatal("the device leg produced no outcome")
	}
	if got := res.outcome.Machine.MachineEndpointID; got != machineEndpoint {
		t.Fatalf("the paired device received machine endpoint id %q, want %q. BeginPairing "+
			"assembles MachinePayload, so an id the config holds and the payload omits never "+
			"leaves the machine", got, machineEndpoint)
	}
}
