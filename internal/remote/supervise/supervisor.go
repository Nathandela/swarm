package supervise

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Supervisor is the seam between the owner-invoked CLI and the host init system.
// ADR-007 D5 forbids the daemon spawning the gateway, so the only caller is cmd/swarm.
// Tests substitute a fake and never touch launchd/systemd.
type Supervisor interface {
	Install(Spec) error // write/refresh the unit; idempotent; files only
	Ensure() error      // make the gateway running now; idempotent, never a restart
	Stop() error        // return the unit to quiescent
}

// ErrNotInstalled is returned by Ensure/Stop when no unit has been installed for this
// state dir. It is a HINT, not a failure: `swarm remote pair` reports it and still exits
// 0. Refusing here, BEFORE shelling out, is also what keeps every test that pairs
// without running `swarm remote init` off the real init system.
var ErrNotInstalled = errors.New("supervise: no gateway unit installed")

// Host returns the Supervisor for the running machine, driving the unit installed under
// stateDir.
func Host(stateDir string) (Supervisor, error) {
	if strings.TrimSpace(stateDir) == "" {
		return nil, errors.New("supervise: empty state dir")
	}
	p, err := HostPlatform()
	if err != nil {
		return nil, err
	}
	path, err := UnitPath(p, stateDir)
	if err != nil {
		return nil, err
	}
	return &hostSupervisor{platform: p, unitPath: path, run: runUnit}, nil
}

// hostSupervisor drives launchctl or systemctl against one installed unit file.
type hostSupervisor struct {
	platform Platform
	unitPath string
	// run is the init-system call. A field, not a direct call, so a test can drive
	// Ensure/Stop's decision logic without a real launchd -- and so the real runner can
	// refuse outright when it finds itself inside a test binary (see runUnit).
	run func(name string, args ...string) ([]byte, error)
}

// Install writes/refreshes the unit. It is files ONLY -- `swarm remote init` runs with
// zero paired devices, where the desired state is quiescent (PB-LIFE-3(a)) and starting
// the gateway would produce exactly the crash loop that requirement prevents.
func (h *hostSupervisor) Install(spec Spec) error {
	unit, err := Render(h.platform, spec)
	if err != nil {
		return err
	}
	dir := filepath.Dir(h.unitPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("supervise: create unit dir: %w", err)
	}
	// Chmod as well as MkdirAll: a re-install must repair permissions, not inherit
	// whatever an earlier umask or an operator left behind (PB-LIFE-4).
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("supervise: unit dir permissions: %w", err)
	}
	if err := os.WriteFile(h.unitPath, unit, 0o600); err != nil {
		return fmt.Errorf("supervise: write unit: %w", err)
	}
	if err := os.Chmod(h.unitPath, 0o600); err != nil {
		return fmt.Errorf("supervise: unit permissions: %w", err)
	}
	return nil
}

// Ensure makes the gateway running now. It never restarts a healthy one: launchctl
// kickstart without -k and systemctl start are both no-ops on a running service.
func (h *hostSupervisor) Ensure() error {
	if err := h.requireUnit(); err != nil {
		return err
	}
	switch h.platform {
	case PlatformLaunchd:
		domain := h.launchdDomain()
		// bootstrap's error is IGNORED, deliberately and entirely. A job already bootstrapped
		// in this domain is the NORMAL case here -- nothing ever boots it out, so every pair
		// after the first one lands on it -- and launchd reports that condition with no
		// stable, documented wording: an already-loaded label commonly comes back as
		// "Bootstrap failed: 5: Input/output error", which is indistinguishable from a real
		// refusal. Sniffing the message for "already" therefore decides the re-pair path on a
		// coin flip, and the losing side never starts the gateway at all (PB-LIFE-3(c)).
		//
		// kickstart is the one call that can tell: it fails if and only if the label is not
		// loaded in the domain. So it, not bootstrap, decides whether Ensure succeeded.
		bootOut, bootErr := h.run("launchctl", "bootstrap", domain, h.unitPath)
		if out, err := h.run("launchctl", "kickstart", domain+"/"+LaunchdLabel); err != nil {
			if bootErr != nil {
				// The label is not loaded AND could not be loaded: bootstrap's output is the
				// only place launchd says why.
				return fmt.Errorf("supervise: launchctl kickstart: %w: %s (bootstrap: %s)", err, out, bootOut)
			}
			return fmt.Errorf("supervise: launchctl kickstart: %w: %s", err, out)
		}
		return nil
	default:
		// enable links the unit into the user manager's search path and makes it come back
		// at login (the systemd half of RunAtLoad); --now starts it if it is not running.
		if out, err := h.run("systemctl", "--user", "enable", "--now", h.unitPath); err != nil {
			return fmt.Errorf("supervise: systemctl enable: %w: %s", err, out)
		}
		return nil
	}
}

// Stop returns the unit to quiescent. A unit that was never loaded is already there, so
// that is a success, not an error.
//
// The teardown alone cannot tell those apart: bootout fails identically whether the label
// was never loaded or the boot-out was REFUSED, and neither launchd nor systemd documents
// stable wording for either. So a second command decides, on its EXIT STATUS -- the same
// shape as Ensure, where kickstart and not bootstrap decides. Reading the outcome off the
// teardown's prose is the defect class Ensure was purged of, and both ways of getting it
// wrong here are real: a spurious warning on every revoke, or -- worse -- swallowing a
// gateway that survived its revoke and will serve the NEXT phone under the OLD epoch,
// since Ensure is a documented no-op against a job that is already running.
func (h *hostSupervisor) Stop() error {
	if err := h.requireUnit(); err != nil {
		return err
	}
	switch h.platform {
	case PlatformLaunchd:
		target := h.launchdDomain() + "/" + LaunchdLabel
		out, err := h.run("launchctl", "bootout", target)
		if err == nil {
			return nil
		}
		// print exits nonzero when the label is not in the domain: nothing left to stop.
		if _, perr := h.run("launchctl", "print", target); perr != nil {
			return nil
		}
		return fmt.Errorf("supervise: launchctl bootout: %w: %s", err, out)
	default:
		out, err := h.run("systemctl", "--user", "disable", "--now", SystemdUnitName)
		if err == nil {
			return nil
		}
		// is-active exits nonzero for anything but a running unit; the gateway surviving
		// its revoke is exactly what the caller needs to hear about.
		if _, aerr := h.run("systemctl", "--user", "is-active", SystemdUnitName); aerr != nil {
			return nil
		}
		return fmt.Errorf("supervise: systemctl disable: %w: %s", err, out)
	}
}

// requireUnit refuses BEFORE any init system is touched when nothing was installed for
// this state dir.
func (h *hostSupervisor) requireUnit() error {
	if _, err := os.Stat(h.unitPath); err != nil {
		return fmt.Errorf("%w at %s", ErrNotInstalled, h.unitPath)
	}
	return nil
}

// launchdDomain is the per-user GUI domain a LaunchAgent lives in. It is the owner's own
// domain: the gateway runs as whoever loads the unit (ADR-007 D4).
func (h *hostSupervisor) launchdDomain() string { return fmt.Sprintf("gui/%d", os.Getuid()) }

// runUnit runs one init-system command, returning its combined output for the error
// message -- launchctl and systemctl both explain themselves there and nowhere else.
//
// It REFUSES inside a test binary. Unit installation is confined to <stateDir>/remote/units
// by construction, but Ensure/Stop are not files: they ask launchd or systemd to load,
// start and stop com.swarm.remote in the developer's OWN session. Until now nothing but
// convention kept a test off that path -- one added Ensure() line in a test that installs a
// real unit was enough. Tests that need to exercise Ensure/Stop substitute
// hostSupervisor.run, which is the only way past this.
func runUnit(name string, args ...string) ([]byte, error) {
	if testing.Testing() {
		return nil, fmt.Errorf("supervise: refusing to run %q inside a test binary; substitute hostSupervisor.run", name)
	}
	out, err := exec.Command(name, args...).CombinedOutput()
	return bytes.TrimSpace(out), err
}
