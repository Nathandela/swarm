//go:build linux

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
		cred, e := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if e == nil {
			pid, uid, ok = int(cred.Pid), cred.Uid, cred.Pid > 0
		}
	})
	if err != nil {
		ok = false
	}
	return
}
