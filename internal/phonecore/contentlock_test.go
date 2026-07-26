// FAILING-FIRST (TDD RED, GG-5) for PB-KEY-7's lock purge and PB-SEC-2's freshness gate,
// against the model ADR-007 B35/B36 established and deliberately did not choose between.
//
// THE DECISION THESE TESTS ENCODE. A lock returns the content tier to LOCKED; it does not
// destroy it. B35 established that destroying it is unimplementable as specified: PB-KEY-10
// moved epoch-key delivery entirely into Go, so nothing on the handset has a source for those
// bytes, and dropKeyMaterial left GrantEpoch/GrantSeq standing so the machine re-appending the
// very same grant is refused as a replay. Wired as it stood, the FIRST SCREEN LOCK would land
// the phone in PB-KEY-3's terminal state, exitable only by physical access to the machine.
//
// What replaces it costs nothing and is the state the design already models. The sealed
// content-key blob at rest is ALREADY behind an auth-gated Keystore KEK
// (`Provisioning.kek`: setUserAuthenticationParameters(60, AUTH_BIOMETRIC_STRONG)), which is
// PB-SEC-1's at-rest gate and holds across a process restart. Destroying that blob buys
// nothing against an attacker holding a locked handset -- it only helps against one who has
// already defeated Keystore, and who therefore also holds device.key and the COMMAND_SIGN seed
// -- while costing the brick above. So the lock:
//
//	drops from MEMORY exactly what a locked LOAD leaves unread: the epoch content key, the
//	  send-seq ceilings, the receive high-waters, the op queue and the three decrypted caches;
//	destroys AT REST only the decrypted caches (ContentPurgeable), which cost nothing because
//	  PB-SYNC-2 re-derives them by resync;
//	CARRIES the sealed content key and ContentKept verbatim, as every Save already does for a
//	  tier this process could not open;
//	leaves the WAKE tier entirely alone (ADR-007 B9/B16: a push arrives with nobody there).
//
// Recovery is PB-KEY-7's own recovery clause read literally -- "require a fresh unwrap before
// restoring content" -- and that unwrap is a Keystore round trip that enforces both the lock
// state and the 60-second window. That is also the whole of B36: the defect there is that the
// content key was unwrapped ONCE at Resume and read from Go memory forever after, so the
// window never bit. Nothing else has to enforce it once the memory is not held across a lock.

package phonecore

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/status"
)

// lockFixture is a paired phone with both tiers sealed, decrypted caches on disk and a
// send-seq ceiling recorded -- i.e. a handset that has been running normally and is about to
// have its screen locked.
func lockFixture(t *testing.T) (dir string, wake, content *s14aSealer, keys crypto.EpochKeys) {
	t.Helper()
	dir, wake, content, keys = s14aR2Sealed(t)

	seeded := s14aR2Resume(t, dir, wake, content)
	st := seeded.State()
	st.Sessions = []CachedSession{{SessionID: "m/s", Group: status.Group("active"), Present: true}}
	st.Snapshots = []Snapshot{{Session: "m/s", Lines: []string{"decrypted terminal content"}, Cols: 80, Rows: 24}}
	st.SendSeq = map[uint32]uint64{st.EpochID: 17}
	st.PendingOps = []QueuedOp{{Op: "kill", SessionID: "m/s"}}
	// The grant watermark a real phone carries: it is what refuses a replayed grant, and it is
	// the reason a purge that destroyed the key at rest could never be recovered from.
	st.GrantEpoch, st.GrantSeq = st.EpochID, 3
	if err := seeded.Save(st); err != nil {
		t.Fatalf("seeding the content tier: %v", err)
	}
	return dir, wake, content, keys
}

// readStateFile returns the raw on-disk blob, which is the only honest reader for "what
// survived": every in-memory accessor answers from RAM.
func readStateFile(t *testing.T, dir string) stateFile {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, StateFileName))
	if err != nil {
		t.Fatalf("reading %s: %v", StateFileName, err)
	}
	var f stateFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("decoding %s: %v", StateFileName, err)
	}
	return f
}

// ---------------------------------------------------------------------------
// PB-KEY-7: what the lock destroys, and what it must not.
// ---------------------------------------------------------------------------

// TestPBKEY7_ALockClearsTheContentKeyAndTheDecryptedCachesFromMemory is the requirement's own
// verification criterion: "no content key and no decrypted session content remains reachable
// after lock". It is the half that is NOT met today -- there is no trigger anywhere, and after
// one resume the core keeps State.Keys.ContentKey with MailboxRouter still bound to it.
func TestPBKEY7_ALockClearsTheContentKeyAndTheDecryptedCachesFromMemory(t *testing.T) {
	dir, wake, content, _ := lockFixture(t)
	core := s14aR2Resume(t, dir, wake, content)

	if err := core.PurgeKeys(); err != nil {
		t.Fatalf("PurgeKeys: %v", err)
	}

	st := core.State()
	if st.Keys.ContentKey != (crypto.ContentKey{}) {
		t.Errorf("PB-KEY-7: the content key is still in core memory after the lock: %x", st.Keys.ContentKey)
	}
	if len(st.Sessions) != 0 || len(st.Snapshots) != 0 || len(st.OpOutcomes) != 0 {
		t.Errorf("PB-KEY-7: the lock left %d session(s), %d snapshot(s) and %d reply outcome(s) in memory",
			len(st.Sessions), len(st.Snapshots), len(st.OpOutcomes))
	}
	if k, _, _, _, _ := core.Router().bound(); k != (crypto.ContentKey{}) {
		t.Errorf("PB-KEY-7: MailboxRouter is still bound to the content key after the lock: %x", k)
	}
}

// TestPBKEY7_ALockDestroysTheDecryptedCachesAtRest is the at-rest half, and it is the clause
// that costs nothing: the three caches are re-derivable by PB-SYNC-2's resync, so destroying
// them strands nothing.
//
// It is asserted from the BYTES, because a Load answers from the in-memory copy the purge just
// cleared and would pass over a blob that survived untouched.
func TestPBKEY7_ALockDestroysTheDecryptedCachesAtRest(t *testing.T) {
	dir, wake, content, _ := lockFixture(t)
	core := s14aR2Resume(t, dir, wake, content)

	if err := core.PurgeKeys(); err != nil {
		t.Fatalf("PurgeKeys: %v", err)
	}

	if blob := readStateFile(t, dir).ContentPurgeable; len(blob) > 0 {
		t.Errorf("PB-KEY-7: the sealed decrypted-cache container survived the lock (%d bytes). "+
			"Zeroizing the key while the content it protected stays on disk is not a purge", len(blob))
	}
}

// TestPBKEY7_ALockLeavesTheContentKeyRecoverableWithNoReGrant is the constraint B35 found and
// declined to resolve, stated as a fence.
//
// dropKeyMaterial destroyed the sealed content key while leaving GrantEpoch/GrantSeq standing,
// and crypto.GrantReceiver enforces strict (epoch, seq) monotonicity -- so the gateway
// re-appending the very same bootstrap frame next session is refused as a replay, forever.
// PB-KEY-10 removed the Kotlin-side copy of those bytes, so nothing on the handset can put the
// key back either. Wired as it stood, the first screen lock was a permanent brick with the
// machine as its only exit (PB-KEY-3's terminal state, PB-STATE-10's).
//
// The mutation this catches is the shipped one: destroy the sealed tiers in Store.PurgeKeys.
func TestPBKEY7_ALockLeavesTheContentKeyRecoverableWithNoReGrant(t *testing.T) {
	dir, wake, content, keys := lockFixture(t)
	core := s14aR2Resume(t, dir, wake, content)
	before := core.State()

	if err := core.PurgeKeys(); err != nil {
		t.Fatalf("PurgeKeys: %v", err)
	}

	// The user authenticates. Nothing else happens: no machine, no relay, no re-grant.
	if err := core.UnsealContent(); err != nil {
		t.Fatalf("PB-KEY-7: the fresh unwrap after a lock failed, so the lock is a brick: %v", err)
	}
	after := core.State()
	if after.Keys.ContentKey != keys.ContentKey {
		t.Errorf("PB-KEY-7: the content key was not restored by a fresh unwrap.\n got %x\nwant %x",
			after.Keys.ContentKey, keys.ContentKey)
	}
	if after.GrantEpoch != before.GrantEpoch || after.GrantSeq != before.GrantSeq {
		t.Errorf("the lock moved the grant watermark (%d/%d -> %d/%d); it is the replay guard and "+
			"must not be rolled back to make recovery work",
			before.GrantEpoch, before.GrantSeq, after.GrantEpoch, after.GrantSeq)
	}
	if core.StreamStale(StreamGrant) {
		t.Errorf("PB-KEY-3: the lock put the phone into the grant-loss terminal state. A screen lock " +
			"must never be a state whose only exit is physical access to the machine")
	}
}

// TestPBKEY7_ALockDoesNotDisturbTheWakeTier. ADR-007 B9/B16: a high-priority FCM push is the
// SOLE background wake path and arrives with nobody there, so the wake KEK is deliberately not
// auth-gated. A lock that took the wake key with it would leave a handset that stops being
// wakeable at the first screen lock -- and the Kotlin side has no source for those bytes
// either, so nothing would put it back (B35).
//
// Both halves are asserted: the live key and the sealed blob.
func TestPBKEY7_ALockDoesNotDisturbTheWakeTier(t *testing.T) {
	dir, wake, content, keys := lockFixture(t)
	core := s14aR2Resume(t, dir, wake, content)

	if err := core.PurgeKeys(); err != nil {
		t.Fatalf("PurgeKeys: %v", err)
	}

	if got := core.State().Keys.WakeKey; got != keys.WakeKey {
		t.Errorf("PB-KEY-7/B16: the lock cleared the WAKE key from memory. The push path is the only "+
			"background wake there is and it runs with nobody present to re-authorize.\n got %x\nwant %x",
			got, keys.WakeKey)
	}
	if blob := readStateFile(t, dir).WakeKey; len(blob) == 0 {
		t.Errorf("PB-KEY-7/B16: the lock destroyed the sealed WAKE key at rest. The handset stops " +
			"being wakeable at the first screen lock, with no on-device source for the bytes")
	}

	// And the strongest reader: a fresh process, with the content tier still locked, must come
	// up holding the wake key -- which is exactly a push arriving after a screen lock.
	content.openErr = crypto.ErrKeyAuthRequired
	woken := s14aR2Resume(t, dir, wake, content)
	if got := woken.State().Keys.WakeKey; got != keys.WakeKey {
		t.Errorf("PB-KEY-7/B16: a push-woken process after a lock holds no wake key.\n got %x\nwant %x",
			got, keys.WakeKey)
	}
}

// TestPBKEY7_ALockPreservesTheReplayGuardsAndTheOpQueueAtRest. PB-STATE-9 clause 2: the
// send-seq ceiling and the receive high-waters are NOT decrypted content -- they are the
// record of how far the streams got -- and destroying them renumbers the phone from 1 under an
// epoch the gateway already holds a high-water for, which stale-drops everything it sends for
// the life of that epoch. PendingOps is content-tier and explicitly non-purgeable.
//
// The lock cannot READ them (it runs with the tier locked by definition) and does not need to:
// the container is carried verbatim, which is what every Save already does for an unopened
// tier.
func TestPBKEY7_ALockPreservesTheReplayGuardsAndTheOpQueueAtRest(t *testing.T) {
	dir, wake, content, _ := lockFixture(t)
	core := s14aR2Resume(t, dir, wake, content)
	epoch := core.State().EpochID

	if err := core.PurgeKeys(); err != nil {
		t.Fatalf("PurgeKeys: %v", err)
	}
	if blob := readStateFile(t, dir).ContentKept; len(blob) == 0 {
		t.Fatalf("PB-STATE-9: the lock destroyed the non-purgeable content container")
	}

	if err := core.UnsealContent(); err != nil {
		t.Fatalf("UnsealContent: %v", err)
	}
	st := core.State()
	if got := st.SendSeq[epoch]; got != 17 {
		t.Errorf("PB-STATE-9: the send-seq ceiling did not survive the lock: got %d, want 17. The phone "+
			"renumbers from 1 and the gateway stale-drops every frame for the life of the epoch", got)
	}
	if len(st.PendingOps) != 1 || st.PendingOps[0].Op != "kill" {
		t.Errorf("PB-STATE-9: the offline op queue did not survive the lock: %+v", st.PendingOps)
	}
}

// TestPBKEY7_ALockLeavesTheStoreExactlyWhereALockedLoadLeavesIt is the fence that keeps the
// two paths from drifting. A lock is not a new state: it is the state a process that came up
// on a push is already in, which the design has modelled since S15 and every Save already
// handles. Anything the lock leaves behind that a locked load does not is content this process
// is holding with the screen locked.
func TestPBKEY7_ALockLeavesTheStoreExactlyWhereALockedLoadLeavesIt(t *testing.T) {
	dir, wake, content, _ := lockFixture(t)

	locked := s14aR2Resume(t, dir, wake, content)
	if err := locked.PurgeKeys(); err != nil {
		t.Fatalf("PurgeKeys: %v", err)
	}

	// The same directory, opened by a process that never had the content tier at all.
	content.openErr = crypto.ErrKeyAuthRequired
	woken := s14aR2Resume(t, dir, wake, content)
	content.openErr = nil

	got, want := locked.State(), woken.State()
	for _, c := range []struct {
		what      string
		got, want int
	}{
		{"sessions", len(got.Sessions), len(want.Sessions)},
		{"snapshots", len(got.Snapshots), len(want.Snapshots)},
		{"reply outcomes", len(got.OpOutcomes), len(want.OpOutcomes)},
		{"send-seq ceilings", len(got.SendSeq), len(want.SendSeq)},
		{"receive high-waters", len(got.Receive), len(want.Receive)},
		{"pending ops", len(got.PendingOps), len(want.PendingOps)},
	} {
		if c.got != c.want {
			t.Errorf("PB-KEY-7: after a lock the core holds %d %s; a process that came up with the "+
				"content tier locked holds %d. A lock must reach the same state, or it is holding "+
				"content-tier material with the screen locked", c.got, c.what, c.want)
		}
	}
	if got.Keys.ContentKey != want.Keys.ContentKey {
		t.Errorf("PB-KEY-7: content key after a lock %x, after a locked load %x", got.Keys.ContentKey, want.Keys.ContentKey)
	}
}

// ---------------------------------------------------------------------------
// PB-SEC-2: the gate is the Keystore refusing, not a flag beside it.
// ---------------------------------------------------------------------------

// TestPBSEC2_RestoringContentGoesBackToKeystoreAndIsRefusedWhileLocked is B36's finding as a
// fence. The content key was unwrapped ONCE at Resume and read from Go memory for every
// outbound send thereafter, so after a single resume neither a screen lock nor the stated
// 60-second window stopped any content operation.
//
// The tier sealer IS the Keystore round trip (mobile/keycustody.go custodySealer holds the
// FETCHER, never a key), so the assertion is that restoring content consults it and honours
// its refusal -- with the refusal arriving as crypto.ErrKeyAuthRequired, which is exactly what
// a handset past its 60-second window returns.
func TestPBSEC2_RestoringContentGoesBackToKeystoreAndIsRefusedWhileLocked(t *testing.T) {
	dir, wake, content, keys := lockFixture(t)
	core := s14aR2Resume(t, dir, wake, content)

	if err := core.PurgeKeys(); err != nil {
		t.Fatalf("PurgeKeys: %v", err)
	}

	// The screen is locked, or the 60-second window has lapsed. Same refusal either way.
	content.openErr = crypto.ErrKeyAuthRequired
	opensBefore := content.opens
	err := core.UnsealContent()
	if !errors.Is(err, crypto.ErrKeyAuthRequired) {
		t.Fatalf("PB-SEC-2: restoring content while the tier is locked must surface the custody "+
			"refusal unchanged; got %v", err)
	}
	if content.opens == opensBefore {
		t.Errorf("PB-SEC-2: restoring content never asked the tier sealer. A core that answers from " +
			"its own memory keeps decrypting content after the screen locked, while every " +
			"restart-based test still passes")
	}
	if got := core.State().Keys.ContentKey; got != (crypto.ContentKey{}) {
		t.Errorf("PB-SEC-2: a refused unwrap installed a content key anyway: %x", got)
	}

	// The user authenticates.
	content.openErr = nil
	if err := core.UnsealContent(); err != nil {
		t.Fatalf("PB-SEC-2: restoring content after a fresh authentication failed: %v", err)
	}
	if got := core.State().Keys.ContentKey; got != keys.ContentKey {
		t.Errorf("PB-SEC-2: the content key was not restored by the fresh unwrap.\n got %x\nwant %x",
			got, keys.ContentKey)
	}
}

// TestPBSEC2_ALockedCoreCannotBeUnlockedByASave is the control on the test above: it must not
// be satisfiable by any path that does not go through the tier KEK. Save adopts State
// wholesale, so a caller holding a pre-lock snapshot is the obvious way back in -- and it is
// the one S14a's purgeGen already closes. Asserted here because this is the requirement that
// depends on it: if a Save can restore the key, "require a fresh unwrap" is decoration.
func TestPBSEC2_ALockedCoreCannotBeUnlockedByASave(t *testing.T) {
	dir, wake, content, keys := lockFixture(t)
	core := s14aR2Resume(t, dir, wake, content)
	stale := core.State() // taken BEFORE the lock, and it holds the real key

	if err := core.PurgeKeys(); err != nil {
		t.Fatalf("PurgeKeys: %v", err)
	}
	content.openErr = crypto.ErrKeyAuthRequired
	_ = core.Save(stale) // may refuse; what matters is what it cannot do

	if got := core.State().Keys.ContentKey; got == keys.ContentKey {
		t.Errorf("PB-SEC-2: a Save carrying a pre-lock snapshot put the content key back with no "+
			"Keystore round trip at all: %x", got)
	}
}
