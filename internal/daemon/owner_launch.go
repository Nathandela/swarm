package daemon

import (
	"errors"
	"strings"

	"github.com/Nathandela/swarm/internal/idempotency"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

var ErrOwnerLaunchConflict = errors.New("daemon: launch operation ID is bound to a different request")

// OwnerLaunchEnvironment retains only launch inputs safe for durable custody.
// Opaque native origins become refusal markers, preserving fail-closed admission.
func OwnerLaunchEnvironment(agent string, env []string) []string {
	if env == nil {
		return nil
	}
	out := persist.FilterEnv(env)
	switch agent {
	case "hermes":
		out = append(out, HermesConfigurationEnvironment(env)...)
	case "opencode":
		for _, entry := range env {
			key, _, _ := strings.Cut(entry, "=")
			if key == "XDG_CONFIG_HOME" {
				out = append(out, entry)
			}
		}
		out = append(out, openCodeSavedEnvironment(env)...)
	}
	return out
}

// RemoteOperationOutcome excludes owner custody from the signed remote read domain.
func (d *Daemon) RemoteOperationOutcome(operationID string) (OpOutcome, bool) {
	rec, known := d.idem.Get(operationID)
	if !known || rec.Action == "owner_launch" {
		return OpOutcome{}, false
	}
	return d.OperationOutcome(operationID)
}

// OwnerLaunchReplay reconciles before launch composition or any new reservation.
// An uncertain reservation never authorizes another process, even after restart.
func (d *Daemon) OwnerLaunchReplay(operationID, requestHash string) (persist.Meta, bool, error) {
	rec, known := d.idem.Get(operationID)
	if !known {
		return persist.Meta{}, false, nil
	}
	if rec.Action != "owner_launch" || rec.RequestHash != requestHash || requestHash == "" {
		return persist.Meta{}, true, ErrOwnerLaunchConflict
	}
	d.mu.Lock()
	s, present := d.sessions[rec.SessionID]
	var m persist.Meta
	if present {
		m = cloneOpenCodeMeta(s.meta)
	}
	d.mu.Unlock()
	if present && m.ShimPID != 0 && m.Status.Process != status.ProcessLost && openCodeLaunchComplete(m) {
		if rec.Phase != idempotency.PhaseCompleted {
			if err := d.idem.Complete(operationID, nil); err != nil {
				return persist.Meta{}, true, err
			}
		}
		return m, true, nil
	}
	if rec.Phase == idempotency.PhaseCompleted {
		return persist.Meta{}, true, ErrLaunchOpConsumed
	}
	return persist.Meta{}, true, ErrLaunchOutcomeUnknown
}

// OwnerLaunchOutcome is a read-only reconciliation surface, separate from remote authority.
func (d *Daemon) OwnerLaunchOutcome(operationID string) (OpOutcome, bool) {
	rec, known := d.idem.Get(operationID)
	if !known || rec.Action != "owner_launch" {
		return OpOutcome{}, false
	}
	return d.OperationOutcome(operationID)
}

// RecordOwnerLaunch binds an assembly resume/adoption that selected an existing
// conversation instead of spawning. Its success still needs a durable receipt.
func (d *Daemon) RecordOwnerLaunch(spec LaunchSpec, m persist.Meta) (persist.Meta, error) {
	if spec.OwnerLaunchHash == "" {
		return m, nil
	}
	rec, existed, err := d.idem.PrepareBound(spec.OperationID, "owner_launch", m.ID, "", spec.OwnerLaunchHash)
	if err != nil {
		return persist.Meta{}, err
	}
	if existed {
		if rec.Action != "owner_launch" || rec.RequestHash != spec.OwnerLaunchHash {
			return persist.Meta{}, ErrOwnerLaunchConflict
		}
		prior, _, err := d.OwnerLaunchReplay(spec.OperationID, spec.OwnerLaunchHash)
		return prior, err
	}
	if m.ShimPID == 0 || m.Status.Process == status.ProcessLost || !openCodeLaunchComplete(m) {
		return persist.Meta{}, ErrLaunchOutcomeUnknown
	}
	if err := d.idem.Complete(spec.OperationID, nil); err != nil {
		return persist.Meta{}, err
	}
	return m, nil
}
