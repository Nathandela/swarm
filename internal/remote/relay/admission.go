package relay

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

// storageAdmissionReason is deliberately a closed, low-cardinality vocabulary: it is
// exported only through aggregate metrics and never contains a route or source identity.
type storageAdmissionReason string

const (
	admissionDurableObjects storageAdmissionReason = "durable_objects"
	admissionGrowthWrites   storageAdmissionReason = "growth_writes"
	admissionDBBytes        storageAdmissionReason = "db_bytes"
	admissionFreeDisk       storageAdmissionReason = "free_disk"
)

type storageAdmissionError struct{ reason storageAdmissionReason }

func (e *storageAdmissionError) Error() string {
	return fmt.Sprintf("relay: durable growth refused by %s admission fence", e.reason)
}

func isStorageAdmissionError(err error) bool {
	var admission *storageAdmissionError
	return errors.As(err, &admission)
}

// storeMutation is the exact durable-object delta produced by one bbolt transaction and
// the aggregate cleanup it performed. Callers calculate it while the affected keys are in
// the same writable snapshot, so the global count cannot race a distributed writer.
type storeMutation struct {
	durableDelta     int64
	growthBytes      int64
	cleanupItems     uint64
	cleanupMailboxes uint64
}

type storeAdmissionState struct {
	mu sync.Mutex

	dbPath     string
	clock      Clock
	diskFreeFn func() (uint64, error)
	limits     Quotas

	durableObjects int64
	windowStart    time.Time
	windowSet      bool
	windowWrites   int
	growthWrites   uint64
	cleanupItems   uint64
	cleanupBoxes   uint64
	refusals       map[storageAdmissionReason]uint64
}

func newStoreAdmissionState(dbPath string) *storeAdmissionState {
	return &storeAdmissionState{
		dbPath:     dbPath,
		clock:      realClock{},
		diskFreeFn: defaultDiskFreeFn(dbPath),
		refusals:   make(map[storageAdmissionReason]uint64),
	}
}

// configureAdmission installs the already-option-resolved server dependencies before any
// request can reach the store. openStore itself keeps zero limits for white-box tests and
// backup validation callers that open a store without constructing a Server.
func (s *store) configureAdmission(limits Quotas, clock Clock, diskFreeFn func() (uint64, error)) {
	a := s.admission
	a.mu.Lock()
	defer a.mu.Unlock()
	a.limits = limits
	if clock != nil {
		a.clock = clock
	}
	if diskFreeFn != nil {
		a.diskFreeFn = diskFreeFn
	}
}

func (s *store) refuseLocked(reason storageAdmissionReason) error {
	s.admission.refusals[reason]++
	return &storageAdmissionError{reason: reason}
}

func (s *store) admitGrowthLocked() error {
	a := s.admission
	if min := a.limits.DiskFreeMinBytes; min > 0 {
		free, err := a.diskFreeFn()
		if err != nil || free < uint64(min) {
			return s.refuseLocked(admissionFreeDisk)
		}
	}
	if max := a.limits.MaxDBBytes; max > 0 {
		info, err := os.Stat(a.dbPath)
		if err != nil || info.Size() >= max {
			return s.refuseLocked(admissionDBBytes)
		}
	}
	if limit := a.limits.DurableGrowthWritesPerMin; limit > 0 {
		now := a.clock.Now()
		if !a.windowSet || now.Sub(a.windowStart) >= time.Minute || now.Before(a.windowStart) {
			a.windowStart = now
			a.windowSet = true
			a.windowWrites = 0
		}
		if a.windowWrites >= limit {
			return s.refuseLocked(admissionGrowthWrites)
		}
		// Reserve before entering bbolt. Even a transaction later rolled back by the
		// object fence consumed bounded storage work and must not become a free CPU loop.
		a.windowWrites++
	}
	return nil
}

// update is the only mutation path for the live relay store. Growth transactions pass
// every global fence; cleanup transactions deliberately bypass them so an at-cap relay can
// still acknowledge, revoke, and purge. admission.mu spans the bbolt transaction, making
// the in-memory object high-water an exact serialization of committed state.
func (s *store) update(growth bool, fn func(*bolt.Tx) (storeMutation, error)) error {
	a := s.admission
	a.mu.Lock()
	defer a.mu.Unlock()

	if growth {
		if err := s.admitGrowthLocked(); err != nil {
			return err
		}
	}
	var mutation storeMutation
	err := s.db.Update(func(tx *bolt.Tx) error {
		m, err := fn(tx)
		if err != nil {
			return err
		}
		if growth {
			if max := a.limits.MaxDurableObjects; max > 0 && a.durableObjects+m.durableDelta > max {
				return s.refuseLocked(admissionDurableObjects)
			}
			if max := a.limits.MaxDBBytes; max > 0 {
				// bbolt spills dirty nodes only after this managed callback returns, so
				// tx.Size alone cannot see a pending large value. Pair its page high-water
				// with the exact positive logical growth reported by the mutation. This is
				// conservative when free pages are reusable, but never permits a one-write
				// overshoot and still rolls the whole transaction back here.
				info, statErr := os.Stat(a.dbPath)
				if statErr != nil || tx.Size() > max || info.Size()+m.growthBytes > max {
					return s.refuseLocked(admissionDBBytes)
				}
			}
		}
		mutation = m
		return nil
	})
	if err != nil {
		return err
	}
	a.durableObjects += mutation.durableDelta
	if a.durableObjects < 0 {
		// This is an internal accounting invariant, not a recoverable runtime state.
		// Recount rather than publish a nonsensical negative metric.
		_ = s.db.View(func(tx *bolt.Tx) error {
			a.durableObjects = durableObjectCountTx(tx)
			return nil
		})
	}
	if growth {
		a.growthWrites++
	}
	a.cleanupItems += mutation.cleanupItems
	a.cleanupBoxes += mutation.cleanupMailboxes
	return nil
}

// durableObjectCountTx counts only caller-created rows and nested mailbox buckets. The
// fixed root buckets and one mailbox-incarnation metadata row are implementation overhead,
// not objects an attacker can mint.
func durableObjectCountTx(tx *bolt.Tx) int64 {
	var total int64
	items := tx.Bucket(bucketItems)
	if items != nil {
		_ = items.ForEach(func(k, v []byte) error {
			total++ // one nested mailbox bucket (v == nil), or a future direct row
			if v == nil {
				mb := items.Bucket(k)
				_ = mb.ForEach(func(_, _ []byte) error {
					total++
					return nil
				})
			}
			return nil
		})
	}
	for _, name := range [][]byte{bucketSeq, bucketPairs, bucketRevoked, bucketTokens, bucketConsents, bucketRetired} {
		if b := tx.Bucket(name); b != nil {
			_ = b.ForEach(func(_, _ []byte) error {
				total++
				return nil
			})
		}
	}
	return total
}

type storageSnapshot struct {
	DurableObjects   int64
	DBBytes          int64
	FreeDiskBytes    uint64
	FreeDiskKnown    bool
	GrowthWrites     uint64
	CleanupItems     uint64
	CleanupMailboxes uint64
	Refusals         map[storageAdmissionReason]uint64
}

func (s *store) storageSnapshot() storageSnapshot {
	a := s.admission
	a.mu.Lock()
	defer a.mu.Unlock()
	snap := storageSnapshot{
		DurableObjects:   a.durableObjects,
		GrowthWrites:     a.growthWrites,
		CleanupItems:     a.cleanupItems,
		CleanupMailboxes: a.cleanupBoxes,
		Refusals:         make(map[storageAdmissionReason]uint64, len(a.refusals)),
	}
	for reason, n := range a.refusals {
		snap.Refusals[reason] = n
	}
	if info, err := os.Stat(a.dbPath); err == nil {
		snap.DBBytes = info.Size()
	}
	if free, err := a.diskFreeFn(); err == nil {
		snap.FreeDiskBytes = free
		snap.FreeDiskKnown = true
	}
	return snap
}
