package skeleton

import (
	"errors"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
)

var errClaudeChatQuarantined = errors.New("skeleton: Claude chat runtime is retained without an active consumer")

// Capture before core assignment; missing later metadata cannot release retained evidence.
func (d *Daemon) rememberClaudeChatRuntime(m persist.Meta) bool {
	if persist.IsClaudeChatRuntime(m) {
		d.claudeChatRetained.Store(m.ID, struct{}{})
		if m.AgentType == "claude" {
			d.claudeNativeStops.LoadOrStore(m.ID, claudeNativeStopHandle{})
		}
		return true
	}
	_, retained := d.claudeChatRetained.Load(m.ID)
	return retained
}

func (d *Daemon) claudeChatQuarantined(sessionID string) bool {
	if d == nil {
		return false
	}
	if _, retained := d.claudeChatRetained.Load(sessionID); retained {
		return true
	}
	if d.core != nil {
		if m, ok := d.core.Get(sessionID); ok {
			return d.rememberClaudeChatRuntime(m)
		}
	}
	return false
}

func passiveClaudeChatCapabilities(c protocol.SessionCapabilities) protocol.SessionCapabilities {
	c.StructuredChat, c.TerminalFallback, c.TerminalControl, c.Interrupt = false, false, false, false
	c.NativeComposer, c.NativeInteractionVersion = false, 0
	return c
}
