// FAILING-FIRST (TDD RED, GG-5) tests for slice S14a's round-2 review findings.
//
// Three of the four are about ONE conflated signal in resealTier: "the caller handed me no
// key" and "destroy this tier" were the same thing (an all-zero key), and "this process
// could not open the tier" was read as "the caller cannot possibly have a key for it". The
// consequences are opposite in direction and both live:
//
//   - F2: a real content key installed AFTER the user authenticates is silently discarded,
//     so the phone comes back on the previous epoch's key and decrypts nothing (PB-KEY-3).
//   - F4: a lock purge taken while the content tier is locked never reaches disk, which the
//     function's own doc comment says it must (PB-KEY-7).
//   - F1: the carry-verbatim behaviour that prevents a permanent brick had NO test at all;
//     deleting it failed nothing repo-wide.
//
// The fourth is an attack surface S14a INTRODUCED. The pre-seam device.key was 128 raw
// bytes and every public was derived from the private material, so nothing could disagree.
// The sealed container carries the four publics in the CLEAR (they must answer while a tier
// is locked) and open never re-derived them, so an adversary with one write to the app's
// private data dir gets the phone to enrol attacker-chosen keys at the next pairing.

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

// s14aR2Resume opens a Core over dir under the given sealers, failing the test if it will
// not come up. Every case below restarts the same directory several times: a restart is the
// only way to ask what actually reached disk, since the in-memory copy answers from RAM.
func s14aR2Resume(t *testing.T, dir string, wake, content Sealer) *Core {
	t.Helper()
	core, err := Resume(Config{Dir: dir, Machine: "m", WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("Resume(dir=%s): %v", dir, err)
	}
	return core
}

// s14aR2Sealed is a state directory with device keys and epoch-1 keys already sealed under
// wake/content, i.e. a phone that has been paired and is running normally.
func s14aR2Sealed(t *testing.T) (dir string, wake, content *s14aSealer, keys crypto.EpochKeys) {
	t.Helper()
	dir = t.TempDir()
	wake, content = s14aNewSealer(t), s14aNewSealer(t)
	s14aSeedDeviceKeys(t, dir, wake, content)
	keys = s14aEpochKeys(t, s14aR2Resume(t, dir, wake, content))
	return dir, wake, content, keys
}

// ---------------------------------------------------------------------------
// F2 / F1: the two halves of the same branch, pulling in opposite directions.
// ---------------------------------------------------------------------------

// TestS14A_AContentKeyInstalledAfterUnlockReachesDisk is PB-KEY-3 against the designed
// post-lock recovery path. The phone wakes on a push with the content tier locked, so this
// process never opened the tier; the user then authenticates, a rotation grant opens, and
// mobile.App.InstallContentKey installs the new epoch's content key and Saves.
//
// If the Save writes the PREVIOUS sealed blob back -- because the process's record of the
// tier still says "could not open" -- the new key never reaches disk. The next restart comes
// up on the old epoch's key against a machine that has rotated, and PB-KEY-3's "permanently
// unable to decrypt" is exactly where the phone lands. Nothing in the process is wrong
// enough to report: the install returned nil.
func TestS14A_AContentKeyInstalledAfterUnlockReachesDisk(t *testing.T) {
	dir, wake, content, epoch1 := s14aR2Sealed(t)

	// A push wake: the process comes up with nobody present.
	content.openErr = crypto.ErrKeyAuthRequired
	locked := s14aR2Resume(t, dir, wake, content)

	// The user authenticates. From here the sealer works, and the app installs the epoch's
	// real content key -- the material this process was missing, now in hand.
	content.openErr = nil
	var epoch2 crypto.ContentKey
	for i := range epoch2 {
		epoch2[i] = byte(0xC0 + i)
	}
	st := locked.State()
	st.Keys.ContentKey = epoch2
	if err := locked.Save(st); err != nil {
		t.Fatalf("installing the content key after the user authenticated: %v", err)
	}

	reopened := s14aR2Resume(t, dir, wake, content)
	got := reopened.State().Keys.ContentKey
	if got == epoch1.ContentKey {
		t.Errorf("PB-KEY-3: the content key installed after the user authenticated was discarded and the "+
			"PREVIOUS epoch's key was written back. The phone restarts unable to decrypt anything the "+
			"machine sends, and the install reported success.\n got %x (epoch 1)\nwant %x (epoch 2)",
			got, epoch2)
	} else if got != epoch2 {
		t.Errorf("PB-KEY-3: the content key installed after the user authenticated did not reach disk.\n"+
			" got %x\nwant %x", got, epoch2)
	}
}

// TestS14A_ALockedContentTierIsCarriedVerbatimAcrossASave is the OPPOSITE direction, and the
// reason the fix above cannot simply reseal whatever the caller holds. The wake path runs
// with the content tier locked and Saves constantly -- every send reserves a seq -- while
// holding a zero content key it merely could not read. Resealing that zero destroys the
// epoch key for good: the user authenticates on a later process and there is nothing left to
// open.
//
// This behaviour shipped in S14a with no test at all; deleting the branch that implements it
// failed nothing repo-wide.
func TestS14A_ALockedContentTierIsCarriedVerbatimAcrossASave(t *testing.T) {
	dir, wake, content, keys := s14aR2Sealed(t)

	content.openErr = crypto.ErrKeyAuthRequired
	locked := s14aR2Resume(t, dir, wake, content)

	// The wake path's normal condition: a Save that has nothing to do with the content key.
	st := locked.State()
	st.RelayCursor = 42
	if err := locked.Save(st); err != nil {
		t.Fatalf("a wake-path Save with the content tier locked: %v", err)
	}

	// The user finally authenticates, on this process or a later one.
	content.openErr = nil
	reopened := s14aR2Resume(t, dir, wake, content)
	if got := reopened.State().Keys.ContentKey; got != keys.ContentKey {
		t.Errorf("PB-KEY-3: a Save taken with the content tier LOCKED destroyed the durable content key. "+
			"The process held a zero because it could not read the tier, not because there was nothing "+
			"there, and the phone is now permanently unable to decrypt.\n got %x\nwant %x",
			got, keys.ContentKey)
	}
	if got := reopened.State().RelayCursor; got != 42 {
		t.Errorf("the Save that carried the tier verbatim lost its own payload: RelayCursor %d, want 42", got)
	}
}

// ---------------------------------------------------------------------------
// F4: destroying a tier is not the same act as having nothing to write.
// ---------------------------------------------------------------------------

// TestS14A_APurgeWithTheContentTierLockedReachesDisk. PB-KEY-7's lock purge must take the
// durable copy with it, and resealTier's own doc comment says so -- but a purge inferred
// from an all-zero key is indistinguishable from the wake path's normal condition, so with
// the content tier locked the old sealed blob survived on disk and the phone recovered the
// supposedly purged key at the next unlock.
//
// The seam this pins: destroying a tier is an EXPLICIT act (Core.PurgeKeys, carried through
// Store.PurgeKeys), never a Save that happens to hold zeros. That is what lets the carry
// above and the purge here coexist -- they stop sharing one signal.
func TestS14A_APurgeWithTheContentTierLockedReachesDisk(t *testing.T) {
	dir, wake, content, keys := s14aR2Sealed(t)

	// Decrypted content the purge must drop with the keys, seeded through the same durable
	// path the app uses. Without this the purge is satisfiable by dropping the keys alone
	// while the plaintext session content it protected stays on disk.
	seeded := s14aR2Resume(t, dir, wake, content)
	st := seeded.State()
	st.Sessions = []CachedSession{{SessionID: "m/s", Group: status.Group("active"), Present: true}}
	st.Snapshots = []Snapshot{{Session: "m/s", Lines: []string{"decrypted terminal content"}, Cols: 80, Rows: 24}}
	if err := seeded.Save(st); err != nil {
		t.Fatalf("seeding the decrypted caches: %v", err)
	}

	// The screen locks on a process that came up on a push and never opened the tier.
	content.openErr = crypto.ErrKeyAuthRequired
	locked := s14aR2Resume(t, dir, wake, content)
	if err := locked.PurgeKeys(); err != nil {
		t.Fatalf("PurgeKeys with the content tier locked: %v", err)
	}

	// The strongest reader there is: a fresh Resume holding BOTH KEKs.
	content.openErr = nil
	reopened := s14aR2Resume(t, dir, wake, content)
	after := reopened.State()
	if after.Keys.ContentKey == keys.ContentKey {
		t.Errorf("PB-KEY-7: the purged content key is still recoverable through the seal after a restart. "+
			"The purge was taken with the tier locked, so it was read as 'nothing to write' and the old "+
			"sealed blob was carried through untouched.\n got %x", after.Keys.ContentKey)
	}
	if after.Keys.WakeKey == keys.WakeKey {
		t.Errorf("PB-KEY-7: the purged wake key survived the purge.\n got %x", after.Keys.WakeKey)
	}
	if len(after.Snapshots) != 0 || len(after.Sessions) != 0 {
		t.Errorf("PB-KEY-7: the purge left %d snapshots and %d sessions on disk. Zeroizing the keys while "+
			"the content they protected stays in the clear is not a purge",
			len(after.Snapshots), len(after.Sessions))
	}
}

// TestS14A_APurgeIsRecoverableNotABrick. PB-KEY-7's purge must be recoverable by installing
// the tier key again, or the first screen lock bricks the app. It is the control the test
// above needs: refusing to ever hold a content key again would satisfy every purge assertion.
func TestS14A_APurgeIsRecoverableNotABrick(t *testing.T) {
	dir, wake, content, keys := s14aR2Sealed(t)

	core := s14aR2Resume(t, dir, wake, content)
	if err := core.PurgeKeys(); err != nil {
		t.Fatalf("PurgeKeys: %v", err)
	}
	st := core.State()
	st.Keys = keys
	if err := core.Save(st); err != nil {
		t.Fatalf("re-installing the tier keys after the purge: %v", err)
	}

	reopened := s14aR2Resume(t, dir, wake, content)
	if got := reopened.State().Keys.ContentKey; got != keys.ContentKey {
		t.Errorf("PB-KEY-7: the content key re-installed after a purge did not reach disk.\n got %x\nwant %x",
			got, keys.ContentKey)
	}
}

// ---------------------------------------------------------------------------
// F5: the cleartext public half must not be believed on its own word.
// ---------------------------------------------------------------------------

// s14aR2AttackerKeys is material an adversary holds the private half of. A forged public is
// only dangerous if it is a REAL key someone can use: random bytes in the field would make
// the phone unreachable, which is noisy, while a key the attacker controls is silent and is
// what mobile/pairing.go would enrol -- a forged RecipientPub means every epoch grant is
// sealed to a key the attacker can open.
func s14aR2AttackerKeys(t *testing.T) crypto.KeyStore {
	t.Helper()
	var m crypto.KeyMaterial
	for i := range m.NoiseStaticPriv {
		m.NoiseStaticPriv[i] = byte(0xE0 + i)
		m.RecipientPriv[i] = byte(0xE1 + i)
		m.CommandSignSeed[i] = byte(0xE2 + i)
		m.RelayAuthSeed[i] = byte(0xE3 + i)
	}
	return crypto.NewKeyStoreFromMaterial(m)
}

// TestS14A_ForgedCleartextPublicKeysAreRefused. keycustody.go claims "the cleartext half of
// the container can never disagree with the sealed half", but that is a WRITE-time
// invariant: open copies all four publics straight out of the file and never re-derives
// them. PB-SEC-1's stated adversary has root or a restored image, i.e. exactly one write to
// the app's private data dir -- after which the Noise handshake runs on the real sealed
// static while the phone publishes the attacker's signing and recipient keys at the next
// pairing.
//
// This surface did not exist before S14a: the 128-raw-byte layout derived every public from
// the private material, so there was nothing to disagree with.
func TestS14A_ForgedCleartextPublicKeysAreRefused(t *testing.T) {
	attacker := s14aR2AttackerKeys(t)

	for _, tc := range []struct {
		field  string
		forge  func(*sealedDeviceKeys)
		damage string
	}{
		{"noise_static_pub", func(f *sealedDeviceKeys) { f.NoiseStaticPub = attacker.NoiseStaticPublic() },
			"the device advertises a Noise static it cannot prove"},
		{"recipient_pub", func(f *sealedDeviceKeys) { f.RecipientPub = attacker.RecipientPublic() },
			"every epoch grant is sealed to a key the attacker holds"},
		{"command_pub", func(f *sealedDeviceKeys) { f.CommandPub = attacker.CommandSigningPublic() },
			"the daemon registry pins the device id to a key the attacker signs with"},
		{"relay_auth_pub", func(f *sealedDeviceKeys) { f.RelayAuthPub = attacker.RelayAuthPublic() },
			"the phone's routing id becomes a mailbox the attacker can authenticate to"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			dir := t.TempDir()
			wake, content := s14aNewSealer(t), s14aNewSealer(t)
			s14aSeedDeviceKeys(t, dir, wake, content)
			honest := s14aR2Resume(t, dir, wake, content)
			want := map[string][]byte{
				"noise_static_pub": honest.KeyStore().NoiseStaticPublic(),
				"recipient_pub":    honest.KeyStore().RecipientPublic(),
				"command_pub":      honest.KeyStore().CommandSigningPublic(),
				"relay_auth_pub":   honest.KeyStore().RelayAuthPublic(),
			}[tc.field]

			path := filepath.Join(dir, s14aDeviceKeyFile)
			buf, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading the sealed device key container: %v", err)
			}
			var f sealedDeviceKeys
			if err := json.Unmarshal(buf, &f); err != nil {
				t.Fatalf("the sealed container is not the JSON this test tampers with: %v", err)
			}
			tc.forge(&f)
			forged, err := json.Marshal(f)
			if err != nil {
				t.Fatalf("re-encoding the tampered container: %v", err)
			}
			if err := os.WriteFile(path, forged, 0o600); err != nil {
				t.Fatalf("writing the tampered container: %v", err)
			}

			// The SEALED halves are untouched, so every unseal still succeeds. Only the
			// cleartext claim about the material changed.
			core, err := Resume(Config{Dir: dir, Machine: "m", WakeSealer: wake, ContentSealer: content})
			if err == nil {
				got := map[string][]byte{
					"noise_static_pub": core.KeyStore().NoiseStaticPublic(),
					"recipient_pub":    core.KeyStore().RecipientPublic(),
					"command_pub":      core.KeyStore().CommandSigningPublic(),
					"relay_auth_pub":   core.KeyStore().RelayAuthPublic(),
				}[tc.field]
				t.Fatalf("PB-SEC-1: Resume accepted a device.key whose cleartext %s was replaced with a key "+
					"the attacker holds the private half of, and now hands it out: %s.\n got %x\nwant %x\n"+
					"The publics must be re-derived from the SEALED material on open and the container "+
					"refused when they disagree", tc.field, tc.damage, got, want)
			}
			if !errors.Is(err, ErrPublicKeyMismatch) {
				t.Errorf("PB-SEC-1: Resume refused the tampered container with %v. A forged public half must "+
					"be a NAMED refusal a caller can act on -- it says the data dir was written to, which is "+
					"a different diagnosis and a different remedy from a KEK that does not match", err)
			}
		})
	}
}

// TestS14A_AnHonestSealedContainerStillOpens is the control the test above needs: refusing
// every container would satisfy it while bricking every phone. The publics an untampered
// container carries must match the sealed material and the store must come up.
func TestS14A_AnHonestSealedContainerStillOpens(t *testing.T) {
	dir, wake, content, _ := s14aR2Sealed(t)

	core := s14aR2Resume(t, dir, wake, content)
	if len(core.KeyStore().RecipientPublic()) == 0 {
		t.Error("an honest sealed container came up with no recipient public key")
	}
	// And with the content tier LOCKED, which is the case the re-derivation cannot check:
	// the phone must still start, or it cannot receive the push that asks for the biometric.
	content.openErr = crypto.ErrKeyAuthRequired
	locked := s14aR2Resume(t, dir, wake, content)
	if len(locked.KeyStore().RelayAuthPublic()) == 0 {
		t.Error("PB-KEY-5: with the content tier locked the phone cannot state its own relay routing id")
	}
	content.openErr = nil
}
