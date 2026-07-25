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

	mu            sync.Mutex
	closed        bool
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
	a.mu.Unlock()

	if s != nil {
		s.cancel()
		<-s.done
	}
	a.suspendInput("the app closed")
	a.events.close()
	return nil
}

// ConnectionState is offline / connecting / online / reconnecting (PB-APP-8).
func (a *App) ConnectionState() (state string, err error) {
	defer barrier(&err)
	if _, err = a.ready(); err != nil {
		return "", err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.connState, nil
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
	st := core.State()
	copy(st.Keys.WakeKey[:], key)
	return core.Save(st)
}

// InstallContentKey installs the epoch CONTENT key. It is also PB-KEY-7's recovery path:
// after PurgeKeys, re-installing the tier key restores content operations, so the first
// screen lock does not brick the app.
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
	st := core.State()
	copy(st.Keys.ContentKey[:], key)
	return core.Save(st)
}

// PurgeKeys is PB-KEY-7's lock purge: zeroize the installed tier keys, destroy the SEALED
// copies at rest with them, and drop every DECRYPTED cache. Invalidating the biometric gate
// is not enough while the process still holds already-decrypted session content, and
// zeroizing only the live copy is not enough while the durable one survives the restart. It
// is recoverable by InstallContentKey.
func (a *App) PurgeKeys() (err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return err
	}
	// An EXPLICIT purge, not a Save whose keys happen to be zero: with the content tier
	// locked -- which is exactly where a screen lock leaves the phone -- custody cannot tell
	// a purge from the wake path holding a key it could not read, and would keep the sealed
	// blob (phonecore.Store.PurgeKeys).
	//
	// The facade's own decrypted caches go whether or not the durable half succeeded, for the
	// same reason the core purges its memory unconditionally: clearing them cannot fail, and
	// returning first left the app rendering decrypted session content with the screen
	// locked. The error still reaches the caller -- the blobs at rest survived.
	err = core.PurgeKeys()
	a.mu.Lock()
	a.journal = nil
	a.needs = map[string]string{}
	a.mu.Unlock()
	return err
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
func (a *App) streamStale(stream string) bool {
	a.mu.Lock()
	reconciled := a.reconciled
	a.mu.Unlock()
	return !reconciled || a.core.StreamStale(stream)
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
func (a *App) RegisterPushToken(token string) (err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return err
	}
	if token == "" {
		return classed(ErrClassInvalidRequest, errors.New("swarmmobile: RegisterPushToken requires a token"))
	}
	cl, err := a.conn()
	if err != nil {
		return err
	}
	if err = cl.TokenRegister(context.Background(), token); err != nil {
		return err
	}
	st := core.State()
	st.PushToken = token
	return core.Save(st)
}

// DeletePushToken removes the token from the relay and from durable state. Deletion on
// revoke or disable is part of the token lifecycle, not an optional cleanup.
func (a *App) DeletePushToken() (err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return err
	}
	if cl, cerr := a.conn(); cerr == nil {
		if err = cl.TokenDelete(context.Background()); err != nil {
			return err
		}
	}
	st := core.State()
	st.PushToken = ""
	return core.Save(st)
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
	st := core.State()
	next := st.PushPreference.Version + 1
	st.PushPreference = phonecore.PushPreference{
		Alerts: pref.Alerts, Mentions: pref.Mentions, Version: next,
	}
	if err = core.Save(st); err != nil {
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
