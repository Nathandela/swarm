package skeleton

// Owner lifecycle arbitration for automatic credential recycling.
//
// An owner Kill/Delete and authwatch's kill can observe the same persisted
// Running row before either asynchronous terminal status is folded. The
// composer lane's endMu gives those actions one linear order. A successful
// owner action retains ownerEnding until endSession retires the whole lane, so
// authwatch cannot mistake daemon.Kill's terminal no-op for a kill it owns and
// later resurrect a session the owner ended deliberately.

import (
	"errors"
	"sync/atomic"
)

var errAuthOwnerEnding = errors.New("authwatch: the owner already ended this session")
var errAuthRecycleInProgress = errors.New("skeleton: session is recycling stale credentials")

var testHookOwnerEndLaneCaptured atomic.Pointer[func(string)]
var testHookAuthResumeLaneCaptured atomic.Pointer[func(string)]

// withOwnerSessionEnd serializes one owner-authorized terminal action against
// auth recycling. A failed action releases the claim; a successful action keeps
// it until forgetInteractions drops the lane at session retirement.
func (d *Daemon) withOwnerSessionEnd(local string, action func() error) error {
	if action == nil {
		return errors.New("skeleton: nil owner session-end action")
	}
	lane := d.composerLaneFor(local)
	if hook := testHookOwnerEndLaneCaptured.Load(); hook != nil {
		(*hook)(local)
	}
	lane.endMu.Lock()
	defer lane.endMu.Unlock()
	// Auth publishes recycling at its composer queue head before it waits for
	// this lifecycle lock. Once that authority has linearized, a later owner
	// action must not create a second meaning for the same terminal edge.
	lane.mu.Lock()
	if lane.recycling {
		lane.mu.Unlock()
		return errAuthRecycleInProgress
	}
	lane.ownerEnding = true
	lane.ownerActive = true
	lane.mu.Unlock()
	if err := action(); err != nil {
		lane.mu.Lock()
		lane.ownerActive = false
		lane.ownerEnding = false
		retired := lane.retired
		lane.mu.Unlock()
		if retired {
			d.composerLanes.CompareAndDelete(local, lane)
		}
		return err
	}
	// Kill may return after merely delivering an asynchronous signal, whereas
	// Delete can invoke endSession synchronously. Retain ownerEnding until that
	// retirement occurs; if it already occurred, remove the map entry now while
	// leaving captured old pointers as refusal tombstones.
	lane.mu.Lock()
	lane.ownerActive = false
	retired := lane.retired
	lane.mu.Unlock()
	if retired {
		d.composerLanes.CompareAndDelete(local, lane)
	}
	return nil
}

// withAuthResumeFence serializes the launch+old-row-delete half of an owed
// recycle against owner Kill/Delete. The recycle bit was restored synchronously
// before either socket became ready. On a final attempt the map entry is retired
// without clearing the old pointer, so a concurrently parked owner still sees
// the auth authority and refuses rather than cancelling the replacement.
func (d *Daemon) withAuthResumeFence(local string, attempt func() bool) (attempted, retry bool) {
	if attempt == nil {
		return false, false
	}
	lane := d.composerLaneFor(local)
	if hook := testHookAuthResumeLaneCaptured.Load(); hook != nil {
		(*hook)(local)
	}
	lane.endMu.Lock()
	defer lane.endMu.Unlock()
	lane.mu.Lock()
	if lane.ownerEnding {
		lane.mu.Unlock()
		return false, false
	}
	if !lane.recycling {
		lane.recycling = true
		lane.recycleRestored = true
	}
	lane.mu.Unlock()
	retry = attempt()
	if !retry {
		d.composerLanes.CompareAndDelete(local, lane)
	}
	return true, retry
}

func (d *Daemon) ownerKill(local string) error {
	if d.core == nil {
		return errors.New("skeleton: owner kill has no daemon core")
	}
	// Unknown calls retain daemon.Kill's established error. An existing terminal
	// row still takes the fence: it may carry a durable auth resume obligation,
	// and the owner's explicit end must win before that replacement launches.
	if _, ok := d.core.Get(local); !ok {
		return d.core.Kill(local)
	}
	return d.withOwnerSessionEnd(local, func() error { return d.core.Kill(local) })
}

func (d *Daemon) ownerDelete(local string) error {
	if d.core == nil {
		return errors.New("skeleton: owner delete has no daemon core")
	}
	if _, ok := d.core.Get(local); !ok {
		return d.core.Delete(local)
	}
	return d.withOwnerSessionEnd(local, func() error { return d.core.Delete(local) })
}
