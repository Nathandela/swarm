//go:build !linux && !darwin

package appserver

import "net"

// Unsupported kernels cannot qualify automatic native execution evidence.
func unixPeer(*net.UnixConn) (int, uint32, bool) { return 0, 0, false }
