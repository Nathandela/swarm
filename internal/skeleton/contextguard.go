package skeleton

// The ContextGuard runtime is the daemon-owned bridge between a pure provider
// parser and the pure policy reducer. Backend callbacks only enqueue/coalesce
// evidence and wake one per-session worker; the worker alone performs policy and
// durability work. Automatic provider dispatch is deliberately absent until the
// two concurrency gates in ADR-023 are proven.

import (
	"bytes"
	"encoding/json"
	"errors"
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
)

const (
	contextGuardStateFile          = "context-guard-state.json"
	contextGuardStateSchemaVersion = 1
	contextGuardPendingLimit       = 64
	contextGuardFrameLimit         = 64 << 10
	contextGuardUsageMethod        = "thread/tokenUsage/updated"
)

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
	if d.contextGuards == nil || d.core == nil || backend == nil || backend.feed == nil {
		if backend != nil && backend.feed != nil {
			d.discardContextGuardFeed(backend.feed)
		}
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
	if !d.contextGuards.registerCurrent(local, key, source, action, isCurrent) || !isCurrent() {
		d.contextGuards.unregister(local, key)
		d.discardContextGuardFeed(backend.feed)
		return
	}
	d.activateContextGuardFeed(local, key, backend.feed)
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
	return m.registerCurrent(id, key, source, action, nil)
}

func (m *contextGuardManager) registerCurrent(id string, key contextguard.Key, source adapter.ContextGuardSource, action adapter.ContextGuardAction, current func() bool) bool {
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
	for {
		select {
		case <-s.stop:
			return
		case <-s.wake:
			s.drain()
		}
	}
}

func (s *contextGuardSession) drain() {
	s.queueMu.Lock()
	if s.lost {
		loss := contextGuardPending{sequence: s.lossSequence, at: s.lossAt}
		s.lost = false
		s.latestUsage = nil
		s.latestConfig = nil
		s.lifecycle = nil
		s.queueMu.Unlock()
		s.apply(contextguard.Event{Kind: contextguard.EventEventLoss, At: loss.at, Key: s.key, SourceSequence: loss.sequence}, nil)
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
	s.stateMu.Lock()
	if s.persistBlocked {
		s.stateMu.Unlock()
		return
	}
	next, decision := contextguard.Reduce(s.machine, event)
	if decision.Rejected != contextguard.RejectNone {
		s.stateMu.Unlock()
		return
	}
	if decision.Persist {
		if err := s.persistFn(next); err != nil {
			s.blockStateWrite()
			s.stateMu.Unlock()
			log.Printf("skeleton: context guard state persistence failed for session %s", s.id)
			s.manager.changed()
			return
		}
	}
	s.machine = next
	s.refreshView(notification)
	s.stateMu.Unlock()
	s.manager.changed()
	// decision.RequestDispatch is intentionally ignored. ContextGuardAction for the
	// characterized provider is observe-only until ADR-023's gates are proven.
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
	s.view.ErrorCode = "action_unverified"
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
