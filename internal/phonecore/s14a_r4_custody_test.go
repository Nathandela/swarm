// FAILING-FIRST (TDD RED, GG-5) tests for slice S14a's round-4 review findings.
//
// Round 4 found no live defect in the shipped code and three fences that could not fail.
// Two of the three are here; the third is mobile/conformance's facade purge test, repointed
// at a.journal in place.
//
//   - F2: the carry-verbatim branch is fenced only at EPOCH 0, because the shared fixture
//     never stamps one. Round 3 added `prev.epoch == epoch` to that branch, so the only test
//     of it compares 0 == 0 and a mutation restricting the carry to epoch 0 ships a permanent
//     content-key brick on every real wake-path Save with five packages green.
//   - F3: the converse assertion about custody's purge stamp runs on a fixture that never
//     purged, so purgeGen is 0 whatever the code does. What it names -- the counter restored
//     across a restart while the handed-out State is left unstamped -- is a silent permanent
//     brick: every Save carrying a key is dropped and every one of them returns nil.
//   - F4 is the one production defect: Core.rebind reads the durable state and applies it to
//     the derived components with no lock held across the two, so a Save whose rebind read
//     PRE-purge state can rebind the router to the purged content key after PurgeKeys has
//     returned.

package phonecore

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// ---------------------------------------------------------------------------
// F2: the carry-verbatim branch, at an epoch that is not zero.
// ---------------------------------------------------------------------------

// s14aR4SealedAtEpoch is s14aR2Sealed with the epoch STAMPED. The shared fixture leaves
// EpochID at 0, which is not a state any paired phone is ever in -- `swarm remote init`
// starts at epoch 1 -- and every predicate of the form `prev.epoch == epoch` is satisfied
// there by two zeroes.
func s14aR4SealedAtEpoch(t *testing.T, epoch uint32) (dir string, wake, content *s14aSealer, keys crypto.EpochKeys) {
	t.Helper()
	dir = t.TempDir()
	wake, content = s14aNewSealer(t), s14aNewSealer(t)
	s14aSeedDeviceKeys(t, dir, wake, content)

	core := s14aR2Resume(t, dir, wake, content)
	st := core.State()
	st.Machine, st.EpochID = "m", epoch
	for i := range st.Keys.WakeKey {
		st.Keys.WakeKey[i] = byte(0x11 + i)
		st.Keys.ContentKey[i] = byte(0x55 + i)
	}
	if err := core.Save(st); err != nil {
		t.Fatalf("seeding epoch %d: %v", epoch, err)
	}
	return dir, wake, content, st.Keys
}

// TestS14A_R4_ALockedContentTierIsCarriedVerbatimAtANonzeroEpoch is
// TestS14A_ALockedContentTierIsCarriedVerbatimAcrossASave at an epoch a phone can actually
// be in.
//
// The carry is what keeps the wake path from destroying the epoch content key: the process
// came up on a push, could not open the content tier, and holds a zero for a key it merely
// could not READ. It Saves constantly -- every send reserves a seq. Round 3 correctly
// narrowed the carry to the epoch the blob was sealed for, and from then on the only fence
// on the SAFE direction was comparing 0 == 0: restricting the carry to epoch 0 leaves
// internal/phonecore, mobile, mobile/conformance, android/gate and internal/phonesim all
// green while every real handset loses its content key on the first wake-path Save and can
// never decrypt again.
func TestS14A_R4_ALockedContentTierIsCarriedVerbatimAtANonzeroEpoch(t *testing.T) {
	const epoch = 7
	dir, wake, content, keys := s14aR4SealedAtEpoch(t, epoch)

	// A push wake: the process comes up with nobody present, so it never opens the tier.
	content.openErr = crypto.ErrKeyAuthRequired
	locked := s14aR2Resume(t, dir, wake, content)

	if got := locked.State().EpochID; got != epoch {
		t.Fatalf("fixture: the locked process is at epoch %d, want %d -- the whole point of this "+
			"test is that the epoch is NOT zero", got, epoch)
	}
	if got := locked.State().Keys.ContentKey; got != (crypto.ContentKey{}) {
		t.Fatal("fixture: the locked process opened the content tier, so the Save below is not the " +
			"wake path's condition and the carry is never exercised")
	}

	// The wake path's normal condition: a Save that has nothing to do with the content key.
	st := locked.State()
	st.RelayCursor = 42
	if err := locked.Save(st); err != nil {
		t.Fatalf("a wake-path Save with the content tier locked at epoch %d: %v", epoch, err)
	}

	// The user finally authenticates, on this process or a later one.
	content.openErr = nil
	reopened := s14aR2Resume(t, dir, wake, content)
	if got := reopened.State().Keys.ContentKey; got != keys.ContentKey {
		t.Errorf("PB-KEY-3: at epoch %d a Save taken with the content tier LOCKED destroyed the durable "+
			"content key. The process held a zero because it could not read the tier, not because there "+
			"was nothing there, and the phone is now permanently unable to decrypt.\n got %x\nwant %x",
			epoch, got, keys.ContentKey)
	}
	if got := reopened.State().RelayCursor; got != 42 {
		t.Errorf("the Save that carried the tier verbatim lost its own payload: RelayCursor %d, want 42", got)
	}
}

// ---------------------------------------------------------------------------
// F3: custody's purge stamp, on a fixture that has actually purged.
// ---------------------------------------------------------------------------

// TestS14A_R4_ThePurgeStampDoesNotSurviveARestart is the converse property
// TestState_EveryResumeCriticalFieldSurvivesARestart states and cannot measure: that test
// never takes a purge, so purgeGen is 0 on both sides of its comparison whatever the code
// does. This one purges first, which is the only condition under which the stamp is non-zero
// and the assertion can fail.
//
// State.purgeGen is custody's own bookkeeping: fileStore stamps every State it hands out, and
// a Save carrying an OLDER stamp is a writer that has not noticed a purge, whose key material
// is dropped rather than re-sealed over it. Persisting the counter is what makes that go
// wrong across a process death, in two shapes that must both be caught:
//
//	round-tripped through the blob        -> a restored Load() reports a purge this process
//	                                         never took.
//	counter restored, State left unstamped-> every fresh caller looks stale forever. Save
//	                                         returns nil and the key is never written: content
//	                                         operations are permanently and SILENTLY dead.
func TestS14A_R4_ThePurgeStampDoesNotSurviveARestart(t *testing.T) {
	dir, wake, content, keys := s14aR2Sealed(t)

	core := s14aR2Resume(t, dir, wake, content)
	if core.State().Keys.ContentKey != keys.ContentKey {
		t.Fatal("fixture: the content key is not live before the purge, so there is nothing for the " +
			"purge below to stamp")
	}
	if err := core.PurgeKeys(); err != nil {
		t.Fatalf("PurgeKeys: %v", err)
	}
	if core.State().purgeGen == 0 {
		t.Fatal("fixture: the purge did not stamp the state it handed out, so the restart below " +
			"would prove nothing")
	}

	// RESTART: a fresh store over the same directory, nothing carried in memory.
	restarted := s14aR2Resume(t, dir, wake, content)
	if got := restarted.State().purgeGen; got != 0 {
		t.Errorf("State.purgeGen after a restart that followed a purge = %d; custody's lock-purge "+
			"counter must not be persisted. A restored one makes a fresh process refuse the first "+
			"Save of every caller holding a legitimate snapshot", got)
	}

	// The consequence, stated as the property the counter exists to protect rather than as a
	// fact about a field: a caller that comes up AFTER the purge is not stale, so the key it
	// installs must land. PB-KEY-7's purge is recoverable by installing the tier key again.
	var reinstalled crypto.ContentKey
	for i := range reinstalled {
		reinstalled[i] = byte(0xD0 + i)
	}
	st := restarted.State()
	st.Keys.ContentKey = reinstalled
	if err := restarted.Save(st); err != nil {
		t.Fatalf("re-installing the content key after a restart: %v", err)
	}
	if got := restarted.State().Keys.ContentKey; got != reinstalled {
		t.Errorf("PB-KEY-7: a content key installed by a caller that started AFTER the purge was "+
			"dropped in memory. The restarted process inherited a purge stamp it never took, so "+
			"every fresh caller looks stale forever.\n got %x\nwant %x", got, reinstalled)
	}

	again := s14aR2Resume(t, dir, wake, content)
	if got := again.State().Keys.ContentKey; got != reinstalled {
		t.Errorf("PB-KEY-7: a content key installed by a caller that started AFTER the purge never "+
			"reached disk, and Save returned nil. Content operations are permanently and silently "+
			"dead.\n got %x\nwant %x", got, reinstalled)
	}
}

// ---------------------------------------------------------------------------
// F4: the purge's IN-MEMORY half against a writer that has not noticed it.
// ---------------------------------------------------------------------------

// TestS14A_R4_APrePurgeRebindCannotRebindTheRouterToThePurgedKey.
//
// Round 3 closed this race on the DURABLE side (a Save from a pre-purge snapshot has its key
// material dropped, TestS14A_R3_APrePurgeStateSnapshotCannotResurrectTheKeys) and left it
// open in memory, which is the half PB-KEY-7 lists FIRST. Core.rebind reads the durable state
// and applies it to the derived components with nothing held across the two, and Core.PurgeKeys
// releases c.mu before its own rebind, so the two interleave: a Save whose rebind read
// pre-purge state applies AFTER the purge's, and MailboxRouter is left bound to the content
// key the purge destroyed -- after PurgeKeys has returned and every writer has finished, with
// the screen locked. The same argument round 3 made for the durable half applies verbatim:
// PurgeKeys arrives from an Android lifecycle callback on another thread, so the window is
// real.
//
// The interleaving is driven rather than raced: testHookRebindRead parks the Save's rebind
// between its read and its application, which is the window, and nothing else can make the
// outcome deterministic.
func TestS14A_R4_APrePurgeRebindCannotRebindTheRouterToThePurgedKey(t *testing.T) {
	dir, wake, content, keys := s14aR2Sealed(t)
	core := s14aR2Resume(t, dir, wake, content)
	if k, _, _, _, _ := core.Router().bound(); k != keys.ContentKey {
		t.Fatal("fixture: the router is not bound to the content key before the purge, so every " +
			"assertion below would pass vacuously")
	}

	readDone, purgeDone := make(chan struct{}), make(chan struct{})
	// CAS, not sync.Once: Once.Do BLOCKS every later caller until the first body returns, and
	// the purge's OWN rebind runs this hook. Parking it there would hold the purge behind the
	// Save under any implementation, which is the interleaving this test exists to rule out --
	// it would pass against the defect it names.
	var parked atomic.Bool
	testHookRebindRead = func() {
		if !parked.CompareAndSwap(false, true) {
			return
		}
		close(readDone)
		// BOUNDED on purpose. With the window closed the purge's own rebind cannot run until
		// this one releases, so waiting for it unconditionally would deadlock the fix rather
		// than test it. The wait makes the DEFECT deterministic -- with the window open the
		// purge completes immediately and this returns at once.
		select {
		case <-purgeDone:
		case <-time.After(2 * time.Second):
		}
	}
	t.Cleanup(func() { testHookRebindRead = nil })

	saved := make(chan error, 1)
	go func() {
		st := core.State() // taken BEFORE the purge: it holds both epoch keys
		st.RelayCursor = 99
		saved <- core.Save(st)
	}()

	<-readDone
	if err := core.PurgeKeys(); err != nil {
		t.Fatalf("PurgeKeys: %v", err)
	}
	close(purgeDone)
	if err := <-saved; err != nil {
		t.Fatalf("the concurrent Save: %v", err)
	}

	if k, _, _, _, _ := core.Router().bound(); k != (crypto.ContentKey{}) {
		t.Errorf("PB-KEY-7: MailboxRouter is bound to the PURGED content key after PurgeKeys returned "+
			"and every writer finished. A Save whose rebind read pre-purge state applied over the "+
			"purge's, so the phone keeps opening frames under a key it was told to destroy, with the "+
			"screen locked.\n got %x\nwant all-zero", k)
	}
	if got := core.State().Keys.ContentKey; got != (crypto.ContentKey{}) {
		t.Errorf("PB-KEY-7: the purged content key is live in State again after a concurrent Save.\n got %x", got)
	}
	// The purge must not have been bought by losing the writer's payload.
	if got := core.State().RelayCursor; got != 99 {
		t.Errorf("the coordinate the concurrent Save was actually writing did not land (RelayCursor "+
			"= %d, want 99)", got)
	}
}
