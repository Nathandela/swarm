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

// remoteState is the on-disk shape of remoteStateFile. Enabled is the derived mirror
// (advisory, never a source of truth). ManualOff is the durable OWNER override behind
// `swarm remote off`/`on` (A4): once set it disables remote control regardless of paired
// devices, so it — unlike Enabled — IS authoritative and is loaded back at assembly.
type remoteState struct {
	Version   int  `json:"version"`
	Enabled   bool `json:"enabled"`
	ManualOff bool `json:"manual_off,omitempty"`
}

// RemoteControlEnabled makes coreAPI a protocol.KillSwitch (R-KS.1/.2): the global
// remote-control master switch is DERIVED from device presence and recomputed AT READ
// TIME (never cached at assembly), so a direct registry Add/Remove flips it. It is TRUE
// iff at least one device is paired (fail-closed on a nil registry) AND the owner has not
// manually turned remote control off (A4) — default OFF until paired, auto-off the instant
// the last device is revoked (a device lost without revocation is otherwise an open
// remote-control surface, RCE, R-KS.2), and OFF whenever the durable manual override is
// set (manual off WINS over device presence).
func (a *coreAPI) RemoteControlEnabled() bool {
	enabled := a.devices != nil && a.devices.Count() > 0 && !a.manualOff.Load()
	a.mirrorKillSwitch(enabled)
	return enabled
}

// SetRemoteControl makes coreAPI a protocol.RemoteControlSetter (A4): it durably flips the
// manual override behind `swarm remote off`/`on`. enabled=false sets ManualOff (disabling
// remote control regardless of paired devices); enabled=true clears it (returning to the
// device-derived value). The override is stored in-memory (atomic, read lock-free on every
// remote op by RemoteControlEnabled) AND persisted to remote-state.json so it survives a
// restart. The durable write is atomic (temp+rename+Sync) and coherent with the derived
// Enabled mirror.
func (a *coreAPI) SetRemoteControl(enabled bool) error {
	a.manualOff.Store(!enabled)
	if a.stateDir == "" {
		return nil // no durable home (test seam); the in-memory override still applies
	}
	// Recompute the derived value AFTER storing the override so the mirror stays coherent.
	derived := a.devices != nil && a.devices.Count() > 0 && enabled
	a.ksMu.Lock()
	defer a.ksMu.Unlock()
	if err := writeRemoteState(a.stateDir, remoteState{Version: 1, Enabled: derived, ManualOff: !enabled}); err != nil {
		return err
	}
	a.ksPersisted = &derived
	return nil
}

// coreAPI ALSO satisfies protocol.KillSwitch so the assembled remote-tier Server refuses
// every remote mutating op with CodeKillSwitch as its FIRST gate while no device is paired
// (R-KS.1, fail-closed-before-signature), and protocol.RemoteControlSetter so the owner
// tier can durably sever remote control (A4).
var (
	_ protocol.KillSwitch          = (*coreAPI)(nil)
	_ protocol.RemoteControlSetter = (*coreAPI)(nil)
)

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
	// Preserve the durable manual override alongside the derived mirror: the file carries
	// BOTH fields, and this write must not clobber ManualOff (A4). manualOff is read live
	// so a concurrent SetRemoteControl is reflected, not lost.
	if err := writeRemoteState(a.stateDir, remoteState{Version: 1, Enabled: enabled, ManualOff: a.manualOff.Load()}); err != nil {
		return
	}
	a.ksPersisted = &enabled
}

// writeRemoteState writes remote-state.json atomically (temp+rename, 0600), mirroring the
// device registry's process-crash durability model (R-KS.1).
func writeRemoteState(stateDir string, st remoteState) error {
	data, err := json.Marshal(st)
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

// loadRemoteState reads the durable remote-state.json so the assembly can restore the
// manual override across a restart (A4). An ABSENT file is the common case (the override
// was never set) and yields a zero state (ManualOff=false, device-derived), nil error. A
// present-but-corrupt file fails CLOSED for the override — ManualOff=true — so a valid
// `swarm remote off` is never silently lost to a bad read (the derived Enabled mirror is
// advisory and is recomputed anyway); the corrupt file self-heals on the next transition.
func loadRemoteState(stateDir string) (remoteState, error) {
	data, err := os.ReadFile(filepath.Join(stateDir, remoteStateFile))
	if os.IsNotExist(err) {
		return remoteState{}, nil
	}
	if err != nil {
		return remoteState{ManualOff: true}, err
	}
	var st remoteState
	if err := json.Unmarshal(data, &st); err != nil {
		return remoteState{ManualOff: true}, err
	}
	return st, nil
}
