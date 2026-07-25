package gate

// PB-KEY-9 / PB-SEC-1, driven through the PRODUCTION path.
//
// WHY THIS EXISTS BESIDE THE TWO TESTS IN keycustody_test.go, which already read the same two
// files' bytes. Those drive phonecore.Resume directly and hand it sealers from Go. The Android
// app cannot do that -- gomobile cannot set a Go struct field -- so until S14 they were the
// fifth standing defect class in its purest form: a fence guarding a path production does not
// take. They were green while the shipped app passed phonecore.InsecureCleartextSealer for both
// tiers and wrote the epoch content key to phone-state.json in the clear.
//
// This one goes through swarmmobile.NewApp, which is the only way the Android app can construct
// a phone at all, and through App.InstallWakeKey / App.InstallContentKey, which are ADR-007 B8's
// single inbound crossing and the only way epoch key material enters the core. Everything it
// asserts is therefore a statement about the shipped app.
//
// WHAT IT DOES NOT COVER, said before the assertions rather than after. The KEK here is a
// software AES key held in this process. Nothing on this machine can assert that a real KEK
// lives in a TEE, that a real biometric gates it, or that StrongBox behaves as advertised --
// that is PB-E2E-5, the physical-handset gate, which is deferred and is not claimed here.

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/phonecore"
	swarmmobile "github.com/Nathandela/swarm/mobile"
)

// facadeCustody is the Android Keystore as swarmmobile.KeyCustody sees it: two per-tier data
// keys the Java side would have unwrapped under an authenticated-Keystore AES KEK.
type facadeCustody struct{ wake, content []byte }

func newFacadeCustody(t *testing.T) *facadeCustody {
	t.Helper()
	c := &facadeCustody{wake: make([]byte, 32), content: make([]byte, 32)}
	for _, k := range [][]byte{c.wake, c.content} {
		if _, err := rand.Read(k); err != nil {
			t.Fatalf("generating a Keystore stand-in KEK: %v", err)
		}
	}
	return c
}

// The facade ZEROIZES what it is handed, which is the contract its package doc states. A copy
// is therefore mandatory, not defensive: returning the field would destroy this test's own key
// on the first call and every later assertion would fail as an opaque decrypt error.
func (c *facadeCustody) WakeKEK() ([]byte, error) {
	return append([]byte(nil), c.wake...), nil
}

func (c *facadeCustody) ContentKEK() ([]byte, error) {
	return append([]byte(nil), c.content...), nil
}

var _ swarmmobile.KeyCustody = (*facadeCustody)(nil)

// open reproduces the facade's AEAD so this test can recover what the core sealed. It is the
// reading side of "does not decrypt without the keystore key", and it is also what makes the
// byte assertions below exact rather than approximate: the needles are the values the core
// actually generated, not values this test chose.
func (c *facadeCustody) open(key, sealed []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(sealed) < g.NonceSize() {
		return nil, errors.New("gate: sealed blob too short")
	}
	return g.Open(nil, sealed[:g.NonceSize()], sealed[g.NonceSize():], nil)
}

// TestS14_TheShippedFacadeSealsBothTiersUnderTheInjectedKEK is the POSITIVE half of PB-KEY-9,
// and the fence internal/phonecore's TestS14A_TheCleartextSealerHasNoCallSitesLeft points at.
// An empty call-site inventory proves only that nobody ASKED for cleartext; this proves the
// seal happened, on the bytes, through the constructor the app uses.
func TestS14_TheShippedFacadeSealsBothTiersUnderTheInjectedKEK(t *testing.T) {
	dir := t.TempDir()
	custody := newFacadeCustody(t)

	app, err := swarmmobile.NewApp(&swarmmobile.Config{StateDir: dir, MachineID: "m"}, custody)
	if err != nil {
		t.Fatalf("swarmmobile.NewApp: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })

	// The epoch keys enter through B8's single inbound crossing, which is the ONLY way they
	// can: nothing else on the bound surface accepts key material. Recognisable bytes, so a
	// leak is findable in any buffer.
	wakeKey := make([]byte, 32)
	contentKey := make([]byte, 32)
	for i := range wakeKey {
		wakeKey[i] = byte(0x11 + i)
		contentKey[i] = byte(0x55 + i)
	}
	if err := app.InstallWakeKey(append([]byte(nil), wakeKey...)); err != nil {
		t.Fatalf("App.InstallWakeKey: %v", err)
	}
	if err := app.InstallContentKey(append([]byte(nil), contentKey...)); err != nil {
		t.Fatalf("App.InstallContentKey: %v", err)
	}

	// ---- device.key: the four device private scalars ----------------------------

	deviceBlob, err := os.ReadFile(filepath.Join(dir, "device.key"))
	if err != nil {
		t.Fatalf("PB-SEC-1: reading the persisted device keys: %v", err)
	}
	if len(deviceBlob) == 0 {
		t.Fatal("PB-SEC-1: device.key is empty; every assertion below would pass vacuously")
	}
	var container struct {
		Content []byte `json:"content"` // noise-static || recipient || command-sign seed
		Wake    []byte `json:"wake"`    // relay-auth seed
	}
	if err := json.Unmarshal(deviceBlob, &container); err != nil {
		t.Fatalf("PB-SEC-1: device.key is not the sealed container this test reads: %v", err)
	}
	// FIRST, and before anything that needs a key: a sealed blob is LONGER than its plaintext,
	// because it carries a nonce and a tag. This is the assertion that names the defect
	// directly when the seam seals nothing -- an identity Seal writes exactly 96 and 32 bytes,
	// and the reader below would then fail with an opaque "message authentication failed" that
	// says nothing about the material being in the clear.
	if len(container.Content) <= 96 || len(container.Wake) <= 32 {
		t.Fatalf("PB-SEC-1: device.key holds a %d-byte content blob and a %d-byte wake blob over "+
			"96 and 32 bytes of plaintext. A blob no longer than its plaintext carries neither "+
			"nonce nor tag and is not authenticated encryption -- the facade sealed NOTHING and "+
			"the four device private scalars are on disk in the clear",
			len(container.Content), len(container.Wake))
	}

	contentTier, err := custody.open(custody.content, container.Content)
	if err != nil {
		t.Fatalf("PB-SEC-1: the content tier does not open under the KEK the facade was given: %v", err)
	}
	wakeTier, err := custody.open(custody.wake, container.Wake)
	if err != nil {
		t.Fatalf("PB-SEC-1: the wake tier does not open under the KEK the facade was given: %v", err)
	}
	if len(contentTier) != 96 || len(wakeTier) != 32 {
		t.Fatalf("PB-SEC-1: the sealed tiers hold %d + %d bytes, want 96 + 32; every assertion "+
			"below would pass vacuously", len(contentTier), len(wakeTier))
	}

	for _, role := range []struct {
		name  string
		bytes []byte
		tier  string
	}{
		{"NoiseStatic", contentTier[0:32], "content"},
		{"Recipient", contentTier[32:64], "content"},
		{"CommandSign", contentTier[64:96], "content"},
		{"RelayAuth", wakeTier, "wake"},
	} {
		assertNotVerbatim(t, deviceBlob, role.bytes,
			"the "+role.name+" private key (PB-KEY-5 "+role.tier+" tier)", "device.key")
	}

	// ---- phone-state.json: the two epoch keys -----------------------------------

	stateBlob, err := os.ReadFile(filepath.Join(dir, phonecore.StateFileName))
	if err != nil {
		t.Fatalf("PB-SEC-1: reading the persisted state blob: %v", err)
	}
	if len(stateBlob) == 0 {
		t.Fatal("PB-SEC-1: the state blob is empty; the assertions below would pass vacuously")
	}
	assertNotVerbatim(t, stateBlob, wakeKey, "the epoch wake key (PB-KEY-2 wake tier)", phonecore.StateFileName)
	assertNotVerbatim(t, stateBlob, contentKey, "the epoch content key (PB-KEY-2 content tier)", phonecore.StateFileName)

	// NON-VACUITY, and it is the assertion that makes the four above mean something. A phone
	// that never wrote the keys at all would satisfy every "not verbatim" check. Re-opening the
	// directory with the SAME custody must recover exactly what was installed -- and the two
	// tiers must come back distinct, or a seam that wrote one key twice would also pass.
	if err := app.Close(); err != nil {
		t.Fatalf("App.Close: %v", err)
	}
	reopened, err := phonecore.Resume(phonecore.Config{
		Dir: dir, Machine: "m",
		WakeSealer:    gateSealerOver(custody.wake),
		ContentSealer: gateSealerOver(custody.content),
	})
	if err != nil {
		t.Fatalf("PB-SEC-1: the directory the facade wrote cannot be reopened under its own KEKs: %v", err)
	}
	st := reopened.State()
	if !bytes.Equal(st.Keys.WakeKey[:], wakeKey) {
		t.Errorf("PB-KEY-9: the wake key did not survive the seal; got %x want %x", st.Keys.WakeKey, wakeKey)
	}
	if !bytes.Equal(st.Keys.ContentKey[:], contentKey) {
		t.Errorf("PB-KEY-9: the content key did not survive the seal; got %x want %x", st.Keys.ContentKey, contentKey)
	}
}

// TestS14_TheFacadeRefusesToConstructAPhoneWithNoCustody. Fail-closed is the whole of B18(c):
// production must not be able to reach cleartext by forgetting something. The constructor is
// the only entry, so this is the only place it can be enforced -- and a nil KeyCustody is the
// shape "forgetting" takes on a bound surface, where every reference is nullable from Java.
func TestS14_TheFacadeRefusesToConstructAPhoneWithNoCustody(t *testing.T) {
	dir := t.TempDir()
	if _, err := swarmmobile.NewApp(&swarmmobile.Config{StateDir: dir, MachineID: "m"}, nil); err == nil {
		t.Fatal("PB-KEY-9: NewApp built a phone with no key custody. Every byte of key material " +
			"at rest would then be written with nothing over it, which is exactly what ADR-007 " +
			"B18(c) decided must not be reachable by omission")
	}
	if entries, err := os.ReadDir(dir); err == nil && len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("PB-KEY-9: the refused NewApp still wrote %v into the state directory. A refusal "+
			"that leaves key material behind is not a refusal", names)
	}
}

// assertNotVerbatim searches haystack for needle in BOTH forms.
//
// Raw AND base64: both files are JSON, so material dropped beside the sealed container travels
// base64 and the raw needle never appears. Without the base64 arm a writer that also emitted the
// privates into a second field passed with every one of them recoverable in the clear -- that is
// not hypothetical, it is what the S14a round-3 review found in this package's sibling test.
//
// WHAT THIS FENCE CANNOT SEE, recorded rather than chased: base64 encodes three input bytes at a
// time, so a 32-byte needle's own encoding appears inside a LONGER field's encoding only when the
// needle starts at a 3-byte-aligned offset and runs to the end. A leak that buried this material
// mid-field at an unaligned offset slips past both arms. DO NOT read a green run here as "no
// cleartext key material on disk". The property is carried by the POSITIVE assertion in the
// caller -- that the material went through the injected sealer and comes back only under it.
func assertNotVerbatim(t *testing.T, haystack, needle []byte, what, where string) {
	t.Helper()
	if bytes.Contains(haystack, needle) || bytes.Contains(haystack, []byte(base64Std(needle))) {
		t.Errorf("PB-SEC-1: %s sits verbatim in %s, written by the SHIPPED facade path. At rest it "+
			"must be sealed under the Android-Keystore-backed KEK swarmmobile.NewApp was given; "+
			"0600 is a filesystem permission, not a seal", what, filepath.Join("<stateDir>", where))
	}
}

// gateSealerOver builds the existing gate sealer over an already-chosen KEK, so the reopen above
// uses the same key the facade sealed with.
func gateSealerOver(kek []byte) *gateSealer { return &gateSealer{kek: kek} }
