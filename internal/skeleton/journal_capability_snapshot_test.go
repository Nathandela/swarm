package skeleton

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

func TestJournalSubscribeFrom_CapturesRosterCapabilitiesAtTheSameBoundary(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "skcapsnap")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	const sessionID = "session"
	store, err := persist.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(persist.Meta{
		ID: sessionID, AgentType: "claude",
		Status: status.Status{Process: status.ProcessExited},
	}); err != nil {
		t.Fatal(err)
	}
	core, err := daemon.Open(daemon.Config{
		StateDir: dir, SocketPath: filepath.Join(dir, "d.sock"),
		LockPath: filepath.Join(dir, "d.lock"), LogPath: filepath.Join(dir, "d.log"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.Close() })

	sk := &Daemon{core: core}
	old := protocol.SessionCapabilities{Provider: "claude", SessionInstance: "old", StructuredChat: true}
	next := protocol.SessionCapabilities{Provider: "claude", SessionInstance: "next", StructuredChat: false}
	sk.capStore.byID = map[string]protocol.SessionCapabilities{sessionID: old}

	api := newCoreAPI(core, "", "ep")
	t.Cleanup(api.close)
	lookupStarted := make(chan struct{})
	releaseLookup := make(chan struct{})
	api.sessionCaps = func(local string) (protocol.SessionCapabilities, bool) {
		close(lookupStarted)
		<-releaseLookup
		return sk.sessionCapabilities(local)
	}
	api.withSessionStateSnapshot = func(capture func()) {
		sk.capStore.authorMu.Lock()
		defer sk.capStore.authorMu.Unlock()
		sk.capStore.transitionMu.Lock()
		defer sk.capStore.transitionMu.Unlock()
		capture()
	}

	type result struct {
		resume protocol.JournalResume
		live   <-chan protocol.JournalRecord
		cancel func()
		err    error
	}
	resultCh := make(chan result, 1)
	go func() {
		resume, live, cancel, err := api.JournalSubscribeFrom(0)
		resultCh <- result{resume: resume, live: live, cancel: cancel, err: err}
	}()
	select {
	case <-lookupStarted:
	case <-time.After(time.Second):
		t.Fatal("capability lookup did not reach the deterministic boundary")
	}

	mutationAttempted := make(chan struct{})
	mutationDone := make(chan struct{})
	go func() {
		close(mutationAttempted)
		sk.capStore.transitionMu.Lock()
		sk.capStore.mu.Lock()
		sk.capStore.byID[sessionID] = next
		sk.capStore.mu.Unlock()
		sk.capStore.transitionMu.Unlock()
		close(mutationDone)
	}()
	<-mutationAttempted
	mutatedBeforeRelease := false
	select {
	case <-mutationDone:
		mutatedBeforeRelease = true
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseLookup)

	var got result
	select {
	case got = <-resultCh:
	case <-time.After(time.Second):
		t.Fatal("JournalSubscribeFrom did not finish")
	}
	if got.cancel != nil {
		got.cancel()
	}
	select {
	case <-mutationDone:
	case <-time.After(time.Second):
		t.Fatal("capability mutation stayed blocked after snapshot completed")
	}
	if got.err != nil {
		t.Fatal(got.err)
	}
	if mutatedBeforeRelease {
		t.Fatal("capability mutation crossed the roster snapshot boundary")
	}
	if len(got.resume.Roster) != 1 || got.resume.Roster[0].Capabilities == nil ||
		*got.resume.Roster[0].Capabilities != old {
		t.Fatalf("atomic roster capability = %#v, want boundary value %#v", got.resume.Roster, old)
	}
}
