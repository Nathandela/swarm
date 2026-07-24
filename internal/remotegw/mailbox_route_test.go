// FAILING-FIRST (TDD RED, GG-5) test for A7 input Slice 5: the gateway command
// loop's mailbox ROUTER. Commands and input frames ride ONE (sender, epoch)
// mailbox stream under a single content key, so the router MUST Accept each
// envelope EXACTLY ONCE (advancing the shared seq high-water a single step) and
// then dispatch on the decoded plaintext -- never try one opener then fall back to
// the other (that double-Accepts and spuriously reports ErrStaleSeq).
//
// THE CONTRACT this test freezes (undefined symbols -> compile-fail RED):
//   - func OpenMailboxFrame(recv, key, raw) (MailboxFrame, error): parse -> Accept
//     ONCE -> peek `t`; "data"/"resize" => an input frame, else a RemoteCommand.
//   - type LeaseRouter interface{ Begin(protocol.RemoteCommand) error;
//     Input(string, InputFrame) error; End(string) } -- the seam the CommandBridge
//     routes take_control + input frames through (fakeable; *LeaseManager satisfies it).
//   - CommandBridgeConfig gains a Leases LeaseRouter field.
//   - CommandBridge routes: kill -> Forwarder.ForwardCommand; take_control ->
//     Leases.Begin (and records the focused session); an input frame ->
//     Leases.Input(focused session, frame); take_control_end -> Leases.End (and
//     clears the focused session); a replayed input seq is dropped ONCE (ErrStaleSeq)
//     and never reaches Input.
package remotegw

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// routedInput records one frame the router handed to the lease plane, with the
// session it was routed to (to prove focused-session tracking).
type routedInput struct {
	session string
	frame   InputFrame
}

// fakeLeaseRouter is an in-memory LeaseRouter: it records Begin/Input/End so the
// routing is unit-tested without a live daemon lease conn.
type fakeLeaseRouter struct {
	mu     sync.Mutex
	begins []protocol.RemoteCommand
	inputs []routedInput
	ends   []string
}

func (f *fakeLeaseRouter) Begin(cmd protocol.RemoteCommand) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.begins = append(f.begins, cmd)
	return nil
}

func (f *fakeLeaseRouter) Input(session string, fr InputFrame) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inputs = append(f.inputs, routedInput{session: session, frame: fr})
	return nil
}

func (f *fakeLeaseRouter) End(session string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ends = append(f.ends, session)
}

// sealRemoteCmd seals a full RemoteCommand (take_control/take_control_end carry an
// Action but no LaunchReq) under the epoch content key, mirroring sealedCmd's bare
// DeviceCommandAuth seal.
func sealRemoteCmd(t *testing.T, key crypto.ContentKey, seq uint64, rc protocol.RemoteCommand) []byte {
	t.Helper()
	plain, err := json.Marshal(rc)
	if err != nil {
		t.Fatalf("marshal remote command: %v", err)
	}
	env, err := crypto.SealMailbox(key, crypto.EnvelopeHeader{Version: crypto.VersionV1, EpochID: 1, Seq: seq}, plain)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	return env.Marshal()
}

// sealInputEnv seals an input-frame plaintext ({t,data,cols,rows}) directly under
// the epoch content key -- the same wire shape phonecore.SealInputData/Resize emit,
// hand-rolled here because remotegw must not import phonecore (import cycle).
func sealInputEnv(t *testing.T, key crypto.ContentKey, seq uint64, w inputFrameWire) []byte {
	t.Helper()
	plain, err := json.Marshal(w)
	if err != nil {
		t.Fatalf("marshal input frame: %v", err)
	}
	env, err := crypto.SealMailbox(key, crypto.EnvelopeHeader{Version: crypto.VersionV1, EpochID: 1, Seq: seq}, plain)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	return env.Marshal()
}

// TestCommandBridge_RoutesInputVsCommand feeds ONE seq-ordered mailbox stream with
// interleaved sealed frames drawn from a single seq allocator and proves the router
// dispatches each on its decoded plaintext after a SINGLE Accept, routing every input
// by the target session id sealed INSIDE the frame:
//
//	seq1 kill             -> Forwarder.ForwardCommand (a mutating op, fresh conn)
//	seq2 take_control     -> LeaseRouter.Begin        (opens the session's lease)
//	seq3 input "data"     -> LeaseRouter.Input(m/s1)  (routed by the frame's session id)
//	seq4 input "resize"   -> LeaseRouter.Input(m/s1)
//	seq5 take_control_end  -> LeaseRouter.End(m/s1)
//	seq3 REPLAY           -> dropped: ErrStaleSeq from the single Accept, NEVER reaches Input
func TestCommandBridge_RoutesInputVsCommand(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 7)
	}

	takeCtrl := protocol.RemoteCommand{DeviceCommandAuth: protocol.DeviceCommandAuth{
		Action: protocol.ActionTakeControl, Session: "m/s1", OperationID: "op-tc", DeviceID: "d1", Sig: "sig-tc",
	}}
	endCtrl := protocol.RemoteCommand{DeviceCommandAuth: protocol.DeviceCommandAuth{
		Action: protocol.OpTakeControlEnd, Session: "m/s1", OperationID: "op-tce", DeviceID: "d1", Sig: "sig-tce",
	}}

	mb := &fakeMailbox{inbox: []relay.Item{
		{Cursor: 1, Envelope: sealedCmd(t, key, 1, protocol.DeviceCommandAuth{Action: protocol.ActionKill, Session: "m/s1", OperationID: "op-kill", DeviceID: "d1", Sig: "sig-k"})},
		{Cursor: 2, Envelope: sealRemoteCmd(t, key, 2, takeCtrl)},
		{Cursor: 3, Envelope: sealInputEnv(t, key, 3, inputFrameWire{T: "data", Session: "m/s1", Data: []byte("ls -la\r")})},
		{Cursor: 4, Envelope: sealInputEnv(t, key, 4, inputFrameWire{T: "resize", Session: "m/s1", Cols: 100, Rows: 40})},
		{Cursor: 5, Envelope: sealRemoteCmd(t, key, 5, endCtrl)},
		{Cursor: 6, Envelope: sealInputEnv(t, key, 3, inputFrameWire{T: "data", Session: "m/s1", Data: []byte("ls -la\r")})}, // REPLAY of seq 3
	}}

	fwd := &fakeForwarder{}
	mgr := &fakeLeaseRouter{}
	b := NewCommandBridge(CommandBridgeConfig{
		Mailbox:     mb,
		Forwarder:   fwd,
		Leases:      mgr,
		Key:         key,
		EpochID:     1,
		ReplyTarget: "phone",
	})

	n, err := b.PollOnce(context.Background())

	// The replayed seq surfaces once as ErrStaleSeq (aggregated), the other five process.
	if !errors.Is(err, crypto.ErrStaleSeq) {
		t.Fatalf("PollOnce err = %v, want it to wrap crypto.ErrStaleSeq (the replayed input seq)", err)
	}
	if n != 5 {
		t.Fatalf("processed %d, want 5 (kill, take_control, 2 inputs, take_control_end; replay dropped)", n)
	}

	// kill was forwarded; take_control / take_control_end / input were NOT.
	if len(fwd.ops) != 1 || fwd.ops[0] != protocol.OpKill {
		t.Fatalf("forwarded ops = %v, want [kill] only (take_control must NOT be forwarded)", fwd.ops)
	}

	// take_control -> Begin with the full command.
	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	if len(mgr.begins) != 1 {
		t.Fatalf("Begin calls = %d, want 1", len(mgr.begins))
	}
	if mgr.begins[0].Action != protocol.ActionTakeControl || mgr.begins[0].Session != "m/s1" {
		t.Fatalf("Begin cmd = %+v, want take_control on m/s1", mgr.begins[0])
	}

	// take_control_end -> End(m/s1).
	if len(mgr.ends) != 1 || mgr.ends[0] != "m/s1" {
		t.Fatalf("End calls = %v, want [m/s1]", mgr.ends)
	}

	// The replay was dropped at Accept: exactly TWO inputs reached the lease plane, each
	// routed by the session id sealed into its own frame (m/s1).
	if len(mgr.inputs) != 2 {
		t.Fatalf("Input calls = %d, want 2 (the replayed seq must be dropped before Input)", len(mgr.inputs))
	}
	if mgr.inputs[0].session != "m/s1" || mgr.inputs[0].frame.Kind != "data" || !bytes.Equal(mgr.inputs[0].frame.Data, []byte("ls -la\r")) {
		t.Fatalf("input[0] = %+v, want data 'ls -la\\r' on m/s1", mgr.inputs[0])
	}
	if mgr.inputs[1].session != "m/s1" || mgr.inputs[1].frame.Kind != "resize" || mgr.inputs[1].frame.Cols != 100 || mgr.inputs[1].frame.Rows != 40 {
		t.Fatalf("input[1] = %+v, want resize 100x40 on m/s1", mgr.inputs[1])
	}
}

// TestCommandBridge_DroppedTakeControlDoesNotMisrouteInput is the A7 finding-A
// adversarial reproduction: the relay (the adversary) can DROP a sealed frame it
// cannot forge or alter. The phone controls session A, then take_control(B); the relay
// DROPS the take_control(B) envelope and delivers B's next keystroke. Under the old
// focus-tracking router (route by mutable currentSession), the still-focused A absorbs
// B's keystroke onto A's LIVE lease -- a cross-session misroute the daemon's per-lease
// gate cannot catch (the frame legitimately matches A's lease).
//
// The fix binds the target session id INSIDE the sealed frame and routes by it, and
// honors the receiver's gap bit: B's keystroke names B (found no lease -> never touches
// A) AND arrives after a mailbox gap (the dropped take_control skipped a seq), so it is
// dropped outright. Either way it MUST NOT ride A's lease.
//
//	seq1 take_control(A)          -> Begin(A)
//	seq2 input(session=A, "keyA") -> Input(A)            (contiguous: no gap)
//	seq3 take_control(B)          -> DROPPED by the relay (never appended)
//	seq4 input(session=B, "keyB") -> Gap (seq 3 skipped): DROPPED, never routed to A
func TestCommandBridge_DroppedTakeControlDoesNotMisrouteInput(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 7)
	}

	takeA := protocol.RemoteCommand{DeviceCommandAuth: protocol.DeviceCommandAuth{
		Action: protocol.ActionTakeControl, Session: "m/sA", OperationID: "op-tcA", DeviceID: "d1", Sig: "sig-tcA",
	}}

	// The take_control(B) at seq 3 is DROPPED by the adversarial relay: it is never
	// appended, so the mailbox jumps from seq 2 straight to B's keystroke at seq 4.
	mb := &fakeMailbox{inbox: []relay.Item{
		{Cursor: 1, Envelope: sealRemoteCmd(t, key, 1, takeA)},
		{Cursor: 2, Envelope: sealInputEnv(t, key, 2, inputFrameWire{T: "data", Session: "m/sA", Data: []byte("keyA\r")})},
		{Cursor: 3, Envelope: sealInputEnv(t, key, 4, inputFrameWire{T: "data", Session: "m/sB", Data: []byte("keyB\r")})},
	}}

	fwd := &fakeForwarder{}
	mgr := &fakeLeaseRouter{}
	b := NewCommandBridge(CommandBridgeConfig{
		Mailbox:     mb,
		Forwarder:   fwd,
		Leases:      mgr,
		Key:         key,
		EpochID:     1,
		ReplyTarget: "phone",
	})

	n, err := b.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("PollOnce err = %v, want nil (a gap is not an error; the gapped input is dropped, not failed)", err)
	}
	if n != 3 {
		t.Fatalf("processed %d, want 3 (take_control, input A, input B; B dropped-but-processed)", n)
	}

	mgr.mu.Lock()
	defer mgr.mu.Unlock()

	// B's take_control was dropped, so only A ever opened a lease.
	if len(mgr.begins) != 1 || mgr.begins[0].Session != "m/sA" {
		t.Fatalf("Begin calls = %+v, want exactly [take_control m/sA] (B's take_control was dropped)", mgr.begins)
	}

	// THE SECURITY ASSERTION: B's keystroke must NEVER reach the lease plane. It names a
	// session (B) whose take_control was dropped AND it follows a mailbox gap, so it is
	// dropped -- exactly ONE input (A's own keystroke) rode a lease. Under the old
	// currentSession router this is TWO, the second being B's "keyB" misrouted onto A.
	if len(mgr.inputs) != 1 {
		t.Fatalf("Input calls = %d, want 1: the gapped input for B (its take_control dropped) must be dropped, not misrouted onto A", len(mgr.inputs))
	}
	if mgr.inputs[0].session != "m/sA" || !bytes.Equal(mgr.inputs[0].frame.Data, []byte("keyA\r")) {
		t.Fatalf("sole input = %+v, want A's own keystroke 'keyA\\r' on m/sA", mgr.inputs[0])
	}
	for _, in := range mgr.inputs {
		if bytes.Equal(in.frame.Data, []byte("keyB\r")) {
			t.Fatalf("B's keystroke reached the lease plane (routed to %q); a dropped take_control(B) must never let B's input ride any lease", in.session)
		}
	}
}

// TestCommandBridge_RoutesInputByEmbeddedSessionNotFocus proves the session-binding
// defense independently of any gap: two sessions hold coexisting leases, and an input
// routes to the session named INSIDE its own frame -- NOT to whichever session was
// taken-control most recently. Under the old router the last take_control (B) is the
// focus, so an input for A would misroute onto B even with a perfectly contiguous seq
// stream (no gap to catch it).
//
//	seq1 take_control(A)          -> Begin(A)
//	seq2 take_control(B)          -> Begin(B)   (both leases coexist)
//	seq3 input(session=A, "toA")  -> Input(A)   (contiguous: no gap)
//	seq4 input(session=B, "toB")  -> Input(B)
func TestCommandBridge_RoutesInputByEmbeddedSessionNotFocus(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 7)
	}

	takeA := protocol.RemoteCommand{DeviceCommandAuth: protocol.DeviceCommandAuth{
		Action: protocol.ActionTakeControl, Session: "m/sA", OperationID: "op-tcA", DeviceID: "d1", Sig: "sig-tcA",
	}}
	takeB := protocol.RemoteCommand{DeviceCommandAuth: protocol.DeviceCommandAuth{
		Action: protocol.ActionTakeControl, Session: "m/sB", OperationID: "op-tcB", DeviceID: "d1", Sig: "sig-tcB",
	}}

	mb := &fakeMailbox{inbox: []relay.Item{
		{Cursor: 1, Envelope: sealRemoteCmd(t, key, 1, takeA)},
		{Cursor: 2, Envelope: sealRemoteCmd(t, key, 2, takeB)},
		{Cursor: 3, Envelope: sealInputEnv(t, key, 3, inputFrameWire{T: "data", Session: "m/sA", Data: []byte("toA\r")})},
		{Cursor: 4, Envelope: sealInputEnv(t, key, 4, inputFrameWire{T: "data", Session: "m/sB", Data: []byte("toB\r")})},
	}}

	fwd := &fakeForwarder{}
	mgr := &fakeLeaseRouter{}
	b := NewCommandBridge(CommandBridgeConfig{
		Mailbox:     mb,
		Forwarder:   fwd,
		Leases:      mgr,
		Key:         key,
		EpochID:     1,
		ReplyTarget: "phone",
	})

	n, err := b.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("PollOnce err = %v, want nil", err)
	}
	if n != 4 {
		t.Fatalf("processed %d, want 4", n)
	}

	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	if len(mgr.inputs) != 2 {
		t.Fatalf("Input calls = %d, want 2", len(mgr.inputs))
	}
	// Each input routes to the session sealed into ITS frame, not the last-focused (B).
	if mgr.inputs[0].session != "m/sA" || !bytes.Equal(mgr.inputs[0].frame.Data, []byte("toA\r")) {
		t.Fatalf("input[0] = %+v, want 'toA\\r' on m/sA -- routing must follow the sealed session id, not the most-recent take_control (m/sB)", mgr.inputs[0])
	}
	if mgr.inputs[1].session != "m/sB" || !bytes.Equal(mgr.inputs[1].frame.Data, []byte("toB\r")) {
		t.Fatalf("input[1] = %+v, want 'toB\\r' on m/sB", mgr.inputs[1])
	}
}

// TestCommandBridge_DropsInputWithNoSession proves an input frame that names no target
// session is dropped, never routed onto whatever session happens to hold a lease.
//
//	seq1 take_control(A)        -> Begin(A)
//	seq2 input(session="", ...) -> DROPPED (empty target)
func TestCommandBridge_DropsInputWithNoSession(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 7)
	}

	takeA := protocol.RemoteCommand{DeviceCommandAuth: protocol.DeviceCommandAuth{
		Action: protocol.ActionTakeControl, Session: "m/sA", OperationID: "op-tcA", DeviceID: "d1", Sig: "sig-tcA",
	}}

	mb := &fakeMailbox{inbox: []relay.Item{
		{Cursor: 1, Envelope: sealRemoteCmd(t, key, 1, takeA)},
		{Cursor: 2, Envelope: sealInputEnv(t, key, 2, inputFrameWire{T: "data", Data: []byte("orphan\r")})}, // no Session
	}}

	fwd := &fakeForwarder{}
	mgr := &fakeLeaseRouter{}
	b := NewCommandBridge(CommandBridgeConfig{
		Mailbox:     mb,
		Forwarder:   fwd,
		Leases:      mgr,
		Key:         key,
		EpochID:     1,
		ReplyTarget: "phone",
	})

	if _, err := b.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce err = %v, want nil", err)
	}

	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	if len(mgr.inputs) != 0 {
		t.Fatalf("Input calls = %d, want 0: an input naming no session must be dropped, not routed onto the leased session", len(mgr.inputs))
	}
}
