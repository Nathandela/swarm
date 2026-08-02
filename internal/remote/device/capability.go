// Package device is the daemon-side durable registry of paired remote-control
// devices (R-DEV.1). Each record pins a device's identity keys and its R-POL.6
// capability tier; the daemon reads it to authorize every remote command (R-POL.9).
// It is fed from a pairing MachineOutcome (whose DevicePayload now carries the
// Ed25519 command-signing key, ADR-007 2026-07-20).
package device

import "fmt"

// Capability is a device's authorization tier (R-POL.6). The zero value is
// deliberately invalid and grants nothing, so a zero-initialised or corrupted
// Capability fails closed.
type Capability uint8

const (
	// capInvalid is the zero value: an unset or corrupted tier that grants nothing.
	capInvalid Capability = iota
	// CapReadOnly may only read (list/journal/snapshot); it may not approve.
	CapReadOnly
	// CapReadApprove may read and approve agent interactions, but not control
	// (input/interrupt/kill/launch).
	CapReadApprove
	// CapFull may read, approve, and control.
	CapFull
)

// Action is a class of remote operation the daemon authorizes against a device's
// Capability (R-POL.9). Each concrete wire op maps onto exactly one of these classes.
type Action uint8

const (
	// ActionRead covers non-mutating ops (list, journal_read/subscribe, snapshot peek).
	ActionRead Action = iota
	// ActionApprove covers approving an agent interaction (R-POL.8).
	ActionApprove
	// ActionControl covers mutating control ops (input, interrupt, kill, launch,
	// take_control).
	ActionControl
)

// Allows reports whether a device holding capability c may perform action a. It is
// fail-closed: an unknown capability value grants nothing, and read_only/read_approve
// never reach ActionControl.
func (c Capability) Allows(a Action) bool {
	switch c {
	case CapFull:
		return a == ActionRead || a == ActionApprove || a == ActionControl
	case CapReadApprove:
		return a == ActionRead || a == ActionApprove
	case CapReadOnly:
		return a == ActionRead
	default:
		return false
	}
}

// valid reports whether c is one of the three defined tiers.
func (c Capability) valid() bool {
	return c == CapReadOnly || c == CapReadApprove || c == CapFull
}

var capToText = map[Capability]string{
	CapReadOnly:    "read_only",
	CapReadApprove: "read_approve",
	CapFull:        "full",
}

// MarshalText renders the tier as its stable snake_case name; an invalid tier is an
// error so a corrupted Capability can never be persisted.
func (c Capability) MarshalText() ([]byte, error) {
	s, ok := capToText[c]
	if !ok {
		return nil, fmt.Errorf("device: cannot marshal invalid capability %d", uint8(c))
	}
	return []byte(s), nil
}

// UnmarshalText parses a snake_case tier name. An unknown string is an error
// (fail-closed) -- it never silently decodes to a tier, above all not CapFull.
func (c *Capability) UnmarshalText(b []byte) error {
	switch string(b) {
	case "read_only":
		*c = CapReadOnly
	case "read_approve":
		*c = CapReadApprove
	case "full":
		*c = CapFull
	default:
		return fmt.Errorf("device: unknown capability %q", string(b))
	}
	return nil
}
