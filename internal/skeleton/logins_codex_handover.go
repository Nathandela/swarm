package skeleton

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/Nathandela/swarm/internal/status"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
)

type codexScopedUsage struct {
	order   uint64
	allowed bool
	limits  codexQuotaSnapshot
	weekly  *protocol.LoginUsageWindow
}
type codexHandoverFeed struct {
	auth                               *backendAuthCoordinator
	current                            func() bool
	publish                            func(codexAuthSelection, string, *codexScopedUsage) error
	operation                          sync.Mutex
	mu                                 sync.Mutex
	installed, adopted, readySelection codexAuthSelection
	grantCurrent                       func() bool
	ready                              bool
	readySequence                      uint64
	target                             codexAuthSelection
	hintPending                        bool
	publishCurrent                     func() bool
	publishFence                       func() bool
	sequence                           atomic.Uint64
	reserveRead                        func() uint64
	attempts                           int
	slots                              chan struct{}
	native                             logins.NativeMember
	feed                               *backendFeed
	lastAttempt                        time.Time
	cancelRead                         context.CancelFunc
}

func newCodexHandoverFeed(auth *backendAuthCoordinator, current func() bool, publish func(codexAuthSelection, string, *codexScopedUsage) error) *codexHandoverFeed {
	baseCurrent := auth.current
	exactCurrent := func() bool { return auth.ctx.Err() == nil && baseCurrent() && current() }
	s := &codexHandoverFeed{auth: auth, current: exactCurrent, publish: publish}
	auth.current = exactCurrent
	auth.handover = s
	auth.synchronize = s.synchronize
	auth.onTokenChange = func() { s.invalidate(); auth.requestSync() }
	return s
}
func (s *codexHandoverFeed) invalidate() {
	s.mu.Lock()
	s.sequence.Add(1)
	s.ready = false
	s.attempts = 0
	if s.cancelRead != nil {
		s.cancelRead()
	}
	s.mu.Unlock()
}
func (s *codexHandoverFeed) readyFor(selection codexAuthSelection) bool {
	s.mu.Lock()
	ready := s.ready && s.readySelection == selection && s.readySequence == s.sequence.Load()
	current := s.grantCurrent
	s.mu.Unlock()
	return ready && s.current() && (current == nil || current())
}
func (s *codexHandoverFeed) adoptedSelection() codexAuthSelection {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.adopted
}
func (s *codexHandoverFeed) synchronize(ctx context.Context) error {
	s.operation.Lock()
	defer s.operation.Unlock()
	if s.slots != nil {
		select {
		case s.slots <- struct{}{}:
			defer func() { <-s.slots }()
		case <-ctx.Done():
			return errBackendAuthUnavailable
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	stop := context.AfterFunc(s.auth.ctx, cancel)
	defer stop()
	defer cancel()
	s.mu.Lock()
	s.cancelRead = cancel
	s.lastAttempt = time.Now()
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.cancelRead = nil; s.mu.Unlock() }()
	s.mu.Lock()
	s.ready = false
	s.attempts++
	s.mu.Unlock()
	if ctx.Err() != nil || !s.current() {
		return errBackendAuthUnavailable
	}
	revision := s.auth.revision.Load()
	token, err := s.auth.access(ctx, false)
	if err != nil || token.selection.id == "" || !s.auth.tokenCurrent(ctx, token) || !s.current() {
		return errBackendAuthUnavailable
	}
	s.mu.Lock()
	installed := s.installed == token.selection
	s.mu.Unlock()
	if !installed {
		if s.auth.install(ctx, token, revision) != nil || !s.current() {
			return errBackendAuthUnavailable
		}
		s.mu.Lock()
		s.installed = token.selection
		s.mu.Unlock()
		if s.publishProof(ctx, token, "acknowledged", nil, s.sequence.Load()) != nil {
			return errBackendAuthUnavailable
		}
	}
	sequence := s.sequence.Load()
	var order uint64
	var raw json.RawMessage
	var release func()
	err = s.auth.conn.CallAtWriteBoundary(ctx, "account/rateLimits/read", map[string]bool{"excludeResetCreditDetails": true}, &raw, func() error {
		if sequence != s.sequence.Load() || !s.current() {
			return errBackendAuthUnavailable
		}
		var ok bool
		release, ok = s.auth.tokenGuard(ctx, token)
		if !ok {
			return errBackendAuthUnavailable
		}
		if s.reserveRead != nil {
			order = s.reserveRead()
		}
		return nil
	}, func() {
		if release != nil {
			release()
			release = nil
		}
	})
	if release != nil {
		release()
	}
	if err != nil || sequence != s.sequence.Load() || !s.current() || !s.auth.tokenCurrent(ctx, token) {
		return errBackendAuthUnavailable
	}
	usage, ok := decodeCodexScopedUsage(raw, token.AccountID, time.Now())
	if !ok {
		return errBackendAuthUnavailable
	}
	usage.order = order
	if s.publishProof(ctx, token, "adopted", &usage, sequence) != nil {
		return errBackendAuthUnavailable
	}
	if sequence != s.sequence.Load() || !s.current() || !s.auth.tokenCurrent(ctx, token) {
		return errBackendAuthUnavailable
	}
	s.mu.Lock()
	if sequence != s.sequence.Load() || ctx.Err() != nil {
		s.mu.Unlock()
		return errBackendAuthUnavailable
	}
	s.readySequence = sequence
	s.adopted = token.selection
	s.readySelection = token.selection
	s.ready = usage.allowed
	s.grantCurrent = token.current
	s.mu.Unlock()
	return nil
}
func decodeCodexScopedUsage(raw json.RawMessage, account string, now time.Time) (codexScopedUsage, bool) {
	var out codexScopedUsage
	if len(raw) > 1<<20 || rejectDuplicateJSONKeys(raw) != nil {
		return out, false
	}
	object, ok := decodeStrictObject(raw)
	if !ok {
		return out, false
	}
	id, ok := strictJSONString(object["accountId"])
	if !ok || id != account {
		return out, false
	}
	allowed := object["ordinaryUsageAllowed"]
	if string(allowed) != "true" && string(allowed) != "false" {
		return out, false
	}
	out.allowed = string(allowed) == "true"
	if limits, present := object["rateLimits"]; present && string(limits) != "null" {
		if json.Unmarshal(limits, &out.limits) != nil {
			return codexScopedUsage{}, false
		}
		var windows struct {
			Primary, Secondary *struct {
				Used    *float64 `json:"usedPercent"`
				Minutes int      `json:"windowDurationMins"`
				Reset   int64    `json:"resetsAt"`
			}
		}
		if json.Unmarshal(limits, &windows) != nil {
			return codexScopedUsage{}, false
		}
		for _, w := range []*struct {
			Used    *float64 `json:"usedPercent"`
			Minutes int      `json:"windowDurationMins"`
			Reset   int64    `json:"resetsAt"`
		}{windows.Primary, windows.Secondary} {
			if w == nil || w.Minutes != 7*24*60 {
				continue
			}
			if out.weekly != nil || w.Used == nil {
				return codexScopedUsage{}, false
			}
			out.weekly = nativeUsageWindow(*w.Used, w.Reset, now, now)
			if out.weekly == nil {
				return codexScopedUsage{}, false
			}
		}
	}
	return out, true
}

// One provider cadence for unsolicited usage hints; every native member still
// performs its own initial account-API adoption read.
type codexHandoverCoordinator struct {
	manager         *loginManager
	mu              sync.Mutex
	closed          bool
	feeds           map[*backendFeed]*codexHandoverFeed
	slots           chan struct{}
	pollSelection   codexAuthSelection
	pollAt          time.Time
	members         func() ([]logins.NativeMember, error)
	ended           func(logins.NativeMember) bool
	usageSelection  codexAuthSelection
	usageAllowed    bool
	usageValid      bool
	attachWG        sync.WaitGroup
	readOrder       atomic.Uint64
	usageOrder      uint64
	afterUsageWrite func()
	bootstrap       *codexBootstrapWorker
	bootstrapNative func(persist.Meta, string) (logins.NativeMember, error)
}

func newCodexHandoverCoordinator(m *loginManager) *codexHandoverCoordinator {
	c := &codexHandoverCoordinator{manager: m, feeds: make(map[*backendFeed]*codexHandoverFeed), slots: make(chan struct{}, 4)}
	c.members = m.codexNativeMembers
	c.ended = codexNativeEnded
	c.bootstrapNative = func(meta persist.Meta, incident string) (logins.NativeMember, error) {
		return m.d.codexBootstrapNative(meta, incident)
	}
	return c
}

// Stop replacement launches before assembly hook/supervision drains begin.
func (c *codexHandoverCoordinator) stopBootstrap() {
	c.mu.Lock()
	c.closed = true
	worker := c.bootstrap
	c.mu.Unlock()
	if worker != nil {
		worker.cancel()
		<-worker.done
	}
}
func (c *codexHandoverCoordinator) close() {
	c.stopBootstrap()
	c.mu.Lock()
	feeds := make([]*codexHandoverFeed, 0, len(c.feeds))
	for _, s := range c.feeds {
		feeds = append(feeds, s)
	}
	c.mu.Unlock()
	c.attachWG.Wait()
	for _, s := range feeds {
		s.auth.close()
	}
}
func (c *codexHandoverCoordinator) states() []*codexHandoverFeed {
	c.mu.Lock()
	defer c.mu.Unlock()
	states := make([]*codexHandoverFeed, 0, len(c.feeds))
	for _, s := range c.feeds {
		states = append(states, s)
	}
	return states
}
func (m *loginManager) attachCodexHandover(local, instance string, feed *backendFeed, auth *backendAuthCoordinator) error {
	if m.codexHandover == nil {
		return errBackendAuthUnavailable
	}
	c := m.codexHandover
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errBackendAuthUnavailable
	}
	c.attachWG.Add(1)
	c.mu.Unlock()
	defer c.attachWG.Done()
	if err := m.prepareCodexSelection(); err != nil {
		return errBackendAuthUnavailable
	}
	meta, ok := m.get(local)
	if !ok || meta.LoginRuntime == nil || !meta.LoginRuntime.ExternalAuth {
		return errBackendAuthUnavailable
	}
	feed.codexExecution.mu.Lock()
	captured := feed.codexExecution.captured
	native := feed.codexExecution.native
	cli := feed.codexExecution.cli
	shimPID, shimStart := feed.codexExecution.shimPID, feed.codexExecution.shimStart
	endpoint := feed.codexExecution.endpoint
	feed.codexExecution.mu.Unlock()
	if !captured {
		return errBackendAuthUnavailable
	}
	baseCurrent := auth.current
	current := func() bool {
		conn := auth.connection()
		if conn == nil || !baseCurrent() || !m.d.backendIngressCurrent(local, instance, feed) {
			return false
		}
		latest, proof, err := m.d.readCodexExecutionNative(local, instance, native.SocketPath)
		return err == nil && proof == native && latest.ShimPID == shimPID && latest.ShimStartTime == shimStart && latest.CLIIdentity != nil && *latest.CLIIdentity == cli && codexExecutionPeerMatches(conn, native.PID, endpoint)
	}
	var s *codexHandoverFeed
	s = newCodexHandoverFeed(auth, current, func(selection codexAuthSelection, phase string, usage *codexScopedUsage) error {
		return m.publishCodexAdoption(s, selection, phase, usage)
	})
	s.native = logins.NativeMember{SessionID: local, NativeIncarnation: fmt.Sprintf("%d:%d", native.PID, native.StartedAtMS), ConversationID: meta.ConversationID, NativePID: native.PID, NativeStartTime: native.StartedAtMS}
	s.feed = feed
	s.slots = c.slots
	s.reserveRead = func() uint64 { return c.readOrder.Add(1) }
	r, err := m.store.Snapshot()
	if err != nil {
		return errBackendAuthUnavailable
	}
	selection, err := selectedCodexAuth(r)
	if err != nil {
		return errBackendAuthUnavailable
	}
	s.target = selection
	auth.onClosed = func() { c.mu.Lock(); delete(c.feeds, feed); c.mu.Unlock() }
	c.mu.Lock()
	if c.closed || len(c.feeds) >= logins.MaxHandoverMembers {
		c.mu.Unlock()
		return errBackendAuthUnavailable
	}
	c.feeds[feed] = s
	c.mu.Unlock()
	return nil
}
func (m *loginManager) codexNativeMembers() ([]logins.NativeMember, error) {
	var members []logins.NativeMember
	for _, meta := range m.list() {
		if meta.AgentType != logins.ProviderCodex || meta.Status.Process != status.ProcessRunning {
			continue
		}
		if meta.LoginRuntime == nil || !meta.LoginRuntime.ExternalAuth || m.d.core == nil {
			return nil, logins.ErrPending
		}
		instance, ok := m.d.sessionInstance(meta.ID)
		channel, planned := m.d.core.SessionBackend(meta.ID)
		if !ok || !planned {
			return nil, logins.ErrPending
		}
		_, native, err := m.d.readCodexExecutionNative(meta.ID, instance, channel.SocketPath)
		if err != nil {
			return nil, logins.ErrPending
		}
		members = append(members, logins.NativeMember{SessionID: meta.ID, NativeIncarnation: fmt.Sprintf("%d:%d", native.PID, native.StartedAtMS), ConversationID: meta.ConversationID, NativePID: native.PID, NativeStartTime: native.StartedAtMS})
	}
	return members, nil
}
func codexNativeEnded(n logins.NativeMember) bool {
	err := syscall.Kill(n.NativePID, 0)
	if errors.Is(err, syscall.ESRCH) {
		return true
	}
	if err != nil {
		return false
	}
	start, err := procstart.StartTime(n.NativePID)
	return err == nil && start != n.NativeStartTime
}
func handoverSwitch(h logins.Handover) logins.Switch {
	return logins.Switch{ID: h.ID, Provider: logins.ProviderCodex, FromID: h.FromID, ToID: h.ToID, ToEpoch: h.Epoch, Phase: h.Phase}
}

// Called with the provider admission fence; durable desired intent precedes signals.
func (m *loginManager) beginCodexHandover(r logins.Registry, toID string) (logins.Switch, error) {
	if m.codexHandover == nil {
		m.codexHandover = newCodexHandoverCoordinator(m)
	}
	c := m.codexHandover
	var h logins.Handover
	var err error
	if existing, pending := r.Handovers[logins.ProviderCodex]; pending {
		target, ok := loginByID(r, toID)
		if !ok {
			return logins.Switch{}, logins.ErrIneligible
		}
		if existing.ToID == toID && existing.DestinationCredentialRevision == target.CredentialRevision {
			c.signalSelection()
			return handoverSwitch(existing), nil
		}
		r, h, err = m.store.RetargetCodexHandover(r.Revision, existing.ID, toID)
	} else {
		members, e := c.members()
		if e != nil {
			return logins.Switch{}, e
		}
		p := r.Providers[logins.ProviderCodex]
		if p.ActiveID != "" && p.DesiredCredentialRevision == 0 {
			r, h, err = m.store.BootstrapCodexHandover(r.Revision, members)
			if err == nil && h.ToID != toID {
				r, h, err = m.store.RetargetCodexHandover(r.Revision, h.ID, toID)
			}
		} else {
			target, ok := loginByID(r, toID)
			if !ok {
				return logins.Switch{}, logins.ErrIneligible
			}
			if p.ActiveID == toID && p.DesiredCredentialRevision == target.CredentialRevision {
				c.signalSelection()
				return logins.Switch{}, nil
			}
			r, h, err = m.store.BeginCodexHandover(r.Revision, toID, members)
		}
	}
	if err != nil {
		return logins.Switch{}, err
	}
	_ = r
	c.signalSelection()
	return handoverSwitch(h), nil
}
func (c *codexHandoverCoordinator) signalSelection() {
	r, err := c.manager.store.Snapshot()
	if err != nil {
		return
	}
	selection, err := selectedCodexAuth(r)
	if err != nil {
		return
	}
	for _, s := range c.states() {
		s.selectTarget(selection, true)
	}
}
func (s *codexHandoverFeed) selectTarget(selection codexAuthSelection, force bool) {
	s.mu.Lock()
	if s.target == selection && !force {
		s.mu.Unlock()
		return
	}
	s.target = selection
	s.sequence.Add(1)
	s.ready = false
	s.attempts = 0
	if s.cancelRead != nil {
		s.cancelRead()
	}
	s.mu.Unlock()
	s.auth.requestSync()
}

func (m *loginManager) stepCodexHandover() {
	if m.codexHandover == nil {
		return
	}
	c := m.codexHandover
	gate := &m.admission[loginProviderIndex(logins.ProviderCodex)]
	gate.Lock()
	r, err := m.store.Snapshot()
	if err != nil || r.MigrationPending || r.Providers[logins.ProviderCodex].AuthOwnerMode != logins.CodexNativeGrantMode {
		gate.Unlock()
		return
	}
	if _, legacy := r.Switches[logins.ProviderCodex]; legacy {
		gate.Unlock()
		return
	}
	if h, pending := r.Handovers[logins.ProviderCodex]; pending {
		for _, member := range h.Members {
			if member.Phase != "retired" && c.ended(member.Native) {
				r, h, err = m.store.RecordCodexReceipt(r.Revision, logins.HandoverReceipt{HandoverID: h.ID, ToID: h.ToID, Epoch: h.Epoch, DestinationCredentialRevision: h.DestinationCredentialRevision, Native: member.Native, Phase: "retired"})
				if err != nil {
					break
				}
			}
		}
		if err == nil && h.Phase == "adopted" {
			r, err = m.store.CompleteCodexHandover(r.Revision, h.ID)
		}
	}
	if err == nil {
		p := r.Providers[logins.ProviderCodex]
		active, ok := loginByID(r, p.ActiveID)
		if ok && (p.DesiredCredentialRevision == 0 || p.DesiredCredentialRevision != active.CredentialRevision) {
			_, _ = m.beginCodexHandover(r, p.ActiveID)
			r, _ = m.store.Snapshot()
		}
	}
	selection, selectedErr := selectedCodexAuth(r)
	gate.Unlock()
	if err != nil || selectedErr != nil {
		return
	}
	for _, s := range c.states() {
		if !s.current() {
			continue
		}
		s.mu.Lock()
		changed := s.target != selection
		retry := !s.ready && s.attempts < 3 && (time.Since(s.lastAttempt) >= 30*time.Second || s.hintPending && time.Since(s.lastAttempt) >= time.Second)
		if retry {
			s.hintPending = false
		}
		s.mu.Unlock()
		if changed {
			s.selectTarget(selection, false)
		} else if retry {
			s.auth.requestSync()
		}
	}
	c.mu.Lock()
	blocked := c.usageSelection == selection && !c.usageAllowed
	c.mu.Unlock()
	if blocked {
		m.noteCodexHint("", false)
	}
}
func (m *loginManager) codexAdmission(local string, r logins.Registry) error {
	if m.codexHandover == nil {
		return logins.ErrPending
	}
	selection, err := selectedCodexAuth(r)
	if err != nil {
		return logins.ErrPending
	}
	c := m.codexHandover
	c.mu.Lock()
	available := !c.closed && c.usageSelection == selection && c.usageAllowed
	c.mu.Unlock()
	if !available {
		return logins.ErrPending
	}
	login, ok := loginByID(r, selection.id)
	if !ok || login.LimitKnown && (login.ExhaustedUntil.IsZero() || time.Now().Before(login.ExhaustedUntil)) {
		return logins.ErrPending
	}
	for _, s := range m.codexHandover.states() {
		if s.native.SessionID == local && s.feed != nil && s.feed.ingressAutomationSafe() && s.readyFor(selection) {
			return nil
		}
	}
	return fmt.Errorf("%w: Codex account readiness is pending; this message was not sent", logins.ErrPending)
}
func sameNativeOwner(a, b logins.NativeMember) bool {
	return a.SessionID == b.SessionID && a.NativeIncarnation == b.NativeIncarnation && a.NativePID == b.NativePID && a.NativeStartTime == b.NativeStartTime && (a.ConversationID == "" || b.ConversationID == "" || a.ConversationID == b.ConversationID)
}
func (m *loginManager) publishCodexAdoption(s *codexHandoverFeed, selection codexAuthSelection, phase string, usage *codexScopedUsage) error {
	if usage != nil && usage.order == 0 {
		return errBackendAuthUnavailable
	}
	gate := &m.admission[loginProviderIndex(logins.ProviderCodex)]
	gate.Lock()
	defer gate.Unlock()
	if !s.current() || !s.publicationCurrent() {
		return errBackendAuthUnavailable
	}
	for attempt := 0; attempt < 3; attempt++ {
		if !s.publicationCurrent() {
			return errBackendAuthUnavailable
		}
		r, err := m.store.Snapshot()
		if err != nil {
			return errBackendAuthUnavailable
		}
		current, err := selectedCodexAuth(r)
		if err != nil || current != selection {
			return errBackendAuthUnavailable
		}
		if h, pending := r.Handovers[logins.ProviderCodex]; pending {
			for _, member := range h.Members {
				if sameNativeOwner(member.Native, s.native) {
					if member.Phase == "retired" {
						return errBackendAuthUnavailable
					}
					r, _, err = m.store.RecordCodexReceipt(r.Revision, logins.HandoverReceipt{HandoverID: h.ID, ToID: h.ToID, Epoch: h.Epoch, DestinationCredentialRevision: h.DestinationCredentialRevision, Native: member.Native, Phase: phase})
					break
				}
			}
			if errors.Is(err, logins.ErrConflict) {
				continue
			}
			if err != nil {
				return errBackendAuthUnavailable
			}
		}
		if usage != nil {
			if !s.publicationCurrent() {
				return errBackendAuthUnavailable
			}
			c := m.codexHandover
			c.mu.Lock()
			stale := c.usageSelection == selection && usage.order < c.usageOrder
			c.mu.Unlock()
			if stale {
				return errBackendAuthUnavailable
			}
			login, ok := loginByID(r, selection.id)
			if !ok {
				return errBackendAuthUnavailable
			}
			if usage.allowed {
				_, err = m.store.UpdateAvailability(r.Revision, selection.id, logins.AuthReady, false, time.Time{})
			} else {
				_, err = m.store.UpdateExhaustion(r.Revision, selection.id, usage.limits.reset(time.Now()), usage.limits.nonRolling())
			}
			if errors.Is(err, logins.ErrConflict) {
				continue
			}
			if err != nil {
				return errBackendAuthUnavailable
			}
			if c.afterUsageWrite != nil {
				c.afterUsageWrite()
			}
			valid := s.publicationCurrent() && s.current()
			s.mu.Lock()
			valid = valid && s.publishFence != nil && s.publishFence()
			c.mu.Lock()
			c.usageSelection = selection
			c.usageOrder = usage.order
			c.usageAllowed = valid && usage.allowed
			c.usageValid = valid
			c.mu.Unlock()
			s.mu.Unlock()
			if !valid {
				// Keep admission and automatic selection fenced during restoration.
				for retry := 0; retry < 3; retry++ {
					latest, readErr := m.store.Snapshot()
					if readErr != nil {
						break
					}
					latestSelection, selectedErr := selectedCodexAuth(latest)
					latestLogin, exists := loginByID(latest, selection.id)
					if selectedErr != nil || latestSelection != selection || !exists || latestLogin.CredentialRevision != login.CredentialRevision || latestLogin.Identity != login.Identity {
						break
					}
					var restoreErr error
					if login.LimitKnown {
						_, restoreErr = m.store.UpdateExhaustion(latest.Revision, selection.id, login.ExhaustedUntil, login.LimitNonRolling)
					} else {
						_, restoreErr = m.store.UpdateAvailability(latest.Revision, selection.id, login.AuthState, false, time.Time{})
					}
					if !errors.Is(restoreErr, logins.ErrConflict) {
						break
					}
				}
				return errBackendAuthUnavailable
			}
			meta, ok := m.get(s.native.SessionID)
			if !ok {
				return errBackendAuthUnavailable
			}
			runtime := logins.Runtime{Provider: logins.ProviderCodex, Epoch: selection.epoch, Identity: login.Identity, Home: m.store.Home(logins.ProviderCodex), ExternalAuth: true}
			meta.LoginRuntime = &runtime
			m.recordLoginUsage(meta, nil, usage.weekly)
		}
		return nil
	}
	return errBackendAuthUnavailable
}
func (m *loginManager) noteCodexHint(local string, force bool) {
	if m == nil || m.codexHandover == nil {
		return
	}
	c := m.codexHandover
	r, err := m.store.Snapshot()
	if err != nil {
		return
	}
	selection, err := selectedCodexAuth(r)
	if err != nil {
		return
	}
	if !force {
		c.mu.Lock()
		recent := c.pollSelection == selection && time.Since(c.pollAt) < 30*time.Second
		c.mu.Unlock()
		if recent {
			return
		}
	}
	var chosen *codexHandoverFeed
	for _, s := range c.states() {
		if force && s.native.SessionID != local {
			continue
		}
		if !s.current() {
			continue
		}
		if force {
			chosen = s
			break
		}
		if s.adoptedSelection() != selection {
			continue
		}
		if chosen == nil {
			chosen = s
		}
		if s.readyFor(selection) {
			chosen = s
			break
		}
	}
	if chosen == nil {
		return
	}
	if !force {
		c.mu.Lock()
		if c.pollSelection == selection && time.Since(c.pollAt) < 30*time.Second {
			c.mu.Unlock()
			return
		}
		c.pollSelection = selection
		c.pollAt = time.Now()
		c.mu.Unlock()
	}
	chosen.mu.Lock()
	if chosen.ready || force {
		chosen.sequence.Add(1)
		chosen.ready = false
		if chosen.cancelRead != nil {
			chosen.cancelRead()
		}
	}
	recent := time.Since(chosen.lastAttempt) < time.Second
	if recent {
		chosen.hintPending = true
	} else {
		chosen.attempts = 0
	}
	chosen.mu.Unlock()
	if !recent {
		chosen.auth.requestSync()
	}
}

func (m *loginManager) prepareCodexSelection() error {
	gate := &m.admission[loginProviderIndex(logins.ProviderCodex)]
	gate.Lock()
	defer gate.Unlock()
	r, err := m.store.Snapshot()
	if err != nil || r.MigrationPending {
		return errBackendAuthUnavailable
	}
	if _, legacy := r.Switches[logins.ProviderCodex]; legacy {
		return errBackendAuthUnavailable
	}
	p := r.Providers[logins.ProviderCodex]
	active, ok := loginByID(r, p.ActiveID)
	if !ok || p.AuthOwnerMode != logins.CodexNativeGrantMode {
		return errBackendAuthUnavailable
	}
	if p.DesiredCredentialRevision == 0 || p.DesiredCredentialRevision != active.CredentialRevision {
		_, err = m.beginCodexHandover(r, active.ID)
		return err
	}
	return nil
}

func (s *codexHandoverFeed) publishProof(ctx context.Context, token backendAuthTokens, phase string, usage *codexScopedUsage, sequence uint64) error {
	grantCurrent := token.current
	s.mu.Lock()
	fence := func() bool { return ctx.Err() == nil && sequence == s.sequence.Load() }
	s.publishFence = fence
	s.publishCurrent = func() bool {
		return fence() && (grantCurrent == nil || grantCurrent())
	}
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.publishCurrent = nil; s.publishFence = nil; s.mu.Unlock() }()
	if !s.publicationCurrent() {
		return errBackendAuthUnavailable
	}
	if s.publish != nil {
		return s.publish(token.selection, phase, usage)
	}
	return nil
}
func (s *codexHandoverFeed) publicationCurrent() bool {
	s.mu.Lock()
	current := s.publishCurrent
	s.mu.Unlock()
	return current != nil && current()
}

func (m *loginManager) codexAutomaticRejectionCurrent(r logins.Registry) bool {
	selection, err := selectedCodexAuth(r)
	if err != nil || m.codexHandover == nil {
		return false
	}
	c := m.codexHandover
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.closed && c.usageSelection == selection && c.usageValid && !c.usageAllowed
}

func (m *loginManager) beginCodexAutomaticHandover(expected logins.Registry) (logins.Switch, error) {
	gate := &m.admission[loginProviderIndex(logins.ProviderCodex)]
	gate.Lock()
	defer gate.Unlock()
	r, err := m.store.Snapshot()
	if err != nil {
		return logins.Switch{}, err
	}
	selection, selectedErr := selectedCodexAuth(r)
	prior, priorErr := selectedCodexAuth(expected)
	if r.Revision != expected.Revision || selectedErr != nil || priorErr != nil || selection != prior {
		return logins.Switch{}, logins.ErrConflict
	}
	if !m.codexAutomaticRejectionCurrent(r) {
		return logins.Switch{}, logins.ErrPending
	}
	active, ok := loginByID(r, selection.id)
	if !ok || !active.LimitKnown || !active.ExhaustedUntil.IsZero() && !time.Now().Before(active.ExhaustedUntil) {
		return logins.Switch{}, logins.ErrPending
	}
	next, err := m.store.NextEligible(logins.ProviderCodex, nil, time.Now())
	if err != nil {
		return logins.Switch{}, err
	}
	return m.beginCodexHandover(r, next.ID)
}
