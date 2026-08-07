package protocol

// FAILING-FIRST proof test for ADR-010 Amendment 1 A2 / Phase 3 PIECE 4: a send_input
// message is ATOMIC against concurrent lease input.
//
// This is the property that makes a daemon-mediated write safe to add beside the attach
// lease. The handler sleeps submitframe.Gap between a message's text frame and its submit
// frame; if it released the per-session input serialization across that sleep, an attached
// human's keystrokes would land BETWEEN them — the injected text would submit whatever the
// human typed in the interval, or the human's line would submit as part of the agent's
// message. So the handler must hold the SAME serialization forwardInput takes (the lease's
// inMu, server.go:844) across every frame of one message.
//
// The test drives real contention rather than asserting on structure: an attached
// controller writes keystrokes continuously while a second connection sends a text+submit
// message, and the recorded shim write sequence must show the message's two frames
// ADJACENT. Every lease keystroke lands wholly before the text or wholly after the CR.
//
// SCOPE, as the docs now state it: both connections here are OWNER-TIER, on one Server, and
// that is exactly the guarantee. A remote take_control controller lives on a DIFFERENT
// Server value with its own per-session serialization over the shared tap and may still
// interleave — accepted for the personal single-owner model (ADR-010 A2), because a remote
// take-control is the human deliberately grabbing the session.
//
// RED today: the file references OpSendInput / SendInputReq / Control.SendInput, which do
// not exist yet, so the package fails to compile ("undefined-only" red).

import (
	"bytes"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/wire"
)

// TestSendInput_AtomicAgainstConcurrentLeaseInput: nothing interleaves between a message's
// frames. The controller hammers input for longer than submitframe.Gap, so its keystrokes
// are guaranteed to contend with the daemon's sleep; the assertion is the invariant, so it
// holds whatever the interleaving turns out to be.
func TestSendInput_AtomicAgainstConcurrentLeaseInput(t *testing.T) {
	d := newSendInputDaemon()
	d.setMetas(statusMeta("sess1", status.TurnIdle, status.InteractionNone))
	sock := serveOwnerAPI(t, d)

	// The attached human.
	owner := rawDial(t, sock)
	orep := owner.hello(Version, []string{CapAttach})
	sid := orep.EndpointID + "/sess1"
	owner.writeControl(Control{Op: OpAttach, EndpointID: orep.EndpointID, SessionID: sid})
	if lease := nextControl(t, owner); lease.Op != OpLease {
		t.Fatalf("owner attach = op %q; want a lease grant", lease.Op)
	}

	// The agent, on its own connection.
	agent := rawDial(t, sock)
	arep := agent.hello(Version, nil)
	agent.writeControl(Control{
		Op: OpSendInput, EndpointID: arep.EndpointID, SessionID: arep.EndpointID + "/sess1",
		SendInput: &SendInputReq{Text: "hello world", Submit: true},
	})

	// Keystrokes from the lease for longer than the daemon's gap, so at least one is
	// certain to arrive while the message is mid-flight (it blocks on the serialization
	// the handler holds, which is the point).
	const keystroke = "k"
	deadline := time.Now().Add(400 * time.Millisecond)
	sent := 0
	for time.Now().Before(deadline) {
		owner.writeFrame(wire.TDataIn, []byte(keystroke))
		sent++
		sleepMS(5)
	}

	if got := nextControl(t, agent); got.Op != OpOK {
		t.Fatalf("send_input reply = op %q error %q; want OpOK", got.Op, got.Error)
	}
	// A trailing round trip on the OWNER connection proves every keystroke written above
	// was already handled (one in-order loop per connection), so the sequence below is
	// complete without sleeping for it.
	owner.writeControl(Control{Op: OpList, EndpointID: orep.EndpointID})
	syncControlOp(t, owner, OpList)

	ws := d.onlyStream(t).written()
	text, cr := -1, -1
	leaseWrites := 0
	for i, w := range ws {
		switch {
		case string(w.payload) == "hello world":
			text = i
		case string(w.payload) == "\r":
			cr = i
		case bytes.Equal(w.payload, []byte(keystroke)):
			leaseWrites++
		}
	}
	if text < 0 || cr < 0 {
		t.Fatalf("the message's frames are not both in the shim write sequence %q", concat(ws))
	}
	if cr != text+1 {
		t.Fatalf("lease input interleaved INSIDE the message: text at frame %d, CR at frame %d, "+
			"sequence %q. The whole send_input must hold the per-session input serialization "+
			"(forwardInput's ls.inMu) across every frame, sleep included", text, cr, concat(ws))
	}
	if leaseWrites == 0 {
		t.Fatalf("no lease keystroke reached the shim out of %d written; the test proved nothing about "+
			"contention (the controller's input path must still work while send_input runs)", sent)
	}
}
