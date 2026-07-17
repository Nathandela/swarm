package protocol

import (
	"bytes"
	"testing"

	"github.com/Nathandela/swarm/internal/wire"
)

// E6.7 — snapshot-before-live-frames ordering guaranteed at the protocol layer
// (S10). On attach the client receives EXACTLY ONE snapshot frame before any live
// output frame, with no gap or overlap.

// TestOrdering_ExactlyOneSnapshotPrecedesLiveFramesRaw drives the attach at the
// wire level and asserts the frame sequence: a lease control, then exactly one
// TSnapshot, then TDataOut live frames — never a live frame before the snapshot,
// never two snapshots.
func TestOrdering_ExactlyOneSnapshotPrecedesLiveFramesRaw(t *testing.T) {
	stub := oneRunningSession()
	sock := serveStub(t, stub)

	// Attach via the Client so the lease/stream is wired, but read the raw frames
	// off a second, protocol-level connection to observe the exact ordering.
	r := rawDial(t, sock)
	hello := r.hello(Version, []string{"attach"})

	// Discover the session id for this endpoint.
	r.writeControl(Control{Op: OpList, EndpointID: hello.EndpointID})
	list := r.readControl()
	if len(list.Sessions) != 1 {
		t.Fatalf("list returned %d sessions, want 1", len(list.Sessions))
	}
	sid := list.Sessions[0].ID

	// Publish live output BEFORE we start reading the attach stream, so the
	// snapshot must still come first regardless of timing.
	r.writeControl(Control{Op: OpAttach, EndpointID: hello.EndpointID, SessionID: sid})

	// The first non-control frame after the lease grant must be the single
	// snapshot. Push a live frame into the stream now.
	// (The stream was opened by the server when it handled OpAttach.)
	pushWhenReady(t, stub, []byte("LIVE-1"))

	sawSnapshot := false
	sawLive := false
	// Read a bounded number of frames and validate the ordering rules.
	for i := 0; i < 6; i++ {
		typ, payload, err := r.readFrame()
		if err != nil {
			break
		}
		switch typ {
		case wire.TControl:
			// The lease grant (OpLease) is allowed before the snapshot; anything
			// else control-wise is not part of the attach data ordering.
			continue
		case wire.TSnapshot:
			if sawSnapshot {
				t.Fatalf("received a SECOND snapshot frame — violates S10 (exactly one)")
			}
			if sawLive {
				t.Fatalf("snapshot arrived AFTER a live frame — violates S10 ordering")
			}
			sawSnapshot = true
			if !bytes.Equal(payload, []byte("SNAPSHOT")) {
				t.Errorf("snapshot payload = %q, want the stream's snapshot", payload)
			}
		case wire.TDataOut:
			if !sawSnapshot {
				t.Fatalf("live TDataOut frame arrived BEFORE the snapshot — violates S10")
			}
			sawLive = true
		}
		if sawSnapshot && sawLive {
			break
		}
	}
	if !sawSnapshot {
		t.Fatalf("no snapshot frame delivered on attach — violates S10/A-4")
	}
	if !sawLive {
		t.Fatalf("no live frame delivered after the snapshot")
	}
}

// TestOrdering_SnapshotDeliveredThroughAttachmentAPI asserts the same guarantee
// through the high-level Attachment: Snapshot() returns the one snapshot and the
// live bytes appear only on Frames(), never mixed into the snapshot.
func TestOrdering_SnapshotDeliveredThroughAttachmentAPI(t *testing.T) {
	stub := oneRunningSession()
	sock := serveStub(t, stub)
	c := dialClient(t, sock, []string{"attach"})

	a, err := c.Attach(onlyViewID(t, c))
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if !bytes.Equal(a.Snapshot(), []byte("SNAPSHOT")) {
		t.Fatalf("Attachment.Snapshot() = %q, want the one snapshot", a.Snapshot())
	}

	stub.lastStream().frames <- []byte("LIVE")
	got, ok := recvFrame(t, a.Frames(), recvTimeout)
	if !ok {
		t.Fatalf("no live frame delivered on Frames()")
	}
	if bytes.Contains(got, []byte("SNAPSHOT")) {
		t.Fatalf("snapshot bytes leaked into the live Frames() stream: %q", got)
	}
	if !bytes.Equal(got, []byte("LIVE")) {
		t.Errorf("live frame = %q, want LIVE", got)
	}
}

// pushWhenReady publishes data into the session's upstream stream once the server
// has opened it (i.e., once DaemonAPI.Attach has been called).
func pushWhenReady(t *testing.T, stub *stubDaemon, data []byte) {
	t.Helper()
	go func() {
		for i := 0; i < 200; i++ {
			if st := stub.lastStream(); st != nil {
				st.frames <- data
				return
			}
			sleepMS(5)
		}
	}()
}
