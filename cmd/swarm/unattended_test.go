package main

// Lane E of docs/specifications/auto-upgrade-plan.md revision 5, section 3 layer L2:
// the REAL-LIFE proof of `swarm daemon restart --unattended`. Everything here is a
// real process. Two `swarm` binaries are built from this tree with different
// -ldflags version stamps (the shape version_test.go uses), the "old" one is started
// as an actual `swarm daemon` under a temp state dir, and the "new" one is run as a
// subprocess whose environment is the one a launchd timer would hand it: PATH=
// /usr/bin:/bin:/usr/sbin:/sbin, HOME, the SWARM_DAEMON_* stamps, and nothing else.
//
// The only thing NOT real is the machine's init system, and it is not faked either:
// every state dir is a fresh temp dir, so supervise.UnitPath resolves to
// <state>/remote/units/com.swarm.remote.plist, which does not exist, and the
// supervisor's requireUnit returns ErrNotInstalled BEFORE any launchctl call. The
// real launchd is never reached and the owner's own gateway is never touched.
//
// HOW THE REPLACEMENT'S ENVIRONMENT IS READ. The plan's evidence is that the
// replacement daemon carries the environment the OLD daemon saved, not the caller's.
// On this darwin, ps does not expose another process's environment at all --
// `ps -o command= -E -p <pid>`, `ps eww -p <pid>` and `ps ewwwp <pid>` were all
// tried and all print the command line only. So the read used here is the
// replacement's OWN report of its environment: internal/daemon's Open writes
// <state>/daemon.env from PolicyEnv(nil), i.e. persist.FilterEnv over its own
// os.Environ(), BEFORE it serves. The file is rewritten by a temp-file-plus-rename
// on every start, so a changed inode is proof the replacement wrote it, and its
// contents are that process's own environment through the S-2 allowlist -- which
// admits both probes used here (PATH exactly, LC_SWARM_PROBE under LC_*).

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/converge"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

const (
	// The two build stamps. They only have to differ from each other and from "dev".
	unattendedOldVersion = "0.0.0-old"
	unattendedNewVersion = "0.0.0-new"

	// unattendedProbe is carried by the DAEMON's environment as "saved" and by the
	// caller's as "caller", so the replacement's environment names its own source. It
	// is on the S-2 allowlist under LC_* (internal/persist/env.go), so it survives
	// FilterEnv into daemon.env exactly as PATH and the API keys do.
	unattendedProbe       = "LC_SWARM_PROBE"
	unattendedProbeSaved  = "saved"
	unattendedProbeCaller = "caller"

	// unattendedSavedPathMark prefixes the daemon's PATH so the replacement's PATH can
	// be identified positively, not merely as "not the launchd one". A directory that
	// does not exist at the head of PATH is inert.
	unattendedSavedPathMark = "/swarm-probe-saved-path"

	// unattendedLaunchdPath is launchd's default PATH for a user agent -- the value
	// the whole of L2 exists to keep OUT of the replacement daemon.
	unattendedLaunchdPath = "/usr/bin:/bin:/usr/sbin:/sbin"

	unattendedUsageLine = "usage: swarm daemon restart [--unattended]"
)

// buildStampedSwarm builds cmd/swarm with internal/version.Version stamped to v, the
// same -ldflags shape .goreleaser.yaml applies at release time.
func buildStampedSwarm(t *testing.T, dir, name, v string) string {
	t.Helper()
	bin := filepath.Join(dir, name)
	ldflags := "-X github.com/Nathandela/swarm/internal/version.Version=" + v
	cmd := exec.Command("go", "build", "-ldflags", ldflags, "-o", bin, "github.com/Nathandela/swarm/cmd/swarm")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build %s stamped %s: %v", name, v, err)
	}
	return bin
}

// unattendedDaemon is one real `swarm daemon` process under its own temp state dir.
type unattendedDaemon struct {
	stateDir  string
	sock      string
	callerEnv []string   // the launchd-like environment the converge subprocess runs under
	pid       int        // the FIRST daemon's pid; the replacement's is read from the pidfile
	exited    chan error // closed-over cmd.Wait, so the test can prove the old process is gone
}

// unattendedCallerEnv is the environment a launchd timer would hand
// `swarm daemon restart --unattended`: no owner PATH, no credentials, and a probe
// that must not reach the replacement. SWARM_DAEMON_STATE is always set, so no code
// path can fall back to the real ~/.local/state/swarm.
func unattendedCallerEnv(stateDir, sock string) []string {
	return []string{
		"PATH=" + unattendedLaunchdPath,
		"HOME=" + os.Getenv("HOME"),
		daemon.EnvStateDir + "=" + stateDir,
		daemon.EnvSocket + "=" + sock,
		daemon.EnvLock + "=" + filepath.Join(stateDir, "d.lock"),
		daemon.EnvLog + "=" + filepath.Join(stateDir, "d.log"),
		unattendedProbe + "=" + unattendedProbeCaller,
	}
}

// startUnattendedDaemon starts bin as a real `swarm daemon` in a fresh temp state dir
// and waits until it serves the client protocol. Its environment is built explicitly
// rather than appended to os.Environ(), so a duplicate PATH entry cannot decide which
// value the daemon actually reads.
func startUnattendedDaemon(t *testing.T, bin, fakeAgent string) *unattendedDaemon {
	t.Helper()
	// A short temp root: darwin caps UNIX socket paths near 104 bytes.
	dir, err := os.MkdirTemp("", "swu")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	d := &unattendedDaemon{stateDir: dir, sock: filepath.Join(dir, "d.sock")}
	d.callerEnv = unattendedCallerEnv(dir, d.sock)

	cmd := exec.Command(bin, "daemon")
	cmd.Env = []string{
		"PATH=" + unattendedSavedPathMark + ":" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"TMPDIR=" + os.Getenv("TMPDIR"),
		daemon.EnvStateDir + "=" + dir,
		daemon.EnvSocket + "=" + d.sock,
		daemon.EnvLock + "=" + filepath.Join(dir, "d.lock"),
		daemon.EnvLog + "=" + filepath.Join(dir, "d.log"),
		envFakeAgentBin + "=" + fakeAgent,
		unattendedProbe + "=" + unattendedProbeSaved,
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s daemon: %v", bin, err)
	}
	d.pid = cmd.Process.Pid
	d.exited = make(chan error, 1)
	go func() { d.exited <- cmd.Wait() }()
	// Registered after the RemoveAll above, so it runs first (LIFO). A converge
	// replaces this process with a DETACHED one that cmd knows nothing about, so
	// whatever the pidfile names now is killed too.
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		if pid, ok := unattendedDaemonPID(dir); ok && pid != d.pid {
			_ = syscall.Kill(pid, syscall.SIGTERM)
		}
	})

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if c, derr := protocol.Dial(d.sock, nil); derr == nil {
			_ = c.Close()
			return d
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("daemon %s never served the client protocol within 15s", bin)
	return nil
}

// unattendedDaemonPID reads the pid out of the "PID START" pidfile (daemon.go's
// writePIDFile format).
func unattendedDaemonPID(stateDir string) (int, bool) {
	data, err := os.ReadFile(filepath.Join(stateDir, "daemon.pid"))
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, false
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, false
	}
	return pid, true
}

// runSwarmUnattended runs `swarm <args...>` from bin under env and returns its exit
// status and stderr. A failure to start the process at all fails the test; a non-zero
// exit is a result, not an error.
func runSwarmUnattended(t *testing.T, bin string, env []string, args ...string) (int, string) {
	t.Helper()
	var stderr bytes.Buffer
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("run `swarm %s`: %v", strings.Join(args, " "), err)
		}
		code = ee.ExitCode()
	}
	t.Logf("`swarm %s` exit=%d stderr=%q", strings.Join(args, " "), code, stderr.String())
	return code, stderr.String()
}

// inodeOf identifies a file by inode, so a rewrite-by-rename is distinguishable from
// a file nobody touched.
func inodeOf(t *testing.T, path string) uint64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("stat %s: no syscall.Stat_t", path)
	}
	return uint64(st.Ino)
}

// savedEnvValue reads one KEY's value out of a daemon.env blob.
func savedEnvValue(blob, key string) string {
	for _, line := range strings.Split(blob, "\n") {
		if v, ok := strings.CutPrefix(line, key+"="); ok {
			return v
		}
	}
	return ""
}

// waitRosterBusy polls the ON-DISK roster (the same reader the converge's rule 2 uses)
// until id derives to a busy group.
func waitRosterBusy(t *testing.T, stateDir, id string) {
	t.Helper()
	read := converge.SessionsFromStore(stateDir)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		sessions, err := read()
		if err == nil {
			for _, s := range sessions {
				if s.ID != id {
					continue
				}
				if g := status.Derive(s.Status); g == status.GroupWorking || g == status.GroupNeedsInput {
					return
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("session %s never appeared busy in the on-disk roster at %s", id, stateDir)
}

// waitRosterIdle polls the on-disk roster until nothing derives to a busy group.
func waitRosterIdle(t *testing.T, stateDir string) {
	t.Helper()
	read := converge.SessionsFromStore(stateDir)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		sessions, err := read()
		if err == nil {
			busy := false
			for _, s := range sessions {
				if g := status.Derive(s.Status); g == status.GroupWorking || g == status.GroupNeedsInput {
					busy = true
					break
				}
			}
			if !busy {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the on-disk roster at %s never went idle", stateDir)
}

// TestUnattendedRestart_AgainstRealDaemons is the whole of lane E's evidence: six
// cases against real `swarm daemon` processes, run sequentially so the two binaries
// are built once.
func TestUnattendedRestart_AgainstRealDaemons(t *testing.T) {
	if testing.Short() {
		t.Skip("builds two stamped binaries and spawns real daemons")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}

	binDir := t.TempDir()
	oldBin := buildStampedSwarm(t, binDir, "swarm-old", unattendedOldVersion)
	newBin := buildStampedSwarm(t, binDir, "swarm-new", unattendedNewVersion)
	// The converge is invoked through a SYMLINK to the new binary, the shape Homebrew
	// installs (/usr/local/bin/swarm -> Caskroom/swarm/<ver>/swarm). The plan's
	// linked-path property (section 3 L2) says the replacement daemon must be spawned
	// as the LINK, never the resolved target: the daemon spawns every shim from its own
	// executable path (main.go's ShimBinary), and a daemon pinned to a Caskroom
	// directory can launch nothing once the next upgrade purges it. Case 1 asserts the
	// replacement's argv[0] is this link.
	newLink := filepath.Join(binDir, "linked", "swarm")
	if err := os.MkdirAll(filepath.Dir(newLink), 0o755); err != nil {
		t.Fatalf("mkdir for the link: %v", err)
	}
	if err := os.Symlink(newBin, newLink); err != nil {
		t.Fatalf("symlink %s -> %s: %v", newLink, newBin, err)
	}
	fakeAgent := filepath.Join(binDir, "swarm-fake-agent")
	buildBinary(t, fakeAgent, "github.com/Nathandela/swarm/cmd/swarm-fake-agent")

	// Cases 1, 2 and 6 share one daemon: 1 converges it onto the new build, 2 proves
	// the second run costs nothing, 6 proves a usage error moves nothing.
	d := startUnattendedDaemon(t, oldBin, fakeAgent)
	convergedPID := 0

	t.Run("case1_restart_spawns_the_replacement_from_the_saved_environment", func(t *testing.T) {
		savedPath := daemon.SavedEnvPath(d.stateDir)
		before, err := os.ReadFile(savedPath)
		if err != nil {
			t.Fatalf("a started daemon must save its environment at %s: %v", savedPath, err)
		}
		if !strings.Contains(string(before), unattendedProbe+"="+unattendedProbeSaved) {
			t.Fatalf("daemon.env must carry %s=%s; got:\n%s", unattendedProbe, unattendedProbeSaved, before)
		}
		beforeIno := inodeOf(t, savedPath)
		oldPID, ok := unattendedDaemonPID(d.stateDir)
		if !ok {
			t.Fatalf("no daemon.pid under %s", d.stateDir)
		}

		code, stderr := runSwarmUnattended(t, newLink, d.callerEnv, "daemon", "restart", "--unattended")
		if code != 0 {
			t.Fatalf("exit = %d, want 0; stderr:\n%s", code, stderr)
		}
		// The third substring is converge's own prose for the benign gateway arm: a temp
		// state dir has no unit, so the real supervisor returns ErrNotInstalled and the
		// wiring maps it. Without it a RestartGateway that returned nil unconditionally
		// would pass this case while never having attempted the gateway.
		for _, want := range []string{"converged", "restarted from the saved environment", "no gateway unit is installed"} {
			if !strings.Contains(stderr, want) {
				t.Fatalf("stderr must contain %q; got:\n%s", want, stderr)
			}
		}

		select {
		case <-d.exited:
		case <-time.After(15 * time.Second):
			t.Fatalf("the old daemon (pid %d) is still running after the converge", oldPID)
		}

		c, err := protocol.Dial(d.sock, nil)
		if err != nil {
			t.Fatalf("the replacement does not answer on %s: %v", d.sock, err)
		}
		build := c.BuildVersion()
		_ = c.Close()
		if build != unattendedNewVersion {
			t.Fatalf("the replacement reports build %q, want %q", build, unattendedNewVersion)
		}

		newPID, ok := unattendedDaemonPID(d.stateDir)
		if !ok {
			t.Fatalf("no daemon.pid after the converge")
		}
		if newPID == oldPID {
			t.Fatalf("daemon.pid still names the original daemon %d", oldPID)
		}
		convergedPID = newPID

		// The linked-path property: the replacement was spawned as the LINK the converge
		// was invoked through, not as the file it resolves to. argv[0] is what `ps`
		// prints as the command, and it is what the daemon's os.Executable() returns on
		// darwin (unresolved), which is what every later shim is spawned from.
		argv, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(newPID)).Output()
		if err != nil {
			t.Fatalf("ps -p %d: %v", newPID, err)
		}
		if got := strings.TrimSpace(string(argv)); !strings.HasPrefix(got, newLink+" ") {
			t.Fatalf("the replacement's argv[0] must be the link %s; ps shows %q (resolving the link would pin every later shim to a directory the next upgrade purges)", newLink, got)
		}

		// The replacement's own environment, as the replacement itself recorded it.
		if got := inodeOf(t, savedPath); got == beforeIno {
			t.Fatalf("daemon.env was not rewritten (inode still %d), so the replacement never started from its own environment", beforeIno)
		}
		after, err := os.ReadFile(savedPath)
		if err != nil {
			t.Fatalf("read %s after the converge: %v", savedPath, err)
		}
		if !strings.Contains(string(after), unattendedProbe+"="+unattendedProbeSaved) {
			t.Fatalf("the replacement must carry %s=%s; its environment is:\n%s",
				unattendedProbe, unattendedProbeSaved, after)
		}
		if strings.Contains(string(after), unattendedProbe+"="+unattendedProbeCaller) {
			t.Fatalf("the replacement inherited the CALLER's %s; its environment is:\n%s", unattendedProbe, after)
		}
		gotPath := savedEnvValue(string(after), "PATH")
		if !strings.Contains(gotPath, unattendedSavedPathMark) {
			t.Fatalf("the replacement's PATH = %q, want it to keep the saved daemon's %q", gotPath, unattendedSavedPathMark)
		}
		if gotPath == unattendedLaunchdPath {
			t.Fatalf("the replacement inherited launchd's PATH %q", gotPath)
		}
	})

	t.Run("case2_a_second_run_against_this_build_touches_nothing", func(t *testing.T) {
		if convergedPID == 0 {
			t.Skip("case 1 did not converge the daemon")
		}
		code, stderr := runSwarmUnattended(t, newBin, d.callerEnv, "daemon", "restart", "--unattended")
		if code != 0 {
			t.Fatalf("exit = %d, want 0; stderr:\n%s", code, stderr)
		}
		if !strings.Contains(stderr, "already runs this build") {
			t.Fatalf("stderr must report idempotence; got:\n%s", stderr)
		}
		pid, ok := unattendedDaemonPID(d.stateDir)
		if !ok || pid != convergedPID {
			t.Fatalf("daemon.pid = %d (present=%v), want the untouched %d", pid, ok, convergedPID)
		}
	})

	t.Run("case3_a_live_session_defers_the_converge", func(t *testing.T) {
		d3 := startUnattendedDaemon(t, oldBin, fakeAgent)
		client, err := protocol.Dial(d3.sock, []string{"attach", "subscribe"})
		if err != nil {
			t.Fatalf("dial %s: %v", d3.sock, err)
		}
		defer func() { _ = client.Close() }()

		script := filepath.Join(t.TempDir(), "idle.txt")
		if err := os.WriteFile(script, []byte("print ready\nidle 300s\n"), 0o600); err != nil {
			t.Fatalf("write script: %v", err)
		}
		id, _, err := client.Launch(protocol.LaunchReq{
			Agent:   "fake",
			Cwd:     t.TempDir(),
			Options: map[string]string{"script": script},
			Env:     []string{"PATH=" + os.Getenv("PATH")},
			Cols:    80,
			Rows:    24,
		})
		if err != nil {
			t.Fatalf("launch a fake session: %v", err)
		}
		// Launch answers with the federation-namespaced id (protocol.NamespacedID:
		// "<endpoint>/<local>"), while the persisted meta.json -- the roster the
		// converge's rule 2 reads -- carries the LOCAL id, so that is the one the
		// deferral line names.
		local := id
		if i := strings.LastIndex(id, "/"); i >= 0 {
			local = id[i+1:]
		}
		waitRosterBusy(t, d3.stateDir, local)

		pidBefore, ok := unattendedDaemonPID(d3.stateDir)
		if !ok {
			t.Fatalf("no daemon.pid under %s", d3.stateDir)
		}
		code, stderr := runSwarmUnattended(t, newBin, d3.callerEnv, "daemon", "restart", "--unattended")
		if code != converge.ExitDeferred {
			t.Fatalf("exit = %d, want %d (deferred); stderr:\n%s", code, converge.ExitDeferred, stderr)
		}
		if !strings.Contains(stderr, "deferred: session") || !strings.Contains(stderr, local) {
			t.Fatalf("stderr must defer naming session %s; got:\n%s", local, stderr)
		}
		if pid, _ := unattendedDaemonPID(d3.stateDir); pid != pidBefore {
			t.Fatalf("a deferred run must touch nothing; daemon.pid moved %d -> %d", pidBefore, pid)
		}

		// End the session and the very next run proceeds.
		if err := client.Kill(id); err != nil {
			t.Fatalf("kill session %s: %v", id, err)
		}
		waitRosterIdle(t, d3.stateDir)
		code, stderr = runSwarmUnattended(t, newBin, d3.callerEnv, "daemon", "restart", "--unattended")
		if code != 0 {
			t.Fatalf("after the session ended, exit = %d, want 0; stderr:\n%s", code, stderr)
		}
		if !strings.Contains(stderr, "converged") {
			t.Fatalf("stderr must report a converge; got:\n%s", stderr)
		}
	})

	t.Run("case4_no_saved_environment_is_refused", func(t *testing.T) {
		d4 := startUnattendedDaemon(t, oldBin, fakeAgent)
		savedPath := daemon.SavedEnvPath(d4.stateDir)
		pidBefore, ok := unattendedDaemonPID(d4.stateDir)
		if !ok {
			t.Fatalf("no daemon.pid under %s", d4.stateDir)
		}

		if err := os.Remove(savedPath); err != nil {
			t.Fatalf("remove %s: %v", savedPath, err)
		}
		code, stderr := runSwarmUnattended(t, newBin, d4.callerEnv, "daemon", "restart", "--unattended")
		if code != converge.ExitRefused {
			t.Fatalf("exit = %d, want %d (refused); stderr:\n%s", code, converge.ExitRefused, stderr)
		}
		if !strings.Contains(stderr, "refused") {
			t.Fatalf("stderr must refuse; got:\n%s", stderr)
		}
		if pid, _ := unattendedDaemonPID(d4.stateDir); pid != pidBefore {
			t.Fatalf("a refused run must leave the daemon running; daemon.pid moved %d -> %d", pidBefore, pid)
		}

		// A daemon.env truncated to zero bytes is refused on the same terms.
		if err := os.WriteFile(savedPath, nil, 0o600); err != nil {
			t.Fatalf("truncate %s: %v", savedPath, err)
		}
		code, stderr = runSwarmUnattended(t, newBin, d4.callerEnv, "daemon", "restart", "--unattended")
		if code != converge.ExitRefused {
			t.Fatalf("exit = %d, want %d (refused); stderr:\n%s", code, converge.ExitRefused, stderr)
		}
		if !strings.Contains(stderr, "empty") {
			t.Fatalf("stderr must name the empty saved environment; got:\n%s", stderr)
		}
		if pid, _ := unattendedDaemonPID(d4.stateDir); pid != pidBefore {
			t.Fatalf("a refused run must leave the daemon running; daemon.pid moved %d -> %d", pidBefore, pid)
		}
	})

	t.Run("case5_no_daemon_converges_and_spawns_nothing", func(t *testing.T) {
		dir, err := os.MkdirTemp("", "swu")
		if err != nil {
			t.Fatalf("state dir: %v", err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		sock := filepath.Join(dir, "d.sock")

		code, stderr := runSwarmUnattended(t, newBin, unattendedCallerEnv(dir, sock),
			"daemon", "restart", "--unattended")
		if code != 0 {
			t.Fatalf("exit = %d, want 0; stderr:\n%s", code, stderr)
		}
		if !strings.Contains(stderr, "no daemon holds the lock") {
			t.Fatalf("stderr must report rule 0; got:\n%s", stderr)
		}
		// A daemon spawned here would be owned by nobody, so it is killed before the
		// failure is reported rather than left holding a PTY until the machine reboots.
		if pid, ok := unattendedDaemonPID(dir); ok {
			_ = syscall.Kill(pid, syscall.SIGTERM)
			t.Fatalf("rule 0 must spawn nothing, but daemon.pid appeared naming %d", pid)
		}
		if c, err := protocol.Dial(sock, nil); err == nil {
			_ = c.Close()
			t.Fatal("rule 0 must spawn nothing, but something answers on the socket")
		}
	})

	t.Run("case6_an_unknown_argument_is_a_usage_error", func(t *testing.T) {
		pidBefore, ok := unattendedDaemonPID(d.stateDir)
		if !ok {
			t.Fatalf("no daemon.pid under %s", d.stateDir)
		}
		code, stderr := runSwarmUnattended(t, newBin, d.callerEnv, "daemon", "restart", "--bogus")
		if code != 2 {
			t.Fatalf("exit = %d, want 2 (usage error); stderr:\n%s", code, stderr)
		}
		if !strings.Contains(stderr, unattendedUsageLine) {
			t.Fatalf("stderr must carry %q; got:\n%s", unattendedUsageLine, stderr)
		}
		if pid, _ := unattendedDaemonPID(d.stateDir); pid != pidBefore {
			t.Fatalf("a usage error must restart nothing; daemon.pid moved %d -> %d", pidBefore, pid)
		}
	})
}
