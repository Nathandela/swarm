package shim

// Item 1.3 (agents-tracker-445) — concurrent shim serving. The shim must serve
// more than one client connection at a time so that, while a controller holds an
// attach, a daemon's fresh signal/hello connection is still answered promptly
// (R1.3.1/R1.3.2). These are black-box tests over the real shim socket.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/wire"
)

// countHelloReplies counts the hello control frames a client has received.
func countHelloReplies(c *shimClient) int {
	n := 0
	for _, f := range c.frames() {
		if f.typ != wire.TControl {
			continue
		}
		if ctrl, err := shimwire.Decode(f.payload); err == nil && ctrl.Type == shimwire.TypeHello {
			n++
		}
	}
	return n
}

// T1.3.a — with a controller attached (holding a serve slot), a SECOND
// connection's hello is answered promptly. Pre-fix the shim serves connections
// one at a time, so the second hello is never read until the first connection
// closes: this times out (RED). Post-fix the second hello returns in well under
// 500ms (R1.3.1/R1.3.2a).
func TestConcurrent_SecondHelloSucceedsDuringAttach(t *testing.T) {
	cfg := helperConfig(t, modeIdle, nil, nil)
	ch := runShimAsync(cfg)

	// conn1: hello + attach, then HOLD the connection open (never signal, never close).
	c1 := dialShim(t, cfg.SocketPath)
	c1.startReader()
	c1.hello(shimwire.Version)
	c1.attach()
	c1.firstSnapshot(3 * time.Second) // attach established: c1 holds a serve slot

	// conn2: a fresh connection's hello must be answered while c1 is attached.
	c2 := dialShim(t, cfg.SocketPath)
	c2.startReader()
	start := time.Now()
	c2.writeControl(shimwire.Control{Type: shimwire.TypeHello, WireVersion: shimwire.Version})
	reply := c2.waitControl(shimwire.TypeHello, 2*time.Second) // fatals on timeout (RED pre-fix)
	if reply.Type != shimwire.TypeHello {
		t.Fatalf("second-connection reply = %q, want hello", reply.Type)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("second-connection hello took %s while a controller was attached; want < 500ms (R1.3.1)", elapsed)
	}

	// The second connection can drive the shim (kill) even though c1 holds the attach.
	c2.writeControl(shimwire.Control{Type: shimwire.TypeSignal, Sig: shimwire.SigKill})
	waitRun(t, ch, 10*time.Second)
}

// T1.3.c — multiple clients are served concurrently with clean framing, and a
// hostile repeated hello on a connection that also carries the live attach stream
// never corrupts the wire (per-connection write serialization, R1.3.2b/e).
// Pre-fix the second client's hello blocks behind the first client's serve slot
// (RED at setup).
//
// [C3 item B fixture amendment, coordinator-sanctioned] Only client 0 attaches:
// an attach by every client made each successive attach an uncoordinated
// supersede, and the C3 hardening now CLOSES a superseded connection (see
// TestHub_SupersededConnGetsEOF), which would break the later hello-spam on
// those connections for a reason unrelated to what this test pins. Secondary
// clients are hello+spam-only; every assertion is unchanged — concurrent
// serving, framing integrity under spam, and the live attach stream on the
// spammed connection are all still exercised.
func TestConcurrent_MultipleClientsServedWithCleanFraming(t *testing.T) {
	cfg := helperConfig(t, modeStreamActive, nil, nil) // continuous DataOut on the active sub
	ch := runShimAsync(cfg)

	const clients = 3
	cs := make([]*shimClient, clients)
	for i := range cs {
		c := dialShim(t, cfg.SocketPath)
		c.startReader()
		c.hello(shimwire.Version) // pre-fix: for i>=1 this fatals (blocked) -> RED
		if i == 0 {
			c.attach()
			c.firstSnapshot(3 * time.Second)
		}
		cs[i] = c
	}

	// Hostile hello spam on every connection, concurrent with the active attach
	// writer's stream: framing must never desync.
	const spam = 15
	for _, c := range cs {
		for j := 0; j < spam; j++ {
			c.writeControl(shimwire.Control{Type: shimwire.TypeHello, WireVersion: shimwire.Version})
		}
	}
	for i, c := range cs {
		deadline := time.Now().Add(3 * time.Second)
		for {
			c.mu.Lock()
			rerr := c.readErr
			c.mu.Unlock()
			if rerr != nil {
				t.Fatalf("client %d: framing desync / read error: %v", i, rerr)
			}
			if countHelloReplies(c) >= 1+spam {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("client %d: got %d hello replies, want %d (framing desync or connection unserved)",
					i, countHelloReplies(c), 1+spam)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	cs[0].writeControl(shimwire.Control{Type: shimwire.TypeSignal, Sig: shimwire.SigKill})
	waitRun(t, ch, 10*time.Second)
}

// T1.3.e — on shutdown (agent exit) every open connection is closed and Run
// returns without hanging, even with one connection attached, one idle after
// hello, and the kill arriving on a third connection (R1.3.2c). Pre-fix the idle
// and kill connections are never served while the first holds its slot, so the
// kill cannot be delivered and Run never returns (RED via waitRun timeout).
func TestConcurrent_ShutdownClosesAllConns(t *testing.T) {
	cfg := helperConfig(t, modeIdle, nil, nil)
	ch := runShimAsync(cfg)

	// conn1: attached controller (holds a serve slot).
	c1 := dialShim(t, cfg.SocketPath)
	c1.startReader()
	c1.hello(shimwire.Version)
	c1.attach()
	c1.firstSnapshot(3 * time.Second)

	// conn2: idle after hello.
	c2 := dialShim(t, cfg.SocketPath)
	c2.startReader()
	c2.hello(shimwire.Version)

	// conn3: delivers the kill on a fresh connection during the attach.
	c3 := dialShim(t, cfg.SocketPath)
	c3.startReader()
	c3.hello(shimwire.Version)
	c3.writeControl(shimwire.Control{Type: shimwire.TypeSignal, Sig: shimwire.SigKill})

	waitRun(t, ch, 10*time.Second) // Run must return (agent killed via c3)

	// Every connection observes a close after shutdown (shutdown closed them all).
	for i, c := range []*shimClient{c1, c2, c3} {
		deadline := time.Now().Add(3 * time.Second)
		for {
			c.mu.Lock()
			rerr := c.readErr
			c.mu.Unlock()
			if rerr != nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("connection %d was not closed by shutdown (leaked)", i+1)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}
