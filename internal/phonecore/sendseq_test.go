package phonecore

// FAILING-FIRST (TDD RED, GG-5) tests for PB-STATE-3 (durable send-seq without an fsync
// per keystroke) and PB-STATE-8 (who absorbs the burned gap).
//
// Today Sequencer.Next() is `s.n.Add(1)` over a bare atomic.Uint64 (input.go:33-36): one
// process death and the phone re-issues seq 1 under the same epoch, which the gateway's
// now-durable inbound high-water (PB-GW-1) refuses as stale forever.
//
// The fix mirrors internal/remotegw/seqstore.go: reserve a CEILING, hand out the block
// from memory, resume at the persisted ceiling. That deliberately burns the unused tail
// of a block on every restart -- and PB-STATE-8 is the consequence the gateway makes
// dangerous. `routeInput` DROPS an input frame whose Gap bit is set
// (command_loop.go:331-334) and ignores Gap on commands, so if the first post-restart
// frame is a keystroke it vanishes with no signal anywhere. The burned gap must be
// absorbed by the re-lease command frame.
//
// THE SEAM THESE TESTS PIN:
//
//	const SeqReserveBlock uint64 = 256           // §6.0's binding value
//	var ErrGapPending error
//	func (*Sequencer) NextCommand() (uint64, error)
//	func (*Sequencer) NextInput() (uint64, error)
//	func (*Sequencer) GapPending() bool
//
// The ceiling lives in State.SendSeq (keyed per epoch), so there is ONE persisted schema
// as PB-STATE-1 requires -- not a second side file. Next() is left alone: it is the
// in-memory allocator the existing tests and phonesim drive, and it cannot report a
// failed reservation, which is exactly why the durable path needs its own two methods.

import (
	"errors"
	"math/rand"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remotegw"
)

// countingStore wraps a Store and counts Save calls. A Save is the phone's fsync
// (temp + fsync + rename + dir fsync, the idiom remotegw.writeFileAtomic pins), so the
// count IS the fsync count PB-STATE-3 bounds.
type countingStore struct {
	inner Store
	saves int
}

func (c *countingStore) Load() State { return c.inner.Load() }
func (c *countingStore) Save(s State) error {
	c.saves++
	return c.inner.Save(s)
}
func (c *countingStore) PurgeKeys() error                   { return c.inner.PurgeKeys() }
func (c *countingStore) UnsealContent() error               { return c.inner.UnsealContent() }
func (c *countingStore) RewindRelayCursor() error           { return c.inner.RewindRelayCursor() }
func (c *countingStore) SetRelayIncarnation(v string) error { return c.inner.SetRelayIncarnation(v) }

// failAfterNStore commits the first n Saves and fails every one after that -- "the
// process died before anything else hit disk", the injection idiom
// remotegw/inbound_crash_matrix_test.go uses (memInboundState.failSave).
type failAfterNStore struct {
	inner Store
	n     int
	saves int
}

var errStoreDied = errors.New("phonecore test: the process died at this boundary")

func (f *failAfterNStore) Load() State { return f.inner.Load() }
func (f *failAfterNStore) Save(s State) error {
	f.saves++
	if f.saves > f.n {
		return errStoreDied
	}
	return f.inner.Save(s)
}
func (f *failAfterNStore) PurgeKeys() error                   { return f.inner.PurgeKeys() }
func (f *failAfterNStore) UnsealContent() error               { return f.inner.UnsealContent() }
func (f *failAfterNStore) RewindRelayCursor() error           { return f.inner.RewindRelayCursor() }
func (f *failAfterNStore) SetRelayIncarnation(v string) error { return f.inner.SetRelayIncarnation(v) }

// memStore is a non-durable Store standing in for the phone's state file across a
// simulated crash: the process dies, the file does not. Reusing ONE memStore across two
// Resume calls models a restart against surviving state, exactly as the S2 crash matrix
// reuses one memInboundState across two bridges.
type memStore struct{ st State }

func (m *memStore) Load() State        { return m.st }
func (m *memStore) Save(s State) error { m.st = s; return nil }
func (m *memStore) PurgeKeys() error {
	m.st.Keys = crypto.EpochKeys{}
	m.st.Snapshots, m.st.Sessions = nil, nil
	return nil
}

// memStore keeps nothing sealed, so there is nothing to re-open: it stands in for a phone
// with no durable custody at all, which is the one shape a fresh unwrap cannot help.
func (m *memStore) UnsealContent() error { return nil }

// memStore has no merge rule to defeat, so the rewind is the assignment itself.
func (m *memStore) RewindRelayCursor() error           { m.st.RelayCursor = 0; return nil }
func (m *memStore) SetRelayIncarnation(v string) error { m.st.RelayIncarnation = v; return nil }

// resumeSeq builds a Core over st and returns its sequencer, for the epoch already
// recorded in the state.
func resumeSeq(t *testing.T, st Store) *Sequencer {
	t.Helper()
	c, err := Resume(Config{State: st})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	return c.Seq()
}

// TestSendSeq_ReservesABlockRatherThanFsyncingPerKeystroke pins the mechanism PB-STATE-3
// mandates and the budget it exists to protect. S2 measured the equivalent gateway fsync
// at 13-15 ms on this host; at §6.0's <= 8 input frames/s a per-frame persist would spend
// ~120 ms of the p50 <= 150 ms typing budget on the phone alone.
func TestSendSeq_ReservesABlockRatherThanFsyncingPerKeystroke(t *testing.T) {
	if SeqReserveBlock != 256 {
		t.Fatalf("SeqReserveBlock = %d; want 256 (§6.0 binds the value; changing it needs committee agreement)", SeqReserveBlock)
	}
	cs := &countingStore{inner: &memStore{}}
	seq := resumeSeq(t, cs)

	const draws = 1000
	for i := 0; i < draws; i++ {
		if _, err := seq.NextInput(); err != nil {
			t.Fatalf("NextInput #%d: %v", i+1, err)
		}
	}
	// Four blocks of 256 cover 1000 draws. Anything materially above that is persisting
	// per keystroke.
	if maxSaves := int(draws/SeqReserveBlock) + 1; cs.saves > maxSaves {
		t.Fatalf("%d draws cost %d Saves; want <= %d (one reservation per block of %d, not one per keystroke)",
			draws, cs.saves, maxSaves, SeqReserveBlock)
	}
	if cs.saves == 0 {
		t.Fatalf("%d draws cost 0 Saves; nothing was reserved durably, so a crash re-issues every seq", draws)
	}
}

// TestSendSeq_NeverReusesASeqAcrossACrashAtAnyPointInTheWindow is PB-STATE-3's acceptance
// criterion verbatim -- "at any point in the reservation window, including a crash between
// reservation and use". Each generation draws a random count that straddles block
// boundaries, then dies without any clean shutdown (the Core is simply dropped, mirroring
// SIGKILL). Every seq ever issued must be strictly greater than every seq issued before
// it, across all generations.
func TestSendSeq_NeverReusesASeqAcrossACrashAtAnyPointInTheWindow(t *testing.T) {
	st := &memStore{}
	rng := rand.New(rand.NewSource(1))
	seen := map[uint64]int{}
	var highest uint64

	for gen := 1; gen <= 25; gen++ {
		seq := resumeSeq(t, st)
		// 0 draws is the "crash between reservation and use" case in its purest form:
		// the previous generation reserved a block it never finished spending.
		for i, n := 0, rng.Intn(400); i < n; i++ {
			var (
				got uint64
				err error
			)
			if i == 0 {
				got, err = seq.NextCommand() // absorbs any burned gap (PB-STATE-8)
			} else {
				got, err = seq.NextInput()
			}
			if err != nil {
				t.Fatalf("gen %d draw %d: %v", gen, i, err)
			}
			if prev, dup := seen[got]; dup {
				t.Fatalf("gen %d re-issued seq %d, first issued in gen %d -- the gateway refuses it as a replay forever", gen, got, prev)
			}
			if got <= highest {
				t.Fatalf("gen %d issued seq %d after a crash at highest=%d; the send-seq must be strictly increasing across restarts", gen, got, highest)
			}
			seen[got], highest = gen, got
		}
	}
}

// TestSendSeq_ResumesAtTheReservedCeilingNotTheLastIssuedSeq pins the resume arithmetic
// exactly, which is what makes the previous test non-vacuous: an implementation that
// persisted the last ISSUED seq instead of the reserved CEILING would also be strictly
// increasing in the happy path, yet would reuse the whole unflushed tail of a block after
// a power loss. The burn is one block remainder, no more and no less.
func TestSendSeq_ResumesAtTheReservedCeilingNotTheLastIssuedSeq(t *testing.T) {
	for _, tc := range []struct{ draws, wantNext uint64 }{
		{draws: 1, wantNext: SeqReserveBlock + 1},
		{draws: SeqReserveBlock, wantNext: SeqReserveBlock + 1},
		{draws: SeqReserveBlock + 1, wantNext: 2*SeqReserveBlock + 1},
	} {
		st := &memStore{}
		seq := resumeSeq(t, st)
		var last uint64
		for i := uint64(0); i < tc.draws; i++ {
			got, err := seq.NextCommand()
			if err != nil {
				t.Fatalf("draw %d: %v", i+1, err)
			}
			last = got
		}
		if last != tc.draws {
			t.Fatalf("%d draws ended at seq %d; want %d (a fresh epoch starts at 1)", tc.draws, last, tc.draws)
		}

		// CRASH: the Core is dropped with no clean shutdown.
		got, err := resumeSeq(t, st).NextCommand()
		if err != nil {
			t.Fatalf("post-crash draw after %d: %v", tc.draws, err)
		}
		if got != tc.wantNext {
			t.Fatalf("after %d draws and a crash the next seq = %d; want %d (resume at the persisted ceiling, burning only the block remainder)",
				tc.draws, got, tc.wantNext)
		}
	}
}

// TestSendSeq_ReservesOnFirstUseNotOnOpen is the Android-shaped case: the OS starts and
// kills the process constantly, and most launches never type. A reservation taken at open
// would burn 256 seqs per launch and walk the counter away from the gateway's high-water
// for no traffic at all -- while a source that reserves nothing at all reuses seq 1 on
// every launch, which is today's defect. Both halves are asserted here so neither
// direction can be satisfied on its own.
func TestSendSeq_ReservesOnFirstUseNotOnOpen(t *testing.T) {
	st := &memStore{}
	for i := 0; i < 5; i++ {
		resumeSeq(t, st) // opened, never drawn from, then killed
	}
	got, err := resumeSeq(t, st).NextCommand()
	if err != nil {
		t.Fatalf("NextCommand after 5 empty launches: %v", err)
	}
	if got != 1 {
		t.Fatalf("first seq after 5 launches that never typed = %d; want 1 (reserve on first use, never on open)", got)
	}
	// That single draw DID reserve, so the next launch resumes above the whole block.
	next, err := resumeSeq(t, st).NextCommand()
	if err != nil {
		t.Fatalf("NextCommand after the drawing launch died: %v", err)
	}
	if next != SeqReserveBlock+1 {
		t.Fatalf("first seq after a launch that drew once = %d; want %d (the block it reserved is burned, never re-handed-out)", next, SeqReserveBlock+1)
	}
}

// TestSendSeq_ReservationFailureIssuesNoSeq is the fail-closed rule the gateway's
// SeqSource states in its own doc: a source that cannot durably reserve must issue NO
// seq, because handing one out anyway is precisely how a seq gets reused across a
// restart. A full or read-only state dir is the real-world trigger.
func TestSendSeq_ReservationFailureIssuesNoSeq(t *testing.T) {
	st := &failAfterNStore{inner: &memStore{}, n: 0}
	seq := resumeSeq(t, st)
	got, err := seq.NextCommand()
	if !errors.Is(err, errStoreDied) {
		t.Fatalf("NextCommand with a failing store = (%d, %v); want the persist error surfaced", got, err)
	}
	if got != 0 {
		t.Fatalf("NextCommand returned seq %d alongside its error; a seq that was never durably reserved must not be issued", got)
	}
}

// TestSendSeq_IsKeyedPerEpoch is standing review question 1 ("what if there is more than
// one?") applied to the send counter. RevokeDevice rotates the epoch
// (skeleton/api.go:231) and the gateway keys its durable inbound high-water per
// (sender, EPOCH) -- a rotated epoch legitimately restarts at seq 1, which is exactly
// why S2's reviewer rejected a scalar. The phone's ceiling must be keyed the same way, or
// applying a reconcile authority stamped with one epoch silently moves another epoch's
// counter (the "reconcile arm does not validate Machine/EpochID" residual recorded in
// remote-phaseB-s1b-evidence.md).
func TestSendSeq_IsKeyedPerEpoch(t *testing.T) {
	st := &memStore{}
	c1, err := Resume(Config{State: st})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	base := c1.State()
	base.EpochID = 7
	if err := c1.Save(base); err != nil {
		t.Fatalf("Save epoch 7: %v", err)
	}
	seq7 := resumeSeq(t, st)
	for i := 0; i < 3; i++ {
		if _, err := seq7.NextCommand(); err != nil {
			t.Fatalf("epoch 7 draw %d: %v", i, err)
		}
	}

	// A revoke rotates the epoch. The phone adopts the new grant.
	c2, err := Resume(Config{State: st})
	if err != nil {
		t.Fatalf("Resume after rotation: %v", err)
	}
	rotated := c2.State()
	rotated.EpochID = 8
	if err := c2.Save(rotated); err != nil {
		t.Fatalf("Save epoch 8: %v", err)
	}

	got, err := resumeSeq(t, st).NextCommand()
	if err != nil {
		t.Fatalf("first draw in epoch 8: %v", err)
	}
	if got != 1 {
		t.Fatalf("first seq in the rotated epoch = %d; want 1 (a new (sender,epoch) stream at the gateway starts unseen; carrying epoch 7's ceiling across is the scalar defect S2 rejected)", got)
	}
	if ceil := st.Load().SendSeq[7]; ceil < 3 {
		t.Fatalf("epoch 7's persisted ceiling = %d after the rotation; want it retained (>= 3) so a re-grant of that epoch cannot reuse its seqs", ceil)
	}
}

// TestGapIsAbsorbedByTheRelease_NotByAKeystroke is PB-STATE-8, asserted AT THE GATEWAY
// GUARD that enforces it. The gateway computes `gap := seen && Seq > hi+1`
// (crypto/envelope.go) and `routeInput` drops a gapped input frame silently, so this must
// be checked on the real remotegw.OpenMailboxFrame result -- not on a phone-side belief
// about what it sent.
func TestGapIsAbsorbedByTheRelease_NotByAKeystroke(t *testing.T) {
	const epoch = uint32(7)
	key := testContentKey()
	recv := gatewayReceiver()
	st := &memStore{}

	// Pre-crash: a lease and one keystroke, both accepted by the gateway.
	seq1 := resumeSeq(t, st)
	for i := 0; i < 2; i++ {
		n, err := seq1.NextCommand()
		if err != nil {
			t.Fatalf("pre-crash draw: %v", err)
		}
		raw, err := SealInputData(key, epoch, n, "m1/s1", []byte("a"))
		if err != nil {
			t.Fatalf("seal: %v", err)
		}
		if _, err := remotegw.OpenMailboxFrame(recv, key, raw); err != nil {
			t.Fatalf("gateway rejected a pre-crash frame at seq %d: %v", n, err)
		}
	}

	// CRASH + RESTART. The reservation ceiling burns the rest of the block, so the next
	// frame the phone sends will carry a Gap at the gateway.
	seq2 := resumeSeq(t, st)
	if !seq2.GapPending() {
		t.Fatalf("GapPending() = false after a restart that burned a reservation block; the phone must know it owes a re-lease")
	}
	if _, err := seq2.NextInput(); !errors.Is(err, ErrGapPending) {
		t.Fatalf("NextInput() while a gap is outstanding = %v; want ErrGapPending -- routeInput drops a gapped keystroke SILENTLY, so the first post-restart keystroke would vanish with no signal", err)
	}

	// The re-lease command absorbs it. Commands ignore Gap at the gateway.
	relSeq, err := seq2.NextCommand()
	if err != nil {
		t.Fatalf("NextCommand for the re-lease: %v", err)
	}
	relRaw, err := SealCommandEnvelope(key, epoch, relSeq, takeControlAuth())
	if err != nil {
		t.Fatalf("seal re-lease: %v", err)
	}
	relFrame, err := remotegw.OpenMailboxFrame(recv, key, relRaw)
	if err != nil {
		t.Fatalf("gateway rejected the re-lease: %v", err)
	}
	if relFrame.Kind != remotegw.FrameCommand {
		t.Fatalf("re-lease opened as kind %v; want FrameCommand", relFrame.Kind)
	}
	if !relFrame.Gap {
		t.Logf("note: the re-lease carried no gap; the burn was absorbed before it reached the wire, which also satisfies PB-STATE-8")
	}
	if seq2.GapPending() {
		t.Fatalf("GapPending() is still true after the re-lease command absorbed it")
	}

	// PB-STATE-8's acceptance criterion: the FIRST post-restart input frame carries no Gap.
	inSeq, err := seq2.NextInput()
	if err != nil {
		t.Fatalf("NextInput after the re-lease: %v", err)
	}
	inRaw, err := SealInputData(key, epoch, inSeq, "m1/s1", []byte("ls\r"))
	if err != nil {
		t.Fatalf("seal keystroke: %v", err)
	}
	inFrame, err := remotegw.OpenMailboxFrame(recv, key, inRaw)
	if err != nil {
		t.Fatalf("gateway rejected the first post-restart keystroke: %v", err)
	}
	if inFrame.Kind != remotegw.FrameInput {
		t.Fatalf("keystroke opened as kind %v; want FrameInput", inFrame.Kind)
	}
	if inFrame.Gap {
		t.Fatalf("the first post-restart input frame carries the Gap bit; routeInput DROPS it (command_loop.go:331-334) and the user's first keystroke disappears with no signal")
	}
}

// TestOperationGapForcesOutcomeReconciliation is PB-STATE-8's second half: "High-level
// operation gaps must trigger durable outcome reconciliation before later state is
// trusted." A burned gap means the phone cannot know whether the ops it had in flight
// reached the daemon, and their replies ride the command-reply bucket -- so that bucket is
// marked stale and the ops stay UNRESOLVED until their durable outcome is recorded
// (PB-SYNC-2: "command replies via the durable operation outcome, or the stream stays
// unresolved").
func TestOperationGapForcesOutcomeReconciliation(t *testing.T) {
	st := &memStore{}
	c1, err := Resume(Config{State: st})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	base := c1.State()
	base.EpochID = 7
	base.PendingOps = []QueuedOp{{Op: "kill", SessionID: "m1/s1", Cmd: takeControlAuth()}}
	if err := c1.Save(base); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := c1.Seq().NextCommand(); err != nil {
		t.Fatalf("draw: %v", err)
	}

	// CRASH + RESTART: a block was reserved and abandoned, so a gap is owed.
	c2, err := Resume(Config{State: st})
	if err != nil {
		t.Fatalf("Resume after crash: %v", err)
	}
	if !c2.Seq().GapPending() {
		t.Fatalf("GapPending() = false after the crash; nothing forces reconciliation")
	}
	if got := c2.UnresolvedOps(); len(got) != 1 {
		t.Fatalf("UnresolvedOps() across an operation gap = %+v; want the in-flight op, whose fate the gap makes unknown", got)
	}
	if !c2.Router().Stale(replyBucket(7)) {
		t.Fatalf("the command-reply bucket is not stale after an operation gap; later state would be trusted without reconciling the outcome")
	}
	if err := c2.RecordOutcome(takeControlReply()); err != nil {
		t.Fatalf("RecordOutcome: %v", err)
	}
	if got := c2.UnresolvedOps(); len(got) != 0 {
		t.Fatalf("UnresolvedOps() after the durable outcome landed = %+v; want empty", got)
	}
}
