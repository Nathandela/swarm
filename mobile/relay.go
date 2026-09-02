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
	"github.com/Nathandela/swarm/internal/remote/transport"
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
// Acks are COALESCED to one per drained page rather than one per frame. The relay ack is
// monotonic and idempotent -- acking cursor N releases everything up to it -- and an ack
// that a process death loses is harmless: the relay redelivers, the phone's DURABLE
// receive high-water refuses the redelivery with crypto.ErrStaleSeq, and the frame is
// acked then. Per-frame acking cost a full websocket round trip and a server-side commit
// on every journal event, which made the phone drain several times slower than the
// machine could publish.
type relayAcker struct{ app *App }

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
	if cursor > a.ackPending {
		a.ackPending = cursor
	}
	a.mu.Unlock()
	return nil
}

// flushAcks releases everything the core has committed since the last flush. It is the
// POLL drain's ack path; the wait drain acks the same coordinates through
// transport.AckBatcher instead (see drainWait), which is why the batcher's flush closure
// mirrors the ackSent bookkeeping below.
//
// The error is REPORTED, not acted on: an ack is an optimisation (the durable receive
// high-water refuses any redelivery), so nothing is lost when one fails. The poll drain
// ignores it, because its next MailboxRead re-probes the link within one
// DefaultCallTimeout anyway.
func (a *App) flushAcks(ctx context.Context, cl *relay.Client, generation uint64) error {
	a.mu.Lock()
	cursor := a.ackPending
	sent := a.ackSent
	a.mu.Unlock()
	if cursor <= sent {
		return nil
	}
	if err := cl.MailboxAckGeneration(ctx, cursor, generation); err != nil {
		return err
	}
	a.mu.Lock()
	if cursor > a.ackSent {
		a.ackSent = cursor
	}
	a.mu.Unlock()
	return nil
}

// requestMailboxDiscard hands an explicit roster refresh to the single mailbox reader and
// waits for its bounded diagnosis/recovery result. Executing on the drain is the concurrency
// boundary: a facade-side InboundAgeRefused check can race the wait page currently being
// accepted and publish the replacement behind the stale backlog. A healthy diagnosis returns
// an empty token and deletes nothing; only authenticated stale age (or a durable pending token)
// crosses into the destructive, incarnation-fenced self-mailbox discard.
func (a *App) requestMailboxDiscard() (string, error) {
	// RefreshRoster is an idempotent command, so it inherits the command plane's brief
	// post-Start wait. Start publishes sess before run publishes client; failing immediately
	// in that ordinary window regresses the roster-only refresh that existed before guarded
	// stale-head diagnosis. This only waits for the connection -- the facade still performs
	// no mailbox read, and the request below is still claimed by the drain's single reader.
	if _, err := a.awaitConn(); err != nil {
		return "", err
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return "", errClosed
	}
	sess := a.sess
	if sess == nil || a.client == nil {
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
	wake := a.waitCancel
	a.mu.Unlock()
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
// in-flight read. acks is the wait path's AckBatcher and nil on the synchronous poll path.
// Reset is both the wait-ack generation barrier before a synchronous healthy-diagnosis ack,
// and the retirement barrier before a destructive transaction is issued.
func (a *App) performMailboxDiscard(cl *relay.Client, acks *transport.AckBatcher) bool {
	a.mu.Lock()
	req := a.mailboxDiscard
	if req == nil || req.claimed {
		a.mu.Unlock()
		return false
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
		return true
	}
	// The stale verdict may not exist YET: RefreshRoster can cross a page already in flight
	// before MailboxRouter opens its authenticated head. Diagnose one immediate page here,
	// while the single reader owns the connection, so one press cannot publish its replacement
	// behind an existing stale backlog. Stop at the first unique, unacked stale-age refusal:
	// later items must not advance the durable cursor past it.
	pendingToken := a.core.DiscardRecoveryToken()
	staleAge := a.core.Router().InboundAgeRefused()
	ackGeneration := cl.MailboxGeneration()
	if !staleAge && pendingToken == "" {
		cursor := a.core.State().RelayCursor
		items, err := cl.MailboxRead(req.ctx, cursor)
		if errors.Is(err, relay.ErrMailboxCursorResetRequired) {
			if err = a.rewindRelayCursor(); err == nil {
				ackGeneration = cl.MailboxGeneration()
				items, err = cl.MailboxRead(req.ctx, 0)
			}
		}
		if err != nil {
			finish("", err)
			return true
		}
		if err := a.adoptRelayIncarnation(cl.MailboxIncarnation()); err != nil {
			finish("", err)
			return true
		}
		staleAge, err = diagnoseMailboxPage(req.ctx, items, a.accept)
		if err != nil {
			finish("", err)
			return true
		}
	}
	// Healthy frames may have repaired the transport while RefreshRoster woke the reader.
	// Compacting them would no longer be the narrowly authorized recovery. Before the caller
	// publishes its ordinary roster refresh, synchronously ack the safe diagnostic high-water:
	// otherwise a mailbox at its depth cap refuses the daemon's replacement. This explicit,
	// user-visible refresh deliberately pays at most one fsync/relay op; ordinary drains retain
	// the off-delivery-path, metered AckBatcher latency and quota behavior. Reset first on the
	// wait path so no old async ack overlaps this generation-fenced flush.
	if !staleAge && pendingToken == "" {
		if acks != nil {
			acks.Reset()
		}
		if err := a.flushAcks(req.ctx, cl, ackGeneration); err != nil {
			finish("", err)
			return true
		}
		finish("", nil)
		return true
	}
	if !a.mailboxRecoverySupported.Load() {
		finish("", relay.ErrPeerCapabilityUnavailable)
		return true
	}
	// The intent must reach durable state BEFORE the destructive RPC. If the process dies
	// after this Save, the next explicit RefreshRoster sees the same token and reissues the
	// incarnation-fenced idempotent discard even though the in-memory age refusal is gone.
	recoveryToken, err := a.core.BeginRelayDiscardRecovery()
	if err != nil {
		finish("", err)
		return true
	}
	if acks != nil {
		acks.Reset()
	}
	target, _ := a.destination()
	if target == "" {
		routeErr := classed(ErrClassOffline, errors.New("swarmmobile: paired machine route unavailable for mailbox recovery"))
		finish("", routeErr)
		return true
	}
	result, err := cl.MailboxDiscard(req.ctx, target)
	if err != nil {
		finish("", err)
		return true
	}
	if err := a.core.AdoptRelayDiscard(result.ThroughCursor, result.MailboxIncarnation); err != nil {
		// The relay operation is idempotent and returns the same durable high-water even when
		// its mailbox is now empty. Report the failed local adoption honestly; the next pull
		// retries it instead of pretending replacement state can resume from an unpersisted
		// coordinate.
		finish("", err)
		return true
	}
	a.mu.Lock()
	a.ackPending = result.ThroughCursor
	a.ackSent = result.ThroughCursor // the destructive op already compacted through it
	a.mu.Unlock()
	finish(recoveryToken, nil)
	return true
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

func (a *App) setClient(cl *relay.Client) {
	a.mu.Lock()
	a.client = cl
	a.mu.Unlock()
}

// run is one Start..Stop generation: dial, drain, reconnect until the context is done.
func (a *App) run(ctx context.Context) {
	first := true
	rb := newReconnectBackoff()
	for ctx.Err() == nil {
		if first {
			a.setConn(connConnecting)
			first = false
		} else {
			// A state that is NOT a recoverable link condition must not be overwritten by
			// "reconnecting": a spinner promises that waiting is enough, and for every state
			// held here waiting is exactly what does not help. The state therefore persists
			// across the retry, and the next successful dial clears it by setting "online".
			//
			// connRevoked only ever survives a retry inside the post-pairing window
			// rearmAfterPairing opens: hiding it behind a spinner there would put back exactly
			// the loop PB-APP-10 forbids. The transport-policy verdicts are on this list for
			// the same reason: they survive a retry only while a pairing is in flight (B58),
			// and overwriting them with "reconnecting" there would put the spinner back over
			// the one screen that says what is actually wrong.
			if s := a.currentConn(); s != connRevoked && s != connRelayUntrusted &&
				s != connRelayInsecure && s != connRelayTrustUnavailable {
				a.setConn(connReconnecting)
			}
			delay := rb.next()
			// Test-only observation seam (nil in production, the internal/shim
			// testHookAfterSignalArm pattern): it publishes the delay the loop SCHEDULED,
			// which is the only place that quantity exists. Every out-of-process rig can
			// see is a dial ARRIVING at a relay, which is the scheduled delay plus however
			// long this host took to wake the goroutine and carry the connection -- a
			// quantity PB-NET-4 does not control and must not assert (Wave R6 review round
			// 3, finding F5(a): a 605.6 ms arrival gap against section 6.0's 600 ms
			// ceiling, on a schedule that had been correct). It adds no production
			// behaviour: nothing reads it unless a test has installed an observer.
			if obs := reconnectDelayObserver.Load(); obs != nil {
				(*obs)(rb.attempt, delay)
			}
			select {
			case <-ctx.Done():
			case <-time.After(delay):
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
			// moment PB-KEY-9's Keystore-backed KEK landed: a destroyed key became an
			// endless "reconnecting" loop against something that would never work again.
			switch {
			case errors.Is(err, crypto.ErrKeyInvalidated),
				errors.Is(err, crypto.ErrKeyAuthRequired):
				// PERMANENT and therefore TERMINAL. The relay-auth key is destroyed or
				// unusable; nothing on-device recovers it and every retry is a round trip
				// spent proving that again. Returning here rather than breaking is
				// deliberate -- break would fall through to setConn("offline") and erase
				// the one state that tells the user to pair again.
				//
				// THE TWO SENTINELS SHARE AN ARM AFTER ADR-007 B133, and they did not
				// before. ErrKeyAuthRequired used to set "reauth_required", which meant
				// "prompt for the biometric and it will connect". There is no prompt left
				// in the product, so the same refusal is now something the user can never
				// satisfy on this handset -- which is what "permanent" means. Pairing again
				// is a real fix and is what the state says: it re-provisions the key
				// without the authenticator that is refusing.
				a.setConn(connRepairRequired)
				a.recordUnpaired()
				a.setClient(nil)
				return
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
				a.recordUnpaired()
				a.setClient(nil)
				return
			case a.relayTrustUnavailable(err):
				// ADR-016 W8's own Conformance row: "No platform verifier | ErrPinRequired |
				// relay_trust_unavailable | distinct copy from a security verdict." Checked
				// BEFORE the generic ErrPinRequired arm below, which this would otherwise
				// also match -- see relayTrustUnavailable's own doc for why the same
				// sentinel needs telling apart by cause rather than by identity alone.
				a.setConn(connRelayTrustUnavailable)
				// Same B58 non-terminal-during-pairing rule as the arm below: the ordinary
				// first pairing on a handset with no delegate installed yet must survive.
				if a.pairingInFlight() || a.withinPairingGrace() {
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
		// The wait verdict is negotiated FIRST, before the client is published and before
		// the presence cadence starts, so the hello is the connection's only in-flight
		// exchange and the drain mode is decided from the relay's own advertisement
		// rather than from a blind probe's timeout (committee findings M1/M3).
		cl.SetMailboxIncarnation(a.core.State().RelayIncarnation)
		a.waitSupport.Store(a.negotiateWaitSupport(ctx, cl))
		a.setClient(cl)
		a.setConn(connOnline)
		rb.reset() // PB-NET-4: a successful connection un-does whatever backoff came before it
		a.onConnected(ctx, cl)
		// The presence cadence lives for exactly this connection's lifetime, so it can never
		// poll through a client the drain has finished with (bead agents-tracker-xtj). Its
		// timer is what keeps a relay round-trip off the render path: a screen reads the
		// cache MachinePresence exposes and never asks the relay itself.
		pctx, endPoll := context.WithCancel(ctx)
		go a.pollPresence(pctx, cl)
		go a.runPublicationPump(pctx, func() (sendCtx, error) {
			return a.resolveSend(func() (*relay.Client, error) { return cl, nil })
		})
		a.wakePublicationPump()
		a.drain(ctx, cl)
		endPoll()
		// The link is gone, so the phone can no longer ask what it last answered. Holding the
		// previous reading would leave the machine rendered "online" on evidence nothing can
		// refresh -- PB-APP-11's silence, one value over.
		if a.presence.forget() {
			a.events.emit(&Event{Kind: "presence", State: presenceUnknown})
		}
		a.setClient(nil)
		// PB-INPUT-2's FIRST enumerated severance event. A gateway restart kills the lease
		// while being unable to seal any notice about it -- the gateway is the thing that
		// died -- so the phone's own transport dropping is the ONLY signal that can exist,
		// and a disconnect must therefore SEVER rather than merely pause. Without this the
		// phone keeps reporting the pre-outage generation live and types against a lease the
		// new gateway does not hold. It also empties the coalescer, so bytes buffered when
		// the link went away resolve as undelivered instead of riding the reconnect.
		a.suspendInput("the connection to the machine was lost")
		// CloseNow, NOT the graceful Close (Opus round-4 F6): this teardown is an
		// abandonment on every path that reaches it -- the link already died (the
		// reconnect must redial, not say goodbye to a peer that is not listening) or
		// the generation was cancelled (Stop/backgrounding joins this loop from the
		// facade's serial command lane). The polite close costs its full five-second
		// handshake wait exactly when the pump has exited and the peer is silent --
		// the state a dead link leaves behind -- which delayed the redial, and a
		// background -> foreground resume, by up to ~5 s. The orderly goodbye
		// survives where it matters: App.Close's machines manager and push gateway
		// (process exit) and the pairing probe's finished exchange.
		_ = cl.CloseNow()
	}
	a.setClient(nil)
	a.setConn(connOffline)
}

// handsetSecurity is the transport policy EVERY session dial this handset makes runs
// under (PB-NET-2, ADR-007 B34/B37, ADR-016 W2/W3) -- POST-ADR-016, updated from this
// comment's own pre-ADR-016 text, which described a pinning-only world this build no
// longer ships. It starts from the platform's default trust-root source, cleartext
// refused, the decision re-asked on every redirect hop -- plus the loopback carve-out,
// which is honoured only inside a test binary and is therefore inert in the shipped .so.
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
// THE PIN IS SCOPED BY POLICY (ADR-016 W3's single rule, applied through
// effectiveStatePin below): consulted verbatim under pinned_spki (and under the unset
// legacy policy, which reads as pinned_spki for a pre-ADR-016 machine's payload), withheld
// entirely under webpki. State.RelaySPKIPin is still written by pin() from
// pairing.MachinePayload regardless of policy (B54's verbatim adoption), so a webpki phone
// carries a pin it never reads here -- deliberate (W4.4), not a bug, and the reason any
// diagnostic that prints the pin must print the policy beside it or it teaches the wrong
// lesson.
//
// THE PLATFORM DELEGATE is layered in by withPlatformTrust below, if SetRelayTrust ever
// installed one (ADR-016 W2): on Android this is what lets TrustRootsPlatformDelegate
// replace the pinning-only floor under webpki, reaching the Conscrypt APEX store Go
// itself cannot see. A pin present in relay.Security still outranks it (security.go's own
// precedence), so a pinned_spki handset's session dial is unaffected by whether a
// delegate is installed at all -- this only ever matters for a webpki handset.
//
// WHAT A USER SEES, in the states a handset can be in:
//
//   - PAIRED, PINNED_SPKI (or a legacy machine whose payload carries no policy field).
//     The pin replaces name and chain verification, so the operator's self-signed relay is
//     reachable and an impostor holding any other key is refused with
//     relay.ErrPinMismatch however well-issued its certificate is.
//   - PAIRED, WEBPKI (the default). No pin is applied. Chain trust comes from the
//     platform's own roots, plus the Android delegate above when one is installed; every
//     platform independently checks the leaf's hostname and validity window
//     (VerifyHostname, security.go) regardless of what the delegate answers. A handset
//     that reaches Android with NO delegate installed is not read as "nothing pinned" --
//     see relayTrustUnavailable (mobile/relaytrust.go), which is what keeps that app fault
//     from reaching the user as connRelayUntrusted's security accusation.
//   - PAIRED, PINNED_SPKI, BEFORE THE MACHINE PUBLISHED A PIN (or paired with a machine
//     that publishes none while itself holding pinned_spki). No pin is applied and, on a
//     handset with no platform delegate available, the wss:// dial is refused with
//     relay.ErrPinRequired rather than falling back to an unverified connection -- fail
//     closed, never dial unpinned.
//   - NEVER PAIRED. There is no relay URL to dial until a QR supplies one, and the pairing
//     dial runs under a DIFFERENT policy entirely -- see below.
//
// THE PAIRING DIAL DOES NOT RUN UNDER THIS FUNCTION, and that is what closes the bootstrap
// this comment used to record as unresolved ("residual 1.9"). mobile/pairing.go's
// pairingDial is its own policy: under webpki it is an ordinary VERIFIED dial (ADR-016
// W3) -- there is no pin to fetch, so the old deadlock does not exist for this policy at
// all. Against a private/self-signed destination (W3's amendment; pinned_spki's own
// population) B45's exemption still lets an unpinned pairing dial complete against a
// machine no CA has issued for, and B48's capture-then-compare then authenticates what
// msg2 actually delivers -- a Noise handshake the operator confirms by comparing a SAS,
// so a hostile terminator on that dial sees routing metadata and no pinned material
// either way. This function governs the SESSION dial only, reached after a pin (if any)
// is already durable.
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

// The relay's mailbox_wait support, as negotiated for the CURRENT connection
// (App.waitSupport). run() overwrites it on every successful dial with the verdict of
// that connection's r_hello capability exchange (negotiateWaitSupport), so the value is
// PER CONNECTION, never process-sticky: an old relay that is upgraded is re-evaluated for
// free on the next reconnect (bead agents-tracker-zphd), and one network stall can no
// longer pin a modern relay to the 500 ms poll for the process lifetime (committee
// finding M2).
//
//   - waitAdvertised: this connection's hello named "wait" among the agreed caps. The
//     drain parks the wait tail on that promise; the first wait the relay ANSWERS --
//     items or a clean empty page alike -- confirms it as waitSupported.
//   - waitSupported: a wait has been answered on this connection; a later black-holed
//     link is a transport failure, never a demotion.
//   - waitUnsupported: the hello omitted "wait" (or failed outright), or -- defense in
//     depth -- a relay that ADVERTISED the op never answered the first wait within
//     waitTimeout (see drainWait). The drain runs the compatibility poll for the
//     remainder of this connection.
const (
	waitAdvertised int32 = iota
	waitSupported
	waitUnsupported
)

// helloRequestCaps is every capability the phone's r_hello asks the relay for -- the
// CLIENT half of the negotiation whose server half is relay's serverCaps. The two sets
// live in different packages by design (the relay cannot know which of its caps a given
// client wants; the phone deliberately omits "rendezvous", which pairing speaks on a raw
// connection), so TestCommitteeR3_PhoneHelloCapsAreServedByTheRelay fences them against
// drift: every capability named here must be granted by the shipped relay, or the phone
// would silently run degraded against its OWN relay (committee round 3, Opus nit 6).
var helloRequestCaps = []string{"mailbox", "push", "presence", "wait", relay.CapabilityMailboxRecovery}

// negotiateWaitSupport derives one connection's wait verdict from its r_hello exchange.
// A refused or failed hello reads as unsupported rather than an error: the poll fallback
// works against every relay, and if the hello failed because the link is dying the drain
// discovers that within one bounded call anyway.
func (a *App) negotiateWaitSupport(ctx context.Context, cl *relay.Client) int32 {
	a.mailboxRecoverySupported.Store(false)
	_, caps, err := cl.Hello(ctx, relay.ProtocolVersion, helloRequestCaps)
	if err != nil {
		return waitUnsupported
	}
	for _, c := range caps {
		if c == relay.CapabilityMailboxRecovery {
			a.mailboxRecoverySupported.Store(true)
		}
	}
	for _, c := range caps {
		if c == "wait" {
			return waitAdvertised
		}
	}
	return waitUnsupported
}

// serverWaitCeiling is §6.0's "Server-side wait (long-poll) maximum | 25 s" (PB-NET-5),
// transcribed exactly as internal/remotegw does for the machine hop: it is the RELAY's
// ceiling, which is why it cannot be the phone's bound and is instead the number the
// phone's own bound has to clear.
const serverWaitCeiling = 25 * time.Second

// waitTimeout bounds ONE MailboxWait from the phone's side: the relay's own 25 s wait
// ceiling plus PB-NET-7's request budget for the frames that carry the wait out and its
// reply back -- composed from §6.0's terms rather than chosen, exactly like the gateway's
// defaultWaitTimeout, and for the same reason (a relay that honours the ceiling is never
// cut off early; one that answers nothing is ended one request budget later).
//
// It is a var, not a const, for one reason only: a test that needs the defense-in-depth
// demotion (an ADVERTISED wait that is never answered, see drainWait) would otherwise
// spend the full production bound proving a timeout fires. Nothing in production writes
// it.
var waitTimeout = serverWaitCeiling + relay.DefaultCallTimeout

// drain hands every mailbox item to the core, in order, until the connection dies: the
// bounded-MailboxWait live tail against every relay whose hello advertised the "wait"
// capability, and the legacy 500 ms poll ONLY against one that did not (playbook section
// 10: "an explicit compatibility fallback only for old relays" -- selected by the
// relay's own capability set, never by a config flag).
//
// The second dispatch below is drainWait's defense-in-depth demotion landing: a relay
// that ADVERTISED the wait and then answered nothing within waitTimeout is polled for
// the remainder of this connection -- still exactly one mailbox reader, handed off
// sequentially on one goroutine (PB-NET-6). If the deadline was really the link dying,
// the poll's first bounded MailboxRead discovers that within DefaultCallTimeout and the
// generation ends for an ordinary reconnect.
func (a *App) drain(ctx context.Context, cl *relay.Client) {
	if a.waitSupport.Load() != waitUnsupported {
		a.drainWait(ctx, cl)
	}
	if ctx.Err() == nil && a.waitSupport.Load() == waitUnsupported {
		a.drainPoll(ctx, cl)
	}
}

// drainWait is the live tail: park a bounded server-side wait at the durable cursor,
// deliver whatever page it returns, ack, park the next one. Reads are paced by the SAME
// transport.DrainPacer the gateway's command-IN loop uses, so §6.0's inbound drain budget
// binds this hop by construction rather than by a second transcription -- including
// against a poisoned mailbox tail, where the wait returns instantly forever and the pacer
// is what keeps the loop at the budget instead of at full speed (PB-SYNC-6; the poll
// loop's progress condition, restated for a drain with no sleep to fall into).
//
// AN OLD RELAY NEVER REACHES THIS FUNCTION. Its hello does not advertise the "wait"
// capability, so negotiateWaitSupport records waitUnsupported at connection setup and
// drain selects the poll outright -- no blind probe, no dark window, and no uncorrelated
// MsgError refusal queued where the next request/reply exchange would consume it as its
// own answer (committee finding H1; internal/remote/relay's pump additionally drops any
// such unsolicited frame as defense in depth).
//
// THE ONE DEMOTION LEFT is therefore itself defense in depth, against a relay that
// ADVERTISED the wait and then answered nothing: a deadline-shaped failure while no wait
// has ever been answered on THIS connection stores waitUnsupported and returns, and
// drain hands the same connection to the poll. That is safe precisely because the relay
// claimed the op -- a claiming relay replies via the correlated MsgWaitReply or not at
// all, so unlike the old blind probe there is no stray in-order error to desynchronise
// the stream. Every other failure -- the link dying under the wait (ErrConnClosed), the
// drain's own shutdown -- returns for an ordinary reconnect with the verdict unchanged,
// and once ANY wait has been answered the verdict is supported and a later black-holed
// link can no longer demote it. The verdict dies with the connection either way: the
// next generation's hello decides afresh (committee finding M2).
//
// ACKS RIDE transport.AckBatcher, OFF the delivery path, at most one metered op per
// second (MaxDrainAcksPerSec) -- the same batcher, and the same argument, as the
// gateway's command-IN loop. The synchronous shape this replaced acked once per
// delivered page, which at the specified 8 frames/s spends the relay's own OpsPerMin
// window (600) in about 40 s and gets the drain quota-refused by the relay it is
// draining (codex committee probe). Dropping an ack is safe: it is an optimisation, the
// durable receive high-water refuses any redelivery, and a cursor that failed to flush
// is re-recorded for the next tick.
func (a *App) drainWait(ctx context.Context, cl *relay.Client) {
	pacer := transport.NewDrainPacer()
	acks := transport.NewAckBatcher(func(actx context.Context, cursor uint64) error {
		if err := cl.MailboxAck(actx, cursor); err != nil {
			return err
		}
		a.mu.Lock()
		if cursor > a.ackSent {
			a.ackSent = cursor
		}
		a.mu.Unlock()
		return nil
	})
	a.setAckReset(acks.Reset)
	defer a.setAckReset(nil)
	actx, stopAcks := context.WithCancel(ctx)
	ackDone := make(chan struct{})
	go func() { defer close(ackDone); acks.Run(actx) }()
	defer func() { stopAcks(); <-ackDone }() // joined, so no ack outlives the drain that owns it
	for ctx.Err() == nil {
		if a.performMailboxDiscard(cl, acks) {
			continue
		}
		if pacer.Pace(ctx) != nil {
			return
		}
		// ONE deadline per wait, cancelled rather than deferred (a defer here would
		// accumulate one live timer per cycle for the connection's life).
		waitCtx, cancelWait := context.WithTimeout(ctx, waitTimeout)
		a.setWaitCancel(cancelWait)
		// The durable cursor is read AFTER the nudge target is registered, which is what
		// closes RewindRelayCursor's race with a parking wait: a rewind that lands before
		// this line is picked up here, and one that lands after it cancels the wait
		// (nudgeDrain), so neither ordering leaves the drain parked at the old cursor.
		cursor := a.core.State().RelayCursor
		ackGeneration := acks.Generation()
		items, _, err := cl.MailboxWait(waitCtx, cursor)
		a.setWaitCancel(nil)
		cancelWait()
		pacer.Observe(len(items))
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if errors.Is(err, relay.ErrMailboxCursorResetRequired) {
				if resetErr := a.rewindRelayCursor(); resetErr != nil {
					return
				}
				continue
			}
			if errors.Is(err, context.Canceled) {
				// The NUDGE, and it can be nothing else: the drain's own shutdown was
				// checked above, the deadline is a different sentinel, and nothing else
				// cancels waitCtx. RewindRelayCursor moved the durable cursor under this
				// parked wait; re-park at the value it holds now. The client already freed
				// the relay's wait slot on the way out (mailbox_wait_cancel travels the
				// same stream in order), so the immediate replacement is admitted.
				continue
			}
			if a.waitSupport.Load() == waitAdvertised && errors.Is(err, context.DeadlineExceeded) {
				// The defense-in-depth demotion the function comment describes: an
				// advertised wait went unanswered for the whole bound. Recorded for THIS
				// connection; drain falls through to the poll on the same one.
				a.waitSupport.Store(waitUnsupported)
			}
			return
		}
		if err := a.adoptRelayIncarnation(cl.MailboxIncarnation()); err != nil {
			return
		}
		a.waitSupport.Store(waitSupported)
		a.acceptMailboxPage(ctx, items)
		// Hand the page's committed high-water to the batcher and re-park immediately.
		// Recording is lock-order-safe (a.mu is not held) and does no I/O; the batcher's
		// own tick meters the flush. The silent-relay bound does not regress: the next
		// PROBE of a relay that answers nothing is the wait parked above, ended by
		// waitTimeout, which is the same bound the old inline-ack early exit defended.
		a.mu.Lock()
		pending, sent := a.ackPending, a.ackSent
		a.mu.Unlock()
		if pending > sent {
			if !acks.RecordGeneration(pending, ackGeneration) {
				// A manual Resync crossed this delivery. Its cursor belongs to the
				// retired mailbox generation, including the facade's coalescing copy.
				a.mu.Lock()
				a.ackPending = 0
				a.ackSent = 0
				a.mu.Unlock()
			}
		}
	}
}

// drainPoll polls the mailbox at pollInterval: the pre-wait drain, kept verbatim as the
// compatibility fallback an old relay's refusal selects (see drain).
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
// setWaitCancel publishes (or clears) the cancel function of the wait the drain is about
// to park, the seam nudgeDrain acts through. Guarded by a.mu like the rest of the App's
// cross-goroutine state.
func (a *App) setWaitCancel(fn context.CancelFunc) {
	a.mu.Lock()
	a.waitCancel = fn
	a.mu.Unlock()
}

func (a *App) setAckReset(fn func()) {
	a.mu.Lock()
	a.ackReset = fn
	a.mu.Unlock()
}

// nudgeDrain wakes a parked mailbox wait so the drain re-reads the durable relay cursor.
//
// IT EXISTS FOR EXACTLY ONE CALLER: Resync, right after RewindRelayCursor. The poll
// fallback re-reads State.RelayCursor every cycle, so a rewind reached it within one
// pollInterval by construction; the wait drain instead PARKS at the cursor it read, and a
// wait parked at a poisoned coordinate (ADR-007 B126) is woken by nothing -- no item is
// ever past it -- until the relay's 25 s ceiling answers it empty. The rewind is the one
// local state change that must interrupt the wait, and cancelling the wait's own context
// is the mechanism the client already defines for withdrawing one cleanly.
//
// With no wait parked it is a no-op, and correctly so: the rewind is already durable, and
// whichever read the drain makes next starts from it.
func (a *App) nudgeDrain() {
	a.mu.Lock()
	fn := a.waitCancel
	a.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// rewindRelayCursor moves only the relay-owned storage coordinate back to zero and retires
// every queued ack from its prior generation. The phonecore rewind preserves authenticated
// receive and grant replay high-waters, so re-served envelopes are refused/acked without
// being applied twice. Both ack counters must move with it: their numeric ordering has no
// meaning in the replacement mailbox and a larger stale ack could otherwise delete new items.
func (a *App) rewindRelayCursor() error {
	a.mu.Lock()
	resetAcks := a.ackReset
	cl := a.client
	a.mu.Unlock()
	// Reset is a barrier: any already-started old-coordinate ack completes while
	// the client still carries the retired incarnation. Clear it only afterwards.
	if resetAcks != nil {
		resetAcks()
	}
	if cl != nil {
		cl.ResetMailboxIncarnation()
	}
	// Write the durable rewind last. If an already-returned page was adopting the
	// retired incarnation concurrently, this write follows it and clears it; if its
	// request was still in flight, the client generation barrier refuses its response.
	if err := a.core.RewindRelayCursor(); err != nil {
		return err
	}
	a.mu.Lock()
	a.ackPending = 0
	a.ackSent = 0
	a.mu.Unlock()
	return nil
}

func (a *App) adoptRelayIncarnation(incarnation string) error {
	if incarnation == "" || a.core.State().RelayIncarnation == incarnation {
		return nil
	}
	return a.core.SetRelayIncarnation(incarnation)
}

func (a *App) drainPoll(ctx context.Context, cl *relay.Client) {
	for ctx.Err() == nil {
		if a.performMailboxDiscard(cl, nil) {
			continue
		}
		cursor := a.core.State().RelayCursor
		ackGeneration := cl.MailboxGeneration()
		items, err := cl.MailboxRead(ctx, cursor)
		if err != nil {
			if errors.Is(err, relay.ErrMailboxCursorResetRequired) {
				if resetErr := a.rewindRelayCursor(); resetErr == nil {
					continue
				}
			}
			return
		}
		if err := a.adoptRelayIncarnation(cl.MailboxIncarnation()); err != nil {
			return
		}
		a.acceptMailboxPage(ctx, items)
		if err := a.flushAcks(ctx, cl, ackGeneration); errors.Is(err, relay.ErrMailboxCursorResetRequired) {
			// A manual recovery crossed this already-returned poll page. Its coalesced
			// cursor belongs to the retired mailbox generation and must not leak into
			// the next page's ack.
			a.mu.Lock()
			a.ackPending = 0
			a.ackSent = 0
			a.mu.Unlock()
		}
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

type mailboxAccept func(context.Context, []byte, uint64) (phonecore.Receipt, error)

func blocksMailboxPage(receipt phonecore.Receipt, err error) bool {
	return err != nil && !receipt.Acked && receipt.Disposition == phonecore.ReceiptRetained
}

// acceptMailboxPage sweeps one relay page until the core says an errored item must be
// retained. Discardable parse/auth/decode failures deliberately do not stop the sweep: an
// untrusted relay could otherwise pin every valid frame behind one malformed head for the
// retention window (PB-SYNC-6). A retained stale-age/custody refusal or failed durable
// commit is the opposite case: accepting a later cursor would make an eventual coalesced ack
// compact the only recoverable copy of the refused item.
func acceptMailboxPage(ctx context.Context, items []relay.Item, accept mailboxAccept) {
	for _, it := range items {
		receipt, err := accept(ctx, it.Envelope, it.Cursor)
		if blocksMailboxPage(receipt, err) {
			break
		}
	}
}

// diagnoseMailboxPage applies the same page fence while distinguishing the one retained
// condition the explicit roster-refresh gesture is authorized to discard. Every other
// retained error is surfaced to the caller; treating it as a healthy diagnosis would let
// the later refresh/ack compact recoverable content the core deliberately kept.
func diagnoseMailboxPage(ctx context.Context, items []relay.Item, accept mailboxAccept) (staleAge bool, err error) {
	for _, it := range items {
		receipt, acceptErr := accept(ctx, it.Envelope, it.Cursor)
		if !blocksMailboxPage(receipt, acceptErr) {
			continue
		}
		if errors.Is(acceptErr, crypto.ErrStaleAge) {
			return true, nil
		}
		return false, acceptErr
	}
	return false, nil
}

func (a *App) acceptMailboxPage(ctx context.Context, items []relay.Item) {
	acceptMailboxPage(ctx, items, a.accept)
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
func (a *App) accept(ctx context.Context, raw []byte, cursor uint64) (phonecore.Receipt, error) {
	key := a.core.State().Keys.ContentKey
	receipt, err := a.core.Router().AcceptCommit(raw, cursor)
	if err != nil {
		return receipt, err
	}
	v, ok := viewFrame(key, raw)
	if !ok {
		return receipt, nil
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
	return receipt, nil
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
// THIS phone's own relay URL under an ordinary verified dial -- the platform delegate on
// Android once SetRelayTrust installed one (W2), the system trust store elsewhere -- on a
// connection separate from the live pinned one, which this never touches.
func (a *App) probeWebPKI(ctx context.Context, _ string) error {
	// REVIEW-ROUND FIX: bounded on ITS OWN deadline rather than inheriting whatever the
	// drain goroutine's ctx happens to carry -- this runs on that goroutine synchronously,
	// so an unbounded probe would block message draining for as long as the relay stays
	// silent. relay.DefaultDialTimeout is the same bound DialRawSecure's own connect phase
	// already applies; this makes it explicit rather than incidental.
	ctx, cancel := context.WithTimeout(ctx, relay.DefaultDialTimeout)
	defer cancel()
	sec := a.withPlatformTrust(relay.Security{AllowLoopbackCleartext: true})
	conn, err := relay.DialRawSecure(ctx, a.relayURL, sec)
	if err != nil {
		return err
	}
	return conn.Close()
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
