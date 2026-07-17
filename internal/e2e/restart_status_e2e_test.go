// Epic 11 FIX 2 end-to-end (L2): typed status survives a daemon restart. Before
// this fix, RegisterSession ran only at fresh launch; reconcile re-adopted a
// reconnected session but never re-registered it with the engine, so after a
// kill -9 + restart every real hook was rejected as "unregistered" and status
// froze. reconcile now re-registers each reconnected running session using the
// per-session hook token re-read from the 0600 shim-launch.json, so a hook bearing
// the SURVIVING token still drives status after the restart.
package e2e

import (
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/hookclient"
	"github.com/Nathandela/swarm/internal/status"
)

func TestE2E_TypedStatusSurvivesDaemonRestart_L2(t *testing.T) {
	buildBinaries(t)
	env := newDaemonEnv(t)
	d1 := startDaemon(t, env)
	c := dial(t, env.sock)

	id := launchFakeSession(t, c, "print RUNNING\nidle 120s\n")
	waitOneView(t, c)
	local := localOf(t, id)
	token := readHookToken(t, env.stateDir, local) // the token persisted in shim-launch.json

	// kill -9 the daemon; the shim (and the token in its shim-launch.json) survive.
	if err := syscall.Kill(d1.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill -9 daemon: %v", err)
	}
	time.Sleep(200 * time.Millisecond) // let the OS release the flock

	// Restart: reconcile must re-register the reconnected session with the engine
	// using the SURVIVING token.
	startDaemon(t, env)
	c2 := dial(t, env.sock)
	waitOneView(t, c2)
	if st, _ := waitForStatus(t, c2, id, l1Bound, func(s status.Status) bool {
		return s.Process != status.ProcessLost
	}); st.Process == status.ProcessLost {
		t.Fatalf("session marked lost after restart; expected reconnected/running")
	}

	// A hook bearing the surviving token must STILL be accepted and drive status.
	cb := engine.Callback{
		SessionID: local,
		Token:     token,
		Sequence:  1,
		Event:     "Notification",
		Payload: map[string]string{
			engine.PayloadKeyTurn:        string(status.TurnIdle),
			engine.PayloadKeyInteraction: string(status.InteractionPermission),
		},
	}
	if err := hookclient.Post(env.sock, cb); err != nil {
		t.Fatalf("post hook with surviving token after restart: %v", err)
	}
	st, ok := waitForStatus(t, c2, id, l1Bound, func(s status.Status) bool {
		return s.Interaction == status.InteractionPermission && s.Turn == status.TurnIdle
	})
	if !ok {
		t.Fatalf("a hook with the surviving token did not update status after restart (last=%+v); reconcile "+
			"must re-register the reconnected session with the engine using the token from the 0600 "+
			"shim-launch.json (L2)", st)
	}
}
