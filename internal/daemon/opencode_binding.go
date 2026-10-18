package daemon

import (
	"errors"
	"slices"

	"github.com/Nathandela/swarm/internal/persist"
)

func cloneOpenCodeMeta(m persist.Meta) persist.Meta {
	m.OpenCodeBinding = persist.CloneOpenCodeBinding(m.OpenCodeBinding)
	return m
}

// SetOpenCodeBinding commits only forward progress for the same native runtime generation.
func (d *Daemon) SetOpenCodeBinding(id string, expected uint64, next persist.OpenCodeBinding) error {
	if next.Revision != expected+1 || !persist.ValidOpenCodeBinding(next) {
		return errors.New("daemon: invalid OpenCode binding update")
	}
	d.writeMu.Lock()
	d.mu.Lock()
	closed := d.closed
	d.mu.Unlock()
	if closed {
		d.writeMu.Unlock()
		return errors.New("daemon: closed")
	}
	m, ok := d.Get(id)
	if !ok || m.RequiredRuntime != persist.RequiredRuntimeOpenCode || m.OpenCodeBinding == nil {
		d.writeMu.Unlock()
		return errors.New("daemon: OpenCode binding unavailable")
	}
	old := m.OpenCodeBinding
	if old.Revision != expected || !sameOpenCodeCustody(*old, next) || !openCodeProgress(*old, next) {
		d.writeMu.Unlock()
		return errors.New("daemon: stale or conflicting OpenCode binding update")
	}
	m.OpenCodeBinding = persist.CloneOpenCodeBinding(&next)
	written, err := d.saveMetaLocked(&m)
	d.writeMu.Unlock()
	if err != nil {
		return err
	}
	if !written {
		return errors.New("daemon: OpenCode session retired")
	}
	if d.cfg.onMetaSave != nil {
		d.cfg.onMetaSave(cloneOpenCodeMeta(m))
	}
	return nil
}

func sameOpenCodeCustody(old, next persist.OpenCodeBinding) bool {
	if old.Mode != next.Mode || old.Generation != next.Generation || !slices.Equal(old.ProfileEnv, next.ProfileEnv) || old.NativeCreatedAtMS > 0 && old.NativeCreatedAtMS != next.NativeCreatedAtMS {
		return false
	}
	if (old.Initial == nil) != (next.Initial == nil) {
		return false
	}
	if old.Initial == nil {
		return true
	}
	a, b := old.Initial, next.Initial
	return a.OperationID == b.OperationID && a.RequestHash == b.RequestHash && (a.MessageID == "" && a.Phase != persist.OpenCodeInitialRejected || *a == *b)
}

func openCodeProgress(old, next persist.OpenCodeBinding) bool {
	phases := map[string]int{persist.OpenCodePhasePlanned: 0, persist.OpenCodePhaseBound: 1, persist.OpenCodePhaseReady: 2}
	if phases[next.Phase] < phases[old.Phase] || phases[next.Phase] > phases[old.Phase]+1 {
		return false
	}
	if old.Initial == nil {
		return true
	}
	a, b := old.Initial.Phase, next.Initial.Phase
	if a == b {
		return true
	}
	switch a {
	case persist.OpenCodeInitialPrepared:
		return b == persist.OpenCodeInitialStarted
	case persist.OpenCodeInitialStarted:
		return b == persist.OpenCodeInitialAdmitted || b == persist.OpenCodeInitialUncertain || b == persist.OpenCodeInitialRejected
	case persist.OpenCodeInitialUncertain:
		return b == persist.OpenCodeInitialAdmitted || b == persist.OpenCodeInitialRejected
	default:
		return false
	}
}
