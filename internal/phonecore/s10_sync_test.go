package phonecore

// FAILING-FIRST (TDD RED, GG-5) tests for slice S10's staleness-and-repair core:
// PB-SYNC-1 (stale per SEQ BUCKET, repair per CHANNEL), PB-SYNC-2 (a repair channel per
// stream), PB-SYNC-3 (clearing is fail-closed and coordinate-correct) and PB-SYNC-8 (a
// journal reseed REPLACES the cache cursor).
//
// WHY THESE ARE DRIVEN THROUGH FRAMES AND NOT THROUGH A REPAIR METHOD. The phone has no
// daemon connection: every machine->phone repair arrives as a sealed frame on the ONE
// shared mailbox, and its own arrival seq is the "matching transport watermark" PB-SYNC-3
// requires the repair to be committed with. Testing a hypothetical Core.Reseed(...) method
// would prove the merge logic and skip the atomicity, which is the half that fails.
//
// THE SEAM THESE TESTS PIN:
//
//	const  StreamJournal / StreamTerminal / StreamReply / StreamGrant
//	func   (*MailboxRouter) StreamStale(stream string) bool
//	frame  {"kind":"journal_reseed", "roster":[...], "events":[...], "cursor":N}
//	type   schema.JournalReseed
//
// WHAT TODAY'S CODE DOES, so the failures below are read as findings and not as noise:
// mobile/relay.go attributes a gap to whichever KIND the gapped frame happened to be
// (a.markStream("terminal", receipt.Gap) inside the terminal_snapshot arm), which is
// exactly the attribution PB-SYNC-1 calls "a failing implementation"; and there is no
// journal_reseed kind at all, so MailboxRouter.open hits its fail-closed default arm and
// the designated journal repair channel does not exist.

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/status"
)

// ---------------------------------------------------------------------------
// Fixtures.
//
// EVERY ROSTER RECORD BELOW HAS Cursor: 0, AND THAT IS THE POINT (PB-SYNC-8's fixture
// rule). internal/daemon/journal.go leaves it deliberately unset -- "a roster record is a
// set member keyed by SessionID, NOT a point in the cursor-ordered event stream" -- so a
// fixture with a nonzero roster cursor tests a wire the machine never emits, and hides the
// silent drop this slice exists to close. s10_rosterfixture_test.go enforces that rule
// mechanically over every test source in the repo.
// ---------------------------------------------------------------------------

// wireJournalReseed is the committed sealed plaintext of a journal-reseed frame: the kind
// discriminator plus the schema.JournalReseed fields. The tests decode the LITERAL, not
// the Go type, so a rename or a field reorder cannot silently move the wire (the
// discipline reconcile_test.go's wireReconcileFrame already applies).
const wireJournalReseed = `{"kind":"journal_reseed","roster":[{"cursor":0,"session_id":"m1/s-alpha","type":"roster","group":"working"},{"cursor":0,"session_id":"m1/s-beta","type":"roster"}],"events":[{"cursor":50,"session_id":"m1/s-beta","type":"group_transition","group":"needs_input"}],"cursor":50}`

// wantJournalReseed is the snapshot those bytes carry.
func wantJournalReseed() schema.JournalReseed {
	return schema.JournalReseed{
		Roster: []schema.JournalRecord{
			{SessionID: "m1/s-alpha", Type: "roster", Group: status.GroupWorking},
			{SessionID: "m1/s-beta", Type: "roster"},
		},
		Events: []schema.JournalRecord{
			{Cursor: 50, SessionID: "m1/s-beta", Type: "group_transition", Group: status.GroupNeedsInput},
		},
		Cursor: 50,
	}
}

// marshalEvent builds a kind-less journal-record plaintext -- the backward-compatible
// shape the daemon's live events already travel in.
func marshalEvent(t *testing.T, rec schema.JournalRecord) []byte {
	t.Helper()
	plain, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal journal record: %v", err)
	}
	return plain
}

// s10Router resumes a Core over a fresh paired memStore and returns both, so a test can
// assert on the durable blob as well as the live router.
func s10Router(t *testing.T) (*Core, *MailboxRouter) {
	t.Helper()
	st := &memStore{}
	seedPaired(t, st)
	c, err := Resume(Config{State: st, Ack: &recordingAcker{}})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	return c, c.Router()
}

// openSharedGap drives one contiguous frame and then one that SKIPS a seq, so the shared
// bucket is left with a hole. It returns the seq the next contiguous frame must carry.
func openSharedGap(t *testing.T, r *MailboxRouter, key crypto.ContentKey) uint64 {
	t.Helper()
	if _, err := r.AcceptCommit(sealFrameFrom(t, key, machineSender, 7, 1,
		marshalEvent(t, schema.JournalRecord{Cursor: 10, SessionID: "m1/s-alpha", Type: "launched"})), 101); err != nil {
		t.Fatalf("seed the shared bucket: %v", err)
	}
	// seq 2 is never delivered: the hole.
	if _, err := r.AcceptCommit(sealFrameFrom(t, key, machineSender, 7, 3,
		marshalEvent(t, schema.JournalRecord{Cursor: 30, SessionID: "m1/s-alpha", Type: "group_transition", Group: status.GroupWorking})), 103); err != nil {
		t.Fatalf("drive the gapped frame: %v", err)
	}
	return 4
}

// ---------------------------------------------------------------------------
// PB-SYNC-8, wire shape first: the reseed's bytes are pinned before anything reads them.
// ---------------------------------------------------------------------------

// TestS10_JournalReseedWireShape pins that the production frame type marshals to the
// committed bytes, so a Go-side rename cannot move the wire under the gateway.
//
// PASSES TODAY BY CONSTRUCTION, and labelled so no evidence line counts it as earned: it
// asserts a data type against its own literal, and RED supplied both. It earns nothing
// about the repair path -- what it buys is that the gateway and the phone cannot drift
// apart later (§9 rule 4's pinned schema).
func TestS10_JournalReseedWireShape(t *testing.T) {
	got, err := json.Marshal(reseedFrame{Kind: kindJournalReseed, JournalReseed: wantJournalReseed()})
	if err != nil {
		t.Fatalf("marshal reseed frame: %v", err)
	}
	if string(got) != wireJournalReseed {
		t.Fatalf("journal reseed wire shape =\n  %s\nwant\n  %s", got, wireJournalReseed)
	}
}

// ---------------------------------------------------------------------------
// PB-SYNC-1: staleness is per SEQ BUCKET, and a shared-bucket gap conservatively marks
// BOTH channels that bucket carries.
// ---------------------------------------------------------------------------

// TestS10_ASharedBucketGapStalesJournalAndTerminal is PB-SYNC-1's headline. The gapped
// frame here is a TERMINAL SNAPSHOT, chosen deliberately: crypto.MailboxResult carries
// {Plaintext, Gap bool} and nothing else, so the missing seq could equally have been a
// journal record -- and an implementation that reads the gap off the frame it arrived on
// attributes it to terminal alone and leaves the journal silently presented as live.
func TestS10_ASharedBucketGapStalesJournalAndTerminal(t *testing.T) {
	key := testContentKey()
	_, r := s10Router(t)

	if _, err := r.AcceptCommit(sealFrameFrom(t, key, machineSender, 7, 1,
		marshalEvent(t, schema.JournalRecord{Cursor: 10, SessionID: "m1/s-alpha", Type: "launched"})), 101); err != nil {
		t.Fatalf("seed the shared bucket: %v", err)
	}
	// seq 2 never arrives. seq 3 is a terminal snapshot, so the ONLY kind the gapped frame
	// names is terminal.
	rcpt, err := r.AcceptCommit(sealFrameFrom(t, key, machineSender, 7, 3,
		marshalSnapshot(t, "m1/s-alpha", []string{"$ "}, 80, 24)), 103)
	if err != nil {
		t.Fatalf("accept the gapped snapshot: %v", err)
	}
	if !rcpt.Gap {
		t.Fatalf("a frame at seq 3 behind a high-water of 1 reported Gap=false; the hole at seq 2 is the premise of every assertion below")
	}

	if !r.StreamStale(StreamTerminal) {
		t.Errorf("after a gap in the shared bucket StreamStale(%q) = false", StreamTerminal)
	}
	if !r.StreamStale(StreamJournal) {
		t.Errorf("after a gap in the shared bucket StreamStale(%q) = false. Journal and terminal "+
			"share ONE (sender, epoch) seq space and crypto.MailboxResult carries a bare Gap bool "+
			"with no frame kind, so the skipped seq 2 CANNOT be attributed to the terminal stream "+
			"just because seq 3 happened to be a snapshot -- it may well have been the journal "+
			"record that said a session exited. PB-SYNC-1 requires the conservative mark; "+
			"attributing a shared-bucket gap to one channel is a failing implementation, and the "+
			"cost is a killed session shown as running with no staleness anywhere on the screen",
			StreamJournal)
	}
	if r.StreamStale(StreamReply) {
		t.Errorf("a gap in the SHARED bucket marked StreamStale(%q). The command-reply stream is a "+
			"SEPARATE seq space (the deliberate SenderKeyID=0 split), so nothing about its "+
			"contiguity is knowable from a hole in this one; staling it would send the phone "+
			"repairing a channel that never lost a frame", StreamReply)
	}
}

// TestS10_ACommandReplyGapStalesNeitherJournalNorTerminal is the mirror, and it is the
// non-vacuity control for the test above: an implementation that simply stales everything
// on any gap passes that one and fails this one.
func TestS10_ACommandReplyGapStalesNeitherJournalNorTerminal(t *testing.T) {
	key := testContentKey()
	_, r := s10Router(t)

	if _, err := r.AcceptCommit(sealFrame(t, key, 1, marshalReply(t, protocol.Control{
		Op: protocol.OpOK, SessionID: "m1/s1", OperationID: "op-1",
	})), 201); err != nil {
		t.Fatalf("seed the reply bucket: %v", err)
	}
	// seq 2 never arrives on the reply bucket.
	rcpt, err := r.AcceptCommit(sealFrame(t, key, 3, marshalReply(t, protocol.Control{
		Op: protocol.OpOK, SessionID: "m1/s1", OperationID: "op-3",
	})), 203)
	if err != nil {
		t.Fatalf("accept the gapped reply: %v", err)
	}
	if !rcpt.Gap {
		t.Fatalf("a reply at seq 3 behind a high-water of 1 reported Gap=false; the hole is the premise here")
	}

	if !r.StreamStale(StreamReply) {
		t.Errorf("a gap in the command-reply bucket left StreamStale(%q) = false", StreamReply)
	}
	if r.StreamStale(StreamJournal) || r.StreamStale(StreamTerminal) {
		t.Errorf("a gap in the COMMAND-REPLY bucket staled journal=%v terminal=%v. Those ride a "+
			"different (sender, epoch) space entirely, so a missing reply says nothing about them "+
			"-- and an implementation that stales everything on any gap makes the per-bucket "+
			"tracking PB-SYNC-1 requires indistinguishable from a single global flag",
			r.StreamStale(StreamJournal), r.StreamStale(StreamTerminal))
	}
}

// ---------------------------------------------------------------------------
// PB-SYNC-2: one repair channel per stream, and they are not interchangeable.
// ---------------------------------------------------------------------------

// TestS10_AJournalReseedRepairsTheJournalChannel is the designated journal repair. Against
// today's code the frame never gets past MailboxRouter.open: journal_reseed is not a
// recognised kind, so it hits the fail-closed default arm and errors -- the repair channel
// PB-SYNC-2 nominates does not exist.
func TestS10_AJournalReseedRepairsTheJournalChannel(t *testing.T) {
	key := testContentKey()
	_, r := s10Router(t)
	next := openSharedGap(t, r, key)

	if !r.StreamStale(StreamJournal) {
		t.Fatalf("precondition: the shared-bucket gap did not stale the journal channel")
	}
	if _, err := r.AcceptCommit(sealFrameFrom(t, key, machineSender, 7, next, []byte(wireJournalReseed)), 200); err != nil {
		t.Fatalf("accept the journal reseed: %v. PB-SYNC-2 makes an atomic roster+events "+
			"snapshot THE journal repair channel; a router that cannot open the frame has no "+
			"journal repair at all", err)
	}
	if r.StreamStale(StreamJournal) {
		t.Errorf("StreamStale(%q) is still true after a contiguous journal reseed landed. The "+
			"reseed IS the repair, and a repair that does not clear the flag leaves the phone "+
			"resyncing forever", StreamJournal)
	}
}

// TestS10_AJournalReseedDoesNotClearTerminalStaleness is PB-SYNC-2's explicit clause. A
// roster+events snapshot carries no terminal grid, so a phone that cleared terminal on it
// would present the screen the user missed as live.
func TestS10_AJournalReseedDoesNotClearTerminalStaleness(t *testing.T) {
	key := testContentKey()
	_, r := s10Router(t)
	next := openSharedGap(t, r, key)

	if !r.StreamStale(StreamTerminal) {
		t.Fatalf("precondition: the shared-bucket gap did not stale the terminal channel")
	}
	if _, err := r.AcceptCommit(sealFrameFrom(t, key, machineSender, 7, next, []byte(wireJournalReseed)), 200); err != nil {
		t.Fatalf("accept the journal reseed: %v", err)
	}
	if !r.StreamStale(StreamTerminal) {
		t.Errorf("a journal reseed cleared StreamStale(%q). It carries a roster and events and "+
			"NO grid, so the frames the terminal stream lost are still lost -- the phone would "+
			"render the pre-gap screen as live (PB-APP-8: a stale view is never presented as live)",
			StreamTerminal)
	}
}

// TestS10_AFreshSnapshotRepairsTheTerminalChannelOnly is the terminal half of PB-SYNC-2,
// and the mirror non-vacuity control for the test above.
func TestS10_AFreshSnapshotRepairsTheTerminalChannelOnly(t *testing.T) {
	key := testContentKey()
	_, r := s10Router(t)
	next := openSharedGap(t, r, key)

	if _, err := r.AcceptCommit(sealFrameFrom(t, key, machineSender, 7, next,
		marshalSnapshot(t, "m1/s-alpha", []string{"$ whoami", "nathan"}, 80, 24)), 200); err != nil {
		t.Fatalf("accept the fresh snapshot: %v", err)
	}
	if r.StreamStale(StreamTerminal) {
		t.Errorf("a fresh FULL server-rendered snapshot left StreamStale(%q) = true. A snapshot is "+
			"latest-wins and complete, so it is the whole repair for this channel -- PB-SYNC-2 "+
			"names it as such", StreamTerminal)
	}
	if !r.StreamStale(StreamJournal) {
		t.Errorf("a terminal snapshot cleared StreamStale(%q). A grid says nothing about which "+
			"sessions exist or which ones exited; the journal channel is repaired by a reseed and "+
			"by nothing else", StreamJournal)
	}
}

// TestS10_AReplyGapLeavesTheOpUnresolved is PB-SYNC-2's third clause: "command replies via
// the durable operation outcome, or the stream stays unresolved". There is no reply
// reseed, so the ONLY thing that can settle an op whose reply the phone never saw is its
// durable outcome -- and until one lands the op must stay visibly unresolved rather than
// be quietly dropped.
func TestS10_AReplyGapLeavesTheOpUnresolved(t *testing.T) {
	key := testContentKey()
	c, r := s10Router(t)

	// One op in flight, its reply lost in the hole.
	st := c.State()
	st.PendingOps = []QueuedOp{{Cmd: protocol.DeviceCommandAuth{
		Action: protocol.ActionKill, Machine: "m1", Session: "m1/s1", OperationID: "op-lost",
	}}}
	if err := c.Save(st); err != nil {
		t.Fatalf("queue the op: %v", err)
	}
	if _, err := r.AcceptCommit(sealFrame(t, key, 1, marshalReply(t, protocol.Control{
		Op: protocol.OpOK, SessionID: "m1/s1", OperationID: "op-other",
	})), 201); err != nil {
		t.Fatalf("seed the reply bucket: %v", err)
	}
	// seq 2 -- op-lost's reply -- never arrives.
	if _, err := r.AcceptCommit(sealFrame(t, key, 3, marshalReply(t, protocol.Control{
		Op: protocol.OpOK, SessionID: "m1/s1", OperationID: "op-later",
	})), 203); err != nil {
		t.Fatalf("accept the gapped reply: %v", err)
	}

	unresolved := c.UnresolvedOps()
	found := false
	for _, op := range unresolved {
		if op.Cmd.OperationID == "op-lost" {
			found = true
		}
	}
	if !found {
		t.Errorf("after the reply for op-lost fell in a seq hole, UnresolvedOps() = %+v and does "+
			"not name it. A mutating op whose verdict the phone can never read must stay "+
			"unresolved: reporting it settled invents an outcome, and dropping it hides a kill "+
			"that may or may not have run", unresolved)
	}
	if !r.StreamStale(StreamReply) {
		t.Errorf("StreamStale(%q) = false after a reply-bucket gap; the channel has no reseed, so "+
			"nothing else can have repaired it", StreamReply)
	}
}

// ---------------------------------------------------------------------------
// PB-SYNC-3: clearing is fail-closed, and it clears the RIGHT stream's coordinate. The
// criterion names two distinct hazards and they fail differently, so they get one test
// each.
// ---------------------------------------------------------------------------

// TestS10_AFailedReseedStaysStale is hazard one: OPTIMISTIC CLEARING. The repair is marked
// applied before -- or without -- the durable commit that carries it, so a process death
// in the window leaves a phone that believes it is fresh over content it never recorded.
func TestS10_AFailedReseedStaysStale(t *testing.T) {
	key := testContentKey()
	mem := &memStore{}
	seedPaired(t, mem)

	// Let the seed and the gap commit (two Saves), then kill every Save from the reseed on.
	failing := &failAfterNStore{inner: mem, n: 2}
	c, err := Resume(Config{State: failing, Ack: &recordingAcker{}})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	r := c.Router()
	next := openSharedGap(t, r, key)
	if !r.StreamStale(StreamJournal) {
		t.Fatalf("precondition: the gap did not stale the journal channel (store saves=%d)", failing.saves)
	}

	if _, err := r.AcceptCommit(sealFrameFrom(t, key, machineSender, 7, next, []byte(wireJournalReseed)), 200); err == nil {
		t.Fatalf("a journal reseed whose durable commit FAILED returned no error; the caller is " +
			"then told the repair landed")
	} else if !errors.Is(err, errStoreDied) {
		// Not fatal: the staleness assertion below is the requirement, and it must still run.
		// Today this reports the unrecognised kind instead, which is the same finding one step
		// earlier -- the repair channel does not exist to fail atomically.
		t.Errorf("the reseed failed with %v, want the INJECTED store death. The test is meant to "+
			"exercise a repair whose durable commit did not land; a failure anywhere earlier "+
			"means the commit was never reached", err)
	}
	if !r.StreamStale(StreamJournal) {
		t.Errorf("StreamStale(%q) was cleared by a reseed whose durable commit FAILED. PB-SYNC-3 "+
			"is fail-closed: nothing reached disk, so the next process start comes back with the "+
			"hole still in the bucket and a phone that has forgotten it -- the repair is "+
			"unrepeatable and the loss is permanent and silent", StreamJournal)
	}
}

// TestS10_AReseedClearsOnlyItsOwnBucketsCoordinate is hazard two: WATERMARK / COORDINATE
// CONFUSION. The journal and the command replies live in different (sender, epoch) seq
// spaces, so a repair that seeds "the" high-water rather than ITS bucket's high-water
// silently raises the reply guard past frames the machine has not sent yet -- and every
// real reply is then refused as a stale seq for the rest of the epoch.
func TestS10_AReseedClearsOnlyItsOwnBucketsCoordinate(t *testing.T) {
	key := testContentKey()
	c, r := s10Router(t)

	// Both buckets in flight, both holed.
	next := openSharedGap(t, r, key)
	if _, err := r.AcceptCommit(sealFrame(t, key, 1, marshalReply(t, protocol.Control{
		Op: protocol.OpOK, SessionID: "m1/s1", OperationID: "op-1",
	})), 301); err != nil {
		t.Fatalf("seed the reply bucket: %v", err)
	}
	if _, err := r.AcceptCommit(sealFrame(t, key, 3, marshalReply(t, protocol.Control{
		Op: protocol.OpOK, SessionID: "m1/s1", OperationID: "op-3",
	})), 303); err != nil {
		t.Fatalf("gap the reply bucket: %v", err)
	}
	replyBefore := c.State().Receive[replyBucket(7)]

	if _, err := r.AcceptCommit(sealFrameFrom(t, key, machineSender, 7, next, []byte(wireJournalReseed)), 400); err != nil {
		t.Fatalf("accept the journal reseed: %v", err)
	}

	if got := c.State().Receive[replyBucket(7)]; got != replyBefore {
		t.Errorf("a JOURNAL reseed moved the COMMAND-REPLY bucket's receive high-water from %d to "+
			"%d. The two are independent seq spaces; raising the reply guard on a journal repair "+
			"makes the machine's next real reply arrive at or below the high-water and be refused "+
			"as crypto.ErrStaleSeq -- no lease confirmation, no op outcome, for the rest of the epoch",
			replyBefore, got)
	}
	if !r.StreamStale(StreamReply) {
		t.Errorf("a JOURNAL reseed cleared StreamStale(%q). It repairs one channel and certifies "+
			"one bucket's watermark; the reply stream lost frames it still has not seen", StreamReply)
	}
	if got := c.State().Receive[journalBucket(7)]; got != next {
		t.Errorf("after a reseed at seq %d the shared bucket's high-water is %d. The repair is "+
			"committed atomically WITH the watermark its own arrival certifies, so the two cannot "+
			"disagree", next, got)
	}
}

// TestS10_StalenessSurvivesTheProcessDeath is what "committed" means. Android SIGKILLs the
// app; a stale flag held only in memory comes back clear, and the phone presents the gap it
// knows about as live on the very next launch.
func TestS10_StalenessSurvivesTheProcessDeath(t *testing.T) {
	key := testContentKey()
	mem := &memStore{}
	seedPaired(t, mem)

	first, err := Resume(Config{State: mem, Ack: &recordingAcker{}})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	openSharedGap(t, first.Router(), key)
	if !first.Router().StreamStale(StreamJournal) {
		t.Fatalf("precondition: the gap did not stale the journal channel")
	}

	// The process dies; the state file does not.
	restarted, err := Resume(Config{State: mem, Ack: &recordingAcker{}})
	if err != nil {
		t.Fatalf("Resume after the restart: %v", err)
	}
	if !restarted.Router().StreamStale(StreamJournal) || !restarted.Router().StreamStale(StreamTerminal) {
		t.Errorf("after a process death the restored phone reports journal stale=%v terminal=%v; "+
			"both were stale when it died. A flag that lives only in memory means every Android "+
			"process death silently promotes a known-holed stream back to live",
			restarted.Router().StreamStale(StreamJournal), restarted.Router().StreamStale(StreamTerminal))
	}
}

// ---------------------------------------------------------------------------
// PB-SYNC-8: the reseed REPLACES the cache cursor.
// ---------------------------------------------------------------------------

// TestS10_AReseedReplacesTheCacheCursorRatherThanMergingIntoIt is the standing class (iv)
// defect the requirement was written around: satisfiable while the defect ships. Merge the
// reseed into the live cache and SessionCache.Apply drops every roster record -- they carry
// Cursor 0, and the cache cursor has already advanced -- so the repair "succeeds", nothing
// errors, and the phone stays exactly as stale as it was.
func TestS10_AReseedReplacesTheCacheCursorRatherThanMergingIntoIt(t *testing.T) {
	key := testContentKey()
	_, r := s10Router(t)

	// One live event advances the cache cursor well past zero. This is the ONLY thing the
	// defect needs: from here, every roster record the machine will ever send is discarded.
	if _, err := r.AcceptCommit(sealFrameFrom(t, key, machineSender, 7, 1,
		marshalEvent(t, schema.JournalRecord{Cursor: 42, SessionID: "m1/s-gone", Type: "launched"})), 101); err != nil {
		t.Fatalf("seed a live event: %v", err)
	}
	if got := r.Sessions().Cursor(); got != 42 {
		t.Fatalf("precondition: the cache cursor is %d, want 42 -- the reseed below is only a "+
			"no-op once the cursor has advanced past the roster records' zero", got)
	}

	if _, err := r.AcceptCommit(sealFrameFrom(t, key, machineSender, 7, 2, []byte(wireJournalReseed)), 102); err != nil {
		t.Fatalf("accept the journal reseed: %v", err)
	}

	for _, id := range []string{"m1/s-alpha", "m1/s-beta"} {
		if _, ok := r.Sessions().Get(id); !ok {
			t.Errorf("session %q is absent from the roster after a reseed that named it. Its "+
				"roster record carries Cursor 0 (the daemon leaves it deliberately unset) and the "+
				"cache cursor is already 42, so SessionCache.Apply drops it as a stale replay -- "+
				"the designated journal repair channel is a silent no-op, the resync reports "+
				"success and the phone stays stale. Reconcile-adopted Running sessions have no "+
				"other enumeration path at all, so they are permanently invisible", id)
		}
	}
	if got := r.Sessions().Cursor(); got != 50 {
		t.Errorf("the cache cursor is %d after a reseed whose snapshot boundary is 50. A reseed "+
			"REPLACES the cursor: leaving it at the higher pre-repair value is what makes every "+
			"subsequent roster snapshot dead on arrival too", got)
	}
	// The reseed's own Events ride on top of the roster, so the record is the merged one.
	if cs, ok := r.Sessions().Get("m1/s-beta"); !ok || cs.Group != status.GroupNeedsInput {
		t.Errorf("m1/s-beta = %+v (present=%v); the reseed's events must be applied ON TOP of its "+
			"roster -- that is what makes it the ATOMIC roster+events snapshot PB-SYNC-2 names, "+
			"rather than a roster that is already out of date when it lands", cs, ok)
	}
}

// TestS10_AReseedReplacesTheSessionSetNotJustTheCursor is the other half of "replace". A
// session the machine no longer lists ended while the phone was not listening; carrying it
// across the repair leaves a dead session on the roster with a live-looking group, which is
// the same lie the stale flag exists to prevent.
func TestS10_AReseedReplacesTheSessionSetNotJustTheCursor(t *testing.T) {
	key := testContentKey()
	_, r := s10Router(t)

	if _, err := r.AcceptCommit(sealFrameFrom(t, key, machineSender, 7, 1,
		marshalEvent(t, schema.JournalRecord{Cursor: 42, SessionID: "m1/s-gone", Type: "launched", Group: status.GroupWorking})), 101); err != nil {
		t.Fatalf("seed a session the reseed will not list: %v", err)
	}
	if _, err := r.AcceptCommit(sealFrameFrom(t, key, machineSender, 7, 2, []byte(wireJournalReseed)), 102); err != nil {
		t.Fatalf("accept the journal reseed: %v", err)
	}

	if cs, ok := r.Sessions().Get("m1/s-gone"); ok {
		t.Errorf("m1/s-gone = %+v is still on the roster after a reseed that did not list it. The "+
			"reseed is the machine's COMPLETE live set as of its cursor (PB-SYNC-2's atomic "+
			"snapshot); a session missing from it is a session that ended, and merging keeps it "+
			"on the phone's screen -- working -- for the life of the install", cs)
	}
}

// An empty roster is still authoritative. Rows and Cursor are both legitimately zero for an
// empty machine, so neither can distinguish it from the cache between pairing and first sync.
func TestS10_EmptyReseedAdvancesTheDurableRosterRevision(t *testing.T) {
	core, router := s10Router(t)
	key := testContentKey()
	plain, err := json.Marshal(reseedFrame{
		Kind:          kindJournalReseed,
		JournalReseed: schema.JournalReseed{Roster: []schema.JournalRecord{}, Cursor: 0},
	})
	if err != nil {
		t.Fatalf("marshal empty reseed: %v", err)
	}

	if _, err := router.AcceptCommit(sealFrameFrom(t, key, machineSender, 7, 1, plain), 101); err != nil {
		t.Fatalf("accept empty reseed: %v", err)
	}
	if got := core.State().RosterRevision; got != 1 {
		t.Fatalf("RosterRevision after authoritative empty reseed = %d, want 1; zero rows and cursor zero must still end first-sync", got)
	}

	if _, err := router.AcceptCommit(sealFrameFrom(t, key, machineSender, 7, 2, plain), 102); err != nil {
		t.Fatalf("accept second empty reseed: %v", err)
	}
	if got := core.State().RosterRevision; got != 2 {
		t.Fatalf("RosterRevision after second reseed = %d, want 2; pull refresh needs a generation it can reconcile against", got)
	}
}
