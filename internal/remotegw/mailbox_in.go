package remotegw

import (
	"encoding/json"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// InboundMaxAge bounds the authenticated age of a phone -> machine sealed frame
// (PB-GW-2, value from §6.0). It is a BACKSTOP behind the per-(sender, epoch) replay
// high-water, not a replacement for it: a restart that loses the high-water leaves a
// fresh receiver with seen == false, which skips the staleness check entirely
// (crypto/envelope.go), so a relay that retained frames would otherwise have them
// re-accepted at the guard. Ten minutes sits well above the 60 s command TTL, §6.0's
// ±30 s clock-skew budget and any plausible relay delivery delay, and well below the
// 7 d retention cap.
const InboundMaxAge = 10 * time.Minute

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
	// Stream and Seq are the envelope's (sender, epoch) stream identity and sequence
	// number -- the coordinate the DURABLE inbound high-water is keyed by (PB-GW-1). They
	// are set only on a frame that passed the replay guard, so recording them is exactly
	// "this seq has been consumed on this stream". Both come from the authenticated
	// header: a relay that rewrites either cannot also produce a valid AEAD tag.
	Stream InboundStream
	Seq    uint64
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
// (crypto.ErrStaleSeq), an authenticated issued_at older than InboundMaxAge
// (crypto.ErrStaleAge), or an undecodable plaintext returns an error and no frame.
// A replayed seq is rejected here, ONCE, and never reaches the router.
func OpenMailboxFrame(recv *crypto.MailboxReceiver, key crypto.ContentKey, raw []byte) (MailboxFrame, error) {
	return OpenMailboxFrameAt(recv, key, raw, time.Now())
}

// OpenMailboxFrameAt is OpenMailboxFrame with the age clock injected, so the bound's
// boundary is testable without waiting ten minutes. Production reads through
// OpenMailboxFrame, which passes time.Now().
//
// The age check lives HERE rather than on the receiver because crypto is frozen:
// MailboxReceiver.maxAge has no setter and NewMailboxReceiver takes no arguments, so
// PB-GW-2's property is enforced at this seam instead of that field.
func OpenMailboxFrameAt(recv *crypto.MailboxReceiver, key crypto.ContentKey, raw []byte, now time.Time) (MailboxFrame, error) {
	env, err := crypto.ParseEnvelope(raw)
	if err != nil {
		return MailboxFrame{}, err
	}
	// PB-GW-2's bounded-age backstop, applied BEFORE Accept so a refusal advances no
	// high-water: otherwise one retained frame carrying an absurd seq would silence the
	// real phone for good. The bound is ONE-SIDED -- IssuedAt is AAD-covered, so a relay
	// can only make frames older, never newer, and bounding the future would refuse a
	// live phone whose clock runs fast.
	//
	// A stale CLAIM is the untrusted relay's until the AEAD vouches for it, so refusing
	// for age happens only after crypto.OpenMailbox authenticates the header -- a forgery
	// is refused as a forgery. OpenMailbox does not touch the receiver, so this costs no
	// seq. A fresh-looking claim needs no separate open: Accept below authenticates it,
	// and a header a relay shifted forward fails the tag there.
	if now.Sub(time.UnixMilli(env.Header.IssuedAt)) > InboundMaxAge {
		if _, err := crypto.OpenMailbox(key, env); err != nil {
			return MailboxFrame{}, err
		}
		return MailboxFrame{}, crypto.ErrStaleAge
	}
	res, err := recv.Accept(key, env)
	if err != nil {
		return MailboxFrame{}, err
	}
	// Peek the discriminator by decoding into the input-frame shape: an input frame
	// has `t` "data"/"resize" (and its data/cols/rows land here in the same pass); a
	// RemoteCommand has no `t`, so w.T is "" and we re-decode as a command. Only ONE
	// Accept (one decrypt) has run; the second Unmarshal is over the same plaintext.
	stream := InboundStream{Sender: env.Header.SenderKeyID, Epoch: env.Header.EpochID}
	var w inputFrameWire
	if err := json.Unmarshal(res.Plaintext, &w); err != nil {
		return MailboxFrame{}, err
	}
	if w.T == "data" || w.T == "resize" {
		return MailboxFrame{
			Kind:  FrameInput,
			Input: InputFrame{Kind: w.T, Session: w.Session, Data: w.Data, Cols: w.Cols, Rows: w.Rows},
			Gap:   res.Gap, Stream: stream, Seq: env.Header.Seq,
		}, nil
	}
	var rc protocol.RemoteCommand
	if err := json.Unmarshal(res.Plaintext, &rc); err != nil {
		return MailboxFrame{}, err
	}
	return MailboxFrame{Kind: FrameCommand, Command: rc, Gap: res.Gap, Stream: stream, Seq: env.Header.Seq}, nil
}
