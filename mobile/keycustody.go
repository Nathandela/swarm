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
// tokens below: an implementation whose exception message contains KeyCustodyKeyInvalidated
// or KeyCustodyAuthRequired is refusing PERMANENTLY (the key cannot be used; the device must
// pair again -- ADR-007 B133, see the constants). Anything else is an opaque failure and is
// treated as fatal, because a custody verdict that cannot be read must not be guessed at.
type KeyCustody interface {
	// WakeKEK is the wake-tier data key. It must open with NO USER PRESENT -- a push
	// arrives with nobody there -- so the Keystore key behind it is deliberately not
	// user-authentication-gated (ADR-007 B9/B16).
	WakeKEK() ([]byte, error)

	// ContentKEK is the content-tier data key, behind the biometric. It is the one that
	// legitimately refuses.
	ContentKEK() ([]byte, error)
}

// The two custody verdicts, as tokens that survive gomobile's error flattening. Kotlin stamps
// both onto the exceptions it throws out of KeyCustody; the facade reads them back and answers
// with ErrClassRepairRequired's token in the other direction (see barrier), which after ADR-007
// B133 is the one class both of them reach. They are exported so the Android side binds these
// constants instead of keeping a second copy of a discriminator string -- a copy that drifted
// would fail silently, turning a verdict with a real remedy into an opaque failure with none.
const (
	// KeyCustodyAuthRequired marks a key gated on a user authentication:
	// crypto.ErrKeyAuthRequired.
	//
	// IT WAS THE RECOVERABLE VERDICT AND IS NO LONGER ONE (ADR-007 B133). "The user must
	// authenticate" named an act the product can no longer offer -- every phone-side
	// authentication mechanism is removed -- so the refusal is now permanent in the only sense
	// that matters to the person holding the handset. The population that still raises it is an
	// install provisioned BEFORE B133, whose content KEK is still AUTH_BIOMETRIC_STRONG because
	// KeystoreCustodyBootstrap.ensure returns early when the alias exists. Pairing again is a
	// real fix for them: it discards the alias and re-provisions without an authenticator.
	//
	// THE TOKEN STAYS AND IS STILL STAMPED. dev.swarm.phone.keys.GoCustodyFailure
	// .AUTH_REQUIRED_TOKEN is the same string and Custody.kt still throws it, so removing this
	// constant would make that verdict unreadable and it would classify as ErrClassInternal --
	// "report a bug" for a handset that needs to pair again.
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
		// A KekProvider that answered the wrong width is a bug in the Android layer, not a
		// custody verdict: there is nothing for the user to authenticate or re-pair.
		return nil, classed(ErrClassInternal,
			fmt.Errorf("swarmmobile: the %s KEK is %d bytes, want 32", s.tier, len(key)))
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
		// The blob at rest cannot be opened by any key, so this is not a locked tier and no
		// prompt helps: the remedy is the permanent one.
		return nil, classed(ErrClassRepairRequired,
			fmt.Errorf("swarmmobile: the %s custody blob is truncated", s.tier))
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
//
// THE TWO VERDICTS KEEP DISTINCT SENTINELS AND SHARE ONE CLASS (ADR-007 B133). The sentinel is
// what the Go side of this repository routes on, and phonecore tells the two apart in several
// places; the CLASS is what the user is shown, and there is only one thing left to show either
// of them -- pair this device again. Collapsing the sentinels too would be a change to
// internal/remote/crypto's contract, which is frozen, for no gain on screen.
func classifyCustodyVerdict(tier string, err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, KeyCustodyKeyInvalidated):
		return classed(ErrClassRepairRequired, fmt.Errorf(
			"swarmmobile: the %s Keystore key is gone: %w: %v", tier, crypto.ErrKeyInvalidated, err))
	case strings.Contains(msg, KeyCustodyAuthRequired):
		return classed(ErrClassRepairRequired, fmt.Errorf(
			"swarmmobile: the %s Keystore key demands a user authentication this handset no "+
				"longer performs: %w: %v", tier, crypto.ErrKeyAuthRequired, err))
	}
	// Deliberately NOT one of the two verdicts. An opaque platform failure mapped onto "pair
	// this device again" is a remedy that cannot fix it, which is the failure PB-KEY-6 exists
	// to remove -- so it classifies as the bug it is.
	return classed(ErrClassInternal,
		fmt.Errorf("swarmmobile: the %s Keystore key could not be obtained: %w", tier, err))
}

// THE OUTBOUND HALF MOVED TO errorclass.go WITH SLICE S16. stampCustodyVerdict stamped these
// two verdicts and nothing else; PB-APP-9 generalised it to the whole surface, so
// stampErrorClass now does the same job for every error class -- including these two, which
// after ADR-007 B133 are both stamped ErrClassRepairRequired, whose token is the string
// KeyCustodyKeyInvalidated already carried and the one the Android side has matched on since
// S14 (dev.swarm.phone.keys.GoCustodyFailure.KEY_INVALIDATED_TOKEN).
//
// One stamping function rather than two is the point: two would each be total over their own
// arm and neither total over the surface, which is how a class ships unmapped.
