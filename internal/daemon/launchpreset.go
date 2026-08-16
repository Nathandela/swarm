package daemon

// Wave R5 (bead agents-tracker-hggx.6, ADR-007 B144(b), playbook "Wave R5 -- phone
// remote launch"): MACHINE-AUTHORED launch presets and their staleness contract, as a
// launch-policy surface of THIS package -- the same package whose two-phase launch()
// the resolved preset feeds, so the preset path cannot fork into a parallel launch
// pipeline. The preset is authored by the terminal (`swarm remote presets`); the
// phone only ever selects and confirms. Nothing in a preset ever comes from a phone.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// LaunchPreset is one machine-authored launch policy: a stable opaque id the phone
// selects by, the display facts the confirm sheet renders, the provider, the
// CANONICAL (symlink-resolved at authoring) allowed workspace/worktree root, the
// allowlisted options, and the worktree-isolation default.
type LaunchPreset struct {
	ID          string            `json:"id"`
	DisplayName string            `json:"display_name"`
	Agent       string            `json:"agent"`
	Root        string            `json:"root"`
	Options     map[string]string `json:"options,omitempty"`
	Worktree    bool              `json:"worktree,omitempty"`
}

// ErrUnknownPreset / ErrStalePreset are ResolveLaunchPreset's sentinels: the
// machine-side decisions behind the stable unknown_preset / stale_preset refusal
// codes, made before any argv composition (playbook:447-448).
var (
	ErrUnknownPreset = errors.New("daemon: unknown launch preset")
	ErrStalePreset   = errors.New("daemon: stale launch preset revision")
)

// launchPresetsFile is the durable preset custody under the daemon state dir.
const launchPresetsFile = "launch-presets.json"

// launchPresetsPath is where the machine-authored presets live: directly under the
// state dir, beside remote-state.json, owner-only.
func launchPresetsPath(stateDir string) string {
	return filepath.Join(stateDir, launchPresetsFile)
}

// SaveLaunchPresets writes the authored presets durably at 0600. The presets file is
// remote-launch POLICY: a group/world-readable policy file is the same defect class
// ADR-004 pins for the session dir, so the mode is enforced even over a pre-existing
// file.
func SaveLaunchPresets(stateDir string, presets []LaunchPreset) error {
	data, err := json.MarshalIndent(presets, "", "  ")
	if err != nil {
		return fmt.Errorf("daemon: encode launch presets: %w", err)
	}
	path := launchPresetsPath(stateDir)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("daemon: write launch presets: %w", err)
	}
	// WriteFile's mode applies only on create; harden a pre-existing file too.
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("daemon: harden launch presets: %w", err)
	}
	return nil
}

// LoadLaunchPresets reads the authored presets. A missing file is ZERO presets and no
// error -- the fail-closed first-run state, in which session_launch can only answer
// unknown_preset until the terminal authors something.
func LoadLaunchPresets(stateDir string) ([]LaunchPreset, error) {
	data, err := os.ReadFile(launchPresetsPath(stateDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("daemon: read launch presets: %w", err)
	}
	var presets []LaunchPreset
	if err := json.Unmarshal(data, &presets); err != nil {
		return nil, fmt.Errorf("daemon: decode launch presets: %w", err)
	}
	return presets, nil
}

// PresetRevision is the deterministic content binding of one preset: identical
// content yields the identical revision (option order aside), and changing ANY
// policy-bearing field changes it. The phone echoes this revision inside its signed
// session_launch, so "the preset I confirmed" and "the preset the machine executes"
// are the same bytes or the launch refuses stale_preset. Length-prefixed sha256 so
// no two distinct presets share an encoding.
func PresetRevision(p LaunchPreset) string {
	h := sha256.New()
	field := func(b []byte) {
		var n [4]byte
		n[0], n[1], n[2], n[3] = byte(len(b)>>24), byte(len(b)>>16), byte(len(b)>>8), byte(len(b))
		h.Write(n[:])
		h.Write(b)
	}
	field([]byte(p.ID))
	field([]byte(p.DisplayName))
	field([]byte(p.Agent))
	field([]byte(p.Root))
	if p.Worktree {
		h.Write([]byte{1})
	} else {
		h.Write([]byte{0})
	}
	keys := make([]string, 0, len(p.Options))
	for k := range p.Options {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		field([]byte(k))
		field([]byte(p.Options[k]))
	}
	return "rev-" + hex.EncodeToString(h.Sum(nil)[:16])
}

// ResolveLaunchPreset resolves one preset by id at the phone-confirmed revision. An
// id this machine never authored is ErrUnknownPreset; a right id at a revision that
// no longer matches the preset's current content is ErrStalePreset -- the machine
// re-authored the preset between the phone's list and its confirm, so the launch the
// user confirmed is not the launch this machine would run.
func ResolveLaunchPreset(presets []LaunchPreset, id, revision string) (LaunchPreset, error) {
	for _, p := range presets {
		if p.ID != id {
			continue
		}
		if PresetRevision(p) != revision {
			return LaunchPreset{}, fmt.Errorf("%w: preset %q", ErrStalePreset, id)
		}
		return p, nil
	}
	return LaunchPreset{}, fmt.Errorf("%w: preset %q", ErrUnknownPreset, id)
}

// LaunchSpecForPreset composes the daemon LaunchSpec from the preset ALONE:
//
//   - Cwd is the SYMLINK-RESOLVED real path of the preset root (D8: the path the
//     policy checked is the path the shim gets -- no check-on-resolved/
//     use-on-original gap survives into the spec). A root that no longer resolves
//     refuses HERE, before any spec exists.
//   - ClientEnv is nil, unconditionally: env comes from daemon policy, never a phone.
//   - Options are the preset's own, COPIED -- a policy object shared by reference is
//     a policy an executed launch can edit. When the preset defaults to worktree
//     isolation the reserved "worktree" option is set (the same key
//     protocol.OptionWorktree names; the string is pinned by the skeleton's
//     worktree PreLaunch consumer).
//   - OperationID carries the signed operation id so the EXISTING two-phase
//     idempotent reservation engages on this exact spec, and InitialPrompt is the one
//     free-text field the phone may contribute.
func LaunchSpecForPreset(p LaunchPreset, operationID, initialPrompt string) (LaunchSpec, error) {
	resolved, err := filepath.EvalSymlinks(p.Root)
	if err != nil {
		return LaunchSpec{}, fmt.Errorf("daemon: preset %q root %q does not resolve: %w", p.ID, p.Root, err)
	}
	opts := make(map[string]string, len(p.Options)+1)
	for k, v := range p.Options {
		opts[k] = v
	}
	if p.Worktree {
		opts["worktree"] = "true"
	}
	return LaunchSpec{
		AgentType:     p.Agent,
		Cwd:           resolved,
		ClientEnv:     nil,
		Options:       opts,
		OperationID:   operationID,
		InitialPrompt: initialPrompt,
	}, nil
}
