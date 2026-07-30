package phonecore

// FAILING-FIRST (TDD RED, GG-5) test for the DIRECTION BINDING of the shared mailbox.
//
// Nothing authenticated bound which LEG a frame was sealed for. Both legs ride ONE epoch
// ContentKey, and the machine's command replies and EVERY phone -> machine seal both land in
// the sender-zero bucket -- deliberately, because crypto.MailboxReceiver buckets its replay
// high-water strictly by (SenderKeyID, EpochID) and PB-SYNC-1 keys the reply channel on that
// exact discriminator. So the sender-zero bucket is the SAME bucket on both legs.
//
// The relay is the declared adversary (ADR-007 D9) and it sees both legs, so it could
// re-serve a frame it merely OBSERVED on one leg onto the other: the AEAD verifies (same
// key), the bounded-age check passes (the frame is fresh), the receiver ADVANCED ITS
// HIGH-WATER, and everything the real peer sent after that was ErrStaleSeq.
//
// The frame IS rejected downstream -- a reply decodes as a kind-less journal record, a
// keystroke as an actionless command -- but the rejection happened AFTER the receiver had
// already committed. The damage is the ADVANCE, not the acceptance, so each arm below proves
// the receiver's cursor is UNTOUCHED by feeding the real peer's next frame through the SAME
// receiver afterwards. A test that only asserted "the reflection was refused" would pass
// against a receiver that had eaten the whole epoch.
//
// THE TAG MAY NOT LIVE IN THE HEADER. PB-SYNC-1 (amended 2026-07-30, B84/B85) forbids a
// direction tag in SenderKeyID or EpochID, because both ARE Bucket: a value in either forks
// the buckets per tag or collides two streams into one, and the collision was measured --
// reply high-water 2 -> 40, content dropped while still acked, staleness on the wrong
// channels, and no repair frame exists for the reply channel, so typing dies for the life of
// the epoch. The tag therefore rides the AEAD-COVERED PLAINTEXT, which is what the amended
// requirement names. Sender-zero is left exactly as it was.
//
// It lives in phonecore because it needs the REAL producer on BOTH legs: phonecore's seals
// for the phone -> machine leg and remotegw's for the machine -> phone leg. phonecore's tests
// may import remotegw (input_test.go, s7b_gateway_live_traffic_test.go); the reverse is
// forbidden, so a test written in remotegw could only hand-roll one of the two producers --
// and a hand-rolled fixture is exactly what hid this: every remotegw fixture seals a
// plaintext with no direction tag because the real phone did.

import (
	"encoding/json"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remotegw"
)

// TestDirectionBinding_MachineReplyReflectedOntoTheGatewayCostsNoInboundSeq is the
// gateway-leg arm, asserted on remotegw.OpenMailboxFrame -- the production choke point
// CommandBridge.handle reads through (command_loop.go), which is where the live poisoning on
// CommandBridge.PollOnce was measured.
//
// The reply seq is 5 and the phone's stream starts at 1 because that is the LIVE ordering:
// the gateway's inbound high-water resets per epoch while the reply seq is lifetime-monotone
// (C2b's durable outbound-reply.seq), so the machine's own reply is reliably AHEAD of the
// phone's next command.
func TestDirectionBinding_MachineReplyReflectedOntoTheGatewayCostsNoInboundSeq(t *testing.T) {
	const epoch = uint32(7)
	key := testContentKey()
	inbound := crypto.NewMailboxReceiver() // the gateway's phone -> machine replay guard

	// A reply the MACHINE sealed for the PHONE, captured off the other leg by the relay.
	reply, err := remotegw.SealControlReply(key, epoch, 5, takeControlReply())
	if err != nil {
		t.Fatalf("SealControlReply: %v", err)
	}
	if _, err := remotegw.OpenMailboxFrame(inbound, key, reply); err == nil {
		t.Error("the gateway ACCEPTED a frame the MACHINE sealed for the phone as phone -> machine traffic; nothing authenticated binds the direction")
	}

	// The real phone's stream must be untouched by that refusal. Seqs come from ONE
	// Sequencer, in order, as the live phone sends them (input.go).
	var seq Sequencer
	cmd, err := SealCommandEnvelope(key, epoch, seq.Next(), takeControlAuth())
	if err != nil {
		t.Fatalf("SealCommandEnvelope: %v", err)
	}
	stroke, err := SealInputData(key, epoch, seq.Next(), "m1/s1", []byte("ls\r"))
	if err != nil {
		t.Fatalf("SealInputData: %v", err)
	}
	for _, f := range []struct {
		name string
		raw  []byte
	}{{"SealCommandEnvelope", cmd}, {"SealInputData", stroke}} {
		if _, err := remotegw.OpenMailboxFrame(inbound, key, f.raw); err != nil {
			t.Errorf("%s: the live phone frame was refused with %v after the reflected reply; the reflection ADVANCED the gateway's inbound high-water, so the phone is silenced for the rest of the epoch", f.name, err)
		}
	}
}

// TestDirectionBinding_PhoneKeystrokeReflectedOntoThePhoneCostsNoReplySeq is the phone-leg
// arm, and it is the worse one. Keystrokes draw from the SAME Sequencer as commands
// (input.go), so the phone's outbound seq runs thousands ahead of the machine's reply seq --
// reflecting ONE keystroke silences EVERY machine reply for the epoch: no lease confirmation
// (PB-INPUT-2 gates typing on it), no operation outcome. The reply channel is also the one
// channel with NO repair frame (PB-SYNC-2), so nothing recovers it.
func TestDirectionBinding_PhoneKeystrokeReflectedOntoThePhoneCostsNoReplySeq(t *testing.T) {
	const epoch = uint32(7)
	key := testContentKey()
	router := NewMailboxRouter(key)

	// A keystroke the PHONE sealed for the MACHINE, at a seq deep into a typing session.
	stroke, err := SealInputData(key, epoch, 4242, "m1/s1", []byte("ls\r"))
	if err != nil {
		t.Fatalf("SealInputData: %v", err)
	}
	if _, err := router.Accept(stroke); err == nil {
		t.Error("the phone ACCEPTED its OWN keystroke as machine -> phone traffic; nothing authenticated binds the direction")
	}

	// The machine's real reply stream starts at 1 and must still land.
	reply, err := remotegw.SealControlReply(key, epoch, 1, takeControlReply())
	if err != nil {
		t.Fatalf("SealControlReply: %v", err)
	}
	if _, err := router.Accept(reply); err != nil {
		t.Fatalf("the machine's live reply was refused with %v after the reflected keystroke; the reflection ADVANCED the phone's reply high-water, so no lease confirmation and no op outcome can ever land", err)
	}
	if n := router.Replies().Len(); n != 1 {
		t.Errorf("reply cache holds %d replies; want 1 (the machine's live reply must reach the phone)", n)
	}
}

// TestDirectionBinding_TheTagIsInThePlaintextAndTheBucketKeyIsUNTOUCHED is the fence that
// keeps the fix off PB-SYNC-1's discriminator. It is not a style assertion: a tag in
// SenderKeyID or EpochID re-keys Bucket, which moves the reply channel's identity, the
// reconcile record's reply_ceiling coordinate (PB-STATE-4(b)) and the durable per-bucket
// high-waters all at once.
func TestDirectionBinding_TheTagIsInThePlaintextAndTheBucketKeyIsUNTOUCHED(t *testing.T) {
	const epoch = uint32(7)
	key := testContentKey()

	stroke, err := SealInputData(key, epoch, 9, "m1/s1", []byte("ls\r"))
	if err != nil {
		t.Fatalf("SealInputData: %v", err)
	}
	env, err := crypto.ParseEnvelope(stroke)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if env.Header.SenderKeyID != ([8]byte{}) {
		t.Errorf("a phone seal stamps SenderKeyID %x; PB-SYNC-1 keys the command-reply bucket on sender-zero and forbids a direction tag in that field (B84/B85)", env.Header.SenderKeyID)
	}
	if env.Header.EpochID != epoch {
		t.Errorf("a phone seal stamps EpochID %d, not %d; EpochID is the other half of Bucket and a tag there forks every bucket per direction", env.Header.EpochID, epoch)
	}

	// The tag is inside the AEAD, so it is authenticated: the relay cannot add, remove or
	// alter it without failing the tag. Reading it requires the key, which is the point.
	plain, err := crypto.OpenMailbox(key, env)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	var disc struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(plain, &disc); err != nil {
		t.Fatalf("decode the sealed plaintext: %v", err)
	}
	if disc.Kind != remotegw.KindPhoneToMachine {
		t.Errorf("the phone's sealed plaintext carries kind %q; want %q -- the two legs must agree on the tag or the pairing dies silently, every frame authenticating into a bucket the peer never reads", disc.Kind, remotegw.KindPhoneToMachine)
	}
	if disc.Kind == kindCommandReply {
		t.Error("the phone's tag equals the machine's command-reply kind; the two legs are indistinguishable again, which is the whole defect")
	}
}
