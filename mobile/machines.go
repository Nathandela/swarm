package swarmmobile

// The multi-machine surface is served by a v2 registry from first launch. A fresh
// unpaired phone owns only its reserved staging namespace; after authenticated pairing
// commits it becomes a normal registry entry. Every pairing has an independent
// namespace with its own keys, seq spaces, cursors and push address.
//
// WHAT THIS SLICE DOES NOT DO, stated rather than implied (the physical
// three-machines-two-relays exit is the owner's; docs/verification/r4-multimachine.md
// discloses the same): SelectMachine records the viewed pairing and feeds the
// deterministic least-recently-viewed connection policy; it does not yet re-target the
// App's live relay session, and pairing a SECOND machine lands its namespace awaiting
// that machine's pairing ceremony rather than performing one.

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/status"
)

// foregroundConnectionCap is the DOCUMENTED foreground connection cap (playbook 4.2:
// "When foregrounded, the app connects to every paired machine within a documented
// concurrency cap"). Connections beyond it are parked by the deterministic
// least-recently-viewed policy and their rows render their last-sync age.
const foregroundConnectionCap = 3

var commitBootstrapAuthority = func(reg *phonecore.MachineRegistry, d phonecore.MachineDescriptor) error {
	return reg.CommitBootstrap(d)
}

// MachineInfo is one machine-switcher row: the four facts of playbook 4.2:198 -- name,
// reachability, last successful sync, needs-input count -- keyed by machine id.
// Identity is the ID; DisplayName is never an authority, so two rows may share a name
// without colliding (MM4).
//
// Broken is MM8's per-machine recovery fact: this pairing's durable state failed to
// resume (Keystore invalidation, backup exclusion, a corrupt blob), BrokenReason says
// why, and the row's affordances are forget-or-re-pair. The aggregate surface says
// WHICH row is broken rather than failing wholesale (machines.recovery).
type MachineInfo struct {
	ID             string
	DisplayName    string
	Connected      bool
	Stale          bool
	LastSyncUnixMs int64
	NeedsInput     int
	Broken         bool
	BrokenReason   string
}

// MachineList is the machine switcher HANDLE (gomobile has no bound list type).
type MachineList struct {
	items []MachineInfo
	cap   int
}

// Count is the number of paired machines.
func (l *MachineList) Count() (n int, err error) {
	defer barrier(&err)
	if l == nil {
		return 0, errNoReceiver
	}
	return len(l.items), nil
}

// At returns the row at index i.
func (l *MachineList) At(i int) (m *MachineInfo, err error) {
	defer barrier(&err)
	if l == nil {
		return nil, errNoReceiver
	}
	if i < 0 || i >= len(l.items) {
		return nil, classed(ErrClassNotFound,
			fmt.Errorf("swarmmobile: machine index %d out of range [0,%d)", i, len(l.items)))
	}
	item := l.items[i]
	return &item, nil
}

// Cap is the documented foreground connection cap the rows were arbitrated under. It
// rides the handle so the switcher can render the limitation honestly (ADR-018: "the
// cap is documented", never hidden).
func (l *MachineList) Cap() (n int, err error) {
	defer barrier(&err)
	if l == nil {
		return 0, errNoReceiver
	}
	return l.cap, nil
}

// InboxItem is one global-inbox row, keyed by the TUPLE (machine id, session id): two
// machines may serve the same session id and the same title without colliding (MM4).
type InboxItem struct {
	MachineID   string
	MachineName string
	SessionID   string
	Title       string
	Group       string
	NeedsInput  bool
}

// InboxList is the global inbox HANDLE: one aggregate list across every pairing.
type InboxList struct {
	items []InboxItem
}

// Count is the number of inbox rows.
func (l *InboxList) Count() (n int, err error) {
	defer barrier(&err)
	if l == nil {
		return 0, errNoReceiver
	}
	return len(l.items), nil
}

// At returns the inbox row at index i.
func (l *InboxList) At(i int) (item *InboxItem, err error) {
	defer barrier(&err)
	if l == nil {
		return nil, errNoReceiver
	}
	if i < 0 || i >= len(l.items) {
		return nil, classed(ErrClassNotFound,
			fmt.Errorf("swarmmobile: inbox index %d out of range [0,%d)", i, len(l.items)))
	}
	row := l.items[i]
	return &row, nil
}

// brokenPairing is one registered machine whose namespace failed to resume: the row
// stays on the aggregate surface, broken and forgettable, so one pairing's fault never
// degrades another (MM8).
type brokenPairing struct {
	displayName string
	reason      string
}

// machinesRuntime is the App's live manager view. Guarded by App.mu.
type machinesRuntime struct {
	mgr  phonecore.MachineManager
	rmgr *phonecore.RegistryManager // non-nil when registry-backed
	reg  *phonecore.MachineRegistry // non-nil when registry-backed
	// cores are the resumed per-machine cores, keyed by machine id. The App's own core
	// is NEVER resumed a second time here -- two live send sequencers for one pairing
	// re-issue seqs the gateway stale-drops forever (MM6 step 5).
	cores map[string]*phonecore.Core
	// broken are the pairings whose namespace refused to resume, keyed by machine id.
	// They are registry rows without a client: rendered broken, selectable only into a
	// named refusal, and still forgettable.
	broken map[string]brokenPairing
}

// ensureMachinesLocked builds (or returns) the manager view. Caller holds a.mu.
func (a *App) ensureMachinesLocked() (*machinesRuntime, error) {
	if a.machines != nil {
		return a.machines, nil
	}
	reg, err := phonecore.OpenMachineRegistry(a.stateDir)
	if err != nil {
		return nil, err
	}
	rt, err := a.registryRuntimeLocked(reg)
	if err != nil {
		return nil, err
	}
	a.machines = rt
	return a.machines, nil
}

// registryRuntimeLocked assembles the N-entry manager over a live registry: one client
// per namespace, the App's own core reused for the pairing it already holds. Caller
// holds a.mu.
func (a *App) registryRuntimeLocked(reg *phonecore.MachineRegistry) (*machinesRuntime, error) {
	rmgr, err := phonecore.NewRegistryManager(reg, phonecore.ManagerOptions{
		Cap: foregroundConnectionCap,
		Now: time.Now,
	})
	if err != nil {
		return nil, err
	}
	rt := &machinesRuntime{
		mgr: rmgr, rmgr: rmgr, reg: reg,
		cores:  map[string]*phonecore.Core{},
		broken: map[string]brokenPairing{},
	}
	for _, d := range reg.Entries() {
		core := a.core
		if dir := reg.MachineDir(d.ID); dir != a.coreDir {
			if core, err = a.resumeNamespace(dir, d.ID); err != nil {
				// ONE pairing's namespace refusing to resume must not abort the whole
				// runtime (MM8): the fault is recorded against ITS row and every other
				// pairing keeps its client. Rendered broken by Machines, refused by name
				// in SelectMachine, removable by ForgetMachine.
				rt.broken[d.ID] = brokenPairing{displayName: d.DisplayName, reason: err.Error()}
				continue
			}
		}
		rt.cores[d.ID] = core
		if err := rmgr.Add(d, phonecore.NewCoreMachineClient(d.ID, core, nil)); err != nil {
			_ = rmgr.Close()
			return nil, err
		}
		// The durable last-heard instant is the pairing's last successful sync fact; the
		// row's visible age is computed from it (playbook 4.2:200-202).
		if heard := core.State().LastHeardAt; heard != 0 {
			rmgr.RecordSync(d.ID, time.UnixMilli(heard))
		}
	}
	go drainAggregate(rmgr.Events())
	return rt, nil
}

// drainAggregate consumes the manager's machine-qualified aggregate stream (MM3) until
// the manager closes. Today's production clients feed no event channel of their own --
// the live event plane still rides the App's single relay session -- so this drain
// terminates on close without observing anything; it exists so the aggregate stream has
// its consumer the moment a per-machine connection loop starts publishing.
func drainAggregate(events <-chan phonecore.MachineEvent) {
	for range events { //nolint:revive // deliberately empty: see doc comment
	}
}

// resumeNamespace resumes one registry namespace under this App's custody, with no
// relay acker: a namespace core that is not the App's own drives no relay mailbox here.
func (a *App) resumeNamespace(dir, machineID string) (*phonecore.Core, error) {
	return phonecore.Resume(phonecore.Config{
		Dir:           dir,
		Machine:       machineID,
		WakeSealer:    a.wakeSealer,
		ContentSealer: a.contentSealer,
	})
}

// resumeOwnNamespace resumes the App's OWN pairing from its registry namespace,
// carrying the relay acker exactly as NewApp's singleton resume does, and re-keys
// coreDir so the machines surface never resumes this namespace a second time.
func (a *App) resumeOwnNamespace(dir, machineID string) (*phonecore.Core, error) {
	core, err := phonecore.Resume(phonecore.Config{
		Dir:           dir,
		Machine:       machineID,
		Ack:           &relayAcker{app: a},
		WakeSealer:    a.wakeSealer,
		ContentSealer: a.contentSealer,
	})
	if err != nil {
		return nil, err
	}
	a.coreDir = dir
	return core, nil
}

// resumeRegistryCore creates a first-run v2 registry or resumes a registered namespace.
// It never resumes StateDir itself: a root phone-state.json is legacy state and is refused
// by OpenMachineRegistry/NewMachineRegistry rather than silently imported.
func (a *App) resumeRegistryCore(cfg *Config) (*phonecore.Core, error) {
	reg, err := phonecore.OpenMachineRegistry(cfg.StateDir)
	if errors.Is(err, phonecore.ErrRegistryNotLive) {
		reg, err = phonecore.NewMachineRegistry(cfg.StateDir)
	}
	if err != nil {
		return nil, err
	}
	entries := reg.Entries()
	if len(entries) == 0 {
		dir, err := reg.EnsureBootstrap()
		if err != nil {
			return nil, err
		}
		a.bootstrap = reg
		return a.resumeOwnNamespace(dir, "")
	}
	id := cfg.MachineID
	if id == "" {
		if len(entries) != 1 {
			return nil, classed(ErrClassInvalidRequest, fmt.Errorf(
				"swarmmobile: %d machines are registered under %s; Config.MachineID must name one",
				len(entries), cfg.StateDir))
		}
		id = entries[0].ID
	}
	found := false
	for _, e := range entries {
		if e.ID == id {
			found = true
			break
		}
	}
	if !found {
		return nil, classed(ErrClassNotFound,
			fmt.Errorf("swarmmobile: machine %q is not in the registry", id))
	}
	if len(entries) == 1 && reg.IsBootstrapMachine(id) {
		attempt, err := a.readPairingState()
		if err != nil {
			return nil, err
		}
		if attempt == bootstrapCommittedPendingACK {
			// The registry rename is local authority only. Until RunDevice records
			// successful ACK completion, retain the bootstrap gate across restarts.
			a.bootstrap = reg
		}
	}
	return a.resumeOwnNamespace(reg.MachineDir(id), id)
}

// Machines returns the machine-switcher snapshot: every paired machine, its
// reachability, last successful sync and needs-input count, arbitrated under the
// documented connection cap.
func (a *App) Machines() (list *MachineList, err error) {
	defer barrier(&err)
	if _, err = a.ready(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	rt, err := a.ensureMachinesLocked()
	if err != nil {
		return nil, err
	}
	out := &MachineList{cap: rt.mgr.ConnectionCap()}
	for _, row := range rt.rmgr.Rows() {
		out.items = append(out.items, MachineInfo{
			ID:             row.ID,
			DisplayName:    row.DisplayName,
			Connected:      row.Connected,
			Stale:          row.Stale,
			LastSyncUnixMs: row.LastSyncUnixMs,
			NeedsInput:     needsInputCount(rt.cores[row.ID]),
		})
	}
	// The pairings whose namespace refused to resume are ROWS, not holes: the
	// aggregate surface says WHICH row is broken (machines.recovery, MM8).
	for id, b := range rt.broken {
		out.items = append(out.items, MachineInfo{
			ID:           id,
			DisplayName:  b.displayName,
			Stale:        true,
			Broken:       true,
			BrokenReason: b.reason,
		})
	}
	sort.Slice(out.items, func(i, j int) bool { return out.items[i].ID < out.items[j].ID })
	return out, nil
}

// needsInputCount counts the sessions of one pairing's durable model that need input.
func needsInputCount(core *phonecore.Core) int {
	if core == nil {
		return 0
	}
	n := 0
	for _, cs := range core.State().Sessions {
		if cs.Group == status.GroupNeedsInput {
			n++
		}
	}
	return n
}

// SelectMachine records that the user switched to machine id. The view is the ONLY
// input the deterministic least-recently-viewed connection policy takes (MM3).
func (a *App) SelectMachine(machineID string) (err error) {
	defer barrier(&err)
	if _, err = a.ready(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	rt, err := a.ensureMachinesLocked()
	if err != nil {
		return err
	}
	if b, ok := rt.broken[machineID]; ok {
		// The broken pairing's OWN fault, named -- never a not-found that pretends the
		// registered row is absent, and never a wholesale failure (MM8).
		return classed(ErrClassStateCorrupt, fmt.Errorf(
			"swarmmobile: machine %q's durable state failed to resume: %s; forget this "+
				"computer or pair it again -- other computers are unaffected", machineID, b.reason))
	}
	if _, err := rt.mgr.Select(machineID); err != nil {
		return classed(ErrClassNotFound, err)
	}
	rt.rmgr.MarkViewed(machineID)
	return nil
}

// AddMachine registers a NEW pairing beside the existing ones ("The new computer
// appears without replacing existing pairings", playbook 4.1). On a phone still holding
// singleton state this is what runs MM6's transactional migration first; the App's own
// core then resumes from its per-machine namespace and the old blob stays
// rollback-readable. The new machine's namespace is created awaiting its pairing
// ceremony. Refused while the app is running: the migration must not race a live drain.
func (a *App) AddMachine(machineID, displayName string) (err error) {
	defer barrier(&err)
	if _, err = a.ready(); err != nil {
		return err
	}
	if machineID == "" {
		return classed(ErrClassInvalidRequest, errors.New("swarmmobile: AddMachine requires a machine id"))
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sess != nil {
		return classed(ErrClassInvalidRequest,
			errors.New("swarmmobile: stop the app before adding a computer; the state migration must not race a live connection"))
	}
	rt, err := a.ensureMachinesLocked()
	if err != nil {
		return err
	}
	d := phonecore.MachineDescriptor{ID: machineID, DisplayName: displayName}
	if a.bootstrap != nil {
		return classed(ErrClassNotPaired,
			errors.New("swarmmobile: pair the first computer before adding another"))
	}
	dir, err := rt.reg.AddMachine(d)
	if err != nil {
		return err
	}
	core, err := a.resumeNamespace(dir, machineID)
	if err != nil {
		return err
	}
	if err := rt.rmgr.Add(d, phonecore.NewCoreMachineClient(machineID, core, nil)); err != nil {
		return err
	}
	rt.cores[machineID] = core
	return nil
}

// migrateToRegistryLocked runs MM6's transactional migration and rebuilds the manager
// registry-backed, re-resuming the App's own core from its namespace. Caller holds
// a.mu, with no live session.
//
//nolint:unused // Compatibility removal is paused pending explicit approval.
func (a *App) migrateToRegistryLocked(old *machinesRuntime) (*machinesRuntime, error) {
	return nil, classed(ErrClassInvalidRequest,
		errors.New("swarmmobile: legacy state migration was removed; reset and pair again"))
}

// commitBootstrapPairing flips the fresh staging namespace into the authenticated
// machine's registry entry. pinWithStagedPushBinding calls it only after the pairing
// facts are durably sealed and before the wire acknowledgement, so an error cannot
// enrol the machine without a durable v2 authority record.
func (a *App) commitBootstrapPairing() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.bootstrap == nil {
		return nil
	}
	st := a.core.State()
	if st.Machine == "" {
		return classed(ErrClassPairingFailed,
			errors.New("swarmmobile: pairing did not authenticate a machine id"))
	}
	d := phonecore.MachineDescriptor{ID: st.Machine, DisplayName: st.MachineName}
	// This record must precede the registry authority flip: after the flip, a crash
	// before or during ACK cannot be distinguished and must restart fail-closed.
	if err := a.writePairingState(bootstrapCommittedPendingACK); err != nil {
		return err
	}
	err := commitBootstrapAuthority(a.bootstrap, d)
	if err != nil {
		return err
	}
	a.coreDir = a.bootstrap.MachineDir(st.Machine)
	if a.machines != nil {
		_ = a.machines.mgr.Close()
		a.machines = nil
	}
	// Keep bootstrap armed until Pairing.finish has observed a successful ACK and
	// durably cleared the recovery record. It gates this process as well as restart.
	return nil
}

func (a *App) completeBootstrapPairing() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.bootstrap = nil
}

func (a *App) preserveBootstrapCompletion() {
	a.mu.Lock()
	pending := a.bootstrap != nil
	a.mu.Unlock()
	if pending {
		// A failed delete may have removed the directory entry before its fsync
		// failed. Put the conservative marker back; the reported error remains.
		_ = a.writePairingState(bootstrapCommittedPendingACK)
	}
}

// ForgetMachine is the PHONE-side removal of one pairing (playbook 4.9): that machine's
// registry row, namespace, keys and caches are deleted, and no other pairing is read,
// rewritten or invalidated (MM7). It is DISTINCT from revoking a phone from a computer,
// and the computer still authorizes the old device id until revoked there.
func (a *App) ForgetMachine(machineID string) (err error) {
	defer barrier(&err)
	if _, err = a.ready(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	rt, err := a.ensureMachinesLocked()
	if err != nil {
		return err
	}
	if rt.cores[machineID] == a.core {
		return classed(ErrClassInvalidRequest,
			errors.New("swarmmobile: switch to another computer before forgetting the active one"))
	}
	if _, ok := rt.broken[machineID]; ok {
		// A broken pairing has no managed client to stop -- its forget is the registry
		// row and namespace directly. This is the row's recovery affordance: without it
		// the only remedy left is clearing the app's data, which destroys every pairing.
		if err := rt.reg.RemoveMachine(machineID); err != nil {
			return classed(ErrClassNotFound, err)
		}
		delete(rt.broken, machineID)
		return nil
	}
	if err := rt.mgr.Remove(machineID); err != nil {
		return classed(ErrClassNotFound, err)
	}
	delete(rt.cores, machineID)
	return nil
}

// GlobalInbox is the aggregate inbox: one list across every pairing, every row keyed by
// the tuple (machine_id, session_id), so two machines serving the same session id are
// two rows, never one (MM4, R4 exit).
func (a *App) GlobalInbox() (list *InboxList, err error) {
	defer barrier(&err)
	if _, err = a.ready(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	rt, err := a.ensureMachinesLocked()
	if err != nil {
		return nil, err
	}
	out := &InboxList{}
	for _, d := range rt.mgr.List() {
		client, err := rt.mgr.Select(d.ID)
		if err != nil {
			continue
		}
		core := client.Core()
		if core == nil {
			core = rt.cores[d.ID]
		}
		if core == nil {
			continue
		}
		for _, cs := range core.State().Sessions {
			title := cs.Name
			if title == "" {
				title = cs.SessionID
			}
			out.items = append(out.items, InboxItem{
				MachineID:   d.ID,
				MachineName: d.DisplayName,
				SessionID:   cs.SessionID,
				Title:       title,
				Group:       string(cs.Group),
				NeedsInput:  cs.Group == status.GroupNeedsInput,
			})
		}
	}
	sort.Slice(out.items, func(i, j int) bool {
		if out.items[i].MachineID != out.items[j].MachineID {
			return out.items[i].MachineID < out.items[j].MachineID
		}
		return out.items[i].SessionID < out.items[j].SessionID
	})
	return out, nil
}
