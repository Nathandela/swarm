package skeleton

// Wave R5 (bead agents-tracker-hggx.6): the coreAPI half of the phone remote-launch
// path -- the assembled daemon's implementations of protocol.LaunchPresetSource,
// protocol.OperationStatusSource and protocol.ActivityRecorder. The preset custody is
// the daemon package's (machine-authored via `swarm remote presets`, stored 0600
// under the state dir); this file only converts it to the wire view and maps the
// sentinels, so the protocol handler's refusal decisions ride the daemon's own
// policy surfaces rather than a re-implementation.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// remoteActivityFile is the ADR-007 D10 activity log: an owner-readable append-only
// record, under the state dir, of every remote-originated mutation and refusal.
const remoteActivityFile = "remote-activity.log"

// LaunchPresetList makes coreAPI a protocol.LaunchPresetSource: EXACTLY the
// machine-authored presets, as wire views, plus a content-bound policy revision. An
// unreadable or absent custody answers an empty list -- fail-closed, nothing is
// launchable until the terminal authors something, and never an invented default.
func (a *coreAPI) LaunchPresetList() ([]schema.LaunchPresetView, string) {
	presets, err := daemon.LoadLaunchPresets(a.stateDir)
	if err != nil || len(presets) == 0 {
		return nil, presetPolicyRevision(nil)
	}
	views := make([]schema.LaunchPresetView, 0, len(presets))
	for _, p := range presets {
		views = append(views, presetView(p))
	}
	return views, presetPolicyRevision(presets)
}

// ResolveLaunchPreset makes coreAPI resolve one preset by id at the phone-confirmed
// revision, riding the daemon's own sentinels: unknown -> protocol.ErrUnknownPreset,
// changed revision -> protocol.ErrStalePreset (playbook:447-448). The returned view
// carries the SYMLINK-RESOLVED root via daemon.LaunchSpecForPreset -- the one place
// root resolution lives -- so the path the policy checks is the path the shim gets
// (D8), and an unresolvable root refuses here as a plain (policy) error before any
// spec exists.
func (a *coreAPI) ResolveLaunchPreset(id, revision string) (schema.LaunchPresetView, error) {
	presets, err := daemon.LoadLaunchPresets(a.stateDir)
	if err != nil {
		return schema.LaunchPresetView{}, err
	}
	p, err := daemon.ResolveLaunchPreset(presets, id, revision)
	switch {
	case errors.Is(err, daemon.ErrUnknownPreset):
		return schema.LaunchPresetView{}, protocol.ErrUnknownPreset
	case errors.Is(err, daemon.ErrStalePreset):
		return schema.LaunchPresetView{}, protocol.ErrStalePreset
	case err != nil:
		return schema.LaunchPresetView{}, err
	}
	spec, err := daemon.LaunchSpecForPreset(p, "", "")
	if err != nil {
		return schema.LaunchPresetView{}, err
	}
	v := presetView(p)
	v.Root = spec.Cwd        // resolved real path, not the stored spelling
	v.Options = spec.Options // the copied (never aliased) option map
	return v, nil
}

// presetView converts one stored preset to its wire view.
func presetView(p daemon.LaunchPreset) schema.LaunchPresetView {
	opts := make(map[string]string, len(p.Options))
	for k, val := range p.Options {
		opts[k] = val
	}
	return schema.LaunchPresetView{
		ID:          p.ID,
		DisplayName: p.DisplayName,
		Agent:       p.Agent,
		Root:        p.Root,
		Options:     opts,
		Worktree:    p.Worktree,
		Revision:    daemon.PresetRevision(p),
	}
}

// presetPolicyRevision is the content binding of the WHOLE preset policy: any edit to
// any preset changes it, so an operator can correlate a phone's stale view with the
// machine's current policy at a glance. Deterministic over the sorted per-preset
// revisions (which are themselves content-bound).
func presetPolicyRevision(presets []daemon.LaunchPreset) string {
	revs := make([]string, 0, len(presets))
	for _, p := range presets {
		revs = append(revs, daemon.PresetRevision(p))
	}
	sort.Strings(revs)
	h := sha256.New()
	for _, r := range revs {
		h.Write([]byte(r))
		h.Write([]byte{0})
	}
	return "policy-" + hex.EncodeToString(h.Sum(nil)[:8])
}

// RemoteOperationOutcome makes coreAPI a protocol.OperationStatusSource over the
// daemon's read-only reconciliation surface. The session id is namespaced to the wire
// form the phone's roster carries, so the applied answer names a session the phone
// can actually find.
func (a *coreAPI) RemoteOperationOutcome(operationID string) (schema.OperationOutcomeView, bool) {
	out, ok := a.core.OperationOutcome(operationID)
	if !ok {
		return schema.OperationOutcomeView{}, false
	}
	view := schema.OperationOutcomeView{State: out.State}
	if out.SessionID != "" {
		view.SessionID = protocol.NamespacedID(a.endpointID, out.SessionID)
	}
	return view, true
}

// RecordRemoteActivity makes coreAPI a protocol.ActivityRecorder (ADR-007 D10): one
// JSON line per remote-originated mutation or refusal, appended to the owner-only
// activity log under the state dir. Best-effort by design: the record is an audit
// trail for the terminal owner, and a failed append must not fail the launch it
// records -- there is no other sink to report the append failure to.
func (a *coreAPI) RecordRemoteActivity(rec schema.ActivityRecord) {
	entry := struct {
		At time.Time `json:"at"`
		schema.ActivityRecord
	}{At: time.Now(), ActivityRecord: rec}
	line, err := json.Marshal(entry)
	if err != nil {
		return
	}
	path := filepath.Join(a.stateDir, remoteActivityFile)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	// O_CREATE's mode applies only on create; harden a pre-existing file too (round-2
	// fix-pack, MEDIUM 1) -- daemon.SaveLaunchPresets's own stated rule, for the same
	// reason: a group/world-readable activity log leaks device ids and operation ids
	// to every local user, which is the ADR-004 defect class.
	_ = os.Chmod(path, 0o600)
	_, _ = f.Write(append(line, '\n'))
}

// DeviceCapability makes coreAPI a protocol.DeviceCapabilitySource (round-2
// fix-pack): the launch_presets reply states the signer's registry-pinned tier, the
// phone's only honest source for its tier-denied launch state. Reads the SAME pinned
// registry authorizeCommand authorizes against -- never the wire -- and fails honest
// on every gap (no registry, unknown device, invalid tier): ok=false, never an
// invented tier.
func (a *coreAPI) DeviceCapability(deviceID string) (string, bool) {
	if a.devices == nil {
		return "", false
	}
	rec, ok := a.devices.Get(deviceID)
	if !ok {
		return "", false
	}
	text, err := rec.Capability.MarshalText()
	if err != nil {
		return "", false
	}
	return string(text), true
}

// The assembled remote-tier Server serves the Wave R5 preset flow through these
// seams; a compile-time tie so a refactor cannot silently drop one.
var (
	_ protocol.LaunchPresetSource     = (*coreAPI)(nil)
	_ protocol.OperationStatusSource  = (*coreAPI)(nil)
	_ protocol.ActivityRecorder       = (*coreAPI)(nil)
	_ protocol.DeviceCapabilitySource = (*coreAPI)(nil)
)
