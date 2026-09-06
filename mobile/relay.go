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
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/url"
	"os"
	"sync/atomic"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/pairing"
	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remote/relayv2"
)

// The reconnect backoff between relay dial attempts, PB-NET-4 / ADR-007 section 6.0's
// numeric budget: initial delay, growth factor, ceiling and jitter fraction. A phone that
// reconnects hard would exhaust the relay's per-target quota, which is shared with the
// journal it is trying to receive -- the exponential growth is what keeps a stuck relay
// from being redialled at a fixed high rate for the life of the process, and the jitter
// keeps a fleet reconnecting after a relay restart from arriving as one herd. These are the
// committee's approved numbers, not tuning knobs: a change here is a change to the budget.
//
// THE NUMBERS THEMSELVES NOW LIVE IN internal/remote/relay AND THESE ARE DELEGATIONS. D9
// binds the schedule to BOTH hops, and while it lived here as four unexported constants it
// was in practice the PHONE's schedule: the gateway sidecar had no reconnect at all, and
// when it got one there was nowhere to take these values from but a second copy. A budget
// that exists twice is two budgets. The constants are GONE from this file rather than
// aliased, so there is no second name anyone can edit and believe they changed the budget;
// relay.ReconnectInitialDelay, ReconnectFactor, ReconnectCeiling and ReconnectJitter are
// the whole of it, and pbnet4_backoff_test.go still asserts section 6.0's transcription
// against literals through these two functions.

// reconnectBackoffBase returns the un-jittered delay before dial attempt n, 1-based:
// relay.ReconnectInitialDelay doubling on every failed attempt, never exceeding
// relay.ReconnectCeiling.
func reconnectBackoffBase(attempt int) time.Duration {
	return relay.ReconnectBackoffBase(attempt)
}

// reconnectJittered spreads base by +/-relay.ReconnectJitter. frac is a value in [-1, 1] --
// taken as a parameter, rather than drawn here, so the spread itself is testable without a
// random source.
func reconnectJittered(base time.Duration, frac float64) time.Duration {
	return relay.ReconnectJittered(base, frac)
}

// reconnectBackoff tracks consecutive failed dial attempts across one App.run generation and
// computes each retry delay. It resets to the initial delay on every successful connection
// (App.run calls reset after setConn(connOnline)), so a link that has been stable for a while
// never carries a stale, grown-out backoff into its next outage.
//
// THE HANDSET RESETS ON CONNECTION AND THE GATEWAY DOES NOT, and that difference is
// deliberate rather than drift. This side has a user watching a connection state, and a
// phone coming back from a tunnel must not carry a grown-out delay into its next blip. The
// sidecar has no such observer and faces the relay as the declared adversary, so it resets
// only on evidence that traffic crossed the link (remotegw.Service.Progressed).
type reconnectBackoff struct {
	attempt int
	frac    func() float64 // returns a value in [-1, 1]; overridden by tests
}

func newReconnectBackoff() *reconnectBackoff {
	return &reconnectBackoff{frac: func() float64 { return rand.Float64()*2 - 1 }}
}

// next returns the delay before the next dial attempt and advances the backoff state.
func (b *reconnectBackoff) next() time.Duration {
	b.attempt++
	return reconnectJittered(reconnectBackoffBase(b.attempt), b.frac())
}

// reset returns the backoff to its initial state, as if no attempt had yet failed.
func (b *reconnectBackoff) reset() {
	b.attempt = 0
}

// reconnectDelayObserver receives every delay App.run SCHEDULES between dial attempts,
// with the 1-based attempt number. It is nil in production and installed only by a test
// in this package; it is an atomic pointer rather than a plain var so an App generation
// still winding down from an earlier test cannot race the installation.
//
// WHY IT EXISTS. PB-NET-4's schedule can be measured exactly here and nowhere else. The
// out-of-process fence (mobile/conformance/pbnet4_flappingrelay_test.go) can only time
// dial ARRIVALS at a relay, which is this delay plus the host's own scheduling and
// transport latency -- so it can prove the schedule GROWS but can never hold it to
// section 6.0's +/-20% band without asserting a quantity the code does not control.
var reconnectDelayObserver atomic.Pointer[func(attempt int, d time.Duration)]

// relayAcker releases consumed relay mailbox items. It is injected into the core, which
// must not import the relay client (PB-BIND-0 constrains its closure).
//
// AcceptPhoneDelivery invokes this only after its durable receive transaction commits.
// The subscription ACK is monotonic and idempotent; a crash before it completes merely
// redelivers a frame the durable receive high-water already knows.
type relayAcker struct{ app *App }

// phoneStream is one relay-v2 stream connection and its sole subscription. It is
// intentionally also the existing publication seam: callers keep appending opaque
// envelopes while this adapter supplies the generation fence and deterministic id.
type phoneStream struct {
	conn        *relayv2.Conn
	sub         *relayv2.Subscription
	binding     relayv2.Binding
	coreBinding phonecore.PhoneBinding
}

// relayV2ProbeConnection is the entire authority a WebPKI migration probe
// needs after relay-v2 authentication succeeds.
type relayV2ProbeConnection interface{ Close() }

var relayV2DialProbe = func(ctx context.Context, profile relayv2.Profile, auth relayv2.Auth) (relayV2ProbeConnection, error) {
	return relayv2.Dial(ctx, profile, auth)
}

func relayMessageID(ciphertext []byte) string {
	digest := sha256.Sum256(ciphertext)
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func (s *phoneStream) MailboxAppend(ctx context.Context, target string, ciphertext []byte) (uint64, error) {
	if s == nil || s.conn == nil || target != s.binding.MachineRID {
		return 0, classed(ErrClassOffline, errors.New("swarmmobile: relay-v2 append target does not match the active binding"))
	}
	result, err := s.conn.Append(ctx, s.binding, relayMessageID(ciphertext), ciphertext)
	return result.Cursor, err
}

func (s *phoneStream) Close() {
	if s != nil && s.conn != nil {
		s.conn.Close()
	}
}

type mailboxDiscardRequest struct {
	ctx     context.Context
	done    chan mailboxDiscardResult
	claimed bool // guarded by App.mu; exactly one drain generation may execute it
}

type mailboxDiscardResult struct {
	recoveryToken string
	err           error
}

const mailboxDiscardRequestTimeout = 15 * time.Second

func (r *relayAcker) Ack(cursor uint64) error {
	a := r.app
	a.mu.Lock()
	stream := a.ackStream
	a.mu.Unlock()
	if stream == nil || stream.sub == nil {
		return errPublicationNoConnection
	}
	return stream.sub.Ack(context.Background(), cursor)
}

// requestMailboxDiscard hands an explicit roster refresh to the single mailbox reader and
// waits for its bounded diagnosis/recovery result. Executing on the drain is the concurrency
// boundary: a facade-side InboundAgeRefused check can race the delivery currently being
// accepted and publish the replacement behind the stale backlog. A healthy diagnosis returns
// an empty token and deletes nothing; only authenticated stale age (or a durable pending token)
// crosses into the destructive, incarnation-fenced self-mailbox discard.
func (a *App) requestMailboxDiscard() (string, error) {
	// RefreshRoster is an idempotent command, so it inherits the command plane's brief
	// post-Start wait. Start publishes sess before run publishes stream; failing immediately
	// in that ordinary window regresses the roster-only refresh that existed before guarded
	// stale-head diagnosis. This only waits for the connection -- the facade still performs
	// no mailbox read, and the request below is still claimed by the drain's single reader.
	if _, err := a.awaitStream(); err != nil {
		return "", err
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return "", errClosed
	}
	sess := a.sess
	if sess == nil || a.stream == nil {
		a.mu.Unlock()
		return "", classed(ErrClassOffline, errors.New("swarmmobile: relay connection not established for mailbox recovery"))
	}
	if a.mailboxDiscard != nil {
		a.mu.Unlock()
		return "", classed(ErrClassRateLimited, errors.New("swarmmobile: a mailbox recovery is already in progress"))
	}
	ctx, cancel := context.WithTimeout(sess.ctx, mailboxDiscardRequestTimeout)
	defer cancel()
	req := &mailboxDiscardRequest{ctx: ctx, done: make(chan mailboxDiscardResult, 1)}
	a.mailboxDiscard = req
	wake := a.recvCancel
	a.mu.Unlock()
	// Recv only waits on the subscription's delivery channel; canceling its caller
	// context wakes the single reader without closing the relay-v2 connection. That
	// keeps the exact bound connection alive for the PROBE/DISCARD transaction.
	if wake != nil {
		wake()
	}

	select {
	case result := <-req.done:
		return result.recoveryToken, result.err
	case <-ctx.Done():
		a.mu.Lock()
		if a.mailboxDiscard == req && !req.claimed {
			a.mailboxDiscard = nil
		}
		a.mu.Unlock()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", classed(ErrClassOffline, fmt.Errorf("swarmmobile: mailbox recovery timed out: %w", relay.ErrTimeout))
		}
		return "", classed(ErrClassOffline, fmt.Errorf("swarmmobile: mailbox recovery canceled with its app session: %w", ctx.Err()))
	}
}

// performMailboxDiscard executes at the top of a drain iteration, when that goroutine owns no
// in-flight Recv. PROBE is the healthy read barrier; DISCARD is issued only from a durable,
// exact-incarnation recovery phase.
func (a *App) performMailboxDiscard(stream *phoneStream) (*phoneStream, bool) {
	a.mu.Lock()
	req := a.mailboxDiscard
	if req == nil || req.claimed {
		a.mu.Unlock()
		return stream, false
	}
	req.claimed = true
	a.mu.Unlock()

	finish := func(recoveryToken string, err error) {
		a.mu.Lock()
		if a.mailboxDiscard == req {
			a.mailboxDiscard = nil
		}
		a.mu.Unlock()
		req.done <- mailboxDiscardResult{recoveryToken: recoveryToken, err: err}
	}
	if err := req.ctx.Err(); err != nil {
		finish("", classed(ErrClassOffline, err))
		return stream, true
	}
	// The stale verdict may not exist YET: RefreshRoster can cross a page already in flight
	// before MailboxRouter opens its authenticated head. Diagnose one immediate page here,
	// while the single reader owns the connection, so one press cannot publish its replacement
	// behind an existing stale backlog. Stop at the first unique, unacked stale-age refusal:
	// later items must not advance the durable cursor past it.
	pendingToken, recoveryIncarnation, recoveryCursor := a.core.DiscardRecovery()
	staleAge := false
	if pendingToken == "" {
		items, err := stream.sub.Probe(req.ctx)
		if err != nil {
			finish("", err)
			return stream, true
		}
		recoveryCursor, err = diagnosePhoneDeliveries(req.ctx, items, func(ctx context.Context, raw []byte, cursor uint64) (phonecore.Receipt, error) {
			return a.acceptPhone(ctx, stream, raw, cursor)
		})
		if err != nil {
			finish("", err)
			return stream, true
		}
		staleAge = recoveryCursor != 0
	}
	// Healthy frames may have repaired the transport while RefreshRoster woke the reader.
	// Compacting them would no longer be the narrowly authorized recovery. Before the caller
	// publishes its ordinary roster refresh, synchronously ack the safe diagnostic high-water:
	// otherwise a mailbox at its depth cap refuses the daemon's replacement.
	if !staleAge && pendingToken == "" {
		finish("", nil)
		return stream, true
	}
	// The intent must reach durable state BEFORE the destructive RPC. If the process dies
	// after this Save, the next explicit RefreshRoster sees the same token and reissues the
	// incarnation-fenced idempotent discard even though the in-memory age refusal is gone.
	recoveryToken, err := a.core.BeginRelayDiscardRecovery(recoveryCursor)
	if err != nil {
		finish("", err)
		return stream, true
	}
	if pendingToken == "" {
		_, recoveryIncarnation, recoveryCursor = a.core.DiscardRecovery()
	}
	// The durable original incarnation is also the recovery phase. If the current
	// checkpoint still equals it, DISCARD and adoption are owed. Once adoption moved
	// the current checkpoint, only the token-bearing roster request remains owed;
	// deleting the new mailbox again could lose deliveries created after recovery.
	if a.core.State().RelayIncarnation == recoveryIncarnation {
		result, err := stream.conn.Discard(req.ctx, stream.binding, recoveryIncarnation, recoveryCursor)
		if err != nil {
			finish("", err)
			return stream, true
		}
		if err := a.core.AdoptPhoneDiscard(stream.coreBinding, recoveryIncarnation, result.Incarnation, result.Cursor); err != nil {
			finish("", err)
			return stream, true
		}
		stream, err = a.dialPhoneStream(req.ctx)
		if err != nil {
			finish("", err)
			return stream, true
		}
		a.setStream(stream)
	}
	finish(recoveryToken, nil)
	return stream, true
}

// conn returns the live mailbox appender, or why there is none.
func (a *App) conn() (*phoneStream, error) {
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
	if a.stream == nil {
		return nil, classed(ErrClassOffline, errors.New("swarmmobile: relay connection not established yet"))
	}
	return a.stream, nil
}

// awaitConn waits briefly for the connection Start is bringing up, so a screen that
// issues a command immediately after Start is not refused by a race it cannot see. A
// stopped or closed App fails immediately -- there is nothing to wait for.
func (a *App) awaitConn() (*phoneStream, error) { return a.awaitStream() }

func (a *App) awaitStream() (*phoneStream, error) {
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
// connRepairRequired is PB-KEY-6's: a custody refusal is not a transport condition and must not
// be reported as one. It means "the key is gone" and is TERMINAL -- the loop stops, because
// retrying a destroyed key forever while showing a spinner is the failure it exists to remove.
//
// THERE IS NO connReauthRequired (ADR-007 B133). It meant "prompt for the biometric and it will
// connect", and there is no prompt left anywhere in the product to offer: all phone-side user
// authentication is removed, so the state had lost its remedy and its producer at once. It was
// deleted in the same change as the error_taxonomy.tsv row, the Kotlin ConnectionState and
// ErrorState entries and Remedy.AUTHENTICATE, because a state surviving on one side of that
// join is a screen nothing can ever reach.
const (
	connOffline        = "offline"
	connConnecting     = "connecting"
	connOnline         = "online"
	connReconnecting   = "reconnecting"
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

	// connRelayTrustUnavailable is ADR-016 W8's `relay_trust_unavailable`: "no platform
	// verifier answered ... an APP fault, not a relay fault. Never a security accusation."
	//
	// IT IS DISTINCT FROM connRelayUntrusted ON PURPOSE, and the webpki punch list's own
	// reproduction is why: a handset that has migrated to webpki and then starts with no
	// RelayTrust installed (Android's PhoneRuntime.installRelayTrust swallows every
	// exception it can raise, so SetRelayTrust is simply never called) resolves to
	// TrustRootsPinned with no pin -- handsetSecurity never populates one under webpki
	// (effectiveStatePin) -- and relay.DialSecure fails PRE-HANDSHAKE with ErrPinRequired,
	// exactly the same sentinel a genuinely-pinned phone with no pin gets. Reading BOTH as
	// connRelayUntrusted told this user "not the relay your machine published" for a fault
	// that is entirely this handset's own, which is the accusation W8 forbids by name.
	// relayTrustUnavailable (below) is what tells the two apart.
	//
	// The relay's own token, relay.RelayTrustUnavailable ("swarm-relaytrust/unavailable"),
	// reaches here too when Kotlin's RelayTrust delegate IS installed but itself fails to
	// consult the platform verifier (RelayTrustImpl's own second verdict) -- both are the
	// SAME "no platform verifier answered" fault, ADR-016's Conformance table's own row:
	// "No platform verifier | ErrPinRequired | relay_trust_unavailable | distinct copy from
	// a security verdict."
	connRelayTrustUnavailable = "relay_trust_unavailable"
)

// transportEndsPairing reports whether a transport state means this handset can no longer act as
// a paired phone. It is half of what App.StateSummary's Paired answers, and it is the half that
// covers every way a registration ends WITHOUT this phone being the one that ended it.
//
// WHY THE LINK IS ASKED AT ALL. App.PurgeKeys has exactly one production caller -- the Settings
// "Replace this computer" press -- so phonecore.State.Disowned records the path the PHONE takes
// and no other. The path the OWNER takes is `swarm remote revoke <device-id>` on the machine,
// which is the documented one and the only mitigation ADR-007 B133 leaves for a lost handset.
// Nothing on the phone runs for it. What the phone gets is a refused handshake, and these two
// states are where that lands (agents-tracker-d0b8).
//
// ONLY THE TERMINAL TWO QUALIFY, and the boundary is the one PB-APP-10 already drew: a state
// nothing on this device can recover from, whose remedy is therefore pairing again. Every other
// state here is a link condition, and ending a pairing on one would take the app away from a phone
// whose relay is merely down. connRelayUntrusted and connRelayInsecure are the two that look
// terminal and are NOT -- ADR-007 B58 has the first arriving on the ORDINARY first pairing, where
// a handset holding no pin yet is refused on every dial until the pairing it is running completes,
// and the second is the machine's configuration, which pairing again does not change.
//
// IT IS NOT WRITTEN DOWN, and that is the whole reason it is a live reading rather than a second
// durable flag. relay.ErrRevoked comes from the RELAY, which this design trusts for nothing else,
// and PB-STATE-10 records that a terminal revoked verdict is exactly the kind a pairing can make
// STALE. A verdict on disk would outlive the recovery that disproves it -- "the brick reached
// through the remedy", which is the sentence that requirement is named for. Held in memory it is
// re-formed on every dial: a relay that answers differently tomorrow is answered differently.
//
// THE GRACE IS THE REVOKE'S ALONE. rearmAfterPairing opens it because the two ends of a recovery
// cannot be ordered -- the machine opens this device's relay route over a connection of its own,
// just after the phone learns the pairing succeeded -- so a first dial that arrives before the ban
// is lifted must not send the handset back to the screen it has just come from. None of that bears
// on a destroyed Keystore entry, and a pairing cannot complete over a key that will not sign.
func transportEndsPairing(state string, graceUntil, now time.Time) bool {
	switch state {
	case connRepairRequired:
		return true
	case connRevoked:
		return graceUntil.IsZero() || !now.Before(graceUntil)
	default:
		return false
	}
}

// recordUnpaired writes down what the transport has just established: this handset's registration
// is over. It is called from the two TERMINAL arms of the dial loop and from nowhere else.
//
// WHY IT IS WRITTEN DOWN AT ALL, when transportEndsPairing already reads the live state. A
// connection state is process memory and Android SIGKILLs this app as routine behaviour. The
// handset comes back somewhere it cannot reach the relay -- no signal, aeroplane mode, a relay
// that is down -- and nothing re-derives the verdict: it reads as paired again, in the four-tab
// scaffold, holding a registration the machine deleted, with nothing on screen suggesting the
// user go looking for "Replace this computer". The live reading is the same fact with a shorter
// life, and it is kept because it cannot fail: it answers in the window before this write lands,
// and it still answers if the write is refused by a full disk or a read-only data directory.
//
// IT IS NOT A PURGE, and the line is deliberate. PB-KEY-7 destroys both key tiers irreversibly and
// its trigger is the OWNER acting on this handset (ADR-007 B133). Running it here would let the
// relay -- which this design trusts with no plaintext, no ordering and no authority -- destroy a
// user's cached content by answering one handshake with `revoked`; and on the connRepairRequired
// arm it would destroy content over a platform fault that is not a revocation at all. What is
// recorded is the fact the gate needs. The purge stays the owner's, and the owner's route to it is
// the pairing this now makes reachable.
//
// IT IS CLEARED BY PAIRING AGAIN, unconditionally and without asking what set it -- mobile/pairing
// .go's pin. A flag the transport could set and only a local press could clear would be a brick the
// other way round: the handset would complete the ceremony and still be shown the pairing screen.
//
// THE ERROR IS SWALLOWED because there is nobody to tell. This runs on the transport goroutine
// after the loop has already decided to end, there is no screen on this path, and the live reading
// covers the process either way. What a failure costs is exactly what not writing at all used to
// cost -- the next launch comes up paired -- and the next terminal dial writes again.
func (a *App) recordUnpaired() {
	a.publicationAuthorityMu.Lock()
	defer a.publicationAuthorityMu.Unlock()
	_ = a.core.Mutate(func(st *phonecore.State) { st.Disowned = true })
}

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

func (a *App) setStream(stream *phoneStream) {
	a.mu.Lock()
	a.stream = stream
	a.mu.Unlock()
}

// run is one native relay-v2 Start..Stop generation.
func (a *App) run(ctx context.Context) {
	first := true
	rb := newReconnectBackoff()
	for ctx.Err() == nil {
		if first {
			a.setConn(connConnecting)
			first = false
		} else {
			a.setConn(connReconnecting)
			delay := rb.next()
			if obs := reconnectDelayObserver.Load(); obs != nil {
				(*obs)(rb.attempt, delay)
			}
			select {
			case <-ctx.Done():
				break
			case <-time.After(delay):
			}
			if ctx.Err() != nil {
				break
			}
		}

		stream, err := a.dialPhoneStream(ctx)
		if err != nil {
			switch {
			case errors.Is(err, crypto.ErrKeyInvalidated), errors.Is(err, crypto.ErrKeyAuthRequired):
				a.setConn(connRepairRequired)
				a.recordUnpaired()
				return
			case errors.Is(err, relay.ErrNotAuthorized):
				a.setConn(connRevoked)
				if a.pairingInFlight() || a.withinPairingGrace() {
					continue
				}
				a.recordUnpaired()
				return
			case a.relayTrustUnavailable(err):
				a.setConn(connRelayTrustUnavailable)
				if a.pairingInFlight() || a.withinPairingGrace() {
					continue
				}
				return
			case errors.Is(err, relay.ErrPinMismatch), errors.Is(err, relay.ErrPinRequired), errors.Is(err, relay.ErrPinMalformed):
				a.setConn(connRelayUntrusted)
				if a.pairingInFlight() || a.withinPairingGrace() {
					continue
				}
				return
			case errors.Is(err, relay.ErrCleartextRefused):
				a.setConn(connRelayInsecure)
				if a.pairingInFlight() || a.withinPairingGrace() {
					continue
				}
				return
			}
			continue
		}

		a.setStream(stream)
		a.setConn(connOnline)
		rb.reset()
		pctx, cancelPump := context.WithCancel(ctx)
		pumpDone := make(chan struct{})
		go func() {
			defer close(pumpDone)
			a.runPublicationPump(pctx, func() (sendCtx, error) {
				return a.resolveSend(func() (*phoneStream, error) { return a.conn() })
			})
		}()
		a.wakePublicationPump()
		stream = a.drainPhone(ctx, stream)
		cancelPump()
		<-pumpDone
		if stream != nil {
			stream.Close()
		}
		a.setStream(nil)
		a.suspendInput("the connection to the machine was lost")
	}
	a.setStream(nil)
	a.setConn(connOffline)
}

// run is one Start..Stop generation: dial, drain, reconnect until the context is done.
func (a *App) handsetSecurity() relay.Security {
	sec := relay.Security{AllowLoopbackCleartext: true}
	st := a.core.State()
	if pin := effectiveStatePin(st.RelayTLSPolicy, st.RelaySPKIPin); len(pin) > 0 {
		sec.PinnedSPKISHA256 = pin
	}
	// ADR-016 W2: Android's platform delegate, if SetRelayTrust installed one. A pin set
	// above still outranks it (security.go's own precedence), so a pinned_spki handset's
	// session dial is unaffected -- this only ever matters for a webpki handset, where it is
	// what lets TrustRootsPlatformDelegate replace the pinning-only floor.
	sec = a.withPlatformTrust(sec)
	if src := os.Getenv(envTestTrustRoots); src != "" {
		sec = relay.WithTrustRootSource(sec, relay.TrustRootSource(src))
	}
	return sec
}

// effectiveRelayPin is ADR-016 W3's PAIRING-dial scoping: "a pin is
// consulted if and only if the effective relay TLS policy is pinned_spki." It decides what
// checkRelayPin ever SEES from a just-authenticated machine payload, never what
// checkRelayPin itself does -- that fence (TestB48_CheckRelayPin) stays untouched.
//
// Empty policy is LEGACY -- a machine build that predates ADR-016 -- and reads as
// pinned_spki, today's behaviour exactly: an old machine's payload carries a pin and no
// policy field, and a phone that stopped consulting it on the strength of an ABSENT field
// would be reading absence as an authenticated webpki claim, which W9's ladder forbids.
func effectiveRelayPin(m pairing.MachinePayload) []byte {
	return effectiveStatePin(m.RelayTLSPolicy, m.RelaySPKIPin)
}

// applyRelayTLSPolicy is ADR-016 W4/W9's migration ladder, run over one reconcile's
// published schema.RemoteProfileV1: "A pinned client migrates only on advertise + prove +
// commit; failure retains the pin and offers a repair path, and never disables
// validation." probe is the injected W4-step-3 dial: a real caller wires it to a webpki
// probe dial (W2's platform delegate + Go's own VerifyHostname) on a connection separate
// from the live pinned one, which stays up throughout.
//
// The four rungs, in the order stated because each depends on the one below:
//
//  1. profile.RelayTLSPolicy == "" is NO ADVERTISEMENT AT ALL -- an old machine build, or
//     a reconcile the profile fields have not reached yet. NO-OP, and the same mechanism
//     covers both "old machine leaves the phone exactly as it is" and "downgrade does not
//     un-migrate silently": in neither case may an absent claim be read as authenticated.
//  2. "pinned_spki" is B54's REVERSE direction, adopted VERBATIM and unconditionally --
//     reverting to the expert policy proves nothing, so no probe is needed.
//  3. "webpki" with a host that does not match a.relayURL is refused as stale_profile (W4
//     step 2): a profile that changes the destination is a re-pairing question, never a
//     TLS migration, and proving a DIFFERENT host tells the phone nothing about whether
//     ITS relay is trustworthy.
//  4. "webpki" with a matching host PROVEs via probe, then COMMITs only on success (W4
//     steps 3-4); a failed probe RETAINS the phone's current policy untouched (W4 step 5)
//     and returns the probe's own error, wrapped, so a caller can turn it into the
//     webpki_unavailable repair state naming the cause.
//
// THE COMMIT NEVER CLEARS RelaySPKIPin (W4.4): B54's verbatim-adoption rule applies to
// whatever the profile carries for it, unrelated to this ladder's own policy write.
func (a *App) applyRelayTLSPolicy(ctx context.Context, profile schema.RemoteProfileV1, probe func(context.Context, string) error) error {
	if profile.RelayTLSPolicy == "" {
		// No authenticated advertisement: never read as a claim, in either direction.
		return nil
	}
	if profile.RelayTLSPolicy != "webpki" {
		if profile.RelayTLSPolicy != "pinned_spki" {
			// W1 names exactly two policy values. A machine (or a future bug) publishing
			// anything else is refused, not adopted verbatim into durable state: a future
			// reader that positively matches "pinned_spki" rather than treating anything
			// unrecognised as "consult the pin" would otherwise misbehave silently, and
			// the state file would carry a value no writer ever validated.
			return classed(ErrClassInvalidRequest, fmt.Errorf(
				"swarmmobile: relay profile names an unrecognised policy %q; refused rather than "+
					"adopted (the two named values are \"webpki\" and \"pinned_spki\")",
				profile.RelayTLSPolicy))
		}
		if len(profile.RelaySPKIPin) == 0 {
			// W1's own rule makes this combination impossible from a legitimate machine:
			// --relay-pin is MANDATORY under pinned_spki. Adopting it anyway would wipe a
			// working pin on the strength of a claim its own policy says cannot be true --
			// ErrPinRequired forever on Android, a silent demotion to system-root
			// verification on desktop, with no probe and no guard. Refused, not adopted.
			return classed(ErrClassInvalidRequest, fmt.Errorf(
				"swarmmobile: relay profile names policy %q with no pin; refused rather than "+
					"adopted (a legitimate machine never publishes this combination)", profile.RelayTLSPolicy))
		}
		// REVIEW-ROUND FIX: a phone already on pinned_spki with this EXACT pin is a NO-OP,
		// so a steady-state pinned_spki machine does not re-Mutate durable state on every
		// reconcile. Pin equality is part of THIS check, unlike the webpki no-op below: a
		// pinned_spki phone's pin is the whole defense (W3), so a ROTATED pin (W5) must
		// still be adopted rather than short-circuited away.
		if st := a.core.State(); st.RelayTLSPolicy == "pinned_spki" && bytes.Equal(st.RelaySPKIPin, profile.RelaySPKIPin) {
			return nil
		}
		// B54's reverse direction: adopted verbatim, unconditionally -- reverting to the
		// expert policy proves nothing, so no probe is needed.
		return a.core.Mutate(func(st *phonecore.State) {
			st.RelayTLSPolicy = profile.RelayTLSPolicy
			st.RelaySPKIPin = profile.RelaySPKIPin
		})
	}
	// W4 step 2: check identity of destination BEFORE proving anything against it, and
	// BEFORE the no-op short-circuit below -- a mismatched host must refuse as stale_profile
	// even when this phone's own policy and pin already happen to equal the profile's
	// (the webpki punch list's LOW ordering finding: the short-circuit used to run first and
	// silently admitted a destination-changing profile as a no-op). Classed
	// ErrClassInvalidRequest (PB-APP-9): the machine's own published profile conflicts with
	// the destination this phone already holds, which is a re-pairing question and not a
	// request this phone can satisfy by retrying.
	//
	// A HOST THIS PHONE CANNOT NAME IS A REFUSAL, NOT A SKIP. relayURLHost returning "" used
	// to fall through and prove a host from nowhere: url.Parse rarely errors on a malformed
	// string, so an unparsable a.relayURL silently disabled the one check W4 step 2 exists
	// to run. A phone that cannot determine its own destination has not satisfied "relay_host
	// must equal the host of the relay URL the phone already holds" -- it refuses.
	host := relayURLHost(a.relayURL)
	if host == "" {
		return classed(ErrClassInvalidRequest, fmt.Errorf(
			"swarmmobile: cannot determine this phone's own relay host from %q; refusing the "+
				"webpki migration profile rather than proving a destination this phone cannot name",
			a.relayURL))
	}
	if host != profile.RelayHost {
		return classed(ErrClassInvalidRequest, fmt.Errorf(
			"swarmmobile: relay profile names host %q, this phone holds %q; "+
				"refused as stale_profile (a profile that changes the destination is a re-pairing "+
				"question, not a TLS migration)", profile.RelayHost, host))
	}
	// REVIEW-ROUND FIX, now AFTER the host check above: a phone already on webpki against
	// the correct host is a NO-OP, so a steady-state webpki machine does not re-open a probe
	// dial (see probeWebPKI) on every reconcile. POLICY ONLY, deliberately not the pin: W3's
	// single rule is that a webpki phone never reads the pin at all, so a profile that
	// withdraws or rotates the W9 compatibility pin changes nothing this phone consults, and
	// must therefore produce NO OBSERVABLE CHANGE here -- ADR-016's own Conformance table
	// for W9 step 6 (line 274): "no observable change on a migrated handset." Comparing pins
	// here as well would reopen a probe dial on every withdrawal, exactly the regression
	// the no-op fix above was written to close, one rung up.
	if st := a.core.State(); st.RelayTLSPolicy == "webpki" {
		return nil
	}
	// W4 step 3: PROVE on a separate connection before touching durable state. Classed
	// ErrClassOffline (PB-APP-9): a failed probe is the transport refusing or failing to
	// validate the relay, recoverable by retrying once the machine or network condition
	// that caused it clears -- W4 step 5's webpki_unavailable repair state.
	if err := probe(ctx, profile.RelayHost); err != nil {
		// W4 step 5: any failure retains the phone's CURRENT policy and pin untouched --
		// never disables validation, never writes a weaker state.
		return classed(ErrClassOffline, fmt.Errorf(
			"swarmmobile: webpki probe of %q failed: %w", profile.RelayHost, err))
	}
	// W4 step 4: COMMIT only on a successful probe. RelaySPKIPin is retained (W4.4), not
	// cleared: a phone that deleted it would have nothing to fall back to on rollback, and
	// clearing fights B54's verbatim-adoption rule on the next reconcile.
	return a.core.Mutate(func(st *phonecore.State) {
		st.RelayTLSPolicy = "webpki"
		st.RelaySPKIPin = profile.RelaySPKIPin
	})
}

// relayURLHost extracts the host from a.relayURL for W4 step 2's destination check. An
// unparsable or empty URL (never paired yet) yields "", which the caller reads as "no
// destination to check against" rather than a manufactured mismatch.
func relayURLHost(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// effectiveStatePin is the one scoping rule (W3's "single rule, stated once"), shared by
// effectiveRelayPin (the pairing dial) and handsetSecurity (the session dial): a pin is
// returned verbatim under pinned_spki or an unset (legacy) policy, and withheld under
// webpki -- "not by teaching tlsConfig to ignore a pin it was handed" but by never handing
// it one, per the Blast radius mechanism sentence.
//
// The literal "webpki" is relaycfg.PolicyWebPKI's own value (pinned by
// TestADR016W1_PolicyConstantsAreTheTwoADRNames); it is not imported here so the mobile
// facade's dependency surface stays exactly what it was.
func effectiveStatePin(policy string, pin []byte) []byte {
	if policy == "webpki" {
		return nil
	}
	return pin
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

func (a *App) dialPhoneStream(ctx context.Context) (*phoneStream, error) {
	target, _ := a.destination()
	if target == "" {
		return nil, errNoDestination
	}
	st := a.core.State()
	ks := a.core.KeyStore()
	profile := relayv2.Profile{
		RelayURL: a.relayURL, MachineRID: target, OperatorNamespace: st.OperatorNamespace,
		Security: a.handsetSecurity(),
	}
	auth := relayv2.Auth{
		PublicKey: ed25519.PublicKey(ks.RelayAuthPublic()), Sign: ks.SignRelayAuth,
		Role: relayv2.RolePhone, Purpose: relayv2.PurposeStream,
	}
	dial := func() (*relayv2.Conn, relayv2.Binding, phonecore.PhoneBinding, error) {
		conn, err := relayv2.Dial(ctx, profile, auth)
		if err != nil {
			return nil, relayv2.Binding{}, phonecore.PhoneBinding{}, err
		}
		binding, err := conn.PhoneBinding()
		if err != nil {
			conn.Close()
			return nil, relayv2.Binding{}, phonecore.PhoneBinding{}, err
		}
		coreBinding := phonecore.PhoneBinding{
			Home: relayv2.HomeID(st.OperatorNamespace, target), PhoneRID: binding.PeerRID,
			Generation: binding.Generation, Active: true,
		}
		if err := a.core.ActivatePhoneBinding(coreBinding); err != nil {
			conn.Close()
			return nil, relayv2.Binding{}, phonecore.PhoneBinding{}, err
		}
		return conn, binding, coreBinding, nil
	}

	conn, binding, coreBinding, err := dial()
	if err != nil {
		return nil, err
	}
	checkpoint := relayv2.Checkpoint{Incarnation: a.core.State().RelayIncarnation, Cursor: a.core.State().RelayCursor}
	sub, err := conn.Subscribe(ctx, binding, checkpoint)
	var protocolErr *relayv2.ProtocolError
	if err != nil && checkpoint.Incarnation != "" && errors.As(err, &protocolErr) && protocolErr.Code == "incarnation_mismatch" {
		recoveryToken, recoveryIncarnation, recoveryCursor := a.core.DiscardRecovery()
		if recoveryToken != "" && recoveryIncarnation == checkpoint.Incarnation {
			discarded, discardErr := conn.Discard(ctx, binding, checkpoint.Incarnation, recoveryCursor)
			if discardErr != nil {
				conn.Close()
				return nil, discardErr
			}
			if err := a.core.AdoptPhoneDiscard(coreBinding, checkpoint.Incarnation, discarded.Incarnation, discarded.Cursor); err != nil {
				return nil, err
			}
		} else {
			conn.Close()
			if err := a.core.RecoverPhoneIncarnation(coreBinding, checkpoint.Incarnation); err != nil {
				return nil, err
			}
		}
		conn, binding, coreBinding, err = dial()
		if err != nil {
			return nil, err
		}
		sub, err = conn.Subscribe(ctx, binding, relayv2.Checkpoint{})
	}
	if err != nil {
		conn.Close()
		return nil, err
	}
	subCheckpoint := sub.Checkpoint()
	if err := a.core.SetPhoneCheckpoint(coreBinding, subCheckpoint.Incarnation, subCheckpoint.Cursor); err != nil {
		conn.Close()
		return nil, err
	}
	return &phoneStream{conn: conn, sub: sub, binding: binding, coreBinding: coreBinding}, nil
}

// drainPhone is the sole Subscription.Recv owner. A refresh cancels only its current
// receive context, then the next loop iteration performs the serialized PROBE/DISCARD.
func (a *App) drainPhone(ctx context.Context, stream *phoneStream) *phoneStream {
	for ctx.Err() == nil {
		if next, handled := a.performMailboxDiscard(stream); handled {
			stream = next
			if stream == nil {
				return nil
			}
			continue
		}
		recvCtx, cancel := context.WithCancel(ctx)
		a.mu.Lock()
		a.recvCancel = cancel
		pendingRecovery := a.mailboxDiscard != nil
		a.mu.Unlock()
		if pendingRecovery {
			cancel()
			a.mu.Lock()
			a.recvCancel = nil
			a.mu.Unlock()
			continue
		}
		delivery, err := stream.sub.Recv(recvCtx)
		cancel()
		a.mu.Lock()
		a.recvCancel = nil
		pendingRecovery = a.mailboxDiscard != nil
		a.mu.Unlock()
		if err != nil {
			if pendingRecovery {
				continue
			}
			return stream
		}
		receipt, err := a.acceptPhone(ctx, stream, delivery.Ciphertext, delivery.Cursor)
		if blocksMailboxPage(receipt, err) {
			return stream
		}
	}
	return stream
}

func diagnosePhoneDeliveries(ctx context.Context, deliveries []relayv2.Delivery, accept mailboxAccept) (uint64, error) {
	for _, delivery := range deliveries {
		receipt, err := accept(ctx, delivery.Ciphertext, delivery.Cursor)
		if !blocksMailboxPage(receipt, err) {
			continue
		}
		if errors.Is(err, crypto.ErrStaleAge) {
			return delivery.Cursor, nil
		}
		return 0, err
	}
	return 0, nil
}

// mailboxAccept is the narrow acceptance seam used by PROBE diagnosis tests.
type mailboxAccept func(context.Context, []byte, uint64) (phonecore.Receipt, error)

func blocksMailboxPage(receipt phonecore.Receipt, err error) bool {
	return err != nil && !receipt.Acked && receipt.Disposition == phonecore.ReceiptRetained
}

// acceptPhone binds the core transaction and its ACK to the exact subscription that
// produced the delivery. Pairing or discard replacement cannot redirect an old ACK onto
// the new generation because lifecycle teardown joins this call before publishing it.
func (a *App) acceptPhone(ctx context.Context, stream *phoneStream, raw []byte, cursor uint64) (phonecore.Receipt, error) {
	key := a.core.State().Keys.ContentKey
	a.mu.Lock()
	a.ackStream = stream
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		if a.ackStream == stream {
			a.ackStream = nil
		}
		a.mu.Unlock()
	}()
	receipt, err := a.core.AcceptPhoneDelivery(stream.coreBinding, raw, cursor)
	if err != nil {
		return receipt, err
	}
	a.publishAccepted(ctx, key, raw)
	return receipt, nil
}

func (a *App) publishAccepted(ctx context.Context, key crypto.ContentKey, raw []byte) {
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
		a.adoptReconcile(ctx)
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
func (a *App) adoptReconcile(ctx context.Context) {
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

	// ADR-016 W4/W9's migration ladder, run over THIS reconcile's own authenticated
	// profile (W4.1: "a relay-supplied or unauthenticated hint is ignored"). LastProfile
	// reads back exactly what the Reconcile call three lines up just adopted -- it takes no
	// parameters, so there is no way to reach this with a profile that check did not pass.
	//
	// THE ERROR IS SURFACED, NOT DISCARDED (the webpki punch list's W4.5 finding).
	// applyRelayTLSPolicy's own failure branch already leaves durable state exactly as it
	// was (W4 step 5) -- the SECURITY half of the ruling never depended on this call being
	// reported, which is why a bare `_ = ...` here shipped without an obvious defect -- but
	// the USER-FACING half of the same ruling is "surfaces webpki_unavailable with the
	// operator-facing cause", and nothing did that. reportWebPKIUnavailable is the
	// pull-and-event surface for it, in exactly the shape reportSkew already gives
	// PB-TIME-1: adoptReconcile still runs on the drain goroutine with no screen
	// necessarily open on this exact call, so a PUSH-only surface would still lose an
	// event raised while nothing was listening.
	a.reportWebPKIUnavailable(a.applyRelayTLSPolicy(ctx, a.core.LastProfile(), a.probeWebPKI))
}

// reportWebPKIUnavailable is ADR-016 W4 step 5's surfacing half, mirroring reportSkew's
// own shape for App.ClockVerdict: cause is applyRelayTLSPolicy's own return from THIS
// reconcile (nil when the ladder had nothing to prove or proved it). Only a CHANGE is
// emitted and the verdict is not latched -- a machine stuck on a failing probe would
// otherwise raise an event on every single reconcile, and a later reconcile that succeeds
// (or has nothing to prove) must clear the verdict rather than leave a stale banner up.
func (a *App) reportWebPKIUnavailable(cause error) {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	a.mu.Lock()
	changed := a.webpkiUnavailable != msg
	a.webpkiUnavailable = msg
	a.mu.Unlock()
	if !changed {
		return
	}
	if msg == "" {
		a.events.emit(&Event{Kind: "connection", Stream: "webpki", State: "available"})
		return
	}
	a.events.emit(&Event{Kind: "connection", Stream: "webpki", State: "webpki_unavailable", Message: msg})
}

// probeWebPKI is W4 step 3's real dial: relay_host already matched a.relayURL (the caller
// checked before calling probe at all), so proving the profile's claim is exactly proving
// THIS phone's own relay URL under an ordinary verified relay-v2 dial -- the platform
// delegate on Android once SetRelayTrust installed one (W2), the system trust store
// elsewhere -- on a
// separately authenticated phone/probe purpose that cannot supersede or issue RPCs on the
// live phone/stream purpose.
func (a *App) probeWebPKI(ctx context.Context, _ string) error {
	// REVIEW-ROUND FIX: bounded on ITS OWN deadline rather than inheriting whatever the
	// drain goroutine's ctx happens to carry -- this runs on that goroutine synchronously,
	// so an unbounded probe would block message draining for as long as the relay stays
	// silent. relayv2.DefaultDialTimeout is the same bound the relay-v2 connect phase applies;
	// this makes it explicit rather than incidental.
	ctx, cancel := context.WithTimeout(ctx, relayv2.DefaultDialTimeout)
	defer cancel()
	target, _ := a.destination()
	if target == "" {
		return errNoDestination
	}
	st := a.core.State()
	ks := a.core.KeyStore()
	conn, err := relayV2DialProbe(ctx, relayv2.Profile{
		RelayURL: a.relayURL, MachineRID: target, OperatorNamespace: st.OperatorNamespace,
		Security: a.withPlatformTrust(relay.Security{AllowLoopbackCleartext: true}),
	}, relayv2.Auth{
		PublicKey: ed25519.PublicKey(ks.RelayAuthPublic()), Sign: ks.SignRelayAuth,
		Role: relayv2.RolePhone, Purpose: relayv2.PurposeProbe,
	})
	if err != nil {
		return err
	}
	conn.Close()
	return nil
}

func (a *App) onJournal(rec schema.JournalRecord) {
	if rec.Type == phonecore.RecordTypeInteraction {
		a.onInteraction(rec)
		return
	}
	entry := JournalEntry{
		Cursor:    int64(rec.Cursor),
		SessionID: rec.SessionID,
		Type:      rec.Type,
		Group:     string(rec.Group),
		TSUnixMs:  unixMs(rec.TS), // the daemon's stamp, 0 where the wire carried none (W7.4)
	}
	a.mu.Lock()
	a.journal = append(a.journal, entry)
	if len(a.journal) > journalLogSize {
		a.journal = a.journal[len(a.journal)-journalLogSize:]
	}
	if rec.SessionID != "" && rec.Type != "" &&
		rec.Type != phonecore.RecordTypeSessionState &&
		rec.Type != phonecore.RecordTypeCapabilityTransition {
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

// onInteraction raises the item-appended event. The core has already folded the item into the
// durable transcript by the time this runs (AcceptCommit commits before apply), so the event
// is a WAKE and not a delivery: a screen re-reads through ReadTranscript, which is the only
// surface that has the folded body.
//
// IT DOES NOT TOUCH a.journal OR a.needs. The journal page is the activity log of roster
// events and Need is the verbatim record type the triage row renders, so an item written into
// either would replace "needs_input" on a session row with a word about carriage, and invent
// a log entry for something the transcript already holds (IS-SS-1).
//
// The KIND rides on Message, unparsed by anything here beyond the discriminator: a wake that
// cannot say whether prose or an approval card arrived forces every screen to re-read the
// whole transcript to find out.
func (a *App) onInteraction(rec schema.JournalRecord) {
	a.mu.Lock()
	subscribed := a.subscribed
	a.mu.Unlock()
	if !subscribed {
		return
	}
	var item struct {
		Kind   string `json:"kind"`
		Status string `json:"status"`
	}
	_ = json.Unmarshal(rec.Item, &item) // an undecodable item was already skipped by the core
	a.events.emit(&Event{
		Kind:      "interaction",
		Stream:    "journal",
		SessionID: rec.SessionID,
		State:     item.Status,
		Message:   item.Kind,
		Cursor:    int64(rec.Cursor),
	})
}

func (a *App) onReply(ctrl schema.Control) {
	// The authenticated timestamp is useful even when a reply was delivered behind a gap, so
	// close/report the skew bracket first. Operation settlement, UI events and kill-switch state
	// require the verdict that commitReceive durably attributed; router.apply's live reply alone
	// is deliberately insufficient.
	a.reportSkew()
	committed, ok := a.core.State().OpOutcomes[ctrl.OperationID]
	if ctrl.OperationID == "" || !ok {
		return
	}
	ctrl = committed
	a.resolve(ctrl.OperationID)
	a.mu.Lock()
	a.killSwitch = ctrl.ErrorCode == schema.CodeKillSwitch
	a.mu.Unlock()
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
		// THE VERDICT IS THE MONITOR'S; THE WORDS ARE NOT (agents-tracker-ksvb.5). What used to
		// go on the wire was err.Error() -- "phonecore: this device's clock is out of sync with
		// the machine: measured 1m45.3018s off (machine minus phone), outside the +/-30s
		// budget" -- and ConnectionUi renders what it is given, so that string WAS the banner
		// above every screen. The measurement is read separately rather than parsed back out of
		// the chain, which is where the number honestly lives.
		msg = clockBannerText(a.core.SkewMonitor().Skew())
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

// clockUnmeasuredBanner is PB-TIME-1's sentence with no figure in it.
//
// SkewMonitor.Check can only answer non-nil after a completed bracket, so a verdict with no
// measurement behind it is not a state this app reaches -- this is the defence, and its shape
// is the point: the half that survives is the REMEDY. A figure of zero would tell a user
// whose clock is broken that it is exactly right, and dropping the sentence entirely would
// leave the banner empty at the one moment it is the only thing explaining why every command
// is being refused.
const clockUnmeasuredBanner = "This phone's clock is too far off your machine's to send " +
	"commands safely. Turn on automatic date and time in Android settings."

// clockBannerText is the measured verdict as something a person can act on.
//
// THE SIGN IS DISCARDED ON PURPOSE. phonecore.Skew.Offset is MACHINE MINUS PHONE, so a handset
// running fast measures negative; "about -105 seconds off" renders the measurement's own sign
// convention as copy, and the reader's fact is the same either way -- the clock is wrong by
// that much, and the setting that fixes it is the same setting.
//
// IT ROUNDS TO WHOLE SECONDS, which the +/-30 s budget makes safe: a verdict exists only when
// the whole bracket lies outside that bound, so the figure is never small enough for the
// rounding to matter and never zero. Full time.Duration precision was what made the old string
// change on every reply.
func clockBannerText(measured phonecore.Skew) string {
	if !measured.Known {
		return clockUnmeasuredBanner
	}
	off := measured.Offset
	if off < 0 {
		off = -off
	}
	seconds := int64((off + time.Second/2) / time.Second)
	if seconds == 0 {
		return clockUnmeasuredBanner
	}
	return fmt.Sprintf("This phone's clock is about %d seconds off your machine's -- too far "+
		"to send commands safely. Turn on automatic date and time in Android settings.", seconds)
}
