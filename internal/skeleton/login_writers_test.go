package skeleton

import (
	"os"
	"os/exec"
	"testing"

	"github.com/Nathandela/swarm/internal/procstart"
)

func TestLoginWriterLivenessRequiresTheSameLiveProcess(t *testing.T) {
	start, err := procstart.StartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if !loginProcessAlive(os.Getpid(), start) || !loginProcessAlive(os.Getpid(), 0) {
		t.Fatal("a live writer was reported absent")
	}
	if loginProcessAlive(os.Getpid(), start+1) {
		t.Fatal("a different process incarnation was reported as the writer")
	}
	child := exec.Command("/bin/sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	childStart, err := procstart.StartTime(child.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if !loginProcessAlive(child.Process.Pid, childStart) {
		t.Fatal("a live child writer was reported absent")
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	if loginProcessAlive(child.Process.Pid, childStart) || loginProcessAlive(1<<30, 1) {
		t.Fatal("an absent writer blocked exact continuation")
	}
}
