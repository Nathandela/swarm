// Epic 11 C1: conversation-id capture must be INDEPENDENT of the live attach. The
// shim serves connections serially, so while a client holds an attach the grid-tap
// can never sample the session; the old grid-tap-only capture would leave such a
// session with an empty ConversationID and non-resumable. Capture is driven off the
// transcript file on disk (poll cadence + a session-end net), so it works regardless.
//
// This test proves the PRIMARY path: capture completes WHILE a client holds the
// attach for the session's whole running life, purely from the transcript-file poll
// (never the grid-tap's own attach, which is blocked here). The session-end capture
// net (OnSessionEnd -> SetConversationID after finalizeTerminal) is exercised by the
// daemon/skeleton unit tests; here the running-phase poll already captures the id.
package e2e

import (
	"os"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
)

func TestE2E_ConversationCapture_DuringHeldAttach_C1(t *testing.T) {
	buildBinaries(t)
	env := newDaemonEnv(t)
	startDaemon(t, env)
	c := dial(t, env.sock)

	const convID = "conv-REF-c1held"
	binDir := fakeReferenceBinDir(t, convID)
	id, _, err := c.Launch(protocol.LaunchReq{
		Agent: "reference", Cwd: t.TempDir(), Options: map[string]string{},
		Env: []string{"PATH=" + binDir + ":" + os.Getenv("PATH")}, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("launch reference: %v", err)
	}
	t.Cleanup(func() { _ = c.Delete(id) })
	local := localOf(t, id)

	// Attach and HOLD for the session's whole life: the shim serves serially, so the
	// grid-tap's own attach can never run while this client holds the session — the
	// old grid-tap-only capture path is blocked.
	att, err := c.Attach(id)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	defer func() { _ = att.Detach() }()

	// Capture must still happen — from the transcript file, independent of the attach.
	waitForConversationID(t, env.stateDir, local, convID)
}
