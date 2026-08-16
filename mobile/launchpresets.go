package swarmmobile

// The Wave R5 phone remote-launch surface (bead agents-tracker-hggx.6, playbook 4.3):
// the phone SELECTS and CONFIRMS a machine-authored preset; it never composes argv,
// cwd, env, or options. Three verbs:
//
//   - LaunchPresets renders WHAT THE MACHINE PUBLISHED and nothing else -- the last
//     launch_presets reply this phone adopted. A phone the machine published nothing
//     to renders an honest empty state (ADR-007 B135: no Go-constant defaults dressed
//     up as wire facts).
//   - RefreshLaunchPresets asks the machine for its current list: one signed
//     launch_presets op, live-only like every signed command. The reply lands on the
//     ordinary reply plane and is adopted when the screen claims it via Outcome.
//   - SessionLaunch confirms ONE preset at the revision the screen displayed. The
//     revision parameter is the staleness binding: an intervening machine-side edit
//     answers stale_preset rather than silently launching different policy.

import (
	"errors"
	"fmt"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// opLaunchPresets is the launch_presets wire op/action spelling, pinned locally for
// opTakeControlEnd's reason: PB-BIND-0 keeps internal/protocol out of the bound
// closure, so this package cannot name protocol.OpLaunchPresets. The schema constant
// carries the same string and is the authority here.
const opLaunchPresets = schema.ActionLaunchPresets

// PresetInfo is one machine-authored launch preset as the selection screen renders
// it. Every field is a wire fact from the machine's launch_presets reply; the phone
// invents none of them.
type PresetInfo struct {
	ID            string
	DisplayName   string
	Provider      string
	WorkspacePath string
	// Revision is what the confirm sheet must echo into SessionLaunch: the screen
	// signs exactly the revision it displayed, never a derived one.
	Revision string
	Worktree bool
}

// PresetList is the preset collection HANDLE (gomobile binds no list type; the
// MachineList/SessionList idiom).
type PresetList struct {
	items []PresetInfo
}

// Count is the number of machine-published presets.
func (l *PresetList) Count() (n int, err error) {
	defer barrier(&err)
	if l == nil {
		return 0, errNoReceiver
	}
	return len(l.items), nil
}

// At returns the preset row at index i.
func (l *PresetList) At(i int) (p *PresetInfo, err error) {
	defer barrier(&err)
	if l == nil {
		return nil, errNoReceiver
	}
	if i < 0 || i >= len(l.items) {
		return nil, classed(ErrClassNotFound,
			fmt.Errorf("swarmmobile: preset index %d out of range [0,%d)", i, len(l.items)))
	}
	item := l.items[i]
	return &item, nil
}

// LaunchPresets returns the machine-published preset list as last adopted from a
// launch_presets reply -- a pure read, callable offline. Empty until the machine
// answers a RefreshLaunchPresets: the empty state is the screens' to render honestly
// (the phone cannot know what the machine would allow).
func (a *App) LaunchPresets() (list *PresetList, err error) {
	defer barrier(&err)
	if _, err = a.ready(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return &PresetList{items: append([]PresetInfo(nil), a.presets...)}, nil
}

// RefreshLaunchPresets asks the machine for its current preset list: one signed
// launch_presets op (the custody lives daemon-side behind the one authorization
// plane, so the read is signed like every semantic op). Live-only; the reply is
// adopted into LaunchPresets when the screen claims it via Outcome.
func (a *App) RefreshLaunchPresets() (op *Op, err error) {
	defer barrier(&err)
	return a.signedCommand(opLaunchPresets, schema.OperationSessionSentinel, nil, commandBody{})
}

// SessionLaunch confirms ONE machine-authored preset: a signed session_launch op over
// the selected preset id at the CONFIRMED revision, with the initial prompt as the
// phone's one free-text contribution. The signed tuple binds all three via
// schema.SessionLaunchContentHash, so nothing between this phone and the daemon can
// re-point the confirmation at different policy.
//
// LIVE-ONLY, NEVER QUEUED (B43): with no connection it refuses ErrClassOffline before
// anything is composed or sealed -- an offline launch op re-sent later could land on
// a preset that was re-authored meanwhile. An empty preset id or revision is a screen
// bug, refused ErrClassInvalidRequest before any signing: a revisionless launch would
// be un-refusable as stale, which is the whole staleness contract.
func (a *App) SessionLaunch(presetID, presetRevision, prompt string) (op *Op, err error) {
	defer barrier(&err)
	if presetID == "" || presetRevision == "" {
		return nil, classed(ErrClassInvalidRequest, errors.New(
			"swarmmobile: SessionLaunch needs the preset id and the confirmed revision the screen displayed"))
	}
	req := &schema.SessionLaunchReq{
		PresetID:       presetID,
		PresetRevision: presetRevision,
		InitialPrompt:  prompt,
		Cols:           defaultLaunchCols,
		Rows:           defaultLaunchRows,
	}
	return a.signedCommand(schema.ActionSessionLaunch, schema.OperationSessionSentinel,
		schema.SessionLaunchContentHash(req), commandBody{sessionLaunch: req})
}

// adoptPresets folds a launch_presets reply into the App's preset cache: REPLACE,
// never merge -- the reply is the machine's complete current list, and an empty one
// means the machine published none (which the screens must render, not paper over).
// Any non-reply (an error, another op) is left alone.
func (a *App) adoptPresets(ctrl schema.Control) {
	if ctrl.Op != opLaunchPresets || ctrl.ErrorCode != "" {
		return
	}
	items := make([]PresetInfo, 0, len(ctrl.Presets))
	for _, p := range ctrl.Presets {
		items = append(items, PresetInfo{
			ID:            p.ID,
			DisplayName:   p.DisplayName,
			Provider:      p.Agent,
			WorkspacePath: p.Root,
			Revision:      p.Revision,
			Worktree:      p.Worktree,
		})
	}
	a.mu.Lock()
	a.presets = items
	// The reply also states THIS device's registry-pinned tier (round-2 fix-pack).
	// Adopted verbatim, REPLACE like the list: a machine that re-tiers the device is
	// believed. An empty stamp (a backend without the capability seam) keeps the
	// prior word rather than blanking it -- absence of the fact is not a demotion.
	if ctrl.DeviceCapability != "" {
		a.launchCapability = ctrl.DeviceCapability
	}
	a.mu.Unlock()
}

// LaunchCapability is this device's own authorization tier as the machine last
// stated it on a launch_presets reply: "full", "read_only" or "read_approve" -- or
// "" while the machine has not answered one yet (the launch screen's first-run
// state). A wire fact, never derived phone-side: capability is pinned machine-side
// at enrollment and no other reply carries it to the device it describes.
func (a *App) LaunchCapability() (tier string, err error) {
	defer barrier(&err)
	if _, err = a.ready(); err != nil {
		return "", err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.launchCapability, nil
}
