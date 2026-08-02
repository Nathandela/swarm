package relay

// ADR-007 B82 -- handleRendezvousClaim WRITES ITS REPLY WHILE HOLDING THE GLOBAL Server.mu.
//
// The handler takes `sc.s.mu.Lock()` with `defer sc.s.mu.Unlock()` and then returns
// `sc.replyOK(...)` / `sc.replyErr(...)` INSIDE that scope, so the socket write in
// writeFrame -- whose ceiling is its own 10-second context -- runs under the lock every
// other connection's every op contends for (meterOp alone takes it at the top of each one).
// A claimer that stops draining its socket therefore freezes the WHOLE relay, from an
// UNAUTHENTICATED connection: rendezvous_claim carries no requireAuth.
//
// handleRendezvousCreate, ...Send, ...Recv and ...Complete all unlock BEFORE they reply.
// Claim was the only one that did not, and it predates round 4 (git show 8861488^), so this
// is not a composition of the round-4 remediation -- it is a defect that remediation walked
// past twice.
//
// WHAT THIS TEST HAD TO GET RIGHT, because "claim still works" proves nothing: the defect is
// what else is blocked WHILE it works. So the property is
//
//	an unrelated connection's op completes while a claimer's reply write is held mid-flight
//
// and the stall must be DETERMINISTIC, not a race the machine's load decides. It is: the
// claimer's socket is served through a listener whose accepted conns run every Write past a
// gate, and the gate holds exactly ONE write on an unbuffered channel until this test
// releases it. Nothing sleeps, nothing polls for a buffer to fill, and no production code
// carries a test seam -- the relay's own handleHTTP is served on a second listener, which is
// the whole of the injection.
//
// The 2s budget on the probe is not the property; it is the difference between "returned in
// microseconds" and "blocked until the gate opens, which this test never lets happen while
// the assertion is live".

import (
	"context"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// writeGate holds exactly one socket write. armOne arms it; the next Write through a
// gatedConn parks until releaseAll, having announced itself on entered.
type writeGate struct {
	armed     atomic.Bool
	entered   chan struct{}
	release   chan struct{}
	releaseOn sync.Once
}

func newWriteGate() *writeGate {
	return &writeGate{entered: make(chan struct{}, 1), release: make(chan struct{})}
}

func (g *writeGate) armOne() { g.armed.Store(true) }

// hold parks the FIRST write after arming. CompareAndSwap rather than a plain load+store so
// two conns writing at once cannot both be held -- exactly one write is the subject.
func (g *writeGate) hold() {
	if !g.armed.CompareAndSwap(true, false) {
		return
	}
	select {
	case g.entered <- struct{}{}:
	default:
	}
	<-g.release
}

func (g *writeGate) releaseAll() { g.releaseOn.Do(func() { close(g.release) }) }

type gatedConn struct {
	net.Conn
	g *writeGate
}

func (c *gatedConn) Write(b []byte) (int, error) {
	c.g.hold()
	return c.Conn.Write(b)
}

type gatedListener struct {
	net.Listener
	g *writeGate
}

func (l *gatedListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &gatedConn{Conn: c, g: l.g}, nil
}

// serveGated exposes the SAME running relay on a second listener whose socket writes this
// test can hold. srv.Start has already installed baseCtx, which serveConn needs; the relay
// is otherwise untouched, and connections accepted here are ordinary members of s.conns.
func serveGated(t *testing.T, s *Server) (string, *writeGate) {
	t.Helper()
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("gated listener: %v", err)
	}
	g := newWriteGate()
	hs := &http.Server{Handler: http.HandlerFunc(s.handleHTTP)}
	go func() { _ = hs.Serve(&gatedListener{Listener: base, g: g}) }()
	t.Cleanup(func() { _ = hs.Close() })
	return "ws://" + base.Addr().String(), g
}

func TestB82_AStalledClaimReplyDoesNotFreezeEveryOtherConnection(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)
	gatedURL, gate := serveGated(t, srv)
	// Registered after serveGated's, so it runs BEFORE it: the held write is released
	// before anything closes the socket under it, on every exit path including t.Fatal.
	t.Cleanup(gate.releaseAll)

	// The machine's side of a real pairing: a live rendezvous for the phone to claim, so
	// the write this test holds is the one a LEGITIMATE claim produces (replyOK), not a
	// refusal's.
	creator := dialRaw(t, srv.URL())
	const label = "b82-live-rendezvous"
	if err := creator.RendezvousCreate(testCtx(t), label); err != nil {
		t.Fatalf("rendezvous_create (machine leg): %v", err)
	}

	// The probe connection is established BEFORE the gate is armed, so what is measured is
	// one op, not the accept path (serveConn takes s.mu for admission control too).
	probe := dialRaw(t, srv.URL())

	claimer, err := DialRaw(testCtx(t), gatedURL)
	if err != nil {
		t.Fatalf("DialRaw(gated): %v", err)
	}
	t.Cleanup(func() { _ = claimer.CloseNow() })

	gate.armOne()
	claimDone := make(chan error, 1)
	go func() { claimDone <- claimer.RendezvousClaim(context.Background(), label) }()

	select {
	case <-gate.entered:
	case err := <-claimDone:
		t.Fatalf("the claim answered (err=%v) without its reply write ever being held; the gate "+
			"is not on the reply path and this test would be measuring nothing", err)
	case <-time.After(testDeadline):
		t.Fatalf("no gated write within %s: the claim's reply never reached the socket", testDeadline)
	}

	// THE PROPERTY. One claimer's reply is in flight and going nowhere. Every other
	// connection must be unaffected -- this one is unauthenticated and shares nothing with
	// the claimer but the relay itself.
	probeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	probeErr := probe.RendezvousCreate(probeCtx, "b82-unrelated-rendezvous")

	gate.releaseAll()

	if probeErr != nil {
		t.Fatalf("an unrelated connection's rendezvous_create failed (%v) while ONE claimer's reply "+
			"write was held mid-flight.\n"+
			"  handleRendezvousClaim holds the GLOBAL Server.mu across its reply -- defer\n"+
			"  sc.s.mu.Unlock() with return sc.replyOK(...) inside -- so writeFrame's socket write runs\n"+
			"  under the lock meterOp takes at the top of every op on every connection. A claimer that\n"+
			"  does not drain its socket freezes the whole relay for writeFrame's 10s ceiling, and\n"+
			"  rendezvous_claim needs no authentication. create/send/recv/complete all unlock first.",
			probeErr)
	}
	if err := <-claimDone; err != nil {
		t.Fatalf("the claim itself failed once its write was released: %v", err)
	}
}
