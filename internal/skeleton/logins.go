package skeleton

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/adapter/registry"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/loginmigration"
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// The journal owns provider stops exclusively. Generic auth/CLI recovery never
// acquires a second claim for these sessions, including after daemon restart.
type loginManager struct {
	d                 *Daemon
	store             *logins.Store
	stepMu            sync.Mutex
	admission         [2]sync.Mutex
	usageMu           sync.Mutex
	usage             [2]loginUsageSnapshot
	preflight         func(string, []persist.Meta) error
	prepareContext    func(string, []persist.Meta) error
	stopProof         func(persist.Meta) error
	release           func(string, string) error
	recordResumeError func(string, string) error
}

func loginProviderIndex(provider string) int {
	if provider == logins.ProviderClaude {
		return 0
	}
	return 1
}
func managedLoginProvider(provider string) bool {
	return provider == logins.ProviderClaude || provider == logins.ProviderCodex
}

func (m *loginManager) pending(provider string) (bool, error) {
	if m == nil {
		return false, nil
	}
	r, err := m.store.Snapshot()
	if err != nil {
		return true, err
	}
	if r.MigrationPending {
		return true, logins.ErrPending
	}
	_, ok := r.Switches[provider]
	return ok, nil
}
func (m *loginManager) owns(local string) bool {
	if m == nil {
		return false
	}
	r, err := m.store.Snapshot()
	if err != nil {
		return true
	}
	for _, op := range r.Switches {
		for _, row := range op.Sessions {
			if row.SourceID == local || row.ChildID == local {
				return true
			}
		}
	}
	return false
}
func (m *loginManager) inputGate(local string) error {
	if m == nil || m.d == nil || (m.d.core == nil && (m.d.authw == nil || m.d.authw.get == nil)) {
		return nil
	}
	meta, ok := m.get(local)
	if !ok || !managedLoginProvider(meta.AgentType) {
		return nil
	}
	if meta.ResumeError != "" {
		return errors.New("native continuation failed; end this attempt and resume the discussion before sending another message")
	}
	r, err := m.store.Snapshot()
	if err != nil {
		return err
	}
	op, pending := r.Switches[meta.AgentType]
	if pending && op.Deactivate {
		return fmt.Errorf("%w: %s is returning to native sign-in; this message was not sent", logins.ErrPending, meta.AgentType)
	}
	if r.MigrationPending || pending {
		return fmt.Errorf("%w: %s is waiting to change its login; this message was not sent", logins.ErrPending, meta.AgentType)
	}
	active := r.Providers[meta.AgentType].ActiveID
	for _, login := range r.Logins {
		if login.ID != active {
			continue
		}
		if login.AuthState == logins.AuthNeedsLogin {
			return fmt.Errorf("%s login needs sign-in; this message was not sent", meta.AgentType)
		}
		if login.LimitKnown && (login.ExhaustedUntil.IsZero() || time.Now().Before(login.ExhaustedUntil)) {
			return fmt.Errorf("%s login is exhausted; activate an available saved login or wait for its reset", meta.AgentType)
		}
	}
	return nil
}

func (m *loginManager) BeginSwitch(provider, toID, reason string) (out logins.Switch, err error) {
	if m == nil || !managedLoginProvider(provider) {
		return out, logins.ErrIneligible
	}
	err = m.operation(func() error {
		m.stepMu.Lock()
		defer m.stepMu.Unlock()
		var e error
		out, e = m.beginSwitch(provider, toID, reason)
		return e
	})
	return
}

func (m *loginManager) beginSwitch(provider, toID, reason string) (logins.Switch, error) {
	return m.beginSwitchExpected(provider, toID, reason, nil)
}
func (m *loginManager) beginSwitchExpected(provider, toID, reason string, expected *uint64) (logins.Switch, error) {
	gate := &m.admission[loginProviderIndex(provider)]
	gate.Lock()
	defer gate.Unlock()
	r, err := m.store.Snapshot()
	if err != nil {
		return logins.Switch{}, err
	}
	if expected != nil && r.Revision != *expected {
		return logins.Switch{}, logins.ErrConflict
	}
	if op, ok := r.Switches[provider]; ok {
		if op.Deactivate {
			return logins.Switch{}, logins.ErrPending
		}
		if op.ToID == toID && op.NextID == "" {
			return op, nil
		}
		if expected != nil {
			err = m.store.BeginSwitchRetryAtRevision(*expected, op.ID, toID, reason)
		} else {
			err = m.store.BeginSwitchRetry(op.ID, toID, reason)
		}
		if err != nil {
			return logins.Switch{}, err
		}
		next, e := m.store.Snapshot()
		if e != nil {
			return logins.Switch{}, e
		}
		return next.Switches[provider], nil
	}
	if r.Providers[provider].ActiveID == toID {
		return logins.Switch{}, nil
	}
	if r.Providers[provider].ActiveID == "" {
		if err = m.refuseNativeBound(provider); err != nil {
			return logins.Switch{}, err
		}
	}
	var sources []persist.Meta
	var rows []logins.Restart
	for _, meta := range m.list() {
		if meta.AgentType != provider || meta.Status.Process != status.ProcessRunning || meta.RosterHidden {
			continue
		}
		if meta.ConversationID == "" {
			return logins.Switch{}, fmt.Errorf("login switch: discussion %q has no exact native conversation", meta.Name)
		}
		sources = append(sources, meta)
		rows = append(rows, logins.Restart{SourceID: meta.ID, ConversationID: meta.ConversationID, Phase: "waiting"})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].SourceID < rows[j].SourceID })
	if err = m.preflightSources(sources); err != nil {
		return logins.Switch{}, err
	}
	if m.preflight != nil {
		sources = m.sourceRows(logins.Switch{Provider: provider})
		if err = m.preflight(provider, sources); err != nil {
			return logins.Switch{}, err
		}
	}
	if _, err = m.store.PrepareActivation(toID); err != nil {
		return logins.Switch{}, err
	}
	return m.store.BeginSwitch(r.Revision, provider, toID, reason, rows)
}

func (m *loginManager) BeginDeactivate(provider string) (logins.Switch, error) {
	return m.beginDeactivateAt(nil, provider)
}
func (m *loginManager) BeginDeactivateAtRevision(expected uint64, provider string) (logins.Switch, error) {
	return m.beginDeactivateAt(&expected, provider)
}
func (m *loginManager) beginDeactivateAt(expected *uint64, provider string) (out logins.Switch, err error) {
	if m == nil || !managedLoginProvider(provider) {
		return out, logins.ErrIneligible
	}
	err = m.operation(func() error {
		m.stepMu.Lock()
		defer m.stepMu.Unlock()
		var e error
		out, e = m.beginDeactivate(provider, expected)
		return e
	})
	return
}

// beginDeactivate returns the provider to native sign-in. Nothing is resumed or
// imported, so rows need no exact conversation and only an earlier unsettled
// native restart blocks it.
func (m *loginManager) beginDeactivate(provider string, expected *uint64) (logins.Switch, error) {
	gate := &m.admission[loginProviderIndex(provider)]
	gate.Lock()
	defer gate.Unlock()
	r, err := m.store.Snapshot()
	if err != nil {
		return logins.Switch{}, err
	}
	if expected != nil && r.Revision != *expected {
		return logins.Switch{}, logins.ErrConflict
	}
	if _, ok := r.Switches[provider]; ok {
		return logins.Switch{}, logins.ErrPending
	}
	if r.Providers[provider].ActiveID == "" {
		return logins.Switch{}, nil
	}
	var rows []logins.Restart
	for _, meta := range m.list() {
		if meta.AgentType == provider && meta.Status.Process == status.ProcessRunning && !meta.RosterHidden {
			rows = append(rows, logins.Restart{SourceID: meta.ID, ConversationID: meta.ConversationID, Phase: "waiting"})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].SourceID < rows[j].SourceID })
	if err = m.preflightRestarts(provider); err != nil {
		return logins.Switch{}, err
	}
	return m.store.BeginDeactivate(r.Revision, provider, "owner returned to native sign-in", rows)
}

// refuseNativeBound keeps native sign-in discussions out of a managed home that
// was imported once already: that home has no history for them to resume.
func (m *loginManager) refuseNativeBound(provider string) error {
	imported, err := loginmigration.ContextImported(m.d.stateDir, provider)
	if err != nil || !imported {
		return err
	}
	var names []string
	for _, meta := range m.list() {
		if meta.AgentType == provider && meta.Status.Process == status.ProcessRunning && !meta.RosterHidden && !loginmigration.ManagedBound(m.d.stateDir, meta) {
			names = append(names, strconv.Quote(meta.Name))
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	return fmt.Errorf("login switch: end or hand off these native sign-in discussions before activating a login: %s", strings.Join(names, ", "))
}

func (m *loginManager) CancelSwitch(provider string) error {
	return m.operation(func() error { return m.cancelSwitch(provider) })
}
func (m *loginManager) cancelSwitch(provider string) error {
	if m == nil {
		return logins.ErrIneligible
	}
	m.stepMu.Lock()
	defer m.stepMu.Unlock()
	r, err := m.store.Snapshot()
	if err != nil {
		return err
	}
	op, ok := r.Switches[provider]
	if !ok {
		return nil
	}
	op.CancelRequested = true
	// Once a stop is owned, cancellation still owes a safe credential commit and
	// exact resumes. Only a completely untouched switch may disappear immediately.
	untouched := op.Phase == "waiting" || op.Phase == "stopping"
	for _, row := range op.Sessions {
		if row.Phase != "waiting" && row.Phase != "cancelled" {
			untouched = false
		}
	}
	if untouched {
		return m.store.CancelSwitch(op.ID)
	}
	return m.store.SaveSwitch(op)
}

func (m *loginManager) step() {
	if m == nil {
		return
	}
	m.stepMu.Lock()
	defer m.stepMu.Unlock()
	r, err := m.store.Snapshot()
	if err != nil || r.MigrationPending {
		return
	}
	for _, provider := range []string{logins.ProviderClaude, logins.ProviderCodex} {
		if op, ok := r.Switches[provider]; ok {
			m.stepSwitch(op)
			continue
		}
		active := r.Providers[provider].ActiveID
		for _, login := range r.Logins {
			if login.ID != active || (!login.LimitKnown && login.AuthState != logins.AuthNeedsLogin) {
				continue
			}
			if login.AuthState != logins.AuthNeedsLogin && !login.ExhaustedUntil.IsZero() && !time.Now().Before(login.ExhaustedUntil) {
				_, _ = m.store.UpdateAvailability(r.Revision, login.ID, login.AuthState, false, time.Time{})
				continue
			}
			next, err := m.store.NextEligible(provider, nil, time.Now())
			if err == nil {
				reason := "subscription exhausted"
				if login.AuthState == logins.AuthNeedsLogin {
					reason = "native sign-in required"
				}
				_, _ = m.beginSwitch(provider, next.ID, reason)
			}
		}
	}
}
func (m *loginManager) saveError(op logins.Switch, err error) {
	if errors.Is(err, logins.ErrDurabilityUncertain) {
		if m.store.Reconcile() != nil {
			return
		}
	}
	r, e := m.store.Snapshot()
	if e != nil {
		return
	}
	current, ok := r.Switches[op.Provider]
	if !ok || current.ID != op.ID {
		return
	}
	if current.LastError == err.Error() {
		return
	}
	current.LastError = err.Error()
	_ = m.store.SaveSwitch(current)
}
func (m *loginManager) sourceRows(op logins.Switch) []persist.Meta {
	var sources []persist.Meta
	for _, meta := range m.list() {
		if meta.AgentType == op.Provider {
			sources = append(sources, meta)
		}
	}
	return sources
}

func (m *loginManager) stepSwitch(op logins.Switch) {
	switch op.Phase {
	case "waiting", "stopping":
		op.Phase = "stopping"
		if err := m.store.SaveSwitch(op); err != nil {
			return
		}
		for i := range op.Sessions {
			row := &op.Sessions[i]
			if row.Phase == "stopped" || row.Phase == "cancelled" {
				continue
			}
			source, exists := m.get(row.SourceID)
			if !exists || source.RosterHidden {
				row.Phase = "cancelled"
				if err := m.store.SaveSwitch(op); err != nil {
					return
				}
				m.d.clearAuthRecycle(row.SourceID)
				continue
			}
			// A deactivation resumes nothing, so its rows track no exact conversation.
			if !op.Deactivate && source.ConversationID != row.ConversationID {
				m.saveError(op, errors.New("login switch: source conversation changed"))
				return
			}
			if source.Status.Process != status.ProcessRunning && row.Phase == "waiting" {
				row.Phase = "cancelled"
				if err := m.store.SaveSwitch(op); err != nil {
					return
				}
				continue
			}
			if source.Status.Process == status.ProcessRunning {
				if m.d.authw == nil || !m.d.authw.settled {
					return
				}
				err := m.stopClaimedSession(&op, i, source)
				if err != nil {
					m.saveError(op, err)
					continue
				}
			}
			source, exists = m.get(row.SourceID)
			if !exists {
				row.Phase = "cancelled"
			} else if source.Status.Process == status.ProcessRunning {
				continue
			} else {
				if err := m.verifyStopped(source); err != nil {
					m.saveError(op, err)
					continue
				}
				row.Phase = "stopped"
				row.LastError = ""
			}
			if err := m.store.SaveSwitch(op); err != nil {
				return
			}
		}
		for _, row := range op.Sessions {
			if row.Phase != "stopped" && row.Phase != "cancelled" {
				return
			}
		}
		// Failed/retained attempts can still have descendants after their main row
		// ended. No credential writer is allowed to escape the fleet snapshot.
		for _, meta := range m.list() {
			if meta.AgentType != op.Provider {
				continue
			}
			if meta.Status.Process == status.ProcessRunning {
				m.saveError(op, errors.New("login switch: an unclaimed provider writer is still running"))
				return
			}
			if err := m.verifyStopped(meta); err != nil {
				m.saveError(op, err)
				return
			}
		}
		if err := m.verifyProviderWriters(op); err != nil {
			m.saveError(op, err)
			return
		}
		if m.prepareContext != nil && !op.Deactivate {
			if err := m.prepareContext(op.Provider, m.sourceRows(op)); err != nil {
				m.saveError(op, err)
				return
			}
		}
		op.Phase = "stopped"
		op.LastError = ""
		if err := m.store.SaveSwitch(op); err != nil {
			return
		}
	case "stopped":
		if err := m.verifyProviderWriters(op); err != nil {
			m.saveError(op, err)
			return
		}
		if err := m.store.CaptureOutgoing(op.ID); err != nil {
			m.saveError(op, err)
		}
	case "captured", "installing":
		if err := m.verifyProviderWriters(op); err != nil {
			m.saveError(op, err)
			return
		}
		if err := m.store.InstallIncoming(op.ID); err != nil {
			m.saveError(op, err)
		}
	case "installed":
		if _, err := m.store.CommitSwitch(op.ID); err != nil {
			m.saveError(op, err)
		}
	case "retry-stopping":
		m.stepRetryStopping(op)
	case "committed", "resuming":
		if op.Deactivate {
			m.finishDeactivation(op)
			return
		}
		if m.retryRejectedDestination(op) {
			return
		}
		op.Phase = "resuming"
		if err := m.store.SaveSwitch(op); err != nil {
			return
		}
		for i := range op.Sessions {
			if op.Sessions[i].Phase == "cancelled" || op.Sessions[i].Phase == "ready" || op.Sessions[i].Phase == "failed" {
				continue
			}
			m.resumeSwitchSession(&op, i)
		}
		for _, row := range op.Sessions {
			if row.Phase != "ready" && row.Phase != "failed" && row.Phase != "cancelled" {
				return
			}
		}
		for _, row := range op.Sessions {
			if row.Phase == "failed" && m.recordResumeError != nil {
				if err := m.recordResumeError(row.SourceID, row.LastError); err != nil {
					m.saveError(op, err)
					return
				}
				if row.ChildID != "" {
					if err := m.recordResumeError(row.ChildID, row.LastError); err != nil {
						m.saveError(op, err)
						return
					}
				}
			}
		}
		if m.retryRejectedDestination(op) {
			return
		}
		if err := m.store.CompleteSwitch(op.ID); err != nil {
			return
		}
		for _, row := range op.Sessions {
			m.d.clearAuthRecycle(row.SourceID)
			if row.ChildID != "" {
				m.d.clearAuthRecycle(row.ChildID)
			}
		}
	}
}

// finishDeactivation ends every stopped discussion in one step and completes on
// the next; replay after a crash finds the already cancelled rows and only
// completes.
func (m *loginManager) finishDeactivation(op logins.Switch) {
	ended := false
	for i := range op.Sessions {
		if row := &op.Sessions[i]; row.Phase != "cancelled" {
			row.Phase = "cancelled"
			row.LastError = "ended by return to native sign-in"
			ended = true
		}
	}
	if ended {
		_ = m.store.SaveSwitch(op)
		return
	}
	if err := m.store.CompleteSwitch(op.ID); err != nil {
		return
	}
	for _, row := range op.Sessions {
		m.d.clearAuthRecycle(row.SourceID)
		if row.ChildID != "" {
			m.d.clearAuthRecycle(row.ChildID)
		}
	}
}

func (m *loginManager) stopClaimedSession(op *logins.Switch, index int, source persist.Meta) error {
	row := &op.Sessions[index]
	claimed := row.Phase == "claimed"
	fence := m.d.withAuthRecycleFence
	if claimed {
		fence = m.d.withClaimedAuthRecycleFence
	}
	return fence(source.ID, func() error {
		current, ok := m.get(source.ID)
		if !ok || current.RosterHidden {
			return errAuthOwnerEnding
		}
		if current.Status.Process != status.ProcessRunning || current.Status.Turn != status.TurnIdle || current.Status.Interaction != status.InteractionNone || m.d.anyControlled(source.ID) || m.d.contextGuardCompactionInFlight(source.ID) || m.d.composerOutcomeUnresolved(source.ID) || m.d.directInputUnresolved(source.ID) {
			return fmt.Errorf("login switch: waiting for discussion %q to finish or detach", current.Name)
		}
		if !op.Deactivate && current.ConversationID != row.ConversationID {
			return errors.New("login switch: exact source conversation unavailable")
		}
		row.Phase = "claimed"
		if err := m.store.SaveSwitch(*op); err != nil {
			latest, readErr := m.store.Snapshot()
			if readErr == nil {
				if current, ok := latest.Switches[op.Provider]; ok && current.ID == op.ID {
					for _, saved := range current.Sessions {
						if saved.SourceID == row.SourceID && saved.Phase == "waiting" {
							row.Phase = "waiting"
							return err
						}
					}
				}
			}
			return fmt.Errorf("%w: %v", errAuthRecycleObligationRetained, err)
		}
		if err := m.d.authw.kill(source.ID); err != nil {
			return fmt.Errorf("%w: %v", errAuthRecycleObligationRetained, err)
		}
		return nil
	})
}
func (m *loginManager) resumeSwitchSession(op *logins.Switch, index int) {
	row := &op.Sessions[index]
	source, exists := m.get(row.SourceID)
	if !exists || source.RosterHidden {
		row.Phase = "cancelled"
		_ = m.store.SaveSwitch(*op)
		return
	}
	// Recover launch-before-journal crashes from durable lineage, never launch a
	// second child or replay the old prompt because the checkpoint was lost.
	if row.ChildID == "" {
		for _, child := range m.list() {
			if child.ResumedFrom == source.ID && child.LoginRuntime != nil && child.LoginRuntime.Epoch == op.ToEpoch {
				if row.ChildID == "" || child.ID > row.ChildID {
					row.ChildID = child.ID
				}
			}
		}
	}
	if row.ChildID == "" {
		runtime, err := m.store.RuntimeForSwitch(op.ID, row.SourceID)
		if err != nil {
			m.saveError(*op, err)
			return
		}
		options, err := switchResumeOptions(source, runtime, m.d.api.endpointID)
		if err != nil {
			row.Phase = "failed"
			row.LastError = err.Error()
			_ = m.store.SaveSwitch(*op)
			return
		}
		attempted, _ := m.d.withAuthResumeFence(source.ID, func() bool {
			fresh, err := m.d.authw.launch(daemon.LaunchSpec{AgentType: source.AgentType, Name: source.Name, Tag: source.Tag, Cwd: source.Cwd, ClientEnv: source.Env, Cols: authRecycleCols, Rows: authRecycleRows, SpawnedFrom: source.SpawnedFrom, SpawnIntent: source.SpawnIntent, Supervision: source.Supervision, LoginRuntime: &runtime, LoginStateRoot: m.d.stateDir, LoginSwitchID: op.ID, LoginSwitchSource: source.ID, InputEmbargo: op.ID, Options: options})
			if fresh.ID != "" {
				row.ChildID = fresh.ID
				row.Phase = "launched"
			} else if err != nil {
				row.Phase = "failed"
				row.LastError = err.Error()
			}
			_ = m.store.SaveSwitch(*op)
			return row.Phase != "failed"
		})
		if !attempted {
			row.Phase = "cancelled"
			_ = m.store.SaveSwitch(*op)
		}
		return
	}
	child, ok := m.get(row.ChildID)
	if !ok || child.Status.Process != status.ProcessRunning {
		row.Phase = "failed"
		row.LastError = "replacement did not remain running; exact conversation retained"
		_ = m.store.SaveSwitch(*op)
		return
	}
	if child.LoginRuntime == nil || child.LoginRuntime.Epoch != op.ToEpoch || child.LoginRuntime.Provider != op.Provider || child.ConversationID != "" && child.ConversationID != row.ConversationID {
		row.Phase = "failed"
		row.LastError = "replacement identity does not match the exact conversation"
		_ = m.store.SaveSwitch(*op)
		return
	}
	if child.ConversationID == "" || (m.d.authw.ready == nil || !m.d.authw.ready(child)) {
		if !child.CreatedAt.IsZero() && time.Since(child.CreatedAt) > 2*time.Minute {
			row.Phase = "failed"
			row.LastError = "native continuation readiness timed out"
			_ = m.store.SaveSwitch(*op)
		}
		return
	}
	if err := m.release(child.ID, op.ID); err != nil {
		m.saveError(*op, err)
		return
	}
	row.Phase = "ready"
	row.LastError = ""
	_ = m.store.SaveSwitch(*op)
}

func (m *loginManager) OwnerEnd(local, action string) error {
	return m.operation(func() error { return m.ownerEnd(local, action) })
}
func (m *loginManager) ownerEnd(local, action string) error {
	m.stepMu.Lock()
	defer m.stepMu.Unlock()
	r, err := m.store.Snapshot()
	if err != nil {
		return err
	}
	target := local
	for _, op := range r.Switches {
		for i := range op.Sessions {
			row := &op.Sessions[i]
			if row.SourceID != local && row.ChildID != local {
				continue
			}
			if row.ChildID != "" {
				target = row.ChildID
			}
			if row.Phase != "cancelled" {
				row.Phase = "cancelled"
				row.LastError = "ended by owner"
				if err = m.store.SaveSwitch(op); err != nil {
					return err
				}
			}
			m.d.clearAuthRecycle(row.SourceID)
			if row.ChildID != "" {
				m.d.clearAuthRecycle(row.ChildID)
			}
		}
	}
	return m.d.withOwnerSessionEnd(target, func() error {
		if action == "delete" {
			return m.d.api.deleteDiscussion(target)
		}
		if m.d.authw != nil && m.d.authw.kill != nil {
			return m.d.authw.kill(target)
		}
		return m.d.core.Kill(target)
	})
}

func (m *loginManager) BeginRelogin(loginID string, credential logins.Credential, reason string) (out logins.Switch, err error) {
	err = m.operation(func() error { var e error; out, e = m.beginRelogin(loginID, credential, reason); return e })
	return
}
func (m *loginManager) beginRelogin(loginID string, credential logins.Credential, reason string) (logins.Switch, error) {
	return m.beginReloginExpected(loginID, credential, reason, nil, "")
}
func (m *loginManager) beginReloginExpected(loginID string, credential logins.Credential, reason string, expected *uint64, jobID string) (logins.Switch, error) {
	m.stepMu.Lock()
	defer m.stepMu.Unlock()
	r, err := m.store.Snapshot()
	if err != nil {
		return logins.Switch{}, err
	}
	if expected != nil && r.Revision != *expected {
		return logins.Switch{}, logins.ErrConflict
	}
	provider := ""
	for _, login := range r.Logins {
		if login.ID == loginID {
			provider = login.Provider
		}
	}
	if !managedLoginProvider(provider) {
		return logins.Switch{}, logins.ErrIneligible
	}
	gate := &m.admission[loginProviderIndex(provider)]
	gate.Lock()
	defer gate.Unlock()
	if op, ok := r.Switches[provider]; ok {
		if op.Deactivate {
			return logins.Switch{}, logins.ErrPending
		}
		if op.ToID != loginID {
			return logins.Switch{}, logins.ErrInUse
		}
		revision := r.Revision
		if expected != nil {
			revision = *expected
		}
		if jobID != "" {
			err = m.store.BeginSwitchReloginJobAtRevision(revision, op.ID, credential, reason, jobID)
		} else {
			err = m.store.BeginSwitchReloginAtRevision(revision, op.ID, credential, reason)
		}
		if err != nil {
			return logins.Switch{}, err
		}
		next, e := m.store.Snapshot()
		if e != nil {
			return logins.Switch{}, e
		}
		return next.Switches[provider], nil
	}
	var rows []logins.Restart
	var sources []persist.Meta
	for _, meta := range m.list() {
		if meta.AgentType == provider && meta.Status.Process == status.ProcessRunning && !meta.RosterHidden {
			if meta.ConversationID == "" {
				return logins.Switch{}, errors.New("login switch: exact native conversation unavailable")
			}
			sources = append(sources, meta)
			rows = append(rows, logins.Restart{SourceID: meta.ID, ConversationID: meta.ConversationID, Phase: "waiting"})
		}
	}
	if err = m.preflightSources(sources); err != nil {
		return logins.Switch{}, err
	}
	if m.preflight != nil {
		sources = m.sourceRows(logins.Switch{Provider: provider})
		if err = m.preflight(provider, sources); err != nil {
			return logins.Switch{}, err
		}
	}
	r, err = m.store.Snapshot()
	if err != nil {
		return logins.Switch{}, err
	}
	if expected != nil && r.Revision != *expected {
		return logins.Switch{}, logins.ErrConflict
	}
	if jobID != "" {
		return m.store.BeginReloginJob(r.Revision, loginID, credential, reason, rows, jobID)
	}
	return m.store.BeginRelogin(r.Revision, loginID, credential, reason, rows)
}

func (m *loginManager) list() []persist.Meta {
	if m.d.authw != nil && m.d.authw.list != nil {
		return m.d.authw.list()
	}
	return m.d.core.List()
}
func (m *loginManager) get(local string) (persist.Meta, bool) {
	if m.d.authw != nil && m.d.authw.get != nil {
		return m.d.authw.get(local)
	}
	return m.d.core.Get(local)
}
func (m *loginManager) verifyStopped(meta persist.Meta) error {
	if m.stopProof != nil {
		return m.stopProof(meta)
	}
	return verifyLoginWritersStopped(m.d.stateDir, meta)
}

func (m *loginManager) preflightRestarts(provider string) error {
	if m.d.authw == nil {
		return nil
	}
	if m.d.authw.stateErr != nil {
		return errors.New("login switch: earlier native restart state is unreadable")
	}
	for id, claimed := range m.d.authw.state.Killed {
		if meta, ok := m.get(id); claimed && ok && provider != "" && meta.AgentType == provider {
			return errors.New("login switch: an earlier native CLI restart is still being reconciled; retry after it settles")
		}
	}
	return nil
}
func (m *loginManager) preflightSources(sources []persist.Meta) error {
	provider := ""
	if len(sources) > 0 {
		provider = sources[0].AgentType
	}
	if err := m.preflightRestarts(provider); err != nil {
		return err
	}
	for _, source := range sources {
		if source.Status.Process != status.ProcessRunning {
			continue
		}
		if source.LaunchOptions[protocol.OptionWorktree] == "true" && source.AgentCwd == "" {
			return errors.New("login switch: isolated discussion has no saved checkout")
		}
		ad, ok := registry.New(source.AgentType)
		if !ok {
			return errors.New("login switch: provider adapter is unavailable")
		}
		resolve := lookPathIn
		if m.d.authw != nil && m.d.authw.resolve != nil {
			resolve = m.d.authw.resolve
		}
		if _, err := resolve(ad.Binary(), source.Env); err != nil {
			return fmt.Errorf("login switch: discussion %q cannot resolve its native CLI: %w", source.Name, err)
		}
	}
	return nil
}

func (m *loginManager) verifyProviderWriters(op logins.Switch) error {
	if m.stopProof != nil {
		return nil
	}
	homes, err := m.store.ProtectedHomes(op.Provider)
	if err != nil {
		return err
	}
	var ids []string
	for _, row := range op.Sessions {
		ids = append(ids, row.SourceID)
		if row.ChildID != "" {
			ids = append(ids, row.ChildID)
		}
	}
	for _, meta := range m.list() {
		if meta.AgentType == op.Provider {
			ids = append(ids, meta.ID)
			if err = m.verifyStopped(meta); err != nil {
				return err
			}
		}
	}
	return verifyProviderNativeWritersStopped(homes, ids)
}

func (m *loginManager) operation(apply func() error) error {
	if m == nil {
		return logins.ErrIneligible
	}
	w := m.d.authw
	if w == nil || w.operations == nil {
		return apply()
	}
	op := authWatchOperation{apply: apply, done: make(chan error, 1)}
	select {
	case <-w.stop:
		return errors.New("login manager is stopping")
	case w.operations <- op:
	}
	select {
	case <-w.stop:
		return errors.New("login manager is stopping")
	case err := <-op.done:
		return err
	}
}

func (m *loginManager) BeginSwitchAtRevision(expected uint64, provider, toID, reason string) (out logins.Switch, err error) {
	err = m.operation(func() error {
		m.stepMu.Lock()
		defer m.stepMu.Unlock()
		var e error
		out, e = m.beginSwitchExpected(provider, toID, reason, &expected)
		return e
	})
	return
}
func (m *loginManager) BeginReloginAtRevision(expected uint64, loginID string, credential logins.Credential, reason string) (out logins.Switch, err error) {
	err = m.operation(func() error {
		var e error
		out, e = m.beginReloginExpected(loginID, credential, reason, &expected, "")
		return e
	})
	return
}
func (m *loginManager) BeginReloginJob(expected uint64, jobID, loginID string, credential logins.Credential, reason string) (out logins.Switch, err error) {
	err = m.operation(func() error {
		var e error
		out, e = m.beginReloginExpected(loginID, credential, reason, &expected, jobID)
		return e
	})
	return
}
func (m *loginManager) CancelSwitchAtRevision(expected uint64, provider string) error {
	return m.operation(func() error {
		m.stepMu.Lock()
		defer m.stepMu.Unlock()
		r, err := m.store.Snapshot()
		if err != nil {
			return err
		}
		if r.Revision != expected {
			return logins.ErrConflict
		}
		op, ok := r.Switches[provider]
		if !ok {
			return nil
		}
		return m.store.CancelSwitchAtRevision(expected, op.ID)
	})
}

func (m *loginManager) restoreClaims() error {
	r, err := m.store.Snapshot()
	if err != nil {
		return err
	}
	for _, op := range r.Switches {
		for _, row := range op.Sessions {
			if row.Phase != "waiting" && row.Phase != "cancelled" {
				m.d.restoreAuthRecycle(row.SourceID)
			}
			if row.ChildID != "" && row.ChildStopClaimed {
				m.d.restoreAuthRecycle(row.ChildID)
			}
		}
	}
	return nil
}
