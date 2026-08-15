package schema

import "errors"

// SessionCapabilities is the daemon-authored, per-session-instance capability record of
// ADR-017 T2 / playbook §6.2: which phone surface a session gets, and which individually
// testable seams it supports. It is authored once at session launch and is immutable for
// the life of the instance (T2 rule 1); the only mutation path afterward is SetStructuredChat,
// and only in the degrading direction (T2 rule 2).
type SessionCapabilities struct {
	Provider         string `json:"provider"`          // adapter identity: claude, codex, opencode, agy, ...
	ProviderVersion  string `json:"provider_version"`  // the DETECTED version of the installed CLI
	AdapterRevision  string `json:"adapter_revision"`  // the revision of the Swarm adapter that produced the record
	StructuredChat   bool   `json:"structured_chat"`   // true only when every T3 row passes against provider_version
	TerminalFallback bool   `json:"terminal_fallback"` // whether the sanitized terminal view may be offered at all
	Interrupt        bool   `json:"interrupt"`         // whether a semantic interrupt reaches the current turn
}

// ErrCapabilityUpgrade is returned by SetStructuredChat when asked to flip a degraded
// record (structured_chat=false) back to true. ADR-017 T2 rule 2: "A runtime integrity
// failure may only degrade the record ... it cannot upgrade a fallback session in place."
var ErrCapabilityUpgrade = errors.New("schema: capability record cannot upgrade structured_chat once degraded")

// SetStructuredChat is the one mutation path a capability record has after launch. It
// allows a healthy record to degrade (true -> false), which also forces TerminalFallback
// true so the session gains the sanitized surface it lost structured chat for. It is
// idempotent in either steady state and refuses an upgrade attempt (false -> true),
// leaving the record unchanged.
func (c *SessionCapabilities) SetStructuredChat(v bool) error {
	if v && !c.StructuredChat {
		return ErrCapabilityUpgrade
	}
	c.StructuredChat = v
	if !v {
		c.TerminalFallback = true
	}
	return nil
}
