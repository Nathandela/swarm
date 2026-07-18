package shim

// E4.4 / invariant S5 — the signal op terminates the whole session process
// group: TERM, then a grace window, then KILL. No process in that group
// survives (descendants included), and the exit outcome is reported.

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/shimwire"
)

// waitPID reads a "<label>\t<pid>" line the agent printed. The label and pid are
// tab-separated in the live stream but space-separated in a snapshot grid (the
// emulator expands the tab), and the marker may land in either depending on the
// attach/first-output race — so match \s+ across the union of both.
func waitPID(t *testing.T, c *shimClient, label string, timeout time.Duration) int {
	t.Helper()
	m := c.waitObservedRE(regexp.MustCompile(label+`\s+(\d+)`), timeout)
	pid, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("bad %s value %q: %v", label, m[1], err)
	}
	return pid
}

// processGone reports whether pid has terminated, polling up to timeout. A pid
// counts as gone when it no longer exists (kill(pid,0) => ESRCH: reaped or never
// existed) OR it is a zombie: a killed process whose exit status has not yet been
// reaped. An orphaned same-group descendant reparents to PID 1 when the group is
// killed; in a container PID namespace PID 1 (the go-test process) does not reap
// orphans, so the killed descendant lingers as an unreaped zombie and kill(pid,0)
// still succeeds. A zombie is terminated, not a survivor, so counting it as gone
// keeps the S5 containment assertion faithful (it never masks a genuinely live
// process — those are in state R/S/D, not Z).
func processGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) || processIsZombie(pid) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// processIsZombie reports whether pid is a Linux zombie (state 'Z' in
// /proc/<pid>/stat). On platforms without /proc (darwin) it returns false —
// there launchd reaps orphaned children promptly, so a killed process reaches
// the kill(pid,0)==ESRCH state without lingering.
func processIsZombie(pid int) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	// Format: "pid (comm) state ...". comm can contain ')' and spaces, so key off
	// the LAST ')': the state letter is two bytes past it.
	i := bytes.LastIndexByte(b, ')')
	return i >= 0 && i+2 < len(b) && b[i+2] == 'Z'
}

// E4.4 / S5 — a signal-ignoring agent AND its same-group child are both killed
// after the grace window: TERM is absorbed, KILL arrives after grace, and
// neither pid survives. The exit is reported as SIGKILL.
func TestSignal_TermGraceKill_WholeGroup(t *testing.T) {
	cfg := helperConfig(t, modeTermStubborn, nil, nil)
	cfg.GraceTimeout = 800 * time.Millisecond
	ch := runShimAsync(cfg)

	c := dialShim(t, cfg.SocketPath)
	c.startReader()
	c.hello(shimwire.Version)
	c.attach()

	parentPID := waitPID(t, c, "PARENT_PID", 5*time.Second)
	childPID := waitPID(t, c, "CHILD_PID", 5*time.Second)
	if parentPID == childPID {
		t.Fatalf("parent and child report the same pid %d", parentPID)
	}

	sent := time.Now()
	c.writeControl(shimwire.Control{Type: shimwire.TypeSignal, Sig: shimwire.SigTerm})
	r := waitRun(t, ch, 10*time.Second)
	elapsed := time.Since(sent)

	// KILL must have come only after the grace window elapsed.
	if elapsed < cfg.GraceTimeout*3/4 {
		t.Errorf("Run returned %s after TERM, sooner than the %s grace — KILL fired early", elapsed, cfg.GraceTimeout)
	}
	if r.err != nil {
		t.Errorf("Run err = %v, want nil (a killed agent is a normal outcome)", r.err)
	}

	// Neither the agent nor its descendant may survive (S5).
	if !processGone(parentPID, 3*time.Second) {
		t.Errorf("agent pid %d still alive after group kill", parentPID)
	}
	if !processGone(childPID, 3*time.Second) {
		t.Errorf("descendant pid %d still alive after group kill (process group not contained)", childPID)
	}

	ei := readExitInfo(t, cfg.SessionDir)
	if ei.ExitSignal != "SIGKILL" {
		t.Errorf("exit_signal = %q, want SIGKILL (agent ignored TERM, so KILL ended it)", ei.ExitSignal)
	}
	if ei.ExitCode != 128+int(syscall.SIGKILL) {
		t.Errorf("exit_code = %d, want %d (128+SIGKILL)", ei.ExitCode, 128+int(syscall.SIGKILL))
	}
}

// E4.4 / S5 — a cooperative agent that dies on TERM exits within grace: no KILL
// is needed, and the exit is reported as SIGTERM.
func TestSignal_CooperativeExitsOnTermWithinGrace(t *testing.T) {
	cfg := helperConfig(t, modeTermCooperate, nil, nil)
	cfg.GraceTimeout = 5 * time.Second
	ch := runShimAsync(cfg)

	c := dialShim(t, cfg.SocketPath)
	c.startReader()
	c.hello(shimwire.Version)
	c.attach()
	parentPID := waitPID(t, c, "PARENT_PID", 5*time.Second)

	sent := time.Now()
	c.writeControl(shimwire.Control{Type: shimwire.TypeSignal, Sig: shimwire.SigTerm})
	r := waitRun(t, ch, 10*time.Second)
	elapsed := time.Since(sent)

	if elapsed >= cfg.GraceTimeout {
		t.Errorf("cooperative agent took %s to exit, >= grace %s — it did not die on TERM", elapsed, cfg.GraceTimeout)
	}
	if r.err != nil {
		t.Errorf("Run err = %v, want nil", r.err)
	}
	if !processGone(parentPID, 3*time.Second) {
		t.Errorf("agent pid %d still alive after cooperative TERM", parentPID)
	}
	ei := readExitInfo(t, cfg.SessionDir)
	if ei.ExitSignal != "SIGTERM" {
		t.Errorf("exit_signal = %q, want SIGTERM (agent died on TERM before grace)", ei.ExitSignal)
	}
	if ei.ExitCode != 128+int(syscall.SIGTERM) {
		t.Errorf("exit_code = %d, want %d (128+SIGTERM)", ei.ExitCode, 128+int(syscall.SIGTERM))
	}
}

// E4.4 — signal "kill" terminates immediately, without waiting out the grace
// window.
func TestSignal_KillIsImmediate(t *testing.T) {
	cfg := helperConfig(t, modeIdle, nil, nil)
	cfg.GraceTimeout = 30 * time.Second // long: proves kill does not wait it out
	ch := runShimAsync(cfg)

	c := dialShim(t, cfg.SocketPath)
	c.startReader()
	c.hello(shimwire.Version)
	c.attach()
	c.waitObserved("IDLING", 5*time.Second)

	sent := time.Now()
	c.writeControl(shimwire.Control{Type: shimwire.TypeSignal, Sig: shimwire.SigKill})
	r := waitRun(t, ch, 10*time.Second)
	if elapsed := time.Since(sent); elapsed >= cfg.GraceTimeout {
		t.Errorf("kill took %s, must not wait out the %s grace", elapsed, cfg.GraceTimeout)
	}
	if r.err != nil {
		t.Errorf("Run err = %v, want nil", r.err)
	}
	ei := readExitInfo(t, cfg.SessionDir)
	if ei.ExitSignal != "SIGKILL" {
		t.Errorf("exit_signal = %q, want SIGKILL", ei.ExitSignal)
	}
}
