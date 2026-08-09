package supervise

// FAILING-FIRST (TDD RED, GG-5) for agents-tracker-nx44.4(a): reading back the program an
// INSTALLED unit names, which nothing could do.
//
// THE OUTAGE THIS SERVES (2026-08-09, Nathan's machine). `swarm remote init` stamped the
// gateway's Caskroom path -- /usr/local/Caskroom/swarm/0.7.0/swarm-remote -- into the plist,
// because that is where resolveGatewayBinary found the binary shipped beside its own. A `brew
// upgrade` then deleted the 0.7.0 directory. launchd kept the plist it had been bootstrapped
// with, the job exited EX_CONFIG (78) on every attempt, and the label landed in the penalty
// box. `swarm remote pair` kickstarts that same stale label and reports success, so the phone
// paired and was served by nothing, with no line anywhere naming the cause.
//
// A UNIT IS A CLAIM ABOUT A PATH, AND A PATH CAN STOP BEING TRUE. Nothing re-checked it because
// nothing could READ it: the unit file was write-only from this package's point of view.
// StampedExec is that read, for both platforms, and it is deliberately a parse of the FILE
// rather than a re-render of a Spec -- the file is what the supervisor is holding, including
// the one an operator hand-edited to get their machine back.
//
// INTENDED PRODUCTION (RED -- neither function exists):
//
//	func StampedExec(p Platform, unit []byte) (string, error)
//	func InstalledExec(stateDir string) (string, error)   // ErrNotInstalled when there is none

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNx444_StampedExecReadsBackWhatRenderWrote is the round trip, on both platforms. The
// parse and the template have to agree about ONE path, and the only way to keep them agreeing
// is to derive the expectation from the renderer.
func TestNx444_StampedExecReadsBackWhatRenderWrote(t *testing.T) {
	spec := Spec{
		Exec:     "/usr/local/Caskroom/swarm/0.7.0/swarm-remote",
		Owner:    "nathan",
		StateDir: "/Users/nathan/.swarm",
		LogPath:  "/Users/nathan/.swarm/remote/gateway.log",
	}
	for _, p := range []Platform{PlatformLaunchd, PlatformSystemd} {
		unit, err := Render(p, spec)
		if err != nil {
			t.Fatalf("Render(%s): %v", p, err)
		}
		got, err := StampedExec(p, unit)
		if err != nil {
			t.Fatalf("StampedExec(%s): %v", p, err)
		}
		if got != spec.Exec {
			t.Errorf("StampedExec(%s) = %q, want the program the unit names, %q", p, got, spec.Exec)
		}
	}
}

// TestNx444_StampedExecReadsAHandEditedPlist: the file is the authority, not a Spec this
// process could have re-rendered. The fixture is the hot fix that got the machine back --
// the versioned path replaced by the version-stable link, by hand, in the installed plist.
func TestNx444_StampedExecReadsAHandEditedPlist(t *testing.T) {
	spec := Spec{Exec: "/usr/local/Caskroom/swarm/0.7.0/swarm-remote", Owner: "nathan", StateDir: "/Users/nathan/.swarm"}
	unit, err := Render(PlatformLaunchd, spec)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	const stable = "/usr/local/bin/swarm-remote"
	edited := strings.Replace(string(unit), spec.Exec, stable, 1)
	if edited == string(unit) {
		t.Fatalf("the fixture did not change the plist; it is measuring nothing")
	}
	got, err := StampedExec(PlatformLaunchd, []byte(edited))
	if err != nil {
		t.Fatalf("StampedExec: %v", err)
	}
	if got != stable {
		t.Errorf("StampedExec = %q, want the hand-edited program %q. A re-render would have "+
			"answered %q, which is the path the operator removed from the file", got, stable, spec.Exec)
	}
}

// TestNx444_StampedExecRefusesAUnitThatNamesNoProgram. A unit with no program is not a unit
// with an empty program: answering "" would read to a caller as a path that is merely not
// executable, and the re-stamp it triggers would be right for the wrong reason -- until the
// day the parse breaks on a format change and silently re-stamps every unit on every pair.
func TestNx444_StampedExecRefusesAUnitThatNamesNoProgram(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    Platform
		unit string
	}{
		{"a plist with no ProgramArguments", PlatformLaunchd,
			"<?xml version=\"1.0\"?>\n<plist version=\"1.0\"><dict><key>Label</key><string>com.swarm.remote</string></dict></plist>\n"},
		{"a plist that is not XML at all", PlatformLaunchd, "ExecStart=/usr/local/bin/swarm-remote\n"},
		{"a service file with no ExecStart", PlatformSystemd,
			"[Unit]\nDescription=swarm remote gateway\n\n[Service]\nRestart=on-failure\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := StampedExec(tc.p, []byte(tc.unit)); err == nil {
				t.Errorf("StampedExec = %q with no error; a unit naming no program must be refused, "+
					"never reported as the empty path", got)
			}
		})
	}
}

// TestNx444_InstalledExecReadsTheUnitOnDisk is the seam the CLI actually calls: it asks the
// state dir, not a platform and not a path.
func TestNx444_InstalledExecReadsTheUnitOnDisk(t *testing.T) {
	dir := t.TempDir()
	spec := Spec{Exec: "/opt/swarm/swarm-remote", Owner: "nathan", StateDir: dir, RemoteSocket: filepath.Join(dir, "remote.sock")}

	if _, err := InstalledExec(dir); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("InstalledExec on a state dir with no unit = %v, want ErrNotInstalled: a machine "+
			"that never ran `swarm remote init` has no claim to check", err)
	}

	sup, err := Host(dir)
	if err != nil {
		t.Skipf("no supervision unit for this platform: %v", err)
	}
	if err := sup.Install(spec); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got, err := InstalledExec(dir)
	if err != nil {
		t.Fatalf("InstalledExec: %v", err)
	}
	if got != spec.Exec {
		t.Errorf("InstalledExec = %q, want the program the installed unit names, %q", got, spec.Exec)
	}

	// And it reads the FILE: an edit made outside this process is what the supervisor holds.
	p, err := HostPlatform()
	if err != nil {
		t.Fatalf("HostPlatform: %v", err)
	}
	path, err := UnitPath(p, dir)
	if err != nil {
		t.Fatalf("UnitPath: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read unit: %v", err)
	}
	const moved = "/usr/local/bin/swarm-remote"
	if err := os.WriteFile(path, []byte(strings.Replace(string(raw), spec.Exec, moved, 1)), 0o600); err != nil {
		t.Fatalf("rewrite unit: %v", err)
	}
	if got, err := InstalledExec(dir); err != nil || got != moved {
		t.Errorf("InstalledExec after an edit outside this process = (%q, %v), want %q", got, err, moved)
	}
}
