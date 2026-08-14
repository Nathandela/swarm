package phonecore

// ADR-007 B85 -- PB-SYNC-2's "OR THE STREAM STAYS UNRESOLVED" AND PB-SYNC-3's "CLEARS ONLY
// AFTER A SUCCESSFUL RESEED OF THAT STREAM" WERE UNFENCED FOR TWO OF THE FOUR CHANNELS.
//
// repairedBy (snapshot.go) is the only place the PB-SYNC-2 mapping is written down: a
// journal reseed repairs the journal channel, a fresh terminal snapshot repairs the terminal
// channel, and EVERY OTHER KIND REPAIRS NOTHING. commitReceive consults it on each
// CONTIGUOUS frame and deletes that channel's stale flag.
//
// Two arms it must never grow were reachable with the whole package green:
//
//	case kindCommandReply: return StreamReply   // a reply "repairing" its own stream
//	case "":               return StreamJournal // an ordinary event "repairing" the journal
//
// Both were verified by mutation to leave `go test ./internal/phonecore/` at exit 0 before
// this file existed. Production is correct only because neither arm is written -- the
// property rested on the absence of code, which is the weakest thing a property can rest on,
// and the clearing logic is under active edit for the B81(1) direction binding.
//
// WHY THE EXISTING SUITE CANNOT CATCH THEM, which is the whole reason this file is separate.
// TestS10_AReplyGapLeavesTheOpUnresolved asserts StreamStale(reply) is true IMMEDIATELY
// AFTER the gapping frame. At that instant the clearing branch is structurally unreachable:
// commitReceive's shape is
//
//	if !contiguous { ...set the flags... } else if s := repairedBy(f.kind); s != "" { ...clear... }
//
// so the frame that OPENS a hole can never be the frame that clears it. The clear needs a
// LATER, CONTIGUOUS frame on the same bucket, and no test in the tree drove one -- the
// round-5 audit probe was the first. A fence that asserts a flag is SET says nothing about
// whether it STAYS set, and "stays set until its own repair lands" is the entire content of
// PB-SYNC-3.
//
// WHAT IS AT STAKE, because "a stale badge" undersells it. The command-reply bucket carries
// the LEASE CONFIRMATION and every op outcome. A cleared reply flag tells the phone that
// channel is healthy over a hole it never filled, and the phone's other signal for the same
// condition -- Core.UnresolvedOps -- reads State.PendingOps, which nothing in production ever
// writes (ADR-007 B84). So the redundancy PB-SYNC-2 leans on is this one flag.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// replyBucketOf asks PRODUCTION which SenderKeyID owns the command-reply stream, rather than
// naming a sentinel this file would then have to be edited to follow.
//
// IT IS NOT CLEVERNESS FOR ITS OWN SAKE -- THE SENTINEL MOVED WHILE THIS FENCE WAS BEING
// WRITTEN. The reply bucket was sender-ZERO; the B81(1) direction binding retires that value
// and stamps StreamCommandReply, because the phone's own outbound seals shared sender-zero and
// a relay could reflect a keystroke into the reply stream.
//
// A fixture that hard-codes either value measures the RETIRED bucket the moment the other one
// lands, and -- this is the part that matters -- it does so SILENTLY. streamsOf's else arm
// attributes any unrecognised sender to journal+terminal, so a stale fixture would open its gap
// in the wrong bucket, the reply flag would never be set, and the assertions below would be
// VACUOUS rather than red. That is the exact shape of the defect this file exists to fence, so
// the fence must not be built on it. Deriving the bucket keeps it measuring the reply stream
// across the change, and fails LOUDLY if no single sender owns that stream.
func replyBucketOf(t *testing.T, epoch uint32) Bucket {
	t.Helper()
	var owners [][8]byte
	for i := 0; i <= 0xff; i++ {
		cand := [8]byte{byte(i)}
		if s := streamsOf(Bucket{Sender: cand}); len(s) == 1 && s[0] == StreamReply {
			owners = append(owners, cand)
		}
	}
	if len(owners) != 1 {
		t.Fatalf("streamsOf maps %d single-byte senders to the %q stream alone; want exactly 1. The "+
			"command-reply bucket is defined by ONE discriminator (PB-SYNC-1); if it has become "+
			"multi-byte or shared, this fence must be re-derived rather than quietly adjusted",
			len(owners), StreamReply)
	}
	return Bucket{Sender: owners[0], Epoch: epoch}
}

// gapTheReplyBucket drives a contiguous reply and then one that SKIPS a seq, leaving the
// command-reply bucket holed. It returns the seq the next CONTIGUOUS reply must carry --
// which is the frame the existing suite never delivers.
func gapTheReplyBucket(t *testing.T, c *Core, r *MailboxRouter, b Bucket) uint64 {
	t.Helper()
	key := testContentKey()
	if _, err := r.AcceptCommit(sealFrameFrom(t, key, b.Sender, b.Epoch, 1, marshalReply(t, protocol.Control{
		Op: protocol.OpOK, SessionID: "m1/s1", OperationID: "op-1",
	})), 201); err != nil {
		t.Fatalf("seed the reply bucket: %v", err)
	}
	// seq 2 -- some op's verdict -- is never delivered: the hole.
	if _, err := r.AcceptCommit(sealFrameFrom(t, key, b.Sender, b.Epoch, 3, marshalReply(t, protocol.Control{
		Op: protocol.OpOK, SessionID: "m1/s1", OperationID: "op-3",
	})), 203); err != nil {
		t.Fatalf("drive the gapped reply: %v", err)
	}
	if !r.StreamStale(StreamReply) {
		t.Fatalf("precondition: a reply-bucket gap did not stale the reply channel, so this test " +
			"cannot measure whether the flag SURVIVES (PB-SYNC-1)")
	}
	if !c.State().StaleStreams[StreamReply] {
		t.Fatalf("precondition: the reply channel's stale flag was not COMMITTED, so nothing " +
			"survives a process death to be cleared later (PB-SYNC-3)")
	}
	return 4
}

// TestB85_AContiguousReplyDoesNotClearTheReplyStream is the fence. The command-reply channel
// has NO repair frame by design (PB-SYNC-2: "via the durable operation outcome, or the stream
// stays unresolved"), so no reply -- however contiguous, however many -- may clear it.
func TestB85_AContiguousReplyDoesNotClearTheReplyStream(t *testing.T) {
	key := testContentKey()
	c, r := s10Router(t)
	b := replyBucketOf(t, 7)
	next := gapTheReplyBucket(t, c, r, b)

	// The frame the existing suite never delivers: the machine's NEXT reply, perfectly
	// contiguous with the gapped one. The hole at seq 2 is still a hole.
	if _, err := r.AcceptCommit(sealFrameFrom(t, key, b.Sender, b.Epoch, next, marshalReply(t, protocol.Control{
		Op: protocol.OpOK, SessionID: "m1/s1", OperationID: "op-4",
	})), 204); err != nil {
		t.Fatalf("accept the next contiguous reply at seq %d: %v", next, err)
	}

	if !r.StreamStale(StreamReply) {
		t.Errorf("a CONTIGUOUS command reply cleared StreamStale(%q). The reply channel has no "+
			"repair frame -- PB-SYNC-2 gives it the durable operation outcome or nothing, and "+
			"PB-SYNC-3 lets a flag clear only on a successful reseed OF THAT STREAM. The frames "+
			"in the hole are still missing, and one of them may be the lease confirmation "+
			"PB-INPUT-2 forbids typing without.", StreamReply)
	}
	// The durable half, checked separately: a live flag the transaction did not commit comes
	// back clear on the next launch, which is the same defect one process death later.
	if !c.State().StaleStreams[StreamReply] {
		t.Errorf("State.StaleStreams[%q] was cleared durably by a contiguous reply; the next "+
			"process death would resume a phone that believes the command-reply channel is "+
			"whole over a hole it never filled", StreamReply)
	}

	// And the flag is not merely latched forever: the ONE thing that resolves an op is its
	// durable outcome, so the op whose verdict fell in the hole must still read unresolved
	// rather than settled. (PB-SYNC-2's "or the stream stays unresolved".)
	if got := c.State().Receive[b]; got != next {
		t.Errorf("the reply bucket's high-water is %d after a contiguous frame at seq %d; the "+
			"watermark must advance with the frame even while the channel stays stale, or the "+
			"machine's next reply is refused as ErrStaleSeq", got, next)
	}
}

// TestB85_AnOrdinaryJournalRecordDoesNotClearTheJournalStream is the same hole on the shared
// bucket. PB-SYNC-2 gives the journal channel exactly one repair -- "an atomic roster+events
// snapshot" -- so an ordinary event, which carries neither, must not clear it. Without this
// the reseed is the designated repair channel and any record at all could stand in for it.
func TestB85_AnOrdinaryJournalRecordDoesNotClearTheJournalStream(t *testing.T) {
	key := testContentKey()
	c, r := s10Router(t)
	next := openSharedGap(t, r, key)
	if !r.StreamStale(StreamJournal) || !r.StreamStale(StreamTerminal) {
		t.Fatalf("precondition: a shared-bucket gap staled journal=%v terminal=%v; both must be "+
			"stale before this test can measure what survives (PB-SYNC-1)",
			r.StreamStale(StreamJournal), r.StreamStale(StreamTerminal))
	}

	// An ordinary journal event, contiguous. It is NOT a reseed: it carries one record, not
	// the roster+events set the repair channel is defined as.
	if _, err := r.AcceptCommit(sealFrameFrom(t, key, machineSender, 7, next,
		marshalEvent(t, schema.JournalRecord{Cursor: 40, SessionID: "m1/s-alpha", Type: "exited"})), 104); err != nil {
		t.Fatalf("accept the next contiguous journal event at seq %d: %v", next, err)
	}

	if !r.StreamStale(StreamJournal) {
		t.Errorf("an ORDINARY journal record cleared StreamStale(%q). PB-SYNC-2 makes an atomic "+
			"roster+events reseed the journal channel's repair; a single event says nothing about "+
			"the records in the hole, and the sessions they would have created or retired stay "+
			"missing from the phone's model", StreamJournal)
	}
	if !r.StreamStale(StreamTerminal) {
		t.Errorf("an ordinary journal record cleared StreamStale(%q) -- a journal frame cannot "+
			"repair the terminal grid at all (PB-SYNC-2)", StreamTerminal)
	}
	if !c.State().StaleStreams[StreamJournal] {
		t.Errorf("State.StaleStreams[%q] was cleared durably by an ordinary journal record",
			StreamJournal)
	}
}
