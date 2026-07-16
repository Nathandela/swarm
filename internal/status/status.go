// Package status defines the session status model shared by every package
// that observes or reports on agent sessions: three orthogonal dimensions
// (process, turn, interaction) and the derivation of a single display Group
// from them. See docs/specifications/system-spec.md, "Session status model"
// (lines 41-56), for the spec this package implements.
//
// This package is dependency-free by design: it is the one place these
// types exist, so every other package can share them without pulling in
// anything beyond the standard library.
package status

// Process is the lifecycle state of a session's underlying process.
type Process string

const (
	ProcessRunning Process = "running"
	ProcessExited  Process = "exited"
	ProcessLost    Process = "lost"
)

// Turn is whether the agent is currently computing or waiting on the user.
type Turn string

const (
	TurnActive  Turn = "active"
	TurnIdle    Turn = "idle"
	TurnUnknown Turn = "unknown"
)

// Interaction is what kind of input the agent is waiting on, if any.
type Interaction string

const (
	InteractionNone       Interaction = "none"
	InteractionPrompt     Interaction = "prompt"
	InteractionPermission Interaction = "permission"
	InteractionUnknown    Interaction = "unknown"
)

// Group is the derived, display-facing status shown to users.
type Group string

const (
	GroupNeedsInput     Group = "needs_input"
	GroupWorking        Group = "working"
	GroupReadyForReview Group = "ready_for_review"
	GroupCompleted      Group = "completed"
)

// Status is a session's raw state along the three orthogonal dimensions.
type Status struct {
	Process     Process
	Turn        Turn
	Interaction Interaction
}

// Derive maps a Status to its display Group, per the precedence hierarchy
// in system-spec.md:51-56:
//
//  1. Any process other than running (exited or lost) is Completed,
//     regardless of turn or interaction.
//  2. Otherwise, an active or unknown turn is Working: the spec's Working
//     rule ORs in both with no interaction qualifier, so it wins outright.
//  3. Otherwise (running and idle), interaction decides: prompt or
//     permission is Needs input; none or unknown is Ready for review.
func Derive(s Status) Group {
	if s.Process != ProcessRunning {
		return GroupCompleted
	}
	if s.Turn == TurnActive || s.Turn == TurnUnknown {
		return GroupWorking
	}
	if s.Interaction == InteractionPrompt || s.Interaction == InteractionPermission {
		return GroupNeedsInput
	}
	return GroupReadyForReview
}
