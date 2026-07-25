// FAILING-FIRST (TDD RED, GG-5) tests for slice S14a / PB-KEY-9(b): the SEALING half of the
// Go-side custody seam.
//
// Nothing the phone core writes is sealed today. <stateDir>/device.key is 128 RAW bytes --
// the four device private scalars concatenated -- and <stateDir>/phone-state.json carries
// wake_key and content_key as plain base64. PB-SEC-1 requires key material at rest to be
// sealed under a Keystore-backed KEK, and the Android slice cannot deliver it: Go opens both
// files for itself at Resume and rewrites them while the app runs.
//
// THE SEAM IS PER TIER, NOT PER FILE. PB-KEY-9 records that a single plaintext state file
// "collapses PB-KEY-2's two-tier split at rest ... one file cannot be gated two ways, so the
// content key is recoverable without the biometric the entire tier design exists to require."
// A single Sealer does not fix that -- it moves it. Sealed under the wake KEK (which must open
// without user authentication, because a push arrives with nobody present) the content key is
// still reachable without the biometric; sealed under the content KEK the wake path cannot
// start at all. So Config takes ONE SEALER PER TIER, and the roles are split across them by
// PB-KEY-5's assignment, which android/gate/keycustody_test.go already encodes: NoiseStatic,
// Recipient and CommandSign are content tier, RelayAuth is wake tier.
//
// S15's PB-STATE-6 consumes this same seam, which is why it is designed once here.
//
// These do not compile until Config carries the seam. That is the RED.

package phonecore

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// s14aDeviceKeyFile is the device key blob's name inside the state directory. It is an
// unexported constant in the frozen crypto package, so it is spelled out here exactly as
// android/gate/keycustody_test.go spells it.
const s14aDeviceKeyFile = "device.key"

// ---------------------------------------------------------------------------
// A fake Keystore-backed sealer.
// ---------------------------------------------------------------------------

// s14aSealer is a real AEAD under a KEK the Go core never sees, standing in for the
// Android-Keystore-backed sealer. openErr drives the two failures a real Keystore produces:
// user-authentication-required (the tier is locked) and permanent invalidation after a
// biometric-enrollment change. Anything else it returns models a blob that is not ours.
//
// It RECORDS every plaintext it is asked to seal. Coverage is asserted against that record
// rather than by trying to open a whole file, because the tier split means a file is not one
// sealed blob -- phone-state.json must stay readable for its non-key fields while its two key
// fields are sealed under different KEKs. Asserting on the record says "this material went
// through THIS KEK" without dictating the file layout.
type s14aSealer struct {
	kek     []byte
	openErr error

	sealed [][]byte
	opens  int
}

// s14aSealedMaterial reports whether this sealer was ever asked to seal a plaintext
// containing needle -- i.e. whether this tier's KEK covers that material.
func (s *s14aSealer) s14aSealedMaterial(needle []byte) bool {
	for _, plain := range s.sealed {
		if bytes.Contains(plain, needle) {
			return true
		}
	}
	return false
}

func s14aNewSealer(t *testing.T) *s14aSealer {
	t.Helper()
	kek := make([]byte, 32)
	if _, err := rand.Read(kek); err != nil {
		t.Fatalf("generating a fake KEK: %v", err)
	}
	return &s14aSealer{kek: kek}
}

func (s *s14aSealer) Seal(plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(s.kek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	s.sealed = append(s.sealed, append([]byte(nil), plaintext...))
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func (s *s14aSealer) Open(sealed []byte) ([]byte, error) {
	s.opens++
	if s.openErr != nil {
		return nil, s.openErr
	}
	block, err := aes.NewCipher(s.kek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcm.NonceSize() {
		return nil, errors.New("s14a: sealed blob too short")
	}
	return gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], nil)
}

// ---------------------------------------------------------------------------
// Fixtures.
// ---------------------------------------------------------------------------

// s14aSeedDeviceKeys writes a device key file with deterministic material, exactly as
// android/gate/keycustody_test.go does, and returns the material so assertions are exact
// rather than guesses about entropy. Seeding it in the CLEAR is deliberate: an installed app
// already has one, so Resume must re-seal what it finds, not only seal what it creates.
func s14aSeedDeviceKeys(t *testing.T, dir string) crypto.KeyMaterial {
	t.Helper()
	var m crypto.KeyMaterial
	for i := range m.NoiseStaticPriv {
		m.NoiseStaticPriv[i] = byte(0x10 + i)
		m.RecipientPriv[i] = byte(0x40 + i)
		m.CommandSignSeed[i] = byte(0x70 + i)
		m.RelayAuthSeed[i] = byte(0xA0 + i)
	}
	if _, err := crypto.NewFileKeyStoreFromMaterial(dir, m); err != nil {
		t.Fatalf("seeding the device key store: %v", err)
	}
	return m
}

// s14aEpochKeys installs recognisable epoch keys through the durable path and returns them.
// Machine is stamped because the store discards a blob belonging to a different endpoint, so
// a fixture that leaves it empty reads back as empty state on the next Resume and every
// assertion about surviving a restart would be measuring nothing.
func s14aEpochKeys(t *testing.T, core *Core) crypto.EpochKeys {
	t.Helper()
	st := core.State()
	st.Machine = "m"
	for i := range st.Keys.WakeKey {
		st.Keys.WakeKey[i] = byte(0x11 + i)
		st.Keys.ContentKey[i] = byte(0x55 + i)
	}
	if err := core.Save(st); err != nil {
		t.Fatalf("saving epoch keys: %v", err)
	}
	return st.Keys
}

// s14aStateDirBytes reads every regular file under dir. Assertions run against the WHOLE
// directory, never one named file: a seal that moved the material into a sibling file would
// pass a per-file check while the material is just as reachable.
func s14aStateDirBytes(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		rel, _ := filepath.Rel(dir, path)
		out[rel] = body
		return nil
	})
	if err != nil {
		t.Fatalf("reading the state directory: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("PB-SEC-1: the state directory is empty; every byte assertion would pass vacuously")
	}
	return out
}

// s14aFindMaterial reports the files in which needle appears verbatim, raw or base64 (JSON
// encodes []byte as base64, so both forms are searched rather than assuming one).
func s14aFindMaterial(files map[string][]byte, needle []byte) []string {
	var hits []string
	b64 := []byte(base64.StdEncoding.EncodeToString(needle))
	for name, body := range files {
		if bytes.Contains(body, needle) || bytes.Contains(body, b64) {
			hits = append(hits, name)
		}
	}
	sort.Strings(hits)
	return hits
}

// ---------------------------------------------------------------------------
// PB-KEY-9(b): both files are sealed.
// ---------------------------------------------------------------------------

// TestS14A_ResumeSealsBothTheDeviceKeysAndTheEpochKeys is the in-package mirror of the
// android/gate PB-SEC-1 acceptance tests, with the seam injected. It asserts the positive
// half those tests cannot: not merely that the material is absent from the files, but that
// the files open under THESE sealers -- so an implementation that hid the bytes some other
// way (a different encoding, a locally-derived obfuscation) does not pass.
func TestS14A_ResumeSealsBothTheDeviceKeysAndTheEpochKeys(t *testing.T) {
	dir := t.TempDir()
	wake, content := s14aNewSealer(t), s14aNewSealer(t)
	m := s14aSeedDeviceKeys(t, dir)

	core, err := Resume(Config{Dir: dir, Machine: "m", WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("Resume with both sealers: %v", err)
	}
	keys := s14aEpochKeys(t, core)
	files := s14aStateDirBytes(t, dir)

	for _, role := range []struct {
		name  string
		bytes []byte
	}{
		{"NoiseStatic", m.NoiseStaticPriv[:]},
		{"Recipient", m.RecipientPriv[:]},
		{"CommandSign", m.CommandSignSeed[:]},
		{"RelayAuth", m.RelayAuthSeed[:]},
		{"WakeKey", keys.WakeKey[:]},
		{"ContentKey", keys.ContentKey[:]},
	} {
		if hits := s14aFindMaterial(files, role.bytes); len(hits) > 0 {
			t.Errorf("PB-SEC-1: the %s key sits verbatim in %v. At rest it must be sealed under a "+
				"KEK held outside the Go core", role.name, hits)
		}
	}

	// Absent bytes are not enough on their own: any re-encoding hides them. The seam must be
	// what hid them, and it must have covered BOTH files. The named defect here is a sealer
	// that covers the state file and not the device key, or the reverse -- each is invisible
	// to a check that only looks for absent bytes in the file it did cover, so the two files
	// are asserted through material that can only have come from one of them.
	for _, cover := range []struct {
		file  string
		what  string
		bytes []byte
	}{
		{s14aDeviceKeyFile, "the device private scalars", m.NoiseStaticPriv[:]},
		{s14aDeviceKeyFile, "the relay-auth seed", m.RelayAuthSeed[:]},
		{StateFileName, "the epoch content key", keys.ContentKey[:]},
		{StateFileName, "the epoch wake key", keys.WakeKey[:]},
	} {
		if !wake.s14aSealedMaterial(cover.bytes) && !content.s14aSealedMaterial(cover.bytes) {
			t.Errorf("PB-KEY-9: %s was never handed to an injected sealer, so whatever removed it from "+
				"%s was not this seam and no external KEK protects it", cover.what, cover.file)
		}
	}
	for _, f := range []string{s14aDeviceKeyFile, StateFileName} {
		if _, present := files[f]; !present {
			t.Errorf("PB-KEY-9: %s is missing from the state directory after Resume+Save", f)
		}
	}
}

// TestS14A_TheTwoTiersAreSealedUnderSeparateKEKs is PB-KEY-2's split, enforced at rest. It is
// the assertion a single-Sealer design cannot pass: with only the WAKE KEK in hand -- which is
// the phone's situation on a push, before any biometric -- the wake key must be recoverable
// and the content key must not.
func TestS14A_TheTwoTiersAreSealedUnderSeparateKEKs(t *testing.T) {
	dir := t.TempDir()
	wake, content := s14aNewSealer(t), s14aNewSealer(t)
	m := s14aSeedDeviceKeys(t, dir)

	core, err := Resume(Config{Dir: dir, Machine: "m", WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("Resume with both sealers: %v", err)
	}
	keys := s14aEpochKeys(t, core)
	files := s14aStateDirBytes(t, dir)

	// Content-tier material must be under the CONTENT KEK and nothing else. Being handed to
	// the wake sealer is the collapse: whatever opens without the biometric opens it too.
	for _, role := range []struct {
		name  string
		bytes []byte
	}{
		{"epoch ContentKey", keys.ContentKey[:]},
		{"NoiseStatic private scalar", m.NoiseStaticPriv[:]},
		{"Recipient private scalar", m.RecipientPriv[:]},
		{"CommandSign seed", m.CommandSignSeed[:]},
	} {
		if wake.s14aSealedMaterial(role.bytes) {
			t.Errorf("PB-KEY-2: the %s (content tier) was sealed under the WAKE KEK, which opens with no "+
				"user present. The tier split exists so content material requires the biometric-gated "+
				"KEK; one KEK over both tiers collapses it exactly as the plaintext file did", role.name)
		}
		if !content.s14aSealedMaterial(role.bytes) {
			t.Errorf("PB-KEY-2: the %s (content tier) never reached the content-tier KEK", role.name)
		}
		if hits := s14aFindMaterial(files, role.bytes); len(hits) > 0 {
			t.Errorf("PB-KEY-2: the %s sits verbatim in %v", role.name, hits)
		}
	}

	// The other half, without which everything above is satisfiable by sealing the whole
	// directory under the content KEK -- which would leave the phone unable to come up on a
	// push, before any biometric, which is the wake tier's entire purpose.
	for _, role := range []struct {
		name  string
		bytes []byte
	}{
		{"epoch WakeKey", keys.WakeKey[:]},
		{"RelayAuth seed", m.RelayAuthSeed[:]},
	} {
		if !wake.s14aSealedMaterial(role.bytes) {
			t.Errorf("PB-KEY-2: the %s (wake tier) is not under the WAKE KEK. The wake tier must open "+
				"with no user present, or a push cannot be handled at all", role.name)
		}
	}
}

// ---------------------------------------------------------------------------
// The two halves meet: a locked content tier is what makes a signing path fail.
// ---------------------------------------------------------------------------

// TestS14A_LockedContentTierRefusesEverySigningPath is PB-KEY-6's criterion driven end to end
// through the real Core rather than through a hand-written stub: the content tier is locked
// (the user has not authenticated), so every content-tier operation must refuse with
// ErrKeyAuthRequired, while the WAKE-tier relay-auth signature must still succeed.
//
// Resume itself must SUCCEED here. A phone that cannot start without the biometric cannot
// receive the push that asks for it.
func TestS14A_LockedContentTierRefusesEverySigningPath(t *testing.T) {
	dir := t.TempDir()
	wake, content := s14aNewSealer(t), s14aNewSealer(t)
	s14aSeedDeviceKeys(t, dir)

	if _, err := Resume(Config{Dir: dir, Machine: "m", WakeSealer: wake, ContentSealer: content}); err != nil {
		t.Fatalf("Resume to seal the directory: %v", err)
	}

	content.openErr = crypto.ErrKeyAuthRequired
	core, err := Resume(Config{Dir: dir, Machine: "m", WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("PB-KEY-2: Resume with the content tier locked returned %v. The wake path must come "+
			"up with no user present", err)
	}
	ks := core.KeyStore()

	if _, err := ks.SignCommand([]byte("op")); !errors.Is(err, crypto.ErrKeyAuthRequired) {
		t.Errorf("PB-KEY-6: SignCommand with the content tier locked returned %v, want ErrKeyAuthRequired", err)
	}
	if _, err := ks.NoiseStatic(); !errors.Is(err, crypto.ErrKeyAuthRequired) {
		t.Errorf("PB-KEY-6: NoiseStatic with the content tier locked returned %v, want ErrKeyAuthRequired", err)
	}
	if _, err := ks.OpenSealedBox([]byte("not a real sealed box")); !errors.Is(err, crypto.ErrKeyAuthRequired) {
		t.Errorf("PB-KEY-6: OpenSealedBox with the content tier locked returned %v, want ErrKeyAuthRequired. "+
			"A locked tier is not a malformed grant", err)
	}

	// The wake tier is NOT gated on the user, and the relay connection is its whole job. Without
	// this the assertions above are satisfiable by a store that refuses everything.
	if sig, err := ks.SignRelayAuth([]byte("challenge")); err != nil || len(sig) == 0 {
		t.Errorf("PB-KEY-5: SignRelayAuth (wake tier) returned sig %d bytes, err %v with only the content "+
			"tier locked; the relay must be reachable before the user authenticates", len(sig), err)
	}
	if len(ks.RelayAuthPublic()) != ed25519.PublicKeySize {
		t.Errorf("PB-KEY-5: RelayAuthPublic is %d bytes with the content tier locked; the phone cannot "+
			"compute its own relay routing id", len(ks.RelayAuthPublic()))
	}
}

// TestS14A_TheContentTierIsUnsealedPerOperationNotCached. PB-KEY-7 forbids holding live key
// material past a lock, and the lock the phone actually experiences is a screen lock DURING
// the process's life -- not a restart. A store that unseals once and memoizes the result keeps
// signing for as long as the process lives, so every restart-based test still passes while the
// property is false on a handset. Requiring the tier to be unsealed per operation is also what
// a Keystore-backed key does natively: an auth-gated key re-checks authorisation on each use.
//
// Counted on the sealer rather than inferred, because the memoized copy is invisible from
// outside: it produces byte-identical signatures.
func TestS14A_TheContentTierIsUnsealedPerOperationNotCached(t *testing.T) {
	dir := t.TempDir()
	wake, content := s14aNewSealer(t), s14aNewSealer(t)
	s14aSeedDeviceKeys(t, dir)

	core, err := Resume(Config{Dir: dir, Machine: "m", WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("Resume with both sealers: %v", err)
	}
	ks := core.KeyStore()

	if _, err := ks.SignCommand([]byte("first")); err != nil {
		t.Fatalf("first SignCommand: %v", err)
	}
	after := content.opens
	if after == 0 {
		t.Fatal("PB-KEY-7: signing never consulted the content-tier sealer at all, so the material it " +
			"used was not under that tier's KEK")
	}
	if _, err := ks.SignCommand([]byte("second")); err != nil {
		t.Fatalf("second SignCommand: %v", err)
	}
	if content.opens == after {
		t.Error("PB-KEY-7: the second signature reused unwrapped content-tier material without going " +
			"back to the sealer. A memoized tier keeps signing after the screen locks, and every " +
			"restart-based test still passes while that is true")
	}
}

// TestS14A_PermanentInvalidationIsSurfacedDistinctly. A biometric re-enrollment permanently
// invalidates the Keystore key: unlike an auth-required refusal, no prompt recovers it and the
// device must re-pair. PB-SEC-2 requires this case be handled, and handling it starts with
// being able to tell it apart.
func TestS14A_PermanentInvalidationIsSurfacedDistinctly(t *testing.T) {
	dir := t.TempDir()
	wake, content := s14aNewSealer(t), s14aNewSealer(t)
	s14aSeedDeviceKeys(t, dir)

	if _, err := Resume(Config{Dir: dir, Machine: "m", WakeSealer: wake, ContentSealer: content}); err != nil {
		t.Fatalf("Resume to seal the directory: %v", err)
	}

	content.openErr = crypto.ErrKeyInvalidated
	core, err := Resume(Config{Dir: dir, Machine: "m", WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("Resume with a permanently invalidated content tier: %v", err)
	}

	_, err = core.KeyStore().SignCommand([]byte("op"))
	if !errors.Is(err, crypto.ErrKeyInvalidated) {
		t.Errorf("PB-SEC-2: SignCommand after permanent invalidation returned %v, want ErrKeyInvalidated", err)
	}
	if errors.Is(err, crypto.ErrKeyAuthRequired) {
		t.Error("PB-SEC-2: permanent invalidation was reported as auth-required. The app would prompt " +
			"for a biometric forever against a key no prompt can restore")
	}
}

// ---------------------------------------------------------------------------
// Fail-closed, and the identity that must survive it.
// ---------------------------------------------------------------------------

// TestS14A_AnUnsealableStateDirFailsClosedAndIsNotOverwritten. core.go already promises
// Resume "fails closed: an unreadable blob or unreadable key custody is an error, never a
// silent start from zero", and today's openKeyStore reaches NewFileKeyStore whenever
// OpenFileKeyStore reports os.ErrNotExist. A blob that will not unseal must NOT take that
// path: generating fresh device keys silently changes the device id the daemon registry pins
// to the command-signing public key (R-DEV.1), unpairing the phone with no diagnosis.
func TestS14A_AnUnsealableStateDirFailsClosedAndIsNotOverwritten(t *testing.T) {
	dir := t.TempDir()
	wake, content := s14aNewSealer(t), s14aNewSealer(t)
	s14aSeedDeviceKeys(t, dir)

	first, err := Resume(Config{Dir: dir, Machine: "m", WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("Resume to seal the directory: %v", err)
	}
	want := append([]byte(nil), first.KeyStore().CommandSigningPublic()...)

	// A DIFFERENT KEK: a restored image, another device's blob, a rotated Keystore key. Not a
	// locked tier -- the blob simply is not ours.
	wrongWake, wrongContent := s14aNewSealer(t), s14aNewSealer(t)
	if _, err := Resume(Config{Dir: dir, Machine: "m", WakeSealer: wrongWake, ContentSealer: wrongContent}); err == nil {
		t.Error("PB-STATE-1: Resume against a state directory it cannot unseal returned no error. " +
			"Starting from zero here regenerates the device identity and silently unpairs the phone")
	}

	// And the failed attempt must not have destroyed the real custody.
	again, err := Resume(Config{Dir: dir, Machine: "m", WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("PB-STATE-1: Resume with the correct sealers after a failed one: %v", err)
	}
	if got := again.KeyStore().CommandSigningPublic(); !bytes.Equal(got, want) {
		t.Errorf("R-DEV.1: the device command-signing key changed across the failed Resume.\n got %x\nwant %x",
			got, want)
	}
}

// TestS14A_DeviceIdentitySurvivesRestartUnderTheSameSealer is the other half of the pair
// above: without it, "fails closed" is satisfiable by an implementation that never opens
// anything. The device keys must be the SAME across a restart (R-DEV.1).
func TestS14A_DeviceIdentitySurvivesRestartUnderTheSameSealer(t *testing.T) {
	dir := t.TempDir()
	wake, content := s14aNewSealer(t), s14aNewSealer(t)
	m := s14aSeedDeviceKeys(t, dir)

	first, err := Resume(Config{Dir: dir, Machine: "m", WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("first Resume: %v", err)
	}
	keys := s14aEpochKeys(t, first)

	second, err := Resume(Config{Dir: dir, Machine: "m", WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("second Resume: %v", err)
	}

	if got, want := second.KeyStore().CommandSigningPublic(), first.KeyStore().CommandSigningPublic(); !bytes.Equal(got, want) {
		t.Errorf("R-DEV.1: the device command-signing key changed across a restart.\n got %x\nwant %x", got, want)
	}
	if got := second.State().Keys.ContentKey; got != keys.ContentKey {
		t.Error("PB-STATE-1: the epoch content key did not survive the restart through the seal")
	}

	// The seal is genuinely reversible for the material it holds, not merely absent.
	sig, err := second.KeyStore().SignCommand([]byte("op"))
	if err != nil || len(sig) == 0 {
		t.Fatalf("SignCommand after an unseal: sig %d bytes, err %v", len(sig), err)
	}
	if err := crypto.VerifyCommandSig(second.KeyStore().CommandSigningPublic(), []byte("op"), sig); err != nil {
		t.Errorf("R-CRY.15: a signature made from unsealed material does not verify: %v", err)
	}
	if !bytes.Equal(second.KeyStore().RecipientPublic(), s14aRecipientPubFor(t, m)) {
		t.Error("R-DEV.1: the recipient public key does not match the seeded material after the seal round trip")
	}
}

func s14aRecipientPubFor(t *testing.T, m crypto.KeyMaterial) []byte {
	t.Helper()
	ks, err := crypto.NewFileKeyStoreFromMaterial(t.TempDir(), m)
	if err != nil {
		t.Fatalf("deriving the expected recipient public key: %v", err)
	}
	return ks.RecipientPublic()
}

// ---------------------------------------------------------------------------
// The KEK stays outside, and the default is a decision.
// ---------------------------------------------------------------------------

// TestS14A_NoUnsealedCopyIsCachedAfterTheFirstUnseal. Sealing at rest is defeated by anything
// that writes the unwrapped material back down, and the natural place for that to appear is
// AFTER the first successful unseal: a "we already paid for the biometric, cache it" fast
// path. A directory checked only at Resume never sees it, because at Resume nothing has been
// unsealed yet -- so this exercises every operation that unseals, then re-reads the directory.
//
// The KEK itself is not asserted on: the Go core is handed a Sealer and has no way to reach
// the key behind it, so a test looking for the KEK on disk could not fail. That property is
// held by the seam's shape instead (TestS14A_TheSealerSeamIsInboundOnly).
func TestS14A_NoUnsealedCopyIsCachedAfterTheFirstUnseal(t *testing.T) {
	dir := t.TempDir()
	wake, content := s14aNewSealer(t), s14aNewSealer(t)
	m := s14aSeedDeviceKeys(t, dir)

	core, err := Resume(Config{Dir: dir, Machine: "m", WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("Resume with both sealers: %v", err)
	}
	keys := s14aEpochKeys(t, core)

	// Drive every operation that has to unseal something.
	ks := core.KeyStore()
	if _, err := ks.SignCommand([]byte("op")); err != nil {
		t.Fatalf("SignCommand: %v", err)
	}
	if _, err := ks.SignRelayAuth([]byte("challenge")); err != nil {
		t.Fatalf("SignRelayAuth: %v", err)
	}
	if _, err := ks.NoiseStatic(); err != nil {
		t.Fatalf("NoiseStatic: %v", err)
	}

	files := s14aStateDirBytes(t, dir)
	for _, role := range []struct {
		name  string
		bytes []byte
	}{
		{"NoiseStatic private scalar", m.NoiseStaticPriv[:]},
		{"Recipient private scalar", m.RecipientPriv[:]},
		{"CommandSign seed", m.CommandSignSeed[:]},
		{"RelayAuth seed", m.RelayAuthSeed[:]},
		{"epoch WakeKey", keys.WakeKey[:]},
		{"epoch ContentKey", keys.ContentKey[:]},
	} {
		if hits := s14aFindMaterial(files, role.bytes); len(hits) > 0 {
			t.Errorf("PB-SEC-1: after the tiers were unsealed for use, the %s is on disk in the clear "+
				"in %v. An unwrapped copy written back down defeats the seal completely", role.name, hits)
		}
	}
}

// TestS14A_NoSealerIsNotSilentlyCleartext pins the default. PB-KEY-9 leaves it open and it
// must be chosen deliberately, so this test admits exactly two answers and nothing else:
// Resume refuses with a NAMED error, or it seals anyway. What it must never do is what it does
// today -- succeed and write the material in the clear.
//
// NOTE FOR THE IMPLEMENTER AND REVIEWER: the two android/gate PB-SEC-1 tests call
// Resume(Config{Dir, Machine}) with NO sealer and require the bytes to be sealed, which forces
// the second answer. The only KEK available in that configuration lives on the same disk, so
// that answer satisfies those tests' literal assertion while leaving the property their own
// comment describes ("worth nothing against ADB backup, a restored image") unmet. That is this
// project's "requirement satisfiable while the defect ships" class and it is flagged, not
// resolved, here: whichever answer is taken needs recording.
func TestS14A_NoSealerIsNotSilentlyCleartext(t *testing.T) {
	dir := t.TempDir()
	m := s14aSeedDeviceKeys(t, dir)

	core, err := Resume(Config{Dir: dir, Machine: "m"})
	if err != nil {
		if !errors.Is(err, ErrNoSealer) {
			t.Fatalf("PB-KEY-9: Resume with no sealer failed with %v. If the default is fail-closed it "+
				"must be a named error a caller can act on, not an opaque one", err)
		}
		return
	}

	keys := s14aEpochKeys(t, core)
	files := s14aStateDirBytes(t, dir)
	for _, role := range []struct {
		name  string
		bytes []byte
	}{
		{"NoiseStatic", m.NoiseStaticPriv[:]},
		{"RelayAuth", m.RelayAuthSeed[:]},
		{"WakeKey", keys.WakeKey[:]},
		{"ContentKey", keys.ContentKey[:]},
	} {
		if hits := s14aFindMaterial(files, role.bytes); len(hits) > 0 {
			t.Errorf("PB-SEC-1: with no sealer injected, Resume succeeded and left the %s key verbatim "+
				"in %v. Succeeding while writing key material in the clear is the defect", role.name, hits)
		}
	}
}

// TestS14A_PurgedContentKeyIsNotRecoverable is PB-KEY-7 at rest. The lock purge zeroizes the
// live tier keys; the durable copy must go with them. A sealer that cached the unwrapped state
// and rewrote it, or a Save that left the previous sealed blob beside the new one, leaves the
// content key recoverable after the lock the purge exists to honour.
func TestS14A_PurgedContentKeyIsNotRecoverable(t *testing.T) {
	dir := t.TempDir()
	wake, content := s14aNewSealer(t), s14aNewSealer(t)
	s14aSeedDeviceKeys(t, dir)

	core, err := Resume(Config{Dir: dir, Machine: "m", WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("Resume with both sealers: %v", err)
	}
	keys := s14aEpochKeys(t, core)

	st := core.State()
	st.Keys = crypto.EpochKeys{}
	if err := core.Save(st); err != nil {
		t.Fatalf("purging the tier keys: %v", err)
	}

	if got := core.State().Keys.ContentKey; got != (crypto.ContentKey{}) {
		t.Error("PB-KEY-7: the live content key survived the purge")
	}
	if hits := s14aFindMaterial(s14aStateDirBytes(t, dir), keys.ContentKey[:]); len(hits) > 0 {
		t.Errorf("PB-KEY-7: the purged content key is still on disk in the clear, in %v", hits)
	}

	// And not behind the seal either. A fresh Resume with BOTH KEKs is the strongest reader
	// there is: if it can still produce the purged key, the durable copy outlived the purge.
	reopened, err := Resume(Config{Dir: dir, Machine: "m", WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("Resume after the purge: %v", err)
	}
	if got := reopened.State().Keys.ContentKey; got == keys.ContentKey {
		t.Error("PB-KEY-7: the purged content key is still recoverable through the seal after a restart. " +
			"The lock zeroized the live copy and left the durable one")
	}
}

// TestS14A_TheSealerSeamIsInboundOnly. ADR-007 B8 pins the JNI key crossing to ONE INBOUND
// artifact and permits the per-role matrix only to NARROW it; B17 records that widening it to
// admit a reverse seam was considered and rejected. The Sealer is the new seam, so its shape
// is where a second or outbound crossing would appear: an accessor handing the KEK back, or a
// method returning unwrapped material. It must be exactly seal-in / open-out.
func TestS14A_TheSealerSeamIsInboundOnly(t *testing.T) {
	typ := reflect.TypeOf((*Sealer)(nil)).Elem()

	want := map[string][2]reflect.Type{
		"Seal": {reflect.TypeOf([]byte(nil)), reflect.TypeOf([]byte(nil))},
		"Open": {reflect.TypeOf([]byte(nil)), reflect.TypeOf([]byte(nil))},
	}
	if typ.NumMethod() != len(want) {
		var got []string
		for i := 0; i < typ.NumMethod(); i++ {
			got = append(got, typ.Method(i).Name)
		}
		sort.Strings(got)
		t.Fatalf("ADR-007 B8: Sealer has methods %v, want exactly Seal and Open. Any further method is "+
			"a second crossing, and one returning key material is an outbound one", got)
	}
	for i := 0; i < typ.NumMethod(); i++ {
		mth := typ.Method(i)
		sig, ok := want[mth.Name]
		if !ok {
			t.Errorf("ADR-007 B8: unexpected Sealer method %s", mth.Name)
			continue
		}
		ft := mth.Type
		if ft.NumIn() != 1 || ft.In(0) != sig[0] {
			t.Errorf("ADR-007 B8: Sealer.%s takes %v, want ([]byte)", mth.Name, ft)
		}
		if ft.NumOut() != 2 || ft.Out(0) != sig[1] || ft.Out(1) != reflect.TypeOf((*error)(nil)).Elem() {
			t.Errorf("ADR-007 B8: Sealer.%s returns %v, want ([]byte, error)", mth.Name, ft)
		}
	}
}
