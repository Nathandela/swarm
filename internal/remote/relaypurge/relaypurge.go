// Package relaypurge is the durable half of ADR-007 D9's deferred relay purge: "an
// offline-at-revoke machine defers the purge to reconnect". `swarm remote revoke`
// records an obligation here BEFORE the destructive local revoke (bead
// agents-tracker-dtc5, the 2026-08-21 incident; a crash between the local delete and a
// later record must lose nothing -- the discipline internal/remotegw's u37c producer
// states and this package mirrors), and the machine's later relay dials drive it.
//
// Two guards ride with the obligation, both taught by review:
//
//   - the routing id is per-install, so the SAME handset re-paired returns on the id a
//     stale obligation names; a driver must retire such an obligation WITHOUT purging
//     (u37c round 3's defect class) -- DriveMachineObligations' per-obligation
//     registry check;
//   - the obligation names the RELAY it is owed against: after `swarm remote init
//     --relay-url` re-points the machine, a purge "landing" at the new relay would be a
//     lie -- the old relay's mailbox and route survive -- so a driver must compare
//     Obligation.RelayURL before claiming anything.
//
// Every mutation re-reads the file under an exclusive flock and writes back through a
// synced temp file + rename (+ directory sync), so two processes -- `swarm remote
// revoke` racing `swarm remote pair`, or two CLI invocations -- can never erase each
// other's obligations, and "recorded durably" holds across power loss, not merely
// across process death.
package relaypurge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Obligation is one deferred relay purge: the routing id the revoke could not
// de-authorize, the relay URL it is owed against, and when the deferral was recorded.
type Obligation struct {
	RoutingID string `json:"routing_id"`
	RelayURL  string `json:"owed_relay_url"`
	// MachineRID is the relay routing id of the machine identity that owes the purge
	// (round-3 codex #2): only that identity can present it. A drive under a
	// DIFFERENT identity (machine.key lost and regenerated) must not let the new
	// identity's ErrNotAuthorized read as "settled" while the old identity's pairing
	// survives at the relay -- it retires loudly as a manual task instead.
	MachineRID string    `json:"owed_machine_rid,omitempty"`
	RecordedAt time.Time `json:"recorded_at"`
	// Resolved marks a TERMINAL outcome that did not land the purge -- a substantive
	// refusal from a reachable relay -- with the reason preserved. A resolved
	// obligation is a tombstone: excluded from Pending (so it cannot brick `swarm
	// remote pair`, round-2 Opus R2-1) but kept on file with its reason (so the
	// unlanded purge stays on the record, round-2 codex #3; u37c's Done+Refusal
	// shape). Pruned after resolvedRetention.
	Resolved bool   `json:"resolved,omitempty"`
	Refusal  string `json:"refusal,omitempty"`
	// ResolvedAt is when the refusal resolved it -- the retention clock. Aging from
	// RecordedAt pruned a tombstone the day it was created whenever the obligation
	// had sat pending longer than the retention (round-3 review F2).
	ResolvedAt time.Time `json:"resolved_at,omitempty"`
}

// resolvedRetention bounds tombstone growth; the operator line at resolution time is
// the primary record, the tombstone is forensics.
const resolvedRetention = 90 * 24 * time.Hour

// Store is a handle on the obligation file. It caches nothing: every operation
// re-reads under the lock, so a Store is safe to hold across other processes' writes.
type Store struct {
	path string
}

// Open validates the store at path and returns a handle. A missing file is an empty
// store, not an error: the common machine has never abandoned a purge.
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	if _, err := s.read(); err != nil {
		return nil, err
	}
	return s, nil
}

// Record durably adds an obligation. Recording a routing id that is already pending
// keeps ONE record (a re-run revoke owes one purge, not two) but adopts the NEW relay
// URL: the newest owed relay is the one this machine can still land a purge at, and
// keeping the older one let a stale obligation be retired by the mismatch ruling while
// the purge genuinely owed at the current relay vanished (round-2 review, Fable
// defect 4).
func (s *Store) Record(routingID, relayURL, machineRID string) error {
	if routingID == "" {
		return errors.New("relaypurge: refusing to record an empty routing id")
	}
	return s.mutate(func(obs []Obligation) ([]Obligation, bool) {
		for i, ob := range obs {
			if ob.RoutingID != routingID || ob.Resolved {
				continue
			}
			if ob.RelayURL == relayURL && ob.MachineRID == machineRID {
				return obs, false
			}
			// The newest owed relay AND identity are the ones a purge can still land
			// under (round-2 Fable 4; round-3 codex #2).
			obs[i].RelayURL = relayURL
			obs[i].MachineRID = machineRID
			return obs, true
		}
		return append(obs, Obligation{
			RoutingID: routingID, RelayURL: relayURL, MachineRID: machineRID,
			RecordedAt: time.Now(),
		}), true
	})
}

// Pending returns the obligations still owed, in recording order. Resolved
// tombstones are not owed and are excluded.
func (s *Store) Pending() ([]Obligation, error) {
	unlock, err := s.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	obs, err := s.read()
	if err != nil {
		return nil, err
	}
	owed := obs[:0]
	for _, ob := range obs {
		if !ob.Resolved {
			owed = append(owed, ob)
		}
	}
	return owed, nil
}

// Resolve marks the obligation for routingID as terminally refused, preserving the
// reason as a tombstone. It stays on file (excluded from Pending) for
// resolvedRetention, then prunes.
func (s *Store) Resolve(routingID, reason string) error {
	return s.mutate(func(obs []Obligation) ([]Obligation, bool) {
		for i, ob := range obs {
			if ob.RoutingID == routingID && !ob.Resolved {
				obs[i].Resolved = true
				obs[i].Refusal = reason
				obs[i].ResolvedAt = time.Now()
				return obs, true
			}
		}
		return obs, false
	})
}

// Resolved returns the tombstones on file: obligations terminally refused, with
// their reasons -- the forensics record Resolve keeps.
func (s *Store) Resolved() ([]Obligation, error) {
	unlock, err := s.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	obs, err := s.read()
	if err != nil {
		return nil, err
	}
	var out []Obligation
	for _, ob := range obs {
		// Display honors the retention even before a mutation prunes the disk.
		if ob.Resolved && time.Since(resolvedClock(ob)) <= resolvedRetention {
			out = append(out, ob)
		}
	}
	return out, nil
}

// resolvedClock is the tombstone's retention clock: resolution time, with the
// recording time as the fallback for tombstones written before ResolvedAt existed.
func resolvedClock(ob Obligation) time.Time {
	if !ob.ResolvedAt.IsZero() {
		return ob.ResolvedAt
	}
	return ob.RecordedAt
}

// pruneResolved drops tombstones older than resolvedRetention.
func pruneResolved(obs []Obligation) ([]Obligation, bool) {
	kept := obs[:0]
	changed := false
	for _, ob := range obs {
		if ob.Resolved && time.Since(resolvedClock(ob)) > resolvedRetention {
			changed = true
			continue
		}
		kept = append(kept, ob)
	}
	return kept, changed
}

// Retire durably removes the PENDING obligation for routingID, if any. A resolved
// tombstone is deliberately not touched: Drive retires on a nil act, and an act that
// just Resolved returns nil -- removing the tombstone it created would erase the
// record Resolve exists to keep.
func (s *Store) Retire(routingID string) error {
	return s.mutate(func(obs []Obligation) ([]Obligation, bool) {
		kept := obs[:0]
		for _, ob := range obs {
			if ob.RoutingID != routingID || ob.Resolved {
				kept = append(kept, ob)
			}
		}
		return kept, len(kept) != len(obs)
	})
}

// lock takes the interprocess mutation lock: a sidecar .lock file, because the data
// file itself is replaced by rename and a lock on a replaced inode guards nothing.
func (s *Store) lock() (func(), error) {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return nil, fmt.Errorf("relaypurge: create state dir: %w", err)
	}
	lf, err := os.OpenFile(s.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("relaypurge: open lock: %w", err)
	}
	// Non-blocking with a bounded wait (the tree's flock precedent,
	// internal/daemon/singleton.go): hold times are milliseconds, so a holder that
	// outlives this wait is stuck, and hanging the CLI silently on it helps nobody.
	locked := false
	for deadline := time.Now().Add(5 * time.Second); ; {
		if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			locked = true
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !locked {
		_ = lf.Close()
		return nil, errors.New("relaypurge: the obligation store is locked by another swarm process")
	}
	return func() {
		_ = syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
		_ = lf.Close()
	}, nil
}

func (s *Store) read() ([]Obligation, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("relaypurge: read %s: %w", s.path, err)
	}
	var obs []Obligation
	if err := json.Unmarshal(data, &obs); err != nil {
		return nil, fmt.Errorf("relaypurge: parse %s: %w", s.path, err)
	}
	return obs, nil
}

// mutate applies fn to a FRESH read of the list under the exclusive lock and persists
// the result, so a concurrent process's record or retire is merged, never erased.
func (s *Store) mutate(fn func([]Obligation) ([]Obligation, bool)) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	obs, err := s.read()
	if err != nil {
		return err
	}
	// Every mutation prunes expired tombstones (round-3 codex #6: prune-on-Record
	// alone left a tombstone on disk forever when no later revoke happened).
	obs, pruned := pruneResolved(obs)
	next, changed := fn(obs)
	if !changed && !pruned {
		return nil
	}
	return s.persist(next)
}

// persist writes through a synced temp file renamed into place, then syncs the
// directory -- the remotegw writeFileAtomic discipline: without the syncs, "recorded
// durably" is a claim about process death only, and the machine that records an
// obligation is by definition one having a bad day.
func (s *Store) persist(obs []Obligation) error {
	data, err := json.Marshal(obs)
	if err != nil {
		return fmt.Errorf("relaypurge: marshal: %w", err)
	}
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".relay-purge-*")
	if err != nil {
		return fmt.Errorf("relaypurge: temp file: %w", err)
	}
	_, werr := tmp.Write(data)
	if werr == nil {
		werr = tmp.Sync()
	}
	if werr != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("relaypurge: write: %w", werr)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("relaypurge: close: %w", err)
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("relaypurge: rename: %w", err)
	}
	// The directory sync is part of the durability claim, not decoration: a failure
	// here must surface, or "recorded durably" is printed over an entry a power loss
	// can still lose (round-2 codex #6). The file itself is already renamed, so a
	// caller that retries on this error re-runs an idempotent mutation.
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("relaypurge: open dir for sync: %w", err)
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return fmt.Errorf("relaypurge: sync dir: %w", err)
	}
	if err := d.Close(); err != nil {
		return fmt.Errorf("relaypurge: close dir: %w", err)
	}
	return nil
}

// Drive presents every pending obligation to act, which owns every ruling (live
// guard, provisioning, mismatch, the dial): a nil return retires the obligation
// durably, an error keeps it. Every obligation is attempted -- a store failure on one
// retire is reported but does not starve the rest -- and the first error of any kind
// is returned after the loop.
func Drive(ctx context.Context, s *Store, act func(context.Context, Obligation) error) error {
	pending, err := s.Pending()
	if err != nil {
		return err
	}
	var firstErr error
	keep := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}
	for _, ob := range pending {
		if err := act(ctx, ob); err != nil {
			keep(err)
			continue
		}
		if err := s.Retire(ob.RoutingID); err != nil {
			keep(err)
		}
	}
	return firstErr
}
