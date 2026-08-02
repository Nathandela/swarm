package phonecore

import (
	"encoding/json"
	"errors"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// kindPhoneToMachine is the DIRECTION TAG this package writes into the AEAD-covered plaintext
// of EVERY phone -> machine seal (command.go's five, input.go's two). remotegw carries the
// same string as remotegw.KindPhoneToMachine with the full rationale, and the two are pinned
// against each other by TestDirectionBinding_TheTagIsInThePlaintextAndTheBucketKeyIsUNTOUCHED
// -- they are duplicated because internal/remote/crypto is FROZEN and PB-BIND-0 forbids
// phonecore importing remotegw, the same reason InboundMaxAge is duplicated.
//
// IT IS DELIBERATELY NOT IN THE HEADER. PB-SYNC-1 (amended 2026-07-30, B84/B85) forbids a
// direction tag in SenderKeyID or EpochID because both ARE Bucket, and the reply channel's
// whole identity -- its staleness attribution, its reconcile reply_ceiling authority
// (PB-STATE-4(b)), its durable high-water -- is keyed on sender-zero. The tag rides the
// plaintext instead, so every bucket coordinate is left exactly as it was.
const kindPhoneToMachine = "phone_to_machine"

// ErrWrongDirection refuses an inbound frame the PHONE ITSELF sealed for the machine and the
// relay reflected back. It is refused BEFORE the receiver is touched: a reflected keystroke
// carries a seq thousands ahead of the reply stream (one Sequencer feeds commands and input),
// so letting it advance the high-water silences every machine reply for the epoch -- no lease
// confirmation, no operation outcome, and no repair frame exists for the reply channel.
var ErrWrongDirection = errors.New("phonecore: mailbox frame sealed for the other direction")

// commandFrame is the sealed phone -> machine COMMAND plaintext: the schema.RemoteCommand
// fields (promoted via anonymous embedding, so its pinned json tags stay the single source of
// truth) plus the direction tag. It mirrors the machine's replyFrame exactly.
//
// Every extra RemoteCommand field is omitempty, so a bare-auth command marshals to precisely
// the bytes it always did plus the tag -- the gateway's decoders (inputFrameWire, then
// protocol.RemoteCommand) ignore the extra key, and the device signature covers the canonical
// tuple rather than this JSON, so neither is disturbed.
type commandFrame struct {
	Kind                 string `json:"kind"`
	schema.RemoteCommand        // device_id, action, machine, session, ... (promoted)
}

// inputEnvelope is the sealed phone -> machine INPUT plaintext: the InputFrame fields plus the
// direction tag. InputFrame keeps its own shape (t/s/data/cols/rows) so the machine's
// inputFrameWire decoder is untouched.
type inputEnvelope struct {
	Kind       string `json:"kind"`
	InputFrame        // t, s, data, cols, rows (promoted)
}

// sealPhoneFrame marshals one direction-tagged plaintext and seals it under the epoch content
// key. Every phone -> machine seal funnels through here so the tag cannot be forgotten at one
// site -- which is exactly how the IssuedAt stamp was once missed at one of five (PB-GW-6).
//
// The header is UNTOUCHED beyond what it always carried: no SenderKeyID, no EpochID games.
func sealPhoneFrame(key crypto.ContentKey, epochID uint32, seq uint64, frame any) ([]byte, error) {
	plaintext, err := json.Marshal(frame)
	if err != nil {
		return nil, err
	}
	env, err := crypto.SealMailbox(key, crypto.EnvelopeHeader{
		Version:  crypto.VersionV1,
		EpochID:  epochID,
		Seq:      seq,
		IssuedAt: issuedAt(),
	}, plaintext)
	if err != nil {
		return nil, err
	}
	return env.Marshal(), nil
}
