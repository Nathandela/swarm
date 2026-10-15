package skeleton

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/status"
)

// TestEmitStatus_FailedPersistIsLoggedAndRetried pins swarm-ymm (R2 RC3): the engine
// commits a status before emitting it, so a SetStatus that fails (ENOSPC on the
// journal append or the meta save) is never re-emitted by the engine. The skeleton
// must log the failure once and keep re-pushing the status until it persists.
func TestEmitStatus_FailedPersistIsLoggedAndRetried(t *testing.T) {
	sink := captureLog(t)
	sk := assemble(t)
	m := launchFake(t, sk, "print HELLO\nidle 60s\n")
	time.Sleep(300 * time.Millisecond) // let the engine settle the fake session's own status

	// Break the meta save: a directory where meta.json is renamed makes store.Save
	// fail exactly like a full disk would, without filling one.
	metaPath := filepath.Join(sk.stateDir, m.ID, "meta.json")
	aside := metaPath + ".aside"
	if err := os.Rename(metaPath, aside); err != nil {
		t.Fatalf("move meta.json aside: %v", err)
	}
	if err := os.Mkdir(metaPath, 0o700); err != nil {
		t.Fatalf("block meta.json: %v", err)
	}

	want := status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionPermission}
	sk.api.emitStatus(m.ID, want)
	time.Sleep(5 * eventPoll) // several failed retries while the save stays broken
	if got, _ := sk.core.Get(m.ID); got.Status == want {
		t.Fatalf("status persisted although the meta save was broken: %+v", got.Status)
	}

	// The disk recovers.
	if err := os.Remove(metaPath); err != nil {
		t.Fatalf("unblock meta.json: %v", err)
	}
	if err := os.Rename(aside, metaPath); err != nil {
		t.Fatalf("restore meta.json: %v", err)
	}

	deadline := time.Now().Add(10 * eventPoll)
	for {
		var got status.Status
		for _, s := range sk.core.List() {
			if s.ID == m.ID {
				got = s.Status
			}
		}
		if got == want {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("List still shows %+v (group %s) after recovery, want %+v (group %s): the failed persist was never retried",
				got, status.Derive(got), want, status.Derive(want))
		}
		time.Sleep(20 * time.Millisecond)
	}

	n := 0
	for _, line := range strings.Split(sink.String(), "\n") {
		if strings.Contains(line, m.ID) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("want exactly one log line naming %s for the failure episode, got %d:\n%s", m.ID, n, sink.String())
	}
}
