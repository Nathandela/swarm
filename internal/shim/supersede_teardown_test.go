package shim

// C3-committee item B (R1.3.3 hardening): an UNCOORDINATED supersede — a second
// connection attaching while a prior subscriber's connection is still open —
// must CLOSE the superseded connection, not just its queue. Closing only the
// queue leaves the superseded peer's reader blocked forever on a silent socket
// (no EOF, no detach control) and a wedged writer blocked mid-Write with
// nothing to unblock it until shim shutdown. The daemon-coordinated supersede
// already closes the old connection first (protocol Phase 2), so this path is
// reached only by an uncoordinated second attach; it must degrade to a prompt
// EOF at the superseded peer. Failing-first: pre-fix the superseded peer's read
// blocked until the test deadline.

import (
	"net"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/vt"
	"github.com/Nathandela/swarm/internal/wire"
)

// TestHub_SupersededConnGetsEOF attaches subscriber A, then supersedes it with
// subscriber B via a direct hub.attach on a second connection, and asserts A's
// connection reads EOF/error promptly instead of blocking forever.
func TestHub_SupersededConnGetsEOF(t *testing.T) {
	emu := vt.NewEmulator(80, 24)
	t.Cleanup(func() { emu.Close() })
	h := &hub{emu: emu, tr: newHubTranscript(t), metrics: &Metrics{}}

	aServer, aClient := net.Pipe()
	defer aServer.Close()
	defer aClient.Close()
	cwA := &connWriter{conn: aServer}
	subA := h.attach(cwA)

	// Drain A's snapshot so its writer goroutine is past sendSnapshot.
	if typ, _, err := wire.ReadFrame(aClient); err != nil || typ != wire.TSnapshot {
		t.Fatalf("subscriber A snapshot: typ=%d err=%v", typ, err)
	}

	bServer, bClient := net.Pipe()
	defer bServer.Close()
	defer bClient.Close()
	go func() {
		// Drain B's snapshot and any frames so its writer never wedges.
		for {
			if _, _, err := wire.ReadFrame(bClient); err != nil {
				return
			}
		}
	}()
	subB := h.attach(&connWriter{conn: bServer})
	defer func() {
		h.detach(subB)
		<-subB.done
	}()

	// A's writer must terminate (queue closed by the supersede)...
	select {
	case <-subA.done:
	case <-time.After(2 * time.Second):
		t.Fatal("superseded subscriber A's writer goroutine did not exit")
	}
	// ...and A's CONNECTION must be closed so its peer sees EOF promptly
	// rather than a silent, permanently-frozen stream.
	_ = aClient.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := wire.ReadFrame(aClient); err == nil {
		t.Fatal("superseded subscriber A's connection still delivered a frame; want EOF/close")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("superseded subscriber A's connection was left open (read timed out); want prompt EOF/close")
	}
}
