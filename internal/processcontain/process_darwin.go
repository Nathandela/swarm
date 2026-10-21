//go:build darwin

package processcontain

import (
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

func Running(pid int) bool {
	proc, alive := observeDarwinProcess(pid)
	return alive && (proc == nil || proc.Proc.P_stat != darwinZombieState)
}

func processAlive(pid int, start int64) bool {
	proc, alive := observeDarwinProcess(pid)
	if !alive {
		return false
	}
	if proc == nil {
		return true
	}
	created := int64(proc.Proc.P_starttime.Sec)*1_000_000 + int64(proc.Proc.P_starttime.Usec)
	return int(proc.Proc.P_pid) == pid && created == start && proc.Proc.P_stat != darwinZombieState
}

func observeDarwinProcess(pid int) (*unix.KinfoProc, bool) {
	raw, err := unix.SysctlRaw("kern.proc.pid", pid)
	if err != nil {
		return nil, processMayExist(err)
	}
	if len(raw) == 0 {
		return nil, false
	}
	proc, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return nil, true
	}
	return proc, int(proc.Proc.P_pid) == pid
}

const darwinZombieState = 5

func processMayExist(err error) bool {
	return !errors.Is(err, syscall.ESRCH) && !errors.Is(err, syscall.ENOENT)
}

func EnableSubreaper() error                        { return ErrUnsupported }
func Descendants(int) ([]Identity, error)           { return nil, ErrUnsupported }
func ContainDescendants(int) error                  { return ErrUnsupported }
func ReapOrphans()                                  {}
func ReapAdoptedZombies(...int) bool                { return false }
func ContainAndReap() error                         { return ErrUnsupported }
func SignalIdentity(Identity, syscall.Signal) error { return ErrUnsupported }

func ChildAttrs() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }
