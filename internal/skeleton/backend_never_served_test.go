package skeleton

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
)

// TestBackendNeverServed_OnlyTheLaunchDialFailureIsDegradedStartEvidence pins that the
// widened Codex resume windows follow only the launch-dial failure, after which the shim
// starts the agent late. A rejoin failure on a restarted daemon still degrades the session
// but its agent started on time, so it must not read as a degraded start.
func TestBackendNeverServed_OnlyTheLaunchDialFailureIsDegradedStartEvidence(t *testing.T) {
	t.Run("launch dial", func(t *testing.T) {
		r := newR7R4Rig(t, fakeCodexThreadID)
		r.sk.backendReady = 400 * time.Millisecond
		r.sk.joinSessionBackend(r.local, daemon.BackendChannel{SocketPath: "/tmp/r7r4-nothing-serves.sock"})
		if !r.sk.sessionDegraded(r.local) {
			t.Fatal("fixture: the launch-dial failure did not degrade the session")
		}
		resolver := &filesystemResumeHistoryResolver{stateDir: r.sk.capStore.dir}
		if !resolver.degradedStart(r.local) {
			t.Fatal("a backend that never served at launch is not degraded-start evidence")
		}
	})
	t.Run("rejoin", func(t *testing.T) {
		r := newR7ComposerRig(t, false)
		r.sk.backendReady = 300 * time.Millisecond
		r.sk.rejoinSessionBackend(r.local, daemon.BackendChannel{SocketPath: "/tmp/r7-no-such-backend.sock"})
		if !r.sk.sessionDegraded(r.local) {
			t.Fatal("fixture: the rejoin failure did not degrade the session")
		}
		resolver := &filesystemResumeHistoryResolver{stateDir: r.sk.capStore.dir}
		if resolver.degradedStart(r.local) {
			t.Fatal("a rejoin failure, whose agent started on time, reads as a degraded start")
		}
	})
}
