package skeleton

// The ContextGuard runtime is the daemon-owned bridge between a pure provider
// parser and the pure policy reducer. Backend callbacks only enqueue/coalesce
// evidence and wake one per-session worker; the worker alone performs policy,
// durability, and -- since ADR-023 amendment 1 -- dispatch work.
//
// THE 2026-09-01 GATES CAME BACK NEGATIVE: a live codex 0.151.0 accepts
// thread/compact/start instantly under every condition -- mid-turn it CANCELS
// the running turn, and two concurrent compacts interrupt each other. The
// provider serializes nothing, so the daemon serializes everything: a dispatch
// happens only from the session's composer lane (FIFO with every daemon-driven
// send and Stop), only after quiet revalidation at the queue head, and only
// while nobody holds the session's controls -- attached typing is the one input
// the lane cannot order, so attended sessions are never auto-compacted. The
// executing record crosses the provider write boundary durably; once bytes may
// have left, every failure is a hold, never a resend.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/contextguard"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

const (
	contextGuardStateFile          = "context-guard-state.json"
	contextGuardStateSchemaVersion = 1
	// contextGuardPendingLimit was sized for a worker that never blocked beyond
	// parse+fsync. The dispatch worker can be parked for a lane wait plus the
	// full reply timeout while a busy turn's item lifecycle flows in, so the
	// bound is sized for that window: overflowing it is an event_loss_hold.
	contextGuardPendingLimit = 256
	contextGuardFrameLimit   = 64 << 10
	contextGuardUsageMethod  = "thread/tokenUsage/updated"
	// contextGuardDispatchTimeout bounds the provider call. The live 0.151.0
	// compact reply arrived in 1ms; a reply that needs longer than this is an
	// unknown outcome, never a retry (compaction is non-idempotent).
	contextGuardDispatchTimeout = 30 * time.Second
	// contextGuardConfirmTimeout bounds awaiting_confirmation/provider_compacting.
	// The reply to thread/compact/start proves nothing about the compaction
	// itself; if the provider's lifecycle completion never arrives (the
	// compaction was interrupted, or a provider patch changed the item shape),
	// the outcome is genuinely unknown and the machine holds rather than wedging
	// silently -- and rather than gating composer sends forever.
	contextGuardConfirmTimeout = 5 * time.Minute
	// contextGuardContinuationTimeout bounds the queue/add enqueue. It is an
	// ordinary request (live-measured well under a second); a provider that
	// cannot take it promptly loses the continuation, never blocks the worker.
	contextGuardContinuationTimeout = 10 * time.Second
)

// contextGuardContinuationPrompt is the daemon-authored message queued behind
// the guard's own compaction (ADR-023 amendment 2). The compaction presents
// itself as mid-task context maintenance and the flow resumes; the final
// clause protects a session that crossed the threshold after finishing its
// task and was merely waiting for review.
const contextGuardContinuationPrompt = "This session's context was automatically compacted by swarm to keep the task focused. Continue the task exactly where you left off. If the task was already complete and you were waiting for review, say so briefly and do not start new work."

// contextGuardDispatchConn is the narrow provider surface a dispatch needs: the
// write-boundary call whose beforeWrite error PROVABLY precedes any bytes (the
// appserver client's documented contract). backendConn deliberately stays the
// minimal Call interface; production's *appserver.Client satisfies this one by
// assertion, and a backend whose conn cannot make the write boundary explicit
// simply never dispatches.
type contextGuardDispatchConn interface {
	CallAtWriteBoundary(ctx context.Context, method string, params, out any, beforeWrite func() error, afterWrite func()) error
	// Call is the plain request path, used for the continuation enqueue
	// (ADR-023 amendment 2): a queue/add is an ordinary, non-destructive
	// message whose ORDERING safety is provider-enforced once the compaction
	// runs, so it needs no write-boundary ceremony.
	Call(ctx context.Context, method string, params, out any) error
}

// errContextGuardDispatchRefused aborts the provider call from beforeWrite when
// the reducer refuses the executing transition (or its durability failed): the
// contract above guarantees no bytes follow.
var errContextGuardDispatchRefused = errors.New("contextguard: dispatch refused at the write boundary")

type contextGuardManager struct {
	d        *Daemon
	stateDir string
	settings *contextGuardSettingsStore

	replaceMu sync.Mutex
	mu        sync.Mutex
	sessions  map[string]*contextGuardSession
	closed    bool
	// captureRevision is the newest durable global revision provider callbacks
	// must stamp. appliedRevision is protected by replaceMu and advances only after
	// that revision has been enqueued to every worker that existed at fan-out.
	captureRevision atomic.Uint64
	appliedRevision uint64
}

type contextGuardSession struct {
	manager *contextGuardManager
	id      string
	key     contextguard.Key
	source  adapter.ContextGuardSource
	action  adapter.ContextGuardAction

	// The dispatch seams (ADR-023 D5/D6; amendment 1). All nil-safe: a session
	// registered without them -- every pre-dispatch caller and every test rig
	// that never wires a backend -- is exactly the old observe-only runtime.
	// conn crosses the provider write boundary; lane yields the session's
	// composer lane so an automatic compaction is FIFO-ordered against every
	// daemon-driven send and Stop; quiet answers the D6 revalidation (running,
	// turn idle, interaction none, and NOBODY at the controls -- the one input
	// the lane cannot serialize is a human typing in the attached PTY, so an
	// attended session is never auto-compacted); uncertain reports the lane's
	// own unresolved-composer-outcome flag.
	conn      contextGuardDispatchConn
	lane      func() *composerLane
	quiet     func() bool
	uncertain func() bool
	// current re-checks the full backend identity (live conn, matching session
	// instance) that registration proved; nil means always current. It runs
	// again at the queue head and inside the write boundary, so a dispatch
	// prepared against a backend that was replaced while it waited never writes.
	current func() bool
	// confirmTimeout overrides contextGuardConfirmTimeout in tests; zero means
	// the production value.
	confirmTimeout time.Duration
	// continuation shapes the post-compaction enqueue (nil = no continuation:
	// observe-only guards, non-continuer adapters, unfenced versions).
	// attended answers "is anyone at the controls right now" -- an attended
	// session gets no queued surprise turn; the human continues themselves.
	continuation func(threadID, messageID, text string) (method string, params map[string]any, ok bool)
	attended     func() bool
	// continuationArmed is worker-goroutine-owned: set when THIS guard's own
	// compact crossed the write boundary, consumed by the single enqueue for
	// that cycle. A native or manual compaction never arms it, so the guard
	// never continues work it did not interrupt. Ambiguity disarms: a hold or
	// any exit from the compaction cycle without provider evidence forfeits
	// the continuation rather than risking a duplicate or misplaced turn.
	continuationArmed bool

	queueMu          sync.Mutex
	stateMu          sync.Mutex
	latestUsage      *contextGuardPending
	latestConfig     *contextGuardPending
	lifecycle        []contextGuardPending
	lost             bool
	lossSequence     uint64
	lossAt           time.Time
	machine          contextguard.Machine
	view             protocol.ContextGuardView
	persistBlocked   bool
	persistFn        func(contextguard.Machine) error
	stopped          bool
	nextQueueOrder   uint64
	settingsRevision atomic.Uint64

	wake chan struct{}
	stop chan struct{}
	done chan struct{}
}

type contextGuardPending struct {
	order            uint64
	sequence         uint64
	at               time.Time
	method           string
	raw              []byte
	settingsRevision uint64
	config           *contextguard.Config
}

type contextGuardFeedFrame struct {
	sequence         uint64
	at               time.Time
	method           string
	raw              []byte
	settingsRevision uint64
}

type contextGuardStateDocument struct {
	SchemaVersion    int                `json:"schema_version"`
	SessionInstance  string             `json:"session_instance"`
	SettingsRevision uint64             `json:"settings_revision"`
	State            contextguard.State `json:"state"`
	TriggerThreshold int                `json:"trigger_threshold,omitempty"`
}

func newContextGuardManager(d *Daemon, stateDir string, settings *contextGuardSettingsStore) *contextGuardManager {
	m := &contextGuardManager{d: d, stateDir: stateDir, settings: settings, sessions: make(map[string]*contextGuardSession)}
	if current, err := settings.ContextGuardSettings(); err == nil {
		m.captureRevision.Store(current.Revision)
		m.appliedRevision = current.Revision
	}
	return m
}

func contextGuardConfig(settings protocol.ContextGuardSettings) contextguard.Config {
	return contextguard.Config{
		Enabled: settings.AutoCompact.Enabled, Threshold: settings.AutoCompact.ThresholdPercent,
		Revision: settings.Revision,
	}
}

func (d *Daemon) registerContextGuardBackend(local string, backend *sessionBackend) {
	if d.contextGuards == nil || backend == nil || backend.feed == nil {
		if backend != nil && backend.feed != nil {
			d.discardContextGuardFeed(backend.feed)
		}
		return
	}
	if d.core == nil {
		// The reconcile window: this backend connected before the engine was
		// published, and startContextGuardsForRunning re-registers it once the
		// core exists. Discarding here would deafen that later registration
		// permanently (the feed's discard is one-way), leaving a guard that
		// advertises support but never hears telemetry. The feed keeps
		// buffering: bounded, and activation flushes or records the loss.
		return
	}
	isCurrent := func() bool {
		d.backend.mu.Lock()
		live := d.backend.live[local] == backend
		d.backend.mu.Unlock()
		instance, ok := d.sessionInstance(local)
		return live && ok && instance == backend.sessionInstance
	}
	if !isCurrent() {
		d.discardContextGuardFeed(backend.feed)
		return
	}
	meta, ok := d.core.Get(local)
	if !ok {
		d.discardContextGuardFeed(backend.feed)
		return
	}
	ad, ok := d.resolveAdapter(meta.AgentType)
	if !ok {
		d.discardContextGuardFeed(backend.feed)
		return
	}
	source, ok := adapter.AsContextGuardSource(ad)
	if !ok {
		d.discardContextGuardFeed(backend.feed)
		return
	}
	version, ok := ad.ParseVersion(backend.feed.userAgent)
	if !ok {
		d.discardContextGuardFeed(backend.feed)
		return
	}
	action, ok := source.ContextGuardAction(version)
	if !ok {
		d.discardContextGuardFeed(backend.feed)
		return
	}
	key := contextguard.Key{
		SessionID: local, SessionInstance: backend.sessionInstance,
		BackendEpoch: backend.feed.epoch, ProviderThreadID: backend.threadID,
	}
	// The dispatch seams (ADR-023 amendment 1) are wired ONLY here, where the
	// live backend conn exists -- every other registration path stays
	// observe-only by construction. A conn that cannot make its write boundary
	// explicit never dispatches.
	dispatchConn, _ := backend.conn.(contextGuardDispatchConn)
	wire := func(s *contextGuardSession) {
		if dispatchConn == nil || !action.AutomaticDispatch {
			return
		}
		s.conn = dispatchConn
		s.lane = func() *composerLane { return d.composerLaneFor(local) }
		s.quiet = func() bool { return d.contextGuardQuiet(local) }
		s.uncertain = func() bool { return d.composerLaneFor(local).uncertainNow() }
		s.current = isCurrent
		// The continuation seams (ADR-023 amendment 2) ride the same wiring:
		// only a live automatic backend ever continues, and only when the
		// adapter's version-fenced continuer exists for this version.
		if continuer, ok := adapter.AsContextGuardContinuer(ad); ok {
			s.continuation = func(threadID, messageID, text string) (string, map[string]any, bool) {
				return continuer.ContextGuardContinuation(version, threadID, messageID, text)
			}
			s.attended = func() bool { return d.anyControlled(local) }
		}
	}
	if !d.contextGuards.registerCurrentWired(local, key, source, action, isCurrent, wire) || !isCurrent() {
		d.contextGuards.unregister(local, key)
		d.discardContextGuardFeed(backend.feed)
		return
	}
	d.activateContextGuardFeed(local, key, backend.feed)
}

// contextGuardQuiet answers ADR-023 D6's revalidation for one session: process
// running, turn idle, no interaction -- and UNATTENDED. The composer lane
// serializes every daemon-driven action against the dispatch, but a human
// typing in the attached PTY is the one input it cannot order (the 2026-09-01
// gates prove that race costs the human's turn), so an attended session is
// never auto-compacted: whoever holds the controls can /compact themselves.
// The guard exists for the unattended fleet.
func (d *Daemon) contextGuardQuiet(local string) bool {
	if d.core == nil {
		return false
	}
	m, ok := d.core.Get(local)
	if !ok || m.Status.Process != status.ProcessRunning ||
		m.Status.Turn != status.TurnIdle || m.Status.Interaction != status.InteractionNone {
		return false
	}
	return !d.anyControlled(local)
}

// contextGuardCompactionInFlight reports whether local's guard has a compaction
// between its durable executing record and its confirmed/held/latched outcome.
// The composer lane orders WRITES; this predicate covers the EFFECT window: a
// compaction runs for seconds after its bytes leave, and any daemon-originated
// stimulus written into that window destroys the compaction, the stimulus, or
// both (2026-09-01 gate evidence). composerSend refuses (retryable) and the
// supervisor defers while it is true. It is bounded by the confirmation
// deadline, so a lost completion degrades to a hold, never a wedged send path.
func (d *Daemon) contextGuardCompactionInFlight(local string) bool {
	if d.contextGuards == nil {
		return false
	}
	return d.contextGuards.compactionInFlight(local)
}

func (m *contextGuardManager) compactionInFlight(local string) bool {
	m.mu.Lock()
	s := m.sessions[local]
	m.mu.Unlock()
	return s != nil && s.compactionInFlight()
}

func (d *Daemon) unregisterContextGuardBackend(local string, backend *sessionBackend) {
	if d.contextGuards == nil || backend == nil || backend.feed == nil {
		return
	}
	d.contextGuards.unregister(local, contextguard.Key{
		SessionID: local, SessionInstance: backend.sessionInstance,
		BackendEpoch: backend.feed.epoch, ProviderThreadID: backend.threadID,
	})
}

func (d *Daemon) ingestContextGuardFrame(local, instance, epoch string, sequence, settingsRevision uint64, method string, frame []byte, at time.Time) {
	if d.contextGuards != nil {
		d.contextGuards.ingestRevision(local, contextguard.Key{
			SessionID: local, SessionInstance: instance, BackendEpoch: epoch,
		}, sequence, settingsRevision, method, frame, at)
	}
}

// captureContextGuardFrame is the callback/read-loop seam. It never parses,
// persists, or performs provider I/O. Before registration it retains only the
// latest usage sample and a bounded lifecycle queue; activation flushes while
// holding guardMu so a later callback cannot overtake the buffered evidence.
func (d *Daemon) captureContextGuardFrame(local, instance string, feed *backendFeed, method string, frame []byte, at time.Time) {
	if feed == nil {
		return
	}
	sequence := feed.seq.Add(1)
	if !contextGuardBackendMethod(method) || at.IsZero() || len(frame) == 0 || len(frame) > contextGuardFrameLimit {
		return
	}
	revision := uint64(0)
	if d.contextGuards != nil {
		revision = d.contextGuards.captureRevision.Load()
	}
	pending := contextGuardFeedFrame{sequence: sequence, at: at, method: method, raw: append([]byte(nil), frame...), settingsRevision: revision}
	feed.guardMu.Lock()
	defer feed.guardMu.Unlock()
	if feed.guardDiscarded {
		return
	}
	if feed.guardReady {
		d.ingestContextGuardFrame(local, instance, feed.epoch, sequence, revision, method, pending.raw, at)
		return
	}
	if method == contextGuardUsageMethod {
		feed.guardLatestUsage = &pending
		return
	}
	if len(feed.guardLifecycle) < contextGuardPendingLimit {
		feed.guardLifecycle = append(feed.guardLifecycle, pending)
		return
	}
	feed.guardDropped = true
	feed.guardDropAt = at
}

func (d *Daemon) activateContextGuardFeed(local string, key contextguard.Key, feed *backendFeed) {
	if feed == nil || d.contextGuards == nil {
		return
	}
	feed.guardMu.Lock()
	defer feed.guardMu.Unlock()
	if feed.guardDiscarded || feed.guardReady {
		return
	}
	if feed.guardDropped {
		d.contextGuards.ingestLoss(local, key, feed.seq.Load(), feed.guardDropAt)
	} else {
		pending := append([]contextGuardFeedFrame(nil), feed.guardLifecycle...)
		if feed.guardLatestUsage != nil {
			pending = append(pending, *feed.guardLatestUsage)
		}
		sort.Slice(pending, func(i, j int) bool { return pending[i].sequence < pending[j].sequence })
		for _, p := range pending {
			d.ingestContextGuardFrame(local, key.SessionInstance, key.BackendEpoch, p.sequence, p.settingsRevision, p.method, p.raw, p.at)
		}
	}
	feed.guardLatestUsage = nil
	feed.guardLifecycle = nil
	feed.guardReady = true
}

func (d *Daemon) discardContextGuardFeed(feed *backendFeed) {
	if feed == nil {
		return
	}
	feed.guardMu.Lock()
	feed.guardLatestUsage = nil
	feed.guardLifecycle = nil
	feed.guardDiscarded = true
	feed.guardMu.Unlock()
}

func (d *Daemon) startContextGuardsForRunning() {
	if d.contextGuards == nil {
		return
	}
	d.backend.mu.Lock()
	entries := make(map[string]*sessionBackend, len(d.backend.live))
	for id, backend := range d.backend.live {
		entries[id] = backend
	}
	d.backend.mu.Unlock()
	for id, backend := range entries {
		d.registerContextGuardBackend(id, backend)
	}
}

func (d *Daemon) stopContextGuard(id string) {
	if d.contextGuards != nil {
		d.contextGuards.stopSession(id)
	}
}

func (m *contextGuardManager) register(id string, key contextguard.Key, source adapter.ContextGuardSource, action adapter.ContextGuardAction) bool {
	return m.registerCurrentWired(id, key, source, action, nil, nil)
}

// registerCurrentWired is registerCurrent with the dispatch seams installed
// BEFORE the worker goroutine starts (installing after would race the worker's
// first promotion check). wire nil is every observe-only registration.
func (m *contextGuardManager) registerCurrentWired(id string, key contextguard.Key, source adapter.ContextGuardSource, action adapter.ContextGuardAction, current func() bool, wire func(*contextGuardSession)) bool {
	m.replaceMu.Lock()
	defer m.replaceMu.Unlock()
	if current != nil && !current() {
		return false
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return false
	}
	old := m.sessions[id]
	if old != nil && old.key == key {
		m.mu.Unlock()
		return true
	}
	delete(m.sessions, id)
	m.mu.Unlock()
	if old != nil {
		old.close()
	}
	if current != nil && !current() {
		m.changed()
		return false
	}

	settings, settingsErr := m.settings.ContextGuardSettings()
	machine, err := contextguard.New(contextGuardConfig(settings), key)
	if err != nil {
		m.changed()
		return false
	}
	if machine.Config.Revision > m.captureRevision.Load() {
		m.captureRevision.Store(machine.Config.Revision)
	}
	s := &contextGuardSession{
		manager: m, id: id, key: key, source: source, action: action, machine: machine,
		wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}),
	}
	s.settingsRevision.Store(machine.Config.Revision)
	s.persistFn = s.persist
	if wire != nil {
		wire(s)
	}
	if settingsErr != nil {
		s.persistBlocked = true
		s.view = protocol.ContextGuardView{Support: string(action.Support), Phase: string(contextguard.StateBlockedCorrupt), ErrorCode: "settings_unavailable"}
	} else {
		s.restore()
		if !s.persistBlocked {
			s.refreshView(nil)
		}
	}

	if current != nil && !current() {
		return false
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return false
	}
	m.sessions[id] = s
	m.mu.Unlock()
	go s.run()
	m.changed()
	return true
}

func (m *contextGuardManager) unregister(id string, key contextguard.Key) {
	m.replaceMu.Lock()
	defer m.replaceMu.Unlock()
	m.mu.Lock()
	s := m.sessions[id]
	if s == nil || s.key != key {
		m.mu.Unlock()
		return
	}
	delete(m.sessions, id)
	m.mu.Unlock()
	s.close()
	m.changed()
}

func (m *contextGuardManager) stopSession(id string) {
	m.replaceMu.Lock()
	defer m.replaceMu.Unlock()
	m.stopSessionExceptInstanceLocked(id, "")
}

// stopSessionExceptInstanceLocked is used by the session-instance publication
// transaction while replaceMu is already held. keepInstance preserves a guard
// that is already bound to the incarnation being published; empty stops any.
func (m *contextGuardManager) stopSessionExceptInstanceLocked(id, keepInstance string) {
	m.mu.Lock()
	s := m.sessions[id]
	if s != nil && keepInstance != "" && s.key.SessionInstance == keepInstance {
		m.mu.Unlock()
		return
	}
	delete(m.sessions, id)
	m.mu.Unlock()
	if s != nil {
		s.close()
		m.changed()
	}
}

func (m *contextGuardManager) close() {
	m.replaceMu.Lock()
	defer m.replaceMu.Unlock()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	sessions := make([]*contextGuardSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.sessions = make(map[string]*contextGuardSession)
	m.mu.Unlock()
	for _, s := range sessions {
		s.close()
	}
}

func (s *contextGuardSession) close() {
	s.queueMu.Lock()
	if !s.stopped {
		s.stopped = true
		close(s.stop)
	}
	s.queueMu.Unlock()
	<-s.done
}

func (m *contextGuardManager) ingest(local string, partialKey contextguard.Key, sequence uint64, method string, frame []byte, at time.Time) {
	m.ingestBound(local, partialKey, sequence, 0, false, method, frame, at)
}

func (m *contextGuardManager) ingestRevision(local string, partialKey contextguard.Key, sequence, settingsRevision uint64, method string, frame []byte, at time.Time) {
	m.ingestBound(local, partialKey, sequence, settingsRevision, true, method, frame, at)
}

func (m *contextGuardManager) ingestBound(local string, partialKey contextguard.Key, sequence, settingsRevision uint64, revisionBound bool, method string, frame []byte, at time.Time) {
	m.mu.Lock()
	s := m.sessions[local]
	m.mu.Unlock()
	if s == nil || sequence == 0 || at.IsZero() || len(frame) == 0 || len(frame) > contextGuardFrameLimit || !contextGuardBackendMethod(method) {
		return
	}
	if partialKey.SessionID != s.key.SessionID || partialKey.SessionInstance != s.key.SessionInstance || partialKey.BackendEpoch != s.key.BackendEpoch {
		return
	}
	// Copy only an already-bounded, method-filtered frame. Parsing belongs to the
	// worker, never the app-server callback/read loop.
	// Queue state is deliberately disjoint from policy/persistence state. A worker
	// may be fsyncing while this callback takes queueMu; it can never make the
	// provider read loop wait for that filesystem operation.
	s.queueMu.Lock()
	if s.stopped {
		s.queueMu.Unlock()
		return
	}
	s.nextQueueOrder++
	if !revisionBound {
		settingsRevision = s.settingsRevision.Load()
	}
	pending := contextGuardPending{
		order: s.nextQueueOrder, sequence: sequence, at: at, method: method,
		raw: append([]byte(nil), frame...), settingsRevision: settingsRevision,
	}
	if method == contextGuardUsageMethod {
		if s.latestUsage == nil || sequence > s.latestUsage.sequence {
			s.latestUsage = &pending
		}
	} else if len(s.lifecycle) < contextGuardPendingLimit {
		s.lifecycle = append(s.lifecycle, pending)
	} else {
		s.lost = true
		if sequence > s.lossSequence {
			s.lossSequence = sequence
			s.lossAt = at
		}
	}
	s.queueMu.Unlock()
	s.signal()
}

func (m *contextGuardManager) ingestLoss(local string, key contextguard.Key, sequence uint64, at time.Time) {
	m.mu.Lock()
	s := m.sessions[local]
	m.mu.Unlock()
	if s == nil || s.key != key || sequence == 0 || at.IsZero() {
		return
	}
	s.queueMu.Lock()
	if s.stopped {
		s.queueMu.Unlock()
		return
	}
	s.lost = true
	if sequence > s.lossSequence {
		s.lossSequence = sequence
		s.lossAt = at
	}
	s.queueMu.Unlock()
	s.signal()
}

func (s *contextGuardSession) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *contextGuardSession) run() {
	defer close(s.done)
	var deadline *time.Timer
	var deadlineC <-chan time.Time
	defer func() {
		if deadline != nil {
			deadline.Stop()
		}
	}()
	for {
		select {
		case <-s.stop:
			return
		case <-s.wake:
			s.drain()
			// The continuation check precedes promotion: a compaction observed
			// to start (or already latched) enqueues the task's resumption
			// before any new pressure decision is made.
			s.maybeContinue()
			// Promotion runs after every drain: a fresh usage sample may have
			// crossed the threshold while the session was already quiet, or a
			// status edge (quietPending) may have arrived with nothing queued.
			s.maybePromote()
		case <-deadlineC:
			deadline, deadlineC = nil, nil
			// Evidence already queued outranks the timer: a completion that
			// arrived but was not yet drained must latch, not hold.
			s.drain()
			s.maybeContinue()
			s.confirmDeadlineExpired()
		}
		// The confirmation deadline is armed exactly while the machine waits on
		// provider lifecycle evidence for a written action, and disarmed the
		// moment that evidence (or any other exit) arrives.
		if s.awaitingOutcome() {
			if deadline == nil {
				timeout := s.confirmTimeout
				if timeout <= 0 {
					timeout = contextGuardConfirmTimeout
				}
				deadline = time.NewTimer(timeout)
				deadlineC = deadline.C
			}
		} else if deadline != nil {
			deadline.Stop()
			deadline, deadlineC = nil, nil
		}
	}
}

// pendingEvidence reports whether anything sits undrained in the session's own
// queue. At the dispatch queue head and write boundary, any queued frame is a
// veto: a lifecycle item on a supposedly quiet session means something is
// running, a config frame may be the disable that outran the dispatch, and a
// trailing usage frame means the fold is behind. The next wake re-drains and a
// later quiet edge retries.
func (s *contextGuardSession) pendingEvidence() bool {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	return s.lost || len(s.lifecycle) > 0 || s.latestConfig != nil || s.latestUsage != nil
}

func (s *contextGuardSession) configRevision() uint64 {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.machine.Config.Revision
}

// awaitingOutcome reports whether the machine currently waits on provider
// lifecycle evidence for an action whose bytes are on the wire.
func (s *contextGuardSession) awaitingOutcome() bool {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.machine.State == contextguard.StateAwaitingConfirmation ||
		s.machine.State == contextguard.StateProviderCompacting
}

// compactionInFlight is the composer/supervisor gate: true from the durable
// executing record until the compaction is confirmed, held, or latched. While
// it is true, nothing the daemon originates may write into the thread -- the
// 2026-09-01 gates prove a mid-compaction stimulus destroys the compaction, the
// stimulus, or both.
func (s *contextGuardSession) compactionInFlight() bool {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	switch s.machine.State {
	case contextguard.StateExecuting, contextguard.StateAwaitingConfirmation, contextguard.StateProviderCompacting:
		return true
	default:
		return false
	}
}

// maybeContinue enqueues the post-compaction continuation (ADR-023 amendment
// 2) for a compaction THIS guard dispatched, exactly once per cycle. It runs
// on the worker goroutine after every drain. The safe window is provider-
// enforced: once the guard has observed provider_compacting, the compaction
// turn is active and codex's queue defers behind it, auto-starting the queued
// message when the compaction completes; at latched the thread is idle and
// the enqueue starts the continuation directly. Any other exit from the cycle
// (a hold, a disable, corruption) forfeits the continuation -- a lost
// continuation costs one stalled task the owner can nudge, while a misplaced
// one costs a surprise turn.
func (s *contextGuardSession) maybeContinue() {
	if !s.continuationArmed || s.continuation == nil || s.conn == nil {
		return
	}
	s.stateMu.Lock()
	state := s.machine.State
	s.stateMu.Unlock()
	switch state {
	case contextguard.StateExecuting, contextguard.StateAwaitingConfirmation:
		return // the compaction's fate is not yet visible; keep waiting
	case contextguard.StateProviderCompacting, contextguard.StateLatched:
		s.continuationArmed = false
		s.enqueueContinuation()
	default:
		s.continuationArmed = false // ambiguity or cycle exit: forfeit, never guess
	}
}

func (s *contextGuardSession) enqueueContinuation() {
	if s.attended != nil && s.attended() {
		log.Printf("skeleton: context guard continuation for session %s skipped: someone is at the controls", s.id)
		return
	}
	messageID := fmt.Sprintf("swarm-cg-continuation-%s-%d", s.key.SessionInstance, time.Now().UnixNano())
	method, params, ok := s.continuation(s.key.ProviderThreadID, messageID, contextGuardContinuationPrompt)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), contextGuardContinuationTimeout)
	defer cancel()
	go func() {
		select {
		case <-s.stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	var res json.RawMessage
	if err := s.conn.Call(ctx, method, params, &res); err != nil {
		// No retry: the enqueue's outcome is now ambiguous, and a duplicate
		// continuation is a surprise turn. The task may need a manual nudge.
		log.Printf("skeleton: context guard continuation for session %s not enqueued: %v", s.id, err)
		return
	}
	log.Printf("skeleton: context guard queued the task continuation for session %s", s.id)
}

// confirmDeadlineExpired converts a silent wedge into an honest hold: the
// action was written, its confirmation never arrived within the deadline, so
// the outcome is unknown (D5: never resent). Re-checked under the current
// state -- evidence that arrived while the timer fired wins.
func (s *contextGuardSession) confirmDeadlineExpired() {
	if !s.awaitingOutcome() {
		return
	}
	s.apply(contextguard.Event{Kind: contextguard.EventActionOutcomeUnknown, At: time.Now(), Key: s.key}, nil)
	log.Printf("skeleton: context guard compaction for session %s unconfirmed after %s; holding", s.id, contextGuardConfirmTimeout)
}

func (s *contextGuardSession) drain() {
	s.queueMu.Lock()
	if s.lost {
		loss := contextGuardPending{sequence: s.lossSequence, at: s.lossAt}
		config := s.latestConfig
		s.lost = false
		s.latestUsage = nil
		s.latestConfig = nil
		s.lifecycle = nil
		s.queueMu.Unlock()
		s.apply(contextguard.Event{Kind: contextguard.EventEventLoss, At: loss.at, Key: s.key, SourceSequence: loss.sequence}, nil)
		// D4: settings edges may not disappear. Usage samples and lifecycle
		// evidence are gone with the loss (that is what the hold records), but a
		// queued config change still advances the machine's revision so
		// post-hold observations are judged against the settings the owner
		// actually chose.
		if config != nil && config.config != nil {
			s.apply(contextguard.Event{Kind: contextguard.EventConfigChanged, At: config.at, Config: *config.config}, nil)
		}
		return
	}
	pending := append([]contextGuardPending(nil), s.lifecycle...)
	if s.latestUsage != nil {
		pending = append(pending, *s.latestUsage)
	}
	if s.latestConfig != nil {
		pending = append(pending, *s.latestConfig)
	}
	s.lifecycle = nil
	s.latestUsage = nil
	s.latestConfig = nil
	s.queueMu.Unlock()
	sort.Slice(pending, func(i, j int) bool { return pending[i].order < pending[j].order })
	for i := range pending {
		s.applyPending(pending[i])
	}
}

func (s *contextGuardSession) applyPending(p contextGuardPending) {
	if p.config != nil {
		s.apply(contextguard.Event{Kind: contextguard.EventConfigChanged, At: p.at, Config: *p.config}, nil)
		return
	}
	notification, ok := s.source.ParseContextGuardNotification(p.raw, s.key.ProviderThreadID)
	if !ok || notification.ThreadID != s.key.ProviderThreadID {
		return
	}
	var event contextguard.Event
	switch notification.Kind {
	case adapter.ContextGuardUsage:
		event = contextguard.Event{Kind: contextguard.EventObservation, At: p.at, Observation: contextguard.Observation{
			Key: s.key, SettingsRevision: p.settingsRevision, IngestSequence: p.sequence,
			ObservedAt: p.at, UsedTokens: notification.UsedTokens,
			ContextLimit: notification.ContextLimit, Quality: contextguard.QualityExact,
		}}
	case adapter.ContextGuardCompactionStarted:
		event = contextguard.Event{Kind: contextguard.EventProviderCompactionStarted, At: p.at, Key: s.key, SourceSequence: p.sequence}
	case adapter.ContextGuardCompactionCompleted:
		event = contextguard.Event{Kind: contextguard.EventProviderCompactionCompleted, At: p.at, Key: s.key, SourceSequence: p.sequence}
	default:
		return
	}
	s.apply(event, &notification)
}

func (s *contextGuardSession) apply(event contextguard.Event, notification *adapter.ContextGuardNotification) {
	s.applyReturning(event, notification)
}

// applyReturning is apply with the reducer's verdict surfaced, for the dispatch
// path: the write-boundary callback must know whether the executing transition
// really committed, and the promotion path must see RequestDispatch. applied is
// false for a rejection or a durability failure; state is the machine AFTER the
// call either way.
func (s *contextGuardSession) applyReturning(event contextguard.Event, notification *adapter.ContextGuardNotification) (state contextguard.Machine, decision contextguard.Decision, applied bool) {
	s.stateMu.Lock()
	if s.persistBlocked {
		state = s.machine
		s.stateMu.Unlock()
		return state, contextguard.Decision{}, false
	}
	next, decision := contextguard.Reduce(s.machine, event)
	if decision.Rejected != contextguard.RejectNone {
		state = s.machine
		s.stateMu.Unlock()
		return state, decision, false
	}
	if decision.Persist {
		if err := s.persistFn(next); err != nil {
			s.blockStateWrite()
			state = s.machine
			s.stateMu.Unlock()
			log.Printf("skeleton: context guard state persistence failed for session %s", s.id)
			s.manager.changed()
			return state, decision, false
		}
	}
	s.machine = next
	s.refreshView(notification)
	state = s.machine
	s.stateMu.Unlock()
	s.manager.changed()
	return state, decision, true
}

// noteStatus is the assembly's status-edge nudge (the supervisor's signal
// precedent): the WORKER re-examines promotion on its own goroutine; nothing is
// parsed or decided on the emitter's path. The wake channel coalesces bursts.
func (m *contextGuardManager) noteStatus(id string) {
	m.mu.Lock()
	s := m.sessions[id]
	m.mu.Unlock()
	if s == nil {
		return
	}
	s.signal()
}

// maybePromote is the ONE producer of EventSessionIdle, and so of the reducer's
// RequestDispatch (ADR-023 amendment 1). It runs on the worker goroutine after
// every drain: a session sitting in pending_idle is promoted the moment the
// assembly reports it quiet AND the promotion's own revalidation still holds. A
// stale observation is a rejection, not an error -- the next usage sample
// retries. Sessions without the dispatch seams (observe-only registrations,
// every pre-dispatch test) never promote.
func (s *contextGuardSession) maybePromote() {
	if s.conn == nil || s.lane == nil || s.quiet == nil || !s.action.AutomaticDispatch {
		return
	}
	s.stateMu.Lock()
	pending := !s.persistBlocked && s.machine.State == contextguard.StatePendingIdle
	s.stateMu.Unlock()
	if !pending || !s.quiet() {
		return
	}
	_, decision, applied := s.applyReturning(contextguard.Event{Kind: contextguard.EventSessionIdle, At: time.Now(), Key: s.key}, nil)
	if applied && decision.RequestDispatch {
		s.dispatchCompaction()
	}
}

// dispatchCompaction performs one automatic compaction under the daemon's OWN
// serialization -- the provider has none (the 2026-09-01 gates: a compact sent
// mid-turn cancels the turn; two concurrent compacts interrupt each other).
//
//   - It joins the session's composer lane, so it is FIFO-ordered against every
//     daemon-driven send, Stop, and approval the daemon can originate.
//   - At the queue head it REVALIDATES quiet (busy -> Prepared degrades to
//     pending_idle and a later idle edge retries) and the lane's own unresolved-
//     outcome flag.
//   - The reducer's executing transition is made durable INSIDE the provider
//     client's write boundary: a refused or unpersistable transition aborts the
//     call with provably no bytes written.
//   - Once bytes may have left, every failure is an unknown outcome and a hold;
//     compaction is non-idempotent and is never blindly repeated (D5).
func (s *contextGuardSession) dispatchCompaction() {
	lane := s.lane()
	if lane == nil {
		return
	}
	// The lane entry is made interruptible (a session close must not wait behind
	// a busy composer queue): a worker told to stop while queued hands its
	// eventual ticket straight back.
	entered := make(chan uint64, 1)
	go func() {
		entered <- lane.enter()
	}()
	var admittedBarrier uint64
	select {
	case admittedBarrier = <-entered:
		// Both may be ready at once and select chooses randomly: a stopping
		// worker must never proceed to the write, so stop is re-checked after
		// winning the ticket.
		select {
		case <-s.stop:
			lane.leave()
			return
		default:
		}
	case <-s.stop:
		go func() {
			<-entered
			lane.leave()
		}()
		return
	}
	// The lane is held across the WRITE BOUNDARY only: holding it through the
	// reply wait would stall every phone send behind a wedged provider for the
	// full dispatch timeout. The compaction's EFFECT window -- write until
	// confirmed, held, or latched -- is covered by compactionInFlight instead:
	// composerSend refuses (retryable) and the supervisor defers while it is
	// true, so releasing the ticket here admits no daemon-originated stimulus
	// into the running compaction.
	left := false
	leaveLane := func() {
		if !left {
			left = true
			lane.leave()
		}
	}
	defer leaveLane()
	// Queue-head revalidation. The quiet/uncertain predicates read the folded
	// core status; pendingEvidence reads the guard's OWN queue -- veto evidence
	// (a compaction item, a settings change, even a trailing usage frame) that
	// arrived during the lane wait must win over a promotion decided before it.
	// barrierChanged means a Stop was admitted while this dispatch was queued
	// (the world the promotion was decided in is gone), and current re-proves
	// the backend identity registration established.
	if lane.barrierChanged(admittedBarrier) || !s.quiet() ||
		(s.uncertain != nil && s.uncertain()) || (s.current != nil && !s.current()) || s.pendingEvidence() {
		s.applyReturning(contextguard.Event{Kind: contextguard.EventSessionBusy, At: time.Now(), Key: s.key}, nil)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), contextGuardDispatchTimeout)
	defer cancel()
	// A stopping session must not sit out the reply wait: the context dies with
	// the worker, and a cancellation after the write is an unknown outcome --
	// exactly what crash recovery would have concluded.
	go func() {
		select {
		case <-s.stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	wrote := false
	var res json.RawMessage
	err := s.conn.CallAtWriteBoundary(ctx, s.action.Method,
		map[string]any{s.action.ThreadIDParameter: s.key.ProviderThreadID}, &res,
		func() error {
			// Last look before bytes (running under the connection's write lock,
			// so nothing can interpose after it): a settings revision the worker
			// has not yet reduced (the enqueued disable that outran the
			// dispatch), evidence that arrived since the queue head, or a
			// backend replaced under the prepared dispatch refuses the write
			// entirely -- provably no bytes follow (the appserver contract).
			if s.settingsRevision.Load() != s.configRevision() || s.pendingEvidence() || (s.current != nil && !s.current()) {
				return errContextGuardDispatchRefused
			}
			state, _, applied := s.applyReturning(contextguard.Event{Kind: contextguard.EventDispatchStarted, At: time.Now(), Key: s.key}, nil)
			if !applied || state.State != contextguard.StateExecuting {
				return errContextGuardDispatchRefused
			}
			return nil
		},
		func() {
			wrote = true
			s.applyReturning(contextguard.Event{Kind: contextguard.EventActionWritten, At: time.Now(), Key: s.key}, nil)
			// Arm the continuation: only a compact THIS guard wrote resumes the
			// task afterwards (afterWrite runs on the worker goroutine, which
			// also consumes the flag). A native or manual compaction never arms.
			s.continuationArmed = true
			leaveLane() // bytes are on the wire; the reply wait happens outside the lane
		},
	)
	switch {
	case err == nil:
		// awaiting_confirmation: the provider's own compaction lifecycle events
		// arrive through the feed and drive provider_compacting -> latched.
		log.Printf("skeleton: context guard dispatched %s for session %s", s.action.Method, s.id)
	case !wrote:
		// Provably no bytes: the boundary refused (reducer/persistence) or the
		// connection was already closed. Prepared degrades to pending_idle so a
		// later quiet edge retries; a refusal that moved the machine elsewhere
		// leaves it exactly where the reducer put it.
		s.applyReturning(contextguard.Event{Kind: contextguard.EventSessionBusy, At: time.Now(), Key: s.key}, nil)
	default:
		// Bytes may have left: timeout, transport loss, or even a typed provider
		// error after the write. Drain FIRST -- the compaction's own lifecycle
		// events may already sit in the queue while the reply stalled, and a
		// definitive completion must latch, never be discarded into a false
		// unknown. Only a machine still waiting on the write's outcome holds.
		s.drain()
		// The internal drain above may be the LAST activity this cycle sees (a
		// stalled reply whose completion was already queued): the continuation
		// check cannot wait for a wake that may never come.
		s.maybeContinue()
		s.stateMu.Lock()
		waiting := s.machine.State == contextguard.StateExecuting || s.machine.State == contextguard.StateAwaitingConfirmation
		s.stateMu.Unlock()
		if waiting {
			s.applyReturning(contextguard.Event{Kind: contextguard.EventActionOutcomeUnknown, At: time.Now(), Key: s.key}, nil)
			log.Printf("skeleton: context guard dispatch outcome unknown for session %s: %v", s.id, err)
		}
		// provider_compacting: the confirmation deadline arbitrates; latched:
		// the outcome is known and nothing is owed.
	}
}

func (m *contextGuardManager) updateSettings(settings protocol.ContextGuardSettings) {
	m.replaceMu.Lock()
	defer m.replaceMu.Unlock()
	// Store publication and worker publication are separate calls so two owner
	// connections can reach this method out of order. Never let a delayed older
	// completion regress either callback provenance or a queued policy config.
	if settings.Revision <= m.appliedRevision {
		return
	}
	if settings.Revision > m.captureRevision.Load() {
		m.captureRevision.Store(settings.Revision)
	}
	m.mu.Lock()
	sessions := make([]*contextGuardSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()
	at := time.Now()
	for _, s := range sessions {
		s.enqueueConfig(contextGuardConfig(settings), at)
	}
	m.appliedRevision = settings.Revision
}

func (s *contextGuardSession) enqueueConfig(config contextguard.Config, at time.Time) {
	s.queueMu.Lock()
	if s.stopped {
		s.queueMu.Unlock()
		return
	}
	s.nextQueueOrder++
	cfg := config
	s.latestConfig = &contextGuardPending{order: s.nextQueueOrder, at: at, config: &cfg}
	// Publish the revision while holding the queue order lock. Every later frame
	// captures the new revision and is queued after this config event; an earlier
	// frame keeps the old revision and can only be accepted before it.
	s.settingsRevision.Store(config.Revision)
	s.queueMu.Unlock()
	s.signal()
}

func (m *contextGuardManager) view(id string) (protocol.ContextGuardView, bool) {
	m.mu.Lock()
	s := m.sessions[id]
	m.mu.Unlock()
	if s == nil {
		return protocol.ContextGuardView{}, false
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.view, true
}

func (m *contextGuardManager) changed() {
	if m.d != nil && m.d.api != nil {
		m.d.api.pokeWatch()
	}
}

func (s *contextGuardSession) refreshView(notification *adapter.ContextGuardNotification) {
	if notification != nil && notification.Kind == adapter.ContextGuardUsage {
		s.view.UsagePercent = contextGuardDisplayPercent(notification.UsedTokens, notification.ContextLimit)
	}
	s.view.Support = string(s.action.Support)
	s.view.Phase = string(s.machine.State)
	// action_unverified is the observe-only protocol's standing code: it means
	// "no dispatch will occur". Stamping it on a guard that DOES dispatch would
	// invert its meaning for every consumer, so an automatic guard carries no
	// standing error code -- its phase and last result are the story.
	if s.action.AutomaticDispatch {
		s.view.ErrorCode = ""
	} else {
		s.view.ErrorCode = "action_unverified"
	}
	if notification != nil && notification.Kind == adapter.ContextGuardCompactionCompleted {
		s.view.LastResult = "compacted"
	} else if s.machine.State == contextguard.StateLatched && s.view.LastResult == "" {
		s.view.LastResult = "compacted"
	}
}

func contextGuardBackendMethod(method string) bool {
	switch method {
	case contextGuardUsageMethod, "item/started", "item/completed", "thread/compacted":
		return true
	default:
		return false
	}
}

func contextGuardDisplayPercent(used, limit uint64) int {
	if limit == 0 {
		return 0
	}
	percent := float64(used) / float64(limit) * 100
	if math.IsInf(percent, 0) || percent > 999 {
		return 999
	}
	if percent < 0 {
		return 0
	}
	return int(percent)
}

func (s *contextGuardSession) statePath() string {
	if s.manager.stateDir == "" {
		return ""
	}
	return filepath.Join(s.manager.stateDir, s.id, contextGuardStateFile)
}

func (s *contextGuardSession) persist(machine contextguard.Machine) error {
	path := s.statePath()
	if path == "" {
		return nil
	}
	doc := contextGuardStateDocument{
		SchemaVersion: contextGuardStateSchemaVersion, SessionInstance: s.key.SessionInstance,
		SettingsRevision: machine.Config.Revision, State: machine.State,
		TriggerThreshold: machine.TriggerThreshold,
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeSessionStateFile(path, data)
}

func (s *contextGuardSession) restore() {
	path := s.statePath()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil || rejectDuplicateJSONKeys(data) != nil {
		s.blockCorruptState()
		return
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var doc contextGuardStateDocument
	if err := dec.Decode(&doc); err != nil {
		s.blockCorruptState()
		return
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		s.blockCorruptState()
		return
	}
	if doc.SchemaVersion != contextGuardStateSchemaVersion || doc.SessionInstance == "" || !contextGuardStateIsValid(doc.State) ||
		doc.SettingsRevision > s.machine.Config.Revision ||
		(doc.TriggerThreshold != 0 && (doc.TriggerThreshold < contextguard.MinThreshold || doc.TriggerThreshold > contextguard.MaxThreshold)) {
		s.blockCorruptState()
		return
	}
	if doc.SessionInstance != s.key.SessionInstance {
		return // replacement instance: the old lifecycle cannot constrain it
	}
	s.machine.State = doc.State
	s.machine.TriggerThreshold = doc.TriggerThreshold
	next, decision := contextguard.Reduce(s.machine, contextguard.Event{Kind: contextguard.EventRecovery, At: time.Now()})
	if decision.Rejected != contextguard.RejectNone {
		s.blockCorruptState()
		return
	}
	s.machine = next
	if decision.Persist {
		if err := s.persist(next); err != nil {
			s.blockStateWrite()
		}
	}
}

func (s *contextGuardSession) blockCorruptState() {
	s.persistBlocked = true
	s.machine.State = contextguard.StateBlockedCorrupt
	s.view = protocol.ContextGuardView{Support: string(s.action.Support), Phase: string(contextguard.StateBlockedCorrupt), ErrorCode: "state_corrupt"}
}

func (s *contextGuardSession) blockStateWrite() {
	s.persistBlocked = true
	s.machine.State = contextguard.StateBlockedCorrupt
	s.view = protocol.ContextGuardView{Support: string(s.action.Support), Phase: string(contextguard.StateBlockedCorrupt), ErrorCode: "state_write_failed"}
}

func contextGuardStateIsValid(state contextguard.State) bool {
	switch state {
	case contextguard.StateDisabled, contextguard.StateUnsupported, contextguard.StateArmed,
		contextguard.StatePendingIdle, contextguard.StatePrepared, contextguard.StateExecuting,
		contextguard.StateAwaitingConfirmation, contextguard.StateProviderCompacting,
		contextguard.StateLatched, contextguard.StateOutcomeUnknownHold,
		contextguard.StateEventLossHold, contextguard.StateBlockedCorrupt:
		return true
	default:
		return false
	}
}
