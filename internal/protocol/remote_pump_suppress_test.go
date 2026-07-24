package protocol

// FAILING-FIRST test for A7 Slice F3 (remote-tier pump-suppression). A REMOTE-tier
// controller (the phone, via take_control) must receive its OpLease grant and input
// acks ONLY — never raw wire.TDataOut nor terminal-snapshot chunks. The phone's
// terminal view is EXCLUSIVELY the sealed daemon-rendered snapshot stream (Slices
// C/D/E/F2); raw output to a remote controller is wasted bytes AND a gateway
// drain-or-die/evictPump hazard, so the pump must suppress it.
//
// The property is TIER-SCOPED: suppression is keyed on s.remoteTier, so a LOCAL
// (owner) controller attached to the SAME backend/session STILL receives raw
// TDataOut. The pump must keep DRAINING the frames channel while suppressed, so
// end-of-session detection and lease lifecycle (OpDetach on stream close) are
// unchanged.
//
// RED today: the pump writes the OpLease's SnapshotLen, the TSnapshot chunk loop, and
// the live TDataOut write on BOTH tiers, so the remote controller receives a snapshot
// chunk (and would receive raw output) — the assertion below fails. GREEN gates those
// three writes on !s.remoteTier while still draining frames.

import (
	"bytes"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/wire"
)

// TestRemotePump_SuppressesRawOutput: an authorized REMOTE-tier take_control and a
// concurrent LOCAL (owner-tier) attach share ONE backend and the SAME local session.
// Each is a controller with its own pump. When each session emits output:
//   - the REMOTE controller receives its OpLease (nonzero generation) and then NOTHING
//     but the end-of-session OpDetach — never a TDataOut, never a TSnapshot chunk;
//   - the LOCAL controller STILL receives the raw TDataOut (suppression is remote-only).
func TestRemotePump_SuppressesRawOutput(t *testing.T) {
	stub := newStubDaemon() // authzFn nil => take_control is accepted; kill switch ON; ops fresh
	ownerSock := serveStub(t, stub)   // owner (local) tier
	remoteSock := serveRemote(t, stub) // remote tier (s.remoteTier == true)

	// LOCAL controller: owner-tier attach establishes a lease + pump (stream index 0).
	local := rawDial(t, ownerSock)
	lrep := local.hello(Version, []string{CapAttach})
	lsid := lrep.EndpointID + "/sess1"
	local.writeControl(Control{Op: OpAttach, EndpointID: lrep.EndpointID, SessionID: lsid})
	lLease := nextControl(t, local)
	if lLease.Op != OpLease {
		t.Fatalf("owner-tier attach = op %q code %q; want a lease grant (OpLease)", lLease.Op, lLease.ErrorCode)
	}

	// REMOTE controller: signed take_control establishes a lease + pump (stream index 1).
	remote := rawDial(t, remoteSock)
	rrep := remote.hello(Version, []string{CapRemoteGateway})
	rsid := rrep.EndpointID + "/sess1"
	exp := time.Now().Add(time.Minute)
	remote.writeControl(Control{
		Op: OpTakeControl, EndpointID: rrep.EndpointID, SessionID: rsid,
		OperationID: "devA:01JTAKE0000000000000000",
		DeviceID:    "devA", DeviceSig: "sig", ExpiresAt: &exp,
		GateToken: "gate-tok",
	})

	// The FIRST frame to the remote controller must be its OpLease. On the vulnerable
	// (pre-suppression) pump the OpLease still comes first — the snapshot chunk follows
	// it — so reading the first frame directly is correct on both RED and GREEN.
	rtyp, rpayload, err := remote.readFrame()
	if err != nil {
		t.Fatalf("remote: read lease frame: %v", err)
	}
	if rtyp != wire.TControl {
		t.Fatalf("remote first frame type = %d; want TControl (the OpLease grant)", rtyp)
	}
	rLease, err := DecodeControl(rpayload)
	if err != nil {
		t.Fatalf("remote: decode lease: %v", err)
	}
	if rLease.Op != OpLease || rLease.Generation == 0 {
		t.Fatalf("remote take_control reply = op %q gen %d; want OpLease with a nonzero generation", rLease.Op, rLease.Generation)
	}

	localStream := stub.streamAt(0)
	remoteStream := stub.streamAt(1)
	if localStream == nil || remoteStream == nil {
		t.Fatalf("want two upstream streams (local + remote); got local=%v remote=%v", localStream, remoteStream)
	}

	// The remote session emits output, then ends. A suppressing pump DRAINS the output
	// frame (no write) and, on close, sends OpDetach — so the next frames the remote
	// controller sees carry NO raw output and terminate cleanly on OpDetach.
	remoteStream.frames <- []byte("REMOTE-OUTPUT")
	close(remoteStream.frames)

	sawDetach := false
	for i := 0; i < 16 && !sawDetach; i++ {
		typ, payload, err := remote.readFrame()
		if err != nil {
			t.Fatalf("remote: read frame after lease: %v", err)
		}
		switch typ {
		case wire.TDataOut:
			t.Fatalf("remote controller received raw TDataOut %q; a REMOTE-tier controller must get OpLease + acks ONLY, never raw output (A7/F3)", payload)
		case wire.TSnapshot:
			t.Fatalf("remote controller received a terminal-snapshot chunk %q; the phone's view is the sealed daemon-rendered snapshot stream, not raw snapshot frames (A7/F3)", payload)
		case wire.TControl:
			c, derr := DecodeControl(payload)
			if derr != nil {
				t.Fatalf("remote: decode control: %v", derr)
			}
			if c.Op == OpDetach {
				sawDetach = true // clean end-of-session: frames drained, lease lifecycle intact
			}
		}
	}
	if !sawDetach {
		t.Fatalf("remote controller never saw OpDetach; suppression must still DRAIN frames so end-of-session + lease lifecycle are unchanged (F3)")
	}

	// Regression guard (tier-scoping): the LOCAL controller on the SAME session STILL
	// receives the raw TDataOut. This must hold before AND after the fix; if it breaks,
	// suppression leaked into the owner tier.
	localStream.frames <- []byte("LOCAL-OUTPUT")
	sawLocalOut := false
	for i := 0; i < 16 && !sawLocalOut; i++ {
		typ, payload, err := local.readFrame()
		if err != nil {
			t.Fatalf("local: read frame after lease: %v", err)
		}
		if typ == wire.TDataOut && bytes.Equal(payload, []byte("LOCAL-OUTPUT")) {
			sawLocalOut = true
		}
	}
	if !sawLocalOut {
		t.Fatalf("owner-tier (local) controller did not receive raw TDataOut; suppression must be REMOTE-tier-only (R-POL.1 local exemption)")
	}
}
