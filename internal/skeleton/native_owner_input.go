package skeleton

import (
	"context"
	"errors"
	"time"

	"github.com/Nathandela/swarm/internal/claudeinput"
	"github.com/Nathandela/swarm/internal/status"
)

var errNativeOwnerInputUnavailable = errors.New("native discussion input is temporarily unavailable; retry")

func (d *Daemon) nativeOwnerInput(local, instance string) bool {
	hint, ok := d.ownerInputHints.snapshot(local)
	return ok && hint.caps.SessionInstance == instance && hint.caps.NativeOwnerInput
}
func (d *Daemon) probeNativeOwnerInput(parent context.Context, local, instance string) error {
	if d.core == nil {
		return errNativeOwnerInputUnavailable
	}
	caps, ok := d.sessionCapabilities(local)
	if !ok || caps.Validate() != nil || caps.SessionInstance != instance {
		return errNativeOwnerInputUnavailable
	}
	meta, ok := d.core.Get(local)
	if !ok || meta.Status.Process != status.ProcessRunning || meta.ResumeError != "" || caps.Provider != meta.AgentType {
		return errNativeOwnerInputUnavailable
	}
	current, ok := d.sessionInstance(local)
	if !ok || current != instance {
		return errNativeOwnerInputUnavailable
	}
	switch meta.AgentType {
	case "opencode":
		if caps.NativeComposer {
			return nil
		}
	case "codex":
		backend, ok := d.sessionBackendFor(local)
		if caps.StructuredChat && ok && backend.sessionInstance == instance && backend.conn != nil && backend.threadID != "" && backend.feed != nil && !backend.feed.retired.Load() {
			return nil
		}
	case "claude":
		if !caps.StructuredChat {
			return errNativeOwnerInputUnavailable
		}
		enrolled, err := d.core.SessionClaudeInputEnrollment(local)
		if err != nil || !enrolled {
			return errNativeOwnerInputUnavailable
		}
		descriptor, err := d.core.SessionClaudeInput(local)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(parent, 150*time.Millisecond)
		defer cancel()
		_, err = claudeinput.NewClient(descriptor).Ready(ctx)
		return err
	}
	return errNativeOwnerInputUnavailable
}
