package remotegw

import (
	"encoding/json"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// FrameKind discriminates the two plaintext shapes a phone seals into the ONE
// (sender, epoch) phone -> machine mailbox stream: a RemoteCommand or an InputFrame.
type FrameKind int

const (
	// FrameCommand is an opened RemoteCommand (kill/delete/launch/take_control/
	// take_control_end). It carries no `t`.
	FrameCommand FrameKind = iota
	// FrameInput is an opened input frame (`t` = "data" or "resize").
	FrameInput
)

// MailboxFrame is one opened phone -> machine mailbox envelope. Exactly one of
// Command / Input is populated, per Kind. Gap is the receiver's gap bit for this
// envelope: it is set when a preceding mailbox seq was skipped (the relay dropped
// a frame). The router honors it for input -- a keystroke that follows a gap is
// dropped, since the lost frame may have been the target's take_control and the
// routing state is therefore uncertain (A7 defense in depth).
type MailboxFrame struct {
	Kind    FrameKind
	Command protocol.RemoteCommand
	Input   InputFrame
	Gap     bool
}

// OpenMailboxFrame opens ONE mailbox envelope through the shared MailboxReceiver,
// calling recv.Accept EXACTLY ONCE so the shared (sender, epoch) seq high-water
// advances a single step, then dispatches on the decoded plaintext's `t`
// discriminator: "data"/"resize" yields an input frame, anything else a
// RemoteCommand (RemoteCommand carries no `t`). This REPLACES the double-Accept of
// trying OpenRemoteCommandGuarded then OpenInputFrame (each of which Accepts) on the
// same envelope -- that advanced the seq twice and spuriously reported ErrStaleSeq.
//
// Fail-closed: a malformed/wrong-key envelope, a replayed/reordered seq
// (crypto.ErrStaleSeq), or an undecodable plaintext returns an error and no frame.
// A replayed seq is rejected here, ONCE, and never reaches the router.
func OpenMailboxFrame(recv *crypto.MailboxReceiver, key crypto.ContentKey, raw []byte) (MailboxFrame, error) {
	env, err := crypto.ParseEnvelope(raw)
	if err != nil {
		return MailboxFrame{}, err
	}
	res, err := recv.Accept(key, env)
	if err != nil {
		return MailboxFrame{}, err
	}
	// Peek the discriminator by decoding into the input-frame shape: an input frame
	// has `t` "data"/"resize" (and its data/cols/rows land here in the same pass); a
	// RemoteCommand has no `t`, so w.T is "" and we re-decode as a command. Only ONE
	// Accept (one decrypt) has run; the second Unmarshal is over the same plaintext.
	var w inputFrameWire
	if err := json.Unmarshal(res.Plaintext, &w); err != nil {
		return MailboxFrame{}, err
	}
	if w.T == "data" || w.T == "resize" {
		return MailboxFrame{Kind: FrameInput, Input: InputFrame{Kind: w.T, Session: w.Session, Data: w.Data, Cols: w.Cols, Rows: w.Rows}, Gap: res.Gap}, nil
	}
	var rc protocol.RemoteCommand
	if err := json.Unmarshal(res.Plaintext, &rc); err != nil {
		return MailboxFrame{}, err
	}
	return MailboxFrame{Kind: FrameCommand, Command: rc, Gap: res.Gap}, nil
}
