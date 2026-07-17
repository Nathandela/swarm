// Package e2e is the Epic 8 walking-skeleton end-to-end suite (GG-1 / E8.7): it
// drives the REAL assembled binary — a `swarm daemon` subprocess — through a real
// protocol.Client against the fake agent, including the headline kill -9 survival
// demo. These are the milestone's acceptance tests.
//
// RED STATE — "assembly not built": these tests COMPILE (every referenced symbol
// exists today) but FAIL AT RUNTIME because `swarm daemon` (cmd/swarm.runDaemon)
// does not yet stand up the client protocol on its socket — it only opens
// daemon.Open, whose socket speaks the Epic 5 four-byte version handshake. So
// protocol.Dial cannot complete a handshake and startDaemon times out. They turn
// green when runDaemon performs the full assembly (see internal/skeleton): serve
// protocol.Serve(FromDaemon(core)) on the daemon socket, run the status engine,
// and route hook posts — AND resolves the reserved walking-skeleton agent "fake"
// (SWARM_FAKE_AGENT_BIN) to the swarm-fake-agent binary so a session can be
// launched through the client before any real adapter exists (Epic 9/11).
//
// PINNED assembly env knobs runDaemon must honor (flagged in the handoff):
//   - SWARM_DAEMON_STATE / _SOCK / _LOCK / _LOG   (already defined: daemon.Env*)
//   - SWARM_FAKE_AGENT_BIN                          NEW: dev/test-only fake-agent path
package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/vt"
)

// envFakeAgentBin is the dev/test-only knob naming the swarm-fake-agent binary the
// walking-skeleton assembly execs for the reserved agent "fake".
const envFakeAgentBin = "SWARM_FAKE_AGENT_BIN"

var (
	buildOnce    sync.Once
	swarmBin     string
	fakeAgentBin string
	buildErr     error
)

func buildBinaries(t *testing.T) {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "swe2e-bin")
		if err != nil {
			buildErr = err
			return
		}
		swarmBin = filepath.Join(dir, "swarm")
		fakeAgentBin = filepath.Join(dir, "swarm-fake-agent")
		for _, b := range []struct{ out, pkg string }{
			{swarmBin, "github.com/Nathandela/swarm/cmd/swarm"},
			{fakeAgentBin, "github.com/Nathandela/swarm/cmd/swarm-fake-agent"},
		} {
			cmd := exec.Command("go", "build", "-o", b.out, b.pkg)
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				buildErr = err
				return
			}
		}
	})
	if buildErr != nil {
		t.Skipf("cannot build e2e binaries: %v", buildErr)
	}
}

// daemonEnv holds the SWARM_DAEMON_* paths for one state dir (short-pathed under
// /tmp for the 104-byte sun_path limit).
type daemonEnv struct {
	stateDir string
	sock     string
	lock     string
	log      string
}

func newDaemonEnv(t *testing.T) daemonEnv {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "swe")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return daemonEnv{
		stateDir: dir,
		sock:     filepath.Join(dir, "d.sock"),
		lock:     filepath.Join(dir, "d.lock"),
		log:      filepath.Join(dir, "d.log"),
	}
}

// startDaemon spawns a real `swarm daemon` subprocess and waits until its socket
// answers the FULL client protocol handshake (proof the assembly is serving). It
// returns the running command; the caller kills it. On timeout it fails with the
// assembly-not-built diagnosis.
func startDaemon(t *testing.T, env daemonEnv) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(swarmBin, "daemon")
	cmd.Env = append(os.Environ(),
		"SWARM_DAEMON_STATE="+env.stateDir,
		"SWARM_DAEMON_SOCK="+env.sock,
		"SWARM_DAEMON_LOCK="+env.lock,
		"SWARM_DAEMON_LOG="+env.log,
		envFakeAgentBin+"="+fakeAgentBin,
	)
	logf, _ := os.Create(filepath.Join(env.stateDir, "daemon.stdio"))
	if logf != nil {
		cmd.Stdout, cmd.Stderr = logf, logf
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start swarm daemon: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		c, err := protocol.Dial(env.sock, []string{"attach", "subscribe"})
		if err == nil {
			_ = c.Close()
			return cmd
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("swarm daemon never served the client protocol on %s within 10s "+
		"(the daemon assembly is not built: runDaemon must stand up protocol.Serve + engine + hook routing)", env.sock)
	return nil
}

func dial(t *testing.T, sock string) *protocol.Client {
	t.Helper()
	c, err := protocol.Dial(sock, []string{"attach", "subscribe"})
	if err != nil {
		t.Fatalf("protocol.Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// launchFakeSession launches the reserved fake agent through the CLIENT protocol
// (the walking-skeleton path), returning the namespaced session id.
func launchFakeSession(t *testing.T, c *protocol.Client, script string) string {
	t.Helper()
	spath := filepath.Join(t.TempDir(), "script.txt")
	if err := os.WriteFile(spath, []byte(script), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	// Make the script world-readable — the daemon subprocess reads it when spawning.
	_ = os.Chmod(spath, 0o644)

	id, err := c.Launch(protocol.LaunchReq{
		Agent:   "fake",
		Cwd:     t.TempDir(),
		Options: map[string]string{"script": spath},
		Env:     []string{"PATH=" + os.Getenv("PATH")},
		Cols:    80,
		Rows:    24,
	})
	if err != nil {
		t.Fatalf("client Launch(fake): %v", err)
	}
	return id
}

func waitOneView(t *testing.T, c *protocol.Client) protocol.SessionView {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		views, err := c.List()
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(views) == 1 {
			return views[0]
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("session never appeared in List within 10s")
	return protocol.SessionView{}
}

// readMeta reads a session's persisted meta straight from disk (no daemon needed —
// used across a kill -9 to inspect the shim PID).
func readMeta(t *testing.T, stateDir, localID string) persist.Meta {
	t.Helper()
	store, err := persist.NewStore(stateDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	metas, err := store.Scan()
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	for _, m := range metas {
		if m.ID == localID {
			return m
		}
	}
	t.Fatalf("meta %s not found on disk", localID)
	return persist.Meta{}
}

func alive(pid int) bool { return pid > 0 && syscall.Kill(pid, 0) == nil }

// agentPIDOf returns the shim's child process — the agent itself. The shim owns the
// agent's PTY and execs it directly (internal/shim), and the shim becomes a session
// leader in place (no re-exec), so the agent is a direct child of meta.ShimPID. This
// lets GG-1 assert the AGENT survives a daemon kill -9, not merely its shim.
func agentPIDOf(t *testing.T, shimPID int) int {
	t.Helper()
	out, err := exec.Command("pgrep", "-P", strconv.Itoa(shimPID)).Output()
	if err != nil {
		t.Fatalf("pgrep -P %d (find the shim's agent child): %v", shimPID, err)
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		t.Fatalf("shim %d has no child agent process", shimPID)
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		t.Fatalf("parse agent pid %q: %v", fields[0], err)
	}
	return pid
}

// attachWhenGridHas attaches and returns once the painted grid contains want,
// re-attaching until it does (the agent prints asynchronously at startup). It gives
// the test a deterministic starting grid, so a later input's PTY echo cannot race
// ahead of the agent's own output and make the grid content nondeterministic.
func attachWhenGridHas(t *testing.T, c *protocol.Client, id, want string) *protocol.Attachment {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		a, err := c.Attach(id)
		if err != nil {
			t.Fatalf("Attach: %v", err)
		}
		if strings.Contains(gridText(t, a.Snapshot()), want) {
			return a
		}
		_ = a.Detach()
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("session grid never showed %q within 10s", want)
	return nil
}

// gridText decodes an attach snapshot — the shim's authoritative serialized grid —
// through a fresh vt.DecodeSnapshot and flattens it to plain per-cell text. Feeding
// the CLIENT-received snapshot back through the emulator's own decoder and reading
// the grid is how GG-1 proves the client-painted grid matches the shim's grid.
func gridText(t *testing.T, snap []byte) string {
	t.Helper()
	s, err := vt.DecodeSnapshot(snap)
	if err != nil {
		t.Fatalf("decode re-attach snapshot: %v", err)
	}
	var b strings.Builder
	for _, ln := range s.Lines {
		for _, run := range ln.Runs {
			b.WriteString(run.Text)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// localOf strips the endpoint namespace from a client-visible session id.
func localOf(t *testing.T, id string) string {
	t.Helper()
	_, local, ok := protocol.ParseID(id)
	if !ok {
		t.Fatalf("session id %q is not namespaced", id)
	}
	return local
}

// TestE2E_WalkingSkeleton_GG1 is THE milestone (GG-1 / E8.7): launch a fake agent
// through the assembled daemon, see it grouped, attach and paint a snapshot, type,
// detach, kill -9 the daemon, confirm the shim (and thus its agent) survives, then
// restart the daemon and confirm the session is reconnected — never lost — and
// re-attachable with its grid intact.
func TestE2E_WalkingSkeleton_GG1(t *testing.T) {
	buildBinaries(t)
	env := newDaemonEnv(t)

	// 1) launch fake agent -> grouped in the general view.
	d1 := startDaemon(t, env)
	c := dial(t, env.sock)
	id := launchFakeSession(t, c, "print SKELETON-LIVES\nidle 120s\n")
	view := waitOneView(t, c)
	if view.ID != id {
		t.Fatalf("listed id %q != launched id %q", view.ID, id)
	}
	if view.Group == "" {
		t.Fatal("listed session has no server-derived group")
	}

	// 2) attach -> snapshot paints the agent's output; type reaches the agent. Wait
	// for the agent's printed line to land in the grid BEFORE typing, so the input's
	// PTY echo cannot race ahead of the agent's own output (which would make the
	// grid content nondeterministic).
	a := attachWhenGridHas(t, c, id, "SKELETON-LIVES")
	if len(a.Snapshot()) == 0 {
		t.Fatal("attach painted an empty snapshot (A-4/S10)")
	}
	if err := a.Input([]byte("hello\n")); err != nil {
		t.Fatalf("Input: %v", err)
	}

	// 3) detach -> session continues.
	if err := a.Detach(); err != nil {
		t.Fatalf("Detach: %v", err)
	}

	// The shim PID — and the AGENT it owns — that must survive the daemon kill.
	local := localOf(t, id)
	meta := readMeta(t, env.stateDir, local)
	if !alive(meta.ShimPID) {
		t.Fatalf("shim %d not alive before kill", meta.ShimPID)
	}
	agentPID := agentPIDOf(t, meta.ShimPID)
	if !alive(agentPID) {
		t.Fatalf("agent %d (child of shim %d) not alive before kill", agentPID, meta.ShimPID)
	}

	// 4) kill -9 the daemon -> the shim AND its agent process MUST survive (S1). The
	// headline claim is agent survival, so assert the real agent PID, not just the shim.
	if err := syscall.Kill(d1.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill -9 daemon: %v", err)
	}
	// Give the OS a moment to release the flock.
	time.Sleep(200 * time.Millisecond)
	if !alive(meta.ShimPID) {
		t.Fatal("shim died when the daemon was kill -9'd — violates S1 survival")
	}
	if !alive(agentPID) {
		t.Fatalf("agent %d died when the daemon was kill -9'd — violates S1 (agent survival)", agentPID)
	}

	// 5) restart daemon -> reconnects the session, nothing lost.
	startDaemon(t, env)
	c2 := dial(t, env.sock)
	view2 := waitOneView(t, c2)
	if view2.ID != id {
		t.Fatalf("after restart, listed id %q != %q", view2.ID, id)
	}
	if view2.Status.Process == status.ProcessLost {
		t.Fatal("reconnected session marked lost after restart — violates GG-1 zero-loss (L2)")
	}

	// Meta continuity: the reconnected session is the SAME shim (same id, same PID),
	// reconnected — not a fresh relaunch that silently lost the original.
	meta2 := readMeta(t, env.stateDir, local)
	if meta2.ID != meta.ID {
		t.Fatalf("meta id changed across restart: %q != %q", meta2.ID, meta.ID)
	}
	if meta2.ShimPID != meta.ShimPID {
		t.Fatalf("reconnected shim PID %d != original %d — the session was relaunched, not reconnected",
			meta2.ShimPID, meta.ShimPID)
	}

	// 6) re-attach after restart -> the client-painted grid still carries the agent's
	// output. Decoding the client's snapshot back through the vt emulator and finding
	// the agent's printed line proves the grid the client paints matches the shim's
	// authoritative grid (GG-1 zero-loss), not merely that some bytes arrived.
	a2, err := c2.Attach(id)
	if err != nil {
		t.Fatalf("re-Attach after restart: %v", err)
	}
	if len(a2.Snapshot()) == 0 {
		t.Fatal("re-attach after restart painted an empty snapshot — grid lost (GG-1 zero-loss)")
	}
	if grid := gridText(t, a2.Snapshot()); !strings.Contains(grid, "SKELETON-LIVES") {
		t.Fatalf("re-attach grid lost the agent's output (GG-1 zero-loss); grid was:\n%s", grid)
	}
}

// TestE2E_Scenario3_SurvivesClientClose (scenario 3 / D-2/D-3) — the session
// continues when the launching client goes away: launch, fully close the client
// connection, then a fresh client still lists the same running session (the shim is
// setsid-detached and independent of any client or terminal).
func TestE2E_Scenario3_SurvivesClientClose(t *testing.T) {
	buildBinaries(t)
	env := newDaemonEnv(t)

	startDaemon(t, env)
	c := dial(t, env.sock)
	id := launchFakeSession(t, c, "print STILL-HERE\nidle 120s\n")
	waitOneView(t, c)
	local := localOf(t, id)
	meta := readMeta(t, env.stateDir, local)

	_ = c.Close() // the launching client goes away entirely

	if !alive(meta.ShimPID) {
		t.Fatal("shim died when the client closed — violates D-2/D-3")
	}

	// A fresh client still sees the running session.
	c2 := dial(t, env.sock)
	view2 := waitOneView(t, c2)
	if view2.ID != id {
		t.Fatalf("after client close, listed id %q != %q", view2.ID, id)
	}
	if view2.Status.Process == status.ProcessLost {
		t.Fatal("session marked lost after a mere client close (scenario 3)")
	}
}

// TestE2E_DaemonKilledMidAttach (E8.6) — the daemon is kill -9'd WHILE a client is
// attached: the shim is unaffected, the client's live stream closes so it detaches
// sanely (never hangs), and a re-attach after restart works.
func TestE2E_DaemonKilledMidAttach(t *testing.T) {
	buildBinaries(t)
	env := newDaemonEnv(t)

	d1 := startDaemon(t, env)
	c := dial(t, env.sock)
	id := launchFakeSession(t, c, "print MID-ATTACH\nidle 120s\n")
	waitOneView(t, c)

	a, err := c.Attach(id)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	local := localOf(t, id)
	meta := readMeta(t, env.stateDir, local)

	// Kill the daemon while attached.
	if err := syscall.Kill(d1.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill -9 daemon: %v", err)
	}

	// The client's live stream must close (detach sanely, never hang).
	closed := make(chan struct{})
	go func() {
		for range a.Frames() {
		}
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("attach Frames() did not close after the daemon died — client would hang")
	}

	// The shim is unaffected.
	if !alive(meta.ShimPID) {
		t.Fatal("shim died with the daemon (mid-attach) — violates S1")
	}

	// Re-attach after restart works.
	time.Sleep(200 * time.Millisecond)
	startDaemon(t, env)
	c2 := dial(t, env.sock)
	waitOneView(t, c2)
	if _, err := c2.Attach(id); err != nil {
		t.Fatalf("re-Attach after mid-attach daemon kill + restart: %v", err)
	}
}
