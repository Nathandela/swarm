package skeleton

import (
	"context"
	"time"

	"github.com/Nathandela/swarm/internal/claudeinput"
	"github.com/Nathandela/swarm/internal/status"
)

func (d *Daemon) nativeOwnerInput(local, instance string) bool {
	hint, ok := d.ownerInputHints.snapshot(local)
	return ok && hint.caps.SessionInstance == instance && hint.caps.NativeOwnerInput
}
func (d *Daemon) probeNativeOwnerInput(parent context.Context, local, instance string) bool {
	if d.core == nil {
		return false
	}
	caps, ok := d.sessionCapabilities(local)
	if !ok || caps.Validate() != nil || caps.SessionInstance != instance {
		return false
	}
	meta, ok := d.core.Get(local)
	if !ok || meta.Status.Process != status.ProcessRunning || meta.ResumeError != "" || caps.Provider != meta.AgentType {
		return false
	}
	current, ok := d.sessionInstance(local)
	if !ok || current != instance {
		return false
	}
	switch meta.AgentType {
	case "opencode":
		return caps.NativeComposer
	case "codex":
		backend, ok := d.sessionBackendFor(local)
		return caps.StructuredChat && ok && backend.sessionInstance == instance && backend.conn != nil && backend.threadID != "" && backend.feed != nil && !backend.feed.retired.Load()
	case "claude":
		if !caps.StructuredChat {
			return false
		}
		enrolled, err := d.core.SessionClaudeInputEnrollment(local)
		if err != nil || !enrolled {
			return false
		}
		descriptor, err := d.core.SessionClaudeInput(local)
		if err != nil {
			return false
		}
		ctx, cancel := context.WithTimeout(parent, 150*time.Millisecond)
		defer cancel()
		_, err = claudeinput.NewClient(descriptor).Ready(ctx)
		return err == nil
	default:
		return false
	}
}
