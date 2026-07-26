package swarmmobile

// The transport plane: one authenticated relay connection per Start..Stop generation,
// draining the machine -> phone mailbox into the core's durable receive transaction and
// appending the phone -> machine one.
//
// Nothing here decides anything about a frame. phonecore.MailboxRouter.AcceptCommit owns
// the per-(sender,epoch) replay guard, the one-Save receive transaction and the ack
// ordering; this loop only supplies bytes and a cursor, and turns what the core accepted
// into events for the app.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"os"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// reconnectDelay is the fixed backoff between relay dial attempts. A phone that
// reconnects hard would exhaust the relay's per-target quota, which is shared with the
// journal it is trying to receive.
const reconnectDelay = 250 * time.Millisecond

// relayAcker releases consumed relay mailbox items. It is injected into the core, which
// must not import the relay client (PB-BIND-0 constrains its closure).
//
// Acks are COALESCED to one per drained page rather than one per frame. The relay ack is
// monotonic and idempotent -- acking cursor N releases everything up to it -- and an ack
// that a process death loses is harmless: the relay redelivers, the phone's DURABLE
// receive high-water refuses the redelivery with crypto.ErrStaleSeq, and the frame is
// acked then. Per-frame acking cost a full websocket round trip and a server-side commit
// on every journal event, which made the phone drain several times slower than the
// machine could publish.
type relayAcker struct{ app *App }

func (r *relayAcker) Ack(cursor uint64) error {
	a := r.app
	a.mu.Lock()
	if cursor > a.ackPending {
		a.ackPending = cursor
	}
	a.mu.Unlock()
	return nil
}

// flushAcks releases everything the core has committed since the last flush.
func (a *App) flushAcks(ctx context.Context, cl *relay.Client) {
	a.mu.Lock()
	cursor := a.ackPending
	sent := a.ackSent
	a.mu.Unlock()
	if cursor <= sent {
		return
	}
	if err := cl.MailboxAck(ctx, cursor); err != nil {
		return
	}
	a.mu.Lock()
	if cursor > a.ackSent {
		a.ackSent = cursor
	}
	a.mu.Unlock()
}

// conn returns the live relay client, or why there is none.
func (a *App) conn() (*relay.Client, error) {
	if a == nil {
		return nil, errNoReceiver
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, errClosed
	}
	if a.sess == nil {
		return nil, errNotRunning
	}
	if a.client == nil {
		return nil, classed(ErrClassOffline, errors.New("swarmmobile: relay connection not established yet"))
	}
	return a.client, nil
}

// awaitConn waits briefly for the connection Start is bringing up, so a screen that
// issues a command immediately after Start is not refused by a race it cannot see. A
// stopped or closed App fails immediately -- there is nothing to wait for.
func (a *App) awaitConn() (*relay.Client, error) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		cl, err := a.conn()
		if err == nil {
			return cl, nil
		}
		if errors.Is(err, errNotRunning) || errors.Is(err, errClosed) || errors.Is(err, errNoReceiver) {
			return nil, err
		}
		if !time.Now().Before(deadline) {
			return nil, err
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The connection states App.ConnectionState reports, named once so the transport loop and
// the Android side cannot disagree about a literal.
//
// The last two are PB-KEY-6's: a custody refusal is not a transport condition and must not be
// reported as one. connReauthRequired means "prompt for the biometric and it will connect";
// connRepairRequired means "the key is gone" and is TERMINAL -- the loop stops, because
// retrying a destroyed key forever while showing a spinner is the failure this pair exists to
// remove.
const (
	connOffline        = "offline"
	connConnecting     = "connecting"
	connOnline         = "online"
	connReconnecting   = "reconnecting"
	connReauthRequired = "reauth_required"
	connRepairRequired = "repair_required"

	// connRevoked is PB-APP-10's seventh state and it is NOT a custody condition.
	//
	// relay.ErrRevoked comes back from the RELAY HANDSHAKE, so it matches neither crypto
	// sentinel and used to fall through the dial switch's bare `continue`: the phone redialled
	// every reconnectDelay for the life of the process behind a "reconnecting" spinner, which
	// is the failure LOOP the requirement forbids in as many words -- reached by the owner
	// doing exactly what the product tells them to do when a handset is lost.
	//
	// It is TERMINAL for the same reason connRepairRequired is: nothing on this device can
	// un-revoke itself, so every retry is a websocket handshake spent re-proving that, on a
	// battery, against the relay's per-source budget. The ONE exception is a pairing that has
	// just completed, which is the owner acting -- see rearmAfterPairing.
	//
	// It is kept apart from connRepairRequired although the two share a remedy, because they
	// do not share a cause: repair_required means this handset's Keystore key is gone, revoked
	// means the OWNER removed it -- and the machine-side registration is what the owner has to
	// clear before a re-pair can succeed.
	connRevoked = "revoked"

	// connRelayUntrusted and connRelayInsecure are the TRANSPORT POLICY's two verdicts, and
	// they are here for the fourth time this switch has had to learn the same lesson.
	//
	// relay.ErrPinMismatch, ErrPinRequired, ErrPinMalformed and ErrCleartextRefused matched
	// none of the sentinels above, so they fell through the bare `continue` and the phone
	// redialled every reconnectDelay behind "Lost the link to your machine; reconnecting."
	// Not one of them is a link that can come back: the relay is presenting a key this phone
	// did not pin, or none was ever pinned, or the machine named a cleartext relay. Waiting
	// resolves none of it, and ConnectionUi.kt states the rule that breaks -- "a spinner is a
	// promise that waiting is enough".
	//
	// They are TWO states and not one because the remedies differ. connRelayUntrusted is the
	// phone's problem to fix by pairing again, which is the one channel that can deliver a
	// current pin. connRelayInsecure is the MACHINE's configuration -- relay.json names a
	// ws:// relay -- and pairing again changes nothing until the owner fixes it, so telling
	// this user to re-pair first would send them round a loop.
	//
	// Both survive a retry inside the post-pairing window, exactly as connRevoked does: a
	// pairing that has just completed may have delivered the very pin that makes this answer
	// stale (rearmAfterPairing).
	connRelayUntrusted = "relay_untrusted"
	connRelayInsecure  = "relay_insecure"
)

func (a *App) setConn(state string) {
	a.mu.Lock()
	changed := a.connState != state
	a.connState = state
	a.mu.Unlock()
	if changed {
		a.events.emit(&Event{Kind: "connection", State: state})
	}
}

// currentConn reads the state without the ready()/barrier wrapping ConnectionState carries:
// it is consulted from inside the transport loop, which is not an entry point.
func (a *App) currentConn() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.connState
}

func (a *App) setClient(cl *relay.Client) {
	a.mu.Lock()
	a.client = cl
	a.mu.Unlock()
}

// run is one Start..Stop generation: dial, drain, reconnect until the context is done.
func (a *App) run(ctx context.Context) {
	first := true
	for ctx.Err() == nil {
		if first {
			a.setConn(connConnecting)
			first = false
		} else {
			// A custody refusal is NOT a transport problem, so it must not be overwritten
			// by "reconnecting": the user has to be told that authenticating is what fixes
			// this, and a spinner tells them the opposite. The state therefore persists
			// across the retry, and the next successful dial clears it by setting "online".
			//
			// connRevoked is held for the same reason, and it only ever survives a retry
			// inside the post-pairing window rearmAfterPairing opens: hiding it behind a
			// spinner there would put back exactly the loop PB-APP-10 forbids.
			// The transport-policy verdicts join this list for the reason the two above are
			// on it: they survive a retry only while a pairing is in flight (B58), and
			// overwriting them with "reconnecting" there would put the spinner back over the
			// one screen that says what is actually wrong.
			if s := a.currentConn(); s != connReauthRequired && s != connRevoked &&
				s != connRelayUntrusted && s != connRelayInsecure {
				a.setConn(connReconnecting)
			}
			select {
			case <-ctx.Done():
			case <-time.After(reconnectDelay):
			}
			if ctx.Err() != nil {
				break
			}
		}
		cl, err := a.dial(ctx)
		if err != nil {
			// PB-KEY-6, at the one production call site of relay.ClientAuth.Sign that can
			// refuse. This error used to be discarded with a bare `continue`, which was
			// unreachable while the app ran on the software keystore and went LIVE the
			// moment PB-KEY-9's Keystore-backed KEK landed: a recoverable refusal became an
			// endless "reconnecting" with no prompt, and a permanent one the same loop
			// against a key that no longer exists.
			switch {
			case errors.Is(err, crypto.ErrKeyInvalidated):
				// PERMANENT and therefore TERMINAL. The relay-auth key is destroyed;
				// nothing on-device recovers it and every retry is a round trip spent
				// proving that again. Returning here rather than breaking is deliberate --
				// break would fall through to setConn("offline") and erase the one state
				// that tells the user to pair again.
				a.setConn(connRepairRequired)
				a.setClient(nil)
				return
			case errors.Is(err, crypto.ErrKeyAuthRequired):
				// RECOVERABLE. Keep retrying -- the biometric may be satisfied at any
				// moment, and the retry is what notices -- but say what is actually wrong.
				a.setConn(connReauthRequired)
			case errors.Is(err, relay.ErrRevoked):
				// PB-APP-10. The THIRD identity this switch has to distinguish, and the one
				// the fix for the first two left behind with an identical shape: a bare
				// `continue` here is an unbounded reconnect the user is shown as a spinner.
				// Returning rather than breaking, for the same reason as the arm above --
				// break falls through to setConn("offline") and erases the one state that
				// tells the user what happened.
				a.setConn(connRevoked)
				// PB-STATE-10: unless a pairing has just made this answer STALE. See
				// rearmAfterPairing -- the state stays "revoked" either way, so nothing is
				// hidden; only the retry survives, and only inside a bounded window.
				if a.withinPairingGrace() {
					continue
				}
				a.setClient(nil)
				return
			case errors.Is(err, relay.ErrPinMismatch),
				errors.Is(err, relay.ErrPinRequired),
				errors.Is(err, relay.ErrPinMalformed):
				// The relay is not the one this phone pinned at pairing, or nothing was
				// pinned and the platform has no trust roots to fall back to. Both are
				// answered by pairing again, which is the only channel that carries a pin.
				a.setConn(connRelayUntrusted)
				// ADR-007 B58: NOT TERMINAL while a pairing is running. The remedy for this
				// verdict IS a pairing, so ending the loop during one destroys the recovery the
				// user is in the middle of performing -- and on a FIRST pairing this is the
				// ordinary path, because a handset that holds no pin yet is refused on every
				// retry. The STATE still stands, so nothing is hidden; only the retry survives.
				if a.pairingInFlight() || a.withinPairingGrace() {
					continue
				}
				a.setClient(nil)
				return
			case errors.Is(err, relay.ErrCleartextRefused):
				// The MACHINE named a cleartext relay. Nothing on the handset can fix it and
				// re-pairing carries the same URL, so this says what is actually wrong.
				a.setConn(connRelayInsecure)
				// B58, same reason: a pairing in flight may be about to publish a relay URL
				// this phone will accept.
				if a.pairingInFlight() || a.withinPairingGrace() {
					continue
				}
				a.setClient(nil)
				return
			}
			continue
		}
		a.setClient(cl)
		a.setConn(connOnline)
		a.onConnected(ctx, cl)
		a.drain(ctx, cl)
		a.setClient(nil)
		// PB-INPUT-2's FIRST enumerated severance event. A gateway restart kills the lease
		// while being unable to seal any notice about it -- the gateway is the thing that
		// died -- so the phone's own transport dropping is the ONLY signal that can exist,
		// and a disconnect must therefore SEVER rather than merely pause. Without this the
		// phone keeps reporting the pre-outage generation live and types against a lease the
		// new gateway does not hold. It also empties the coalescer, so bytes buffered when
		// the link went away resolve as undelivered instead of riding the reconnect.
		a.suspendInput("the connection to the machine was lost")
		_ = cl.Close()
	}
	a.setClient(nil)
	a.setConn(connOffline)
}

// handsetSecurity is the transport policy EVERY dial this handset makes runs under
// (PB-NET-2, ADR-007 B34/B37). It is the default policy -- TLS verified against the
// platform's stated trust roots, cleartext refused, the decision re-asked on every
// redirect hop -- plus the loopback carve-out, which is honoured only inside a test
// binary and is therefore inert in the shipped .so.
//
// SO A RELEASE HANDSET REFUSES CLEARTEXT OUTRIGHT, which is the point: auth_init carries
// the phone's full relay-auth public key, and a passive observer who reads it can revoke
// a never-paired identity through B27's first-use clause. The refusal is decided from the
// URL before a socket is opened, so a QR naming ws:// costs the handset nothing -- not
// even the connection that would tell an attacker's relay that this phone scanned it.
//
// It deliberately does NOT use relay.MachineSecurity: on a handset "loopback" is the
// handset, so a ws://127.0.0.1 relay is never a legitimate destination, and a QR that
// named one would be pointing the phone at something already running on it.
//
// THE PIN COMES FROM PAIRING AND IS PERSISTED (ADR-007 B33/B34). State.RelaySPKIPin is
// written by pin() from pairing.MachinePayload -- the one authenticated channel that can
// carry it, since the QR has no room for 43 base64 characters and every later frame
// already rides the connection the pin exists to protect. Reading it here is what makes it
// load-bearing: a pin that is carried, persisted and never consulted is exactly the fence
// guarding an untaken path that B34 records.
//
// WHAT A USER SEES, in the three states a handset can be in:
//
//   - PAIRED, PIN HELD. The pin replaces name and chain verification, so the operator's
//     self-signed relay is reachable and an impostor holding any other key is refused with
//     relay.ErrPinMismatch however well-issued its certificate is.
//   - PAIRED BEFORE THE MACHINE PUBLISHED A PIN, or paired with a machine that publishes
//     none. No pin is applied. On a pinning-only platform -- which is every Android
//     handset (relay.TrustRootSourceFor) -- the wss:// dial is refused with
//     relay.ErrPinRequired rather than falling back to an unverified connection. That
//     refusal IS residual 1.9's resolution: fail closed, do not dial unpinned.
//   - NEVER PAIRED. There is no relay URL to dial until a QR supplies one, and the pairing
//     dial itself cannot be pinned -- see below.
//
// THE PAIRING DIAL IS NOT COVERED BY THIS PIN AND CANNOT BE. It is the dial that FETCHES
// the pin, so nothing on the handset can know it beforehand; mobile/pairing.go's join()
// runs under the same policy with State.RelaySPKIPin still empty. What protects that
// exchange is not TLS: the payload is a Noise handshake the operator confirms by comparing
// a SAS, so a hostile terminator sees routing metadata and no pinned material.
//
// AND THAT LEAVES A BOOTSTRAP THIS WIRING DOES NOT CLOSE, stated here rather than
// discovered later. On a pinning-only platform an unpinned wss:// dial is not merely
// unverified, it is REFUSED (Security.tlsConfig returns ErrPinRequired before any packet).
// The pairing dial is unpinned by construction, so on an Android handset it is refused, so
// the pin never arrives -- the phone cannot pair over wss:// at all, and the pin it would
// have learned is unreachable. Residual 1.9 is therefore NOT resolved by consulting the
// pin here; closing it needs a decision about what policy the PAIRING dial runs under,
// which is a security decision and not a call-site change. Recorded, not papered over.
func (a *App) handsetSecurity() relay.Security {
	sec := relay.Security{AllowLoopbackCleartext: true}
	if pin := a.core.State().RelaySPKIPin; len(pin) > 0 {
		sec.PinnedSPKISHA256 = pin
	}
	if src := os.Getenv(envTestTrustRoots); src != "" {
		sec = relay.WithTrustRootSource(sec, relay.TrustRootSource(src))
	}
	return sec
}

// envTestTrustRoots names the handset's platform for a test that has to reach the
// pinning-only branch. It is FORWARDED, not interpreted: relay.WithTrustRootSource honours it
// only inside a test binary and its field is unexported, so a release build ignores whatever
// this variable says -- and that inertness is proven where the rule lives, by a non-test
// binary, in internal/remote/transport's TestPBNET2_TheTrustRootOverrideIsInertInAReleaseBuild.
//
// Forwarding rather than re-deciding is the whole design. A second copy of "only in tests" here
// would be a second thing to get wrong and a second thing to prove, and this phase has spent
// itself on rules that existed in two places and disagreed.
//
// WHY IT IS NEEDED AT ALL. The case that bites a handset is a phone with NO pin yet: on a
// pinning-only platform that dial is refused with ErrPinRequired, which is the ordinary first
// pairing. On the desktop the suite runs on, the same dial verifies against the system roots
// and fails with a generic x509 error that never reaches the verdict -- so without this the
// ordinary path can only be fenced by proxy, through ErrPinMismatch, which is a different
// error reached a different way (ADR-007 B58).
const envTestTrustRoots = "SWARM_TEST_TRUST_ROOTS"

// errRelayPinUnmatched is the phone's refusal when the certificate its UNVERIFIED pairing
// dial accepted is not the one the machine pinned in msg2 (ADR-007 B48). It reaches the
// user as ErrClassPairingFailed: the attempt ended with nothing pinned, and the remedy is
// to pair again — on a network the owner trusts.
//
// It is unexported because the Android app never names it: PB-BIND-7's golden surface is
// what the app compiles against, and a sentinel the screens only ever see through
// App.ErrorClass does not belong on it.
var errRelayPinUnmatched = classed(ErrClassPairingFailed,
	errors.New("swarm: the relay presented a certificate the machine did not pin; the pairing connection is being intercepted"))

// checkRelayPin is B48's amendment to B45, and it is the whole of it: the pairing dial
// cannot VERIFY the relay -- it is the dial that fetches the pin -- so what it presented
// is compared, once msg2 lands, against the pin the REAL MACHINE authored. A network
// attacker terminating that TLS cannot make the two agree: it cannot reach inside the
// Noise+PSK frame to change the pin, and it cannot present the machine's relay key.
//
// TWO CASES ARE DELIBERATELY NOT REFUSALS, and neither is a hole this can close.
//
// A machine with NO pin configured (machinePin empty) says nothing about its relay, so
// there is nothing to compare and the check passes. That is B34's own contract -- the pin
// is optional -- and a phone cannot invent a claim its machine never made.
//
// A dial that observed NO certificate (presented empty) is a cleartext dial, which reaches
// this only through the loopback carve-out: a release build refuses every other cleartext
// URL from the URL itself, before a socket. There is no path by which an attacker turns a
// wss:// pairing dial into an unobserved one.
//
// AND IT DOES NOT COVER THE QR-HOLDER. Someone who photographed the code is a legitimate
// party to the ceremony and reaches the real relay under its real certificate; this
// comparison passes for them. That case belongs to the SAS gate and to the consent the
// phone now withholds until that gate passes (ADR-007 B52).
func checkRelayPin(machinePin, presented []byte) error {
	if len(machinePin) == 0 || len(presented) == 0 {
		return nil
	}
	if !bytes.Equal(machinePin, presented) {
		return errRelayPinUnmatched
	}
	return nil
}

// dial names the PINNED MACHINE as the peer whose revocation verdict this handset is here
// for, and that is what keeps PB-APP-10's signal alive (ADR-007 B49).
//
// A ban used to refuse the banned routing id's every dial, whoever placed it. That made
// every device_revoke mutual assured destruction — a stolen handset removed the machine
// from the relay for good and no party the owner controlled could undo it — so the ban is
// now scoped to the relationship it ended, and a scoped verdict has to be ASKED FOR. The
// relay cannot supply the missing coordinate itself: after a revoke the machine and the
// handset hold identical relay state, so no rule it can apply tells them apart.
//
// This is the one place that knows which answer matters to this device. An empty
// destination — a handset whose durable state has no machine yet — asks for no verdict and
// is admitted, which is correct: it has no relationship for a revoke to have ended.
func (a *App) dial(ctx context.Context) (*relay.Client, error) {
	ks := a.core.KeyStore()
	target, _ := a.destination()
	return relay.DialSecure(ctx, a.relayURL, relay.ClientAuth{
		RelayAuthPub: ed25519.PublicKey(ks.RelayAuthPublic()),
		Sign:         ks.SignRelayAuth,
		Peer:         target,
	}, a.handsetSecurity())
}

// onConnected re-establishes the per-connection state the relay does not persist: the
// push token (PB-PUSH-9 requires re-registration on every authenticated reconnect).
//
// IT NO LONGER AUTHORIZES THE MACHINE, and the deletion is the point rather than a
// simplification (ADR-007 B38). That call was `authorize_device` naming the machine, and
// the relay now records nothing from such a call without the NAMED party's signed consent
// — which only the machine can produce and the phone has never held. Making the phone
// carry the machine's consent would have been redundant as well as unprovable: a consented
// authorize_device records BOTH directed edges at once, so the machine's own call, at
// pairing (cmd/swarm/remote.go authorizeAtRelay) and on every gateway connect
// (cmd/swarm-remote/deliver.go), already writes the edge this one used to write.
//
// What was silently depending on it: nothing that survives. Its documented job was "the
// machine's authorization to append to this phone's mailbox", which the pairs bucket holds
// durably in bbolt; its side effect was ADR-007 B22's ban-lift, which belongs to the owner's
// machine and is still performed there. What it also did, and could not stop doing, was
// assert an authority relation from one side's say-so — the whole of ADR-007 B25.
//
// THE TOKEN ARM RECONCILES IN BOTH DIRECTIONS, which is what makes an offline DELETION reach
// the relay at all. It used to register when durable state held a token and do nothing when it
// did not -- so a deletion issued while backgrounded (the normal state under ADR-007 B16)
// cleared the phone and left the relay delivering forever, with nothing to retry it because the
// phone had forgotten the token. Durable state is authoritative for what the relay should hold,
// so no token means DELETE, and the deletion is owed by exactly the mechanism that owes a
// registration.
//
// The empty case cannot destroy a good registration, which is the objection worth answering.
// State.PushToken is durable and wake-tier, so it survives process death and a lock purge
// (PB-STATE-9, and fileStore.PurgeKeys carries the wake container byte for byte). The only ways
// to reach a connect with no token held are a phone that has never registered one -- for which
// the relay holds nothing either -- and a phone whose user deleted it, which is the case this
// arm exists for.
func (a *App) onConnected(ctx context.Context, cl *relay.Client) {
	if token := a.core.State().PushToken; token != "" {
		_ = cl.TokenRegister(ctx, token)
	} else {
		_ = cl.TokenDelete(ctx)
	}
}

// drain polls the mailbox and hands every item to the core, in order.
//
// The immediate next read is conditioned on PROGRESS -- the durable cursor moved -- and
// not on the page having been non-empty. The cursor advances only for a frame the core
// OPENED (phonecore commits it inside the receive transaction), so an item that cannot be
// opened is re-served by every subsequent read: one undecodable frame at the mailbox TAIL
// makes every page non-empty forever. Looping on a non-empty page would then spin at full
// speed on a battery-powered device and burn the relay's per-source ops budget until the
// connection dies -- an unbounded-work lever handed to the party the design treats as
// hostile (PB-SYNC-6), and reachable benignly by any frame that arrives before
// InstallContentKey. A real backlog still drains at full speed: it advances the cursor.
func (a *App) drain(ctx context.Context, cl *relay.Client) {
	for ctx.Err() == nil {
		cursor := a.core.State().RelayCursor
		items, err := cl.MailboxRead(ctx, cursor)
		if err != nil {
			return
		}
		for _, it := range items {
			a.accept(it.Envelope, it.Cursor)
		}
		a.flushAcks(ctx, cl)
		if a.core.State().RelayCursor > cursor {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(pollInterval):
		}
	}
}

// accept runs the core's durable receive transaction for one envelope, then -- only for a
// frame the core ACCEPTED -- builds the app-facing read models and events from it.
//
// IT NO LONGER ATTRIBUTES THE GAP, and that is PB-SYNC-1. This used to mark the stream of
// the frame it happened to be holding -- a hole seen while decoding a terminal snapshot
// staled "terminal" -- but journal and terminal share ONE (sender, epoch) seq space and
// crypto.MailboxResult carries a bare Gap bool with no frame kind, so the skipped seq may
// just as well have been the journal record saying a session exited. The conservative
// per-bucket mark and the per-channel clear both live in the core now, inside the same
// durable transaction that moves the watermark (PB-SYNC-3), and StreamState reads them from
// there.
func (a *App) accept(raw []byte, cursor uint64) {
	key := a.core.State().Keys.ContentKey
	if _, err := a.core.Router().AcceptCommit(raw, cursor); err != nil {
		return
	}
	v, ok := viewFrame(key, raw)
	if !ok {
		return
	}
	switch v.Kind {
	case "terminal_snapshot":
		a.events.emit(&Event{Kind: "terminal", Stream: "terminal", SessionID: v.Terminal.Session})
	case "command_reply":
		a.onReply(v.Reply)
	case "reconcile":
		a.adoptReconcile()
	case "journal_reseed":
		// The repair landed and the core has already replaced the session model with it. The
		// facade's own journal PAGE is deliberately not rewritten: it is a log of events, and
		// a reseed is a set, so folding one in would invent entries the machine never
		// journalled. The event tells a screen to re-read the roster.
		a.events.emit(&Event{Kind: "journal", Stream: "journal", State: "resynced"})
	case "":
		a.onJournal(v.Record)
	}
}

// adoptReconcile folds the machine's rollback authorities into every durable coordinate
// they cover and RECORDS THE ADOPTION durably. Without the durable record every Android
// process death would re-arm the fail-closed refusal of mutating ops, clearable only by a
// gateway reconnect the phone cannot trigger -- fail-closed turning into PB-STATE-10's
// brick. A record naming another machine or epoch is refused by the core and must be a
// NO-OP here: an adopted foreign authority is unrewindable.
func (a *App) adoptReconcile() {
	if err := a.core.Reconcile(); err != nil {
		return
	}
	// Under the core lock: the adoption is recorded against the epoch durable state holds
	// when the record lands, never against a snapshot a concurrent grant has moved on from
	// (phonecore.Core.Mutate).
	if err := a.core.Mutate(func(st *phonecore.State) {
		st.ReconciledEpoch = st.EpochID
	}); err != nil {
		return
	}
	a.mu.Lock()
	a.reconciled = true
	a.mu.Unlock()
	a.events.emit(&Event{Kind: "connection", Stream: "reconcile", State: "reconciled"})
}

func (a *App) onJournal(rec schema.JournalRecord) {
	entry := JournalEntry{
		Cursor:    int64(rec.Cursor),
		SessionID: rec.SessionID,
		Type:      rec.Type,
		Group:     string(rec.Group),
	}
	a.mu.Lock()
	a.journal = append(a.journal, entry)
	if len(a.journal) > journalLogSize {
		a.journal = a.journal[len(a.journal)-journalLogSize:]
	}
	if rec.SessionID != "" && rec.Type != "" {
		a.needs[rec.SessionID] = rec.Type
	}
	subscribed := a.subscribed
	a.mu.Unlock()
	if !subscribed {
		return
	}
	a.events.emit(&Event{
		Kind:      "journal",
		Stream:    "journal",
		SessionID: rec.SessionID,
		State:     string(rec.Group),
		Message:   rec.Type,
		Cursor:    entry.Cursor,
	})
}

func (a *App) onReply(ctrl schema.Control) {
	if ctrl.OperationID != "" {
		a.resolve(ctrl.OperationID)
	}
	a.mu.Lock()
	a.killSwitch = ctrl.ErrorCode == schema.CodeKillSwitch
	a.mu.Unlock()
	a.reportSkew()
	a.events.emit(&Event{
		Kind:      "outcome",
		Stream:    "reply",
		SessionID: ctrl.SessionID,
		State:     ctrl.Op,
		Message:   ctrl.OperationID,
	})
}

// reportSkew surfaces PB-TIME-1's verdict, which this reply may just have produced: the
// AAD-covered IssuedAt on a machine reply is the only authenticated machine time the phone
// ever sees, so a bracket can close nowhere else.
//
// It is a REPORT, not a gate. A phone two minutes out signs an ExpiresAt the daemon refuses,
// and the daemon's refusal reads "not authorized" -- which sends the user to re-pair when
// the fix is to correct their clock. Refusing the command locally instead would stop the
// command that re-measures, so the verdict could never clear once it went bad; the daemon
// stays the enforcement and this is the explanation. Only a CHANGE is emitted, or a
// two-minute-slow phone would raise an event per reply for the life of the session.
//
// THE CHANGE IS THE VERDICT, NOT ITS WORDING. Every reply closes a fresh bracket out of two
// wall-clock reads around a network round trip, so one CONSTANT skew measures a slightly
// different offset each time and skew.go renders it at full time.Duration precision.
// Comparing the rendered message therefore sees a change on every single reply -- a dedupe
// that can never dedupe, producing exactly the per-reply spam this guard exists to stop. The
// user's fact is binary (the clock is out of budget, or it is not) and so is the key. The
// verdict is not latched: correcting the clock clears it and a later relapse reports again.
// It also maintains the PULL surface App.ClockVerdict reads, and it EMITS ON BOTH
// TRANSITIONS. The `msg == ""` early return that used to sit here meant nothing was raised
// when the verdict went back to healthy, so a UI that latched the first event went on telling
// a user with a correct clock to fix their clock -- the same latch S11's round-1 fix removed
// from the command path, re-created one layer up. A screen that is already open never calls a
// pull surface, so the clearing event is what reaches it.
func (a *App) reportSkew() {
	msg := ""
	if err := a.core.SkewMonitor().Check(); err != nil {
		msg = err.Error()
	}
	a.mu.Lock()
	changed := a.skewed != (msg != "")
	a.skewed = msg != ""
	a.clockVerdict = msg
	a.mu.Unlock()
	if !changed {
		return
	}
	if msg == "" {
		a.events.emit(&Event{Kind: "clock", Stream: "clock", State: "healthy"})
		return
	}
	a.events.emit(&Event{Kind: "clock", Stream: "clock", State: "skewed", Message: msg})
}
