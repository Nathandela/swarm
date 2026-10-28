//go:build !linux

package daemon

import (
	"github.com/Nathandela/swarm/internal/shimwire"
	"net"
)

func claudeNativeStopPeer(net.Conn, shimwire.ClaudeStopBinding) bool { return false }
