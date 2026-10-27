package persist

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/Nathandela/swarm/internal/claudeinput"
	"github.com/Nathandela/swarm/internal/status"
)

// Only the new Claude subsection uses version4; existing provider records keep3.
const ClaudeExecutionVersion = 4
const ClaudeObservationSubset = "claude_observation_subset"

// ClaudeExecution is the bounded observation subset; it never grants recovery.
type ClaudeExecution struct {
	BaseInteraction            status.Interaction            `json:"base_interaction"`
	DescriptorGeneration       string                        `json:"descriptor_generation"`
	ObserverEpoch              string                        `json:"observer_epoch"`
	NativeSessionID            string                        `json:"native_session_id"`
	Sequence                   uint64                        `json:"sequence"`
	LastFingerprint            string                        `json:"last_fingerprint"`
	Failure                    string                        `json:"failure"`
	StartupPrebootstrapUnknown bool                          `json:"startup_prebootstrap_unknown"`
	ObservationComplete        bool                          `json:"observation_complete"`
	Invocations                map[string]ClaudeInvocation   `json:"invocations"`
	Calls                      map[string]ClaudeCall         `json:"calls"`
	Turns                      map[string]ClaudeTurn         `json:"turns"`
	Children                   map[string]ClaudeChild        `json:"children"`
	Cancelled                  map[string]ClaudeCancellation `json:"cancelled"`
}
type ClaudeInvocation struct {
	Event    string          `json:"event"`
	Context  json.RawMessage `json:"context"`
	Data     json.RawMessage `json:"data"`
	Sequence uint64          `json:"sequence"`
}
type ClaudeCall struct {
	ActorID         string `json:"actor_id,omitempty"`
	TurnID          string `json:"turn_id,omitempty"`
	ToolID          string `json:"tool_id"`
	Tool            string `json:"tool"`
	Invocation      uint64 `json:"invocation"`
	ContextKnown    bool   `json:"context_known"`
	Returned        bool   `json:"returned"`
	AskPerson       bool   `json:"ask_person"`
	HumanRequestID  string `json:"human_request_id,omitempty"`
	AgentStatus     string `json:"agent_status,omitempty"`
	AgentActorID    string `json:"agent_actor_id,omitempty"`
	SpawnActorID    string `json:"spawn_actor_id,omitempty"`
	SpawnSequence   uint64 `json:"spawn_sequence"`
	FreshBackground bool   `json:"fresh_background"`
}
type ClaudeTurn struct {
	ActorID      string `json:"actor_id,omitempty"`
	TurnID       string `json:"turn_id"`
	Index        uint64 `json:"index"`
	Invocation   uint64 `json:"invocation"`
	NativeOrigin bool   `json:"native_origin"`
	ActiveStep   bool   `json:"active_step"`
}
type ClaudeChild struct {
	ToolID        string `json:"tool_id"`
	ParentActorID string `json:"parent_actor_id,omitempty"`
	ParentTurnID  string `json:"parent_turn_id"`
	SpawnSequence uint64 `json:"spawn_sequence"`
}
type ClaudeCancellation struct {
	ActorID          string `json:"actor_id,omitempty"`
	TurnID           string `json:"turn_id"`
	EnteredSequence  uint64 `json:"entered_sequence"`
	ReturnedSequence uint64 `json:"returned_sequence"`
}

func ClaudeCustodyKey(parts ...string) string { raw, _ := json.Marshal(parts); return string(raw) }
func claudeOptionalID(value string) bool      { return len(value) <= 128 }
func (c ClaudeExecution) Validate() error {
	if c.BaseInteraction != status.InteractionNone && c.BaseInteraction != status.InteractionUnknown && c.BaseInteraction != status.InteractionPrompt && c.BaseInteraction != status.InteractionPermission && c.BaseInteraction != status.InteractionError {
		return fmt.Errorf("persist: Claude base interaction missing or invalid")
	}
	if c.Invocations == nil || c.Calls == nil || c.Turns == nil || c.Children == nil || c.Cancelled == nil || !c.StartupPrebootstrapUnknown || c.ObservationComplete || len(c.Failure) > 128 || len(c.NativeSessionID) > 128 || len(c.DescriptorGeneration) > 32 || len(c.ObserverEpoch) > 128 || len(c.Invocations)+len(c.Calls)+len(c.Turns)+len(c.Children)+len(c.Cancelled) > MaxExecutionActors {
		return fmt.Errorf("persist: invalid Claude execution scope/bounds")
	}
	if c.Sequence == 0 {
		if c.LastFingerprint != "" || c.DescriptorGeneration != "" || c.ObserverEpoch != "" {
			return fmt.Errorf("persist: invalid initial Claude receipt")
		}
	} else if !executionID(c.NativeSessionID) || !executionID(c.ObserverEpoch) || len(c.DescriptorGeneration) != 32 || !executionDigest(c.LastFingerprint) {
		return fmt.Errorf("persist: invalid Claude receipt identity")
	}
	for key, frame := range c.Invocations {
		seq, err := strconv.ParseUint(key, 10, 64)
		if err != nil || strconv.FormatUint(seq, 10) != key || seq == 0 || seq > c.Sequence || frame.Sequence != seq || claudeinput.ValidateControlInvocation(frame.Event, seq, frame.Context, frame.Data) != nil {
			return fmt.Errorf("persist: invalid Claude invocation")
		}
	}
	for key, call := range c.Calls {
		if key != ClaudeCustodyKey(call.ActorID, call.TurnID, call.ToolID) || !claudeOptionalID(call.ActorID) || !claudeOptionalID(call.TurnID) || !executionID(call.ToolID) || len(call.ToolID) > 128 || call.Tool == "" || len(call.Tool) > 256 || call.Invocation == 0 || call.Invocation > c.Sequence || call.SpawnSequence > c.Sequence || call.ContextKnown && call.TurnID == "" || !claudeOptionalID(call.AgentActorID) || !claudeOptionalID(call.SpawnActorID) {
			return fmt.Errorf("persist: invalid Claude call")
		}
		if call.HumanRequestID != "" && (call.HumanRequestID != call.ToolID || call.Tool != "AskUserQuestion" || call.Returned || !call.ContextKnown) {
			return fmt.Errorf("persist: invalid Claude human guard")
		}
		if call.AgentStatus != "" && call.AgentStatus != "completed" && call.AgentStatus != "async_launched" && call.AgentStatus != "remote_launched" {
			return fmt.Errorf("persist: invalid Claude Agent status")
		}
	}
	for key, turn := range c.Turns {
		if key != ClaudeCustodyKey(turn.ActorID, turn.TurnID) || !claudeOptionalID(turn.ActorID) || !executionID(turn.TurnID) || len(turn.TurnID) > 128 || turn.Invocation == 0 || turn.Invocation > c.Sequence {
			return fmt.Errorf("persist: invalid Claude turn")
		}
	}
	for actor, child := range c.Children {
		if !executionID(actor) || len(actor) > 128 || !executionID(child.ToolID) || len(child.ToolID) > 128 || !claudeOptionalID(child.ParentActorID) || !executionID(child.ParentTurnID) || len(child.ParentTurnID) > 128 || child.SpawnSequence == 0 || child.SpawnSequence > c.Sequence {
			return fmt.Errorf("persist: invalid Claude child")
		}
	}
	for key, cancel := range c.Cancelled {
		if key != ClaudeCustodyKey(cancel.ActorID, cancel.TurnID) || !claudeOptionalID(cancel.ActorID) || !executionID(cancel.TurnID) || len(cancel.TurnID) > 128 || cancel.EnteredSequence == 0 || cancel.EnteredSequence >= cancel.ReturnedSequence || cancel.ReturnedSequence > c.Sequence {
			return fmt.Errorf("persist: invalid Claude cancellation")
		}
	}
	return nil
}
func executionDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
