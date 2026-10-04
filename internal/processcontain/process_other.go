//go:build !linux

package processcontain

import "syscall"

func EnableSubreaper() error                        { return ErrUnsupported }
func Running(int) bool                              { return false }
func Descendants(int) ([]Identity, error)           { return nil, ErrUnsupported }
func ContainDescendants(int) error                  { return ErrUnsupported }
func ReapOrphans()                                  {}
func ContainAndReap() error                         { return ErrUnsupported }
func SignalIdentity(Identity, syscall.Signal) error { return ErrUnsupported }

func ChildAttrs() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }
