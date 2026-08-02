package phonecore

// PB-PUSH-3's RECEIVER half: the push envelope TTL and replay window, at the only party
// that can enforce them.
//
// WHY THIS IS OWED AT ALL. Section 6.0 requires a "Push envelope TTL / replay window, 10 min,
// with the replay coordinate persisted per PB-STATE-1", against PB-PUSH-3, which slice S12
// shipped. The coordinate IS persisted -- State.WakeReplay, sealed under the wake tier since
// S15 -- and the SENDER half is covered (`TestPBPUSH3_WakeCarriesAMonotonicReplayCoordinate`,
// `_WakeSeqDoesNotRestartAfterAGatewayRestart`). But outside state.go's own persist/merge/load
// plumbing, NOTHING in internal/ or mobile/ ever wrote it. A wake envelope replayed by the
// relay -- the declared adversary under PB-SYNC-6, and the party that necessarily handles
// every wake -- was therefore not detected by anyone.
//
// WHAT A REPLAY COSTS, stated so the fence is not oversold. The payload is content-free and a
// constant 78 bytes with zeroed key ids (ADR-007 B20), so a replay discloses nothing. It costs
// a spurious reconnect and battery, repeatable for as long as the relay cares to repeat it, on
// a device whose only wake path this is. Bounded, and until now unmet.
//
// WHY IT CANNOT LIVE IN internal/remote/crypto. That package is FROZEN.
// crypto.MailboxReceiver already does exactly this job for type-0x01 mailbox frames and
// refuses type 0x02 by construction (`Accept` returns ErrWrongKeyType), so the wake path has
// no receiver at all. The check belongs here anyway: the coordinate it advances is durable
// state, and durable state is this package's.

import (
	"errors"
	"fmt"
	"time"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// WakeMaxAge is section 6.0's push envelope TTL: a wake whose AUTHENTICATED issued_at is
// older than this is refused.
//
// It is a bound on how long a captured wake stays useful, not a freshness nicety. The seq
// window alone cannot express it: a relay that captures wake seq N and holds it while the
// gateway is quiet can deliver it days later, and seq N is still the highest the phone has
// seen. issued_at is AAD-covered (crypto/envelope.go), so it is authenticated rather than
// advisory -- an attacker cannot move it without breaking the tag.
const WakeMaxAge = 10 * time.Minute

var (
	// ErrWakeReplay refuses a wake at or below the persisted replay coordinate.
	ErrWakeReplay = errors.New("phonecore: push wake replays or reorders the persisted wake coordinate")

	// ErrWakeExpired refuses a wake whose authenticated issued_at is older than WakeMaxAge.
	ErrWakeExpired = errors.New("phonecore: push wake is older than the replay window")

	// ErrNoWakeKey refuses a wake at a phone that holds no epoch wake key, and it is a
	// SEPARATE identity from the AEAD failure a forgery produces because the two are
	// indistinguishable from the bytes and have opposite remedies.
	//
	// A phone paired minutes ago and backgrounded before its first grant landed is in
	// PB-APP-10's third state: it is WAITING, the gateway re-appends its sidecar every
	// session, and the state clears itself. Reported as PB-KEY-3's grant loss instead it
	// sends a healthy user to their machine, and BeginPairing fail-fasts while this device is
	// registered (PB-STATE-10), so the advice cannot even be followed. The facade maps this
	// to ErrClassAwaitingKey; without the distinct identity it could only guess.
	ErrNoWakeKey = errors.New("phonecore: the phone holds no epoch wake key yet, so no push wake can be authenticated")
)

// AcceptWake authenticates one push-wake envelope and, only if it is genuine, fresh and
// beyond the persisted coordinate, advances that coordinate DURABLY.
//
// The order is the whole contract and is not an implementation detail:
//
//  1. parse and require type 0x02 -- a mailbox frame must never be accepted here;
//  2. require the wake to name the epoch this phone holds a key for;
//  3. OPEN under the epoch wake key, which authenticates the header including issued_at;
//  4. only then compare seq against State.WakeReplay and issued_at against WakeMaxAge;
//  5. only then persist the advanced coordinate.
//
// Steps 3-before-4 is the part that is easy to get backwards and expensive to get wrong. A
// receiver that advanced the coordinate before authenticating would hand the relay a
// one-packet permanent denial of service: an unopenable envelope carrying seq 2^63 pins the
// window at the top and every genuine wake afterwards is refused as a replay, on a device
// whose ONLY background path from the machine is this one.
//
// Steps 1 and 3 are ONE call for the TYPE. crypto.OpenWake refuses a type-0x01 envelope
// before it touches the AEAD (A15/F10), so reaching for the header type here would be a
// second implementation of a rule the frozen package already owns -- and the failure mode of
// the second copy is a mailbox frame advancing the wake window, which lets the machine's
// ordinary journal traffic starve the wake path.
//
// STEP 2 IS NOT REDUNDANT WITH THE TAG, which is the reading that leaves it out. The epoch id
// is AAD-covered, so it cannot be EDITED after sealing -- but a sealer that stamps another
// epoch from the start produces a tag that verifies perfectly under the key it sealed with.
// The wake key is per epoch and a revoke rotates it, so an envelope naming another epoch is
// one this phone must not act on however well it opens; and because the coordinate is NOT
// reset by a rotation, an accepted old-epoch wake carrying a high seq is the same
// pin-the-window lever a forgery would be. It is checked against durable state rather than
// inferred from a decrypt failure, because inferring it is exactly the step that does not
// hold.
//
// It must work with the content tier LOCKED, because that is the only condition it ever runs
// in: a push arrives with no user present. Nothing here touches content-tier state -- the
// write carries the state this process already holds with one wake-tier field changed, so a
// locked process writes the wake container and carries the two content containers verbatim
// (see fileStore.Save). There is no rebind either: no derived component reads WakeReplay, and
// rebuilding the router and the op queue on a wake would be work done with no user present
// for a coordinate nothing is bound to.
func (c *Core) AcceptWake(raw []byte) error {
	env, err := crypto.ParseEnvelope(raw)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// KEYLESSNESS IS TESTED FIRST, before the epoch. A phone that has never been granted
	// anything holds epoch 0, so an epoch check placed above this one answers every wake with a
	// mismatch -- an invalid-request verdict for PB-APP-10's third state, which is neither the
	// phone's fault nor anything the user can act on except by waiting.
	//
	// STATED AT ITS ACTUAL REACH, because the two orders are indistinguishable for most of the
	// phones that get here: a PAIRED phone has its epoch set by the pairing, so the epoch
	// matches and the keyless arm answers either way. What the order changes is the verdict at a
	// phone holding epoch ZERO, which is reachable -- PushTokens.requestInitialToken runs from
	// Application.onCreate before any pairing and relay registration is open, so an install that
	// has never paired can hold a registered push token and be woken. Pinned by
	// TestS17_AKeylessPhoneReportsNoWakeKeyRatherThanAnEpochMismatch, which is the only test in
	// the tree that separates the two orders: a reviewer swapped them and the whole S17 suite
	// stayed green.
	if c.st.Keys.WakeKey == (crypto.WakeKey{}) {
		return ErrNoWakeKey
	}
	if env.Header.EpochID != c.st.EpochID {
		return fmt.Errorf("phonecore: push wake names epoch %d, not %d",
			env.Header.EpochID, c.st.EpochID)
	}
	if _, err := crypto.OpenWake(c.st.Keys.WakeKey, env); err != nil {
		return err
	}
	// AUTHENTICATED from here down: every field consulted below is AAD-covered, so it is what
	// the machine wrote rather than what the relay chose.
	if env.Header.Seq <= c.st.WakeReplay {
		return ErrWakeReplay
	}
	if time.Since(time.UnixMilli(env.Header.IssuedAt)) > WakeMaxAge {
		return ErrWakeExpired
	}
	st := c.st.clone()
	st.WakeReplay = env.Header.Seq
	return c.persistLocked(st)
}
