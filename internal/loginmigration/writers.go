package loginmigration

import (
	"errors"
	"github.com/Nathandela/swarm/internal/procstart"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// A native owner home is copied only offline. Unknown process visibility fails
// closed rather than assuming an IDE or detached CLI cannot refresh/write it.
var checkExternalWriters = nativeWriters

func nativeWriters(provider string) error {
	out, e := exec.Command("ps", "-ax", "-o", "uid=,pid=,comm=").Output()
	if e != nil {
		return ErrLiveWriter
	}
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.Fields(line)
		if len(parts) < 3 {
			continue
		}
		uid, e := strconv.Atoi(parts[0])
		if e != nil || uid != os.Getuid() {
			continue
		}
		name := strings.ToLower(strings.Join(parts[2:], " "))
		if strings.Contains(name, "/"+provider) || name == provider {
			return ErrLiveWriter
		}
	}
	return nil
}
func live(pid int, start int64) bool {
	if pid <= 0 {
		return false
	}
	actual, e := procstart.StartTime(pid)
	if e != nil {
		probe := syscall.Kill(pid, 0)
		return !errors.Is(probe, syscall.ESRCH) && !errors.Is(probe, os.ErrNotExist)
	}
	return start <= 0 || actual == start
}
