// PB-KEY-7's purge, re-anchored on its surviving trigger (ADR-007 B133).
//
// WHAT MOVED. The requirement used to read "lock purges live memory", with three named
// triggers -- screen lock, background, auth expiry. B133 removes every phone-side user
// authentication mechanism, so none of the three exists as an event any more. The MECHANISM
// survives whole and matters more than it did: `MailboxRouter` holds `ContentKey` by value,
// and B133 makes revoke-from-the-computer the ONLY surviving mitigation for a lost handset,
// so a revoked device that keeps a resident key and decrypted content has not been revoked
// in any sense the owner would recognise.
//
// THE TRIGGER IS NOW REVOKE / UNPAIR, and the three expectations B133 left open are decided:
//
//	BOTH TIERS GO. The wake tier was spared by a lock because a push arrives with nobody
//	  there and the handset had to stay wakeable (ADR-007 B9/B16). A revoked device must not
//	  be wakeable: the wake key is what lets the machine reach it at all.
//	IT IS NOT RECOVERABLE WITHOUT RE-PAIRING. The lock kept the sealed blobs because
//	  PB-KEY-10 left nothing on the handset that could re-derive them, so destroying them
//	  made the first lock a permanent brick (ADR-007 B35). A revoke has no such cost: the
//	  pairing is what was destroyed, and re-pairing is the intended and only way back.
//	THE WATERMARKS AND THE OP QUEUE DO NOT SURVIVE. PB-STATE-9(2)/(3) rule them
//	  non-purgeable, and that ruling was argued against a LOCK: a phone that keeps talking
//	  under the same epoch must not renumber its send-seq from 1. A revoke rotates the epoch
//	  on the machine and ends the pairing, so there is no stream left for a watermark to
//	  guard and no lease for a queued op to run under -- and PendingOps carries session ids
//	  and typed command lines, which is exactly the user content a revoke exists to remove.
//
// PB-SEC-2 IS VOID (B133), and its two tests are deleted rather than adapted. Its whole
// subject was "the biometric gate is cryptographically enforced, not cosmetic"; there is no
// gate. They were the worst vacuous-green case in the repo: pure Go over fake sealers, with
// `crypto.ErrKeyAuthRequired` still compiling because `internal/remote/crypto` is FROZEN, so
// they would have stayed GREEN while fencing a screen-lock event that exists nowhere.

package phonecore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/status"
)

// revokeFixture is a paired phone with both tiers sealed, decrypted caches on disk, a
// send-seq ceiling and a queued op recorded -- i.e. a handset that has been running normally
// and is about to be revoked from the computer.
func revokeFixture(t *testing.T) (dir string, wake, content *s14aSealer, keys crypto.EpochKeys) {
	t.Helper()
	dir, wake, content, keys = s14aR2Sealed(t)

	seeded := s14aR2Resume(t, dir, wake, content)
	st := seeded.State()
	st.Sessions = []CachedSession{{SessionID: "m/s", Group: status.Group("active"), Present: true}}
	st.Snapshots = []Snapshot{{Session: "m/s", Lines: []string{"decrypted terminal content"}, Cols: 80, Rows: 24}}
	st.SendSeq = map[uint32]uint64{st.EpochID: 17}
	st.PendingOps = []QueuedOp{{Op: "kill", SessionID: "m/s"}}
	// The grant watermark a real phone carries: it is what refuses a replayed grant.
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
// PB-KEY-7: what a revoke destroys, and what it must not.
// ---------------------------------------------------------------------------

// TestPBKEY7_ARevokeClearsTheContentKeyAndTheDecryptedCachesFromMemory is the requirement's
// own verification criterion, with its trigger moved: no content key and no decrypted
// session content remains reachable after revoke/unpair.
//
// The router half is the point. `MailboxRouter` holds `ContentKey` BY VALUE for its
// lifetime, so a purge that only cleared State would leave the live object still able to
// open every frame the relay hands it.
func TestPBKEY7_ARevokeClearsTheContentKeyAndTheDecryptedCachesFromMemory(t *testing.T) {
	dir, wake, content, _ := revokeFixture(t)
	core := s14aR2Resume(t, dir, wake, content)

	if err := core.PurgeKeys(); err != nil {
		t.Fatalf("PurgeKeys: %v", err)
	}

	st := core.State()
	if st.Keys.ContentKey != (crypto.ContentKey{}) {
		t.Errorf("PB-KEY-7: the content key is still in core memory after the revoke: %x", st.Keys.ContentKey)
	}
	if len(st.Sessions) != 0 || len(st.Snapshots) != 0 || len(st.OpOutcomes) != 0 {
		t.Errorf("PB-KEY-7: the revoke left %d session(s), %d snapshot(s) and %d reply outcome(s) in memory",
			len(st.Sessions), len(st.Snapshots), len(st.OpOutcomes))
	}
	if k, _, _, _, _ := core.Router().bound(); k != (crypto.ContentKey{}) {
		t.Errorf("PB-KEY-7: MailboxRouter is still bound to the content key after the revoke: %x", k)
	}
}

// TestPBKEY7_ARevokeDestroysTheDecryptedCachesAtRest is the at-rest half.
//
// It is asserted from the BYTES, because a Load answers from the in-memory copy the purge
// just cleared and would pass over a blob that survived untouched.
func TestPBKEY7_ARevokeDestroysTheDecryptedCachesAtRest(t *testing.T) {
	dir, wake, content, _ := revokeFixture(t)
	core := s14aR2Resume(t, dir, wake, content)

	if err := core.PurgeKeys(); err != nil {
		t.Fatalf("PurgeKeys: %v", err)
	}

	if blob := readStateFile(t, dir).ContentPurgeable; len(blob) > 0 {
		t.Errorf("PB-KEY-7: the sealed decrypted-cache container survived the revoke (%d bytes). "+
			"Zeroizing the key while the content it protected stays on disk is not a purge", len(blob))
	}
}

// TestPBKEY7_ARevokeIsNotRecoverableWithoutRePairing is the first of the three expectations
// B133 left open, decided.
//
// A LOCK deliberately kept the sealed content key: PB-KEY-10 moved epoch-key delivery
// entirely into Go, so nothing on the handset could put those bytes back, and the grant
// watermark refuses the machine's re-appended frame as a replay -- so destroying the blob
// made the first screen lock a permanent brick with the machine as its only exit (B35).
//
// A REVOKE inverts that arithmetic. The pairing is the thing being destroyed, so "the phone
// cannot get back in without the machine" is the OUTCOME rather than the cost, and the only
// intended way back is re-pairing -- which mints a fresh epoch and fresh keys anyway. A
// revoked handset that can restore its content key with one local unwrap has not been
// revoked; it has been asked nicely.
func TestPBKEY7_ARevokeIsNotRecoverableWithoutRePairing(t *testing.T) {
	dir, wake, content, keys := revokeFixture(t)
	core := s14aR2Resume(t, dir, wake, content)

	if err := core.PurgeKeys(); err != nil {
		t.Fatalf("PurgeKeys: %v", err)
	}

	// Everything a re-pair does NOT do: no machine, no relay, no re-grant. Just the local
	// unwrap that used to be the whole recovery from a lock.
	if err := core.UnsealContent(); err != nil {
		t.Fatalf("UnsealContent after a revoke must be a no-op, not an error: %v", err)
	}
	if got := core.State().Keys.ContentKey; got != (crypto.ContentKey{}) {
		t.Errorf("PB-KEY-7: a local unwrap put the content key back after a revoke: %x. The revoke is "+
			"the only mitigation B133 leaves for a lost handset, and one that a fresh process undoes "+
			"by itself is not one", got)
	}

	// The strongest reader there is: a fresh process holding BOTH real KEKs.
	reopened := s14aR2Resume(t, dir, wake, content)
	if got := reopened.State().Keys.ContentKey; got == keys.ContentKey {
		t.Errorf("PB-KEY-7: the sealed content key survived the revoke at rest, so a restart recovers "+
			"it: %x", got)
	}
	if blob := readStateFile(t, dir).ContentKey; len(blob) > 0 {
		t.Errorf("PB-KEY-7: the sealed content-key blob is still on disk after the revoke (%d bytes)", len(blob))
	}
}

// TestPBKEY7_ARevokePurgesTheWakeTierToo is the second decided expectation, and it is the
// one that inverts a rule the lock purge was built around.
//
// ADR-007 B9/B16 spared the wake tier from a lock because a high-priority FCM push is the
// SOLE background wake path and arrives with nobody there: a lock that took the wake key
// left a handset that stopped being wakeable at the first screen lock, with no on-device
// source for the bytes. That argument is about a phone that must go on WORKING.
//
// A revoked phone must not go on working. The wake key is precisely what lets the machine
// reach it, and PB-PUSH-9 already requires deletion of the push registration on revoke; a
// wake key left behind is the other half of the same door.
func TestPBKEY7_ARevokePurgesTheWakeTierToo(t *testing.T) {
	dir, wake, content, keys := revokeFixture(t)
	core := s14aR2Resume(t, dir, wake, content)

	if err := core.PurgeKeys(); err != nil {
		t.Fatalf("PurgeKeys: %v", err)
	}

	if got := core.State().Keys.WakeKey; got == keys.WakeKey {
		t.Errorf("PB-KEY-7: the revoke left the WAKE key in memory: %x", got)
	}
	if blob := readStateFile(t, dir).WakeKey; len(blob) > 0 {
		t.Errorf("PB-KEY-7: the sealed WAKE key survived the revoke at rest (%d bytes). The wake key is "+
			"what lets the machine reach this handset at all", len(blob))
	}

	// And the strongest reader: a fresh process, which is what a push would wake.
	woken := s14aR2Resume(t, dir, wake, content)
	if got := woken.State().Keys.WakeKey; got == keys.WakeKey {
		t.Errorf("PB-KEY-7: a process started after the revoke comes up holding the wake key: %x", got)
	}
}

// TestPBKEY7_ARevokeTakesTheWatermarksAndTheOpQueue is the third decided expectation.
//
// PB-STATE-9(2)/(3) rule the send-seq ceiling, the receive high-waters and PendingOps
// NON-PURGEABLE, and that ruling is sound against a LOCK: a phone that keeps talking under
// the same epoch and renumbers its send-seq from 1 is stale-dropped by the gateway for the
// life of that epoch, and a queued op is work the user asked for and has not been told about.
//
// A revoke ends the epoch and the pairing together. There is no stream left for a watermark
// to guard, no lease for a queued op to ride, and PendingOps carries session ids and the
// command line typed for a launch -- user content by any reading. Carrying it across a
// revoke would leave the one thing the owner revoked the device to remove.
func TestPBKEY7_ARevokeTakesTheWatermarksAndTheOpQueue(t *testing.T) {
	dir, wake, content, _ := revokeFixture(t)
	core := s14aR2Resume(t, dir, wake, content)

	if err := core.PurgeKeys(); err != nil {
		t.Fatalf("PurgeKeys: %v", err)
	}
	if blob := readStateFile(t, dir).ContentKept; len(blob) > 0 {
		t.Errorf("PB-KEY-7: the non-purgeable content container survived the revoke (%d bytes). It "+
			"carries the send-seq ceiling, the receive high-waters and the offline op queue -- "+
			"session ids and the command line typed for a launch", len(blob))
	}

	// The strongest reader there is: a fresh process holding BOTH real KEKs.
	reopened := s14aR2Resume(t, dir, wake, content)
	st := reopened.State()
	if len(st.SendSeq) != 0 {
		t.Errorf("PB-KEY-7: the send-seq ceilings survived the revoke: %+v", st.SendSeq)
	}
	if len(st.Receive) != 0 {
		t.Errorf("PB-KEY-7: the receive high-waters survived the revoke: %+v", st.Receive)
	}
	if len(st.PendingOps) != 0 {
		t.Errorf("PB-KEY-7: the offline op queue survived the revoke: %+v", st.PendingOps)
	}
}

// TestPBKEY7_ARevokedDirectoryHoldsNoKeyMaterialAtAll is the whole-purge fence, and it is
// what stops the four above from being satisfied one field at a time.
//
// It replaces the equivalence fence this file used to carry ("a lock leaves the store
// exactly where a locked load leaves it"), which was right for a lock -- a lock WAS the
// state a push-woken process is already in -- and is wrong for a revoke: a push-woken
// process still holds the wake key, and a revoked one holds nothing.
func TestPBKEY7_ARevokedDirectoryHoldsNoKeyMaterialAtAll(t *testing.T) {
	dir, wake, content, _ := revokeFixture(t)
	core := s14aR2Resume(t, dir, wake, content)

	if err := core.PurgeKeys(); err != nil {
		t.Fatalf("PurgeKeys: %v", err)
	}

	f := readStateFile(t, dir)
	for _, c := range []struct {
		what string
		blob []byte
	}{
		{"the sealed wake key", f.WakeKey},
		{"the sealed content key", f.ContentKey},
		{"the wake-tier state container", f.WakeState},
		{"the non-purgeable content container", f.ContentKept},
		{"the decrypted-cache container", f.ContentPurgeable},
	} {
		if len(c.blob) > 0 {
			t.Errorf("PB-KEY-7: %s survived the revoke (%d bytes)", c.what, len(c.blob))
		}
	}

	// MUTATION CONTROL: a purge that took the whole state FILE would satisfy every
	// assertion above and would also lose the machine binding the load path must read
	// before it can open anything (PB-STATE-9(1)). The file must still be a state file.
	if f.Machine == "" {
		t.Error("PB-KEY-7: the revoke destroyed the state file's machine binding, so the assertions " +
			"above are measuring an absent file rather than a purged one")
	}
}
