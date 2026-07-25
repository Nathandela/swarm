// FAILING-FIRST (TDD RED, GG-5) tests for PB-GW-4 (§4.6 / §6.0b): PER-ACTION-CLASS
// replay tests across a gateway restart against a RETAINING (adversarial) relay -- one
// that does not honour ack-deletion, so every frame it ever held stays replayable.
//
// SCOPE DISCIPLINE (§4.6, and what these tests deliberately do NOT claim): the full
// keystroke-injection exploit was investigated and DISPROVED for the shipped tree --
// internal/phonecore and internal/phonesim are imported by no production binary, so a
// retaining relay has nothing to replay against; a restart gives an empty LeaseManager
// that drops input with no lease; and a replayed take_control is refused because its
// operation_id is single-use in the DURABLE idempotency store. What remains true, and
// what these tests encode, is the conditional Phase-B trace: with a SEQ-REGRESSED phone
// (durable keys but a send-seq that restarted at 1 -- exactly the state Phase B creates
// before PB-STATE lands) the replay guard provides nothing at all.
//
// Every assertion is made AT THE GUARD -- the fake routers/forwarder record every
// dispatch unconditionally -- and never at a downstream side effect (an empty lease map,
// the daemon's durable operation_id idempotency) that other mechanisms already prevent.
// That is the "proves nothing" trap PB-GW-4 names explicitly.
//
// These tests share the seam pinned by inbound_state_test.go (InboundState /
// InboundCheckpoint / InboundStream / CommandBridgeConfig.Inbound).
package remotegw

import (
	"context"
	"errors"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// TestReplay_SeqRegressedPhoneRetainedInputNeverReachesLease is the §4.6 trace, verbatim:
//
//	run 1  phone sends take_control(m/s1) at seq 1 and keystrokes at seqs 2..61.
//	       The relay retains the tail (seqs 60, 61) rather than deleting it on ack.
//	--- gateway restarts; the phone restarts too and its send-seq REGRESSES to 1 ---
//	run 2  1. a LEGITIMATE fresh take_control at seq 1: on today's fresh receiver
//	          seen == false, so the staleness check is skipped, a lease opens.
//	       2. retained input at seq 60: gap := 60 > 1+1 -> true -> routeInput DROPS it,
//	          but Accept still advances the high-water to 60.
//	       3. retained input at seq 61: gap := 61 > 60+1 -> FALSE -> routed to the live
//	          lease and on to the PTY.
//
// With a seeded high-water of 61 every one of those three frames is refused with
// crypto.ErrStaleSeq. That the legitimate take_control is refused TOO is the intended
// fail-closed consequence of a monotonic per-(sender,epoch) guard, not a goal: it is
// exactly why PB-GW-1 and PB-STATE-3/-4 must land together.
func TestReplay_SeqRegressedPhoneRetainedInputNeverReachesLease(t *testing.T) {
	key := inboundKey(41)
	const epoch uint32 = 7
	st := &memInboundState{}

	// Run 1: a monotonic phone, seqs 1..61 on the single shared command+input stream.
	rl1 := &retainingRelay{}
	rl1.add(1, sealAt(t, key, epoch, 1, protocol.RemoteCommand{DeviceCommandAuth: protocol.DeviceCommandAuth{
		Action: protocol.ActionTakeControl, Session: "m/s1", OperationID: "op-tc-run1", DeviceID: "d1", Sig: "sig-tc",
	}}))
	var retained60, retained61 []byte
	for seq := uint64(2); seq <= 61; seq++ {
		env := sealAt(t, key, epoch, seq, inputFrameWire{T: "data", Session: "m/s1", Data: []byte("x")})
		rl1.add(seq, env)
		switch seq {
		case 60:
			retained60 = env
		case 61:
			retained61 = env
		}
	}
	mgr1 := &fakeLeaseRouter{}
	b1 := NewCommandBridge(CommandBridgeConfig{
		Mailbox: rl1, Forwarder: &fakeForwarder{}, Leases: mgr1, Key: key, EpochID: epoch,
		ReplyTarget: "phone", Inbound: st,
	})
	if _, err := b1.PollOnce(context.Background()); err != nil {
		t.Fatalf("run 1 PollOnce: %v", err)
	}
	mgr1.mu.Lock()
	run1Inputs := len(mgr1.inputs)
	mgr1.mu.Unlock()
	if run1Inputs != 60 {
		t.Fatalf("run 1 routed %d keystrokes, want 60 (the pre-restart stream must be delivered normally)", run1Inputs)
	}

	// RESTART. The relay serves only what it chose to retain: the phone's NEW take_control
	// (seq 1 again -- the regressed send-seq) followed by the two retained keystrokes.
	rl2 := &retainingRelay{}
	rl2.add(100, sealAt(t, key, epoch, 1, protocol.RemoteCommand{DeviceCommandAuth: protocol.DeviceCommandAuth{
		Action: protocol.ActionTakeControl, Session: "m/s1", OperationID: "op-tc-run2", DeviceID: "d1", Sig: "sig-tc2",
	}}))
	rl2.add(101, retained60)
	rl2.add(102, retained61)

	mgr2 := &fakeLeaseRouter{}
	b2 := NewCommandBridge(CommandBridgeConfig{
		Mailbox: rl2, Forwarder: &fakeForwarder{}, Leases: mgr2, Key: key, EpochID: epoch,
		ReplyTarget: "phone", Inbound: st,
	})
	_, err := b2.PollOnce(context.Background())

	mgr2.mu.Lock()
	gotInputs, gotBegins := len(mgr2.inputs), len(mgr2.begins)
	mgr2.mu.Unlock()

	if gotInputs != 0 {
		t.Fatalf("%d retained keystroke(s) reached the lease plane after the restart, want 0: seq 60 is "+
			"gap-dropped but still advances the high-water to 60, so seq 61 is contiguous, gap is false, and "+
			"it routes to the live lease and the PTY (§4.6 trace)", gotInputs)
	}
	if !errors.Is(err, crypto.ErrStaleSeq) {
		t.Fatalf("post-restart PollOnce err = %v, want it to wrap crypto.ErrStaleSeq: with no seeded "+
			"inbound high-water the restarted receiver has seen == false, skips the staleness check, and "+
			"accepts a retained keystroke that is contiguous with the previous retained one", err)
	}
	if gotBegins != 0 {
		t.Fatalf("the seq-regressed phone's take_control opened %d lease(s), want 0: a monotonic seeded "+
			"per-(sender,epoch) high-water necessarily refuses a phone that restarted its send-seq. This "+
			"fail-closed refusal is the intended consequence -- PB-STATE-3/-4 is what stops the phone "+
			"regressing in the first place", gotBegins)
	}
}

// replayClass is one action class of the PB-GW-4 matrix: the plaintext the phone seals,
// and how many times that class was DISPATCHED past the guard.
type replayClass struct {
	name      string
	plaintext any
	dispatch  func(*fakeLeaseRouter, *fakeForwarder, *fakeTerminalWatchRouter) int
}

// TestReplay_RetainedFrameClassesRefusedAfterRestart covers the remaining four action
// classes of PB-GW-4 (the input class is the seq-regressed trace above): take_control,
// take_control_end, an idempotent mutation, and terminal watch/unwatch. Each is delivered
// once pre-restart, then re-served by the retaining relay at a fresh storage cursor after
// the restart, and each asserts at ITS OWN dispatch seam that the replay never gets past
// the guard.
func TestReplay_RetainedFrameClassesRefusedAfterRestart(t *testing.T) {
	classes := []replayClass{
		{
			name: "take_control",
			plaintext: protocol.RemoteCommand{DeviceCommandAuth: protocol.DeviceCommandAuth{
				Action: protocol.ActionTakeControl, Session: "m/s1", OperationID: "op-tc", DeviceID: "d1", Sig: "s",
			}},
			dispatch: func(l *fakeLeaseRouter, _ *fakeForwarder, _ *fakeTerminalWatchRouter) int {
				l.mu.Lock()
				defer l.mu.Unlock()
				return len(l.begins)
			},
		},
		{
			name: "take_control_end",
			plaintext: protocol.RemoteCommand{DeviceCommandAuth: protocol.DeviceCommandAuth{
				Action: protocol.OpTakeControlEnd, Session: "m/s1", OperationID: "op-tce", DeviceID: "d1", Sig: "s",
			}},
			dispatch: func(l *fakeLeaseRouter, _ *fakeForwarder, _ *fakeTerminalWatchRouter) int {
				l.mu.Lock()
				defer l.mu.Unlock()
				return len(l.ends)
			},
		},
		{
			name:      "idempotent_mutation_kill",
			plaintext: killCmd("m/s1", "op-kill"),
			dispatch: func(_ *fakeLeaseRouter, f *fakeForwarder, _ *fakeTerminalWatchRouter) int {
				f.mu.Lock()
				defer f.mu.Unlock()
				return len(f.seen)
			},
		},
		{
			name: "terminal_watch",
			plaintext: protocol.RemoteCommand{DeviceCommandAuth: protocol.DeviceCommandAuth{
				Action: protocol.ActionTerminalWatch, Session: "m/s1",
			}},
			dispatch: func(_ *fakeLeaseRouter, _ *fakeForwarder, w *fakeTerminalWatchRouter) int {
				w.mu.Lock()
				defer w.mu.Unlock()
				return len(w.watched)
			},
		},
		{
			name: "terminal_unwatch",
			plaintext: protocol.RemoteCommand{DeviceCommandAuth: protocol.DeviceCommandAuth{
				Action: protocol.ActionTerminalUnwatch, Session: "m/s1",
			}},
			dispatch: func(_ *fakeLeaseRouter, _ *fakeForwarder, w *fakeTerminalWatchRouter) int {
				w.mu.Lock()
				defer w.mu.Unlock()
				return len(w.unwatched)
			},
		},
	}

	for _, cl := range classes {
		t.Run(cl.name, func(t *testing.T) {
			key := inboundKey(53)
			const epoch uint32 = 7
			st := &memInboundState{}
			env := sealAt(t, key, epoch, 1, cl.plaintext)

			run := func(rl *retainingRelay) (*fakeLeaseRouter, *fakeForwarder, *fakeTerminalWatchRouter, error) {
				leases, fwd, watch := &fakeLeaseRouter{}, &fakeForwarder{}, &fakeTerminalWatchRouter{}
				b := NewCommandBridge(CommandBridgeConfig{
					Mailbox: rl, Forwarder: fwd, Leases: leases, Watchers: watch, Key: key, EpochID: epoch,
					ReplyTarget: "phone", Inbound: st,
				})
				_, err := b.PollOnce(context.Background())
				return leases, fwd, watch, err
			}

			rl1 := &retainingRelay{}
			rl1.add(1, env)
			l1, f1, w1, err := run(rl1)
			if err != nil {
				t.Fatalf("run 1 PollOnce: %v", err)
			}
			if got := cl.dispatch(l1, f1, w1); got != 1 {
				t.Fatalf("run 1 dispatched %d, want 1 (the legitimate frame must be delivered normally)", got)
			}

			// RESTART: the relay never deleted the acked item and re-serves the IDENTICAL
			// sealed envelope at a fresh storage cursor.
			rl2 := &retainingRelay{}
			rl2.add(5, env)
			l2, f2, w2, err := run(rl2)
			if got := cl.dispatch(l2, f2, w2); got != 0 {
				t.Fatalf("the retained %s was dispatched %d time(s) after the restart, want 0: it must be "+
					"refused at the replay guard, not merely be harmless because a downstream mechanism "+
					"(an empty lease map, the daemon's durable operation_id idempotency) happens to absorb it",
					cl.name, got)
			}
			if !errors.Is(err, crypto.ErrStaleSeq) {
				t.Fatalf("post-restart PollOnce err = %v, want it to wrap crypto.ErrStaleSeq: nothing "+
					"persists or seeds the inbound high-water, so the restarted receiver accepts the "+
					"retained frame as the first it has ever seen", err)
			}
		})
	}
}
