package skeleton

// Direct-input uncertainty is the durable bridge between terminal-shaped input
// and auth recycling. SessionStream.Input cannot report whether an errored write
// consumed bytes, and plain text may remain indefinitely in the provider's local
// editor without changing Core status. Protocol therefore records Draft at its
// last pre-write boundary. Only an explicit Enter/Submit whose Input returned
// success advances to Submitted; even then, a later changed authoritative status
// tuple is required before the marker is cleared.

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

const (
	directInputUnresolvedFile = "direct-input-unresolved.json"
	directInputStateVersion   = 1
)

type directInputRecord struct {
	Version             int                       `json:"version"`
	Class               protocol.DirectInputClass `json:"class"`
	BaselineTurn        status.Turn               `json:"baseline_turn"`
	BaselineInteraction status.Interaction        `json:"baseline_interaction"`
}

type directInputState struct {
	mu         sync.Mutex
	dir        string
	unresolved map[string]directInputRecord
	// pendingDurability means rename committed in this process but its parent
	// directory Sync failed. Memory remains fail-closed, and a later input must
	// rewrite+sync before the observer permits bytes.
	pendingDurability map[string]bool
	write             func(path string, record directInputRecord) (committed bool, err error)
}

func (d *Daemon) directInputPath(local string) string {
	if d.directInput.dir == "" || local == "" || filepath.Base(local) != local {
		return ""
	}
	return filepath.Join(d.directInput.dir, local, directInputUnresolvedFile)
}

// restoreDirectInputState runs before either protocol server can serve input.
// A malformed/unreadable existing marker is a permanent draft, the fail-closed
// interpretation: corrupt state must not authorize an unattended recycle.
func (d *Daemon) restoreDirectInputState(locals []string) {
	d.directInput.mu.Lock()
	defer d.directInput.mu.Unlock()
	d.directInput.dir = d.stateDir
	d.directInput.unresolved = make(map[string]directInputRecord)
	d.directInput.pendingDurability = make(map[string]bool)
	for _, local := range locals {
		path := d.directInputPath(local)
		if path == "" {
			continue
		}
		raw, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		record := directInputRecord{Version: directInputStateVersion, Class: protocol.DirectInputDraft}
		if err == nil {
			var decoded directInputRecord
			if json.Unmarshal(raw, &decoded) == nil && validDirectInputRecord(decoded) {
				record = decoded
			}
		}
		d.directInput.unresolved[local] = record
	}
}

func validDirectInputRecord(record directInputRecord) bool {
	if record.Version != directInputStateVersion {
		return false
	}
	if record.Class != protocol.DirectInputDraft && record.Class != protocol.DirectInputSubmitted {
		return false
	}
	return validDirectInputTurn(record.BaselineTurn) && validDirectInputInteraction(record.BaselineInteraction)
}

func validDirectInputTurn(turn status.Turn) bool {
	return turn == status.TurnActive || turn == status.TurnIdle || turn == status.TurnUnknown
}

func validDirectInputInteraction(interaction status.Interaction) bool {
	return interaction == status.InteractionNone || interaction == status.InteractionPrompt ||
		interaction == status.InteractionPermission || interaction == status.InteractionUnknown
}

// ObserveDirectInput makes coreAPI the optional protocol.DirectInputObserver.
// A bare coreAPI used by small tests has no production callback and remains a
// no-op; Serve wires the durable callback before constructing either Server.
func (a *coreAPI) ObserveDirectInput(local string, class protocol.DirectInputClass) error {
	if a.directInputObserver == nil {
		return nil
	}
	return a.directInputObserver(local, class)
}

var _ protocol.DirectInputObserver = (*coreAPI)(nil)

// markDirectInputUnresolved is the production observer. The exact current
// folded tuple is captured before persistence so an older status observation
// cannot clear a newer input marker.
func (d *Daemon) markDirectInputUnresolved(local string, class protocol.DirectInputClass) error {
	if d.core == nil {
		return fmt.Errorf("skeleton: direct input for %q has no lifecycle authority", local)
	}
	m, ok := d.core.Get(local)
	if !ok || m.Status.Process != status.ProcessRunning {
		return fmt.Errorf("skeleton: direct input session %q is not running", local)
	}
	return d.markDirectInputUnresolvedAt(local, class, m.Status)
}

func (d *Daemon) markDirectInputUnresolvedAt(local string, class protocol.DirectInputClass, baseline status.Status) error {
	if class != protocol.DirectInputDraft && class != protocol.DirectInputSubmitted {
		return fmt.Errorf("skeleton: invalid direct-input class %q", class)
	}
	record := directInputRecord{
		Version: directInputStateVersion, Class: class,
		BaselineTurn: baseline.Turn, BaselineInteraction: baseline.Interaction,
	}
	if !validDirectInputRecord(record) {
		return fmt.Errorf("skeleton: invalid direct-input baseline %q/%q", baseline.Turn, baseline.Interaction)
	}

	d.directInput.mu.Lock()
	defer d.directInput.mu.Unlock()
	if d.directInput.unresolved == nil {
		d.directInput.unresolved = make(map[string]directInputRecord)
	}
	if d.directInput.pendingDurability == nil {
		d.directInput.pendingDurability = make(map[string]bool)
	}
	if current, ok := d.directInput.unresolved[local]; ok && current == record &&
		!d.directInput.pendingDurability[local] {
		return nil
	}
	// More printable bytes cannot make an already-durable draft safer and need
	// no extra fsync. Every other transition (draft->submit, submitted->new
	// draft, or a newer submitted baseline) is persisted before memory changes.
	if current, ok := d.directInput.unresolved[local]; ok &&
		current.Class == protocol.DirectInputDraft && class == protocol.DirectInputDraft &&
		!d.directInput.pendingDurability[local] {
		return nil
	}
	if path := d.directInputPath(local); path != "" {
		write := d.directInput.write
		if write == nil {
			write = writeDirectInputRecord
		}
		committed, err := write(path, record)
		if committed {
			// Rename is visible now. Publish the marker even when the following
			// directory Sync failed: allowing auth to observe false after the
			// input fence releases would be the dangerous interpretation.
			d.directInput.unresolved[local] = record
			d.directInput.pendingDurability[local] = err != nil
		}
		if err != nil {
			return err
		}
		delete(d.directInput.pendingDurability, local)
	}
	d.directInput.unresolved[local] = record
	return nil
}

// writeDirectInputRecord distinguishes failure before rename (nothing committed)
// from failure after rename (the marker is visible but directory durability is
// unconfirmed). The caller needs that distinction to remain fail-closed in memory.
func writeDirectInputRecord(path string, record directInputRecord) (committed bool, err error) {
	data, err := json.Marshal(record)
	if err != nil {
		return false, err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp*")
	if err != nil {
		return false, err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return false, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return false, err
	}
	dirHandle, err := os.Open(dir)
	if err != nil {
		return true, err
	}
	defer func() { _ = dirHandle.Close() }()
	if err := dirHandle.Sync(); err != nil {
		return true, err
	}
	return true, nil
}

// directInputUnresolved is intentionally no-create: an auth safety read for an
// unknown/retired session neither creates a composer lane nor authors state.
func (d *Daemon) directInputUnresolved(local string) bool {
	d.directInput.mu.Lock()
	defer d.directInput.mu.Unlock()
	_, ok := d.directInput.unresolved[local]
	return ok
}

// noteDirectInputStatus is called only after coreAPI successfully folds a
// provider status. Drafts never clear here. Submitted attempts clear on a later
// known tuple change; unknown is staleness/absence, not proof of progress.
func (d *Daemon) noteDirectInputStatus(local string, current status.Status) {
	if current.Process != status.ProcessRunning || current.Turn == status.TurnUnknown ||
		current.Interaction == status.InteractionUnknown {
		return
	}
	d.directInput.mu.Lock()
	defer d.directInput.mu.Unlock()
	record, ok := d.directInput.unresolved[local]
	if !ok || record.Class != protocol.DirectInputSubmitted ||
		record.BaselineTurn == current.Turn && record.BaselineInteraction == current.Interaction {
		return
	}
	if err := d.removeDirectInputFileLocked(local); err != nil {
		log.Printf("skeleton: retain direct-input uncertainty for %s after provider progress: %v", local, err)
		return
	}
	delete(d.directInput.unresolved, local)
	delete(d.directInput.pendingDurability, local)
}

// forgetDirectInput retires both memory and disk state. It is called before a
// session directory is deleted; an error retains the memory gate fail-closed.
func (d *Daemon) forgetDirectInput(local string) error {
	d.directInput.mu.Lock()
	defer d.directInput.mu.Unlock()
	if _, ok := d.directInput.unresolved[local]; !ok {
		return nil
	}
	if err := d.removeDirectInputFileLocked(local); err != nil {
		return err
	}
	delete(d.directInput.unresolved, local)
	delete(d.directInput.pendingDurability, local)
	return nil
}

func (d *Daemon) removeDirectInputFileLocked(local string) error {
	path := d.directInputPath(local)
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}
