package discord

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/voice"
)

// preflight checks the two local dependencies before the connector starts offering calls.
func (d *dispatcherCalls) preflight() string {
	cfg := d.t.service.Config.Calls
	if cfg == nil {
		return ""
	}
	if _, err := voice.LoadOpenAIKey(cfg.SpeechStateDir); err != nil {
		if strings.Contains(err.Error(), "invalid speech credential") {
			return "speech key is invalid; run swarm voice setup"
		}
		return "speech key is unavailable; run swarm voice setup"
	}
	ctx, cancel := context.WithTimeout(d.ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, cfg.HelperPath, "--check")
	cmd.Env = []string{"PATH=/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin"}
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Run(); err != nil {
		return "voice helper check failed. Verify the helper installation and Node 24.17+ within Node 24"
	}
	return ""
}

func (d *dispatcherCalls) stopCall() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.suspended = true
	d.invitedUntil = time.Time{}
	clear(d.consent)
	if d.active != nil {
		d.active.cancel()
	}
	d.signalLocked()
}
