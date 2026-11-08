package skeleton

import (
	"errors"
	"fmt"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

const managerRestartRecovery = "voice manager requires controller reconciliation; automatic restart or login change deferred. Reconcile or explicitly end its Swarm session, complete the login or CLI change, then relaunch through voice control with a fresh binding"

func (m *loginManager) quarantineManager(meta persist.Meta) error {
	if m.recordResumeError != nil {
		if err := m.recordResumeError(meta.ID, managerRestartRecovery); err != nil {
			return fmt.Errorf("%s: cannot retain manager quarantine: %w", managerRestartRecovery, err)
		}
	}
	return errors.New(managerRestartRecovery)
}

// Background lifecycle jobs cannot renew the controller's manager authority.
func managerRestartPreflight(meta persist.Meta) error {
	if meta.Role == "manager" {
		return fmt.Errorf("%s (session %s)", managerRestartRecovery, meta.ID)
	}
	return nil
}

func (w *authWatcher) holdManagerRestart(meta persist.Meta, quarantine bool) bool {
	err := managerRestartPreflight(meta)
	if err == nil {
		return false
	}
	w.once("manager-restart:"+meta.ID, "manager lifecycle: %v", err)
	if quarantine && w.logins != nil && w.logins.recordResumeError != nil {
		if err := w.logins.recordResumeError(meta.ID, managerRestartRecovery); err != nil {
			w.once("manager-quarantine:"+meta.ID, "manager lifecycle: cannot retain login quarantine for %s: %v", meta.ID, err)
		}
	}
	return true
}

func (m *loginManager) preflightManagerSwitch(op logins.Switch) error {
	for _, meta := range m.list() {
		if meta.Status.Process == status.ProcessRunning && m.providerWriter(op, meta) {
			if err := managerRestartPreflight(meta); err != nil {
				return err
			}
		}
	}
	for _, row := range op.Sessions {
		if row.Phase == "cancelled" {
			continue
		}
		for _, id := range []string{row.SourceID, row.ChildID} {
			if meta, ok := m.get(id); ok {
				if err := managerRestartPreflight(meta); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
