package conformance_test

// The harness's stand-in for the Android Keystore (PB-KEY-9).
//
// It replaces phonecore.InsecureCleartextSealer, which this suite used to hand the facade
// because there was no way for the Android side to supply a sealer at all -- the seam was a
// Go struct field and gomobile cannot set one. swarmmobile.NewApp now REQUIRES a KeyCustody,
// so the cleartext sealer has no call site left anywhere in the repository and the harness
// exercises the same custody path the shipped app takes.
//
// WHAT IT MODELS AND WHAT IT CANNOT. It models the CONTRACT: a per-tier data key that the
// Java side unwrapped under a Keystore KEK and handed Java -> Go, one that can refuse per
// operation with a verdict the Go side must distinguish. It models no hardware property
// whatsoever -- not that the KEK is really in a TEE, not that a real biometric gates it.
// That is PB-E2E-5, the physical-handset gate, which is deferred.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"sync"
	"testing"

	swarmmobile "github.com/Nathandela/swarm/mobile"
)

// testCustody implements swarmmobile.KeyCustody over two software AES keys.
//
// The refusals are per TIER and are settable while the app runs, because that is how the
// platform behaves: the content KEK answers or refuses depending on whether the user has
// authenticated recently, and the answer changes underneath a running process.
type testCustody struct {
	mu      sync.Mutex
	wake    []byte
	content []byte
	// refuse maps a tier name onto the verdict token its unwrap must fail with, or "" to
	// let it succeed.
	refuse map[string]string
	// unwraps counts every request per tier. It is how a test observes that the transport
	// STOPPED retrying: each dial calls SignRelayAuth exactly once, which unseals the wake
	// tier exactly once, so a count that stops growing is a loop that stopped.
	unwraps map[string]int
}

func newTestCustody(t *testing.T) *testCustody {
	t.Helper()
	c := &testCustody{
		wake:    make([]byte, 32),
		content: make([]byte, 32),
		refuse:  map[string]string{},
		unwraps: map[string]int{},
	}
	for _, k := range [][]byte{c.wake, c.content} {
		if _, err := rand.Read(k); err != nil {
			t.Fatalf("generating a Keystore stand-in KEK: %v", err)
		}
	}
	return c
}

// Refuse makes the named tier ("wake" or "content") fail every subsequent unwrap with the
// given verdict token, exactly as a Kotlin KeyCustody implementation would by throwing a
// typed exception whose message carries it. "" restores it.
func (c *testCustody) Refuse(tier, token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refuse[tier] = token
}

// Unwraps is the number of times the named tier's KEK has been asked for.
func (c *testCustody) Unwraps(tier string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.unwraps[tier]
}

func (c *testCustody) key(tier string, material []byte) ([]byte, error) {
	c.mu.Lock()
	token := c.refuse[tier]
	c.unwraps[tier]++
	c.mu.Unlock()
	if token != "" {
		return nil, errors.New("android keystore refused the " + tier + " KEK: " + token)
	}
	// A COPY, because the facade zeroizes what it is handed. Returning the field itself
	// would destroy the harness's own key on the first call and turn every later assertion
	// into a decrypt failure with no obvious cause.
	return append([]byte(nil), material...), nil
}

func (c *testCustody) WakeKEK() ([]byte, error) { return c.key("wake", c.wake) }

func (c *testCustody) ContentKEK() ([]byte, error) { return c.key("content", c.content) }

var _ swarmmobile.KeyCustody = (*testCustody)(nil)

// custodySealer is phonecore.Sealer over one of those keys, for the harness's own direct
// phonecore calls (seeding the state directory before the facade opens it).
//
// It reproduces the facade's construction -- AES-256-GCM, 12-byte random nonce prefixed to
// the ciphertext -- because the two must interoperate byte for byte: the harness seals the
// seed state and the facade opens it. A divergence would surface as an opaque authentication
// failure at Resume, so the shape is stated here rather than assumed.
type custodySealer struct{ key []byte }

func (s custodySealer) gcm() (cipher.AEAD, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (s custodySealer) Seal(plaintext []byte) ([]byte, error) {
	g, err := s.gcm()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return g.Seal(nonce, nonce, plaintext, nil), nil
}

func (s custodySealer) Open(sealed []byte) ([]byte, error) {
	g, err := s.gcm()
	if err != nil {
		return nil, err
	}
	if len(sealed) < g.NonceSize() {
		return nil, errors.New("conformance: sealed blob too short")
	}
	return g.Open(nil, sealed[:g.NonceSize()], sealed[g.NonceSize():], nil)
}

func (c *testCustody) wakeSealer() custodySealer { return custodySealer{key: c.wake} }

func (c *testCustody) contentSealer() custodySealer { return custodySealer{key: c.content} }
