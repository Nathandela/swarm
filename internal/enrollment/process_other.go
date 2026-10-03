//go:build !linux

package enrollment

import (
	"net"
	"syscall"
)

// The descendant containment/peer credential contract is currently characterized
// only on Linux. Other platforms refuse native enrollment before child exec.
func detachedAttrs() *syscall.SysProcAttr                  { return &syscall.SysProcAttr{Setsid: true} }
func childAttrs() *syscall.SysProcAttr                     { return &syscall.SysProcAttr{Setpgid: true} }
func nativeAttrs() *syscall.SysProcAttr                    { return &syscall.SysProcAttr{Setpgid: true} }
func nativePTYAttrs() *syscall.SysProcAttr                 { return &syscall.SysProcAttr{Setsid: true, Setctty: true} }
func enableSubreaper() error                               { return ErrUnsupported }
func ownerPeer(*net.UnixConn) bool                         { return false }
func processRunning(int) bool                              { return false }
func descendants(int) ([]ProcessIdentity, error)           { return nil, ErrUnsupported }
func containDescendants(int) error                         { return ErrUnsupported }
func reapOrphans()                                         {}
func signalIdentity(ProcessIdentity, syscall.Signal) error { return ErrUnsupported }
