//go:build linux

package daemon

import (
	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/Nathandela/swarm/internal/shimwire"
	"golang.org/x/sys/unix"
	"net"
	"os"
)

func claudeNativeStopPeer(conn net.Conn, expected shimwire.ClaudeStopBinding) bool {
	c, ok := conn.(*net.UnixConn)
	if !ok {
		return false
	}
	raw, err := c.SyscallConn()
	if err != nil {
		return false
	}
	match := false
	err = raw.Control(func(fd uintptr) {
		cred, e := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		match = e == nil && cred != nil && int(cred.Pid) == expected.ShimPID && cred.Uid == uint32(os.Getuid())
	})
	if err != nil || !match {
		return false
	}
	birth, err := procstart.StartTime(expected.ShimPID)
	return err == nil && birth == expected.ShimStartTime
}
