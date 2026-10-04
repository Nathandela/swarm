package skeleton

import (
	"github.com/Nathandela/swarm/internal/accountcheck"
	"github.com/Nathandela/swarm/internal/processcontain"
)

// retainedCheckReferences is called only by the auth-watch writer. A failed
// pre-rename permit release can differ between RAM and disk; both remain
// references until the visible checked document and its parent are confirmed.
func (m *accountRotationManager) retainedCheckReferences() ([]accountcheck.CustodyReference, error) {
	if m.w.stateErr != nil {
		return nil, m.w.stateErr
	}
	durable, err := loadAuthWatchStateChecked(m.w.stateDir)
	if err != nil {
		return nil, err
	}
	root, err := openAccountRecoveryRoot(m.w.stateDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	if err := historySync(root); err != nil {
		return nil, err
	}
	var retained []accountcheck.CustodyReference
	for _, state := range []authWatchState{m.w.state, durable} {
		for _, permit := range state.AccountHalfOpen {
			retained = append(retained, accountcheck.CustodyReference{Binding: permit.Stamp.Binding, Worker: processcontain.Identity{PID: permit.WorkerPID, StartTime: permit.WorkerStartTime}})
		}
	}
	return retained, nil
}

func (m *accountRotationManager) collectChecks() {
	// Most owners have never run a native check. Avoid journal reads and a
	// collector cursor until the private check inventory actually exists.
	root, err := openAccountRecoveryRoot(m.w.stateDir)
	if err != nil {
		return
	}
	_, err = root.Lstat("accounts/checks")
	_ = root.Close()
	if err != nil {
		return
	}
	retained, err := m.retainedCheckReferences()
	if err != nil {
		return
	}
	if m.checkCollector == nil {
		m.checkCollector = accountcheck.NewCollector(m.w.stateDir)
	}
	// Collection errors preserve custody, rather than changing recovery or
	// native admission authority. The next owner tick retries bounded work.
	_, _ = m.checkCollector.Step(retained)
}
