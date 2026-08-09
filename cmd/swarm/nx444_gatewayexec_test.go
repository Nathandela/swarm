package main

// FAILING-FIRST (TDD RED, GG-5) for agents-tracker-nx44.4(a): the 2026-08-09 outage, both
// halves of it.
//
// WHAT HAPPENED. `swarm remote init` resolved the gateway BESIDE its own executable, which on
// a Homebrew cask means inside the versioned Caskroom directory
// (/usr/local/Caskroom/swarm/0.7.0/swarm-remote), and stamped that path into the LaunchAgent.
// The adjacency rule is right -- the archive is what guarantees the two binaries belong to one
// another -- but the path it produced belongs to ONE RELEASE. `brew upgrade` deleted 0.7.0,
// launchd went on exec'ing it, the job exited EX_CONFIG (78) on every restart until the label
// sat in the penalty box, and the owner's phone was served by nothing. `swarm remote pair` then
// kickstarted that same stale label and printed success.
//
// THE TWO FIXES ARE INDEPENDENT AND BOTH ARE NEEDED:
//
//	PREFER THE VERSION-STABLE NAME when the unit is stamped. A cask links swarm AND
//	swarm-remote into its bin directory (.goreleaser.yaml's binaries:, fenced by
//	release_cask_test.go), and brew re-points those links on every upgrade. So the link is the
//	same program with a name that survives -- and it is preferred ONLY when it provably
//	resolves to the very file adjacency picked, because an unrelated swarm-remote earlier on
//	PATH is the hazard the adjacency rule exists to avoid.
//
//	VERIFY THE STAMPED PATH before activating. The first fix is worthless to every machine
//	already carrying a versioned unit -- including the one this bug was found on -- and no
//	command re-stamped it: init installs, pair only ensures, and pairing is refused while a
//	device is enrolled. So ensureGatewayRunning reads the unit it is about to kickstart, and a
//	program that is no longer executable is re-stamped and reloaded rather than started again.
//
// THE RELOAD IS PART OF THE FIX, not a flourish. launchd holds the plist it was bootstrapped
// with: `bootstrap` on a loaded label is a no-op (supervisor.go ignores its error for exactly
// that reason), so a fresh file alone changes nothing about the running job. Stop-then-Ensure
// is the bootout/bootstrap pair the hot fix performed by hand.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/remote/supervise"
)

// caskLayout builds the install layout a Homebrew cask produces: both binaries staged under a
// versioned directory, both linked from a version-stable bin directory. It returns the bin
// directory's swarm-remote link and the versioned file behind it.
func caskLayout(t *testing.T, version string) (link, staged string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	caskroom := filepath.Join(root, "Caskroom", "swarm", version)
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	staged = writeGatewayExecutable(t, filepath.Join(caskroom, "swarm-remote"))
	writeGatewayExecutable(t, filepath.Join(caskroom, "swarm"))
	for _, name := range []string{"swarm", "swarm-remote"} {
		if err := os.Symlink(filepath.Join(caskroom, name), filepath.Join(bin, name)); err != nil {
			t.Fatalf("link %s: %v", name, err)
		}
	}
	swapExecutablePath(t, filepath.Join(bin, "swarm"))
	return filepath.Join(bin, "swarm-remote"), staged
}

// TestNx444_TheUnitIsStampedWithTheVersionStableGatewayPath is the outage's cause.
func TestNx444_TheUnitIsStampedWithTheVersionStableGatewayPath(t *testing.T) {
	dir := shortStateDir(t)
	t.Setenv(daemon.EnvStateDir, dir)
	link, staged := caskLayout(t, "0.7.0")
	t.Setenv("PATH", filepath.Dir(link))
	f := installFakeSupervisor(t)

	var stdout, stderr bytes.Buffer
	if exit := runRemoteInit(nil, &stdout, &stderr); exit != 0 {
		t.Fatalf("runRemoteInit exit = %d, want 0; stderr=%q", exit, stderr.String())
	}
	if f.spec.Exec == staged {
		t.Fatalf("Spec.Exec = %q, the VERSIONED path. It names one release: the next `brew upgrade` "+
			"deletes that directory, launchd goes on exec'ing it, the job exits EX_CONFIG on every "+
			"restart until the label is in the penalty box, and the owner's phone is served by "+
			"nothing. The cask links %q at the same file and re-points it on every upgrade.", staged, link)
	}
	if f.spec.Exec != link {
		t.Errorf("Spec.Exec = %q, want the version-stable link %q", f.spec.Exec, link)
	}
}

// TestNx444_AStableNameIsPreferredOnlyWhenItIsTheSameProgram is the control on the rule above,
// and it is the reason the preference is a RESOLUTION and not a name match. An unrelated
// swarm-remote on PATH -- an older release, another checkout -- is exactly what the adjacency
// rule was written to refuse, and preferring a stable-looking path over the shipped sibling
// would hand the supervisor a different program under the same name.
func TestNx444_AStableNameIsPreferredOnlyWhenItIsTheSameProgram(t *testing.T) {
	dir := shortStateDir(t)
	t.Setenv(daemon.EnvStateDir, dir)

	caskroom, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	sibling := writeGatewayExecutable(t, filepath.Join(caskroom, "swarm-remote"))
	writeGatewayExecutable(t, filepath.Join(caskroom, "swarm"))
	// The invoked path's directory holds a DIFFERENT swarm-remote: same name, another program.
	bin, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	stranger := writeGatewayExecutable(t, filepath.Join(bin, "swarm-remote"))
	if err := os.Symlink(filepath.Join(caskroom, "swarm"), filepath.Join(bin, "swarm")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	swapExecutablePath(t, filepath.Join(bin, "swarm"))
	t.Setenv("PATH", bin)
	f := installFakeSupervisor(t)

	var stdout, stderr bytes.Buffer
	if exit := runRemoteInit(nil, &stdout, &stderr); exit != 0 {
		t.Fatalf("runRemoteInit exit = %d, want 0; stderr=%q", exit, stderr.String())
	}
	if f.spec.Exec == stranger {
		t.Fatalf("Spec.Exec = %q, a DIFFERENT program that merely shares the gateway's name. The "+
			"stable path is preferred only when it resolves to the binary this install ships (%q)",
			stranger, sibling)
	}
	if f.spec.Exec != sibling {
		t.Errorf("Spec.Exec = %q, want the sibling gateway %q", f.spec.Exec, sibling)
	}
}

// installRealUnit writes an actual unit file into <stateDir>/remote/units naming exec, so
// InstalledExec has something to read. The fake supervisor installed over the seam writes no
// files, which is what makes this the only way to stage the machine the outage left behind.
func installRealUnit(t *testing.T, stateDir, exec string) string {
	t.Helper()
	p, err := supervise.HostPlatform()
	if err != nil {
		t.Skipf("no supervision unit for this platform: %v", err)
	}
	unit, err := supervise.Render(p, supervise.Spec{
		Exec:         exec,
		Owner:        "tester",
		StateDir:     stateDir,
		RemoteSocket: filepath.Join(stateDir, "remote.sock"),
	})
	if err != nil {
		t.Fatalf("render unit: %v", err)
	}
	path, err := supervise.UnitPath(p, stateDir)
	if err != nil {
		t.Fatalf("unit path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir unit dir: %v", err)
	}
	if err := os.WriteFile(path, unit, 0o600); err != nil {
		t.Fatalf("write unit: %v", err)
	}
	return path
}

// TestNx444_PairReStampsAUnitWhoseGatewayIsGone is the machine the fix above cannot reach:
// the unit was stamped by an older release, the binary it names has been deleted, and pairing
// is the one command the owner runs next.
func TestNx444_PairReStampsAUnitWhoseGatewayIsGone(t *testing.T) {
	dir := shortStateDir(t)
	host := newScriptedPairingHost()
	startFakePairingDaemon(t, dir, host)
	// The gateway this install actually ships, resolvable now.
	good := fakeGatewayBinaryOnPath(t)
	swapExecutablePath(t, writeGatewayExecutable(t, filepath.Join(t.TempDir(), "swarm")))
	// The machine as an older release left it: PROVISIONED (the re-stamp needs a remote
	// identity, or the unit it would write could name no socket) and carrying that release's
	// unit. This half runs on the real supervisor, which writes files and nothing else.
	var initOut, initErr bytes.Buffer
	if exit := runRemoteInit(nil, &initOut, &initErr); exit != 0 {
		t.Fatalf("runRemoteInit exit = %d, want 0; stderr=%q", exit, initErr.String())
	}
	// The unit the upgrade orphaned: an absolute path under a Caskroom version that is gone.
	installRealUnit(t, dir, filepath.Join(t.TempDir(), "Caskroom", "swarm", "0.7.0", "swarm-remote"))
	f := installFakeSupervisor(t)

	var stdout, stderr bytes.Buffer
	if exit := runRemotePair(nil, strings.NewReader("y\n"), &stdout, &stderr); exit != 0 {
		t.Fatalf("runRemotePair exit = %d, want 0; stderr=%q", exit, stderr.String())
	}

	if f.count("install") != 1 {
		t.Fatalf("Install called %d times, want 1: the unit names a program that no longer exists, "+
			"so kickstarting it can only put the label back in the penalty box. calls=%v",
			f.count("install"), f.calls)
	}
	if f.spec.Exec != good {
		t.Errorf("re-stamped Spec.Exec = %q, want the gateway this install can resolve, %q", f.spec.Exec, good)
	}
	// THE ORDER IS THE FIX. launchd holds the plist it was bootstrapped with, so the reload is
	// what makes a re-stamp mean anything: install, bootout, bootstrap+kickstart.
	want := []string{"install", "stop", "ensure"}
	if strings.Join(f.calls, ",") != strings.Join(want, ",") {
		t.Errorf("supervisor calls = %v, want %v. A fresh unit file alone changes nothing about a "+
			"job launchd has already loaded -- `bootstrap` on a loaded label is a no-op, which is "+
			"why the hot fix had to bootout first", f.calls, want)
	}
	if !strings.Contains(stderr.String(), "swarm-remote") {
		t.Errorf("nothing on stderr names the program that had gone missing; an owner whose gateway "+
			"was silently re-stamped learns nothing about why it had stopped:\n%s", stderr.String())
	}
}

// TestNx444_PairLeavesAHealthyUnitAlone is the negative half, and it matters as much: Ensure is
// documented never to restart a healthy gateway, and a re-stamp that ran on every pair would
// bootout a live job -- dropping the phone's connection at the exact moment it is being paired.
func TestNx444_PairLeavesAHealthyUnitAlone(t *testing.T) {
	dir := shortStateDir(t)
	host := newScriptedPairingHost()
	startFakePairingDaemon(t, dir, host)
	good := fakeGatewayBinaryOnPath(t)
	swapExecutablePath(t, writeGatewayExecutable(t, filepath.Join(t.TempDir(), "swarm")))
	var initOut, initErr bytes.Buffer
	if exit := runRemoteInit(nil, &initOut, &initErr); exit != 0 {
		t.Fatalf("runRemoteInit exit = %d, want 0; stderr=%q", exit, initErr.String())
	}
	installRealUnit(t, dir, good)
	f := installFakeSupervisor(t)

	var stdout, stderr bytes.Buffer
	if exit := runRemotePair(nil, strings.NewReader("y\n"), &stdout, &stderr); exit != 0 {
		t.Fatalf("runRemotePair exit = %d, want 0; stderr=%q", exit, stderr.String())
	}
	if got := f.count("install"); got != 0 {
		t.Errorf("Install called %d times against a unit naming a program that is right there; "+
			"a re-stamp on every pair is a re-stamp that means nothing", got)
	}
	if got := f.count("stop"); got != 0 {
		t.Errorf("Stop called %d times on the pair path, want 0. Booting out a healthy gateway "+
			"drops the connection of the phone that is being paired", got)
	}
	if got := f.count("ensure"); got != 1 {
		t.Errorf("Ensure called %d times after a successful pair, want 1 (PB-LIFE-2); calls=%v", got, f.calls)
	}
}

// TestNx444_AMachineWithNoUnitIsStillToldToRunInit: the check must not swallow the hint that
// already exists for a machine that never ran `swarm remote init`. There is no stamped path to
// verify there, and re-stamping one would install a unit behind the owner's back on the one
// path that is meant to send them to init.
func TestNx444_AMachineWithNoUnitIsStillToldToRunInit(t *testing.T) {
	dir := shortStateDir(t)
	host := newScriptedPairingHost()
	startFakePairingDaemon(t, dir, host)
	fakeGatewayBinaryOnPath(t)
	f := installFakeSupervisor(t)
	f.ensureErr = supervise.ErrNotInstalled

	var stdout, stderr bytes.Buffer
	if exit := runRemotePair(nil, strings.NewReader("y\n"), &stdout, &stderr); exit != 0 {
		t.Fatalf("runRemotePair exit = %d, want 0; stderr=%q", exit, stderr.String())
	}
	if got := f.count("install"); got != 0 {
		t.Errorf("Install called %d times for a state dir carrying no unit at all; the owner is "+
			"pointed at `swarm remote init`, which is the command that installs one", got)
	}
	if !strings.Contains(strings.ToLower(stdout.String()+stderr.String()), "remote init") {
		t.Errorf("the no-unit hint was lost:\n%s%s", stdout.String(), stderr.String())
	}
}
