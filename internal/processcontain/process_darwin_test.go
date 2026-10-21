//go:build darwin

package processcontain

import (
	"os/exec"
	"syscall"
	"testing"

	"github.com/Nathandela/swarm/internal/procstart"
)

func TestDarwinIdentityAliveUsesProcessIncarnation(t *testing.T) {
	start, err := procstart.StartTime(syscall.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	identity := Identity{PID: syscall.Getpid(), StartTime: start}
	if !identity.Alive() {
		t.Fatal("current process identity was not alive")
	}
	identity.StartTime++
	if identity.Alive() {
		t.Fatal("mismatched process incarnation was alive")
	}
}

func TestDarwinIdentityAliveRejectsDeadAndUnknownPIDs(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	start, err := procstart.StartTime(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal(err)
	}
	identity := Identity{PID: cmd.Process.Pid, StartTime: start}
	if !identity.Alive() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal("live child identity was not alive")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			t.Fatal(err)
		}
	}
	if identity.Alive() {
		t.Fatal("dead child was observed as alive")
	}
	if (Identity{PID: 1 << 30, StartTime: 1}).Alive() {
		t.Fatal("unknown process identity was observed as alive")
	}
}

func TestDarwinProcessObservationDistinguishesAbsenceFromUncertainty(t *testing.T) {
	proc, alive := observeDarwinProcess(syscall.Getpid())
	if !alive || proc == nil {
		t.Fatal("nonempty current-process observation was not live")
	}
	proc, alive = observeDarwinProcess(1 << 30)
	if alive || proc != nil {
		t.Fatal("empty unknown-PID observation was treated as live")
	}
	if !processMayExist(syscall.EIO) || !processMayExist(syscall.EPERM) || processMayExist(syscall.ENOENT) || processMayExist(syscall.ESRCH) {
		t.Fatal("indeterminate or absent PID errors were classified unsafely")
	}
}
