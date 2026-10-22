package skeleton

import (
	"encoding/json"
	"path/filepath"

	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

type loginNativeReady struct {
	Runtime        logins.Runtime `json:"runtime"`
	ConversationID string         `json:"conversation_id"`
	ShimPID        int            `json:"shim_pid"`
	ShimStartTime  int64          `json:"shim_start_time"`
	Sequence       uint64         `json:"sequence"`
}

func (d *Daemon) recordLoginReady(meta persist.Meta, cb engine.Callback) error {
	if meta.LoginRuntime == nil {
		return nil
	}
	raw, err := json.Marshal(loginNativeReady{Runtime: *meta.LoginRuntime, ConversationID: meta.ConversationID, ShimPID: meta.ShimPID, ShimStartTime: meta.ShimStartTime, Sequence: cb.Sequence})
	if err != nil {
		return err
	}
	_, err = writeAuthWatchState(filepath.Join(d.stateDir, meta.ID, "login-native-ready.json"), raw)
	return err
}
func (d *Daemon) loginRecoveryReady(meta persist.Meta) bool {
	if meta.AgentType != logins.ProviderClaude {
		return d.authRecoveryReady(meta)
	}
	if meta.LoginRuntime == nil || meta.Status.Process != status.ProcessRunning || meta.Status.Turn != status.TurnIdle || meta.Status.Interaction != status.InteractionNone {
		return false
	}
	var ready loginNativeReady
	return readNativeProof(d.stateDir, meta.ID, "login-native-ready.json", &ready) == nil && ready.Runtime == *meta.LoginRuntime && ready.ConversationID == meta.ConversationID && ready.ShimPID == meta.ShimPID && ready.ShimStartTime == meta.ShimStartTime && ready.Sequence > 0
}
