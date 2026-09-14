package skeleton

import (
	"testing"
	"time"
)

// An auth resume holds the lifecycle lane before acquiring the resume mutex.
// Owner deletion must take the same order or the two operations deadlock.
func TestDiscussionDeleteWaitsForLifecycleBeforeResumeMutex(t *testing.T) {
	r := newResumeAPIRig(t, "codex", migratedConversationID, nil)
	d := &Daemon{core: r.core, api: r.api}
	r.api.deleteFn = d.ownerDelete
	lane := d.composerLaneFor(r.sourceID)
	lane.endMu.Lock()
	unlocked := false
	defer func() {
		if !unlocked {
			lane.endMu.Unlock()
		}
	}()
	captured := make(chan struct{}, 1)
	hook := func(local string) {
		if local == r.sourceID {
			captured <- struct{}{}
		}
	}
	testHookOwnerEndLaneCaptured.Store(&hook)
	defer testHookOwnerEndLaneCaptured.Store(nil)
	done := make(chan error, 1)
	go func() { done <- r.api.Delete(r.sourceID) }()
	select {
	case <-captured:
	case <-time.After(time.Second):
		t.Fatal("delete did not reach lifecycle fence")
	}
	acquired := r.api.externalResumeMu.TryLock()
	if acquired {
		r.api.externalResumeMu.Unlock()
	}
	lane.endMu.Unlock()
	unlocked = true
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("delete: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("delete did not finish")
	}
	if !acquired {
		t.Fatal("delete held resume mutex while waiting for lifecycle fence")
	}
}
