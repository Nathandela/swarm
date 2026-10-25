package skeleton

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/logins"
)

func TestCodexOwnerAdmissionReadinessIsCheckedBeforeBegin(t *testing.T) {
	f := newLoginFixture(t)
	enableCodexNativeGrantUsage(t, f)
	if err := f.m.prepareCodexSelection(); err != nil {
		t.Fatal(err)
	}
	r, err := f.m.store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	selection, err := selectedCodexAuth(r)
	if err != nil {
		t.Fatal(err)
	}
	const local = "source-1"
	feed := newBackendFeed()
	state := &codexHandoverFeed{
		native: logins.NativeMember{SessionID: local, NativeIncarnation: "same-native"},
		feed:   feed, current: func() bool { return true },
		adopted: selection, readySelection: selection, ready: true,
	}
	c := newCodexHandoverCoordinator(f.m)
	f.m.codexHandover = c
	c.feeds[feed] = state
	c.usageSelection, c.usageAllowed = selection, true

	backend := newR7FakeBackend()
	conn := &backendWriteBoundaryRecorder{base: backend}
	lane := &composerLane{}
	var begins atomic.Int32
	// Characterize the dispatch boundary, not full RPC routing. composerSendFrom
	// supplies this same semantic gate before the original durable Begin callback.
	begin := func() error {
		if err := f.m.semanticInputGate(local); err != nil {
			return err
		}
		begins.Add(1)
		return nil
	}
	send := func() error {
		return callComposerBackendAtWriteBoundaryPrepared(context.Background(), local, lane, 0, conn, "turn/start", nil, nil, begin, nil, nil)
	}
	begun, release := make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	hook := func(id string) {
		if id == local {
			close(begun)
			<-release
		}
	}
	testHookComposerBackendBegunNotYetCalled.Store(&hook)
	t.Cleanup(func() { testHookComposerBackendBegunNotYetCalled.Store(nil) })
	done := make(chan error, 1)
	exited := make(chan struct{})
	defer func() {
		unblock.Do(func() { close(release) })
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			t.Error("owner sender did not exit before fixture cleanup")
		}
	}()
	go func() {
		defer close(exited)
		done <- send()
	}()
	select {
	case <-begun:
	case <-time.After(5 * time.Second):
		t.Fatal("owner input did not complete Begin")
	}
	if begins.Load() != 1 {
		t.Fatal("hook ran before original Begin completed")
	}
	state.invalidate()
	if state.readyFor(selection) {
		t.Fatal("same native feed retained readiness after invalidation")
	}
	unblock.Do(func() { close(release) })
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("already admitted owner input: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("admitted write remained blocked")
	}
	testHookComposerBackendBegunNotYetCalled.Store(nil)
	if got := methodsOf(backend); len(got) != 1 || got[0] != "turn/start" {
		t.Fatalf("admitted writes = %v; want one turn/start", got)
	}
	secondErr := send()
	if got := methodsOf(backend); len(got) != 1 {
		t.Fatalf("fresh input wrote after readiness invalidation: %v", got)
	}
	if secondErr == nil || begins.Load() != 1 {
		t.Fatalf("fresh input must refuse before Begin: err=%v begins=%d", secondErr, begins.Load())
	}
}
