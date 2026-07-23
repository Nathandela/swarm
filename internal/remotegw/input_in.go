package remotegw

import (
	"encoding/json"
	"errors"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// ErrNotInputFrame rejects a well-formed, in-sequence mailbox envelope whose
// plaintext is not an input frame (its `t` is neither "data" nor "resize") -- a
// command, for instance. Slice 5's router uses it to tell input from commands.
var ErrNotInputFrame = errors.New("remotegw: envelope is not an input frame")

// InputFrame is the opened phone -> machine input event: Kind is "data" (Data
// carries the keystroke bytes) or "resize" (Cols/Rows carry the new terminal
// size). It is defined here rather than imported from phonecore: remotegw must
// not import phonecore (phonecore's tests import remotegw, so the reverse edge
// would cycle), and protocol is off-limits this slice, so the small wire shape
// is duplicated the way command_in.go keeps its opener local.
type InputFrame struct {
	Kind string
	Data []byte
	Cols int
	Rows int
}

// inputFrameWire is the phone -> machine input-frame wire shape; it mirrors
// phonecore.InputFrame's JSON tags so the two packages agree without a shared
// type. RemoteCommand carries no `t`, so decoding a command here yields Kind ""
// and OpenInputFrame reports ErrNotInputFrame.
type inputFrameWire struct {
	T    string `json:"t"`
	Data []byte `json:"data,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

// OpenInputFrame opens an input-frame envelope the phone sealed under the epoch
// content key, seq-gated through a crypto.MailboxReceiver so a replayed or
// reordered seq for the (sender, epoch) stream is rejected with
// crypto.ErrStaleSeq -- the SAME discipline OpenRemoteCommandGuarded gives
// commands, and through the SAME receiver, because input and commands share one
// seq space. Fail-closed: a malformed/wrong-key envelope, a stale seq, or a
// non-input-frame plaintext (ErrNotInputFrame) returns an error and no frame.
func OpenInputFrame(recv *crypto.MailboxReceiver, key crypto.ContentKey, raw []byte) (InputFrame, error) {
	env, err := crypto.ParseEnvelope(raw)
	if err != nil {
		return InputFrame{}, err
	}
	res, err := recv.Accept(key, env)
	if err != nil {
		return InputFrame{}, err
	}
	var w inputFrameWire
	if err := json.Unmarshal(res.Plaintext, &w); err != nil {
		return InputFrame{}, err
	}
	if w.T != "data" && w.T != "resize" {
		return InputFrame{}, ErrNotInputFrame
	}
	return InputFrame{Kind: w.T, Data: w.Data, Cols: w.Cols, Rows: w.Rows}, nil
}
