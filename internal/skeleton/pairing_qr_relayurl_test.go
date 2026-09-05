package skeleton

// FAILING-FIRST tests for Phase B slice S3, requirement PB-PAIR-7 (§4.4, §6.3): the
// pairing QR must carry the RELAY ENDPOINT the scanning phone has to dial.
//
// THE DEFECT (verified at a2b6397). pairing.go's BeginPairing mints
//
//	pairing.EncodeQR(pairing.QRPayload{RendezvousID: id, PairingSecret: secret})
//
// and never sets RelayURL, although the codec reserves wire space for it
// (internal/remote/pairing/qr.go:40,50,68-69) and the URL is available two frames up
// (loadRelayURL, pairing_config.go:81) — it is read only to build the NewRendezvous
// closure and then discarded. A phone that scans today's QR recovers a rendezvous id
// and a single-use secret AND NO ENDPOINT TO DIAL, so it cannot claim the rendezvous.
// "pairs" — the first verb of the Phase B exit criterion — has no destination.
//
// INTENDED PRODUCTION (RED — none of this exists yet; GREEN implements it):
//
//	The relay URL loadPairingConfig already reads survives onto the pairing config
//	(a field, not a closure capture), and BeginPairing puts it in the QR payload:
//
//	    pairing.EncodeQR(pairing.QRPayload{
//	        RelayURL:      <the configured relay URL, verbatim>,
//	        RendezvousID:  id,
//	        PairingSecret: secret,
//	    })
//
//	VERBATIM is the contract these tests pin: the string in <stateDir>/remote/relay.json
//	is what the machine itself dials, so it is the one value known to be reachable and
//	the one PB-PAIR-6's origin-display gate shows the user. A rewritten or normalized
//	URL is a different destination and is not accepted here.
//
//	MachineStaticPub (QRFlagMachineStaticPub) is a SEPARATE, explicit decision PB-PAIR-7
//	leaves to the implementer (pinning it closes a TOFU window but costs 43 characters of
//	payload, which the 80x24 sizing budget in cmd/swarm/remote_pair_qr_test.go does not
//	obviously have). It is deliberately NOT pinned here either way.
//
// RED today: both tests COMPILE (every symbol already exists) and fail on the
// assertion — DecodeQR yields RelayURL == "".
//
// Reused from sibling files in this package, NOT redeclared here: startPairingRelay /
// writeRelayURL / relayDeviceRendezvous (pairing_relay_test.go), writeTestIdentity /
// loadPairingConfig (pairing_config_test.go), assemble (serve_test.go), dialRemote
// (remote_journal_test.go), awaitControl / runDeviceLeg (pairing_integration_test.go).

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/pairing"
	"github.com/Nathandela/swarm/internal/remote/relaycfg"
)

// TestBeginPairing_QRCarriesTheConfiguredRelayURL is the direct PB-PAIR-7 regression
// guard, asserted at the point the payload is MINTED (coreAPI.BeginPairing) rather than
// at the codec — the codec already round-trips RelayURL fine (qr_test.go); the
// production caller is what never sets it.
//
// The relay is real and in-process only so the rendezvous seam loadPairingConfig wires
// has something to dial; the assertion is entirely about the QR.
func TestBeginPairing_QRCarriesTheConfiguredRelayURL(t *testing.T) {
	relayURL := "ws://127.0.0.1:4567"

	stateDir := t.TempDir()
	writeTestIdentity(t, stateDir, "qr-relayurl-host")
	if err := relaycfg.Save(stateDir, relaycfg.Config{RelayURL: relayURL, OperatorNamespace: "owner"}); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadPairingConfig(stateDir)
	if err != nil {
		t.Fatalf("loadPairingConfig with a machine identity + relay.json: %v", err)
	}
	if cfg == nil {
		t.Fatal("loadPairingConfig returned nil despite a present identity + relay.json")
	}
	cfg.NewRendezvous = func(context.Context, [16]byte) (pairing.RendezvousTransport, error) {
		machine, _ := rendezvousPair()
		return machine, nil
	}

	sk := assemble(t)
	sk.api.pairing = cfg

	// Drive one pairing far enough to mint the QR, then abandon it: the cancel unwinds
	// the background handshake goroutine (its confirm gate is never reached).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	confirm := func([]string, string) (bool, error) { return false, nil }
	results := make(chan protocol.PairResult, 1)
	view, err := sk.api.BeginPairing(ctx, protocol.PairStartReq{Capability: "full"}, confirm,
		func(r protocol.PairResult) {
			select {
			case results <- r:
			default:
			}
		})
	if err != nil {
		t.Fatalf("BeginPairing over a configured relay: %v", err)
	}

	qp, err := pairing.DecodeQR(view.QR)
	if err != nil {
		t.Fatalf("pair_start QR is not a decodable pairing QR: %v", err)
	}

	// PB-PAIR-7: the endpoint. Without it the phone has a rendezvous id and a secret and
	// nowhere to dial, so the rendezvous can never be claimed.
	if qp.RelayURL == "" {
		t.Fatalf("decoded pairing QR carries an EMPTY RelayURL (PB-PAIR-7): BeginPairing must "+
			"populate pairing.QRPayload.RelayURL from the configured relay URL %q, which "+
			"loadPairingConfig already reads (loadRelayURL) but currently discards after "+
			"building the rendezvous closure", relayURL)
	}
	if qp.RelayURL != relayURL {
		t.Fatalf("decoded pairing QR RelayURL = %q; want the configured relay URL %q verbatim "+
			"(the machine's own dial target is the one endpoint known reachable, and the one "+
			"PB-PAIR-6 displays to the user before joining)", qp.RelayURL, relayURL)
	}

	// Dialable, not merely non-empty: a phone must be able to parse it into a websocket
	// destination with no out-of-band knowledge.
	u, err := url.Parse(qp.RelayURL)
	if err != nil {
		t.Fatalf("QR RelayURL %q does not parse as a URL: %v", qp.RelayURL, err)
	}
	if u.Scheme != "ws" && u.Scheme != "wss" {
		t.Errorf("QR RelayURL scheme = %q; want a dialable relay websocket scheme (ws or wss)", u.Scheme)
	}
	if u.Host == "" {
		t.Errorf("QR RelayURL %q carries no host; nothing to dial", qp.RelayURL)
	}

	// The other two fields must still be intact — adding the endpoint must not disturb
	// the payload the device leg is driven from.
	if qp.RendezvousID == ([16]byte{}) {
		t.Error("decoded pairing QR carries a zero RendezvousID")
	}
	if qp.PairingSecret == ([32]byte{}) {
		t.Error("decoded pairing QR carries a zero PairingSecret")
	}

	// The abandoned pairing must unwind rather than wedge the daemon behind the new
	// endpoint plumbing.
	cancel()
	select {
	case <-results:
	case <-time.After(15 * time.Second):
		t.Error("abandoned pairing never reported a terminal result after ctx cancel")
	}
}

// TestBeginPairing_RefusesAConfigWithNoRelayEndpoint is slice S3 review finding N3: make
// PB-PAIR-7's precondition ENFORCED rather than coincidental. No path today lets a pairing
// start with no endpoint, but nothing in BeginPairing says so — pairing.EncodeQR encodes
// RelayURL "" perfectly happily (the field is length-prefixed, so empty is well-formed),
// and the QR is minted BEFORE cfg.NewRendezvous is ever called, so the nil-seam guard that
// currently carries the invariant runs too late to be the one enforcing it. A config that
// somehow carries a rendezvous seam without an endpoint must fail CLOSED, not hand the
// operator a QR that leaves the scanning phone with an id, a secret, and nowhere to dial.
func TestBeginPairing_RefusesAConfigWithNoRelayEndpoint(t *testing.T) {
	sk := assemble(t)
	deviceEnds := injectPairing(t, sk)
	sk.api.pairing.RelayURL = "" // a rendezvous seam, but no endpoint to put in the QR

	confirm := func([]string, string) (bool, error) { return true, nil }
	view, err := sk.api.BeginPairing(context.Background(),
		protocol.PairStartReq{Capability: "full"}, confirm, func(protocol.PairResult) {})
	if err == nil {
		qp, decErr := pairing.DecodeQR(view.QR)
		t.Fatalf("BeginPairing with an empty RelayURL returned a QR instead of failing closed "+
			"(decoded RelayURL=%q, decode err=%v); a phone that scans it has a rendezvous id and "+
			"a secret and no address to dial (PB-PAIR-7)", qp.RelayURL, decErr)
	}
	select {
	case <-deviceEnds:
		t.Error("a rendezvous transport was opened despite the refusal; the guard must run " +
			"before any transport work, like the nil-seam and single-device guards beside it")
	default:
	}
}
