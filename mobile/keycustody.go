package swarmmobile

// PB-KEY-9's closing half: the Android-Keystore-backed KEK, reachable from Kotlin.
//
// THE PROBLEM THIS FILE SOLVES. phonecore.Resume takes a Sealer per PB-KEY-2 tier and fails
// closed without one, so the phone core's key material at rest is sealed under a KEK the Go
// core does not hold. But the seam is a Go struct field, and gomobile cannot set one -- so
// until this file existed the only reachable answer was phonecore.InsecureCleartextSealer,
// and the shipped app wrote the epoch content key to phone-state.json in the clear while
// PB-SEC-1's acceptance gate stayed green by injecting real sealers from Go. That is the
// project's standing "the acceptance test is green and the product is not" class, and
// ADR-007 B18 forecast it in as many words.
//
// THE DIRECTION, because it is the whole of ADR-007 B8. KeyCustody is REVERSE-BOUND: Go
// calls it, Kotlin implements it. On a reverse-bound interface a RESULT travels Java -> Go
// and a PARAMETER travels Go -> Java, which is the mirror image of the rule for App's own
// methods. So B8's "single crossing, inbound only" reads here as: the per-tier data key
// comes IN as a result, and NOTHING carrying key material may go OUT as a parameter. There
// is deliberately no Seal/Open pair on this interface, which is the shape that would have
// been natural: sealing needs the PLAINTEXT device scalars, and handing those to Java is an
// outbound key crossing however tidy the diff looks. Go does the AEAD itself, under a key
// Kotlin unwrapped from Keystore.
//
// TestS14_TheCustodySeamIsInboundOnly pins that, and it is a fence rather than a comment
// because the outbound shape is the one a later reader will reach for first.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// KeyCustody is the Android Keystore, as the Go core sees it.
//
// Each method returns the TRANSIENT PER-TIER DATA KEY of ADR-007 B8: a 32-byte AES key the
// Java side unwrapped under an authenticated-Keystore AES KEK and passed Java -> Go. The
// Keystore key itself never crosses, and no method here accepts key material.
//
// THE CALLS ARE MADE PER OPERATION AND THE RESULT IS NEVER MEMOIZED. That is what makes the
// content tier's gate real: an auth-gated Keystore key re-checks authorisation on every
// unwrap, so a locked handset makes ContentKEK fail, and a Go core that had cached the
// answer would keep decrypting content after the screen locked (PB-KEY-7) while every
// restart-based test still passed. The array is zeroized as soon as the AEAD is built.
//
// FAILURE IS EXPECTED AND MUST BE DISTINGUISHABLE. gomobile flattens a Java exception into
// a Go error carrying only its MESSAGE, so the two custody verdicts are carried by the
// tokens below: an implementation whose exception message contains KeyCustodyAuthRequired is
// a recoverable refusal (prompt for the biometric), and one containing
// KeyCustodyKeyInvalidated is permanent (the key is gone; the device must pair again).
// Anything else is an opaque failure and is treated as fatal, because a custody verdict that
// cannot be read must not be guessed at.
type KeyCustody interface {
	// WakeKEK is the wake-tier data key. It must open with NO USER PRESENT -- a push
	// arrives with nobody there -- so the Keystore key behind it is deliberately not
	// user-authentication-gated (ADR-007 B9/B16).
	WakeKEK() ([]byte, error)

	// ContentKEK is the content-tier data key, behind the biometric. It is the one that
	// legitimately refuses.
	ContentKEK() ([]byte, error)
}

// The two custody verdicts, as tokens that survive gomobile's error flattening in BOTH
// directions: Kotlin stamps them onto the exceptions it throws out of KeyCustody, and the
// facade stamps them onto every error it returns that wraps the matching crypto sentinel
// (see barrier). They are exported so the Android side binds these constants instead of
// keeping a second copy of a discriminator string -- a copy that drifted would fail
// silently, degrading a permanent invalidation into a prompt the user can never satisfy.
const (
	// KeyCustodyAuthRequired marks a RECOVERABLE refusal: crypto.ErrKeyAuthRequired. The
	// user must authenticate; the operation is worth retrying afterwards.
	KeyCustodyAuthRequired = "swarm-custody/auth-required"

	// KeyCustodyKeyInvalidated marks a PERMANENT one: crypto.ErrKeyInvalidated. The key is
	// destroyed -- a biometric enrollment change, a cleared credential, a restored image --
	// and no prompt brings it back. The device must pair again.
	KeyCustodyKeyInvalidated = "swarm-custody/key-invalidated"
)

// custodySealer is phonecore.Sealer over one tier of a KeyCustody.
//
// It holds the FETCHER, never a key: every Seal and every Open goes back to Keystore. See
// KeyCustody's doc for why that is the property rather than an inefficiency.
type custodySealer struct {
	tier  string
	fetch func() ([]byte, error)
}

func (s custodySealer) aead() (cipher.AEAD, error) {
	key, err := s.fetch()
	if err != nil {
		return nil, classifyCustodyVerdict(s.tier, err)
	}
	// Zeroized before the function returns, whatever happens next. AES's key schedule is
	// already built by then, so the plaintext key is not needed past this point and leaving
	// it live on the Go heap buys nothing.
	defer func() {
		for i := range key {
			key[i] = 0
		}
	}()
	if len(key) != 32 {
		return nil, fmt.Errorf("swarmmobile: the %s KEK is %d bytes, want 32", s.tier, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (s custodySealer) Seal(plaintext []byte) ([]byte, error) {
	g, err := s.aead()
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
	g, err := s.aead()
	if err != nil {
		return nil, err
	}
	if len(sealed) < g.NonceSize() {
		return nil, fmt.Errorf("swarmmobile: the %s custody blob is truncated", s.tier)
	}
	return g.Open(nil, sealed[:g.NonceSize()], sealed[g.NonceSize():], nil)
}

// classifyCustodyVerdict converts what Kotlin threw back into the crypto package's own
// sentinels, so every layer between here and phonecore keeps treating a locked content tier
// as a per-operation refusal rather than a corrupt blob.
//
// It is the INBOUND half of the same mapping barrier applies outbound. Getting it wrong in
// this direction is worse than in the other: phonecore.openSealedDeviceKeys refuses a Resume
// outright for any content-tier error that is NOT one of these two, so a refusal that failed
// to classify would turn a locked handset into an app that cannot start.
func classifyCustodyVerdict(tier string, err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, KeyCustodyKeyInvalidated):
		return fmt.Errorf("swarmmobile: the %s Keystore key is gone: %w: %v",
			tier, crypto.ErrKeyInvalidated, err)
	case strings.Contains(msg, KeyCustodyAuthRequired):
		return fmt.Errorf("swarmmobile: the %s Keystore key needs a fresh authentication: %w: %v",
			tier, crypto.ErrKeyAuthRequired, err)
	}
	return fmt.Errorf("swarmmobile: the %s Keystore key could not be obtained: %w", tier, err)
}

// stampCustodyVerdict is the OUTBOUND half: it prefixes an error that wraps one of the two
// crypto sentinels with the matching token, so the Android side can branch on a type instead
// of on prose.
//
// It runs in barrier, which every exported entry point already installs as its first
// statement (PB-BIND-5), so it is total by construction: a verb cannot forget to classify,
// and a verb added later inherits it. Doing it per-verb was the alternative and was rejected
// for exactly that reason -- an enumeration of verbs is a list somebody has to keep correct.
func stampCustodyVerdict(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case errors.Is(err, crypto.ErrKeyInvalidated):
		if strings.Contains(msg, KeyCustodyKeyInvalidated) {
			return err
		}
		return fmt.Errorf("%s: %w", KeyCustodyKeyInvalidated, err)
	case errors.Is(err, crypto.ErrKeyAuthRequired):
		if strings.Contains(msg, KeyCustodyAuthRequired) {
			return err
		}
		return fmt.Errorf("%s: %w", KeyCustodyAuthRequired, err)
	}
	return err
}
