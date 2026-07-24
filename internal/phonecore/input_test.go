package phonecore

// FAILING-FIRST (TDD RED, GG-5) test for A7 input Slice 2: the phone's sealed
// INPUT-FRAME encoder and its ONE shared monotonic sequence allocator. Input
// frames ride the SAME phone -> machine mailbox as commands and share a single
// MailboxReceiver key (SenderKeyID stays zero), so the phone MUST stamp commands
// AND input from ONE Sequencer -- a per-kind counter would restart at 1 and the
// receiver would reject the collision as a replay. This test pins three things:
//   1. Sequencer.Next hands strictly increasing 1, 2, 3, ...;
//   2. SealInputData / SealInputResize produce envelopes a crypto.MailboxReceiver
//      accepts IN ORDER, remotegw.OpenInputFrame recovers the frame, and a
//      replayed seq is rejected with crypto.ErrStaleSeq;
//   3. commands (SealCommandEnvelope) and input frames drawn FROM THE SAME
//      allocator interleave through ONE receiver without a stale-seq collision,
//      proving the shared seq space.

import (
	"bytes"
	"errors"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remotegw"
)

func testContentKey() crypto.ContentKey {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 7)
	}
	return key
}

func TestSealInputFrame_SharedSeqSpaceRejectsReplay(t *testing.T) {
	const epoch = uint32(1)
	key := testContentKey()

	// (1) A fresh Sequencer hands strictly increasing 1, 2, 3.
	var probe Sequencer
	for want := uint64(1); want <= 3; want++ {
		if got := probe.Next(); got != want {
			t.Fatalf("Sequencer.Next() = %d; want %d (must hand strictly increasing seqs from 1)", got, want)
		}
	}

	// (2) In-order accept + recover through ONE receiver, drawing every seq from
	// ONE allocator.
	seqr := &Sequencer{}
	recv := crypto.NewMailboxReceiver()

	dataSeq := seqr.Next()
	rawData, err := SealInputData(key, epoch, dataSeq, "machine1/sess1", []byte("ls -la\r"))
	if err != nil {
		t.Fatalf("SealInputData: %v", err)
	}
	frameData, err := remotegw.OpenInputFrame(recv, key, rawData)
	if err != nil {
		t.Fatalf("OpenInputFrame(data): %v", err)
	}
	if frameData.Kind != "data" {
		t.Fatalf("data frame Kind = %q; want \"data\"", frameData.Kind)
	}
	// The target session id is bound INSIDE the sealed frame and recovered on open,
	// so the machine can route the keystroke by it (never by mutable focus state).
	if frameData.Session != "machine1/sess1" {
		t.Fatalf("data frame Session = %q; want %q (session must be sealed into the frame)", frameData.Session, "machine1/sess1")
	}
	if !bytes.Equal(frameData.Data, []byte("ls -la\r")) {
		t.Fatalf("data frame Data = %q; want %q", frameData.Data, "ls -la\r")
	}

	resizeSeq := seqr.Next()
	rawResize, err := SealInputResize(key, epoch, resizeSeq, "machine1/sess1", 120, 40)
	if err != nil {
		t.Fatalf("SealInputResize: %v", err)
	}
	frameResize, err := remotegw.OpenInputFrame(recv, key, rawResize)
	if err != nil {
		t.Fatalf("OpenInputFrame(resize): %v", err)
	}
	if frameResize.Kind != "resize" {
		t.Fatalf("resize frame Kind = %q; want \"resize\"", frameResize.Kind)
	}
	if frameResize.Session != "machine1/sess1" {
		t.Fatalf("resize frame Session = %q; want %q", frameResize.Session, "machine1/sess1")
	}
	if frameResize.Cols != 120 || frameResize.Rows != 40 {
		t.Fatalf("resize frame = %dx%d; want 120x40", frameResize.Cols, frameResize.Rows)
	}

	// A replayed seq (the data frame again, seq <= high-water) is rejected as a
	// stale/reordered sequence -- the same guard commands get.
	if _, err := remotegw.OpenInputFrame(recv, key, rawData); !errors.Is(err, crypto.ErrStaleSeq) {
		t.Fatalf("replayed input frame err = %v; want crypto.ErrStaleSeq", err)
	}

	// (3) Shared seq space: interleave a command seal and input seals FROM THE
	// SAME allocator through ONE receiver. Every seq is distinct and increasing,
	// so nothing is rejected as stale. Were commands and input on SEPARATE
	// allocators, both would restart at 1 and the second stream would collide.
	sharedSeq := &Sequencer{}
	shared := crypto.NewMailboxReceiver()

	cmd := protocol.DeviceCommandAuth{
		Action:      protocol.ActionKill,
		Machine:     "machine1",
		Session:     "machine1/sess1",
		OperationID: "op-kill-1",
	}

	// cmd (seq 1) -> input data (seq 2) -> cmd (seq 3) -> input resize (seq 4).
	rawCmd1, err := SealCommandEnvelope(key, epoch, sharedSeq.Next(), cmd)
	if err != nil {
		t.Fatalf("SealCommandEnvelope #1: %v", err)
	}
	if _, err := remotegw.OpenRemoteCommandGuarded(shared, key, rawCmd1); err != nil {
		t.Fatalf("guarded command #1 through shared receiver: %v", err)
	}

	rawIn1, err := SealInputData(key, epoch, sharedSeq.Next(), "machine1/sess1", []byte("echo hi\r"))
	if err != nil {
		t.Fatalf("SealInputData shared: %v", err)
	}
	if _, err := remotegw.OpenInputFrame(shared, key, rawIn1); err != nil {
		t.Fatalf("input frame after command through shared receiver: %v", err)
	}

	rawCmd2, err := SealCommandEnvelope(key, epoch, sharedSeq.Next(), cmd)
	if err != nil {
		t.Fatalf("SealCommandEnvelope #2: %v", err)
	}
	if _, err := remotegw.OpenRemoteCommandGuarded(shared, key, rawCmd2); err != nil {
		t.Fatalf("guarded command #2 through shared receiver: %v", err)
	}

	rawIn2, err := SealInputResize(key, epoch, sharedSeq.Next(), "machine1/sess1", 80, 24)
	if err != nil {
		t.Fatalf("SealInputResize shared: %v", err)
	}
	if _, err := remotegw.OpenInputFrame(shared, key, rawIn2); err != nil {
		t.Fatalf("resize frame after command through shared receiver: %v", err)
	}
}
