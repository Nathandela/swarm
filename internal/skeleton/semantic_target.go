package skeleton

import (
	"context"
	"errors"
	"time"

	"github.com/Nathandela/swarm/internal/claudeinput"
	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

func (a *coreAPI) ResolveSemanticTarget(local string) (string, protocol.ErrorCode, error) {
	if a.semanticTarget == nil {
		return "", protocol.CodeUnavailable, errors.New("semantic target lookup is unavailable")
	}
	return a.semanticTarget(local)
}

func (d *Daemon) resolveSemanticTarget(local string) (string, protocol.ErrorCode, error) {
	meta, ok := d.core.Get(local)
	if !ok || meta.RosterHidden {
		return "", protocol.CodeUnavailable, errors.New("discussion is absent; refresh the roster")
	}
	if meta.Status.Process != status.ProcessRunning {
		return "", protocol.CodeUnavailable, errors.New("discussion is not running; resume it explicitly")
	}
	switch meta.AgentType {
	case "claude", "codex", "opencode":
	default:
		if !meta.HermesNative {
			return "", protocol.CodeStructuredUnsupported, errors.New("provider does not support semantic messages")
		}
	}
	instance, ok := d.sessionInstance(local)
	if !ok || meta.ShimPID <= 0 || meta.ShimStartTime <= 0 {
		return "", protocol.CodeUnavailable, errors.New("discussion incarnation is temporarily unavailable")
	}
	// A retained Codex shim can be live before its asynchronous backend attach.
	// Wait only before submission and keep checking the original incarnation.
	deadline := time.Now().Add(2 * time.Second)
	for {
		current, ok := d.core.Get(local)
		if !ok || current.ShimPID != meta.ShimPID || current.ShimStartTime != meta.ShimStartTime ||
			!d.matchesResumeSourceInstance(current, instance) {
			return "", protocol.CodeStaleInstance, errors.New("discussion incarnation changed; resolve the intended session again")
		}
		if current.Status.Process != status.ProcessRunning {
			return "", protocol.CodeUnavailable, errors.New("discussion is not running; resume it explicitly")
		}
		start, err := procstart.StartTime(current.ShimPID)
		if err != nil || start != current.ShimStartTime {
			return "", protocol.CodeUnavailable, errors.New("discussion shim is temporarily unavailable; retry")
		}
		if current.AgentType == "claude" {
			enrolled, err := d.core.SessionClaudeInputEnrollment(local)
			if err != nil {
				return "", protocol.CodeUnavailable, err
			}
			if !enrolled {
				if code, err := d.requireStructuredComposer(local, local); err != nil {
					return "", code, err
				}
				return instance, "", nil
			}
		}
		var proofErr error
		switch current.AgentType {
		case "codex", "claude":
			proofErr = d.probeNativeOwnerInput(context.Background(), local, instance)
			if proofErr == nil {
				return instance, "", nil
			}
		default:
			// These bridges already perform their own operation-specific readiness checks.
			return instance, "", nil
		}
		if errors.Is(proofErr, claudeinput.ReadinessRetired) || errors.Is(proofErr, managerproof.ErrQuarantined) || !time.Now().Before(deadline) {
			return "", protocol.CodeUnavailable, proofErr
		}
		time.Sleep(25 * time.Millisecond)
	}
}
