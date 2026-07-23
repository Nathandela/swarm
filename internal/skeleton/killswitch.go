package skeleton

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/Nathandela/swarm/internal/protocol"
)

// remoteStateFile is the durable remote-control kill-switch mirror under the state dir
// (R-KS.1). It is an artifact of the derived switch, never the source of truth: the
// enabled value is always recomputed from device presence at read time.
const remoteStateFile = "remote-state.json"

// remoteState is the on-disk shape of remoteStateFile.
type remoteState struct {
	Version int  `json:"version"`
	Enabled bool `json:"enabled"`
}

// RemoteControlEnabled makes coreAPI a protocol.KillSwitch (R-KS.1/.2): the global
// remote-control master switch is DERIVED from device presence and recomputed AT READ
// TIME (never cached at assembly), so a direct registry Add/Remove flips it. It is TRUE
// iff at least one device is paired (fail-closed on a nil registry) — default OFF until
// paired, and auto-off the instant the last device is revoked, since a device lost
// without revocation is otherwise an open remote-control surface (RCE, R-KS.2).
func (a *coreAPI) RemoteControlEnabled() bool {
	enabled := a.devices != nil && a.devices.Count() > 0
	a.mirrorKillSwitch(enabled)
	return enabled
}

// coreAPI ALSO satisfies protocol.KillSwitch so the assembled remote-tier Server refuses
// every remote mutating op with CodeKillSwitch as its FIRST gate while no device is paired
// (R-KS.1, fail-closed-before-signature).
var _ protocol.KillSwitch = (*coreAPI)(nil)

// mirrorKillSwitch persists the derived kill-switch state to the durable 0600
// remote-state.json on each TRANSITION (R-KS.1), guarded so the on-every-remote-op
// concurrent readers never race the write. ponytail: read-time diff-write, because the
// device registry has no change-observer hook and the E2E tests mutate it via direct
// Add/Remove that any hook would bypass — so the mirror writes only when the derived
// value differs from the last one this process persisted, not on every call. A write
// failure is advisory (the registry stays authoritative) and is NOT remembered, so the
// next read retries.
func (a *coreAPI) mirrorKillSwitch(enabled bool) {
	if a.stateDir == "" {
		return
	}
	a.ksMu.Lock()
	defer a.ksMu.Unlock()
	if a.ksPersisted != nil && *a.ksPersisted == enabled {
		return
	}
	if err := writeRemoteState(a.stateDir, enabled); err != nil {
		return
	}
	a.ksPersisted = &enabled
}

// writeRemoteState writes remote-state.json atomically (temp+rename, 0600), mirroring the
// device registry's process-crash durability model (R-KS.1).
func writeRemoteState(stateDir string, enabled bool) error {
	data, err := json.Marshal(remoteState{Version: 1, Enabled: enabled})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(stateDir, remoteStateFile+".tmp*") // os.CreateTemp creates 0600
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename; cleans up on any error
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, filepath.Join(stateDir, remoteStateFile))
}
