package shimwire

import (
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
)

const (
	TypeClaudeStop       = "claude_stop"
	TypeClaudeStopResult = "claude_stop_result"
)

// ClaudeStopBinding is immutable reservation custody, independent of primary
// initialization and reader health. It conveys containment only, never cleanup.
type ClaudeStopBinding struct {
	Version         int                 `json:"v"`
	SchemaVersion   int                 `json:"schema_version"`
	SessionID       string              `json:"session_id"`
	RequiredRuntime string              `json:"required_runtime"`
	NativeSessionID string              `json:"native_session_id"`
	Incarnation     string              `json:"incarnation"`
	Runtime         logins.Runtime      `json:"runtime"`
	Cwd             string              `json:"cwd"`
	CLIIdentity     persist.CLIIdentity `json:"cli_identity"`
	ShimPID         int                 `json:"shim_pid"`
	ShimStartTime   int64               `json:"shim_start_time"`
	ScopeGeneration string              `json:"scope_generation"`
}

type ClaudeStopRequest struct {
	Version int               `json:"v"`
	Binding ClaudeStopBinding `json:"binding"`
}

type ClaudeStopResult struct {
	Version int               `json:"v"`
	Binding ClaudeStopBinding `json:"binding"`
	Status  string            `json:"status"` // delivered or unconfirmed, never death proof
}
