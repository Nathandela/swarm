// ADR-007 B57/B58 (FAILING FIRST): a transport verdict reached WHILE A PAIRING IS IN FLIGHT
// must not be terminal, and must still be terminal outside one.
//
// THE DEFECT. relay.ErrPinMismatch / ErrPinRequired set connRelayUntrusted, which is terminal:
// App.run returns, and only Start and rearmAfterPairing ever launch it again. But the remedy
// this verdict tells the user to perform IS a pairing -- so a verdict reached during one ends
// the loop that the pairing was about to fix. On a FIRST pairing that is the ordinary path and
// not an edge: a handset holds no pin until msg2 delivers one, an unpinned dial on a
// pinning-only platform is refused before a packet, and the loop is failing exactly that way on
// every retry for the whole time the user spends comparing SAS symbols.
//
// rearmAfterPairing does not cover it. It runs at the END of pin(), so its grace window is
// still closed during this window; and it polls the dead generation's channel ONCE and
// non-blockingly, so a loop that dies between that poll and its own deferred close is never
// restarted by anything.
//
// BOTH DIRECTIONS ARE ASSERTED. "Never terminal" would satisfy the survival half on its own and
// is the over-correction to avoid: outside a pairing this verdict must still stop the loop, or
// the phone spends a battery re-proving a pin that is never going to start matching, against
// the relay's per-source budget.
//
// THE OBSERVABLE IS DIAL ATTEMPTS, not the connection state. The state reads relay_untrusted in
// both cases -- that is the point of it -- so it cannot distinguish a loop still retrying from
// one that has stopped. A counting TLS front measures what the phone actually does.
package conformance_test

import (
	"crypto/sha256"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	swarmmobile "github.com/Nathandela/swarm/mobile"
)

// pinTap counts TCP connections reaching the relay's TLS front. A retrying transport loop keeps
// arriving; a terminal one stops.
type pinTap struct {
	srv *httptest.Server

	mu sync.Mutex
	n  int
}

// newPinTap fronts a ws:// relay with TLS and counts every accepted connection. The pin it
// returns is the terminator's own, so a phone holding any other value is refused by the
// transport policy rather than by the relay.
func newPinTap(t *testing.T, wsURL string) (tap *pinTap, wss string, spki []byte) {
	t.Helper()
	target, err := url.Parse(strings.Replace(wsURL, "ws://", "http://", 1))
	if err != nil {
		t.Fatalf("parse relay url: %v", err)
	}
	tap = &pinTap{}
	tap.srv = httptest.NewUnstartedServer(httputil.NewSingleHostReverseProxy(target))
	tap.srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state != http.StateNew {
			return
		}
		tap.mu.Lock()
		tap.n++
		tap.mu.Unlock()
	}
	tap.srv.StartTLS()
	t.Cleanup(tap.srv.Close)
	sum := sha256.Sum256(tap.srv.Certificate().RawSubjectPublicKeyInfo)
	return tap, strings.Replace(tap.srv.URL, "https://", "wss://", 1), sum[:]
}

// dialsOver reports how many fresh connections arrived during d.
func (p *pinTap) dialsOver(d time.Duration) int {
	p.mu.Lock()
	before := p.n
	p.mu.Unlock()
	time.Sleep(d)
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.n - before
}

// seedPinInto writes a relay pin into a state directory before any App opens it, which is the
// state a handset restored from a previous pairing comes back in.
func seedPinInto(t *testing.T, dir string, custody *testCustody, pin []byte) {
	t.Helper()
	store, err := phonecore.OpenStore(filepath.Join(dir, phonecore.StateFileName), testMachineID,
		custody.wakeSealer(), custody.contentSealer())
	if err != nil {
		t.Fatalf("open phone state: %v", err)
	}
	st := store.Load()
	st.RelaySPKIPin = pin
	if err := store.Save(st); err != nil {
		t.Fatalf("save phone state: %v", err)
	}
}

// TestB58_APinVerdictStopsTheLoopOutsideAPairing is the half that keeps the fix honest. It runs
// as its own test rather than as a preamble so that a change making the verdict never terminal
// fails HERE, by name, instead of quietly widening the other half.
func TestB58_APinVerdictStopsTheLoopOutsideAPairing(t *testing.T) {
	_, relayURL := s16FreshRelay(t)
	tap, wss, _ := newPinTap(t, relayURL)

	dir, custody := t.TempDir(), newTestCustody(t)
	wrong := sha256.Sum256([]byte("a relay this phone will never reach"))
	seedPinInto(t, dir, custody, wrong[:])
	app := s16UnpairedApp(t, dir, wss, custody)

	eventually(t, "the phone never reported relay_untrusted against a relay whose key it did "+
		"not pin", func() bool {
		st, err := app.ConnectionState()
		return err == nil && st == "relay_untrusted"
	})

	// Let a dial already in flight finish, then measure a clean window.
	time.Sleep(500 * time.Millisecond)
	if n := tap.dialsOver(2 * time.Second); n > 0 {
		t.Fatalf("the phone made %d further dial(s) in 2s after a pin verdict with NO pairing "+
			"running. This verdict must stop the loop: a pin that does not match is not going to "+
			"start matching, and every retry is a handshake spent on a battery against the "+
			"relay's per-source budget", n)
	}
}

// TestB58_APinVerdictDoesNotStopTheLoopDuringAPairing is ADR-007 B57 itself.
//
// The pairing is held at the SAS gate -- where a real one waits on a person comparing six
// symbols, which is precisely when this window is widest -- and the assertion is that the
// transport is still trying while it waits. Then the pairing completes with the relay's real
// pin and the phone must reach the relay, which a loop that had ended could not do.
func TestB58_APinVerdictDoesNotStopTheLoopDuringAPairing(t *testing.T) {
	_, relayURL := s16FreshRelay(t)
	tap, wss, spki := newPinTap(t, relayURL)

	dir, custody := t.TempDir(), newTestCustody(t)
	wrong := sha256.Sum256([]byte("a relay this phone will never reach"))
	seedPinInto(t, dir, custody, wrong[:])

	// THE PAIRING STARTS BEFORE THE TRANSPORT, so the verdict is reached DURING it. That is the
	// ordering B57 is about, and it is not contrived: BeginPairing needs only the core, so a
	// handset can be at the SAS gate when the surface calls App.Start -- on resume, or on a
	// rotation. Reaching the verdict first instead would test rearmAfterPairing's recovery path,
	// which is a different question and already has an answer.
	app, err := swarmmobile.NewApp(&swarmmobile.Config{
		StateDir: dir, RelayURL: wss, MachineID: testMachineID,
	}, custody)
	if err != nil {
		t.Fatalf("swarmmobile.NewApp: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })

	// The machine publishes the pin that actually matches this relay, which is what makes the
	// verdict stale -- exactly the recovery the verdict's own remedy tells the user to perform.
	machine := newB58Machine(t, relayURL, wss)
	p := s16BeginConfirmed(t, app, machine.offer(t, spki))
	s16AwaitSAS(t, p)

	if err := app.Start(); err != nil {
		t.Fatalf("App.Start: %v", err)
	}
	eventually(t, "the phone never reported relay_untrusted; the rig never reached the verdict "+
		"this test is about", func() bool {
		st, err := app.ConnectionState()
		return err == nil && st == "relay_untrusted"
	})

	// IN FLIGHT, waiting on the user. The loop must still be trying.
	if n := tap.dialsOver(2 * time.Second); n == 0 {
		t.Fatalf("the phone made NO dial attempt in 2s while a pairing was at the SAS gate. " +
			"The transport loop ended on a verdict whose remedy is the pairing that is running: " +
			"on a first pairing this is the ordinary path, because a handset holds no pin until " +
			"msg2 delivers one and every dial until then is refused (ADR-007 B57)")
	}

	if err := p.Confirm(); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	s16AwaitState(t, p, "paired")

	eventually(t, "the phone never came online after a pairing delivered the relay's real pin; "+
		"the loop the verdict ended was never restarted -- rearmAfterPairing polls the dead "+
		"generation once and non-blockingly, so it loses that race", func() bool {
		st, err := app.ConnectionState()
		return err == nil && st == "online"
	})
}

// newB58Machine is b54Machine with the phone's own relay URL carried separately: the phone
// dials the TLS front, and the machine leg speaks to the plain relay behind it.
func newB58Machine(t *testing.T, machineRelayURL, phoneRelayURL string) *b58Machine {
	t.Helper()
	return &b58Machine{inner: newB54Machine(t, machineRelayURL), phoneURL: phoneRelayURL}
}

type b58Machine struct {
	inner    *b54Machine
	phoneURL string
}

// offer mints a QR naming the URL the PHONE must dial, over a rendezvous the machine created on
// the relay behind the terminator.
func (m *b58Machine) offer(t *testing.T, pin []byte) string {
	t.Helper()
	return m.inner.offerAt(t, pin, m.phoneURL)
}

// TestB58_TheFirstPairingSurvivesAPinningOnlyPlatform is the ORDINARY path, fenced directly
// instead of by proxy.
//
// The two tests above drive ErrPinMismatch -- a phone holding the WRONG pin -- because that is
// what a desktop can produce. The case that actually bites a handset is ErrPinRequired: a phone
// holding NO pin at all, which is every phone until msg2 delivers one, refused before a packet
// because relay.TrustRootSourceFor makes Android pinning-only. On this host the identical dial
// verifies against the system roots and fails with a generic x509 error that falls through to
// `continue` and never reaches the verdict at all -- so without stating the platform, the
// ordinary first pairing is fenced only by a different error reached a different way.
//
// The override is FORWARDED to relay.WithTrustRootSource, which honours it only inside a test
// binary and whose inertness in a release build is proven by a non-test binary in
// internal/remote/transport. Nothing here re-decides that rule.
func TestB58_TheFirstPairingSurvivesAPinningOnlyPlatform(t *testing.T) {
	t.Setenv("SWARM_TEST_TRUST_ROOTS", "pinned")

	_, relayURL := s16FreshRelay(t)
	_, wss, spki := newPinTap(t, relayURL)

	// NO PIN SEEDED. This is a fresh install, which is the whole point: the phone cannot hold a
	// pin before the pairing that delivers it.
	dir, custody := t.TempDir(), newTestCustody(t)
	app, err := swarmmobile.NewApp(&swarmmobile.Config{
		StateDir: dir, RelayURL: wss, MachineID: testMachineID,
	}, custody)
	if err != nil {
		t.Fatalf("swarmmobile.NewApp: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })

	seen := &connWatch{}
	if err := app.SetEventListener(seen); err != nil {
		t.Fatalf("SetEventListener: %v", err)
	}

	machine := newB58Machine(t, relayURL, wss)
	p := s16BeginConfirmed(t, app, machine.offer(t, spki))
	s16AwaitSAS(t, p)

	if err := app.Start(); err != nil {
		t.Fatalf("App.Start: %v", err)
	}
	eventually(t, "a phone with no pin on a pinning-only platform never reported "+
		"relay_untrusted; the platform override did not reach the dial, so this test is not "+
		"exercising ErrPinRequired at all", func() bool {
		st, err := app.ConnectionState()
		return err == nil && st == "relay_untrusted"
	})

	// THE DISCRIMINATOR IS THE `connecting` EVENT, and choosing it is the whole test.
	//
	// Two observables that suggest themselves both fail here. A dial count is blind: an
	// ErrPinRequired refusal is decided from the policy BEFORE a socket opens, so nothing
	// reaches the tap whether the loop lives or dies. And "does it come online" is VACUOUS --
	// measured, not assumed: with the guard removed this test passed three times out of three,
	// because rearmAfterPairing restarts the dead loop and wins its race on an idle machine.
	//
	// What separates the two worlds is HOW it comes back. A surviving loop is mid-generation,
	// so its next iteration goes straight to online. A loop that DIED and was rearmed starts a
	// fresh generation, whose first act is setConn(connConnecting). So a `connecting` event
	// after the verdict is proof the loop ended -- exactly what must not happen during a
	// pairing, and exactly what rearm papers over afterwards.
	if err := p.Confirm(); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	s16AwaitState(t, p, "paired")

	eventually(t, "the phone never came online after its FIRST pairing delivered the relay pin "+
		"on a pinning-only platform", func() bool {
		st, err := app.ConnectionState()
		return err == nil && st == "online"
	})

	if n := seen.connectingAfterVerdict(); n > 0 {
		t.Fatalf("the transport loop reported `connecting` %d time(s) after the pin verdict, "+
			"which only a FRESH generation does -- so the loop ended during the pairing and "+
			"rearmAfterPairing restarted it. That rearm is a race it can lose: it polls the dead "+
			"generation's channel once, non-blockingly, and nothing else ever relaunches the "+
			"loop. On a first pairing this is the ordinary path (ADR-007 B57)", n)
	}
}

// connWatch records the connection states the facade reports, so a test can tell a loop that
// SURVIVED from one that died and was restarted underneath it.
type connWatch struct {
	mu     sync.Mutex
	states []string
}

func (w *connWatch) OnEvent(e *swarmmobile.Event) {
	if e == nil || e.Kind != "connection" {
		return
	}
	w.mu.Lock()
	w.states = append(w.states, e.State)
	w.mu.Unlock()
}

// connectingAfterVerdict counts `connecting` reports that follow the first pin verdict. A
// generation already running never emits one; only a newly launched one does.
func (w *connWatch) connectingAfterVerdict() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, seenVerdict := 0, false
	for _, st := range w.states {
		switch {
		case st == "relay_untrusted":
			seenVerdict = true
		case seenVerdict && st == "connecting":
			n++
		}
	}
	return n
}
