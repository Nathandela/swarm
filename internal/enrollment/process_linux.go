//go:build linux

package enrollment

import (
	"errors"
	"github.com/Nathandela/swarm/internal/processcontain"
	"golang.org/x/sys/unix"
	"net"
	"os"
	"syscall"
)

func detachedAttrs() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
func childAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
}
func nativeAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}
func nativePTYAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true, Setctty: true, Pdeathsig: syscall.SIGKILL}
}
func ownerPeer(conn *net.UnixConn) bool {
	raw, err := conn.SyscallConn()
	if err != nil {
		return false
	}
	var uid uint32
	var credErr error
	if raw.Control(func(fd uintptr) {
		var cred *unix.Ucred
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if credErr == nil {
			uid = cred.Uid
		}
	}) != nil {
		return false
	}
	return credErr == nil && uid == uint32(os.Getuid())
}

func enableSubreaper() error {
	err := processcontain.EnableSubreaper()
	if errors.Is(err, processcontain.ErrUnsupported) {
		return ErrUnsupported
	}
	return err
}
func processRunning(pid int) bool                    { return processcontain.Running(pid) }
func descendants(pid int) ([]ProcessIdentity, error) { return processcontain.Descendants(pid) }
func containDescendants(pid int) error {
	err := processcontain.ContainDescendants(pid)
	if errors.Is(err, processcontain.ErrUnavailable) {
		return ErrUnavailable
	}
	return err
}
func reapOrphans() { processcontain.ReapOrphans() }
func signalIdentity(p ProcessIdentity, signal syscall.Signal) error {
	return processcontain.SignalIdentity(p, signal)
}
