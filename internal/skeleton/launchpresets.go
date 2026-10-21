package skeleton

// Wave R5 (bead agents-tracker-hggx.6): the coreAPI half of the phone remote-launch
// path -- the assembled daemon's implementations of protocol.LaunchPresetSource,
// protocol.OperationStatusSource and protocol.ActivityRecorder. The preset custody is
// the daemon package's (machine-authored via `swarm remote presets`, stored 0600
// under the state dir); this file only converts it to the wire view and maps the
// sentinels, so the protocol handler's refusal decisions ride the daemon's own
// policy surfaces rather than a re-implementation.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// The audit trail is bounded independently of durable operation/replay custody.
const (
	remoteActivityFile   = "remote-activity.log"
	remoteActivityLimit  = 1 << 20
	remoteActivityRetain = remoteActivityLimit / 2
)

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
	line = append(line, '\n')
	if len(line) > remoteActivityLimit {
		return
	}
	a.activityMu.Lock()
	defer a.activityMu.Unlock()
	path := filepath.Join(a.stateDir, remoteActivityFile)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Nlink != 1 || st.Uid != uint32(os.Getuid()) || f.Chmod(0o600) != nil {
		return
	}
	complete := info.Size() == 0
	if !complete {
		var last [1]byte
		_, err = f.ReadAt(last[:], info.Size()-1)
		complete = err == nil && last[0] == '\n'
	}
	if complete && info.Size() <= int64(remoteActivityLimit-len(line)) {
		_, _ = f.Write(line)
		return
	}
	// Leave room for many ordinary appends before another compaction.
	keep := min(int64(remoteActivityRetain), int64(remoteActivityLimit-len(line)), info.Size())
	tail := make([]byte, keep)
	offset := info.Size() - keep
	if _, err := f.ReadAt(tail, offset); err != nil {
		return
	}
	if offset > 0 {
		if first := bytes.IndexByte(tail, '\n'); first >= 0 {
			tail = tail[first+1:]
		} else {
			tail = nil
		}
	}
	// An interrupted append is not a complete audit record.
	tail = tail[:bytes.LastIndexByte(tail, '\n')+1]
	tmp, err := os.CreateTemp(a.stateDir, ".remote-activity-*")
	if err != nil {
		return
	}
	defer func() { _ = tmp.Close(); _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(append(tail, line...)); err != nil {
		return
	}
	if tmp.Sync() != nil || tmp.Close() != nil {
		return
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, current) {
		return
	}
	_ = os.Rename(tmp.Name(), path)
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
