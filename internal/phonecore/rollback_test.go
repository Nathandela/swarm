package phonecore

// FAILING-FIRST (TDD RED, GG-5) tests for PB-STATE-4: durable state that is rolled back
// fails CLOSED, against a named trust anchor, PER COORDINATE.
//
// AEAD and atomic writes detect corruption, not rollback: a valid older blob sealed by
// the same Keystore key stays valid, and KeyMint rollback-resistance protects key blobs,
// not arbitrary app state. So the anchor is remote and authenticated -- the reconcile
// record slice S1b shipped (schema.ReconcileRecord, demuxed by MailboxRouter) -- with a
// DISTINCT authority per coordinate:
//
//	(a) phone send-seq            -> InboundHighWater  (the gateway's durable inbound
//	                                 accepted high-water, PB-GW-1)
//	(b) receive high-waters       -> JournalCeiling for the shared journal/terminal
//	                                 bucket, ReplyCeiling for the sender-zero
//	                                 command-reply bucket (outbound-reply.seq, NOT the
//	                                 journal outbox)
//	(c) grant watermark           -> GrantEpoch + GrantSeq
//
// v3.2 named the gateway's inbound high-water as THE authority for all three; it carries
// no information about the other two, so an implementation could pass while rollback
// still reset them. These tests therefore exercise each coordinate separately.
//
// THE ROLLBACK IS REAL, per PB-STATE-4's own constraint ("the test may not rely on hidden
// state unavailable after a real rollback"): the durable state is snapshotted, the phone
// runs on, the snapshot is restored WHOLESALE behind the app's back, and a FRESH Core is
// built from it. Nothing in memory survives to supply an answer no rolled-back phone would
// have. The fail-closed test does the same at the byte level, over the state DIRECTORY,
// so the on-disk form is covered too and no test needs to know the file layout.
//
// THE SEAM THESE TESTS PIN, beyond state_test.go's:
//
//	func (*Core) Reconcile() error   // adopt the router's record into every coordinate
//	                                 // and persist it; ErrUnreconciled until one lands

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// rollbackFixture is the paired phone plus the machine key material the grant coordinate
// needs.
//
// The rollback target is the DURABLE STATE ITSELF, replaced wholesale behind the app's
// back -- which is what a filesystem snapshot, a restored backup or a rolled-back Keystore
// blob does. It cannot be modelled by calling Save with older values: durable custody is
// monotonic precisely so a caller cannot walk a replay guard backwards, so a real rollback
// necessarily bypasses the write API. Every Core below is rebuilt from that state alone,
// so nothing survives that a rolled-back phone would not have.
//
// That the state really does live on disk under Config.Dir, byte for byte, is asserted
// separately by TestState_EveryResumeCriticalFieldSurvivesARestart and by
// TestRollback_FailsClosedForMutatingOpsAndMarksChannelsStale, which does its rollback by
// restoring the state DIRECTORY.
type rollbackFixture struct {
	dir string
	// PB-KEY-9: Resume fails closed with no sealer, and the directory-level restart below
	// must present the SAME KEKs -- a different KEK is a different device.
	wake, content Sealer
	store         *memStore
	key           crypto.ContentKey
	machinePub    ed25519.PublicKey
	machinePriv   ed25519.PrivateKey
	epochKeys     crypto.EpochKeys
}

func newRollbackFixture(t *testing.T) *rollbackFixture {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("machine key: %v", err)
	}
	keys, err := crypto.NewEpochKeys()
	if err != nil {
		t.Fatalf("epoch keys: %v", err)
	}
	keys.ContentKey = testContentKey()

	f := &rollbackFixture{
		dir: t.TempDir(), store: &memStore{}, key: testContentKey(),
		wake: s14aNewSealer(t), content: s14aNewSealer(t),
		machinePub: pub, machinePriv: priv, epochKeys: keys,
	}
	f.store.st = State{
		Machine: "m1", RoutingID: "rid-m1", EpochID: 7, Keys: keys, MachineSignPub: pub,
	}
	// The same pairing, on disk, for the directory-level rollback test.
	c, err := Resume(Config{Dir: f.dir, WakeSealer: f.wake, ContentSealer: f.content})
	if err != nil {
		t.Fatalf("Resume (pairing): %v", err)
	}
	if err := c.Save(f.store.st); err != nil {
		t.Fatalf("Save paired state: %v", err)
	}
	return f
}

// snapshot copies the durable state whole, maps included.
func (f *rollbackFixture) snapshot() State {
	s := f.store.st
	s.SendSeq = maps.Clone(s.SendSeq)
	s.Receive = maps.Clone(s.Receive)
	s.Stale = maps.Clone(s.Stale)
	s.OpOutcomes = maps.Clone(s.OpOutcomes)
	s.Sessions = slices.Clone(s.Sessions)
	s.Snapshots = slices.Clone(s.Snapshots)
	s.PendingOps = slices.Clone(s.PendingOps)
	return s
}

// rollback replaces the durable state with that snapshot and returns a FRESH Core over it.
func (f *rollbackFixture) rollback(t *testing.T, snap State) *Core {
	t.Helper()
	f.store.st = snap
	return f.resume(t)
}

// snapshotDir copies every regular file in the state directory -- the byte-level form of
// the same operation, used by the directory-level test. It makes no assumption about how
// the implementer lays the state out.
func (f *rollbackFixture) snapshotDir(t *testing.T) map[string][]byte {
	t.Helper()
	snap := map[string][]byte{}
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		t.Fatalf("read state dir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(f.dir, e.Name()))
		if err != nil {
			t.Fatalf("snapshot %s: %v", e.Name(), err)
		}
		snap[e.Name()] = b
	}
	return snap
}

// rollbackDir restores that snapshot byte for byte -- removing anything written since --
// and returns a FRESH Core built from the directory alone.
func (f *rollbackFixture) rollbackDir(t *testing.T, snap map[string][]byte) *Core {
	t.Helper()
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		t.Fatalf("read state dir: %v", err)
	}
	for _, e := range entries {
		if _, kept := snap[e.Name()]; !kept && !e.IsDir() {
			if err := os.Remove(filepath.Join(f.dir, e.Name())); err != nil {
				t.Fatalf("remove %s: %v", e.Name(), err)
			}
		}
	}
	for name, b := range snap {
		if err := os.WriteFile(filepath.Join(f.dir, name), b, 0o600); err != nil {
			t.Fatalf("restore %s: %v", name, err)
		}
	}
	c, err := Resume(Config{Dir: f.dir, Machine: "m1", WakeSealer: f.wake, ContentSealer: f.content})
	if err != nil {
		t.Fatalf("Resume after the directory rollback: %v", err)
	}
	return c
}

// resume opens the phone at its current durable state.
func (f *rollbackFixture) resume(t *testing.T) *Core {
	t.Helper()
	c, err := Resume(Config{State: f.store, Machine: "m1"})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	return c
}

// deliverReconcile seals the pinned reconcile record onto the shared journal bucket at
// its own JournalCeiling seq -- which is what makes that ceiling self-certifying -- and
// hands it to the router.
func (f *rollbackFixture) deliverReconcile(t *testing.T, c *Core) {
	t.Helper()
	rec := wantReconcileRecord()
	raw := sealFrameFrom(t, f.key, machineSender, 7, rec.JournalCeiling, []byte(wireReconcileFrame))
	if _, err := c.Router().AcceptCommit(raw, 100); err != nil {
		t.Fatalf("deliver the reconcile record: %v -- an AEAD failure here means Resume restored no epoch content key, so the phone cannot open ANY machine frame; that is PB-STATE-1's first enumerated coordinate and it gates every authority below", err)
	}
}

// TestRollback_FailsClosedForMutatingOpsAndMarksChannelsStale is the first half of
// PB-STATE-4: with no authority in hand the phone cannot know whether any coordinate was
// rolled back, so authoring a mutating op is unsafe. It must refuse, mark the affected
// channels stale, and -- PB-STATE-10 -- recover the moment the authority lands, never
// latch.
func TestRollback_FailsClosedForMutatingOpsAndMarksChannelsStale(t *testing.T) {
	f := newRollbackFixture(t)
	before := f.snapshotDir(t)

	c := f.rollbackDir(t, before)
	// The restored bytes must actually be read back, or nothing below is about rollback.
	if got := c.State().Machine; got != "m1" {
		t.Errorf("State().Machine after restoring the state directory = %q; want \"m1\" -- Resume is not reading the durable bytes at all, so no rollback question can even be posed", got)
	}
	if err := c.Router().RequireReconciled(); !errors.Is(err, ErrUnreconciled) {
		t.Fatalf("RequireReconciled() on a rolled-back phone = %v; want ErrUnreconciled (fail closed for mutating ops)", err)
	}
	if err := c.Reconcile(); !errors.Is(err, ErrUnreconciled) {
		t.Fatalf("Reconcile() with no record = %v; want ErrUnreconciled", err)
	}
	for name, b := range map[string]Bucket{"journal/terminal": journalBucket(7), "command-reply": replyBucket(7)} {
		if !c.Router().Stale(b) {
			t.Errorf("the %s bucket is not marked stale while the authority is unreachable; PB-STATE-4 requires the affected channels be marked", name)
		}
	}

	f.deliverReconcile(t, c)
	if err := c.Reconcile(); err != nil {
		t.Fatalf("Reconcile() once the record landed = %v; want nil (a permanent refusal is the brick PB-SYNC-7 exists to prevent)", err)
	}
	if err := c.Router().RequireReconciled(); err != nil {
		t.Fatalf("RequireReconciled() after reconciling = %v; want nil", err)
	}
	for name, b := range map[string]Bucket{"journal/terminal": journalBucket(7), "command-reply": replyBucket(7)} {
		if c.Router().Stale(b) {
			t.Errorf("the %s bucket is still stale after a successful reconcile; the phone would never trust its own caches again", name)
		}
	}
}

// TestRollback_SendSeqResumesAboveTheGatewayInboundHighWater is coordinate (a). A
// rolled-back send-seq is the §4.3 brick with no local symptom whatsoever: the phone
// seals happily and the gateway refuses every frame as stale.
func TestRollback_SendSeqResumesAboveTheGatewayInboundHighWater(t *testing.T) {
	f := newRollbackFixture(t)
	before := f.snapshot()

	// The phone runs on: it types enough to push the gateway's inbound high-water to 42.
	live := f.resume(t)
	for i := 0; i < 42; i++ {
		if _, err := live.Seq().NextCommand(); err != nil {
			t.Fatalf("pre-rollback draw %d: %v", i, err)
		}
	}

	// ROLLBACK to the snapshot: the phone's counter is back at zero, the gateway's is not.
	rolled := f.rollback(t, before)
	f.deliverReconcile(t, rolled)
	if err := rolled.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	rec := wantReconcileRecord()
	got, err := rolled.Seq().NextCommand()
	if err != nil {
		t.Fatalf("NextCommand after reconciling: %v", err)
	}
	if got <= rec.InboundHighWater {
		t.Fatalf("first seq after a rollback + reconcile = %d; want > %d (the gateway's durable inbound high-water refuses everything at or below it)", got, rec.InboundHighWater)
	}

	// The resume must be DURABLE, not a patch to a live object: a further process death
	// must not drop the phone back below the authority.
	after := f.resume(t)
	got2, err := after.Seq().NextCommand()
	if err != nil {
		t.Fatalf("NextCommand after a restart: %v", err)
	}
	if got2 <= got {
		t.Fatalf("seq after restarting a reconciled phone = %d; want > %d (Reconcile must persist, not only repair memory)", got2, got)
	}
}

// TestRollback_ReceiveHighWatersRefuseRetainedFramesPerBucket is coordinate (b), tested
// per bucket exactly as PB-STATE-4 demands. The shared journal/terminal bucket is
// reseeded by the reconcile frame's OWN seq; the sender-zero command-reply bucket is a
// different stream that the frame's arrival does not touch, so it needs ReplyCeiling.
// An implementation that seeded only the bucket it happened to arrive on leaves every
// retained command reply replayable.
func TestRollback_ReceiveHighWatersRefuseRetainedFramesPerBucket(t *testing.T) {
	f := newRollbackFixture(t)
	before := f.snapshot()
	rec := wantReconcileRecord()

	// The phone runs on and consumes machine frames on both buckets.
	live := f.resume(t)
	if _, err := live.Router().AcceptCommit(sealFrameFrom(t, f.key, machineSender, 7, 2, marshalSnapshot(t, "m1/s1", []string{"pre"}, 80, 24)), 10); err != nil {
		t.Fatalf("pre-rollback journal-bucket frame: %v", err)
	}
	if _, err := live.Router().AcceptCommit(sealFrame(t, f.key, 5, marshalReply(t, takeControlReply())), 11); err != nil {
		t.Fatalf("pre-rollback reply-bucket frame: %v", err)
	}

	// ROLLBACK. Both receive high-waters are back to zero and a retaining relay still
	// holds every frame above them.
	rolled := f.rollback(t, before)
	f.deliverReconcile(t, rolled)
	if err := rolled.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// Journal/terminal bucket: at or below JournalCeiling is refused, above is accepted.
	retainedJournal := sealFrameFrom(t, f.key, machineSender, 7, rec.JournalCeiling, marshalSnapshot(t, "m1/s1", []string{"retained"}, 80, 24))
	if _, err := rolled.Router().AcceptCommit(retainedJournal, 30); !errors.Is(err, crypto.ErrStaleSeq) {
		t.Fatalf("retained journal-bucket frame at seq %d = %v; want crypto.ErrStaleSeq", rec.JournalCeiling, err)
	}
	liveJournal := sealFrameFrom(t, f.key, machineSender, 7, rec.JournalCeiling+1, marshalSnapshot(t, "m1/s1", []string{"live"}, 80, 24))
	if _, err := rolled.Router().AcceptCommit(liveJournal, 31); err != nil {
		t.Fatalf("journal-bucket frame at ceiling+1 = %v; want accepted (the authority must not stale-drop live traffic)", err)
	}

	// Command-reply bucket: its own authority, its own ceiling.
	retainedReply := sealFrame(t, f.key, rec.ReplyCeiling, marshalReply(t, takeControlReply()))
	if _, err := rolled.Router().AcceptCommit(retainedReply, 32); !errors.Is(err, crypto.ErrStaleSeq) {
		t.Fatalf("retained command-reply at seq %d = %v; want crypto.ErrStaleSeq -- ReplyCeiling is a SEPARATE authority (outbound-reply.seq), and seeding only the journal bucket leaves this stream replayable", rec.ReplyCeiling, err)
	}
	liveReply := sealFrame(t, f.key, rec.ReplyCeiling+1, marshalReply(t, protocol.Control{Op: protocol.OpOK, OperationID: "op-live"}))
	if _, err := rolled.Router().AcceptCommit(liveReply, 33); err != nil {
		t.Fatalf("command-reply at ReplyCeiling+1 = %v; want accepted", err)
	}

	// Durable: the reseed survives the next process death.
	after := f.resume(t)
	if _, err := after.Router().AcceptCommit(retainedReply, 40); !errors.Is(err, crypto.ErrStaleSeq) {
		t.Fatalf("retained command-reply after a restart = %v; want crypto.ErrStaleSeq (the reseed must be persisted)", err)
	}
}

// TestRollback_GrantWatermarkRefusesAnOlderSignedGrant is coordinate (c), and its
// acceptance criterion verbatim: "an older correctly-signed grant [is] refused after a
// rollback". The grant carries the machine's real signature, so no signature check helps;
// only the persisted (epoch, grant_seq) watermark does.
func TestRollback_GrantWatermarkRefusesAnOlderSignedGrant(t *testing.T) {
	f := newRollbackFixture(t)
	before := f.snapshot()
	rec := wantReconcileRecord()

	// ONE KeyStore across the rollback, passed explicitly to both Accepts. The device
	// keys' own durability is PB-STATE-1's (TestState_DeviceKeysSurviveARestart); holding
	// them fixed here isolates the coordinate actually under test, so a failure below can
	// only be the missing watermark.
	live := f.resume(t)
	ks := live.KeyStore()
	retained, err := crypto.SealEpochGrant(f.machinePriv, ks.RecipientPublic(), rec.GrantEpoch, rec.GrantSeq, f.epochKeys)
	if err != nil {
		t.Fatalf("seal retained grant: %v", err)
	}
	if _, _, _, err := live.Grants().Accept(ks, f.machinePub, retained); err != nil {
		t.Fatalf("accept the legitimate grant: %v", err)
	}

	// ROLLBACK: the watermark is gone and the relay still holds the grant.
	rolled := f.rollback(t, before)
	f.deliverReconcile(t, rolled)
	if err := rolled.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if _, _, _, err := rolled.Grants().Accept(ks, f.machinePub, retained); !errors.Is(err, crypto.ErrGrantReplay) {
		t.Fatalf("older correctly-signed grant after a rollback = %v; want crypto.ErrGrantReplay (GrantEpoch/GrantSeq is the only anchor -- the signature is genuine)", err)
	}
	next, err := crypto.SealEpochGrant(f.machinePriv, ks.RecipientPublic(), rec.GrantEpoch, rec.GrantSeq+1, f.epochKeys)
	if err != nil {
		t.Fatalf("seal next grant: %v", err)
	}
	if _, _, _, err := rolled.Grants().Accept(ks, f.machinePub, next); err != nil {
		t.Fatalf("grant above the reseeded watermark = %v; want accepted (an anchor, not a wall)", err)
	}
}

// TestReconcile_RefusesAnAuthorityForAnotherMachineOrEpoch closes the residual S1b
// recorded verbatim: "The reconcile arm does not validate Machine/EpochID against the
// router. Currently defended by the per-epoch content key and seq ordering; worth an
// explicit check when the authorities are applied." This slice is where they are applied,
// so this is that check.
//
// It is also standing review question 1 ("what if there is more than one?"): the record
// names the machine and epoch its three authorities belong to, and applying a record
// stamped with a different one moves the wrong counters. A retained pre-rotation record
// still opens under the retained epoch content key, and its stale InboundHighWater --
// monotonic, so unrewindable -- would push the phone's send-seq into a range the new
// epoch's gateway stream has never seen.
func TestReconcile_RefusesAnAuthorityForAnotherMachineOrEpoch(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*protocol.ReconcileRecord)
		wantErr string
	}{
		{name: "another machine", mutate: func(r *protocol.ReconcileRecord) { r.Machine = "m2" }},
		{name: "another epoch", mutate: func(r *protocol.ReconcileRecord) { r.EpochID = 8 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// No file rollback is needed here, so the paired state is injected
			// directly -- this test is about which authorities are ADOPTED, not
			// about what survives a restart.
			store := &memStore{}
			seedPaired(t, store)
			c, err := Resume(Config{State: store, Machine: "m1"})
			if err != nil {
				t.Fatalf("Resume: %v", err)
			}

			rec := wantReconcileRecord()
			tc.mutate(&rec)
			plain, err := json.Marshal(reconcileFrame{Kind: kindReconcile, ReconcileRecord: rec})
			if err != nil {
				t.Fatalf("marshal foreign reconcile: %v", err)
			}
			// Sealed under the CURRENT epoch content key: a relay retaining a
			// pre-rotation record can do exactly this.
			if _, err := c.Router().AcceptCommit(sealFrameFrom(t, testContentKey(), machineSender, 7, 3, plain), 100); err != nil {
				t.Fatalf("accept foreign reconcile frame: %v", err)
			}
			if err := c.Reconcile(); err == nil {
				t.Fatalf("Reconcile() adopted a record for %s; want it refused -- its authorities describe coordinates that are not this phone's", tc.name)
			}
			if got, _ := c.Seq().NextCommand(); got > rec.InboundHighWater {
				t.Fatalf("the send-seq jumped to %d from a foreign authority; SeedFrom is monotonic, so this can never be undone", got)
			}
			if err := c.Router().RequireReconciled(); !errors.Is(err, ErrUnreconciled) {
				t.Fatalf("RequireReconciled() after a refused record = %v; want ErrUnreconciled (a refused authority is not a reconciliation)", err)
			}
		})
	}
}
