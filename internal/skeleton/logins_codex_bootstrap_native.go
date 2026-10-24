package skeleton

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/status"
)

func bootstrapProofFields(raw json.RawMessage, keys ...string) (map[string]json.RawMessage, bool) {
	object, ok := decodeStrictObject(raw)
	if !ok {
		return nil, false
	}
	for key := range object {
		known := false
		for _, allowed := range keys {
			if key == allowed {
				known = true
				break
			}
		}
		if !known {
			return nil, false
		}
	}
	return object, true
}
func readBootstrapNativeProof(root, local, name string, value any) error {
	var raw json.RawMessage
	if err := readNativeProof(root, local, name, &raw); err != nil {
		return err
	}
	var native json.RawMessage
	switch name {
	case shim.NativeProcessFile:
		native = raw
	case shim.NativeStoppedFile:
		object, ok := bootstrapProofFields(raw, "schema_version", "native", "writers_stopped", "descendants_contained")
		if !ok {
			return logins.ErrUnsafePath
		}
		native = object["native"]
	case shim.BackendFile:
		if _, ok := bootstrapProofFields(raw, "pid", "pgid", "started_at_ms", "socket_path"); !ok {
			return logins.ErrUnsafePath
		}
	default:
		return logins.ErrUnsafePath
	}
	if native != nil {
		object, ok := bootstrapProofFields(native, "schema_version", "generation", "backend_pid", "backend_start_time", "pid", "pgid", "start_time", "shim_pid", "shim_start_time", "provider", "runtime", "incident_id")
		if !ok {
			return logins.ErrUnsafePath
		}
		if _, ok = bootstrapProofFields(object["runtime"], "provider", "epoch", "identity", "home", "external_auth"); !ok {
			return logins.ErrUnsafePath
		}
	}
	return json.Unmarshal(raw, value)
}

// Missing birth/stop evidence preserves an obligation; it never manufactures retirement.
func (d *Daemon) codexBootstrapNative(meta persist.Meta, incident ...string) (logins.NativeMember, error) {
	expectedIncident := meta.InputEmbargo
	if len(incident) != 0 {
		expectedIncident = incident[0]
	}
	if meta.InputEmbargo != "" && meta.InputEmbargo != expectedIncident {
		return logins.NativeMember{}, logins.ErrPending
	}
	if meta.AgentType != logins.ProviderCodex || meta.LoginRuntime == nil || !meta.LoginRuntime.ExternalAuth || meta.ShimPID <= 0 || meta.ShimStartTime <= 0 {
		return logins.NativeMember{}, logins.ErrPending
	}
	var native shim.NativeProcessInfo
	if readBootstrapNativeProof(d.stateDir, meta.ID, shim.NativeProcessFile, &native) != nil {
		return logins.NativeMember{}, logins.ErrPending
	}
	generation, generationErr := hex.DecodeString(native.Generation)
	if generationErr != nil || len(generation) != 16 || native.SchemaVersion != shim.NativeWriterSchemaVersion || native.Provider != meta.AgentType || native.Runtime != *meta.LoginRuntime || native.ShimPID != meta.ShimPID || native.ShimStartTime != meta.ShimStartTime || native.IncidentID != expectedIncident || native.PID <= 0 || native.PGID != native.PID || native.StartTime <= 0 || native.BackendPID <= 0 || native.BackendStartTime <= 0 {
		return logins.NativeMember{}, logins.ErrPending
	}
	if meta.Status.Process == status.ProcessRunning && d.core != nil {
		instance, ok := d.sessionInstance(meta.ID)
		channel, planned := d.core.SessionBackend(meta.ID)
		if ok && planned {
			_, backend, err := d.readCodexExecutionNative(meta.ID, instance, channel.SocketPath)
			var final shim.NativeProcessInfo
			if err == nil && backend.PID == native.BackendPID && backend.StartedAtMS == native.BackendStartTime && readBootstrapNativeProof(d.stateDir, meta.ID, shim.NativeProcessFile, &final) == nil && final == native {
				return bootstrapNativeMember(meta, backend), nil
			}
		}
		return logins.NativeMember{}, logins.ErrPending
	}
	var backend shim.BackendInfo
	var stopped shim.NativeStoppedInfo
	if readBootstrapNativeProof(d.stateDir, meta.ID, shim.NativeProcessFile, &native) != nil || readBootstrapNativeProof(d.stateDir, meta.ID, shim.BackendFile, &backend) != nil || readBootstrapNativeProof(d.stateDir, meta.ID, shim.NativeStoppedFile, &stopped) != nil {
		return logins.NativeMember{}, logins.ErrPending
	}
	if native.Provider != meta.AgentType || native.Runtime != *meta.LoginRuntime || native.ShimPID != meta.ShimPID || native.ShimStartTime != meta.ShimStartTime || native.IncidentID != expectedIncident || native.BackendPID <= 0 || native.BackendStartTime <= 0 || backend.PID != native.BackendPID || backend.PGID != native.BackendPID || backend.StartedAtMS != native.BackendStartTime || backend.SocketPath != filepath.Join(d.stateDir, meta.ID, "codex.sock") || stopped.Native != native {
		return logins.NativeMember{}, logins.ErrPending
	}
	if err := verifyLoginWritersStopped(d.stateDir, meta); err != nil {
		return logins.NativeMember{}, logins.ErrPending
	}
	var final shim.NativeProcessInfo
	var finalBackend shim.BackendInfo
	if readBootstrapNativeProof(d.stateDir, meta.ID, shim.NativeProcessFile, &final) != nil || final != native || readBootstrapNativeProof(d.stateDir, meta.ID, shim.BackendFile, &finalBackend) != nil || finalBackend != backend {
		return logins.NativeMember{}, logins.ErrPending
	}
	return bootstrapNativeMember(meta, backend), nil
}
func bootstrapNativeMember(meta persist.Meta, native shim.BackendInfo) logins.NativeMember {
	return logins.NativeMember{SessionID: meta.ID, ConversationID: meta.ConversationID, NativeIncarnation: fmt.Sprintf("%d:%d", native.PID, native.StartedAtMS), NativePID: native.PID, NativeStartTime: native.StartedAtMS}
}
