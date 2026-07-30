package remotegw

import (
	"encoding/json"
	"errors"
)

// KindPhoneToMachine is the DIRECTION TAG every phone -> machine seal writes into its
// AEAD-covered plaintext, alongside the frame families the machine already tags
// (kindCommandReply here, terminal_snapshot / reconcile / journal_reseed / epoch_grant /
// push in phonecore). phonecore stamps it; this side refuses anything carrying it back.
//
// WHAT IT FIXES. The two legs share ONE epoch ContentKey and BOTH use the sender-zero seq
// bucket, so nothing authenticated said which leg a frame was sealed for. The relay sees both
// legs (ADR-007 D9), so it could re-serve a frame it merely OBSERVED on one leg onto the
// other: same key, so the AEAD verified; fresh, so the bounded-age check passed; and
// crypto.MailboxReceiver ADVANCED ITS HIGH-WATER before anything downstream noticed the frame
// made no sense. Every later frame from the real peer was then crypto.ErrStaleSeq. One
// observed reply silenced the phone's whole command stream for the epoch; one observed
// keystroke silenced every machine reply, because keystrokes draw from the phone's command
// Sequencer and therefore run thousands of seqs ahead of the reply stream -- and the reply
// channel is the one channel with NO repair frame (PB-SYNC-2), so nothing recovers it.
//
// WHY THE PLAINTEXT AND NOT THE HEADER. PB-SYNC-1 (amended 2026-07-30, B84/B85) forbids a
// direction tag in SenderKeyID or EpochID: both ARE Bucket, so a value in either forks every
// bucket per direction or collides two streams into one. The collision was measured -- reply
// high-water 2 -> 40, the colliding frame's content dropped while the frame was still acked,
// staleness attributed to the wrong channels -- and since command replies carry the lease
// confirmation that PB-INPUT-2 gates keystrokes on, it costs TYPING for the life of the
// epoch, recoverable only by re-pairing. The requirement names the remedy: tag inside the
// AEAD-covered plaintext, or a header field outside Bucket. There is no free header field --
// the AAD covers {Version, Type, EpochID, Seq, SenderKeyID, IssuedAt}; Version and Type are
// validated to fixed values by FROZEN parse/seal code, EpochID and SenderKeyID are Bucket,
// Seq is the replay counter itself and is durably persisted on both sides, IssuedAt is
// compared against the wall clock, and RecipientKeyID is the one spare field precisely
// BECAUSE it is excluded from the AAD (A5 fan-out), so it can bind nothing. The plaintext it
// is.
//
// THE TAG IS AUTHENTICATED, which is the property that matters: it rides inside the AEAD, so
// the relay can neither add it, strip it, nor alter it without failing the tag. It is
// stronger than an AAD field, not weaker -- it is authenticated AND confidential, so unlike a
// cleartext header byte it tells the relay nothing at all.
//
// IT MUST BE READ BEFORE crypto.MailboxReceiver.Accept, never after. Accept decrypts and
// advances the high-water in ONE call and only then hands the caller the plaintext, so a
// direction check on Accept's own result is a correct inner check behind an outer scan that
// has already committed -- the exact structure of the defect. Both openers therefore
// crypto.OpenMailbox first (which touches no receiver, the same trick the bounded-age check
// already uses), inspect the tag, and only then Accept.
const KindPhoneToMachine = "phone_to_machine"

// ErrWrongDirection refuses a mailbox frame whose authenticated direction tag says it was
// sealed for the OTHER leg -- the relay reflecting a frame it observed. Refused BEFORE the
// receiver is touched, because the damage of a reflection was never the acceptance (the
// plaintext fails to route) but the seq high-water it advanced on its way in.
var ErrWrongDirection = errors.New("remotegw: mailbox frame sealed for the other direction")

// refuseForeignDirection reads the direction tag off an AUTHENTICATED plaintext and refuses a
// frame sealed for the machine -> phone leg. The caller must have opened the envelope already
// and must NOT yet have called Accept.
//
// A plaintext that does not decode as JSON is passed through untouched: it is not this
// check's business to reject it, and the caller's own decode reports it with a better error.
func refuseForeignDirection(plain []byte) error {
	var disc struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(plain, &disc); err != nil {
		return nil
	}
	if disc.Kind != "" && disc.Kind != KindPhoneToMachine {
		return ErrWrongDirection
	}
	return nil
}
