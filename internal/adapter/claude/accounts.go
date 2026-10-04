package claude

import (
	"encoding/json"

	"github.com/Nathandela/swarm/internal/adapter"
)

// ManagedObservations adds pure observation declarations for the characterized
// account-pool invocation. The assembly version-gates and installs these hooks;
// ordinary Claude launches retain their existing command and event surface.
func ManagedObservations() managedClaudeAdapter { return managedClaudeAdapter{} }

type managedClaudeAdapter struct{ claudeAdapter }

func (managedClaudeAdapter) SignalSources() []adapter.SignalSource {
	sources := New().SignalSources()
	for _, event := range []string{"SessionStart", "StopFailure"} {
		sources = append(sources, adapter.SignalSource{Kind: "hook", Descriptor: map[string]string{"event": event, adapter.DescriptorCapture: adapter.CaptureRaw}})
	}
	return sources
}

func (managedClaudeAdapter) Interactions(p adapter.HookPayload) []adapter.Interaction {
	if p.Event != "StopFailure" {
		return New().Interactions(p)
	}
	if len(p.Raw) > 64<<10 {
		return nil
	}
	var body struct {
		Error   string `json:"error"`
		AgentID string `json:"agent_id"`
	}
	if json.Unmarshal(p.Raw, &body) != nil || body.AgentID != "" {
		return nil
	}
	var text string
	switch body.Error {
	case "rate_limit":
		text = "The account reached its usage limit. This request failed; it has not been retried."
	case "authentication_failed":
		text = "The account needs to sign in again. This request failed; it has not been retried."
	case "api_error", "billing_error", "invalid_request", "max_output_tokens", "server_error", "unknown":
		text = "The provider could not complete this request. It has not been retried."
	default:
		return nil
	}
	return []adapter.Interaction{{Kind: adapter.KindAgentMessage, Status: adapter.StatusFailed, Text: text}}
}
