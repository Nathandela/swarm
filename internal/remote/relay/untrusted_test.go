package relay

// R-REL.6 — untrusted-relay invariants (forward-only). The relay can only
// drop/delay; it cannot read, forge, or reorder undetectably. AEAD blocks
// forgery, the authenticated seq blocks reorder/dup. (Live-MITM and pairing-MITM
// rejection are proven in the crypto/pair packages, not here.)

import (
	"errors"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// TestRelay_CannotForgeEvent asserts a relay-crafted mailbox item (the relay
// lacks the content key) fails the device's AEAD — the relay cannot inject a
// forged event.
func TestRelay_CannotForgeEvent(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil)
	machine, device, devRID, sp := mailboxFixture(t, srv, clk)

	// One genuine event the device accepts, establishing the high-water mark.
	genuine := sp.sealMailbox(t, 1, []byte("real"), clk)
	if _, err := machine.MailboxAppend(testCtx(t), devRID, genuine); err != nil {
		t.Fatalf("MailboxAppend(genuine): %v", err)
	}

	// The relay forges the next event: it holds only ciphertext, so it can at
	// best craft an envelope under a key it invented — which fails the device's
	// AEAD under the real content key.
	forgedKey, err := crypto.NewEpochKeys()
	if err != nil {
		t.Fatalf("NewEpochKeys: %v", err)
	}
	forgedHeader := crypto.EnvelopeHeader{
		Version:     crypto.VersionV1,
		EpochID:     sp.epochID,
		Seq:         2,
		SenderKeyID: sp.senderKeyID,
		IssuedAt:    clk.Now().UnixMilli(),
	}
	forged, err := crypto.SealMailbox(forgedKey.ContentKey, forgedHeader, []byte("injected"))
	if err != nil {
		t.Fatalf("seal forged: %v", err)
	}
	if _, err := machine.MailboxAppend(testCtx(t), devRID, forged.Marshal()); err != nil {
		// The relay accepts arbitrary opaque bytes; forgery detection is the
		// device's job, below.
		t.Fatalf("relay refused to store opaque bytes (it should be content-blind): %v", err)
	}

	items, err := device.MailboxRead(testCtx(t), 0)
	if err != nil {
		t.Fatalf("MailboxRead: %v", err)
	}
	rcv := crypto.NewMailboxReceiver()
	real0, _ := crypto.ParseEnvelope(items[0].Envelope)
	if _, err := rcv.Accept(sp.keys.ContentKey, real0); err != nil {
		t.Fatalf("genuine event rejected: %v", err)
	}
	forged0, _ := crypto.ParseEnvelope(items[1].Envelope)
	if _, err := rcv.Accept(sp.keys.ContentKey, forged0); err == nil {
		t.Fatalf("relay-forged event was accepted — forgery went undetected")
	}
}

// TestRelay_DropIsOnlyUndetectableAction asserts the boundary of what an
// untrusted relay can do without detection: withholding/delaying the TAIL looks
// like "not yet delivered" (undetectable, allowed availability loss), but a
// mid-stream drop surfaces a seq gap, a tamper fails the AEAD, and a reorder
// fails the seq check.
func TestRelay_DropIsOnlyUndetectableAction(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil)
	machine, device, devRID, sp := mailboxFixture(t, srv, clk)

	for i := uint64(1); i <= 4; i++ {
		env := sp.sealMailbox(t, i, []byte{byte('a' + i)}, clk)
		if _, err := machine.MailboxAppend(testCtx(t), devRID, env); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	items, err := device.MailboxRead(testCtx(t), 0)
	if err != nil {
		t.Fatalf("MailboxRead: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("read count: got %d, want 4", len(items))
	}
	parse := func(i int) *crypto.Envelope {
		e, perr := crypto.ParseEnvelope(items[i].Envelope)
		if perr != nil {
			t.Fatalf("ParseEnvelope %d: %v", i, perr)
		}
		return e
	}

	// (1) Tail-withhold/delay: seq 1..3 accepted, seq 4 simply never fed — the
	// receiver sees a valid, gap-free prefix. This is the ONLY undetectable action.
	rcv := crypto.NewMailboxReceiver()
	for i := 0; i < 3; i++ {
		res, aerr := rcv.Accept(sp.keys.ContentKey, parse(i))
		if aerr != nil {
			t.Fatalf("tail-withhold prefix accept %d: %v", i, aerr)
		}
		if res.Gap {
			t.Fatalf("tail-withhold surfaced a spurious gap at %d", i)
		}
	}

	// (2) Mid-stream drop: fresh receiver accepts seq 1, then the relay drops
	// seq 2 and serves seq 3 — the gap is surfaced.
	rcv2 := crypto.NewMailboxReceiver()
	if _, aerr := rcv2.Accept(sp.keys.ContentKey, parse(0)); aerr != nil {
		t.Fatalf("accept seq1: %v", aerr)
	}
	res, aerr := rcv2.Accept(sp.keys.ContentKey, parse(2)) // skips index 1 (seq 2)
	if aerr != nil {
		t.Fatalf("accept after drop errored instead of surfacing a gap: %v", aerr)
	}
	if !res.Gap {
		t.Fatalf("mid-stream drop went undetected: no gap surfaced")
	}

	// (3) Reorder: after seq 3, replaying seq 2 is rejected.
	if _, aerr := rcv2.Accept(sp.keys.ContentKey, parse(1)); !errors.Is(aerr, crypto.ErrStaleSeq) {
		t.Fatalf("reorder undetected: got %v, want ErrStaleSeq", aerr)
	}

	// (4) Tamper: flip a ciphertext byte the relay stored — the AEAD tag fails.
	tampered := parse(0)
	if len(tampered.Ciphertext) == 0 {
		t.Fatalf("fixture produced empty ciphertext")
	}
	tampered.Ciphertext[0] ^= 0xff
	rcv3 := crypto.NewMailboxReceiver()
	if _, aerr := rcv3.Accept(sp.keys.ContentKey, tampered); aerr == nil {
		t.Fatalf("relay tamper went undetected — AEAD accepted a modified body")
	}
}
