package swarmmobile

// The pairing surface (PB-PAIR-2/-4/-5/-6, PB-SAS-1/-2).
//
// Decoding a QR and JOINING what it names are separate calls on purpose: PB-PAIR-6
// requires the destination to be displayed and confirmed before anything is joined, and
// that is impossible if the two are one call.
//
// The SAS is computed by the SHARED Go core from the Noise channel binding and crosses as
// ONE display string. The emoji table is never re-implemented on the Kotlin side: a
// second table is a second source of truth, and the two ends disagreeing is
// indistinguishable, to the user, from the man-in-the-middle the SAS exists to catch.

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/pairing"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// The pairing state machine (PB-PAIR-5). Every terminal state is its OWN value: collapsed
// into "failed" with prose beside it, the screen can only show the user an error string,
// which is the opaque error the requirement exists to remove -- and three of these need
// genuinely different next steps.
const (
	// pairConfirmDestination is PB-PAIR-6's step, and it is the FIRST state now. Nothing has
	// been joined: the QR has been decoded for display and the phone is waiting to be told
	// that the destination it is about to connect to is the one the user meant.
	pairConfirmDestination = "confirm_destination"
	pairPairing            = "pairing"    // the handshake is running; a SAS may be derived
	pairConfirming         = "confirming" // the user compared the two displays and said yes
	pairPaired             = "paired"

	// The five terminal states PB-PAIR-5 enumerates, plus the two the flow already had.
	pairDeclined    = "declined"     // the machine operator refused at their own SAS gate
	pairSASMismatch = "sas_mismatch" // the user said the two displays DISAGREE
	pairTimeout     = "rendezvous_timeout"
	pairExpired     = "expired" // the rendezvous is gone: TTL elapsed, or the QR was used

	// pairDifferentMachine is the QR belonging to a machine OTHER than the one this phone is
	// already pinned to, and it is decided MID-HANDSHAKE rather than at BeginPairing.
	//
	// It has to be. PB-PAIR-7 decided not to carry the machine's Noise static in the QR, so
	// nothing before msg2 knows which machine a QR belongs to -- the discriminator is
	// pairing.DeviceOutcome.MachineStatic, and it does not exist until the handshake
	// authenticates it.
	//
	// The defect it closes is destructive and silent: pin() assigned MachineStatic,
	// MachineSignPub and MachineRelayAuthPub unconditionally, so a phone paired to A that
	// scanned B's QR re-pinned to B. v1 is single-machine (section 5 cut the switcher), so the
	// user has abandoned the machine they were working on, with no warning, no terminal state,
	// and an empty roster as the first symptom.
	//
	// A GUARD ON "IS THIS PHONE ALREADY PAIRED" WOULD BE WRONG, and was written that way first.
	// Re-pairing the SAME machine is a supported flow -- a revoke rotates the epoch and the
	// phone pairs again, which is what pin() exists to serve -- so keying on the fact of a
	// pairing rather than on WHICH machine deletes it, and then needs a carve-out for revoked
	// and custody-invalidated handsets to stop PB-APP-10's own remedy becoming unreachable.
	pairDifferentMachine = "different_machine"

	pairCancelled   = "cancelled"
	pairRateLimited = "rate_limited"
	pairFailed      = "failed"

	// pairOriginMismatch is what a confirmation naming a DIFFERENT destination from the one
	// displayed leaves behind. It is terminal and it is not "cancelled": the user said yes to
	// one URL and something offered another, which is a security event.
	pairOriginMismatch = "refused_origin_mismatch"
)

// pairingTTL is how long the phone will wait on a rendezvous before declaring
// rendezvous_timeout.
//
// §6.0 pins it at 60 s to match the relay's authoritative RendezvousTTL. It is transcribed
// here rather than read from relay.DefaultConfig() because that is the SERVER's default and
// the phone is talking to a relay whose configuration it cannot see; what the phone can
// guarantee is that its own deadline is never LATER than the pinned one, since a phone still
// waiting on a rendezvous the relay has already destroyed can only ever fail.
//
// Without a declared deadline the handshake blocks on RendezvousRecv forever and
// rendezvous_timeout is a state nothing can reach.
const pairingTTL = 60 * time.Second

// DecodeQR parses a scanned pairing QR into what the scanner screen may DISPLAY. The
// pairing secret is deliberately not part of the result: it never leaves the Go core.
// Fails closed on a malformed payload.
func DecodeQR(qr string) (p *QRPayload, err error) {
	defer barrier(&err)
	payload, err := pairing.DecodeQR(qr)
	if err != nil {
		return nil, classed(ErrClassPairingFailed, err)
	}
	return &QRPayload{
		RelayURL:     payload.RelayURL,
		RendezvousID: hex.EncodeToString(payload.RendezvousID[:]),
		HasStaticPub: len(payload.MachineStaticPub) == 32,
	}, nil
}

// Pairing is one in-flight pairing attempt. It is a handle, not a value: the handshake
// runs on a Go goroutine and the screen polls it.
type Pairing struct {
	mu       sync.Mutex
	origin   string
	private  bool
	sas      string
	state    string
	err      error
	deadline time.Time

	// payload is the decoded QR, held from BeginPairing until ConfirmOrigin releases it.
	// Nothing in it reaches the screen except the origin: the pairing secret never leaves
	// the Go core.
	payload pairing.QRPayload

	confirmed chan struct{}
	once      sync.Once
	joinOnce  sync.Once
	cancel    context.CancelFunc

	// abandoned records that App.Close tore this attempt down, and reached the transition it
	// had got to when that happened. See abandon: a shutdown is not a resolution, so it must
	// not clear PB-PAIR-4's durable record.
	abandoned bool
	reached   string

	mu2  sync.Mutex // guards conn, which is written by ConfirmOrigin and read by Cancel
	conn *relay.Conn

	app *App
}

// BeginPairing decodes a scanned QR and stops. It JOINS NOTHING (PB-PAIR-6).
//
// THE DEFECT THIS CLOSES. This used to dial on its second statement:
//
//	payload, err := pairing.DecodeQR(qr)
//	conn, err := relay.DialRaw(ctx, payload.RelayURL)
//
// so a QR naming an attacker's relay had the handset's TCP connection before the user had
// seen the URL -- and a connection is already the whole disclosure: the attacker learns the
// handset's IP, that it holds a swarm pairing QR, and when it was scanned. Refusing afterwards
// does not take that back. The file's own comment said the split existed for this reason, and
// it did not: DecodeQR and BeginPairing were separate calls, but BeginPairing performed BOTH
// halves, so an app that decoded for display and then began was exactly the app the
// requirement describes.
//
// The handle comes back in confirm_destination. ConfirmOrigin is the only thing that dials.
func (a *App) BeginPairing(qr string) (p *Pairing, err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return nil, err
	}
	payload, err := pairing.DecodeQR(qr)
	if err != nil {
		return nil, classed(ErrClassPairingFailed, err)
	}

	// NO CONTEXT AND NO CANCEL FUNC YET, because there is nothing to cancel: the handshake
	// context is created by join(), which is the only thing that dials. Cancel before then is
	// a state change and a file write.
	pr := &Pairing{
		origin:  payload.RelayURL,
		private: originIsPrivate(payload.RelayURL),
		state:   pairConfirmDestination,
		// DECLARED AT THE START, not at the dial. The rendezvous the QR names was created by
		// the machine before the QR was rendered, so the clock the relay destroys it against
		// is already running; a deadline measured from the user's confirmation would outlive
		// it.
		deadline:  time.Now().Add(pairingTTL),
		payload:   payload,
		confirmed: make(chan struct{}),
		app:       a,
	}

	// The Noise-static key is CONTENT tier, so on a locked device pairing must stop here
	// rather than handshaking with a nil handle (ADR-007 B14). It is resolved BEFORE the
	// attempt is recorded, so a locked handset is refused without leaving a pairing the next
	// launch would offer to resume.
	if _, err = core.KeyStore().NoiseStatic(); err != nil {
		return nil, err
	}
	pr.persist(pairConfirmDestination)
	return pr, nil
}

// Origin is the destination the scanned QR named, for the confirm sheet to render.
func (p *Pairing) Origin() (origin string, err error) {
	defer barrier(&err)
	if p == nil {
		return "", errNoReceiver
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state == "" {
		return "", errNoReceiver
	}
	return p.origin, nil
}

// OriginIsPrivate reports whether the destination is a private or link-local address.
//
// PB-PAIR-6 resolves the LAN case EXPLICITLY: a private destination is ALLOWED after display
// and confirmation, because a blanket private-address rule would reject the very handset
// demonstration PB-OPS-1 describes -- a phone reaching the laptop over the LAN. So the confirm
// sheet has to be able to say which kind it is showing.
//
// The classification travels with the handle rather than being redone in Kotlin, for PB-SAS-1's
// reason: a second implementation of a security-relevant rule is a second thing to get wrong,
// and the two ends disagreeing is invisible.
//
// It never resolves a hostname. A DNS lookup before the user has confirmed anything is a
// disclosure of a different shape -- it tells a resolver, and anyone watching it, that this
// handset is about to pair with that name.
func (p *Pairing) OriginIsPrivate() (private bool, err error) {
	defer barrier(&err)
	if p == nil {
		return false, errNoReceiver
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	// An empty state is a handle this package never constructed (PB-BIND-5). Answering
	// "false, nil" for one would tell a confirm sheet that a destination it knows nothing
	// about is on the public internet.
	if p.state == "" {
		return false, errNoReceiver
	}
	return p.private, nil
}

// DeadlineMillis is the unix-millisecond instant this pairing gives up and becomes
// rendezvous_timeout. See pairingTTL for why it exists and why it is never later than the
// relay's own.
func (p *Pairing) DeadlineMillis() (at int64, err error) {
	defer barrier(&err)
	if p == nil {
		return 0, errNoReceiver
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	// As OriginIsPrivate: a zero Pairing has no deadline, and the zero time rendered as unix
	// milliseconds is a date in 1754 that a screen would happily display as "expired".
	if p.state == "" {
		return 0, errNoReceiver
	}
	return p.deadline.UnixMilli(), nil
}

// ConfirmOrigin is the user's yes, and it is the ONLY thing in this package that joins a
// destination (PB-PAIR-6).
//
// IT CARRIES THE ORIGIN BACK, which is what makes a swap after display impossible rather than
// merely unlikely: the phone compares the string the sheet actually rendered against the
// payload it decoded, and refuses a mismatch. Passing nothing would leave the sheet's content
// and the phone's destination two independent facts.
//
// THE DIAL IS ASYNCHRONOUS AND THAT IS DELIBERATE. A confirm button must not block on a
// network round trip, and -- more importantly -- a destination that cannot be reached is a
// PAIRING STATE the screen renders (failed / expired / rendezvous_timeout), not an error on
// the button. Returning the dial error here would put a transport failure and a swapped origin
// through the same channel, and only one of those is the user's business.
func (p *Pairing) ConfirmOrigin(origin string) (err error) {
	defer barrier(&err)
	if p == nil {
		return errNoReceiver
	}
	p.mu.Lock()
	state := p.state
	want := p.origin
	p.mu.Unlock()
	if state == "" {
		return errNoReceiver
	}
	if state != pairConfirmDestination {
		return classed(ErrClassPairingFailed, errors.New(
			"swarmmobile: this pairing's destination has already been settled (state "+state+")"))
	}
	if origin != want {
		p.setState(pairOriginMismatch)
		p.cancelHandshake()
		return classed(ErrClassPairingFailed, errors.New(
			"swarmmobile: the confirmation names "+origin+" and the scanned QR names "+want+
				"; the destination was swapped after it was displayed and nothing has been joined"))
	}
	// The state moves SYNCHRONOUSLY, before the goroutine is spawned. A caller that polls
	// State() immediately after confirming must not see confirm_destination again: to every
	// reader that value means "waiting for the user", and a screen -- or a test loop -- that
	// treats it as terminal would report the pairing dead in the millisecond before the dial
	// returns. The user has answered; the pairing is in progress from that instant.
	p.setState(pairPairing)
	// The App owns the goroutine, so Close can tear it down and WAIT for it: the handshake's
	// last act is a write into the phone's state directory, and Close must not return while
	// one is outstanding (App.Close).
	p.joinOnce.Do(func() {
		// THE CANCEL FUNC IS INSTALLED BEFORE THE GOROUTINE STARTS. join() used to create it
		// as its own first act, which left a window in which a concurrent cancelHandshake
		// found p.cancel still nil, cancelled nothing, and returned -- and the handshake then
		// ran to its 60 s pairing deadline with nobody able to reach it. That was invisible
		// while Close did not wait; it is a 60 s hang on the caller's shutdown path now that
		// it does, which is the same defect either way, only louder.
		base, cancel := context.WithCancel(context.Background())
		p.mu.Lock()
		p.cancel = cancel
		p.mu.Unlock()
		if !p.app.startPairingJoin(p, base) {
			// The app closed between the user's yes and the dial. Nothing was joined, and the
			// attempt must not be left claiming to be in progress.
			cancel()
			p.setState(pairCancelled)
		}
	})
	return nil
}

// join dials the confirmed destination and drives the device half of the handshake. base
// carries the cancellation ConfirmOrigin installed; the deadline below is layered on it.
func (p *Pairing) join(base context.Context) {
	// Releases the context and any dialled connection on EVERY exit path, so a handshake that
	// ended on its own leaves nothing behind for Close to wait on.
	defer p.cancelHandshake()

	// The handshake's own deadline, so rendezvous_timeout is a state something can reach.
	// Derived from the deadline declared at BeginPairing, never from the confirmation.
	p.mu.Lock()
	deadline, payload, app := p.deadline, p.payload, p.app
	p.mu.Unlock()

	ctx, stop := context.WithDeadline(base, deadline)
	defer stop()

	// Under the handset's transport policy, like every other dial this app makes: the URL
	// came out of a scanned QR, so it is the LEAST trusted destination the phone ever
	// reaches. A cleartext one is refused here rather than after the connection, which
	// matters for the same reason BeginPairing no longer dials at all -- a connection is
	// already a disclosure (ADR-007 B37).
	//
	// THE PIN IS NOT APPLIED HERE AND CANNOT BE: this is the dial that fetches it, so
	// State.RelaySPKIPin is empty on the pairing that first learns it (App.handsetSecurity
	// carries the full argument). What guards this exchange is the Noise handshake and the
	// SAS the operator compares, not the relay's certificate; what the transport policy
	// contributes is the cleartext refusal, which is decided from the URL.
	conn, err := relay.DialRawSecure(ctx, payload.RelayURL, app.handsetSecurity())
	if err != nil {
		p.finish(nil, err, ctx)
		return
	}
	p.mu2.Lock()
	p.conn = conn
	p.mu2.Unlock()

	ks := app.core.KeyStore()
	static, err := ks.NoiseStatic()
	if err != nil {
		_ = conn.Close()
		p.finish(nil, err, ctx)
		return
	}
	params := pairing.DeviceParams{
		Static:           static,
		Secret:           payload.PairingSecret,
		RendezvousID:     payload.RendezvousID,
		MachineStaticPub: payload.MachineStaticPub,
		Payload: pairing.DevicePayload{
			DeviceName:           "swarm phone",
			DeviceRoutingID:      []byte(relay.RoutingID(ks.RelayAuthPublic())),
			DeviceRelayAuthPub:   ks.RelayAuthPublic(),
			RecipientPub:         ks.RecipientPublic(),
			DeviceCommandSignPub: ks.CommandSigningPublic(),
		},
		// The relay-route consent (ADR-007 B27/B38): this phone granting THE MACHINE IT
		// JUST AUTHENTICATED, and no other party, the right to append to, wake, and revoke
		// this phone's relay route. It is signed with the relay-auth key through the same
		// custody that answers the relay's connection challenge (KeyStore.SignRelayAuth) --
		// no new key, no new crypto, kept apart from the challenge by ConsentMessage's
		// domain separator.
		//
		// The grantee is derived from MachineRelayAuthPub rather than taken from
		// MachineRoutingID, so the consent binds to the key the relay will actually
		// authenticate the machine under and not to a routing-id string the machine
		// asserted alongside it.
		Consent: func(m pairing.MachinePayload) ([]byte, error) {
			return ks.SignRelayAuth(relay.ConsentMessage(relay.RoutingID(m.MachineRelayAuthPub)))
		},
		// The SAS gate: surfaced to the screen, then held until the operator has compared
		// it against the machine's own display. Returning an error here fails the pairing
		// CLOSED -- nothing is pinned.
		DeviceSAS: func(ctx context.Context, sas [6]string) error {
			p.setSAS(strings.Join(sas[:], " "))
			select {
			case <-p.confirmed:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}
	rt := &rendezvous{conn: conn, label: hex.EncodeToString(payload.RendezvousID[:])}
	out, err := pairing.RunDevice(ctx, params, rt)
	_ = conn.Close()
	p.finish(out, err, ctx)
}

// finish maps the handshake's outcome onto ONE of PB-PAIR-5's explicit terminal states.
//
// Each is its own value rather than "failed" plus prose, because the screen's next step
// differs: an expired QR is re-scanned from the machine, a declined pairing needs the operator
// at the other end, a timeout is retried, an already-paired phone must be revoked first, and a
// SAS mismatch is a suspected interception that must not be retried against the same attacker.
func (p *Pairing) finish(out *pairing.DeviceOutcome, err error, ctx context.Context) {
	p.mu.Lock()
	// A state the USER already settled wins: RejectSAS and Cancel both tear the handshake
	// down, so the error arriving here is the consequence of their answer, not a verdict of
	// its own.
	if p.state == pairCancelled || p.state == pairSASMismatch || p.state == pairOriginMismatch {
		p.mu.Unlock()
		p.persistState()
		return
	}
	switch {
	case err == nil && p.app.differentMachine(out):
		// The handshake succeeded and authenticated a machine that is not the one this phone
		// is pinned to. Nothing is pinned: the pairing the user had is left exactly as it was.
		p.state = pairDifferentMachine
	case err == nil:
		p.state = pairPaired
	case errors.Is(err, pairing.ErrPairingDeclined):
		p.state = pairDeclined
	case errors.Is(err, pairing.ErrRateLimited):
		p.state = pairRateLimited
	case errors.Is(err, relay.ErrRendezvousExpired), errors.Is(err, relay.ErrRendezvousBurned):
		// The rendezvous is gone: its TTL elapsed, or the QR was already used. Both look
		// identical from here and lead to the same place -- ask the machine for a fresh QR --
		// so they share a state rather than inventing a distinction the phone cannot make.
		p.state = pairExpired
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		p.state = pairTimeout
	case ctx.Err() != nil:
		p.state = pairCancelled
	default:
		p.state, p.err = pairFailed, err
	}
	pinned := p.state == pairPaired
	p.mu.Unlock()

	if pinned && out != nil {
		p.app.pin(out)
	}
	p.persistState()
}

// cancelHandshake tears down whatever the attempt is holding. Safe before the dial.
func (p *Pairing) cancelHandshake() {
	p.mu.Lock()
	cancel := p.cancel
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	p.mu2.Lock()
	conn := p.conn
	p.conn = nil
	p.mu2.Unlock()
	if conn != nil {
		// CloseNow, not Close: this is the ABORT path, and Close's graceful handshake burns
		// five seconds waiting for a close frame that a cancelled connection can never read
		// (relay.Conn.CloseNow). App.Close waits for this, so that timeout would be five
		// seconds of an Android lifecycle callback.
		_ = conn.CloseNow()
	}
}

// setState moves the state machine and records the move durably.
func (p *Pairing) setState(s string) {
	p.mu.Lock()
	p.state = s
	p.mu.Unlock()
	p.persist(s)
}

// abandon tears the handshake down because the APP IS CLOSING, which is not the user
// cancelling it, and the difference is durable.
//
// Cancel and RejectSAS RESOLVE an attempt -- the user has answered, so persist clears
// PB-PAIR-4's record. A process going away has answered nothing: the machine's half of the
// handshake may have committed, and the next launch must be able to say so rather than
// offer a scanner that will fail-fast against a device the machine still has registered
// (PB-STATE-10). So the state the handshake had REACHED is captured here and written in
// place of the cancellation the teardown is about to produce.
func (p *Pairing) abandon() {
	p.mu.Lock()
	p.abandoned, p.reached = true, p.state
	p.mu.Unlock()
	p.cancelHandshake()
}

func (p *Pairing) persistState() {
	p.mu.Lock()
	s := p.state
	if p.abandoned {
		s = p.reached // see abandon: a shutdown must not read as a resolution
	}
	p.mu.Unlock()
	p.persist(s)
}

// pairingStateFile is the name of PB-PAIR-4's durable record inside the state directory.
const pairingStateFile = "pairing-attempt"

// persist writes PB-PAIR-4's state machine to disk.
//
// WHY IT IS DURABLE AT ALL. Android SIGKILLs the app, and every transition of this handshake
// lived in a struct, a goroutine and a channel -- so the requirement's own kill/restart case
// had nothing to observe. The subject is not the loss but what the NEXT launch believes: a
// phone that has forgotten an in-flight attempt offers the user a scanner, and the machine may
// have committed, in which case BeginPairing fail-fasts while this device is registered
// (PB-STATE-10) and the only exit is physical access to the machine.
//
// WHY IT IS ITS OWN FILE AND NOT A phonecore.State FIELD, which is the placement a reader will
// expect from PB-STATE-1's "one enumerated durable schema". It was written that way first and
// the shipped guards reject it, correctly: TestState_EveryResumeCriticalFieldSurvivesARestart
// requires fullState() to set every exported field, and TestStateStore_PinnedSealedFixtures
// StillLoad then requires the pinned v4 and v5 blobs -- written before this field existed -- to
// restore that same fullState(). A new top-level field can only satisfy both by being spliced
// into fixtures that never carried it, which would falsify the one artifact proving forward
// migration works. The alternative was to weaken a shipped guard, and this record does not earn
// that: it is a UX coordinate, not a replay guard. Nothing in it is user content or key
// material -- no pairing secret, no Noise static, no SAS -- so PB-STATE-9 assigns it no tier,
// exactly as it assigns none to the staleness marks, and it is written in the clear beside the
// blob rather than inside it.
//
// paired and cancelled are RESOLVED and clear the record: after a success the phone simply is
// paired, and a cancellation is the user saying there is nothing to resume. Every other
// terminal state is kept, because the next launch has something to explain.
func (p *Pairing) persist(state string) {
	if state == pairPaired || state == pairCancelled {
		state = ""
	}
	// Best effort by design: a failed write must not take down a handshake that is otherwise
	// fine. What it costs is the next launch's explanation, not the pairing.
	_ = p.app.writePairingState(state)
}

// writePairingState replaces the record atomically, or removes it when there is nothing
// outstanding. Atomically because the process is killed without warning: a half-written record
// would be read by the next launch as a state nothing produced.
func (a *App) writePairingState(state string) error {
	if a.stateDir == "" {
		return classed(ErrClassInternal,
			errors.New("swarmmobile: no state directory; the App was not built by NewApp"))
	}
	path := filepath.Join(a.stateDir, pairingStateFile)
	if state == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return classed(ErrClassInternal, err)
		}
		return nil
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(state), 0o600); err != nil {
		return classed(ErrClassInternal, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return classed(ErrClassInternal, err)
	}
	return nil
}

// PairingState is the PERSISTED pairing state machine (PB-PAIR-4): "" when no attempt is
// outstanding, and otherwise the transition the last attempt reached.
//
// It is on App rather than on Pairing because the caller that needs it does not have a Pairing
// -- it is the launch after the process died, deciding whether to show a scanner or to say the
// machine may already hold this device. Those two need different screens, and without this the
// second is indistinguishable from the first.
func (a *App) PairingState() (state string, err error) {
	defer barrier(&err)
	if _, err = a.ready(); err != nil {
		return "", err
	}
	raw, rerr := os.ReadFile(filepath.Join(a.stateDir, pairingStateFile))
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return "", nil
		}
		return "", classed(ErrClassInternal, rerr)
	}
	return strings.TrimSpace(string(raw)), nil
}

// originIsPrivate classifies a destination URL's host as private/LAN without touching the
// network. A name it cannot classify from the literal alone is reported as NOT private, which
// is the conservative answer: the sheet then says "this is a destination on the internet",
// and a user shown that for their own laptop will notice, whereas the reverse would label an
// attacker's public relay as local.
func originIsPrivate(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return host == "localhost" || strings.HasSuffix(host, ".local") ||
		strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".home.arpa")
}

// differentMachine reports whether an authenticated handshake belongs to a machine other than
// the one this phone is already pinned to.
//
// The comparison is the machine's NOISE STATIC and nothing else. It is the identity the
// handshake authenticates -- msg2 carries it and the XXpsk0 pattern binds it -- whereas the
// relay-auth key and the grant-signing key are coordinates the machine may legitimately rotate
// under the same identity. Comparing either of those would refuse a same-machine re-pair after
// a rotation, which is the flow this guard must not break.
//
// A phone with NO pinned static has never completed a pairing that recorded one, so there is
// nothing to be different from and the answer is false.
func (a *App) differentMachine(out *pairing.DeviceOutcome) bool {
	if out == nil {
		return false
	}
	st := a.core.State()
	if len(st.MachineStatic) == 0 {
		return false
	}
	return !bytes.Equal(st.MachineStatic, out.MachineStatic)
}

// pin records the machine coordinates the handshake authenticated (PB-PAIR-7). It is the
// only place MachineRelayAuthPub is learned: the phone's send target derives from it, and
// without it the restored phone would know who the machine is and not how to reach it.
//
// A pairing that lands in a DIFFERENT epoch invalidates every epoch-scoped coordinate at
// once, and both of them are re-armed here rather than at the next process start. The
// tier keys belong to the old epoch: sealing under them while labelling the frame with
// the new epoch id yields frames the machine cannot open. The adopted rollback
// authorities belong to the old epoch too, and bound nothing in the new one -- so
// mutating ops must be refused until the machine republishes (PB-SYNC-7). NewApp already
// re-arms both on the next launch by comparing ReconciledEpoch against EpochID; on
// Android that launch can be hours away, and the whole window is one in which the live
// App permits mutations it cannot bound.
//
// IT MUTATES UNDER THE CORE LOCK, and that is the requirement rather than a tidy-up. This
// runs on the PAIRING goroutine while the relay drain runs on its own, and the machine's
// epoch grant lands immediately after a pairing -- so a read-modify-write across a released
// lock reverts State.EpochID and State.Keys to the pre-grant snapshot, destroying the content
// key the drain just installed while the monotonically-merged watermark survives at that
// grant's coordinates. The re-appended bootstrap frame is then refused as a replay forever.
// phonecore.Core.Mutate carries the whole account.
func (a *App) pin(out *pairing.DeviceOutcome) {
	var newEpoch bool
	err := a.core.Mutate(func(st *phonecore.State) {
		st.MachineStatic = out.MachineStatic
		st.MachineSignPub = out.Machine.MachineSignPub
		st.MachineRelayAuthPub = out.Machine.MachineRelayAuthPub
		// The relay's SPKI pin (ADR-007 B33/B34), and pairing is the ONLY channel that can
		// carry it: the QR has no room (MaxRelayURLLen = 39 leaves one byte of slack in the
		// v6-L symbol) and every later frame already rides the connection the pin is meant
		// to protect. A machine that publishes NO pin leaves what is already there, for the
		// same reason MachineEndpointID does below -- overwriting a known pin with nothing
		// would silently downgrade a handset that had one.
		if len(out.Machine.RelaySPKIPin) > 0 {
			st.RelaySPKIPin = out.Machine.RelaySPKIPin
		}
		// The machine's NAME (S19), and the only production source a handset has for it:
		// Config.MachineID is "" on a phone, and the gateway's reconcile record -- the one
		// other authenticated frame carrying it -- cannot arrive before the gateway exists,
		// which PB-LIFE-3 defers until after this pairing. Every mutating verb signs over it
		// and crypto.Command.Canonical refuses an empty one, so a pairing that does not write
		// it here leaves the phone paired and unable to author anything.
		//
		// A machine that publishes NO id leaves what is already there, and that guard is not
		// politeness. Persisting Machine="" is the S9 defect in full (phonecore.OpenStore's
		// own doc carries it): the load-time filter discards a blob stamped with a machine
		// that is not the caller's, so the next process start would throw away the pairing,
		// the epoch, the sealed content key, the relay cursor and the send-seq ceilings --
		// silently, on the first Android process death. Overwriting a known name with nothing
		// is strictly worse than keeping it, and the machine side is where an absent id is
		// caught (internal/skeleton's S19 tests).
		if out.Machine.MachineEndpointID != "" {
			st.Machine = out.Machine.MachineEndpointID
		}
		newEpoch = st.EpochID != out.Machine.EpochID
		if newEpoch {
			st.Keys = crypto.EpochKeys{}
		}
		st.EpochID = out.Machine.EpochID
	})
	if err != nil {
		return
	}
	if newEpoch {
		a.mu.Lock()
		a.reconciled = false
		a.mu.Unlock()
	}
	a.setDestination(out.Machine.MachineRelayAuthPub)
	// PB-STATE-10: a pairing is the owner acting, and it is the one event that can make a
	// terminal "revoked" stale. Without this the recovered handset stays on that screen until
	// the Android process is rebuilt -- the brick reached through the remedy.
	a.rearmAfterPairing()
}

func (p *Pairing) setSAS(s string) {
	p.mu.Lock()
	if p.state == pairPairing {
		p.sas = s
	}
	p.mu.Unlock()
}

// SAS is the six-emoji short authentication string as ONE display string, computed by the
// shared Go core. It errors until the handshake has derived it, and on a dead session.
func (p *Pairing) SAS() (sas string, err error) {
	defer barrier(&err)
	if p == nil {
		return "", errNoReceiver
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state != pairPairing && p.state != pairConfirming {
		msg := "swarmmobile: pairing session is " + p.state + "; there is no SAS to compare"
		if p.err != nil {
			return "", classed(ErrClassPairingFailed, errors.New(msg+": "+p.err.Error()))
		}
		return "", classed(ErrClassPairingFailed, errors.New(msg))
	}
	if p.sas == "" {
		return "", classed(ErrClassPairingFailed,
			errors.New("swarmmobile: the handshake has not derived a SAS yet"))
	}
	return p.sas, nil
}

// State is the pairing state machine as a user-legible string. The values are the pair*
// constants at the top of this file, and every terminal one is its own (PB-PAIR-5).
func (p *Pairing) State() (state string, err error) {
	defer barrier(&err)
	if p == nil {
		return "", errNoReceiver
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state == "" {
		return "", errNoReceiver
	}
	return p.state, nil
}

// Confirm records that the operator compared the two SAS displays and they matched. It
// releases the handshake; it does not block on the machine's decision.
func (p *Pairing) Confirm() (err error) {
	defer barrier(&err)
	if p == nil {
		return errNoReceiver
	}
	p.mu.Lock()
	if p.state != pairPairing {
		state := p.state
		p.mu.Unlock()
		if state == "" {
			return errNoReceiver
		}
		return classed(ErrClassPairingFailed,
			errors.New("swarmmobile: cannot confirm a pairing that is "+state))
	}
	if p.sas == "" {
		p.mu.Unlock()
		return classed(ErrClassPairingFailed,
			errors.New("swarmmobile: cannot confirm before a SAS has been derived"))
	}
	p.state = pairConfirming
	p.mu.Unlock()
	p.persist(pairConfirming)
	p.once.Do(func() { close(p.confirmed) })
	return nil
}

// RejectSAS is the user's "these do not match" (PB-PAIR-5).
//
// IT IS NOT Cancel, and the difference is the point. A mismatch is a suspected
// man-in-the-middle and the ONLY signal this protocol has for one; Cancel is "I changed my
// mind". Recording the two identically discards the single most security-relevant thing this
// flow can learn, and it invites the user to simply try again -- against the same attacker.
// Before this verb existed the only button on a mismatch screen was Cancel.
//
// It takes nothing, like Confirm. PB-SAS-3's whole content is that the SAS is COMPARED on two
// screens by the person holding them and never typed: a verb that ingested one would move the
// comparison from the human who can see both to the phone, which sees one string and whatever
// the attacker relayed.
//
// The handshake is torn down, so nothing is pinned: a mismatch means the peer is not the
// machine.
func (p *Pairing) RejectSAS() (err error) {
	defer barrier(&err)
	if p == nil {
		return errNoReceiver
	}
	p.mu.Lock()
	state := p.state
	if state == "" {
		p.mu.Unlock()
		return errNoReceiver
	}
	if state != pairPairing && state != pairConfirming {
		p.mu.Unlock()
		return classed(ErrClassPairingFailed, errors.New(
			"swarmmobile: cannot reject the SAS of a pairing that is "+state))
	}
	p.state, p.sas = pairSASMismatch, ""
	p.mu.Unlock()
	p.persist(pairSASMismatch)
	p.cancelHandshake()
	return nil
}

// Cancel abandons the pairing. It is a TERMINAL state, not a hang: the rendezvous is
// dropped, the handshake fails closed, and nothing is pinned.
//
// It means "I changed my mind" and nothing more. A user who believes the two SAS displays
// disagree presses RejectSAS instead; see there for why the two are not one verb.
func (p *Pairing) Cancel() (err error) {
	defer barrier(&err)
	if p == nil {
		return errNoReceiver
	}
	p.mu.Lock()
	if p.state == "" {
		p.mu.Unlock()
		return errNoReceiver
	}
	p.state, p.sas = pairCancelled, ""
	p.mu.Unlock()
	p.persist(pairCancelled)
	p.cancelHandshake()
	return nil
}

// rendezvous adapts a raw relay connection to the pairing package's transport seam. The
// relay only ever forwards opaque bytes; it never sees the pairing secret or any
// handshake plaintext.
type rendezvous struct {
	conn  *relay.Conn
	label string
}

func (r *rendezvous) Create(ctx context.Context, id string) error {
	return r.conn.RendezvousCreate(ctx, id)
}
func (r *rendezvous) Claim(ctx context.Context, id string) error {
	return r.conn.RendezvousClaim(ctx, id)
}
func (r *rendezvous) Send(ctx context.Context, msg []byte) error {
	return r.conn.RendezvousSend(ctx, r.label, msg)
}
func (r *rendezvous) Recv(ctx context.Context) ([]byte, error) { return r.conn.RendezvousRecv(ctx) }
func (r *rendezvous) Complete(ctx context.Context, id string) error {
	return r.conn.RendezvousComplete(ctx, id)
}

var _ pairing.RendezvousTransport = (*rendezvous)(nil)
