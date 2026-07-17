package shim

// E4.4 / invariant S5 — the signal op terminates the whole session process
// group: TERM, then a grace window, then KILL. No process in that group
// survives (descendants included), and the exit outcome is reported.

import (
	"errors"
	"regexp"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/shimwire"
)

var pidRE = map[string]*regexp.Regexp{
	"PARENT_PID": regexp.MustCompile(`PARENT_PID\t(\d+)`),
	"CHILD_PID":  regexp.MustCompile(`CHILD_PID\t(\d+)`),
}

func waitPID(t *testing.T, c *shimClient, label string, timeout time.Duration) int {
	t.Helper()
	out := c.waitOutput(label+"\t", timeout)
	m := pidRE[label].FindSubmatch(out)
	if m == nil {
		t.Fatalf("could not parse %s from output:\n%s", label, out)
	}
	pid, err := strconv.Atoi(string(m[1]))
	if err != nil {
		t.Fatalf("bad %s value %q: %v", label, m[1], err)
	}
	return pid
}

// processGone reports whether pid is gone (kill(pid,0) => ESRCH), polling up to
// timeout to allow for reaping.
func processGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
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
	c.waitOutput("IDLING", 5*time.Second)

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
