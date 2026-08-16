package swarmmobile

// The ADR-015 PUSH GATEWAY, as the Android app reaches it.
//
// WHAT THIS FILE IS FOR. Wave R3 shipped the phone-side gateway client, the durable
// installation identity, the per-pairing wake key and both revocation paths -- and NOTHING ON
// A HANDSET CALLED ANY OF IT. That is PB-PUSH-9's own warning ("a facade method can exist
// while no Android code ever calls it") and this project's standing defect class, so the
// registration verb below exists to be called from the ONE Kotlin funnel both token events
// already reach (PushTokens.register: SwarmApplication's initial getToken, and
// SwarmMessagingService.onNewToken's rotation).
//
// WHAT IS DELIBERATELY NOT HERE, so nobody reads the gap as an oversight:
//
//   - PLAY INTEGRITY. The gateway refuses an unattested registration (PG-AUTH-11/13) and the
//     verdict provider is owner-console setup this repository cannot do. The attestor below
//     therefore REFUSES BY NAME rather than inventing a token: a fabricated verdict is
//     simulated data in production code, and it would turn a refusal an operator can act on
//     into a 403 nobody can explain. A build with no attestation provider is honestly
//     foreground-only, which is the same graceful-and-loud shape PB-PUSH-5 already requires
//     of an absent Firebase project.
//   - THE PAIRING CONVEYANCE. Allocating an address and putting the binding in msg4 is gated
//     on a machine-side capability signal that does not exist yet (see
//     pairing.DeviceParams.PushBinding's MIXED-VERSION OBLIGATION); wiring it without that
//     signal breaks every mixed-version pair.
//
// The gateway URL crosses on Config, exactly like the relay URL: the phone core has no
// durable field for either, and the Android side supplies both at construction.

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
)

// pushRegisterTimeout bounds one registration round trip. Both callers are background
// callbacks with no user present, so the bound exists to stop a dead radio pinning an
// Android thread, not to make anybody wait less.
const pushRegisterTimeout = 30 * time.Second

var (
	// errNoPushGateway is a build with no gateway endpoint configured. It is an ERROR and
	// not a silent success for the reason PB-PUSH-5 gives: a phone that reports a healthy
	// push path it does not have is the failure that is invisible on both sides.
	errNoPushGateway = classed(ErrClassOffline, errors.New(
		"swarmmobile: no push gateway is configured for this build; the phone works without "+
			"push and will not receive background wakes"))

	// errPushAttestationParked is the ONE external this slice cannot supply. See the file
	// comment: a named refusal, never a fabricated verdict token.
	errPushAttestationParked = classed(ErrClassOffline, errors.New(
		"swarmmobile: no app attestation provider is configured for this build, and the push "+
			"gateway refuses an unattested registration (PG-AUTH-11/13); the phone works "+
			"without push and will not receive background wakes"))
)

// PushWakeDrops is the refused-wake counter, with the reason attached.
//
// IT IS A DIAGNOSIS AND NOT A HEALTH BADGE. A phone whose machine has a clock three minutes
// ahead has 100% of its wakes correctly refused, forever -- remotegw stamps issued_at from
// the machine clock and re-sends the SAME sealed bytes on every retry (PG-WAKE-12) -- and
// until this record existed that was indistinguishable from someone forging wakes, because
// the only trace was a single total. PeerClockAhead and Unauthenticated are the two answers,
// and they have different remedies: fix the machine's clock, or do not trust this address.
//
// The fields are int64 because that is what crosses the bind boundary; the core's counter is
// unsigned and monotonic, so a negative value here is not reachable.
type PushWakeDrops struct {
	// Total is every refusal.
	Total int64
	// Malformed is a payload refused on SHAPE before the AEAD was touched (PG-WAKE-3).
	Malformed int64
	// NoKey is the WAITING verdict: no wake key held for the address yet.
	NoKey int64
	// Revoked is a wake for an address a machine-side revoke severed forever (PG-WAKE-14).
	Revoked int64
	// Unauthenticated is a BAD MAC: the envelope did not open under the address's key.
	Unauthenticated int64
	// Replay is an authenticated envelope at or below the per-address high-water.
	Replay int64
	// Expired is an authenticated envelope older than the five-minute bound (PG-WAKE-7).
	Expired int64
	// PeerClockAhead is an authenticated envelope stamped too far in the FUTURE: the
	// sending machine's clock is ahead, and every wake it sends is refused until it is
	// fixed.
	PeerClockAhead int64
}

// WakeDropCounts reports the durable refused-wake counter. It needs no Start and no
// connection: the counter is durable state, and the process that reads it is usually not the
// process that refused anything.
func (a *App) WakeDropCounts() (counts *PushWakeDrops, err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return nil, err
	}
	c := core.WakeDropCounts()
	return &PushWakeDrops{
		Total:           int64(c.Total),
		Malformed:       int64(c.Malformed),
		NoKey:           int64(c.NoKey),
		Revoked:         int64(c.Revoked),
		Unauthenticated: int64(c.Unauthenticated),
		Replay:          int64(c.Replay),
		Expired:         int64(c.Expired),
		PeerClockAhead:  int64(c.PeerClockAhead),
	}, nil
}

// EnsurePushRegistration registers this installation with the push gateway, or rotates the
// token under the installation it already holds (ADR-015 P5, push-gateway-api.md 3.1-3.2).
// It is the verb Android's two token moments call, through their one shared funnel.
//
// THE TOKEN IS RECORDED BEFORE IT IS USED, and that is the fix the core's TokenSource seam
// exists for. Two callers can be in flight -- the initial getToken and an onNewToken
// rotation arrive on different threads -- and the core serialises them end to end. A caller
// that read its token first and won the lock second would install a token that was already
// stale, and no phone-side rule can order two opaque token strings after the fact. So this
// verb stores the token at ARRIVAL and hands the core a source that reads the newest one at
// ACT time: whichever caller writes last, the wire and the durable record end on it.
//
// EVERY REFUSAL IS RETURNED. The Kotlin caller catches and logs it (both entry points are
// background callbacks with no user present), but a verb that swallowed its own refusal
// would be a phone that reports a push path it does not have.
func (a *App) EnsurePushRegistration(token string) (err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return err
	}
	if strings.TrimSpace(token) == "" {
		return classed(ErrClassInvalidRequest, errors.New(
			"swarmmobile: EnsurePushRegistration requires the provider's current token"))
	}

	a.mu.Lock()
	a.pushToken = token
	url := a.pushGatewayURL
	a.mu.Unlock()
	if url == "" {
		return errNoPushGateway
	}

	client, cerr := a.pushGatewayClient(core, url)
	if cerr != nil {
		return cerr
	}
	ctx, cancel := context.WithTimeout(context.Background(), pushRegisterTimeout)
	defer cancel()
	_, rerr := core.EnsurePushRegistration(ctx, client, a.currentPushToken)
	return rerr
}

// currentPushToken is the core's TokenSource: the newest token any caller has handed this
// App, read at act time under the core's registration lock.
func (a *App) currentPushToken() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pushToken == "" {
		// Unreachable through EnsurePushRegistration, which stores a non-empty token before
		// it ever reaches here -- so this is a bug in this file rather than a state a phone
		// can be in, and it is classed as one.
		return "", classed(ErrClassInternal,
			errors.New("swarmmobile: no push token has been reported to this App"))
	}
	return a.pushToken, nil
}

// pushGatewayClient returns the one client this App uses, building it on first need. It is
// cached because the client carries the clock offset PG-AUTH-3's server_time taught it: a
// fresh client per call would re-learn a handset's clock skew on every registration, one
// wasted round trip at a time.
func (a *App) pushGatewayClient(core *phonecore.Core, url string) (*phonecore.GatewayClient, error) {
	a.mu.Lock()
	if a.pushClient != nil {
		defer a.mu.Unlock()
		return a.pushClient, nil
	}
	a.mu.Unlock()

	// Built with a.mu RELEASED: minting the installation key seals and rewrites the push
	// container, which takes the core's own lock and reaches Keystore through the wake-tier
	// sealer.
	signer, err := core.InstallationSigner()
	if err != nil {
		return nil, err
	}
	client := phonecore.NewGatewayClient(url, signer, parkedAttestor, nil)

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pushClient == nil {
		a.pushClient = client
	}
	return a.pushClient, nil
}

// parkedAttestor is the Play Integrity seam with no provider behind it (see the file
// comment). It refuses; it does not invent.
func parkedAttestor(_ [32]byte) (string, error) {
	return "", errPushAttestationParked
}
