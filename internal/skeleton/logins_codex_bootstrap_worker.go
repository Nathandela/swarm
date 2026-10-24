package skeleton

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
)

type codexBootstrapAttempt struct {
	selection codexAuthSelection
	at        time.Time
	count     int
}
type codexBootstrapWorker struct {
	coordinator *codexHandoverCoordinator
	ctx         context.Context
	cancel      context.CancelFunc
	wake, done  chan struct{}
	launch      func(daemon.LaunchSpec) (persist.Meta, error)
	handoverID  string
	attempts    map[string]codexBootstrapAttempt
}

func newCodexBootstrapWorker(c *codexHandoverCoordinator) *codexBootstrapWorker {
	parent := context.Background()
	if c.manager.codexAccess != nil {
		parent = c.manager.codexAccess.ctx
	}
	ctx, cancel := context.WithCancel(parent)
	w := &codexBootstrapWorker{coordinator: c, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), done: make(chan struct{}), attempts: make(map[string]codexBootstrapAttempt)}
	if c.manager.d.core != nil && c.manager.d.api != nil {
		w.launch = c.manager.d.api.Launch
	}

	return w
}
func (c *codexHandoverCoordinator) requestBootstrap() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	if c.bootstrap == nil {
		c.bootstrap = newCodexBootstrapWorker(c)
		go c.bootstrap.run()
	}
	select {
	case c.bootstrap.wake <- struct{}{}:
	default:
	}
}
func (w *codexBootstrapWorker) run() {
	defer close(w.done)
	for {
		select {
		case <-w.ctx.Done():
			return
		case <-w.wake:
			w.step()
		}
	}
}
func (m *loginManager) wakeCodexBootstrap() {
	if m.codexHandover == nil {
		return
	}
	r, err := m.store.Snapshot()
	if err != nil {
		return
	}
	h, ok := r.Handovers[logins.ProviderCodex]
	if !ok {
		return
	}
	for _, b := range h.Bootstrap {
		if b.Phase != "ready" && b.Phase != "failed" && b.Phase != "cancelled" {
			m.codexHandover.requestBootstrap()
			return
		}
	}
}
func (w *codexBootstrapWorker) mutate(id string, change func(logins.Registry, logins.Handover) error) error {
	m := w.coordinator.manager
	m.stepMu.Lock()
	defer m.stepMu.Unlock()
	gate := &m.admission[loginProviderIndex(logins.ProviderCodex)]
	gate.Lock()
	defer gate.Unlock()
	r, err := m.store.Snapshot()
	if err != nil {
		return err
	}
	h, ok := r.Handovers[logins.ProviderCodex]
	if !ok || h.ID != id {
		return logins.ErrConflict
	}
	return change(r, h)
}
func bootstrapRow(h logins.Handover, source string) (logins.BootstrapObligation, bool) {
	for _, b := range h.Bootstrap {
		if b.SourceID == source {
			return b, true
		}
	}
	return logins.BootstrapObligation{}, false
}
func bootstrapNonce(h logins.Handover, b logins.BootstrapObligation) string {
	if b.Launch != nil {
		return b.Launch.ID
	}
	return h.MigrationSwitchID
}
func (w *codexBootstrapWorker) step() {
	if w.ctx.Err() != nil {
		return
	}
	m := w.coordinator.manager
	r, err := m.store.Snapshot()
	if err != nil {
		return
	}
	h, ok := r.Handovers[logins.ProviderCodex]
	if !ok || len(h.Bootstrap) == 0 {
		return
	}
	if w.handoverID != h.ID {
		w.handoverID = h.ID
		w.attempts = make(map[string]codexBootstrapAttempt)
	}
	for _, b := range h.Bootstrap {
		if w.ctx.Err() != nil {
			return
		}
		if b.Phase == "ready" || b.Phase == "failed" || b.Phase == "cancelled" {
			continue
		}
		w.progress(h.ID, b.SourceID)
	}
}
func (w *codexBootstrapWorker) progress(id, sourceID string) {
	m := w.coordinator.manager
	r, err := m.store.Snapshot()
	if err != nil {
		return
	}
	h, ok := r.Handovers[logins.ProviderCodex]
	if !ok || h.ID != id {
		return
	}
	b, ok := bootstrapRow(h, sourceID)
	if !ok {
		return
	}
	if b.Phase == "pending" && b.ChildID == "" && b.Launch == nil {
		w.issue(r, h, b)
		return
	}
	if b.ChildID == "" {
		child, n, found := w.findChild(h, b)
		if !found {
			return
		}
		err = w.mutate(id, func(r logins.Registry, h logins.Handover) error {
			current, ok := bootstrapRow(h, sourceID)
			if !ok || current.ChildID != "" || bootstrapNonce(h, current) != bootstrapNonce(h, b) {
				return logins.ErrConflict
			}
			_, _, err := m.store.RecordCodexBootstrapChild(r.Revision, id, sourceID, claimID(current), n)
			return err
		})
		if err != nil {
			return
		}
		w.republish(n)
		_ = child
		r, _ = m.store.Snapshot()
		h = r.Handovers[logins.ProviderCodex]
		b, _ = bootstrapRow(h, sourceID)
	}
	w.finish(h, b)
}
func claimID(b logins.BootstrapObligation) string {
	if b.Launch != nil {
		return b.Launch.ID
	}
	return ""
}
func (w *codexBootstrapWorker) issue(r logins.Registry, h logins.Handover, b logins.BootstrapObligation) {
	if w.launch == nil {
		return
	}
	m := w.coordinator.manager
	selection, err := selectedCodexAuth(r)
	if err != nil {
		return
	}
	attempt := w.attempts[b.SourceID]
	if attempt.selection != selection {
		attempt = codexBootstrapAttempt{selection: selection}
	}
	if attempt.count >= 3 || !attempt.at.IsZero() && time.Since(attempt.at) < 30*time.Second {
		return
	}
	attempt.at = time.Now()
	w.attempts[b.SourceID] = attempt
	source, ok := m.get(b.SourceID)
	if !ok || source.RosterHidden || source.AgentType != logins.ProviderCodex || source.ConversationID != b.ConversationID {
		return
	}
	if m.qualifyCodexMigration([]persist.Meta{source}) != nil {
		return
	}
	login, ok := loginByID(r, selection.id)
	if !ok || login.LimitKnown && (login.ExhaustedUntil.IsZero() || time.Now().Before(login.ExhaustedUntil)) {
		return
	}
	runtime, err := m.store.Runtime(logins.ProviderCodex)
	if err != nil {
		return
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return
	}
	nonce := hex.EncodeToString(random[:])
	err = w.mutate(h.ID, func(r logins.Registry, h logins.Handover) error {
		if w.ctx.Err() != nil {
			return logins.ErrPending
		}
		current, ok := bootstrapRow(h, b.SourceID)
		now, e := selectedCodexAuth(r)
		if e != nil || now != selection || !ok || current.Phase != "pending" || current.Launch != nil || current.ChildID != "" {
			return logins.ErrConflict
		}
		_, _, e = m.store.ClaimCodexBootstrapLaunch(r.Revision, h.ID, b.SourceID, nonce)
		return e
	})
	if err != nil {
		return
	}
	attempt.count++
	w.attempts[b.SourceID] = attempt
	options := map[string]string{protocol.OptionResumeFrom: m.d.api.endpointID + "/" + source.ID}
	spec := daemon.LaunchSpec{AgentType: source.AgentType, Name: source.Name, Tag: source.Tag, Cwd: source.Cwd, ClientEnv: source.Env, Cols: authRecycleCols, Rows: authRecycleRows, SpawnedFrom: source.SpawnedFrom, SpawnIntent: source.SpawnIntent, Supervision: source.Supervision, LoginRuntime: &runtime, LoginStateRoot: m.d.stateDir, InputEmbargo: nonce, OperationID: nonce, BootstrapFirstAttempt: true, Options: options}
	var launchErr error
	attempted, _ := m.d.withAuthResumeFence(source.ID, func() bool {
		if w.ctx.Err() != nil {
			launchErr = daemon.BootstrapLaunchNotStarted(nonce)
			return true
		}
		_, launchErr = w.launch(spec)
		return true
	})
	if !attempted {
		launchErr = daemon.BootstrapLaunchNotStarted(nonce)
	}
	if daemon.BootstrapLaunchDidNotStart(launchErr, nonce) {
		_ = w.mutate(h.ID, func(r logins.Registry, h logins.Handover) error {
			_, _, e := m.store.ResetCodexBootstrapLaunch(r.Revision, h.ID, b.SourceID, nonce)
			return e
		})
		return
	}
	// An uncertain call is never replayed. Only its persisted lineage can bind it.
	w.progress(h.ID, b.SourceID)
}
func (w *codexBootstrapWorker) findChild(h logins.Handover, b logins.BootstrapObligation) (persist.Meta, logins.NativeMember, bool) {
	m := w.coordinator.manager
	var found persist.Meta
	var member logins.NativeMember
	for _, meta := range m.list() {
		if b.LegacyChildID != "" && meta.ID != b.LegacyChildID {
			continue
		}
		if meta.ID == b.SourceID || b.ChildID != "" && meta.ID != b.ChildID || meta.ResumedFrom != b.SourceID || meta.ConversationID != b.ConversationID || meta.AgentType != logins.ProviderCodex || meta.LoginRuntime == nil || !meta.LoginRuntime.ExternalAuth {
			continue
		}
		nonce := bootstrapNonce(h, b)
		if nonce == "" || meta.InputEmbargo != nonce && ((b.ChildID != meta.ID && b.LegacyChildID != meta.ID) || meta.InputEmbargo != "") {
			continue
		}
		n, err := w.coordinator.bootstrapNative(meta, nonce)
		if err != nil {
			continue
		}
		if b.ChildID != "" && n.NativeIncarnation != b.ChildIncarnation {
			continue
		}
		if found.ID != "" {
			return persist.Meta{}, logins.NativeMember{}, false
		}
		found, member = meta, n
	}
	if b.ChildID == "" && b.LegacyChildID == "" && found.ID != "" {
		for _, retained := range h.Members {
			if retained.Native.ConversationID == b.ConversationID && retained.Native.SessionID != found.ID && retained.Phase != "retired" {
				// Missing roster rows do not retire another possible native owner.
				return persist.Meta{}, logins.NativeMember{}, false
			}
		}
	}
	return found, member, found.ID != ""
}
func (w *codexBootstrapWorker) republish(n logins.NativeMember) {
	m := w.coordinator.manager
	for _, s := range w.coordinator.states() {
		if s.native != n {
			continue
		}
		s.operation.Lock()
		r, err := m.store.Snapshot()
		selection, e := selectedCodexAuth(r)
		s.mu.Lock()
		sequence := s.sequence.Load()
		valid := err == nil && e == nil && s.adopted == selection && s.readySequence == sequence
		current := s.grantCurrent
		s.mu.Unlock()
		if valid && s.current() {
			_ = s.publishProof(w.ctx, backendAuthTokens{selection: selection, current: current}, "adopted", nil, sequence)
		} else {
			s.auth.requestSync()
		}
		s.operation.Unlock()
	}
}
func (w *codexBootstrapWorker) finish(h logins.Handover, b logins.BootstrapObligation) {
	if b.ChildID == "" || w.ctx.Err() != nil {
		return
	}
	m := w.coordinator.manager
	_, native, found := w.findChild(h, b)
	if !found {
		return
	}
	err := w.mutate(h.ID, func(r logins.Registry, current logins.Handover) error {
		row, ok := bootstrapRow(current, b.SourceID)
		if !ok || row.Phase != "bound" || row.ChildID != b.ChildID || row.ChildIncarnation != b.ChildIncarnation || claimID(row) != claimID(b) {
			return logins.ErrPending
		}
		phase := "failed"
		if !w.coordinator.ended(native) {
			child, exists := m.get(row.ChildID)
			proof, e := w.coordinator.bootstrapNative(child, bootstrapNonce(current, row))
			if !exists || e != nil || proof != native || child.ResumedFrom != row.SourceID || child.ConversationID != row.ConversationID {
				return logins.ErrPending
			}
			ready := false
			if m.d.core != nil {
				ready = m.d.loginRecoveryReady(child)
			} else {
				ready = m.d.authw != nil && m.d.authw.ready != nil && m.d.authw.ready(child)
			}
			if !ready || m.codexAdmission(child.ID, r) != nil {
				return logins.ErrPending
			}
			adopted := false
			for _, member := range current.Members {
				if member.Native == native && member.Phase == "adopted" {
					adopted = true
				}
			}
			if !adopted {
				return logins.ErrPending
			}
			selection, e := selectedCodexAuth(r)
			if e != nil {
				return e
			}
			var releaseErr error
			attempted, _ := m.d.withAuthResumeFence(child.ID, func() bool {
				latest, exists := m.get(child.ID)
				proof, e := w.coordinator.bootstrapNative(latest, bootstrapNonce(current, row))
				if !exists || e != nil || proof != native || latest.ResumedFrom != row.SourceID || latest.ConversationID != row.ConversationID || m.codexAdmission(child.ID, r) != nil {
					releaseErr = logins.ErrPending
					return true
				}
				if latest.InputEmbargo != "" {
					if latest.InputEmbargo != bootstrapNonce(current, row) || m.release == nil {
						releaseErr = logins.ErrConflict
					} else {
						releaseErr = m.release(child.ID, latest.InputEmbargo)
					}
				}
				return true
			})
			if !attempted || releaseErr != nil {
				return logins.ErrPending
			}
			latest, e := m.store.Snapshot()
			now, selectionErr := selectedCodexAuth(latest)
			if e != nil || selectionErr != nil || now != selection || m.codexAdmission(child.ID, latest) != nil {
				return logins.ErrPending
			}
			// The bounded shim acknowledgment sends no prompt. A lost readiness proof
			// retains this binding for revalidation rather than publishing completion.
			r = latest
			phase = "ready"
		}
		_, _, e := m.store.RecordCodexBootstrapOutcome(r.Revision, current.ID, row.SourceID, claimID(row), phase)
		return e
	})
	if err == nil {
		m.d.clearAuthRecycle(b.SourceID)
		m.d.clearAuthRecycle(b.ChildID)
	}
}
