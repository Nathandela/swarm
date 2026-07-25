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

var (
	errClosed        = errors.New("swarmmobile: App is closed")
	errNotRunning    = errors.New("swarmmobile: App is not running; call Start first")
	errNoDestination = errors.New("swarmmobile: no machine relay destination in durable state; the phone knows who the machine is but not how to reach it -- pair first")
	errUnreconciled  = errors.New("swarmmobile: refusing a mutating op until the machine publishes its rollback authorities (PB-SYNC-7)")
	errNoContentKey  = errors.New("swarmmobile: no content key installed; call InstallContentKey after unlocking")
)

// App is the phone. Construct it with NewApp; the zero value is unusable and every
// method says so rather than panicking.
type App struct {
	core *phonecore.Core

	relayURL string

	events *dispatcher

	// coalesce paces keystrokes to §6.0's 8 frames/s (PB-INPUT-5). It has its own lock and
	// is not guarded by a.mu.
	coalesce *phonecore.InputCoalescer

	mu            sync.Mutex
	closed        bool
	drainTimer    *time.Timer
	skewMsg       string // last reported clock-skew verdict, so only a CHANGE raises an event
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
	staleStrm     map[string]bool
	inflight      map[string]bool
}

// session is one Start..Stop generation.
type session struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// NewApp resumes the phone from its state directory (PB-STATE-1/-2: every durable
// coordinate, or a fail-closed error -- never a silent start from zero) and returns the
// bound App. It does not connect; Start does.
func NewApp(cfg *Config) (app *App, err error) {
	defer barrier(&err)
	if cfg == nil {
		return nil, errors.New("swarmmobile: NewApp requires a Config")
	}
	a := &App{
		relayURL:   cfg.RelayURL,
		events:     newDispatcher(),
		coalesce:   phonecore.NewInputCoalescer(time.Now),
		connState:  "offline",
		subscribed: true,
		needs:      map[string]string{},
		staleStrm:  map[string]bool{},
		inflight:   map[string]bool{},
	}
	core, err := phonecore.Resume(phonecore.Config{
		Dir:     cfg.StateDir,
		Machine: cfg.MachineID,
		Ack:     &relayAcker{app: a},
		// RECORDED DEFECT, owned by S14 (ADR-007 B18). PB-KEY-9's seam is
		// phonecore.Config, which the Android side cannot reach: gomobile cannot set a
		// Go struct field and mobile.Config is golden-pinned with no verb for a sealer.
		// So the shipped app still writes key material in the clear -- named here at the
		// call site rather than reached by omitting a field, which Resume refuses
		// outright (ErrNoSealer). S14 adds the facade verb, changes the golden and
		// replaces these two with the Android-Keystore-backed KEKs; PB-KEY-9 is not
		// delivered until it does.
		WakeSealer:    phonecore.InsecureCleartextSealer(),
		ContentSealer: phonecore.InsecureCleartextSealer(),
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
	out := &UndeliveredList{items: make([]UndeliveredInput, 0, len(entries))}
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

// IsRunning reports whether the relay drain is live.
func (a *App) IsRunning() (running bool, err error) {
	defer barrier(&err)
	if _, err = a.ready(); err != nil {
		return false, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sess != nil, nil
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
		Restored:    st.Machine != "",
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
		return fmt.Errorf("swarmmobile: wake key must be %d bytes", len(crypto.WakeKey{}))
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
		return fmt.Errorf("swarmmobile: content key must be %d bytes", len(crypto.ContentKey{}))
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
	if err = core.PurgeKeys(); err != nil {
		return err
	}
	a.mu.Lock()
	a.journal = nil
	a.needs = map[string]string{}
	a.mu.Unlock()
	return nil
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
	out := &SessionList{items: make([]Session, 0, len(cached))}
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
		return nil, fmt.Errorf("swarmmobile: no session %q in the roster", id)
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
		return nil, fmt.Errorf("swarmmobile: no terminal snapshot for %q", session)
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
	a.mu.Lock()
	defer a.mu.Unlock()
	out := &JournalPage{next: from}
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

// Resync clears a stream's stale mark and forces an immediate mailbox drain, so the
// durable model catches up on the next contiguous frame (PB-SYNC-1/-6).
func (a *App) Resync(stream string) (err error) {
	defer barrier(&err)
	if _, err = a.ready(); err != nil {
		return err
	}
	a.mu.Lock()
	delete(a.staleStrm, stream)
	a.mu.Unlock()
	return nil
}

func (a *App) streamStale(stream string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.staleStrm[stream] || !a.reconciled
}

func (a *App) markStream(stream string, stale bool) {
	a.mu.Lock()
	if stale {
		a.staleStrm[stream] = true
	}
	a.mu.Unlock()
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
		return nil, errors.New("swarmmobile: Outcome requires an operation id")
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
		return errors.New("swarmmobile: RegisterPushToken requires a token")
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

// SetPushPreference persists the toggles and returns the operation that carries them to
// the machine.
//
// CROSS-SLICE SPLIT, recorded in screen_coverage.tsv: S8 owns this SURFACE, S12 owns the
// wired verb. Local filtering is NOT a substitute -- the push would still be sent and the
// provider would still see the token, the timing and the size (PB-PUSH-8) -- so the
// preference is persisted and the missing verb is recorded as a durable, legible refusal,
// exactly as Interrupt does. It is not reported as if the machine had acknowledged it,
// and it is not left in flight: an op no verb can resolve would raise PendingOpCount on
// every toggle, for the life of the process.
func (a *App) SetPushPreference(pref *PushPreference) (op *Op, err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return nil, err
	}
	if pref == nil {
		return nil, errors.New("swarmmobile: SetPushPreference requires a preference")
	}
	st := core.State()
	st.PushPreference = phonecore.PushPreference{Alerts: pref.Alerts, Mentions: pref.Mentions}
	if err = core.Save(st); err != nil {
		return nil, err
	}
	return a.refuse("push_preference", "",
		"push preference is persisted locally but has no wire verb yet; carrying it to the machine "+
			"is owed by another slice (PB-PUSH-8)")
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
