package remotegw

// Bead agents-tracker-r3p -- the COMPOSITION assertion. The per-layer tests
// (internal/phonecore TestR3PCoalescer_*, and TestR3PLeaseConn_* beside this file) can both
// be green while the thing the user does still fails, and that is the failure class this
// repository keeps shipping. This test drives one path with nothing stubbed between the
// phone's coalescer and the frames a daemon would read:
//
//	real phonecore.InputCoalescer -> real sealed mailbox envelopes -> real CommandBridge
//	(open, Accept, route by the session sealed inside the frame) -> real LeaseManager-shaped
//	router -> real LeaseConn.WriteDataIn -> the wire frames a daemon receives
//
// IT IS ADVERSARIAL WHERE THE RELAY IS. Both envelopes are delivered in ONE batch, so the
// 125 ms the coalescer put between them is gone before the gateway ever sees them -- which is
// what a store-and-forward mailbox does, not a pathological case invented for this test. If
// the gap is not created here it does not exist anywhere.
//
// WHAT IT DOES NOT REACH: the daemon -> shim -> PTY hop. That hop is frame-for-frame and this
// bead did not touch it -- internal/protocol/server.go:2278 calls forwardInput once per
// received frame, forwardInput ends in one stream.Input(p) (server.go:804),
// shimStream.Input is one wire.WriteFrame per call (internal/protocol/fromdaemon.go:196), and
// the shim does one s.ptyIn.Write(payload) per frame (internal/shim/server.go:187). Asserting
// the write boundary AT the PTY needs a reader that puts the terminal in raw mode and reports
// each read(): the tty line discipline is canonical there, so it hands complete lines to the
// agent regardless of how many writes produced them, and the fake agent reads with
// bufio.ReadString('\n'). The real CLIs see raw chunks because they call tcsetattr; the fake
// agent would have to grow the same, which is a new script step, per-OS termios handling and
// a new slow end-to-end run. That cost is stated in the report rather than paid here.
//
// This file contains NO implementation.

import (
	"context"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// r3pLeaseRouter is the LeaseRouter the bridge routes through, forwarding Input to a REAL
// LeaseConn. Only the daemon socket underneath that conn is a test double; the write path,
// its lock and its pacing are the shipped ones.
type r3pLeaseRouter struct{ lc *LeaseConn }

func (r *r3pLeaseRouter) Begin(protocol.RemoteCommand) error { return nil }
func (r *r3pLeaseRouter) End(string)                         {}
func (r *r3pLeaseRouter) Generation(string) uint64           { return 1 }
func (r *r3pLeaseRouter) OnSever(func(SeveredLease))         {}

func (r *r3pLeaseRouter) Input(_ string, f InputFrame) error {
	switch f.Kind {
	case "data":
		return r.lc.WriteDataIn(f.Data)
	case "resize":
		return r.lc.WriteResize(f.Cols, f.Rows)
	}
	return nil
}

// TestR3PComposition_TypingALineAndEnterArrivesAsTwoSpacedWrites is the end-to-end property:
// what the user does on the phone ("type a line, press Enter") must arrive as two writes with
// a gap between them, no matter that the relay delivered the frames together.
func TestR3PComposition_TypingALineAndEnterArrivesAsTwoSpacedWrites(t *testing.T) {
	const session = "m1/s1"

	// ---- the phone: the REAL coalescer, given exactly what PhoneSurface sends ----------
	c := phonecore.NewInputCoalescer(time.Now)
	frames := c.Type(session, []byte("git status\r"))
	// Flush is the boundary release (App.scheduleDrain's timer does the same thing 125 ms
	// later); taking it now is the adversarial case, because both frames then reach the
	// relay together and the gateway is the only place left that can space them.
	frames = append(frames, c.Flush()...)

	if len(frames) != 2 {
		t.Fatalf("the coalescer produced %d frames for %q, want 2 -- the phone half of the fix is not in the path this test drives", len(frames), "git status\\r")
	}

	// ---- the relay: both sealed frames in ONE delivered batch --------------------------
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 7)
	}
	items := make([]relay.Item, 0, len(frames))
	for i, f := range frames {
		seq := uint64(i + 1)
		items = append(items, relay.Item{
			Cursor:   seq,
			Envelope: sealInputEnv(t, key, seq, inputFrameWire{T: f.T, Session: f.Session, Data: f.Data}),
		})
	}

	// ---- the machine: the real bridge, routing into a real LeaseConn -------------------
	lc, arrivals := r3pLeaseConn(t)
	b := NewCommandBridge(CommandBridgeConfig{
		Mailbox:     &fakeMailbox{inbox: items},
		Forwarder:   &fakeForwarder{},
		Leases:      &r3pLeaseRouter{lc: lc},
		Key:         key,
		EpochID:     1,
		ReplyTarget: "phone",
	})

	n, err := b.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if n != len(items) {
		t.Fatalf("the bridge processed %d of %d frames; a dropped keystroke is not a pacing question", n, len(items))
	}

	// ---- what a daemon received --------------------------------------------------------
	text := r3pNext(t, arrivals)
	submit := r3pNext(t, arrivals)

	if text.payload != "git status" {
		t.Fatalf("the first write was %q, want %q -- text and its submit in one write is the frame Claude Code reads as a paste", text.payload, "git status")
	}
	if submit.payload != "\r" {
		t.Fatalf("the second write was %q, want a lone CR", submit.payload)
	}
	if gap := submit.at.Sub(text.at); gap+r3pSkew < r3pWantGap {
		t.Fatalf("the two writes were %v apart, want at least %v -- the phone spaced them by %v and the batched relay delivery collapsed that, so the gateway is the only hop left that can restore it", gap, r3pWantGap, phonecore.InputFrameInterval)
	}
}
