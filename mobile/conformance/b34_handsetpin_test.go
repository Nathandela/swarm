// ADR-007 B34, handset half (FAILING FIRST): the relay pin the phone pinned at pairing is
// APPLIED on the dial the phone actually makes.
//
// The pin is carried (pairing.MachinePayload.RelaySPKIPin), decoded at the machine boundary
// and persisted (phonecore.State.RelaySPKIPin, schema v7). The consent-signature slice
// deliberately stopped there and said why: "a pin that is carried, persisted, and never
// consulted is precisely B34's 'a fence guarding a path production does not take'." This file
// is the fence that makes consulting it non-optional.
//
// IT DRIVES THE REAL App, not relay.DialSecure, for the reason that produced B34 in the first
// place: a pin proved against the secure helper says nothing about the path a handset takes.
package conformance_test

import (
	"crypto/sha256"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
)

// tlsFrontedRelay puts a TLS terminator in front of the harness's ws:// relay -- the topology
// docs/operations/relay-runbook.md prescribes, and the only one in which a pin means anything.
// It returns the wss:// URL and SHA-256 over the terminator's SubjectPublicKeyInfo.
func tlsFrontedRelay(t *testing.T, wsURL string) (wss string, spki []byte) {
	t.Helper()
	target, err := url.Parse(strings.Replace(wsURL, "ws://", "http://", 1))
	if err != nil {
		t.Fatalf("parse relay url: %v", err)
	}
	front := httptest.NewTLSServer(httputil.NewSingleHostReverseProxy(target))
	t.Cleanup(front.Close)
	sum := sha256.Sum256(front.Certificate().RawSubjectPublicKeyInfo)
	return strings.Replace(front.URL, "https://", "wss://", 1), sum[:]
}

// persistRelayPin writes the pin into the phone's durable state, which is what a completed
// pairing does (mobile/pairing.go pin()). It is written directly here so the test is about the
// DIAL rather than about re-running a handshake S16 already covers.
func persistRelayPin(t *testing.T, h *harness, pin []byte) {
	t.Helper()
	store, err := phonecore.OpenStore(filepath.Join(h.Dir, phonecore.StateFileName), h.Machine,
		h.Custody.wakeSealer(), h.Custody.contentSealer())
	if err != nil {
		t.Fatalf("open phone state: %v", err)
	}
	st := store.Load()
	st.RelaySPKIPin = pin
	if err := store.Save(st); err != nil {
		t.Fatalf("save phone state: %v", err)
	}
}

// TestPBOPS5_TheHandsetAppliesThePinItPinnedAtPairing is the pair of assertions that make the
// persisted pin load-bearing: the MATCHING pin connects, and a valid pin for a different key
// does not. The matching half runs first, so the refusal below cannot be a rig that could
// never have connected in the first place.
func TestPBOPS5_TheHandsetAppliesThePinItPinnedAtPairing(t *testing.T) {
	h := newHarness(t)
	wss, spki := tlsFrontedRelay(t, h.RelayURL)

	// ---- control: the pin the phone holds is the relay's own -------------
	if err := h.App.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	persistRelayPin(t, h, spki)
	h.AppRelayURL = wss
	h.App = h.openApp()
	eventually(t, "the phone never came online through a relay whose pin it holds", func() bool {
		st, err := h.App.ConnectionState()
		return err == nil && st == "online"
	})

	// ---- the fence: a valid pin for a DIFFERENT key ----------------------
	if err := h.App.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	wrong := sha256.Sum256([]byte("some other relay's public key"))
	persistRelayPin(t, h, wrong[:])
	h.App = h.openApp()

	// Long enough for several dial attempts at App.run's 250ms backoff.
	time.Sleep(2 * time.Second)
	st, err := h.App.ConnectionState()
	if err != nil {
		t.Fatalf("ConnectionState: %v", err)
	}
	if st == "online" {
		t.Fatalf("the phone came online against a relay that does not match the pin it pinned "+
			"at pairing (state %q). A pin that is carried, persisted and never consulted is the "+
			"defect ADR-007 B34 exists to record", st)
	}
	// AND IT SAYS SO. A pin mismatch is not a link that can come back, so reporting it as
	// "reconnecting" is a spinner promising that waiting is enough -- the fourth instance of
	// the defect App.run's own switch was written to stop.
	if st != "relay_untrusted" {
		t.Fatalf("ConnectionState = %q against a relay whose key the phone never pinned; want "+
			"%q. %q leaves the user watching a spinner for a certificate that is never going to "+
			"start matching", st, "relay_untrusted", st)
	}
}

// TestPBAPP10_ACleartextRelayIsReportedAsTheMachinesMisconfiguration is the second of the two
// transport-policy verdicts, and it is a DIFFERENT one on purpose: nothing on the handset can
// fix a machine whose relay.json names ws://, and pairing again re-delivers the same URL, so a
// user told to re-pair would go round a loop.
func TestPBAPP10_ACleartextRelayIsReportedAsTheMachinesMisconfiguration(t *testing.T) {
	h := newHarness(t)
	if err := h.App.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// A NAME, not a loopback literal: the carve-out that lets every other test in this
	// package use ws://127.0.0.1 resolves nothing, so this is refused exactly as a routable
	// cleartext relay would be on a handset.
	h.AppRelayURL = "ws://localhost:9/"
	h.App = h.openApp()

	time.Sleep(2 * time.Second)
	st, err := h.App.ConnectionState()
	if err != nil {
		t.Fatalf("ConnectionState: %v", err)
	}
	if st != "relay_insecure" {
		t.Fatalf("ConnectionState = %q against a cleartext relay; want %q. Reporting the "+
			"machine's configuration as a lost link sends the user to wait for something no "+
			"amount of waiting fixes", st, "relay_insecure")
	}
}

// TestPBOPS5_APhoneWithNoPinIsUnchanged is the "optional" half, and it is what keeps every
// other conformance test in this package working: the harness's phones hold no pin and dial a
// loopback ws:// relay, which is the carve-out a test binary is allowed.
//
// It is ALSO the case whose real-handset behaviour is NOT visible here, and the difference is
// the platform. relay.TrustRootSourceFor makes Android pinning-only, so on a handset this same
// unpinned state refuses a wss:// dial with ErrPinRequired; on the darwin host this test runs
// on it verifies against the system roots instead. The refusal is asserted where it is decided
// -- internal/remote/transport/tls_test.go -- and named here so nobody reads this pass as
// evidence that an unpinned handset connects.
func TestPBOPS5_APhoneWithNoPinIsUnchanged(t *testing.T) {
	h := newHarness(t)
	eventually(t, "a phone holding no pin could not reach the loopback relay", func() bool {
		st, err := h.App.ConnectionState()
		return err == nil && st == "online"
	})
}
