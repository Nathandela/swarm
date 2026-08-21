package schema

import (
	"errors"
	"fmt"
)

// SessionCapabilities is the daemon-authored, per-session-instance capability record of
// ADR-017 T2 / playbook §6.2: which phone surface a session gets, and which individually
// testable seams it supports. It is authored once at session launch and is immutable for
// the life of the instance (T2 rule 1); the only mutation path afterward is SetStructuredChat,
// and only in the degrading direction (T2 rule 2).
type SessionCapabilities struct {
	Provider         string `json:"provider"`          // adapter identity: claude, codex, opencode, agy, ...
	ProviderVersion  string `json:"provider_version"`  // the DETECTED version of the installed CLI
	AdapterRevision  string `json:"adapter_revision"`  // the revision of the Swarm adapter that produced the record
	SessionInstance  string `json:"session_instance"`  // the per-incarnation identifier every generation and snapshot binds to (ADR-017 T8-a)
	StructuredChat   bool   `json:"structured_chat"`   // true only when every T3 row passes against provider_version
	TerminalFallback bool   `json:"terminal_fallback"` // whether the sanitized terminal view may be offered at all
	TerminalControl  bool   `json:"terminal_control"`  // whether raw input may be entered on that view (ADR-017 T6-b)
	Interrupt        bool   `json:"interrupt"`         // whether a semantic interrupt reaches the current turn
}

// CurrentCapabilityRecordVersion is the SessionCapabilities record version a machine
// declares in RemoteProfileV1.CapabilityRecordVersion. A phone that reads ZERO there treats
// every record as untrusted and routes every session to the status card (ADR-017 T5-a), so
// this constant is what a machine must actually publish for the fallback to exist at all.
const CurrentCapabilityRecordVersion = 1

// ErrCapabilityInconsistent is what Validate wraps. ADR-017 T2-b: the record is the
// router, so an inconsistent one is REJECTED rather than resolved -- resolving it means
// choosing which boolean to believe, and either choice is a routing decision taken by the
// reader rather than by the daemon that authored the record. Every seam recognises the
// same refusal through errors.Is rather than by string-matching a message.
var ErrCapabilityInconsistent = errors.New("schema: inconsistent session capability record")

// Validate enforces the record's validity rule (ADR-017 T2-b), at the three seams that
// matter: where the record is authored, where it is decoded off the wire in the gateway,
// and where it is decoded on the phone.
//
//   - structured_chat && terminal_fallback is UNREPRESENTABLE. Today the pair is consistent
//     only by construction; a gate written over one of the two booleans enforces T2 rule 4
//     only for as long as that derivation stays right.
//   - terminal_control without terminal_fallback authorizes bytes to a screen that does
//     not exist (T6).
//   - a record with no session_instance binds no incarnation, so no generation and no
//     snapshot it carries can be refused when the session is replaced (T8-a).
//
// A nil receiver is INVALID rather than vacuously valid: absence is not validity, and a
// caller that validates before routing must be told there is nothing to route on (T2-a).
func (c *SessionCapabilities) Validate() error {
	if c == nil {
		return fmt.Errorf("%w: no record", ErrCapabilityInconsistent)
	}
	if c.StructuredChat && c.TerminalFallback {
		return fmt.Errorf("%w: structured_chat and terminal_fallback are mutually exclusive", ErrCapabilityInconsistent)
	}
	if c.TerminalControl && !c.TerminalFallback {
		return fmt.Errorf("%w: terminal_control without terminal_fallback", ErrCapabilityInconsistent)
	}
	if c.SessionInstance == "" {
		return fmt.Errorf("%w: no session_instance", ErrCapabilityInconsistent)
	}
	return nil
}

// AllowsTerminalWatch is the ONE predicate every watch gate is written over, on both
// booleans, every time (ADR-017 T2-b). It is nil-safe on purpose: "no record" and
// terminal_fallback=false take one code path, so the fail-closed default is not a nil
// check every caller writes for itself and one caller forgets (T2-a).
func (c *SessionCapabilities) AllowsTerminalWatch() bool {
	if c == nil || c.Validate() != nil {
		return false
	}
	return c.TerminalFallback && !c.StructuredChat
}

// AllowsTerminalControl is the same predicate one authority level up. terminal_fallback
// alone never grants it: control is granted only where terminal_control was authored true
// AT LAUNCH, never where terminal_fallback was derived by a degrade (ADR-017 T6-b).
func (c *SessionCapabilities) AllowsTerminalControl() bool {
	return c.AllowsTerminalWatch() && c.TerminalControl
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
//
// IT NEVER TOUCHES TerminalControl, and that omission is a RULING rather than an
// oversight (ADR-017 T6-b / D-DEGRADE-ORIGIN fence 1). The symmetrical-looking next edit
// -- also setting TerminalControl = true "for consistency" -- silently inverts T6-b and
// hands a keyboard to a degraded Claude session whose live TUI has an uncharacterized
// input region (the expected_input_revision gap T9 discloses as still open). A degrade
// grants a SCREEN, never a keyboard.
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
