// FAILING-FIRST (TDD RED, GG-5) tests for slice S14a's round-3 review findings.
//
// Round 2 bound the sealed container's cleartext publics to the material sealed inside it,
// which closed the "one write to the app's private data dir enrols attacker-chosen keys"
// hole for the CONTAINER. It left the pre-seam layout beside it: 128 raw bytes, no public
// half, so nothing to bind and nothing to check. That path adopts whatever it is handed --
// private halves included -- and once S14 makes the container unforgeable it is the only
// unauthenticated ingress left to the device identity (B1).
//
// The other four are the lock purge's, and they share a shape: PB-KEY-7 lists the MEMORY
// half first and it cannot fail, but every one of these gates it behind something that can,
// or lets something put back what it destroyed.
//
//   - B2: the memory clear sits behind the durable write. A read-only data dir leaves the
//     keys live and bound with the screen locked.
//   - B3: an epoch rotation still infers "carry the old blob" from a zero key, so the OLD
//     epoch's sealed content key is written back under the NEW epoch id.
//   - B4: a Save built from a State snapshot taken BEFORE the purge re-seals what the purge
//     destroyed -- a direct consequence of round 2's own "a real key always wins" fix.
//   - B5: the decrypted reply cache is not dropped, and the purge's own rebind refills it.

package phonecore

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// ---------------------------------------------------------------------------
// B1: the unauthenticated ingress.
// ---------------------------------------------------------------------------

// s14aR3RawDeviceKeyLen is the pre-seam layout's length: four raw 32-byte scalars. It is
// spelled out here rather than imported because production no longer knows the layout at
// all -- which is the point of the fix this test fences.
const s14aR3RawDeviceKeyLen = 128

// TestS14A_R3_ARawDeviceKeyBlobIsRefusedNotAdopted.
//
// The sealed container's cleartext publics are checked against the material sealed under
// them, so a forged container is refused (round 2). The pre-seam layout cannot be checked
// that way -- it is four private scalars and nothing else, so there is no claim to
// contradict -- and it is adopted on nothing but its LENGTH. PB-SEC-1's adversary has one
// write to the app's private data directory; 128 bytes of their own scalars is all it takes
// to have the phone re-seal attacker-held Noise-static, recipient, command-signing and
// relay-auth keys as its own identity, and then sign and decrypt with them.
//
// Nothing in production writes that layout any more: crypto.NewFileKeyStore is reached from
// tests only, and no Phase B phone app has ever shipped, so there is no installed base to
// migrate. The path must therefore refuse, not adopt.
func TestS14A_R3_ARawDeviceKeyBlobIsRefusedNotAdopted(t *testing.T) {
	dir := t.TempDir()
	wake, content := s14aNewSealer(t), s14aNewSealer(t)

	// Material the ATTACKER holds the private halves of, in the pre-seam layout.
	var m crypto.KeyMaterial
	for i := range m.NoiseStaticPriv {
		m.NoiseStaticPriv[i] = byte(0xC0 + i)
		m.RecipientPriv[i] = byte(0xE0 + i)
		m.CommandSignSeed[i] = byte(0x01 + i)
		m.RelayAuthSeed[i] = byte(0x21 + i)
	}
	raw := make([]byte, 0, s14aR3RawDeviceKeyLen)
	raw = append(raw, m.NoiseStaticPriv[:]...)
	raw = append(raw, m.RecipientPriv[:]...)
	raw = append(raw, m.CommandSignSeed[:]...)
	raw = append(raw, m.RelayAuthSeed[:]...)
	if err := os.WriteFile(filepath.Join(dir, s14aDeviceKeyFile), raw, 0o600); err != nil {
		t.Fatalf("planting the raw blob: %v", err)
	}

	attacker, err := crypto.NewFileKeyStoreFromMaterial(t.TempDir(), m)
	if err != nil {
		t.Fatalf("deriving the attacker's public keys: %v", err)
	}

	core, err := Resume(Config{Dir: dir, Machine: "m", WakeSealer: wake, ContentSealer: content})
	if err != nil {
		return // refused, which is the requirement
	}
	for _, role := range []struct {
		name          string
		got, attacker []byte
		damage        string
	}{
		{"command_pub", core.KeyStore().CommandSigningPublic(), attacker.CommandSigningPublic(),
			"the daemon registry pins the device id to a key the attacker signs every mutating op with"},
		{"recipient_pub", core.KeyStore().RecipientPublic(), attacker.RecipientPublic(),
			"every epoch grant is sealed to a key the attacker holds"},
		{"noise_static_pub", core.KeyStore().NoiseStaticPublic(), attacker.NoiseStaticPublic(),
			"the pairing handshake runs on a static the attacker can complete"},
		{"relay_auth_pub", core.KeyStore().RelayAuthPublic(), attacker.RelayAuthPublic(),
			"the phone's routing id becomes a mailbox the attacker can authenticate to"},
	} {
		if bytes.Equal(role.got, role.attacker) {
			t.Errorf("PB-SEC-1: Resume ADOPTED a %d-byte device.key as the device identity, so %s is now "+
				"attacker-held: %s.\nA layout with no public half cannot be authenticated, so it must be "+
				"REFUSED rather than adopted on its length",
				s14aR3RawDeviceKeyLen, role.name, role.damage)
		}
	}
}

// ---------------------------------------------------------------------------
// B2: the half that cannot fail must not be gated behind the half that can.
// ---------------------------------------------------------------------------

// s14aR3MakeUnwritable makes dir reject the atomic write every durable path goes through,
// which is what a full disk or a read-only data dir looks like from in here. Running as root
// defeats the mode bits, so the probe is verified rather than assumed.
func s14aR3MakeUnwritable(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("making the state dir read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	f, err := os.CreateTemp(dir, ".probe-*")
	if err == nil {
		name := f.Name()
		_ = f.Close()
		_ = os.Remove(name)
		t.Skip("this process can still write to a 0500 directory (root?); the failure this test " +
			"needs cannot be produced")
	}
}

// TestS14A_R3_APurgeClearsMemoryEvenWhenTheDurableWriteFails.
//
// PB-KEY-7: "the core must stop content operations, zeroize/discard native key custody,
// purge decrypted session/snapshot/reply caches and sensitive UI state". The memory half is
// listed FIRST and it cannot fail -- there is no way for zeroizing a field to error. Today it
// sits behind the durable write, so a data directory that has gone read-only leaves the epoch
// keys live in State, still bound into the router, with the screen locked.
//
// The "in-memory advances only once the write succeeded" ordering is right for a SAVE and
// backwards for a PURGE: a Save must not claim what is not durable, a purge must not keep
// what it was told to destroy. The durable failure must still be REPORTED -- a purge that
// swallowed it would leave the caller believing the blob is gone.
func TestS14A_R3_APurgeClearsMemoryEvenWhenTheDurableWriteFails(t *testing.T) {
	dir, wake, content, keys := s14aR2Sealed(t)
	core := s14aR2Resume(t, dir, wake, content)
	if core.State().Keys.ContentKey != keys.ContentKey {
		t.Fatalf("fixture: the content key is not live before the purge, so the assertions below " +
			"would pass vacuously")
	}

	s14aR3MakeUnwritable(t, dir)

	err := core.PurgeKeys()
	if err == nil {
		t.Fatal("fixture: the durable write was expected to fail against a read-only state dir; " +
			"this test cannot say anything about the fail-open path if it succeeded")
	}
	if got := core.State().Keys.ContentKey; got != (crypto.ContentKey{}) {
		t.Errorf("PB-KEY-7: the epoch CONTENT key is still live in State after a purge whose durable "+
			"write failed (%v). The screen is locked and the process still holds the key", err)
	}
	if got := core.State().Keys.WakeKey; got != (crypto.WakeKey{}) {
		t.Error("PB-KEY-7: the epoch WAKE key is still live in State after a purge whose durable write failed")
	}
	if k, _, _, _, _ := core.Router().bound(); k != (crypto.ContentKey{}) {
		t.Error("PB-KEY-7: MailboxRouter is still BOUND to the purged content key, so the phone keeps " +
			"opening frames under a key it was told to destroy")
	}
	if len(core.State().Snapshots) != 0 || len(core.State().Sessions) != 0 {
		t.Error("PB-KEY-7: the decrypted session/snapshot caches survived a purge whose durable write failed")
	}
}

// ---------------------------------------------------------------------------
// B3: the fourth meaning inferred from an all-zero key.
// ---------------------------------------------------------------------------

// TestS14A_R3_AnEpochRotationDoesNotCarryTheOldEpochsSealedContentKey.
//
// resealTier enumerates three cases "that must stay distinct". There is a fourth: rotation.
// mobile.App.pin zeroes State.Keys deliberately when a pairing lands in a DIFFERENT epoch --
// "the tier keys belong to the old epoch: sealing under them while labelling the frame with
// the new epoch id yields frames the machine cannot open" -- and then Saves.
//
// A process that came up on a push has contentTier.opened == false (the tier was locked at
// load, and nothing re-sets it later). The rotating Save therefore hits the carry-verbatim
// branch and writes the OLD epoch's sealed content key back under the NEW epoch id. That is
// not a detectable zero: it is a plausible-looking key that decrypts nothing, and the phone
// restarts holding it.
func TestS14A_R3_AnEpochRotationDoesNotCarryTheOldEpochsSealedContentKey(t *testing.T) {
	dir := t.TempDir()
	wake, content := s14aNewSealer(t), s14aNewSealer(t)
	s14aSeedDeviceKeys(t, dir, wake, content)

	// Epoch 1, paired and running normally.
	first := s14aR2Resume(t, dir, wake, content)
	st := first.State()
	st.Machine, st.EpochID = "m", 1
	for i := range st.Keys.WakeKey {
		st.Keys.WakeKey[i] = byte(0x11 + i)
		st.Keys.ContentKey[i] = byte(0x55 + i)
	}
	epoch1 := st.Keys
	if err := first.Save(st); err != nil {
		t.Fatalf("seeding epoch 1: %v", err)
	}

	// A push wake: the process comes up with the content tier LOCKED, so it never opened the
	// blob it is holding.
	content.openErr = crypto.ErrKeyAuthRequired
	locked := s14aR2Resume(t, dir, wake, content)
	content.openErr = nil

	// The user authenticates and re-pairs into epoch 2. This is mobile.App.pin verbatim.
	rot := locked.State()
	rot.Keys = crypto.EpochKeys{}
	rot.EpochID = 2
	if err := locked.Save(rot); err != nil {
		t.Fatalf("saving the rotation: %v", err)
	}

	again := s14aR2Resume(t, dir, wake, content)
	if got := again.State().Keys.ContentKey; got == epoch1.ContentKey {
		t.Errorf("PB-KEY-3: after a rotation into epoch %d the durable content key is still epoch 1's. "+
			"The rotating Save carried the old sealed blob verbatim because this process had never "+
			"opened it, so the phone comes back holding a key that decrypts nothing and cannot tell "+
			"that from an epoch it simply has no key for", again.State().EpochID)
	}
	if got := again.State().Keys.ContentKey; got != (crypto.ContentKey{}) {
		t.Errorf("after a rotation the durable content key is %x, want all-zero: the new epoch has no "+
			"key until one is installed", got[:8])
	}
}

// ---------------------------------------------------------------------------
// B4: the purge must survive a writer holding a snapshot taken before it.
// ---------------------------------------------------------------------------

// TestS14A_R3_APrePurgeStateSnapshotCannotResurrectTheKeys.
//
// fileStore.PurgeKeys drops the tier records "so nothing is left for a later Save to carry
// back". That is true of the tier records and false of a State the CALLER is holding: round
// 2's own fix made a real key always win, whatever the tier record says, so a snapshot taken
// before the purge re-seals both keys straight over it.
//
// Every mobile State()->Save() pair is a few statements, but PurgeKeys arrives from an
// Android lifecycle callback on another thread, so the window is real. The purge must win.
//
// It must win WITHOUT bricking the Save: refusing every Save taken before the purge would
// satisfy the first assertion while losing the coordinate the caller was actually writing,
// so the second assertion pins that the rest of the Save still lands.
func TestS14A_R3_APrePurgeStateSnapshotCannotResurrectTheKeys(t *testing.T) {
	dir, wake, content, keys := s14aR2Sealed(t)
	core := s14aR2Resume(t, dir, wake, content)

	stale := core.State() // taken BEFORE the purge: it holds both epoch keys
	if stale.Keys.ContentKey != keys.ContentKey {
		t.Fatal("fixture: the snapshot does not hold the content key, so this test proves nothing")
	}
	if err := core.PurgeKeys(); err != nil {
		t.Fatalf("PurgeKeys: %v", err)
	}

	stale.PushToken = "tok-after-purge"
	if err := core.Save(stale); err != nil {
		t.Fatalf("Save from a pre-purge snapshot: %v", err)
	}

	again := s14aR2Resume(t, dir, wake, content)
	if got := again.State().Keys.ContentKey; got != (crypto.ContentKey{}) {
		t.Error("PB-KEY-7: a Save built from a State snapshot taken BEFORE the purge re-sealed the " +
			"content key over it. The purge is durable only until the next writer that has not " +
			"noticed it")
	}
	if got := again.State().Keys.WakeKey; got != (crypto.WakeKey{}) {
		t.Error("PB-KEY-7: a pre-purge snapshot re-sealed the WAKE key over the purge")
	}
	if got := again.State().PushToken; got != "tok-after-purge" {
		t.Errorf("the coordinate the caller was actually writing did not land (PushToken = %q). "+
			"Refusing the whole Save holds the purge by losing every unrelated field with it", got)
	}
}

// ---------------------------------------------------------------------------
// B5: "decrypted session/snapshot/REPLY caches".
// ---------------------------------------------------------------------------

// TestS14A_R3_APurgeDropsTheDecryptedReplyCache.
//
// PB-KEY-7 names three caches and the purge clears two: State.OpOutcomes is left in place,
// and it is decrypted machine->phone content like any other. Worse, Core.PurgeKeys rebinds
// afterwards -- it must, or the router keeps opening frames under the purged key -- and
// MailboxRouter.rebind refills r.replies from st.OpOutcomes. The purge repopulates the cache
// it was supposed to drop.
//
// Dropping them costs the ops they resolve: PB-SYNC-2 settles an operation by its durable
// outcome "or the stream stays unresolved", which is the defined outcome here, and a queued
// op that re-sends carries the same OperationID.
func TestS14A_R3_APurgeDropsTheDecryptedReplyCache(t *testing.T) {
	dir, wake, content, _ := s14aR2Sealed(t)
	core := s14aR2Resume(t, dir, wake, content)

	st := core.State()
	st.OpOutcomes = map[string]schema.Control{
		"op-r3": {Op: "ok", OperationID: "op-r3", EndpointID: "m"},
	}
	if err := core.Save(st); err != nil {
		t.Fatalf("seeding a decrypted reply: %v", err)
	}
	if core.Router().Replies().Len() == 0 {
		t.Fatal("fixture: the reply never reached the decrypted cache, so the purge below would " +
			"have nothing to drop")
	}

	if err := core.PurgeKeys(); err != nil {
		t.Fatalf("PurgeKeys: %v", err)
	}
	if n := core.Router().Replies().Len(); n != 0 {
		t.Errorf("PB-KEY-7: %d decrypted reply(s) are still in the router cache after the lock purge. "+
			"Core.PurgeKeys rebinds, and rebind refills r.replies from State.OpOutcomes -- the purge "+
			"puts back the content it was told to drop", n)
	}
	if n := len(core.State().OpOutcomes); n != 0 {
		t.Errorf("PB-KEY-7: %d durable decrypted outcome(s) survived the lock purge", n)
	}

	again := s14aR2Resume(t, dir, wake, content)
	if n := len(again.State().OpOutcomes); n != 0 {
		t.Errorf("PB-KEY-7: %d decrypted outcome(s) came back on the next Resume; the purge never "+
			"reached disk for them", n)
	}
}
