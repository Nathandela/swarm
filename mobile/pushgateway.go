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
// Android installs Play Integrity and its Keystore P-256 signer through
// ConfigurePushRegistration immediately after NewApp. A platform/build that does not install
// both retains the named fail-closed path below: it enrolls nothing and remains foreground-only.
// No fake verdict or exportable fallback authority is minted.
//
// PAIRING CONVEYANCE IS HERE TOO. preparePairingPushBinding runs only after the QR msg1/msg2
// negotiation authenticated machine support, allocates and stages immediately before msg4,
// and returns the exact rollback arm pairing.RunDevice invokes on every non-accept outcome.
// A legacy QR, short code, old machine, unregistered phone, or build without the two platform
// authorities stays on the byte-compatible foreground path and allocates nothing.
//
// The gateway URL crosses on Config, exactly like the relay URL: the phone core has no
// durable field for either, and the Android side supplies both at construction.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/pairing"
)

// pushRegisterTimeout bounds one registration round trip. Both callers are background
// callbacks with no user present, so the bound exists to stop a dead radio pinning an
// Android thread, not to make anybody wait less.
const (
	pushRegisterTimeout       = 30 * time.Second
	pushPairingCleanupTimeout = 10 * time.Second
)

var pushPairingCleanupBackoffs = []time.Duration{0, 250 * time.Millisecond, time.Second, 4 * time.Second}

var (
	// errNoPushGateway is a build with no gateway endpoint configured. It is an ERROR and
	// not a silent success for the reason PB-PUSH-5 gives: a phone that reports a healthy
	// push path it does not have is the failure that is invisible on both sides.
	errNoPushGateway = classed(ErrClassOffline, errors.New(
		"swarmmobile: no push gateway is configured for this build; the phone works without "+
			"push and will not receive background wakes"))

	// errPushAttestationParked is the explicit fallback when a platform installed no real
	// providers. It is a named refusal, never a fabricated verdict token.
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

	client, cerr := a.pushGatewayClient(url)
	if cerr != nil {
		return cerr
	}
	ctx, cancel := context.WithTimeout(context.Background(), pushRegisterTimeout)
	defer cancel()
	_, rerr := core.EnsurePushRegistration(ctx, client, a.currentPushToken)
	if rerr == nil {
		a.schedulePendingPairingPushRevokes()
	}
	return rerr
}

// schedulePendingPairingPushRevokes starts one bounded cleanup worker when startup or a
// foreground token callback discovers durable failed-pairing obligations. It never waits
// under App.mu, pushProviderMu, or Core.mu. A permanently unavailable gateway leaves the
// marker intact after bounded backoff; the next foreground/configure trigger safely retries.
func (a *App) schedulePendingPairingPushRevokes() {
	if a == nil || a.core == nil || a.core.PushInstallationID() == "" || len(a.core.PendingPushBindingRevocations()) == 0 {
		return
	}
	a.mu.Lock()
	if a.closed || a.pushCleanupRunning || a.pushGatewayURL == "" || a.pushAttestor == nil || a.pushSigner == nil {
		a.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.pushCleanupRunning = true
	a.pushCleanupCancel = cancel
	a.pushCleanupWG.Add(1)
	url := a.pushGatewayURL
	a.mu.Unlock()

	go a.runPendingPairingPushRevokes(ctx, url)
}

func (a *App) runPendingPairingPushRevokes(ctx context.Context, url string) {
	defer a.pushCleanupWG.Done()
	defer func() {
		a.mu.Lock()
		a.pushCleanupRunning = false
		a.pushCleanupCancel = nil
		a.mu.Unlock()
	}()
	client, err := a.pushGatewayClient(url)
	if err != nil {
		return
	}
	for _, backoff := range pushPairingCleanupBackoffs {
		if backoff > 0 {
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return
			case <-timer.C:
			}
		}
		if a.drainPendingPairingPushRevokes(ctx, client) == nil {
			return
		}
	}
}

func (a *App) drainPendingPairingPushRevokes(parent context.Context, client *phonecore.GatewayClient) error {
	for _, pending := range a.core.PendingPushBindingRevocations() {
		ctx, cancel := context.WithTimeout(parent, pushPairingCleanupTimeout)
		err := client.RevokeAddress(ctx, pending)
		cancel()
		if err != nil {
			return err
		}
		if err := a.core.CompleteStagedPushRevoke(pending); err != nil {
			return err
		}
	}
	return nil
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

// preparePairingPushBinding is the phone half of the negotiated msg4 extension. It is
// deliberately called lazily by pairing.RunDevice immediately before msg4: allocating
// before the SAS gate would release a live public address for every abandoned scan.
//
// A configured URL is not enough. Both Android production authorities and an accepted
// installation must already exist, otherwise this phone is honestly foreground-only and
// returns a nil binding without touching the gateway. Every allocation is paired with a
// sealed compensation marker before it can leave the process.
func (a *App) preparePairingPushBinding(ctx context.Context) (*pairing.PushBinding, func(), error) {
	core, err := a.ready()
	if err != nil {
		return nil, nil, err
	}
	a.mu.Lock()
	url := a.pushGatewayURL
	productionProviders := a.pushAttestor != nil && a.pushSigner != nil
	a.mu.Unlock()
	if url == "" || !productionProviders || core.PushInstallationID() == "" {
		return nil, nil, nil
	}
	client, err := a.pushGatewayClient(url)
	if err != nil {
		return nil, nil, err
	}

	// A killed earlier ceremony may have left an allocation whose key was already
	// erased locally. Clear those obligations before minting another address so repeated
	// failures cannot accumulate live public objects.
	for _, pending := range core.PendingPushBindingRevocations() {
		if err := client.RevokeAddress(ctx, pending); err != nil {
			return nil, nil, err
		}
		if err := core.CompleteStagedPushRevoke(pending); err != nil {
			return nil, nil, err
		}
	}

	alloc, err := client.AllocateAddress(ctx, core.PushInstallationID())
	if err != nil {
		return nil, nil, err
	}
	wakeKey, err := phonecore.NewPairingWakeKey()
	if err != nil {
		revokePairingAllocation(client, alloc.Address)
		return nil, nil, err
	}
	if err := core.StagePushBinding(alloc.Address, wakeKey); err != nil {
		revokePairingAllocation(client, alloc.Address)
		return nil, nil, err
	}

	binding := &pairing.PushBinding{
		WakeKey:                 append([]byte(nil), wakeKey[:]...),
		PushAddress:             append([]byte(nil), alloc.Address[:]...),
		SubmitCapability:        alloc.SubmitCapability,
		MachineRevokeCapability: alloc.MachineRevokeCapability,
		CapabilityRecordVersion: schema.CurrentCapabilityRecordVersion,
	}
	var once sync.Once
	rollback := func() {
		once.Do(func() {
			// Once the pin+ownership transaction has landed, this allocation belongs to
			// the pinned phone even if the final acknowledgement is lost. RunDevice calls
			// rollback on every post-msg4 error, so consult durable classification rather
			// than revoking an address whose acceptance is already locally committed.
			owned, found, err := core.PairingPushOwnership()
			if err != nil {
				return // fail closed: an unreadable ownership phase is never authority to revoke
			}
			if found && owned == alloc.Address {
				return
			}
			if !core.StagedPushBindingPending(alloc.Address) {
				return
			}
			// Erase first and durably retain the revoke obligation. The network leg is
			// detached from the pairing context because that context is normally already
			// cancelled on exactly the paths that call this rollback.
			_ = core.AbandonStagedPushBinding(alloc.Address)
			cleanupCtx, cancel := context.WithTimeout(context.Background(), pushPairingCleanupTimeout)
			defer cancel()
			if err := client.RevokeAddress(cleanupCtx, alloc.Address); err == nil {
				_ = core.CompleteStagedPushRevoke(alloc.Address)
			}
		})
	}
	return binding, rollback, nil
}

func revokePairingAllocation(client *phonecore.GatewayClient, addr phonecore.PushAddress) {
	ctx, cancel := context.WithTimeout(context.Background(), pushPairingCleanupTimeout)
	defer cancel()
	_ = client.RevokeAddress(ctx, addr)
}

// pushGatewayClient returns the one client this App uses, building it on first need. It is
// cached because the client carries the clock offset PG-AUTH-3's server_time taught it: a
// fresh client per call would re-learn a handset's clock skew on every registration, one
// wasted round trip at a time.
func (a *App) pushGatewayClient(url string) (*phonecore.GatewayClient, error) {
	a.pushProviderMu.Lock()
	defer a.pushProviderMu.Unlock()
	a.mu.Lock()
	if a.pushClient != nil {
		defer a.mu.Unlock()
		return a.pushClient, nil
	}
	a.mu.Unlock()

	// Built with a.mu RELEASED: the legacy fallback can seal and rewrite the push
	// container. Production installs both reverse-bound providers before this path runs.
	a.mu.Lock()
	platformAttestor, platformSigner := a.pushAttestor, a.pushSigner
	a.mu.Unlock()
	var signer phonecore.InstallationSigner
	attest := parkedAttestor
	if platformAttestor != nil && platformSigner != nil {
		signer = platformSigner
		attest = func(requestHash [32]byte) (string, error) {
			return platformAttestor.Attest(append([]byte(nil), requestHash[:]...))
		}
	} else {
		// No production authorities means no registration can pass attestation. Do not mint
		// an exportable Go private key merely to reach that named refusal.
		signer = parkedInstallationSigner{}
	}
	client := phonecore.NewGatewayClient(url, signer, attest, nil)

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pushClient == nil {
		a.pushClient = client
	}
	return a.pushClient, nil
}

// parkedAttestor is the fail-closed no-provider path. It refuses; it does not invent.
func parkedAttestor(_ [32]byte) (string, error) {
	return "", errPushAttestationParked
}

type parkedInstallationSigner struct{}

func (parkedInstallationSigner) PublicKey() []byte { return nil }
func (parkedInstallationSigner) Sign([]byte) ([]byte, error) {
	return nil, errPushAttestationParked
}
