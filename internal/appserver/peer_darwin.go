//go:build darwin

package appserver

import (
	"net"

	"golang.org/x/sys/unix"
)

func unixPeer(conn *net.UnixConn) (pid int, uid uint32, ok bool) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return
	}
	err = raw.Control(func(fd uintptr) {
		p, e := unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
		if e != nil {
			return
		}
		cred, e := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if e != nil {
			return
		}
		pid, uid, ok = p, cred.Uid, p > 0
	})
	if err != nil {
		ok = false
	}
	return
}
