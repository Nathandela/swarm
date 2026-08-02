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
	// AUTHENTICATE FIRST, and only then judge the frame. crypto.OpenMailbox touches NO
	// receiver, so everything between here and Accept below costs no seq and can refuse
	// freely -- which is the whole point, since a refusal that has already advanced the
	// high-water is not a refusal. A forgery is refused HERE, as a forgery, before any
	// verdict about age or direction is reached; those verdicts are only meaningful about a
	// frame the AEAD has vouched for.
	//
	// This open is what the direction tag costs: Accept below decrypts the same envelope a
	// second time (MailboxReceiver exposes no pre-advance hook and crypto is FROZEN). One
	// extra XChaCha20-Poly1305 open of a small frame, against §6.0's 150 ms typing budget.
	plain, err := crypto.OpenMailbox(key, env)
	if err != nil {
		return MailboxFrame{}, err
	}
	// PB-GW-2's bounded-age backstop, applied BEFORE Accept so a refusal advances no
	// high-water: otherwise one retained frame carrying an absurd seq would silence the
	// real phone for good. The bound is ONE-SIDED -- IssuedAt is AAD-covered, so a relay
	// can only make frames older, never newer, and bounding the future would refuse a
	// live phone whose clock runs fast.
	if now.Sub(time.UnixMilli(env.Header.IssuedAt)) > InboundMaxAge {
		return MailboxFrame{}, crypto.ErrStaleAge
	}
	// DIRECTION (direction.go), also before Accept. This is the relay re-serving a frame it
	// observed on the machine -> phone leg onto this one: it authenticates under the shared
	// content key and its plaintext then fails to route, but the receiver would already have
	// advanced the phone's command high-water and every real command after it would be
	// ErrStaleSeq.
	//
	// The rule is "an explicit kind that is not the phone's", and it is COMPLETE for the one
	// bucket that can actually be poisoned. Only sender-zero frames share the phone's bucket,
	// and the ONLY machine -> phone frames sealed sender-zero are command replies, which carry
	// kind:"command_reply" (every other machine frame stamps the machine key id and therefore
	// lands in a different bucket entirely). Stating it as "any foreign kind" rather than
	// naming command_reply also covers a future machine kind that forgets to stamp the sender,
	// without duplicating phonecore's kind constants across the package boundary.
	//
	// A kind-LESS plaintext is deliberately still accepted: that is the shape every hand-rolled
	// fixture and every pre-tag phone seal produces, and it cannot be a reflected machine frame
	// for the reason above.
	if err := refuseForeignDirection(plain); err != nil {
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
