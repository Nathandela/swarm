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
// dispatches each on its decoded plaintext after a SINGLE Accept:
//
//	seq1 kill            -> Forwarder.ForwardCommand (a mutating op, fresh conn)
//	seq2 take_control    -> LeaseRouter.Begin        (opens the session's lease; focuses it)
//	seq3 input "data"    -> LeaseRouter.Input(m/s1)  (rides the focused session's lease)
//	seq4 input "resize"  -> LeaseRouter.Input(m/s1)
//	seq5 take_control_end -> LeaseRouter.End(m/s1)    (clears the focused session)
//	seq6 input "data"    -> LeaseRouter.Input("")     (no focus after End: dropped/unfocused)
//	seq3 REPLAY          -> dropped: ErrStaleSeq from the single Accept, NEVER reaches Input
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
		{Cursor: 3, Envelope: sealInputEnv(t, key, 3, inputFrameWire{T: "data", Data: []byte("ls -la\r")})},
		{Cursor: 4, Envelope: sealInputEnv(t, key, 4, inputFrameWire{T: "resize", Cols: 100, Rows: 40})},
		{Cursor: 5, Envelope: sealRemoteCmd(t, key, 5, endCtrl)},
		{Cursor: 6, Envelope: sealInputEnv(t, key, 6, inputFrameWire{T: "data", Data: []byte("after-end\r")})},
		{Cursor: 7, Envelope: sealInputEnv(t, key, 3, inputFrameWire{T: "data", Data: []byte("ls -la\r")})}, // REPLAY of seq 3
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

	// The replayed seq surfaces once as ErrStaleSeq (aggregated), the other six process.
	if !errors.Is(err, crypto.ErrStaleSeq) {
		t.Fatalf("PollOnce err = %v, want it to wrap crypto.ErrStaleSeq (the replayed input seq)", err)
	}
	if n != 6 {
		t.Fatalf("processed %d, want 6 (kill, take_control, 2 inputs, take_control_end, 1 input; replay dropped)", n)
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

	// The replay was dropped at Accept: exactly THREE inputs reached the lease plane.
	if len(mgr.inputs) != 3 {
		t.Fatalf("Input calls = %d, want 3 (the replayed seq must be dropped before Input)", len(mgr.inputs))
	}
	// input data + resize routed to the FOCUSED session (take_control's m/s1).
	if mgr.inputs[0].session != "m/s1" || mgr.inputs[0].frame.Kind != "data" || !bytes.Equal(mgr.inputs[0].frame.Data, []byte("ls -la\r")) {
		t.Fatalf("input[0] = %+v, want data 'ls -la\\r' on m/s1", mgr.inputs[0])
	}
	if mgr.inputs[1].session != "m/s1" || mgr.inputs[1].frame.Kind != "resize" || mgr.inputs[1].frame.Cols != 100 || mgr.inputs[1].frame.Rows != 40 {
		t.Fatalf("input[1] = %+v, want resize 100x40 on m/s1", mgr.inputs[1])
	}
	// After take_control_end cleared the focus, the next input routes to "" (unfocused).
	if mgr.inputs[2].session != "" || mgr.inputs[2].frame.Kind != "data" {
		t.Fatalf("input[2] = %+v, want data on the cleared (empty) session after End", mgr.inputs[2])
	}
}
