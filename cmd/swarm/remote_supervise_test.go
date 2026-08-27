package main

// FAILING-FIRST tests for slice S4 — PB-LIFE-2: a successful `swarm remote pair` ensures
// the gateway is running, with no manual restart, and the DAEMON never spawns it.
//
// WHY THE CLI IS THE HOOK. ADR-007 D5 requires cmd/swarm-remote to run "under an external
// supervisor ... never spawned by the daemon" -- the daemon owns PTYs and spawns agents,
// and the gateway is the one component parsing attacker-influenced relay bytes, so the
// daemon must not be its parent. The owner-invoked CLI is therefore the only correct
// place to install the unit (`swarm remote init`) and to activate it (`swarm remote
// pair`), which is exactly what PB-LIFE-2 names.
//
// INTENDED PRODUCTION (RED — the seam does not exist yet; GREEN implements it):
//
//	// newGatewaySupervisor is the CLI's hook into gateway supervision. A var so tests
//	// substitute a fake and NEVER touch the real launchd/systemd.
//	var newGatewaySupervisor = func(stateDir string) (supervise.Supervisor, error) {
//		return supervise.Host(stateDir)
//	}
//
//	// runRemoteInit, after provisioning <stateDir>/remote/: build the unit Spec and
//	// Install it. On the zero-device machine that is the whole of it -- the desired
//	// state there is quiescent (PB-LIFE-3(a)) -- and it converges on whatever the
//	// device count implies, which on a machine that already has its one device means
//	// Ensure (see TestRemoteInit_EnsuresGatewayWhenADeviceIsAlreadyPaired).
//	//
//	// runRemotePair, after res.Paired: call Ensure(). A supervise.ErrNotInstalled is a
//	// HINT on stderr, not a failure -- the pairing itself succeeded and is durable.
//
// HOW THESE TESTS STAY OFF THE REAL SYSTEM. Two independent guards:
//  1. Every test here installs a fake supervisor over newGatewaySupervisor.
//  2. The real supervisor is constructed from the STATE DIR, and it installs units into
//     <stateDir>/remote/units -- never ~/Library/LaunchAgents, never ~/.config/systemd.
//     Its Ensure/Stop refuse with supervise.ErrNotInstalled before shelling out when no
//     unit exists there, so the pre-existing pair tests in this package (which never run
//     `remote init`) cannot reach launchctl either.

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/supervise"
)

// fakeGatewaySupervisor records the calls `swarm remote init` and `swarm remote pair`
// make through the seam, plus the Spec they installed.
type fakeGatewaySupervisor struct {
	calls     []string
	spec      supervise.Spec
	stateDir  string
	ensureErr error
}

func (f *fakeGatewaySupervisor) Install(s supervise.Spec) error {
	f.calls = append(f.calls, "install")
	f.spec = s
	return nil
}

func (f *fakeGatewaySupervisor) Ensure() error {
	f.calls = append(f.calls, "ensure")
	return f.ensureErr
}

func (f *fakeGatewaySupervisor) Stop() error {
	f.calls = append(f.calls, "stop")
	return nil
}

func (f *fakeGatewaySupervisor) Restart() error {
	f.calls = append(f.calls, "restart")
	return nil
}

func (f *fakeGatewaySupervisor) count(name string) int {
	n := 0
	for _, c := range f.calls {
		if c == name {
			n++
		}
	}
	return n
}

// installFakeSupervisor swaps the CLI's supervisor factory for the duration of a test.
func installFakeSupervisor(t *testing.T) *fakeGatewaySupervisor {
	t.Helper()
	f := &fakeGatewaySupervisor{}
	prev := newGatewaySupervisor
	newGatewaySupervisor = func(stateDir string) (supervise.Supervisor, error) {
		f.stateDir = stateDir
		return f, nil
	}
	t.Cleanup(func() { newGatewaySupervisor = prev })
	return f
}

// fakeGatewayBinaryOnPath puts an executable named swarm-remote on PATH so the CLI can
// resolve an ABSOLUTE Exec for the unit without the host having swarm installed.
func fakeGatewayBinaryOnPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "swarm-remote")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake swarm-remote: %v", err)
	}
	t.Setenv("PATH", dir)
	return bin
}

// TestRemoteInit_InstallsGatewayUnitButDoesNotActivateIt pins PB-LIFE-2's install half.
// `swarm remote init` provisions <stateDir>/remote/ and installs the supervision unit in
// the same step -- an owner who ran init has a machine that will start its gateway when a
// device is paired. It must NOT activate anything: with zero paired devices the desired
// state is quiescent (PB-LIFE-3(a)), and starting the gateway here would produce exactly
// the crash loop that requirement exists to prevent.
func TestRemoteInit_InstallsGatewayUnitButDoesNotActivateIt(t *testing.T) {
	dir := shortStateDir(t)
	t.Setenv(daemon.EnvStateDir, dir)
	bin := fakeGatewayBinaryOnPath(t)
	f := installFakeSupervisor(t)

	var stdout, stderr bytes.Buffer
	if exit := runRemoteInit(nil, &stdout, &stderr); exit != 0 {
		t.Fatalf("runRemoteInit exit = %d, want 0; stderr=%q", exit, stderr.String())
	}

	if f.count("install") != 1 {
		t.Fatalf("Install called %d times, want 1; calls=%v", f.count("install"), f.calls)
	}
	if f.count("ensure") != 0 {
		t.Errorf("Ensure called %d times during init; with zero paired devices the unit stays quiescent", f.count("ensure"))
	}
	if f.stateDir != dir {
		t.Errorf("supervisor built for state dir %q, want %q", f.stateDir, dir)
	}

	spec := f.spec
	if spec.Exec != bin {
		t.Errorf("Spec.Exec = %q, want the resolved absolute gateway binary %q", spec.Exec, bin)
	}
	if !filepath.IsAbs(spec.Exec) {
		t.Errorf("Spec.Exec = %q is not absolute; a supervisor resolves a relative path against a cwd nobody controls", spec.Exec)
	}
	if spec.StateDir != dir {
		t.Errorf("Spec.StateDir = %q, want %q", spec.StateDir, dir)
	}
	if spec.Owner == "" || spec.Owner == "root" {
		t.Errorf("Spec.Owner = %q; the gateway runs as the owner and never as root (ADR-007 D4)", spec.Owner)
	}
	if spec.Backoff < 0 {
		t.Errorf("Spec.Backoff = %v, want zero (DefaultBackoff) or positive", spec.Backoff)
	}
	// ADR-007 D4: the gateway dials the dedicated remote-tier UDS, not the owner socket.
	if want := filepath.Join(dir, "remote.sock"); spec.RemoteSocket != want {
		t.Errorf("Spec.RemoteSocket = %q, want the default remote-tier socket %q", spec.RemoteSocket, want)
	}
	// The unit must not carry a private path from the CLI's own environment.
	if strings.Contains(spec.LogPath, "..") {
		t.Errorf("Spec.LogPath = %q escapes its directory", spec.LogPath)
	}
}

// writeGatewayExecutable writes an executable file at path (creating its directory).
func writeGatewayExecutable(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// swapExecutablePath points the CLI's view of its own binary at path for one test.
func swapExecutablePath(t *testing.T, path string) {
	t.Helper()
	prev := osExecutable
	osExecutable = func() (string, error) { return path, nil }
	t.Cleanup(func() { osExecutable = prev })
}

// TestRemoteInit_PrefersTheGatewayBesideItsOwnExecutable is PB-LIFE-1 on a real install
// layout. swarm and swarm-remote ship in ONE archive, so the gateway is a SIBLING of the
// running binary -- and adjacency is the relationship the archive actually guarantees.
// PATH is not: an installer that links one binary and not the other, or an operator with
// an older swarm-remote earlier on PATH, both produce a unit pointing at the wrong program
// (or at nothing) while the right one sits next door.
//
// The layout below is Homebrew's: the binary is reached through a symlink into the
// Caskroom, so the sibling is only visible after the link is resolved.
func TestRemoteInit_PrefersTheGatewayBesideItsOwnExecutable(t *testing.T) {
	dir := shortStateDir(t)
	t.Setenv(daemon.EnvStateDir, dir)

	// EvalSymlinks on the fixture too: /var is itself a symlink to /private/var on macOS,
	// and the unit records the fully resolved path.
	caskroom, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	sibling := writeGatewayExecutable(t, filepath.Join(caskroom, "swarm-remote"))
	writeGatewayExecutable(t, filepath.Join(caskroom, "swarm"))
	link := filepath.Join(t.TempDir(), "swarm")
	if err := os.Symlink(filepath.Join(caskroom, "swarm"), link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	swapExecutablePath(t, link)

	// A DIFFERENT swarm-remote on PATH. The one shipped in this install's own archive wins.
	onPath := fakeGatewayBinaryOnPath(t)
	f := installFakeSupervisor(t)

	var stdout, stderr bytes.Buffer
	if exit := runRemoteInit(nil, &stdout, &stderr); exit != 0 {
		t.Fatalf("runRemoteInit exit = %d, want 0; stderr=%q", exit, stderr.String())
	}
	if f.spec.Exec == onPath {
		t.Fatalf("Spec.Exec = %q, the PATH copy; the gateway shipped BESIDE this binary (%q) is "+
			"the one this install guarantees", f.spec.Exec, sibling)
	}
	if f.spec.Exec != sibling {
		t.Errorf("Spec.Exec = %q, want the sibling gateway %q", f.spec.Exec, sibling)
	}
}

// TestRemoteInit_FallsBackToPATHWithNoSibling: a source checkout has `go install`ed
// binaries on PATH and no archive layout at all, so PATH stays the fallback.
func TestRemoteInit_FallsBackToPATHWithNoSibling(t *testing.T) {
	dir := shortStateDir(t)
	t.Setenv(daemon.EnvStateDir, dir)
	swapExecutablePath(t, writeGatewayExecutable(t, filepath.Join(t.TempDir(), "swarm")))
	onPath := fakeGatewayBinaryOnPath(t)
	f := installFakeSupervisor(t)

	var stdout, stderr bytes.Buffer
	if exit := runRemoteInit(nil, &stdout, &stderr); exit != 0 {
		t.Fatalf("runRemoteInit exit = %d, want 0; stderr=%q", exit, stderr.String())
	}
	if f.spec.Exec != onPath {
		t.Errorf("Spec.Exec = %q, want the PATH gateway %q", f.spec.Exec, onPath)
	}
}

// TestRemoteInit_EnsuresGatewayWhenADeviceIsAlreadyPaired closes PB-LIFE-2's operability
// hole. Ensure had exactly ONE call site -- `swarm remote pair` -- and pairing is refused
// while a device is already enrolled (single-device v1), so an owner whose Ensure failed
// (a transient launchctl refusal, a machine that upgraded from a build with no unit) had
// NO supported command that starts the gateway: init only wrote files. `swarm remote init`
// is idempotent and always available, so it is the right place to converge the machine on
// the state its device count already implies.
func TestRemoteInit_EnsuresGatewayWhenADeviceIsAlreadyPaired(t *testing.T) {
	dir := shortStateDir(t)
	t.Setenv(daemon.EnvStateDir, dir)
	seedDevice(t, dir, "Nathan's iPhone", device.CapFull)
	fakeGatewayBinaryOnPath(t)
	f := installFakeSupervisor(t)

	var stdout, stderr bytes.Buffer
	if exit := runRemoteInit(nil, &stdout, &stderr); exit != 0 {
		t.Fatalf("runRemoteInit exit = %d, want 0; stderr=%q", exit, stderr.String())
	}
	if f.count("install") != 1 {
		t.Errorf("Install called %d times, want 1; calls=%v", f.count("install"), f.calls)
	}
	if got := f.count("ensure"); got != 1 {
		t.Errorf("Ensure called %d times by `swarm remote init` with one paired device, want 1 "+
			"(supervise.Desired(1) is StateActive); calls=%v", got, f.calls)
	}
}

// TestRemoteRevoke_StopsTheGateway is the freshness half of PB-LIFE-3(c). The revoked
// device's gateway is expected to notice and self-exit, but it only does so if it can read
// the registry -- deviceRevoked() returns false on a read error -- and a surviving
// pre-revoke process makes the NEXT pairing's Ensure a documented no-op, so the new phone
// would be served by a gateway still holding the old epoch. Revoke is the moment the owner
// is present and the desired state is known, so it is where the process is ended.
func TestRemoteRevoke_StopsTheGateway(t *testing.T) {
	dir := shortStateDir(t)
	id := seedDevice(t, dir, "Nathan's iPhone", device.CapFull)
	startCLIDaemon(t, dir)
	f := installFakeSupervisor(t)

	var stdout, stderr bytes.Buffer
	if exit := runRemote([]string{"revoke", id}, &stdout, &stderr); exit != 0 {
		t.Fatalf("runRemote([revoke %s]) exit = %d, want 0; stderr=%q", id, exit, stderr.String())
	}
	if got := f.count("stop"); got != 1 {
		t.Errorf("Stop called %d times after revoking the only paired device, want 1; calls=%v", got, f.calls)
	}
	if got := f.count("ensure"); got != 0 {
		t.Errorf("Ensure called %d times on the revoke path, want 0; zero devices is quiescent", got)
	}
}

// TestRemoteInit_HonorsConfiguredRemoteSocket: when the owner has configured a remote-tier
// socket, the unit must carry THAT path, or the supervised gateway dials somewhere the
// daemon is not listening and every command fails after a successful pair.
func TestRemoteInit_HonorsConfiguredRemoteSocket(t *testing.T) {
	dir := shortStateDir(t)
	sock := filepath.Join(dir, "custom-remote.sock")
	t.Setenv(daemon.EnvStateDir, dir)
	t.Setenv(daemon.EnvRemoteSocket, sock)
	fakeGatewayBinaryOnPath(t)
	f := installFakeSupervisor(t)

	var stdout, stderr bytes.Buffer
	if exit := runRemoteInit(nil, &stdout, &stderr); exit != 0 {
		t.Fatalf("runRemoteInit exit = %d, want 0; stderr=%q", exit, stderr.String())
	}
	if f.spec.RemoteSocket != sock {
		t.Errorf("Spec.RemoteSocket = %q, want the configured %q", f.spec.RemoteSocket, sock)
	}
}

// TestRemotePair_SuccessEnsuresGatewayRunning is PB-LIFE-2 itself: after the operator
// approves a pairing, the gateway is running. No second command, no reboot, no "now start
// swarm-remote by hand" -- the phone that just paired has a gateway to talk to, and the
// epoch grant delivery that runs at gateway start (cmd/swarm-remote's deliverEpochGrant)
// is what makes the pairing usable at all.
//
// It reuses this package's scripted fake owner daemon (remote_pair_test.go), so the
// pairing round trip is the real wire contract.
func TestRemotePair_SuccessEnsuresGatewayRunning(t *testing.T) {
	dir := shortStateDir(t)
	host := newScriptedPairingHost()
	startFakePairingDaemon(t, dir, host)
	f := installFakeSupervisor(t)

	var stdout, stderr bytes.Buffer
	exit := runRemotePair(nil, strings.NewReader("y\n"), &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("runRemotePair exit = %d, want 0; stderr=%q", exit, stderr.String())
	}
	if !strings.Contains(stdout.String(), "paired") {
		t.Fatalf("pair output missing the terminal paired result:\n%s", stdout.String())
	}
	if got := f.count("ensure"); got != 1 {
		t.Errorf("Ensure called %d times after a successful pair, want exactly 1 (PB-LIFE-2: no manual restart); calls=%v", got, f.calls)
	}
	if got := f.count("stop"); got != 0 {
		t.Errorf("Stop called %d times on the success path; a fresh pairing must leave the gateway active", got)
	}
	if f.stateDir != dir {
		t.Errorf("supervisor built for state dir %q, want %q", f.stateDir, dir)
	}
}

// TestRemotePair_DeclinedDoesNotActivateGateway: a denied pairing enrolls nothing, so the
// device count stays at zero and starting a gateway would immediately hit the
// zero-device quiescent exit. The seam must not be touched at all.
func TestRemotePair_DeclinedDoesNotActivateGateway(t *testing.T) {
	dir := shortStateDir(t)
	host := newScriptedPairingHost()
	startFakePairingDaemon(t, dir, host)
	f := installFakeSupervisor(t)

	var stdout, stderr bytes.Buffer
	if exit := runRemotePair(nil, strings.NewReader("n\n"), &stdout, &stderr); exit == 0 {
		t.Fatalf("runRemotePair(deny) exit = 0, want nonzero")
	}
	if got := f.count("ensure"); got != 0 {
		t.Errorf("Ensure called %d times after a DECLINED pairing, want 0; calls=%v", got, f.calls)
	}
}

// TestRemotePair_UnitNotInstalledIsAHintNotAFailure: pairing is durable and already
// committed by the time the gateway is started, so a supervisor that has no unit yet (the
// owner never ran `swarm remote init`, or ran an older build) must not turn a successful
// pairing into a nonzero exit. It must say so on stderr instead -- a silent no-op here is
// a phone that pairs and then never receives anything, with nothing on screen explaining
// why.
func TestRemotePair_UnitNotInstalledIsAHintNotAFailure(t *testing.T) {
	dir := shortStateDir(t)
	host := newScriptedPairingHost()
	startFakePairingDaemon(t, dir, host)
	f := installFakeSupervisor(t)
	f.ensureErr = supervise.ErrNotInstalled

	var stdout, stderr bytes.Buffer
	exit := runRemotePair(nil, strings.NewReader("y\n"), &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("runRemotePair exit = %d, want 0: the pairing itself succeeded and is durable; stderr=%q", exit, stderr.String())
	}
	if !strings.Contains(stdout.String(), "paired") {
		t.Errorf("pair output missing the terminal paired result:\n%s", stdout.String())
	}
	combined := strings.ToLower(stdout.String() + stderr.String())
	if !strings.Contains(combined, "remote init") {
		t.Errorf("no unit installed, but the output does not point at `swarm remote init`:\n%s", combined)
	}
}

// TestRemotePair_SupervisorFailureIsReportedNotSwallowed: any OTHER Ensure error is a real
// operational problem (a broken unit, a launchctl refusal). The pairing still stands, but
// the operator must be told -- otherwise the only symptom is a phone that pairs and goes
// quiet.
func TestRemotePair_SupervisorFailureIsReportedNotSwallowed(t *testing.T) {
	dir := shortStateDir(t)
	host := newScriptedPairingHost()
	startFakePairingDaemon(t, dir, host)
	f := installFakeSupervisor(t)
	f.ensureErr = errors.New("launchctl: Bootstrap failed: 5: Input/output error")

	var stdout, stderr bytes.Buffer
	runRemotePair(nil, strings.NewReader("y\n"), &stdout, &stderr)
	if !strings.Contains(stderr.String(), "Bootstrap failed") {
		t.Errorf("supervisor failure was swallowed; stderr=%q", stderr.String())
	}
}

// TestDaemonNeverSpawnsTheGateway is ADR-007 D5's structural half, checked at the source
// level because it is an ARCHITECTURE property, not a runtime one: only the owner-invoked
// CLI (and the gateway binary itself, for the quiescent exit code) may depend on the
// supervision package, and the daemon-side packages may not reach for the gateway binary
// at all.
//
// The positive half is what makes this RED today: cmd/swarm does not yet import the
// supervision package, so nothing installs or activates anything.
func TestDaemonNeverSpawnsTheGateway(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	const supervisePkg = `"github.com/Nathandela/swarm/internal/remote/supervise"`

	allowed := map[string]bool{
		"cmd/swarm":                 true,
		"cmd/swarm-remote":          true,
		"internal/remote/supervise": true,
	}

	importers := map[string]bool{}
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// .claude holds the sibling git worktrees this repo is developed in
			// (.claude/worktrees/*), which are FULL checkouts and are visible from the main
			// one. Walking them would record their cmd/swarm as another importer and fail
			// this test for a package that is this same package, in another working copy.
			if name := d.Name(); name == ".git" || name == ".claude" || name == "dist" || name == "docs" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		rel, rerr := filepath.Rel(root, filepath.Dir(path))
		if rerr != nil {
			return rerr
		}
		pkg := filepath.ToSlash(rel)
		if strings.Contains(string(b), supervisePkg) {
			importers[pkg] = true
		}
		// The daemon must never reach for the gateway binary itself.
		if strings.HasPrefix(pkg, "internal/daemon") || strings.HasPrefix(pkg, "internal/skeleton") || strings.HasPrefix(pkg, "internal/remotegw") {
			if strings.Contains(string(b), "swarm-remote") && strings.Contains(string(b), "exec.Command") {
				t.Errorf("%s both names the gateway binary and calls exec.Command; ADR-007 D5 forbids the daemon spawning the gateway", filepath.Join(pkg, d.Name()))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}

	if !importers["cmd/swarm"] {
		t.Errorf("cmd/swarm does not import %s; nothing installs or activates the gateway unit (PB-LIFE-2)", supervisePkg)
	}
	for pkg := range importers {
		if !allowed[pkg] {
			t.Errorf("package %s imports %s; only the owner-invoked CLI and the gateway binary may (ADR-007 D5)", pkg, supervisePkg)
		}
	}
}
