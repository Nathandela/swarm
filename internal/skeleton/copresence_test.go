package skeleton

// M0.1 (mirror-program.md wave M0, bead agents-tracker-dwwv.1.1) -- the CO-PRESENCE
// ground truth. Two sources in this repo disagree about what happens when an OWNER-tier
// TUI attach and a REMOTE-tier phone take_control exist on the SAME session at once:
//
//   - internal/skeleton/sessiontap_test.go:4-8 says the shared per-session tap fans live
//     frames to two consumers and "neither supersedes the other";
//   - internal/tui/attach.go:65-70 says a TUI attach "unconditionally evicts the phone"
//     because both contend for the SAME single shim subscriber slot.
//
// Only one can be true, and the tap unit test cannot settle it: it drives the tap through
// its injectable dial seam, below the two protocol Servers. The eviction claim is about
// what the two Servers do to each other. So this test is deliberately assembled at the
// production wiring (serve.go:283-312): ONE coreAPI serving BOTH an owner-tier Server on
// the main UDS and a remote-tier Server on remote.sock, a REAL shim session producing real
// PTY output, a REAL device keystore signing a REAL take_control (gate token bound into the
// signature, no crypto shortcut -- takecontrol_gatetoken_test.go's helpers verbatim), and a
// REAL protocol.Client attach on the owner socket.
//
// The phone's live terminal stream is its terminal_subscribe peek (the take_control pump
// suppresses raw output on the remote tier by design, server.go:723), so the phone holds
// TWO things that a "single subscriber slot" would have to fight over: the peek's read-only
// tap AND the control lease's read-write tap. Both are asserted:
//
//   - the peek must deliver a screen containing output produced AFTER the owner attaches
//     (its live stream survived), and
//   - a keystroke written on the take_control connection AFTER the owner attaches must
//     still reach the session's PTY (its control lease survived), with the resulting
//     output visible on the OWNER's attachment (the owner's live stream survived too).
//
// Then the mirror-image order: owner first, remote second.
//
// If either surface really evicted the other, the evicted side's stream would go silent
// (or close) and the corresponding await below would time out.

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/wire"
)

// copresenceScript blocks the fake agent on stdin twice, so the test -- not the clock --
// decides when NEW output is produced, and from WHICH surface the keystroke that produces
// it came. Each answered ask prints "got: <line>", a string the session cannot emit on its
// own, so seeing it is proof that input landed AND that output flowed after it.
const copresenceScript = "print READY\nask Q1?\nask Q2?\nidle 60s\n"

// dataIn writes a raw keystroke frame on a remote-tier connection, exactly as the gateway's
// LeaseConn does under an established take_control lease (server.go dispatches wire.TDataIn
// to handleDataIn, which gates on the live control session).
func dataIn(t *testing.T, r *rawRemote, p []byte) {
	t.Helper()
	if err := wire.WriteFrame(r.conn, wire.TDataIn, p); err != nil {
		t.Fatalf("write data_in on the remote lease: %v", err)
	}
}

// subscribePeek opens the phone's read-only terminal peek on a remote-tier connection.
func subscribePeek(t *testing.T, r *rawRemote, session string) {
	t.Helper()
	r.write(protocol.Control{Op: protocol.OpTerminalSubscribe, EndpointID: r.endpointID, SessionID: session})
	if got := r.read(10 * time.Second); got.Op != protocol.OpOK {
		t.Fatalf("terminal_subscribe = op %q code %q; want OpOK (the phone's live terminal stream)", got.Op, got.ErrorCode)
	}
}

// awaitPeek scans a peek connection's server-pushed terminal_snapshot renders for one whose
// screen contains want, returning whether it arrived within the bound and the last screen
// seen (for a legible failure).
func awaitPeek(r *rawRemote, want string, within time.Duration) (bool, string) {
	deadline := time.Now().Add(within)
	last := ""
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false, last
		}
		c, err := r.readTry(remaining)
		if err != nil {
			return false, last
		}
		if c.Op != protocol.OpTerminalSnapshot || c.Terminal == nil {
			continue
		}
		last = strings.Join(c.Terminal.Lines, "\n")
		if strings.Contains(last, want) {
			return true, last
		}
	}
}

// awaitFrames drains an owner attachment's live output frames until the accumulated bytes
// contain want, returning whether they did and everything drained (for a legible failure).
// A closed channel means the lease died -- reported as a miss, with what had arrived.
func awaitFrames(att *protocol.Attachment, want string, within time.Duration) (bool, string) {
	var buf []byte
	timeout := time.After(within)
	for {
		select {
		case f, ok := <-att.Frames():
			if !ok {
				return false, string(buf)
			}
			buf = append(buf, f...)
			if bytes.Contains(buf, []byte(want)) {
				return true, string(buf)
			}
		case <-timeout:
			return false, string(buf)
		}
	}
}

// awaitFramesClosed drains an owner attachment until its frame channel CLOSES (the lease
// was superseded or released), reporting whether that happened within the bound.
func awaitFramesClosed(att *protocol.Attachment, within time.Duration) bool {
	timeout := time.After(within)
	for {
		select {
		case _, ok := <-att.Frames():
			if !ok {
				return true
			}
		case <-timeout:
			return false
		}
	}
}

// TestCoPresence_OwnerAttachAndRemoteControl_BothStreamsLive settles the disagreement in
// both orders. The two subtests are the same three surfaces (owner attachment, phone peek,
// phone control lease) established in opposite orders; in each, output is produced AFTER
// the second surface joins, and BOTH live streams must carry it.
func TestCoPresence_OwnerAttachAndRemoteControl_BothStreamsLive(t *testing.T) {
	t.Run("RemoteFirstThenOwnerAttach", func(t *testing.T) {
		sk, rsock := assembleWithRemote(t)
		ks := registerPhone(t, sk, device.CapFull) // real device; also turns the durable kill switch ON
		meta := launchFake(t, sk, copresenceScript)
		session := protocol.NamespacedID(sk.api.endpointID, meta.ID)

		// --- REMOTE tier first: the phone's peek, then its signed control lease. ---
		peek := dialRemote(t, rsock, protocol.CapRemoteGateway)
		subscribePeek(t, peek, session)
		if ok, last := awaitPeek(peek, "Q1?", 15*time.Second); !ok {
			t.Fatalf("the phone's peek never showed the session's own output before any owner attach; last screen:\n%s", last)
		}

		ctl := dialRemote(t, rsock, protocol.CapRemoteGateway)
		const gateToken = "gate-copresence-remote-first"
		cmd := signedTakeControl(t, ks, sk.api.endpointID, session, "devCP:01JCOPRESREMOTEFIRST00", gateToken, time.Now().Add(time.Minute))
		sendTakeControl(ctl, ctl.endpointID, session, cmd, gateToken)
		if got := ctl.read(10 * time.Second); got.Op != protocol.OpLease || got.Generation == 0 {
			t.Fatalf("take_control = op %q code %q gen %d; want an OpLease grant (the phone holds the remote control lease)", got.Op, got.ErrorCode, got.Generation)
		}

		// --- OWNER tier second: the TUI's attach, over the main UDS. This is the dial that
		// internal/tui/attach.go:65-70 claims "unconditionally evicts the phone". ---
		oc := dialClient(t, sk)
		att, err := oc.Attach(session)
		if err != nil {
			t.Fatalf("owner attach while the phone holds control: %v", err)
		}
		defer func() { _ = att.Detach() }()

		// NEW output, produced AFTER the owner attached, driven from the OWNER's keyboard.
		if err := att.Input([]byte("alpha\n")); err != nil {
			t.Fatalf("owner input: %v", err)
		}
		// THE ASSERTION: the phone's live terminal stream still carries frames produced after
		// the owner attach. Under eviction this render never arrives.
		if ok, last := awaitPeek(peek, "got: alpha", 20*time.Second); !ok {
			t.Fatalf("EVICTION: the phone's live terminal stream carried NO output produced after the owner attached; last screen:\n%s", last)
		}
		if ok, drained := awaitFrames(att, "got: alpha", 20*time.Second); !ok {
			t.Fatalf("the owner's own attachment carried no output after its input; drained %q", drained)
		}

		// Non-vacuity control for awaitPeek: a string the session never emits must NOT be
		// found, so the positive result above is a real observation and not a helper that
		// returns true unconditionally.
		if ok, _ := awaitPeek(peek, "got: never-typed", 1500*time.Millisecond); ok {
			t.Fatal("awaitPeek matched a string the session never emitted; the peek assertions above are vacuous")
		}

		// And the phone's CONTROL lease survived the owner attach too: a keystroke written on
		// the take_control connection after the owner attached still reaches the PTY, and the
		// output it provokes is visible on the OWNER's live stream.
		dataIn(t, ctl, []byte("beta\n"))
		if ok, drained := awaitFrames(att, "got: beta", 20*time.Second); !ok {
			t.Fatalf("EVICTION: the phone's control lease no longer reached the session's PTY after the owner attached "+
				"(or the owner's stream died); owner drained %q", drained)
		}
		if ok, last := awaitPeek(peek, "got: beta", 20*time.Second); !ok {
			t.Fatalf("the phone's peek missed the output its own keystroke produced; last screen:\n%s", last)
		}

		// NEGATIVE CONTROL -- this harness CAN see an eviction, so the survivals above are
		// findings and not blind spots. Eviction is real WITHIN one tier: a second OWNER attach
		// supersedes the first on the owner Server's own lease map (protocol/server.go attach,
		// phase 2 tears down the prior controller). The first attachment's frame channel must
		// therefore CLOSE -- which is exactly the signal the cross-tier assertions above looked
		// for and did not find.
		oc2 := dialClient(t, sk)
		att2, err := oc2.Attach(session)
		if err != nil {
			t.Fatalf("second owner attach: %v", err)
		}
		defer func() { _ = att2.Detach() }()
		if !awaitFramesClosed(att, 10*time.Second) {
			t.Fatal("negative control failed: a SECOND owner attach did not close the first owner attachment's stream, " +
				"so this harness cannot distinguish survival from eviction")
		}
	})

	t.Run("OwnerFirstThenRemoteTakeControl", func(t *testing.T) {
		sk, rsock := assembleWithRemote(t)
		ks := registerPhone(t, sk, device.CapFull)
		meta := launchFake(t, sk, copresenceScript)
		session := protocol.NamespacedID(sk.api.endpointID, meta.ID)

		// --- OWNER tier first. ---
		oc := dialClient(t, sk)
		att, err := oc.Attach(session)
		if err != nil {
			t.Fatalf("owner attach: %v", err)
		}
		defer func() { _ = att.Detach() }()

		// --- REMOTE tier second: peek + signed control lease, both onto the session the
		// owner is already attached to. ---
		peek := dialRemote(t, rsock, protocol.CapRemoteGateway)
		subscribePeek(t, peek, session)
		ctl := dialRemote(t, rsock, protocol.CapRemoteGateway)
		const gateToken = "gate-copresence-owner-first"
		cmd := signedTakeControl(t, ks, sk.api.endpointID, session, "devCP:01JCOPRESOWNERFIRST00", gateToken, time.Now().Add(time.Minute))
		sendTakeControl(ctl, ctl.endpointID, session, cmd, gateToken)
		if got := ctl.read(10 * time.Second); got.Op != protocol.OpLease || got.Generation == 0 {
			t.Fatalf("take_control onto an owner-attached session = op %q code %q gen %d; want an OpLease grant", got.Op, got.ErrorCode, got.Generation)
		}

		// NEW output, produced AFTER the phone took control, driven from the PHONE.
		dataIn(t, ctl, []byte("gamma\n"))
		// THE MIRROR ASSERTION: the OWNER's live stream still carries frames produced after
		// the phone took control. Under eviction (in this direction) it would go silent.
		if ok, drained := awaitFrames(att, "got: gamma", 20*time.Second); !ok {
			t.Fatalf("EVICTION: the owner's live terminal stream carried NO output produced after the phone took control; drained %q", drained)
		}
		if ok, last := awaitPeek(peek, "got: gamma", 20*time.Second); !ok {
			t.Fatalf("the phone's peek carried no output while the owner was attached; last screen:\n%s", last)
		}

		// The owner can still drive the session with the phone's lease live.
		if err := att.Input([]byte("delta\n")); err != nil {
			t.Fatalf("owner input while the phone holds control: %v", err)
		}
		if ok, drained := awaitFrames(att, "got: delta", 20*time.Second); !ok {
			t.Fatalf("the owner's input no longer reached the session's PTY once the phone held control; drained %q", drained)
		}
	})
}
