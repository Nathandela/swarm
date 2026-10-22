package skeleton

import (
	"errors"
	"fmt"
	"time"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// A rejected destination cannot erase original restart obligations. Another
// login is selected under the same provider journal, without replaying a turn.
func (m *loginManager) retryRejectedDestination(op logins.Switch) bool {
	r, err := m.store.Snapshot()
	if err != nil {
		m.saveError(op, err)
		return true
	}
	var rejected bool
	reason := "subscription exhausted"
	for _, login := range r.Logins {
		if login.ID != op.ToID {
			continue
		}
		if login.AuthState == logins.AuthNeedsLogin {
			rejected = true
			reason = "native sign-in required"
		}
		if login.LimitKnown && (login.ExhaustedUntil.IsZero() || time.Now().Before(login.ExhaustedUntil)) {
			rejected = true
		}
	}
	if !rejected {
		return false
	}
	next, err := m.store.NextEligible(op.Provider, op.AttemptedIDs, time.Now())
	if err != nil {
		m.saveError(op, fmt.Errorf("%s logins are exhausted or need sign-in; sign in or activate an available login to resume these discussions", op.Provider))
		return true
	}
	if err = m.store.BeginSwitchRetry(op.ID, next.ID, reason); err != nil {
		m.saveError(op, err)
	}
	return true
}
func (m *loginManager) stepRetryStopping(op logins.Switch) {
	for index := range op.Sessions {
		row := &op.Sessions[index]
		if row.ChildID == "" {
			continue
		}
		child, ok := m.get(row.ChildID)
		if !ok {
			continue
		}
		if child.Status.Process == status.ProcessRunning {
			if m.d.authw == nil || !m.d.authw.settled {
				return
			}
			if err := m.stopSwitchChild(&op, index, child); err != nil {
				m.saveError(op, err)
				return
			}
			child, ok = m.get(row.ChildID)
			if ok && child.Status.Process == status.ProcessRunning {
				return
			}
		}
		if ok {
			if err := m.verifyStopped(child); err != nil {
				m.saveError(op, err)
				return
			}
		}
	}
	if err := m.verifyProviderWriters(op); err != nil {
		m.saveError(op, err)
		return
	}
	if err := m.store.RetargetSwitch(op.ID); err != nil {
		m.saveError(op, err)
	}
}
func (m *loginManager) stopSwitchChild(op *logins.Switch, index int, child persist.Meta) error {
	row := &op.Sessions[index]
	fence := m.d.withAuthRecycleFence
	if row.ChildStopClaimed {
		fence = m.d.withClaimedAuthRecycleFence
	}
	return fence(child.ID, func() error {
		current, ok := m.get(child.ID)
		if !ok || current.Status.Process != status.ProcessRunning {
			return nil
		}
		if current.LoginRuntime == nil || current.LoginRuntime.Provider != op.Provider || current.LoginRuntime.Epoch != op.ToEpoch || current.ConversationID != row.ConversationID {
			return errors.New("login switch: replacement runtime changed before retry stop")
		}
		if current.Status.Turn != status.TurnIdle || current.Status.Interaction != status.InteractionNone || m.d.anyControlled(child.ID) || m.d.contextGuardCompactionInFlight(child.ID) || m.d.composerOutcomeUnresolved(child.ID) || m.d.directInputUnresolved(child.ID) {
			return fmt.Errorf("login switch: waiting for replacement %q to finish or detach", current.Name)
		}
		row.ChildStopClaimed = true
		if err := m.store.SaveSwitch(*op); err != nil {
			latest, readErr := m.store.Snapshot()
			if readErr == nil {
				if current, ok := latest.Switches[op.Provider]; ok && current.ID == op.ID {
					for _, saved := range current.Sessions {
						if saved.SourceID == row.SourceID && !saved.ChildStopClaimed {
							row.ChildStopClaimed = false
							return err
						}
					}
				}
			}
			return fmt.Errorf("%w: %v", errAuthRecycleObligationRetained, err)
		}
		if err := m.d.authw.kill(child.ID); err != nil {
			return fmt.Errorf("%w: %v", errAuthRecycleObligationRetained, err)
		}
		return nil
	})
}
