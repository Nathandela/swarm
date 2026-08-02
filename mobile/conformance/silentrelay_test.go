package conformance_test

// FAILING-FIRST (TDD RED, GG-5) for the round-7 blocker, at the only place it can ship
// broken: the FACADE, against a real link, with the relay doing the one thing it costs the
// relay nothing to do -- nothing at all.
//
// THE DEFECT. relay.Client has no timeout of any kind, and every shipped phone call site
// passes context.Background() (mobile/commands.go input/resize, every signed command and every
// unsigned read; mobile/relay.go's drain; mobile/app.go's Presence and the two token verbs).
// roundtrip holds c.mu across write-then-read, and the phone's own a.bucketMu is held across
// the whole allocate -> seal -> append. So a relay that answers nothing parks the inbound
// drain holding the connection's exchange lock, and every keystroke, take_control, launch and
// kill queues behind it forever -- with no error, no state change, and ConnectionState still
// reporting "online". The only recovery is restarting the app.
//
// IT IS ALSO THE ORDINARY BENIGN CASE. A half-open TCP after a WiFi -> cellular handoff
// presents to the handset exactly as this proxy does.
//
// WHY THE ASSERTIONS ARE WHAT THEY ARE. A bound that merely returns an error nothing surfaces
// is the same wedge with extra steps, so each half is asserted:
//   - the call RETURNS, bounded, and returns an ERROR (not a success for a frame nobody
//     acknowledged),
//   - the error carries a class the screen can act on -- ErrClassOffline, whose remedy is to
//     wait, and NOT ErrClassInternal, whose remedy is "report a bug",
//   - and the TRANSPORT STATE stops claiming to be online, because a phone that says "online"
//     while nothing it sends is answered is lying to the one person who could act on it.
//
// This file contains NO implementation.

import (
	"testing"
	"time"

	swarmmobile "github.com/Nathandela/swarm/mobile"
)

// silentBound is how long this test waits for a call that must be bounded. It is well above
// the bound itself -- §6.0's 10 s non-wait request timeout, which relay.DefaultCallTimeout now
// carries -- and well below "forever": a call still parked here is parked because nothing
// bounds it.
//
// It is generous enough to absorb TWO of those deadlines, which is a real cost of the fix
// rather than a flake allowance: a.bucketMu is a plain mutex, so a keystroke issued behind a
// command that is itself waiting out its deadline pays that deadline first.
const silentBound = 40 * time.Second

// awaitWithin polls fn until it reports true, or fails after d. The harness's own eventually
// is fixed at five seconds, which is shorter than the bound under test here.
func awaitWithin(t *testing.T, d time.Duration, msg string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out after %v: %s", d, msg)
}

// requireOffline asserts an error is present and classed as the transport condition it is.
func requireOffline(t *testing.T, app *swarmmobile.App, verb string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s returned nil against a relay that answered nothing. The phone was told the "+
			"frame was delivered when no reply was ever received", verb)
	}
	class, cerr := app.ErrorClass(err.Error())
	if cerr != nil {
		t.Fatalf("App.ErrorClass: %v", cerr)
	}
	if class != swarmmobile.ErrClassOffline {
		t.Errorf("%s failed with class %q (%v), want %q.\n"+
			"A relay that will not answer is a TRANSPORT condition: the app is healthy, the link "+
			"is not, and waiting is the honest remedy. Any other class sends the user somewhere "+
			"that cannot help -- %q's remedy is to report a bug.",
			verb, class, err, swarmmobile.ErrClassOffline, swarmmobile.ErrClassInternal)
	}
}

// TestSilentRelay_TheOutboundPlaneIsNeverParkedForever drives the two verbs the blocker names
// -- a signed command (kill) and live input -- against a relay that has gone silent underneath
// a healthy, reconciled, lease-holding phone.
func TestSilentRelay_TheOutboundPlaneIsNeverParkedForever(t *testing.T) {
	h, proxy := s11rProxiedHarness(t)

	// PREMISE: the plane works before the silence, so a hang afterwards is caused by it.
	if err := h.App.Paste(testSession, "premise\r"); err != nil {
		t.Fatalf("Paste before the relay went silent: %v", err)
	}
	h.AwaitInput("data")

	proxy.Silence()

	// A SIGNED COMMAND. kill is the one in the blocker for a reason: it is the verb a user
	// reaches for when something has gone wrong, and it is held behind a.bucketMu.
	killed := make(chan error, 1)
	go func() {
		_, err := h.App.Kill(testSession)
		killed <- err
	}()

	var killErr error
	select {
	case killErr = <-killed:
	case <-time.After(silentBound):
		t.Fatalf("App.Kill was STILL PARKED after %v against a relay that answered nothing.\n"+
			"sealSignedCommand appends with context.Background() while holding a.bucketMu, and "+
			"relay.Client has no timeout, so the relay wedges every command the phone can issue "+
			"by doing nothing at all -- including the one the user reaches for when something is "+
			"wrong. Recovery today is restarting the app.", silentBound)
	}
	requireOffline(t, h.App, "App.Kill", killErr)

	// LIVE INPUT, on the same wedged plane. Paste rather than SendInput because a keystroke may
	// legitimately be held for the 125 ms coalescing window and return nil without appending
	// anything, which would make this assertion pass for the wrong reason; a paste is one event
	// and is appended before the call returns (PB-INPUT-6).
	pasted := make(chan error, 1)
	go func() { pasted <- h.App.Paste(testSession, "into the silence\r") }()

	var pasteErr error
	select {
	case pasteErr = <-pasted:
	case <-time.After(silentBound):
		t.Fatalf("App.Paste was STILL PARKED after %v against a silent relay; live input is "+
			"resolved against the connection as it stands (ADR-007 D7) and must fail, not hang",
			silentBound)
	}
	requireOffline(t, h.App, "App.Paste", pasteErr)

	// AND THE FAILURE IS RECORDED, not just returned. PB-INPUT-1 resolves input the phone
	// accepted from the user and could not deliver as an explicit undelivered entry: a screen
	// that opens after the failure must still be able to say what did not reach the machine.
	led, lerr := h.App.UndeliveredInputs()
	if lerr != nil {
		t.Fatalf("UndeliveredInputs: %v", lerr)
	}
	n, lerr := led.Count()
	if lerr != nil {
		t.Fatalf("UndeliveredList.Count: %v", lerr)
	}
	if n == 0 {
		t.Error("nothing on the undelivered ledger after a paste the relay never acknowledged; " +
			"PB-INPUT-1 forbids resolving it as a silent drop")
	}

	// AND THE COMMAND IS NOT LEFT IN FLIGHT. A kill that never reached the machine must not
	// raise PendingOpCount for the life of the process -- that hides every genuinely pending
	// op behind one that can never resolve, which is the wedge moved into the op tracker.
	pending, perr := h.App.PendingOpCount()
	if perr != nil {
		t.Fatalf("PendingOpCount: %v", perr)
	}
	if pending != 0 {
		t.Errorf("PendingOpCount = %d after a kill whose append failed, want 0: an op issued for "+
			"a frame that was never delivered can never be answered", pending)
	}
}

// TestSilentRelay_TheConnectionStopsClaimingToBeOnline is the state half, and it is the one a
// user actually sees.
//
// The inbound drain sits in MailboxRead under the Start..Stop context, which carries no
// deadline and is cancelled only by Stop or Close. Against a silent relay that call never
// returns, so drain never returns, so run() never severs the lease, never drops the client and
// never reconnects. ConnectionState goes on answering "online" forever while nothing the phone
// sends is answered -- a spinner would at least be honest.
func TestSilentRelay_TheConnectionStopsClaimingToBeOnline(t *testing.T) {
	h, proxy := s11rProxiedHarness(t)

	if s, err := h.App.ConnectionState(); err != nil || s != "online" {
		t.Fatalf("premise: ConnectionState = %q, %v; want online", s, err)
	}

	proxy.Silence()

	awaitWithin(t, silentBound, "ConnectionState still reported \"online\" against a relay that "+
		"answered nothing.\nThe drain is parked in MailboxRead on context.Background(), so the "+
		"phone never observes the outage: it cannot sever the lease (PB-INPUT-2), cannot drop "+
		"the client and cannot reconnect. The user is shown a healthy link and told nothing.",
		func() bool {
			s, err := h.App.ConnectionState()
			return err == nil && s != "online"
		})
}
