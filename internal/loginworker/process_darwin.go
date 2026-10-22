//go:build darwin

package loginworker

import (
	"errors"
	"net"
	"os"
	"syscall"

	"github.com/Nathandela/swarm/internal/processcontain"

	"golang.org/x/sys/unix"
)

func detachedAttrs() *syscall.SysProcAttr  { return &syscall.SysProcAttr{Setsid: true} }
func childAttrs() *syscall.SysProcAttr     { return &syscall.SysProcAttr{Setpgid: true} }
func nativeAttrs() *syscall.SysProcAttr    { return processcontain.ChildAttrs() }
func nativePTYAttrs() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true, Setctty: true} }

// Darwin uses exact process groups and the private-home writer scan before ready.
func enableSubreaper() error       { return nil }
func containDescendants(int) error { return nil }
func reapOrphans()                 {}

func signalIdentity(p ProcessIdentity, sig syscall.Signal) error {
	if !p.Alive() {
		return nil
	}
	if err := syscall.Kill(-p.PID, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

func ownerPeer(c *net.UnixConn) bool {
	raw, err := c.SyscallConn()
	if err != nil {
		return false
	}
	var uid uint32
	var credErr error
	err = raw.Control(func(fd uintptr) {
		var cred *unix.Xucred
		cred, credErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if credErr == nil {
			uid = cred.Uid
		}
	})
	return err == nil && credErr == nil && uid == uint32(os.Getuid())
}
