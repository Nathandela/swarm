// Epic 11 RESUME-AS-NEW-SESSION end-to-end (E11.5 / R-2, scenario 12), strengthened
// per audit-010 B1/B2: resume is a real flow, not a label. A session whose adapter
// captures a native conversation id, once ended, RESUMES into a NEW session whose
// argv is the adapter's REAL resume argv carrying that id (never a fresh launch
// falsely stamped ResumedFrom). This drives the reference adapter (registry-resolved,
// resumable, marker-based id extraction) backed by a FAKE "reference-cli" stub — no
// billable real-CLI run; the exact real Claude/Codex markers are Epic 14 VERIFY.
package e2e

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// fakeReferenceBinDir writes a stub "reference-cli" (the reference adapter's binary)
// that emits the conv-id marker its ExtractConversationID reads, then idles so the
// session stays running long enough for the runtime capture.
func fakeReferenceBinDir(t *testing.T, convID string) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "swfakeref")
	if err != nil {
		t.Fatalf("bin dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	script := "#!/bin/sh\n" +
		"# Fake reference-cli (Epic 11 B2 capture test): print the conv-id marker the\n" +
		"# reference adapter extracts from the transcript, then idle.\n" +
		"printf 'conv-id=%s\\n' '" + convID + "'\n" +
		"sleep 30\n"
	if err := os.WriteFile(filepath.Join(dir, "reference-cli"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake reference-cli: %v", err)
	}
	return dir
}

// TestE2E_ResumeAsNewSession_R2 launches a reference-adapter session that emits a
// conversation-id marker, lets the daemon CAPTURE it at runtime from the output tap
// (B2), ends it, then RESUMES it — asserting the new session's argv is the adapter's
// real RESUME argv carrying the captured id (not a fresh launch), and that
// ResumedFrom links back to the source (R-2, B1).
// It then ends that child and resumes it once more, proving the native identity was
// persisted into the child before its shim ran rather than being usable for only one
// resume hop.
func TestE2E_ResumeAsNewSession_R2(t *testing.T) {
	buildBinaries(t)
	env := newDaemonEnv(t)
	startDaemon(t, env)
	c := dial(t, env.sock)

	const convID = "conv-REF-abc123"
	binDir := fakeReferenceBinDir(t, convID)
	agentEnv := []string{"PATH=" + binDir + ":" + os.Getenv("PATH")}
	srcCwd := t.TempDir()

	srcID, _, err := c.Launch(protocol.LaunchReq{
		Agent: "reference", Cwd: srcCwd, Options: map[string]string{},
		Env: agentEnv, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("launch reference: %v", err)
	}
	t.Cleanup(func() { _ = c.Delete(srcID) })
	srcLocal := localOf(t, srcID)

	// (B2) The daemon captures the conversation id from the session's live output.
	waitForConversationID(t, env.stateDir, srcLocal, convID)

	// End the source so it is resumable (resume requires an ended/lost source).
	if err := c.Kill(srcID); err != nil {
		t.Fatalf("kill source: %v", err)
	}
	if _, ok := waitForStatus(t, c, srcID, l1Bound, func(s status.Status) bool {
		return s.Process != status.ProcessRunning
	}); !ok {
		t.Fatalf("source session never ended after Kill")
	}

	// Resume it → a NEW session whose argv is the adapter RESUME argv carrying the id.
	newID, _, err := c.Launch(protocol.LaunchReq{
		Agent: "reference", Cwd: srcCwd,
		Options: map[string]string{protocol.OptionResumeFrom: srcID},
		Env:     agentEnv, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("resume launch: %v", err)
	}
	t.Cleanup(func() { _ = c.Delete(newID) })
	newLocal := localOf(t, newID)
	if newLocal == srcLocal {
		t.Fatalf("resume reused the source id %q; R-2 resumes as a NEW session", srcLocal)
	}

	// The composed argv must be the adapter's resume argv carrying the captured id —
	// proof the resume actually resumes, rather than a fresh launch (B1).
	argv := readShimArgv(t, env.stateDir, newLocal)
	if !argvHas(argv, "--resume") || !argvHas(argv, convID) {
		t.Fatalf("resumed session argv %v is not the adapter resume argv carrying %q", argv, convID)
	}

	// The new session must link back to the source and durably inherit the provider
	// identity. This meta is written during the daemon reservation, before the shim
	// starts, so a daemon crash or a lost child cannot erase the next resume hop.
	deadline := time.Now().Add(l1Bound)
	for time.Now().Before(deadline) {
		meta := readMeta(t, env.stateDir, newLocal)
		if meta.ResumedFrom == srcLocal && meta.ConversationID == convID {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	newMeta := readMeta(t, env.stateDir, newLocal)
	if newMeta.ResumedFrom != srcLocal || newMeta.ConversationID != convID {
		t.Fatalf("resumed child meta = {ResumedFrom:%q ConversationID:%q}; want {%q %q}",
			newMeta.ResumedFrom, newMeta.ConversationID, srcLocal, convID)
	}

	// A lost resumed child must be a valid source for the next resume. This is the
	// regression: before native identity inheritance, B had an empty ConversationID
	// until provider output happened to be captured, so B -> C could silently fail.
	if err := c.Kill(newID); err != nil {
		t.Fatalf("kill resumed child: %v", err)
	}
	if _, ok := waitForStatus(t, c, newID, l1Bound, func(s status.Status) bool {
		return s.Process != status.ProcessRunning
	}); !ok {
		t.Fatalf("resumed child never ended after Kill")
	}

	thirdID, _, err := c.Launch(protocol.LaunchReq{
		Agent: "reference", Cwd: srcCwd,
		Options: map[string]string{protocol.OptionResumeFrom: newID},
		Env:     agentEnv, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("resume child: %v", err)
	}
	t.Cleanup(func() { _ = c.Delete(thirdID) })
	thirdLocal := localOf(t, thirdID)
	thirdArgv := readShimArgv(t, env.stateDir, thirdLocal)
	if !argvHas(thirdArgv, "--resume") || !argvHas(thirdArgv, convID) {
		t.Fatalf("second resumed session argv %v is not the adapter resume argv carrying %q", thirdArgv, convID)
	}
	thirdMeta := readMeta(t, env.stateDir, thirdLocal)
	if thirdMeta.ResumedFrom != newLocal || thirdMeta.ConversationID != convID {
		t.Fatalf("second resumed child meta = {ResumedFrom:%q ConversationID:%q}; want {%q %q}",
			thirdMeta.ResumedFrom, thirdMeta.ConversationID, newLocal, convID)
	}
}

// waitForConversationID polls a session's persisted meta until its ConversationID
// equals want — the daemon's runtime capture from the output tap (B2), or fails.
func waitForConversationID(t *testing.T, stateDir, local, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if readMeta(t, stateDir, local).ConversationID == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("daemon never captured conversation id %q for %s within 10s (B2: the grid-tap must call "+
		"adapter.ExtractConversationID on the output and persist Meta.ConversationID)", want, local)
}
