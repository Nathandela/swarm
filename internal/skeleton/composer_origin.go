package skeleton

import "github.com/Nathandela/swarm/internal/protocol"

// composerOriginIntent retains provenance even when a provider has no exact native echo.
type composerOriginIntent struct {
	HermesInput *hermesInputIntent      `json:"hermes_input,omitempty"`
	ClaudeInput *claudeInputIntent      `json:"claude_input,omitempty"`
	Version     int                     `json:"composer_origin_version"`
	Origin      *protocol.MessageOrigin `json:"origin"`
}

func sameMessageOrigin(a, b *protocol.MessageOrigin) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
