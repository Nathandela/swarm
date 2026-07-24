package shim

// C3-committee item A: the snapshot-only, non-subscribing request. The grid tap
// previously sampled by ATTACHING, which supersedes the hub's single subscriber;
// the daemon-side IsControlled skip is a TOCTOU check (the async sample can land
// after a controller attaches, stealing its stream). snapshot_req removes the
// class: the shim answers with a one-shot snapshot on the requesting connection
// and NEVER touches h.sub, so a tap can never supersede a controller no matter
// how the timing falls. The capability is advertised in the shim's hello reply
// (SnapshotOnly, optional field, WireVersion stays 1) so an old daemon simply
// never sends the request and a new daemon falls back to attach-based sampling
// against an old shim (G-D).
//
// Failing-first: pre-implementation the shim ignored snapshot_req (unknown
// control type), so the requester timed out waiting for its snapshot, and the
// hello reply did not advertise SnapshotOnly.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/shimwire"
)

// TestHello_AdvertisesSnapshotOnly pins the capability advertisement.
func TestHello_AdvertisesSnapshotOnly(t *testing.T) {
	cfg := helperConfig(t, modeIdle, nil, nil)
	ch := runShimAsync(cfg)

	c := dialShim(t, cfg.SocketPath)
	c.startReader()
	reply := c.hello(shimwire.Version)
	if !reply.SnapshotOnly {
		t.Fatal("hello reply did not advertise snapshot_only")
	}

	c.writeControl(shimwire.Control{Type: shimwire.TypeSignal, Sig: shimwire.SigKill})
	waitRun(t, ch, 10*time.Second)
}

// TestSnapshotReq_DoesNotSupersedeActiveAttach is the race pin: with a live
// controller attached and streaming, a snapshot_req on a SECOND connection must
// deliver a snapshot to the requester while the controller keeps receiving live
// frames — the sample never steals the subscriber, by construction rather than
// by timing.
func TestSnapshotReq_DoesNotSupersedeActiveAttach(t *testing.T) {
	cfg := helperConfig(t, modeStreamActive, nil, nil)
	ch := runShimAsync(cfg)

	// Controller: attach and confirm the live stream is flowing.
	ctl := dialShim(t, cfg.SocketPath)
	ctl.startReader()
	ctl.hello(shimwire.Version)
	ctl.attach()
	ctl.firstSnapshot(3 * time.Second)
	ctl.waitOutput("1", 3*time.Second)

	// Tap: a second connection issues snapshot_req and must get a snapshot
	// WITHOUT attaching.
	tap := dialShim(t, cfg.SocketPath)
	tap.startReader()
	tap.hello(shimwire.Version)
	tap.writeControl(shimwire.Control{Type: shimwire.TypeSnapshotReq})
	if snap := tap.firstSnapshot(3 * time.Second); len(snap) == 0 {
		t.Fatal("snapshot_req returned an empty snapshot")
	}

	// The controller's stream must CONTINUE after the tap's snapshot: capture
	// the current accumulated output length, then require growth.
	before := len(ctl.dataOut())
	deadline := time.Now().Add(3 * time.Second)
	for {
		if len(ctl.dataOut()) > before {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("controller stopped receiving live frames after a snapshot_req: the tap superseded the subscriber")
		}
		time.Sleep(5 * time.Millisecond)
	}

	ctl.writeControl(shimwire.Control{Type: shimwire.TypeSignal, Sig: shimwire.SigKill})
	waitRun(t, ch, 10*time.Second)
}
