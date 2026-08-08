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
	"fmt"
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

	// pairRelayUnreachable is the dial dying before a single handshake byte: the phone could
	// not REACH the relay at all (agents-tracker-n4vs). Its own value by PB-PAIR-5's rule --
	// the user's next move differs from every state above: fix the network, not the code.
	// The field event that minted it: a LAN relay dialled from cellular, and a screen that
	// said "ask your machine for a new code" about a code that was never the problem.
	pairRelayUnreachable = "relay_unreachable"

	// pairOriginMismatch is what a confirmation naming a DIFFERENT destination from the one
	// displayed leaves behind. It is terminal and it is not "cancelled": the user said yes to
	// one URL and something offered another, which is a security event.
	pairOriginMismatch = "refused_origin_mismatch"
)

// errLateCancel is what a Cancel or a SAS rejection gets when it arrives while the pairing's
// durable effects are already being written (ADR-007 B58).
//
// IT LOSES, AND IT SAYS SO. Cancel means "stop before it lands"; once pin() has written the
// machine coordinates the pairing is COMPLETE, and publishing `cancelled` over them would leave
// the phone pinned to a machine it believes it cancelled -- PB-PAIR-4's half-paired state,
// reached through the one verb that exists to prevent it. Rolling the write back instead is a
// larger change than a window this size justifies, and it would have to un-pin coordinates the
// machine has already enrolled against.
//
// So the state stays `paired` and this rides alongside it, naming the verb that DOES undo a
// completed pairing. A user who is told nothing would reasonably believe the cancel worked.
var errLateCancel = classed(ErrClassPairingFailed, errors.New(
	"swarmmobile: the pairing completed before this was cancelled and the device is now paired; "+
		"use revoke to undo it"))

// errDifferentMachine is the durable commit REFUSING a machine this phone is not pinned to
// (PB-PAIR-4). It travels as an error rather than as a state read off the outcome because the
// refusal has to reach the wire: the commit runs before the acknowledgement, so returning it
// here is what stops the frame the machine enrols on. finish maps it back to
// pairDifferentMachine, which is the state the user's next move is keyed to.
var errDifferentMachine = classed(ErrClassPairingFailed, errors.New(
	"swarmmobile: this QR belongs to a machine other than the one this phone is paired to; "+
		"nothing was pinned and the machine was not acknowledged"))

// errRelayUnreachable marks a pairing dial that died before a single handshake byte
// crossed the wire (agents-tracker-n4vs). Attached at the dial site -- the only place that
// knows the stage -- and consumed by finish(), which knows the vocabulary.
var errRelayUnreachable = classed(ErrClassPairingFailed, errors.New(
	"swarmmobile: this phone could not reach the relay"))

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

// DeviceName is what this phone calls itself when it enrols with a machine: the DeviceName
// field of the pairing payload, sent once in msg3 and thereafter the label the machine's own
// device registry shows its owner (internal/remote/enroll).
//
// IT IS OWNED HERE, AND RETURNED RATHER THAN MERELY SENT, for the reason WakeNotificationText
// gives one file over: a second copy in Kotlin is a copy that drifts. A screen that wants to
// show the paired device's name must not type this string -- it would be rendering a Go
// constant as though the wire had carried it, which is ADR-007 B135's defect class.
//
// AND THE WIRE DOES NOT CARRY IT BACK. pairing.DeviceOutcome returns the SAS, the machine's
// static key and the machine's payload -- there is no device payload in it. Nothing on this
// side persists the name either: phonecore.State has no field for it and the pairing record
// holds one label from a closed set. So App.PairedDeviceName returns THIS, honestly, and if a
// screen ever needs the name the MACHINE holds -- which an owner can rename -- that is a
// different verb carrying a fact the wire actually delivers.
const DeviceName = "swarm phone"

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
	return a.beginWith(core, payload)
}

// BeginPairingWithCode is BeginPairing for the ten-character spelling (ADR-007 B140): the
// typed code plus the relay URL this phone already knows construct the SAME payload the QR
// would have carried, and everything downstream is the QR path, byte for byte. It JOINS
// NOTHING, exactly like BeginPairing -- the handle comes back in confirm_destination, and the
// destination the confirm sheet renders is this relayURL, so PB-PAIR-6's display-then-confirm
// covers the remembered value the same way it covers a scanned one.
func (a *App) BeginPairingWithCode(code, relayURL string) (p *Pairing, err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return nil, err
	}
	payload, err := payloadFromShortCode(code, relayURL)
	if err != nil {
		return nil, err
	}
	return a.beginWith(core, payload)
}

// payloadFromShortCode derives the ceremony from its typed spelling. The relay URL is the
// one thing the code cannot carry; refusing its absence HERE keeps the failure a routed
// message on the pairing screen rather than a handle holding a ceremony with no address to
// dial (PB-PAIR-7's state, reached from the other side).
//
// THE URL IS TYPED BY A PERSON NOW (agents-tracker-3fkm), which is why it is validated and not
// merely present. Until the first-run prompt existed this string could only have come from a QR
// this app had already decoded; it now arrives from a phone keyboard, copied off a terminal
// across the room, so a typo is an ordinary event rather than an impossible one. Caught here it
// is a sentence on the pairing screen naming the shape an address has. Passed through, it is a
// ceremony whose confirm step asks the user to approve their own typo, followed by a transport
// failure with nothing actionable in it.
func payloadFromShortCode(code, relayURL string) (pairing.QRPayload, error) {
	trimmed := strings.TrimSpace(relayURL)
	if trimmed == "" {
		return pairing.QRPayload{}, classed(ErrClassRelayUnknown,
			errors.New("this phone has no relay yet: scan the QR once, or paste the full code"))
	}
	dest, err := relayAddress(trimmed)
	if err != nil {
		// Already classed by relayAddress, which is where its sentence is written.
		return pairing.QRPayload{}, err
	}
	id, psk, err := pairing.DeriveShortCode(code)
	if err != nil {
		return pairing.QRPayload{}, classed(ErrClassPairingCodeInvalid, err)
	}
	return pairing.QRPayload{RelayURL: dest, RendezvousID: id, PairingSecret: psk}, nil
}

// relayAddress is the typed relay URL as something dialable, or the sentence the typist gets.
//
// `ws://` IS ACCEPTED ALONGSIDE `wss://`. PB-OPS-1's demonstration is a phone reaching a laptop
// over the LAN, where there is no certificate to be had, and PB-PAIR-6 already displays the
// destination and LABELS a private address rather than forbidding it. Refusing the unencrypted
// scheme here would refuse that case one layer below the step built to judge it.
//
// It returns the PARSED form, so a scheme typed in capitals reaches the transport in the one
// spelling the transport reads. Everything else is preserved as written -- the string the confirm
// step shows has to be the address the user can compare against their terminal.
func relayAddress(raw string) (string, error) {
	shape := classed(ErrClassRelayAddressInvalid, errors.New("that is not a relay address: it looks "+
		"like wss://host:port (or ws:// on your own network), and your machine printed the "+
		"whole thing"))
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", shape
	}
	if parsed.Scheme != "ws" && parsed.Scheme != "wss" {
		return "", shape
	}
	if parsed.Host == "" {
		return "", shape
	}
	return parsed.String(), nil
}

// beginWith is the shared tail of the two spellings: the handle in confirm_destination,
// nothing dialled, the locked-handset refusal before the attempt is recorded.
func (a *App) beginWith(core *phonecore.Core, payload pairing.QRPayload) (*Pairing, error) {
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
	if _, err := core.KeyStore().NoiseStatic(); err != nil {
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
	// relay.PairingSecurity, and this is the ONE dial in the product that uses it
	// (ADR-007 B45). The pin cannot be applied here -- this is the dial that FETCHES it --
	// and on a pinning-only platform an unpinned wss:// dial is refused rather than merely
	// unverified, so under any other policy a handset could never pair over wss:// at all.
	// What guards this exchange is the Noise handshake and the SAS the operator compares,
	// not the relay's certificate. Cleartext is refused here exactly as everywhere else.
	//
	// Every dial AFTER this one is pinned (App.handsetSecurity), and that scope is fenced:
	// see mobile/b45_pairingscope_test.go, which fails if this policy becomes reachable
	// from the session dial.
	conn, err := relay.DialRawSecure(ctx, payload.RelayURL, relay.PairingSecurity())
	if err != nil {
		// THE STAGE IS THE CLASSIFICATION (agents-tracker-n4vs): nothing has crossed the
		// wire yet, so whatever the dial's own error says -- refused, no route, a connect
		// timeout on a black-holed LAN address, a listener that is not TLS -- the fact the
		// user can act on is that this phone could not reach the relay. The sentinel is
		// attached HERE because only this site knows no handshake byte was spent; finish()
		// knows the vocabulary, not the stage.
		err = classed(ErrClassPairingFailed, fmt.Errorf("%w: %w", errRelayUnreachable, err))
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
			DeviceName:           DeviceName,
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
		//
		// It is bound to THIS CEREMONY by the rendezvous id the QR carried (ADR-007 B47):
		// a consent whose ceremony the relay has retired -- because the owner revoked this
		// device, or because a later pairing superseded it -- authorizes nothing, so the
		// bytes left behind in the machine's state directory cannot undo a revoke. The id
		// comes from the scanned payload rather than from anything the machine asserted,
		// which is the same rule the grantee follows one line below.
		Consent: func(m pairing.MachinePayload) ([]byte, error) {
			ceremonyID := hex.EncodeToString(payload.RendezvousID[:])
			sig, err := ks.SignRelayAuth(relay.ConsentMessage(ceremonyID, relay.RoutingID(m.MachineRelayAuthPub)))
			if err != nil {
				return nil, err
			}
			return relay.MarshalConsent(ceremonyID, sig), nil
		},
		// ADR-007 B48: the certificate this dial accepted UNVERIFIED, checked against the
		// pin the real machine authored and put inside the authenticated msg2. It runs
		// before the SAS gate below, so an operator is never asked to compare a code for a
		// connection already known to be terminated.
		VerifyMachine: func(m pairing.MachinePayload) error {
			return checkRelayPin(m.RelaySPKIPin, conn.PeerSPKI())
		},
		// PB-PAIR-4's DURABLE COMMIT, and the whole meaning of the acknowledgement this phone
		// is about to send. It runs on the machine's authenticated acceptance and BEFORE that
		// frame leaves, because the machine enrols on the frame: a phone that acknowledged
		// first and wrote afterwards was attesting that the acceptance had ARRIVED, and a full
		// disk, a read-only data directory, a Keystore refusal or an Android SIGKILL in the
		// window that followed left the machine enrolled with remote control live while this
		// phone held no pin at all -- the single-device slot spent, every further pairing
		// refused, and a desktop revoke the only exit.
		//
		// THE DIFFERENT-MACHINE REFUSAL BELONGS HERE FOR THE SAME REASON, and it used to sit
		// in finish() one frame too late. v1 is single-machine, so a QR from a machine other
		// than the pinned one pins NOTHING -- but the acknowledgement had already gone out, so
		// the machine on the other end enrolled a handset that had just refused it. The refusal
		// has to reach the wire, and the only way it reaches the wire is by not acknowledging.
		Commit: func(out *pairing.DeviceOutcome) error {
			if app.differentMachine(out) {
				return errDifferentMachine
			}
			return app.pin(out)
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
	// LANDED IS DECIDED BEFORE ANYTHING ELSE, and it is a fact about the world rather than a
	// choice this function makes. RunDevice commits durably before it acknowledges (PB-PAIR-4),
	// so an outcome with no error is one whose durable effects are ALREADY on disk and whose
	// machine has already been told. Nothing here can un-land it.
	landed := err == nil && out != nil

	p.mu.Lock()
	// A state the USER already settled wins: RejectSAS and Cancel both tear the handshake
	// down, so the error arriving here is the consequence of their answer, not a verdict of
	// its own.
	//
	// UNLESS THE PAIRING LANDED, and PB-PAIR-4 is why: publishing `cancelled` over effects that
	// are already durable, against a machine that is already enrolled, leaves the phone paired
	// to a machine it believes it cancelled -- the half-paired state reached through the one
	// verb that exists to prevent it. So the user's answer LOSES, and it loses VISIBLY:
	// errLateCancel says the pairing completed and names the verb that undoes it. This is
	// ADR-007 B58's ruling unchanged; what moved is where the write happens, so the guard that
	// used to be re-asked after it is now asked once, here.
	if p.state == pairCancelled || p.state == pairSASMismatch || p.state == pairOriginMismatch {
		if landed {
			p.state, p.err = pairPaired, errLateCancel
		}
		p.mu.Unlock()
		p.persistState()
		return
	}
	var next string
	var failErr error
	switch {
	case landed:
		// The durable pin and the machine's enrolment both happened inside RunDevice, so no
		// observer of this label can see a world where the effects have not landed (ADR-007
		// B58) -- and there is no window left between the two in which they could disagree.
		next = pairPaired
	case errors.Is(err, errDifferentMachine):
		// The handshake authenticated a machine that is not the one this phone is pinned to.
		// The commit refused it, so nothing is pinned AND nothing was acknowledged: the pairing
		// the user had is left exactly as it was, and the machine at the other end claims
		// nothing either.
		next = pairDifferentMachine
	case errors.Is(err, pairing.ErrPairingDeclined):
		next = pairDeclined
	case errors.Is(err, pairing.ErrRateLimited):
		next = pairRateLimited
	case errors.Is(err, errRelayUnreachable):
		// BEFORE the ctx cases deliberately: the cellular-against-a-LAN-relay failure is a
		// connect timeout, so the pairing window is often already expired when the dial
		// returns -- and "your machine did not answer in time" would be a second wrong story
		// about a machine that was awake all along. No handshake byte was spent; the network
		// is the story (agents-tracker-n4vs).
		next = pairRelayUnreachable
		failErr = err
	case errors.Is(err, relay.ErrRendezvousExpired), errors.Is(err, relay.ErrRendezvousBurned):
		// The rendezvous is gone: its TTL elapsed, or the QR was already used. Both look
		// identical from here and lead to the same place -- ask the machine for a fresh QR --
		// so they share a state rather than inventing a distinction the phone cannot make.
		next = pairExpired
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		next = pairTimeout
	case ctx.Err() != nil:
		next = pairCancelled
	default:
		// A REFUSED WRITE IS NOT A PAIRING (ADR-007 B60), and it arrives here as
		// pairing.ErrNotCommitted: the machine said yes and this phone could not remember it.
		// The state is pairFailed rather than a value of its own because PB-PAIR-5's rule is
		// that a state earns its own value when the USER'S NEXT MOVE differs, and here it does
		// not: try the pairing again. What differs is the reason, and that rides on the error.
		//
		// It also keeps PB-PAIR-4's recovery record: persist() clears the durable attempt only
		// for pairPaired and pairCancelled, so a failed write leaves the next launch able to
		// explain itself.
		next, failErr = pairFailed, err
	}
	p.state = next
	if failErr != nil {
		p.err = failErr
	}
	p.mu.Unlock()

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

// PairedDeviceName is the name this phone gave itself when it paired -- the string the
// machine's device registry lists it under (bead agents-tracker-xtj).
//
// IT IS THIS SIDE'S CONSTANT, NOT A WIRE FACT, and the distinction is the whole reason the
// verb exists. The name goes out once in the pairing handshake and nothing returns it; a
// screen that needs it would otherwise type the literal itself, which renders a Go constant
// as though it had come from the machine. Returning it here keeps one source. It does not
// claim the machine still calls this device that -- an owner can rename it there, and reading
// THAT would need a verb carrying something the wire delivers.
//
// It is gated on being paired because before there is a pairing there is no paired device to
// name, and a pane about the machine this phone is bound to must not answer for one that does
// not exist.
func (a *App) PairedDeviceName() (name string, err error) {
	defer barrier(&err)
	if _, err = a.ready(); err != nil {
		return "", err
	}
	return DeviceName, nil
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
func (a *App) pin(out *pairing.DeviceOutcome) error {
	var newEpoch bool
	err := a.core.Mutate(func(st *phonecore.State) {
		st.MachineStatic = out.MachineStatic
		st.MachineSignPub = out.Machine.MachineSignPub
		st.MachineRelayAuthPub = out.Machine.MachineRelayAuthPub
		// The relay's SPKI pin (ADR-007 B33/B34), and pairing is the ONLY channel that can
		// carry it: the QR has no room (MaxRelayURLLen = 39 leaves one byte of slack in the
		// v6-L symbol) and every later frame already rides the connection the pin is meant
		// to protect.
		//
		// ADOPTED VERBATIM, INCLUDING ITS ABSENCE (ADR-007 B54). This assignment is
		// deliberately NOT guarded by `if len(...) > 0`, and it is the one coordinate here
		// that differs from MachineEndpointID below.
		//
		// It used to be guarded, on the reasoning that overwriting a known pin with nothing
		// silently downgrades a handset that had one. That is true and it is the wrong trade,
		// because of where the two states lead. `swarm remote init --relay-pin` is optional,
		// so "publishes no pin" is the ordinary case, and a phone that keeps a stale pin
		// fails every dial with ErrPinMismatch, reports relay_untrusted, and is told to pair
		// again -- which re-runs this code, keeps the stale pin, and returns it to the same
		// screen. There is no on-device exit. The downgrade the guard prevented recovers the
		// moment the operator supplies a pin; the loop it created recovers never.
		//
		// And adoption is right on its own terms, not merely the lesser evil: a completed
		// pairing is authenticated by the Noise handshake and confirmed by two operators
		// comparing a SAS. What it carries is the machine's own statement about its own
		// relay, made over the one channel this design trusts for exactly that. An absent pin
		// is part of the statement, not a gap in it.
		st.RelaySPKIPin = out.Machine.RelaySPKIPin
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
		// THE UNPAIR ENDS HERE, and it is the reason a revoke can be recorded durably at all
		// (phonecore.State.Disowned). A flag the revoke sets and nothing clears would replace an
		// unpairable phone with a permanently unpairable one -- the handset would complete this
		// handshake and still be shown the pairing screen, for good.
		//
		// It is CLEARED RATHER THAN MERGED, and the Save behind this Mutate is what makes that
		// safe: a pre-revoke writer arrives with an older purge stamp and has the unpair
		// re-applied, while this state was read after it. So the one act that can clear the flag
		// is the one act that proves it has seen it, which is the owner pairing again.
		st.Disowned = false
		newEpoch = st.EpochID != out.Machine.EpochID
		if newEpoch {
			st.Keys = crypto.EpochKeys{}
		}
		st.EpochID = out.Machine.EpochID
	})
	// THE ERROR IS RETURNED, not swallowed (ADR-007 B60). This used to be a bare `return`
	// on a void function, so finish() published `paired` without being able to know whether
	// anything had been written -- a refused Keystore unwrap, a full disk or a read-only data
	// directory all produced a phone that said it was paired and held none of the machine
	// coordinates a pairing exists to pin.
	if err != nil {
		return err
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
	return nil
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
