package swarmmobile

// App is the bound object the Android app holds for the life of the process: the resumed
// phone core (internal/phonecore), the relay connection that drains the machine -> phone
// mailbox and appends the phone -> machine one, and the read models the screens render.
//
// Everything here is a THIN wrapper. The durable coordinates, the replay guards, the
// send-seq reservation and the fail-closed rules live in the core; this file adds the
// bind-legal shape, the panic barrier and the lifecycle.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// pollInterval is the IDLE mailbox drain cadence while connected; a page that came back
// non-empty is followed immediately by the next read, so a backlog drains at full speed.
// It matches the gateway's own command-IN cadence for the same reason: the relay meters
// mailbox_read and mailbox_ack against a per-source ops budget (600/min by default), and a
// phone that polled at tens of hertz would spend that budget refusing its own reads.
const pollInterval = 500 * time.Millisecond

// journalLogSize bounds the in-memory journal read model. The DURABLE model is the
// core's; this is the page cache ReadJournal serves, so it is bounded rather than grown
// for the life of the process.
const journalLogSize = 1024

// §6.0's STATED resync bound (PB-SYNC-6): <= 1 per stream per 5 s, <= 12 per 5 min. These
// are numbers someone chose, not emergent ones, so they are transcribed rather than derived
// -- and they are ENFORCED here rather than documented, because the party who decides when a
// phone wants to resync is the relay the design treats as hostile.
const (
	resyncMinInterval = 5 * time.Second
	resyncWindow      = 5 * time.Minute
	resyncPerWindow   = 12
)

var (
	errClosed        = classed(ErrClassClosed, errors.New("swarmmobile: App is closed"))
	errNotRunning    = classed(ErrClassOffline, errors.New("swarmmobile: App is not running; call Start first"))
	errNoDestination = classed(ErrClassNotPaired, errors.New("swarmmobile: no machine relay destination in durable state; the phone knows who the machine is but not how to reach it -- pair first"))
	errUnreconciled  = classed(ErrClassUnreconciled, errors.New("swarmmobile: refusing a mutating op until the machine publishes its rollback authorities (PB-SYNC-7)"))

	// errNoContentKey is the phone WAITING for its epoch key, and its message says so.
	//
	// It used to read "call InstallContentKey after unlocking", which was advice nothing in
	// production could act on: InstallContentKey is called from Kotlin and Kotlin has no
	// source for the bytes -- the key arrives as a machine-sealed grant on the mailbox
	// (PB-KEY-10). The distinction from errGrantLost below is the whole of PB-KEY-3: this one
	// resolves itself when the grant lands, and that one never will.
	errNoContentKey = classed(ErrClassAwaitingKey, errors.New("swarmmobile: no epoch content key yet; the machine delivers it as a signed grant on the mailbox (PB-KEY-10) and none has arrived"))

	// errGrantLost is PB-KEY-3's terminal state at the facade. It WRAPS the core's sentinel
	// rather than restating it, so errors.Is keeps working for every Go caller while the
	// message carries the class Kotlin routes on.
	errGrantLost = classed(ErrClassGrantLost, fmt.Errorf(
		"swarmmobile: the phone holds no epoch key and its mailbox has nothing left that can deliver one; "+
			"only the machine can re-grant (PB-KEY-3): %w", phonecore.ErrGrantLost))
)

// App is the phone. Construct it with NewApp; the zero value is unusable and every
// method says so rather than panicking.
type App struct {
	core *phonecore.Core

	relayURL string
	// stateDir is the phone's private state directory. The core owns phone-state.json and
	// device.key inside it; the facade keeps PB-PAIR-4's pairing-attempt record beside them
	// (see mobile/pairing.go persist for why that one is not a State field).
	stateDir string

	events *dispatcher

	// coalesce paces keystrokes to §6.0's 8 frames/s (PB-INPUT-5). It has its own lock and
	// is not guarded by a.mu.
	coalesce *phonecore.InputCoalescer

	// presence is the machine's reachability as the relay last reported it, fed by the
	// per-connection poll and read O(1) by MachinePresence. Like coalesce it has its own
	// lock: the relay goroutine writes it while the UI thread reads.
	presence *presenceCache

	// bucketMu orders the phone -> machine MAILBOX BUCKET -- every envelope on it, command
	// and input alike. It is held across allocate-seal-append at each of the three append
	// sites (sendInputFrame, sealSignedCommand, unsignedCommand), for the reason
	// remotegw.CommandBridge.sealReply states one bucket over.
	//
	// THE SCOPE IS THE BUCKET AND NOTHING SMALLER, because the bucket has ONE Sequencer:
	// phonecore.Sequencer's own doc records that "Commands AND input frames draw from ONE
	// Sequencer per epoch because they share a single MailboxReceiver key". A lock covering
	// only the input frames leaves every command author allocating and appending unserialised
	// on the same stream, so the inversion survives -- which is exactly what happened while
	// this field was called inputMu and scoped to sendInputFrame alone.
	//
	// It is deliberately NOT a.mu -- a.mu guards the app's lifecycle and is taken on paths
	// that must not queue behind a relay append.
	bucketMu sync.Mutex

	// pairingWG counts the in-flight pairing handshakes started by startPairingJoin. It is
	// NOT guarded by a.mu -- Close waits on it with the lock released, because a handshake
	// winding down takes a.mu itself (pin -> rearmAfterPairing).
	pairingWG sync.WaitGroup

	mu     sync.Mutex
	closed bool
	// pairings are the live handshakes, so Close has something to cancel. A handshake's last
	// act is a write into stateDir, so an App that could not reach them could not honestly
	// report itself closed (see Close).
	pairings      map[*Pairing]struct{}
	drainTimer    *time.Timer
	skewed        bool // whether the clock is currently out of budget, so only a CHANGE raises an event
	sess          *session
	client        *relay.Client
	machineTarget string
	machinePub    ed25519.PublicKey
	connState     string
	reconciled    bool
	killSwitch    bool
	subscribed    bool
	ackPending    uint64
	ackSent       uint64
	journal       []JournalEntry
	needs         map[string]string
	inflight      map[string]bool
	// resyncAt are the resync attempt times PER STREAM, the state §6.0's rate bound is
	// enforced against. Per stream because a shared budget lets the two repairable channels
	// starve each other, and one shared-bucket gap stales both at once.
	resyncAt map[string][]time.Time
	// resyncAsked are the streams a repair has been REQUESTED for and not yet landed
	// (PB-APP-8's fourth state). It is a fact ORTHOGONAL to staleness, never a third value of
	// StreamState: a stream is stale-and-repairing or stale-and-idle, and one enum cannot
	// carry both. Collapsing them would also contradict the shipped S10 fence that requires
	// StreamState to still read "stale" at exactly this moment -- the guard standing between
	// this product and PB-SYNC-3's optimistic clear.
	resyncAsked map[string]bool
	// clockVerdict is PB-TIME-1's CURRENT verdict, "" when the clock is in budget. It exists
	// because the event plane alone cannot serve a screen that opens AFTER the measurement,
	// which on Android is most of them -- the process is killed and rebuilt constantly.
	clockVerdict string
	// pairingGraceUntil is how long the transport keeps retrying a relay that still answers
	// "revoked" after a pairing re-armed it (PB-STATE-10). Zero -- the normal case -- means
	// no grace at all, so a revocation stays terminal exactly as PB-APP-10 requires.
	pairingGraceUntil time.Time
}

// session is one Start..Stop generation.
type session struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// NewApp resumes the phone from its state directory (PB-STATE-1/-2: every durable
// coordinate, or a fail-closed error -- never a silent start from zero) and returns the
// bound App. It does not connect; Start does.
//
// custody is PB-KEY-9's seam and it is REQUIRED, not optional. There is deliberately no
// second constructor that omits it: an App with no custody would write the phone's key
// material at rest with nothing over it, and an optional parameter is a parameter somebody
// passes nil to. Refusing here costs one line at the one call site the Android app has, and
// it is the only shape under which "key material at rest is sealed" is a property of the
// product rather than of the test fixtures.
func NewApp(cfg *Config, custody KeyCustody) (app *App, err error) {
	defer barrier(&err)
	if cfg == nil {
		return nil, classed(ErrClassInvalidRequest, errors.New("swarmmobile: NewApp requires a Config"))
	}
	if custody == nil {
		return nil, classed(ErrClassInvalidRequest, errors.New(
			"swarmmobile: NewApp requires a KeyCustody (PB-KEY-9): the phone's key material at "+
				"rest is sealed under the Android Keystore, and there is no cleartext fallback"))
	}
	a := &App{
		relayURL:    cfg.RelayURL,
		stateDir:    cfg.StateDir,
		events:      newDispatcher(),
		coalesce:    phonecore.NewInputCoalescer(time.Now),
		presence:    newPresenceCache(time.Now),
		connState:   "offline",
		subscribed:  true,
		needs:       map[string]string{},
		inflight:    map[string]bool{},
		resyncAt:    map[string][]time.Time{},
		resyncAsked: map[string]bool{},
	}
	core, err := phonecore.Resume(phonecore.Config{
		Dir:     cfg.StateDir,
		Machine: cfg.MachineID,
		Ack:     &relayAcker{app: a},
		// PB-KEY-9, delivered. Both tiers are sealed under a key the Android Keystore
		// unwraps and this process never stores: the sealers hold the FETCHER, so every
		// seal and every open goes back to Keystore and the content tier's gate is the
		// unwrap refusing rather than a flag beside it. A separate sealer per tier is
		// what keeps PB-KEY-2's split real at rest -- one file cannot be gated two ways,
		// and one sealer over both would put the content key behind the wake tier's KEK,
		// which opens with no user present.
		WakeSealer:    custodySealer{tier: "wake", fetch: custody.WakeKEK},
		ContentSealer: custodySealer{tier: "content", fetch: custody.ContentKEK},
	})
	if err != nil {
		a.events.close()
		return nil, err
	}
	a.core = core

	st := core.State()
	a.setDestination(st.MachineRelayAuthPub)
	// PB-STATE-10: adoption of the machine's rollback authorities is DURABLE, so a
	// process death -- routine on Android -- does not re-arm the fail-closed refusal of
	// every mutating op with no phone-triggerable exit.
	a.reconciled = st.EpochID != 0 && st.ReconciledEpoch == st.EpochID
	return a, nil
}

// setDestination derives the machine's relay mailbox from its pinned relay-auth public
// key. It is the ONE coordinate that says how to reach the machine, so a pairing that
// re-pins it re-targets the live app rather than waiting for the next launch.
func (a *App) setDestination(pub []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(pub) != ed25519.PublicKeySize {
		a.machinePub, a.machineTarget = nil, ""
		return
	}
	a.machinePub = ed25519.PublicKey(append([]byte(nil), pub...))
	a.machineTarget = relay.RoutingID(a.machinePub)
}

// destination is the machine's relay mailbox and the key it was derived from.
func (a *App) destination() (string, ed25519.PublicKey) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.machineTarget, a.machinePub
}

// ready reports the core to operate on, or why the receiver cannot be used.
func (a *App) ready() (*phonecore.Core, error) {
	if a == nil || a.core == nil {
		return nil, errNoReceiver
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, errClosed
	}
	return a.core, nil
}

// ---- lifecycle ----------------------------------------------------------------

// Start connects to the relay and begins draining the machine -> phone mailbox. It is
// IDEMPOTENT: a second Start while running is a no-op.
func (a *App) Start() (err error) {
	defer barrier(&err)
	if _, err = a.ready(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sess != nil {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &session{cancel: cancel, done: make(chan struct{})}
	a.sess = s
	go func() {
		defer close(s.done)
		a.run(ctx)
	}()
	return nil
}

// pairingRevokeGrace is how long a transport that a PAIRING re-armed keeps retrying a relay
// still answering "revoked".
//
// It exists because the two ends of a recovery cannot be ordered. The phone learns the
// pairing succeeded the instant the machine's acceptance frame lands; the machine opens this
// device's relay route just after, over a connection of its own (`swarm remote pair`'s
// authorizeAtRelay, and again whenever the gateway boots). So the phone's first dial after a
// re-pair can legitimately arrive before the ban is lifted, and latching on it would mean the
// recovery only worked if the phone lost a race -- PB-STATE-10's brick, one layer down.
//
// It is bounded because a relay that is genuinely still refusing must eventually be believed,
// and generous because the losing side of the race can be a supervised process starting.
const pairingRevokeGrace = 30 * time.Second

// rearmAfterPairing restarts a transport generation that a revocation ended, and opens the
// window above. It is the phone half of PB-STATE-10 and it runs on exactly one event: a
// pairing that pinned a destination.
//
// WHY A RE-ARM IS OWED AT ALL. connRevoked returns from the loop rather than breaking, so the
// generation is OVER -- correctly, since nothing on-device can un-revoke itself. But a
// completed pairing is the owner having acted, which is the one thing that can make that
// verdict stale, and the App carries it across: the handset the user is holding shows REVOKED,
// they pair from that very screen, and without this they would go on seeing REVOKED until the
// Android process happened to be rebuilt. That is the same brick the requirement is named for,
// reached through the remedy.
//
// A generation still RUNNING is left alone -- the ordinary first pairing, where nothing was
// ever revoked -- so no grace is opened and a later revocation stays terminal.
func (a *App) rearmAfterPairing() {
	a.mu.Lock()
	dead := a.sess
	a.mu.Unlock()
	if dead == nil {
		return // never started, or stopped: Start owns that transition
	}
	select {
	case <-dead.done:
	default:
		return // still connected or retrying; nothing to re-arm
	}

	a.mu.Lock()
	if a.sess != dead || a.closed {
		a.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &session{cancel: cancel, done: make(chan struct{})}
	a.sess = s
	a.pairingGraceUntil = time.Now().Add(pairingRevokeGrace)
	a.mu.Unlock()

	dead.cancel() // release the finished generation's context
	go func() {
		defer close(s.done)
		a.run(ctx)
	}()
}

// pairingInFlight reports whether a handshake is running right now (ADR-007 B57/B58).
//
// A TRANSPORT VERDICT REACHED DURING A PAIRING MUST NOT BE TERMINAL, because the thing that
// would clear it is the pairing already running. The window is real and it is the ORDINARY
// path, not an edge: a handset pairing for the first time holds no relay pin, an unpinned dial
// on a pinning-only platform is refused before a packet, and the transport loop is retrying
// that refusal on the reconnect backoff for the whole time the user is comparing SAS symbols.
//
// withinPairingGrace cannot serve here and that is why this exists. It is opened by
// rearmAfterPairing, which runs at the END of pin() -- after the durable write -- so it is
// still closed during the window this covers. Worse, rearm polls the dead generation's channel
// ONCE and non-blockingly, so a loop that dies between that poll and its own deferred close is
// never restarted by anything: Start and rearmAfterPairing are the only two launch sites.
//
// Membership spans the write. startPairingJoin adds the handle before the goroutine starts and
// deletes it in that goroutine's defer, and join -> finish -> pin all run inside it, so this
// stays true until the pairing's durable effects have landed.
func (a *App) pairingInFlight() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.pairings) > 0
}

// withinPairingGrace reports whether a "revoked" from the relay is still explicable by a
// pairing this app has just completed.
func (a *App) withinPairingGrace() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return !a.pairingGraceUntil.IsZero() && time.Now().Before(a.pairingGraceUntil)
}

// Stop disconnects. It is IDEMPOTENT and safe to call concurrently with any other
// method: Android calls it from a lifecycle callback while a UI thread is mid-Peek.
func (a *App) Stop() (err error) {
	defer barrier(&err)
	if _, err = a.ready(); err != nil {
		return err
	}
	a.mu.Lock()
	s := a.sess
	a.sess = nil
	a.mu.Unlock()
	if s == nil {
		return nil
	}
	s.cancel()
	<-s.done
	a.suspendInput("the app stopped controlling this machine")
	return nil
}

// suspendInput is the whole-device input boundary: app backgrounding, and every Stop or
// Close. PB-INPUT-2 enumerates backgrounding as a severance event, so every lease goes --
// a lease the phone can no longer reach is one it must not assume it still holds -- and
// PB-INPUT-6 requires the buffer be emptied, so what is left is resolved as an explicit
// "delivery unknown / not sent" rather than dropped silently or held for a later replay.
func (a *App) suspendInput(reason string) {
	a.mu.Lock()
	t := a.drainTimer
	a.drainTimer = nil
	a.mu.Unlock()
	if t != nil {
		t.Stop()
	}
	for _, u := range a.coalesce.Abandon(reason) {
		a.emitUndelivered(u)
	}
	a.core.Leases().SeverAll(reason)
}

// emitUndelivered reports one resolved-as-undelivered unit of input on the event plane.
// PB-INPUT-1 requires the state be SURFACED, and most of these are produced asynchronously
// -- by a drain timer or by a link dropping -- where there is no call for an error to return
// from. UndeliveredInputs is the matching pull surface for a screen that opens afterwards.
func (a *App) emitUndelivered(u phonecore.Undelivered) {
	a.events.emit(&Event{
		Kind: "input", Stream: "input", SessionID: u.Session,
		State: "undelivered", Message: u.Reason, Cursor: int64(u.Bytes),
	})
}

// UndeliveredInputs is the ledger of input the phone accepted from the user and could not
// deliver (PB-INPUT-1). Input is live-only, so none of it will be retried: the entries exist
// so the user is TOLD what did not reach the machine instead of believing they typed it.
//
// It is a READ, not a drain: the state must survive the call that produced it, and a screen
// that opens after the failure must still see it.
func (a *App) UndeliveredInputs() (list *UndeliveredList, err error) {
	defer barrier(&err)
	if _, err = a.ready(); err != nil {
		return nil, err
	}
	entries := a.coalesce.Undelivered()
	out := &UndeliveredList{
		items:   make([]UndeliveredInput, 0, len(entries)),
		dropped: a.coalesce.UndeliveredDropped(),
	}
	for _, u := range entries {
		out.items = append(out.items, UndeliveredInput{
			SessionID: u.Session,
			Bytes:     u.Bytes,
			Reason:    u.Reason,
			AtMillis:  u.At.UnixMilli(),
		})
	}
	return out, nil
}

// ClearUndeliveredInputs is the acknowledgement half of the ledger (PB-INPUT-1).
//
// Without it the notice the user has already read stays on screen for the life of the
// process, and the next genuine loss arrives underneath a wall of ones they dealt with an
// hour ago. It is a separate verb rather than a draining UndeliveredInputs because the two
// callers want opposite things: a screen that OPENS must see the backlog, and a user who
// DISMISSES it says so once, for every screen.
//
// It does not disable the ledger. A clear that stopped recording would satisfy the same
// assertion and silently drop every future loss -- PB-INPUT-1's forbidden silent drop,
// reached through its own remedy.
func (a *App) ClearUndeliveredInputs() (err error) {
	defer barrier(&err)
	if _, err = a.ready(); err != nil {
		return err
	}
	a.coalesce.ClearUndelivered()
	return nil
}

// IsRunning reports whether the relay drain is LIVE: a session has been started and its
// goroutine has not returned.
//
// The two are not the same thing, and the gap is a TERMINAL state, not a race. run() returns
// outright on crypto.ErrKeyInvalidated -- the relay-auth key is destroyed, every retry is a
// round trip spent proving that again, and the state it leaves behind is "pair again". Stop
// is what clears a.sess, and nothing calls Stop on that path: the drain simply ends. Reading
// the field alone therefore reports a live drain forever afterwards, so a UI gating its
// re-pair affordance on !IsRunning() would never show it -- on exactly the handset that
// cannot do anything else.
//
// Start stays a no-op while a session object exists, terminal or not, and deliberately: the
// state this reports false in is reached only when the relay-auth key is destroyed, so
// restarting the drain would spend another handshake proving that again. Re-pairing is the
// remedy, which is why it is the affordance this answer gates.
func (a *App) IsRunning() (running bool, err error) {
	defer barrier(&err)
	if _, err = a.ready(); err != nil {
		return false, err
	}
	a.mu.Lock()
	s := a.sess
	a.mu.Unlock()
	if s == nil {
		return false, nil
	}
	select {
	case <-s.done:
		return false, nil
	default:
		return true, nil
	}
}

// Close stops the app and releases the durable state handle for good. It is idempotent;
// every other method on a closed App fails.
func (a *App) Close() (err error) {
	defer barrier(&err)
	if a == nil || a.core == nil {
		return errNoReceiver
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	s := a.sess
	a.sess = nil
	a.closed = true
	live := make([]*Pairing, 0, len(a.pairings))
	for p := range a.pairings {
		live = append(live, p)
	}
	a.mu.Unlock()

	// AN IN-FLIGHT HANDSHAKE IS A WRITER ON stateDir, and Close used to leave it running: it
	// joined the relay-drain session and nothing else. The handshake's last act is persist()
	// -- PB-PAIR-4's record -- and on the success path pin() -> Core.Save, which rewrites the
	// sealed state blob. So an App could report itself closed while a goroutine it no longer
	// tracked went on rewriting the durable state the caller believed was released.
	//
	// ON A HANDSET THAT IS A TORN WRITE. Close is what the Android lifecycle calls before the
	// process is taken away, so "still writing after Close" and "killed mid-write" name the
	// same instant; the fact that Save is itself atomic only narrows the window to the app's
	// own state, it does not close it. The suite saw the milder half of the same fact --
	// t.TempDir's RemoveAll racing a file being recreated -- and that symptom is what
	// TestPBPAIR5_CloseWaitsForTheHandshakeItTearsDown fences.
	//
	// Cancel first, then wait: cancelHandshake unblocks the SAS gate and the relay read, and
	// each goroutine then runs finish() -- which settles the state and completes its last
	// write -- BEFORE Wait returns. The wait is also why events.close() below is reachable
	// safely: a handshake still running could otherwise publish into a closed dispatcher.
	// abandon, not cancelHandshake: the teardown must not be recorded as the user cancelling,
	// which would clear PB-PAIR-4's durable record of an attempt the machine may have
	// committed (Pairing.abandon).
	for _, p := range live {
		p.abandon()
	}
	if s != nil {
		s.cancel()
		<-s.done
	}
	a.pairingWG.Wait()
	a.suspendInput("the app closed")
	a.events.close()
	return nil
}

// startPairingJoin runs p's handshake on a goroutine the App OWNS, so Close can cancel it and
// wait for it. It reports false when the app is already closed, in which case nothing is
// started -- a handshake spawned after Close would be a writer nothing could ever join.
func (a *App) startPairingJoin(p *Pairing, base context.Context) bool {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return false
	}
	if a.pairings == nil {
		a.pairings = make(map[*Pairing]struct{})
	}
	a.pairings[p] = struct{}{}
	a.pairingWG.Add(1)
	a.mu.Unlock()

	go func() {
		defer func() {
			a.mu.Lock()
			delete(a.pairings, p)
			a.mu.Unlock()
			a.pairingWG.Done()
		}()
		p.join(base)
	}()
	return true
}

// ConnectionState is offline / connecting / online / reconnecting (PB-APP-8).
//
// IT IS NOT A READ OF THE SOCKET, and the difference is ADR-007 B42. A phone whose inbound
// frames are all being refused past PB-TIME-2's bound -- a clock running fast, or a relay that
// withheld a page for eleven minutes and then released it -- has a live websocket and receives
// nothing: no journal, no terminal, no lease confirmation, no op outcome. Answering "online"
// there renders "Connected to your machine." with no banner, which reported the destruction of
// the phone's whole inbound plane AS HEALTH.
//
// It reports connOffline rather than a new state on purpose. "Not connected to your machine"
// is TRUE of a phone nothing can reach through -- every reply rides the plane being refused, so
// no op it sends can ever be answered -- and it is a state the screens already render, whereas
// a new wire literal is one dev.swarm.phone.keys.ConnectionState.of would refuse with error(),
// i.e. a crash on the very handset this condition describes. The distinct, actionable diagnosis
// belongs to PB-TIME-1's clock verdict, which is a separate surface.
func (a *App) ConnectionState() (state string, err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	state = a.connState
	a.mu.Unlock()
	if state == connOnline && core.Router().InboundAgeRefused() {
		return connOffline, nil
	}
	return state, nil
}

// StateSummary is what the restart actually restored (PB-STATE-1/-2).
//
// Restored is derived from MachineRelayAuthPub rather than from State.Machine. The machine
// endpoint id is now stamped by phonecore.OpenStore from the CONFIGURED id (it has to be: it is
// the filter the durable blob is loaded against, and an empty one made the phone discard its
// own state on the next launch), so it is present on a phone that has never paired and says
// nothing about what was restored. MachineRelayAuthPub is pinned only by a pairing -- it is the
// one coordinate that says how to REACH the machine, and phonecore.State's own doc records that
// a phone missing it holds a valid content key, a valid send-seq and no destination with
// nothing failing loudly. That is exactly the condition this flag exists to surface.
//
// PAIRED IS THE FACT THE PRESENTATION GATE TURNS ON, and it is a field because inferring it from
// the machine name was agents-tracker-d0b8. `PhoneSurface.renderReady` asks
// `PairOnlyScreen.presentationOf` whether this handset is shown the app at all, and it asked
// `Machine != ""` -- reasonable on its face, since a completed pairing clears the attempt record
// and the pinned machine is the trace it leaves. But the machine endpoint id is a COORDINATE:
// phonecore filters the durable blob on it and every mutating verb signs over it, so nothing
// clears it, and the revoke behind "Replace this computer" -- which deregisters the device,
// rotates the epoch, severs the gateway and destroys both key tiers -- left it exactly where it
// was. The gate answered "show the app" for a handset with no registration, and the pairing entry
// point lives on the settings screen inside that app. There was no way back short of clearing the
// app's data.
//
// IT IS THE OLD CRITERION PLUS THE MISSING ONE, deliberately, rather than a better criterion. A
// pairing is still what makes Machine non-empty on a handset (Config.MachineID is "" on a phone,
// so nothing else can), and phonecore.State.Disowned is the durable record of the owner ending it.
// Deriving Paired from something else -- Restored, or key material -- would change which phones
// read as paired TODAY, and that is not this defect's subject: the phones that work must go on
// working, and the revoked one must stop.
func (a *App) StateSummary() (sum *StateSummary, err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return nil, err
	}
	st := core.State()
	a.mu.Lock()
	reconciled, pending := a.reconciled, len(a.inflight)
	a.mu.Unlock()
	return &StateSummary{
		Machine:     st.Machine,
		EpochID:     int64(st.EpochID),
		SendSeq:     int64(st.SendSeq[st.EpochID]),
		RelayCursor: int64(st.RelayCursor),
		PendingOps:  pending,
		Restored:    len(st.MachineRelayAuthPub) == ed25519.PublicKeySize,
		Reconciled:  reconciled,
		Paired:      st.Machine != "" && !st.Disowned,
	}, nil
}

// ---- key custody (ADR-007 B8 / PB-KEY-1, PB-KEY-7) -----------------------------

// InstallWakeKey installs the epoch WAKE key the Java side unwrapped with its
// authenticated-Keystore KEK. Inbound only; the caller zeroizes its copy on return.
func (a *App) InstallWakeKey(key []byte) (err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return err
	}
	if len(key) != len(crypto.WakeKey{}) {
		return classed(ErrClassInvalidRequest,
			fmt.Errorf("swarmmobile: wake key must be %d bytes", len(crypto.WakeKey{})))
	}
	return core.Mutate(func(st *phonecore.State) { copy(st.Keys.WakeKey[:], key) })
}

// InstallContentKey installs the epoch CONTENT key.
//
// IT IS NOT PB-KEY-7'S RECOVERY PATH, and the claim that it was is the false statement
// ADR-007 B35 names in this file. Kotlin has no source for these bytes: PB-KEY-10 moved epoch
// key delivery entirely into Go (App.Start -> drain -> AcceptCommit -> installGrant, opened
// under the recipient key in device.key), and every reference to the Kotlin-side epoch-key blob
// is under src/test/. A screen lock is recovered by UnlockContent -- a fresh Keystore unwrap of
// the blob the lock deliberately leaves at rest -- and by nothing else.
func (a *App) InstallContentKey(key []byte) (err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return err
	}
	if len(key) != len(crypto.ContentKey{}) {
		return classed(ErrClassInvalidRequest,
			fmt.Errorf("swarmmobile: content key must be %d bytes", len(crypto.ContentKey{})))
	}
	return core.Mutate(func(st *phonecore.State) { copy(st.Keys.ContentKey[:], key) })
}

// PurgeKeys is PB-KEY-7's REVOKE/UNPAIR purge, and it must never be reached from an exported
// component (PB-SEC-11).
//
// ITS TRIGGER MOVED AND THIS COMMENT DID NOT, which is worth recording because the stale version
// described the opposite verb. It used to read "PB-KEY-7's lock purge ... the verb the Android
// lifecycle layer calls on every InvalidationEvent -- backgrounding, screen off, auth expiry",
// and it listed as deliberate non-behaviour the two things this verb now does. ADR-007 B133
// removed every phone-side user authentication mechanism, so there is no lock event to call it:
// dev.swarm.phone.SwarmApplication records that nothing observes the screen lock any more, and
// the only caller left is the revoke behind Settings' "Replace this computer".
//
// WHAT IT DOES. Zeroize BOTH tier keys, unbind MailboxRouter from the content key, drop the
// decrypted session/snapshot/reply caches from memory AND destroy their sealed containers at
// rest, drop the push token, and record the unpair durably (phonecore.State.Disowned) so the
// presentation gate stops showing the app to a handset with no registration.
//
// IT IS NOT RECOVERABLE IN PLACE, which is the inversion B133 makes rather than an oversight.
// While the trigger was a screen lock, sparing the sealed content key and the whole wake tier was
// correct: PB-KEY-10 leaves nothing on the handset that could re-derive those bytes and the grant
// watermark refuses the machine's re-appended grant as a replay, so destroying them made the
// first lock a permanent brick (ADR-007 B35/B36). For a revoke the same fact reads the other way
// -- the pairing is the thing being destroyed, a revoked handset must not stay wakeable, and
// re-pairing mints a fresh epoch and fresh keys anyway. UnlockContent opens nothing afterwards.
//
// An error means the material AT REST survived (a full disk, a read-only data directory). The
// memory half has happened regardless: it cannot fail, PB-KEY-7 lists it first, and gating it
// behind a write that can fail left the key live and bound on a device the owner has revoked.
func (a *App) PurgeKeys() (err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return err
	}
	// An EXPLICIT purge, not a Save whose keys happen to be zero: a process that came up on a
	// push holds zeros for a content key it merely could not read and Saves constantly, so
	// custody cannot tell the two apart from the bytes and would keep the sealed blob
	// (phonecore.Store.PurgeKeys). It is also what records the unpair, which no Save can.
	//
	// The facade's own decrypted caches go whether or not the durable half succeeded, for the
	// same reason the core purges its memory unconditionally: clearing them cannot fail, and
	// returning first left the app rendering decrypted session content on a handset its owner
	// has just revoked. The error still reaches the caller -- the blobs at rest survived.
	err = core.PurgeKeys()
	a.mu.Lock()
	a.journal = nil
	a.needs = map[string]string{}
	a.mu.Unlock()
	return err
}

// UnlockContent is PB-KEY-7's "require a fresh unwrap before restoring content", as a verb.
//
// It re-opens the content tier through the Keystore-backed KEK, which is the gate: it succeeds
// only while the device has authenticated inside the window that KEK was provisioned with
// (PB-SEC-2's 60 seconds), and otherwise refuses with the reauth verdict PB-APP-9's table
// already routes to "authenticate again". There is no flag beside it and no other way back
// from PurgeKeys.
//
// IT IS SAFE TO CALL AT ANY TIME. An unlocked core answers without consulting Keystore, and a
// phone that has never been granted a key has no sealed blob to open, so this is not a prompt
// trigger for a device that is merely waiting for its first grant (PB-APP-10's transient
// waiting state, which is not this one).
func (a *App) UnlockContent() (err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return err
	}
	return core.UnsealContent()
}

// ---- read models ---------------------------------------------------------------

// Roster is the journal-derived session model (PB-APP-2), as a handle.
func (a *App) Roster() (list *SessionList, err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return nil, err
	}
	cached := core.Router().Sessions().List()
	out := &SessionList{
		items: make([]Session, 0, len(cached)),
		// PB-APP-8. The roster is rendered from the JOURNAL stream, so a roster read while
		// that stream has an unrepaired hole may be missing a session, an exit or a
		// needs_input. The flag rides the handle because the triage inbox is the first screen
		// the user opens and the one they act on.
		stale: a.streamStale(phonecore.StreamJournal),
	}
	for _, cs := range cached {
		out.items = append(out.items, a.session(cs))
	}
	sortSessions(out.items)
	return out, nil
}

// Session is one roster row. Group is verbatim from the wire.
func (a *App) Session(id string) (s *Session, err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return nil, err
	}
	cs, ok := core.Router().Sessions().Get(id)
	if !ok {
		return nil, classed(ErrClassNotFound, fmt.Errorf("swarmmobile: no session %q in the roster", id))
	}
	out := a.session(cs)
	return &out, nil
}

func (a *App) session(cs phonecore.CachedSession) Session {
	a.mu.Lock()
	need := a.needs[cs.SessionID]
	a.mu.Unlock()
	title := cs.SessionID
	if _, local, ok := strings.Cut(cs.SessionID, "/"); ok {
		title = local
	}
	return Session{
		ID:      cs.SessionID,
		Title:   title,
		Group:   string(cs.Group),
		Agent:   cs.Agent,
		Need:    need,
		Present: cs.Present,
	}
}

// Peek renders the daemon-sanitized terminal snapshot for a session (PB-APP-4). There is
// no VT emulator on the device: what the daemon rendered is what is shown.
func (a *App) Peek(session string) (snap *Snapshot, err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return nil, err
	}
	s, ok := core.Router().Snapshots().Get(session)
	if !ok {
		return nil, classed(ErrClassNotFound, fmt.Errorf("swarmmobile: no terminal snapshot for %q", session))
	}
	return &Snapshot{
		SessionID: s.Session,
		Text:      strings.Join(s.Lines, "\n"),
		Cols:      s.Cols,
		Rows:      s.Rows,
		Stale:     a.streamStale("terminal"),
	}, nil
}

// ReadJournal returns the journal entries after cursor from, at most limit of them.
func (a *App) ReadJournal(from int64, limit int) (page *JournalPage, err error) {
	defer barrier(&err)
	if _, err = a.ready(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = journalLogSize
	}
	// Read BEFORE a.mu is taken: streamStale takes it too.
	stale := a.streamStale(phonecore.StreamJournal)
	a.mu.Lock()
	defer a.mu.Unlock()
	out := &JournalPage{next: from, stale: stale}
	for _, e := range a.journal {
		if e.Cursor <= from {
			continue
		}
		out.items = append(out.items, e)
		if e.Cursor > out.next {
			out.next = e.Cursor
		}
		if len(out.items) >= limit {
			break
		}
	}
	return out, nil
}

// SubscribeJournal resumes journal event delivery to the EventListener.
func (a *App) SubscribeJournal() (err error) {
	defer barrier(&err)
	if _, err = a.ready(); err != nil {
		return err
	}
	a.mu.Lock()
	a.subscribed = true
	a.mu.Unlock()
	return nil
}

// UnsubscribeJournal stops journal event delivery, so a backgrounded screen is not fed
// events it will never render. The durable model keeps advancing regardless.
func (a *App) UnsubscribeJournal() (err error) {
	defer barrier(&err)
	if _, err = a.ready(); err != nil {
		return err
	}
	a.mu.Lock()
	a.subscribed = false
	a.mu.Unlock()
	return nil
}

// SetEventListener installs the app's callback sink. Events arrive on a Go goroutine;
// the UI must marshal them onto its own thread.
func (a *App) SetEventListener(l EventListener) (err error) {
	defer barrier(&err)
	if _, err = a.ready(); err != nil {
		return err
	}
	a.events.setListener(l)
	return nil
}

// Presence is the machine's coarse reachability as the relay sees it (PB-APP-5).
func (a *App) Presence() (state string, err error) {
	defer barrier(&err)
	if _, err = a.ready(); err != nil {
		return "", err
	}
	cl, err := a.conn()
	if err != nil {
		return "", err
	}
	target, _ := a.destination()
	if target == "" {
		return "", errNoDestination
	}
	info, err := cl.Presence(context.Background(), target)
	if err != nil {
		return "", err
	}
	return string(info.State), nil
}

// StreamState is PER-STREAM staleness (PB-APP-8 / PB-SYNC-1): "live" or "stale". It is
// per stream on purpose -- a global flag would present a stale view as live whenever any
// other stream happened to be healthy.
func (a *App) StreamState(stream string) (state string, err error) {
	defer barrier(&err)
	if _, err = a.ready(); err != nil {
		return "", err
	}
	if a.streamStale(stream) {
		return "stale", nil
	}
	return "live", nil
}

// MachineFreshness is PB-APP-11: how long it has been since the machine itself last spoke,
// and whether anything the phone holds may still be shown as current.
//
// IT IS THE STATE THE PHONE DEGRADES TO INSTEAD OF A SUCCESSFUL EMPTY POLL. The relay is the
// declared adversary (ADR-007 D9) and its cheapest attack is not a forgery: it withholds the
// newest frames and keeps answering. No gap forms, so no stream is marked stale; the poll
// succeeds, so ConnectionState goes on reading "online"; and Presence() asks that same relay
// whether the machine is alive. Section 6.0's freshness budget is the only thing in the
// system that can see it, because it measures the machine's own AAD-covered stamp -- which a
// relay can make older by holding a frame, and can never make newer.
//
// PRESENCE IS NOT THIS, and a screen may not substitute one for the other: Presence is the
// relay's opinion, and this is the phone's evidence.
func (a *App) MachineFreshness() (f *Freshness, err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return nil, err
	}
	last := core.LastHeard()
	out := &Freshness{Silent: core.MachineSilentAt(time.Now())}
	if !last.IsZero() {
		out.LastHeardUnixMs = last.UnixMilli()
	}
	return out, nil
}

// Resync ASKS THE MACHINE to repair a stream. It does NOT clear the stale mark: PB-SYNC-3
// clears a channel only when that channel's own repair lands, committed atomically with the
// matching transport watermark, so clearing on the request would turn "resync" into "forget"
// and show the user a hole as live -- the one thing PB-APP-8 forbids.
//
// Only the JOURNAL has a stream-scoped repair verb, and that is the shape of the four
// channels rather than an omission (PB-SYNC-2): the journal is repaired by an atomic
// roster+events reseed, which is what this requests; the TERMINAL by a fresh full
// server-rendered snapshot, which an open peek already publishes on its own and which is
// requested per SESSION (TerminalWatch) because a grid belongs to one; a REPLY by that op's
// durable outcome, or it stays unresolved; and the GRANT by a machine-side re-grant the
// phone cannot ask for at all. The budget below is spent for every stream regardless, so a
// caller cannot use the three verbless channels to evade it.
//
// It is RATE BOUNDED at §6.0's stated numbers. The relay is the declared adversary and it
// controls exactly the input that triggers a resync -- it can withhold one frame whenever it
// likes -- so an unbounded resync is an amplifier with the lever in the adversary's hand,
// and the relay's own quota is not the backstop: mailbox_read and mailbox_ack meter against
// the same per-source budget the repair itself has to arrive on, so a resync storm spends
// the budget the phone needs to RECEIVE the answer.
func (a *App) Resync(stream string) (err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return err
	}
	switch stream {
	case phonecore.StreamJournal, phonecore.StreamTerminal, phonecore.StreamReply, phonecore.StreamGrant:
	default:
		// Fail closed on a name that is not a repair channel. Admitting it would spend a
		// budget, report success, and repair a stream that does not exist -- and a caller
		// that mistyped one of the four would see exactly what a working resync looks like.
		return classed(ErrClassInvalidRequest,
			fmt.Errorf("swarmmobile: %q is not a repair channel (%s, %s, %s, %s)", stream,
				phonecore.StreamJournal, phonecore.StreamTerminal, phonecore.StreamReply, phonecore.StreamGrant))
	}
	if err = a.resyncBudget(stream, time.Now()); err != nil {
		return err
	}
	// RESET THE READ POSITION, LOCALLY, BEFORE ANY ROUND TRIP. This verb used to mean "ask the
	// machine for a reseed"; it now means "reset my read position AND ask for a reseed", and
	// the local half has to come first because the reseed is delivered THROUGH the read
	// position. A relay that rewrites one item's storage cursor past every real one ends all
	// machine->phone delivery permanently and durably (ADR-007 B126), and a repair that only
	// sent a request would be answered into the same hole -- which is measured: the poisoned
	// phone's Resync returned nil and changed nothing.
	//
	// It rides EVERY admitted resync, not only the journal's, because the read cursor is the
	// TRANSPORT's and all four channels share it -- a user whose terminal has gone silent must
	// not have to guess which button repairs the connection. It is inside the budget for the
	// same reason the rest of the verb is: the work is one re-drain of a depth-capped mailbox,
	// and the seq high-water refuses every frame in it that was already applied.
	if err = core.RewindRelayCursor(); err != nil {
		return err
	}
	// ADMITTED, so the repair is in flight from this instant (PB-APP-8's fourth state). It is
	// marked BEFORE the request is sealed rather than after: the seal can take a relay round
	// trip, and a user who pressed a button and saw nothing change for a second is the exact
	// experience this state exists to remove. A seal that then fails leaves the flag set until
	// the stream repairs or a later resync succeeds, which is honest -- the request was made.
	a.mu.Lock()
	a.resyncAsked[stream] = true
	a.mu.Unlock()
	if stream != phonecore.StreamJournal {
		return nil
	}
	_, err = a.unsignedResync(core.Router().Sessions().Cursor())
	return err
}

// ResyncPending is PB-APP-8's fourth state: a repair has been asked for and has not landed.
//
// IT IS NOT A THIRD VALUE OF StreamState, and that is the load-bearing part. Stale and
// repairing are ORTHOGONAL -- a stream is stale-and-repairing or stale-and-idle -- so one
// enum cannot carry both, and expressing "repairing" by clearing the stale mark is exactly
// PB-SYNC-3's optimistic clear: it shows the user a known hole as live. The shipped
// TestS10_ResyncDoesNotClearStalenessBeforeTheRepairLands fences that, and no screen state
// may be bought by weakening it.
//
// It is DERIVED rather than latched: the flag is only ever true while that channel's own
// stale mark is still up, so the repair landing clears it in the same durable transaction
// that clears the staleness. A spinner that never stops is the same object as no spinner.
func (a *App) ResyncPending(stream string) (pending bool, err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return false, err
	}
	a.mu.Lock()
	asked := a.resyncAsked[stream]
	a.mu.Unlock()
	if !asked {
		return false, nil
	}
	if !core.StreamStale(stream) {
		a.mu.Lock()
		delete(a.resyncAsked, stream)
		a.mu.Unlock()
		return false, nil
	}
	return true, nil
}

// ClockVerdict is the PULL surface for PB-TIME-1's verdict: "" when the clock is in budget,
// and the user-legible reason when it is not.
//
// It exists because the event plane alone cannot serve a screen that opens AFTER the
// measurement -- and on Android that is most of them, since the process is killed and rebuilt
// constantly. UndeliveredInputs already has exactly this shape, for exactly this reason.
//
// The verdict is NOT LATCHED. Correcting the clock clears it, and reportSkew raises an event
// on that transition too, so a screen that rendered the first event has something to clear it
// with. Without both halves a user who did precisely what they were told would go on being
// told to fix a clock that is now right -- and the daemon's refusal of a skewed command reads
// "not authorized", which sends them to re-pair instead.
func (a *App) ClockVerdict() (verdict string, err error) {
	defer barrier(&err)
	if _, err = a.ready(); err != nil {
		return "", err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.clockVerdict, nil
}

// resyncBudget admits one resync attempt for a stream under §6.0's stated bound -- <= 1 per
// stream per 5 s and <= 12 per 5 min -- or refuses it.
//
// THE BUDGET IS PER STREAM, not shared. A shared one lets the two repairable channels starve
// each other, and a single gap in the shared seq bucket stales BOTH of them at once
// (PB-SYNC-1), so the very first thing a user would do is spend the whole budget on one of
// the two holes they have.
func (a *App) resyncBudget(stream string, now time.Time) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	at := a.resyncAt[stream]
	if len(at) > 0 && now.Sub(at[len(at)-1]) < resyncMinInterval {
		return classed(ErrClassRateLimited,
			fmt.Errorf("swarmmobile: %s was resynced less than %s ago (§6.0 bounds the resync rate at "+
				"1 per stream per %s); the repair clears the stale mark when it lands, not when it is asked for",
				stream, resyncMinInterval, resyncMinInterval))
	}
	kept := at[:0]
	for _, t := range at {
		if now.Sub(t) < resyncWindow {
			kept = append(kept, t)
		}
	}
	if len(kept) >= resyncPerWindow {
		return classed(ErrClassRateLimited,
			fmt.Errorf("swarmmobile: %s has been resynced %d times in the last %s (§6.0's cap); "+
				"a channel that will not repair is not repaired by asking harder",
				stream, len(kept), resyncWindow))
	}
	a.resyncAt[stream] = append(kept, now)
	return nil
}

// streamStale is the app-facing staleness of one repair channel. The per-channel flags are
// the CORE's, and durable: a facade-local mirror would come back clear on every Android
// process death and present a hole the phone already knows about as live.
//
// The unreconciled clause is separate and additive: with no authority in hand the phone
// cannot verify any bucket of this epoch, so every channel is reported stale until a
// reconcile record lands (PB-STATE-4).
//
// THE SILENCE CLAUSE IS THE THIRD, AND IT IS THE ONE NO GAP CAN EXPRESS (PB-APP-11). The
// other two answer "is there a hole in what arrived"; a relay that simply stops delivering
// leaves no hole, answers every poll, and is itself the source of the only other liveness
// signal the phone has. So the age of the newest authenticated machine timestamp bounds every
// channel at once -- unlike a gap, which belongs to one bucket -- because all four are
// rendered from content that came over the same withheld link.
//
// It is applied HERE rather than at each read model deliberately: this one function is what
// StreamState, SessionList.Stale, JournalPage.Stale and Snapshot.Stale all resolve to, and a
// screen that has to remember to ask a second question beside every read is one that will
// forget once, silently.
func (a *App) streamStale(stream string) bool {
	a.mu.Lock()
	reconciled := a.reconciled
	a.mu.Unlock()
	return !reconciled || a.core.MachineSilentAt(time.Now()) || a.core.StreamStale(stream)
}

// ---- outcomes ------------------------------------------------------------------

// Outcome claims the verdict for one operation BY OPERATION ID. The unkeyed FIFO drain
// is deliberately not used: rebuilt from an unpruned outcome map, it can hand the app a
// stale reply belonging to another op, and the wrong verdict for a mutating op is worse
// than none (PB-SYNC-2).
func (a *App) Outcome(operationID string) (out *Outcome, err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return nil, err
	}
	if operationID == "" {
		return nil, classed(ErrClassInvalidRequest, errors.New("swarmmobile: Outcome requires an operation id"))
	}
	if ctrl, ok := core.Router().Replies().TakeFor(operationID); ok {
		_ = core.RecordOutcome(ctrl)
	}
	if ctrl, ok := core.State().OpOutcomes[operationID]; ok {
		a.resolve(operationID)
		return outcomeOf(ctrl), nil
	}
	return &Outcome{OperationID: operationID}, nil
}

// PendingOpCount is how many operations this app has issued whose outcome has not landed.
func (a *App) PendingOpCount() (n int, err error) {
	defer barrier(&err)
	if _, err = a.ready(); err != nil {
		return 0, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.inflight), nil
}

func outcomeOf(ctrl schema.Control) *Outcome {
	code := string(ctrl.ErrorCode)
	if code == "" {
		code = ctrl.Op
	}
	msg := ctrl.Error
	if msg == "" {
		msg = ctrl.Op
	}
	return &Outcome{OperationID: ctrl.OperationID, Code: code, Message: msg, Resolved: true}
}

func (a *App) issue(op *Op) {
	a.mu.Lock()
	a.inflight[op.OperationID] = true
	a.mu.Unlock()
}

func (a *App) resolve(operationID string) {
	a.mu.Lock()
	delete(a.inflight, operationID)
	a.mu.Unlock()
}

// ---- push ----------------------------------------------------------------------

// RegisterPushToken records the provider token with the relay AND persists it, so it
// survives process death and app upgrade and is re-registered on every authenticated
// reconnect (PB-PUSH-9 / PB-STATE-9).
//
// IT PERSISTS FIRST AND REGISTERS SECOND, and the order is the requirement rather than a
// preference. This verb used to take a.conn() as its first act and return that error, so a
// rotation arriving with no connection was DISCARDED -- and FCM does not ask: onNewToken fires
// on reinstall, on app data restore, on a token TTL expiry, on any of them while the app is
// backgrounded and therefore, under ADR-007 B16, disconnected. The consequence was never "the
// rotation is retried later": durable state still held the OLD token, so onConnected
// re-registered the DEAD one, the provider answered UNREGISTERED, the relay pruned it, and the
// handset was unreachable by push with no token registered anywhere, nothing that would ever
// register the new one, and nothing on either side reporting it. The phone looked healthy
// throughout.
//
// A MISSING CONNECTION IS THEREFORE NOT AN ERROR HERE. The work is not lost, it is OWED: the
// token is durable and onConnected carries whatever durable state holds on the next
// authenticated reconnect, which is the mechanism PB-PUSH-9 already requires for its own
// reasons. A relay that is reached and REFUSES is a different matter and is reported.
func (a *App) RegisterPushToken(token string) (err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return err
	}
	if token == "" {
		return classed(ErrClassInvalidRequest, errors.New("swarmmobile: RegisterPushToken requires a token"))
	}
	if err = core.Mutate(func(st *phonecore.State) { st.PushToken = token }); err != nil {
		return err
	}
	cl, cerr := a.conn()
	if cerr != nil {
		return nil
	}
	return cl.TokenRegister(context.Background(), token)
}

// DeletePushToken removes the token from the relay and from durable state. Deletion on
// revoke or disable is part of the token lifecycle, not an optional cleanup.
func (a *App) DeletePushToken() (err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return err
	}
	return a.dropPushToken(core)
}

// dropPushToken is deletion's whole implementation, shared with RevokeThisDevice.
//
// IT CLEARS DURABLE STATE WHETHER OR NOT THE RELAY IS REACHABLE, and that used to be the
// defect rather than the fix: the old shape told the relay only `if cl, cerr := a.conn(); cerr
// == nil` and cleared local state either way, so a user who turned notifications off while the
// phone was backgrounded -- the normal state under ADR-007 B16 -- left the relay holding a live
// token forever, with nothing retrying it because the phone had forgotten the token it would
// have had to delete. The user saw notifications they had switched off and a settings screen
// that agreed they were off.
//
// What closes it is not a queued deletion but the durable state being AUTHORITATIVE: the phone
// holds no token, and onConnected reconciles the relay to that on every authenticated
// reconnect. So the deletion is owed by the same mechanism that owes a registration, there is
// no second flag to keep in step, and the converse holds too -- re-registration carries what
// durable state HOLDS, and after a deletion that is nothing.
func (a *App) dropPushToken(core *phonecore.Core) error {
	if err := core.Mutate(func(st *phonecore.State) { st.PushToken = "" }); err != nil {
		return err
	}
	cl, cerr := a.conn()
	if cerr != nil {
		return nil
	}
	return cl.TokenDelete(context.Background())
}

// PushPreference is the persisted pair of coarse toggles (PB-APP-7).
func (a *App) PushPreference() (pref *PushPreference, err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return nil, err
	}
	p := core.State().PushPreference
	return &PushPreference{Alerts: p.Alerts, Mentions: p.Mentions}, nil
}

// SetPushPreference persists the toggles and carries them to the machine as a signed
// push_prefs command (PB-APP-7, PB-PUSH-8).
//
// THE CROSS-SLICE SPLIT IS CLOSED. This verb used to persist locally and record a durable
// refusal -- "no wire verb yet ... owed by another slice" -- which was correct when it was
// written and went stale the moment S12 shipped ActionPushPrefs, protocol.OpPushPrefs, the
// daemon's requireRemoteAuthz arm, remotegw.applyPushPrefs and the notifier's categoryEnabled
// gate. Until now the user's toggle was a local boolean the machine had never heard of, so
// every push they turned off was still SENT -- and PB-PUSH-8 is explicit that filtering on the
// handset does not satisfy the requirement, because the provider has already seen the token,
// the timing and the size (PB-PUSH-3).
//
// THE MAPPING IS A BIJECTION AND THIS IS WHERE IT IS DECIDED. The facade's fields are named
// Alerts and Mentions; the wire's are NeedsInput and Finished; PB-APP-7's categories are "a
// transition into needs_input" and "a transition into ready_for_review or completed". There
// are no mentions anywhere in this product, so the pairing had to be chosen: Alerts carries
// NEEDS_INPUT -- the agent is blocked on the owner, which is what an alert is -- and Mentions
// carries FINISHED. Two of the three plausible readings are silent defects: wiring both to
// NeedsInput leaves the second switch dead, and swapping them silences the notifications the
// user asked for while delivering the ones they refused.
//
// THE VERSION IS DRAWN FROM DURABLE STATE, and PB-PUSH-10 is why. The machine refuses any
// update whose Version does not STRICTLY exceed the stored one, because the relay may replay a
// frame from before the user turned pushes off. A counter held in memory restarts at 1 after
// the process death Android hands out routinely, and every toggle from then on is refused
// while the settings screen shows the new value -- a brick with no visible symptom.
//
// The local Save happens FIRST and deliberately. If the append then fails the phone has burned
// one version, which costs nothing (the next update strictly exceeds it anyway) and leaves the
// settings screen showing what the user chose. Saving after a successful append would instead
// lose the user's choice on exactly the path where the machine already has it.
func (a *App) SetPushPreference(pref *PushPreference) (op *Op, err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return nil, err
	}
	if pref == nil {
		return nil, classed(ErrClassInvalidRequest, errors.New("swarmmobile: SetPushPreference requires a preference"))
	}
	// The DRAW AND THE WRITE ARE ONE TRANSACTION. Beyond the whole-blob hazard every site
	// here shares, this one has its own: two toggles racing would both read version N and
	// both claim N+1, and the machine refuses anything that does not STRICTLY exceed what it
	// holds -- so the second is silently dropped while the settings screen shows its value.
	var next uint64
	if err = core.Mutate(func(st *phonecore.State) {
		next = st.PushPreference.Version + 1
		st.PushPreference = phonecore.PushPreference{
			Alerts: pref.Alerts, Mentions: pref.Mentions, Version: next,
		}
	}); err != nil {
		return nil, err
	}
	// The signed tuple names LaunchSessionSentinel: a preference has no target session, and
	// the tuple requires a non-empty one. It is the same reserved value the daemon's own
	// push_prefs authorization test signs with, so the two ends cannot disagree.
	return a.signedCommand(schema.ActionPushPrefs, schema.LaunchSessionSentinel, nil,
		commandBody{prefs: &schema.PushPrefs{
			Version:    next,
			NeedsInput: pref.Alerts,
			Finished:   pref.Mentions,
		}})
}

// ---- kill switch ---------------------------------------------------------------

// KillSwitchEngaged reports whether the machine has refused a remote op because its
// owner turned remote control off.
//
// READ ONLY, DELIBERATELY. remote_set_control is owner-tier only: the daemon refuses the
// remote tier BEFORE consulting its backend, because a remote device must never re-enable
// a switch its owner turned off (PB-SEC-6). A setter here would be a surface-level bypass
// of that gate, so there is none -- the phone's own panic action is RevokeThisDevice.
func (a *App) KillSwitchEngaged() (engaged bool, err error) {
	defer barrier(&err)
	if _, err = a.ready(); err != nil {
		return false, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.killSwitch, nil
}

// ---- helpers -------------------------------------------------------------------

func newOperationID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func sortSessions(items []Session) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j].ID < items[j-1].ID; j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}

// parseOptions turns the bind-legal "k=v,k=v" launch options string into the map the
// canonical content hash is computed over. An empty string is no options at all, which
// hashes identically to a nil map.
func parseOptions(s string) map[string]string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	out := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(part, "=")
		if k = strings.TrimSpace(k); k == "" || !ok {
			continue
		}
		out[k] = strings.TrimSpace(v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// deviceID is the phone's own registry id, derived canonically from its command-signing
// public key exactly as the daemon pins it (R-DEV.1).
func (a *App) deviceID() string {
	return device.DeviceIDFor(a.core.KeyStore().CommandSigningPublic())
}

// frameView is the AUTHENTICATED plaintext of one inbound frame, decoded a second time
// for the facade's READ MODELS only. The authoritative decision -- the per-(sender,epoch)
// replay guard and the durable receive transaction -- belongs to phonecore and has
// already been made by the time this runs; nothing here re-decides it, and a frame the
// guard refused is never viewed.
type frameView struct {
	Kind     string
	Record   schema.JournalRecord
	Terminal schema.TerminalSnapshot
	Reply    schema.Control
}

func viewFrame(key crypto.ContentKey, raw []byte) (frameView, bool) {
	env, err := crypto.ParseEnvelope(raw)
	if err != nil {
		return frameView{}, false
	}
	plain, err := crypto.OpenMailbox(key, env)
	if err != nil {
		return frameView{}, false
	}
	var disc struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(plain, &disc); err != nil {
		return frameView{}, false
	}
	v := frameView{Kind: disc.Kind}
	switch disc.Kind {
	case "terminal_snapshot":
		var f struct {
			schema.TerminalSnapshot
		}
		if err := json.Unmarshal(plain, &f); err != nil {
			return frameView{}, false
		}
		v.Terminal = f.TerminalSnapshot
	case "command_reply":
		var f struct {
			schema.Control
		}
		if err := json.Unmarshal(plain, &f); err != nil {
			return frameView{}, false
		}
		v.Reply = f.Control
	case "":
		if err := json.Unmarshal(plain, &v.Record); err != nil {
			return frameView{}, false
		}
	}
	return v, true
}
