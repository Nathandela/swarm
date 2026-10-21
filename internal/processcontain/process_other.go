//go:build !linux && !darwin

package processcontain

import "syscall"

func EnableSubreaper() error                        { return ErrUnsupported }
func Running(int) bool                              { return true }
func processAlive(int, int64) bool                  { return true }
func Descendants(int) ([]Identity, error)           { return nil, ErrUnsupported }
func ContainDescendants(int) error                  { return ErrUnsupported }
func ReapOrphans()                                  {}
func ReapAdoptedZombies(...int) bool                { return false }
func ContainAndReap() error                         { return ErrUnsupported }
func SignalIdentity(Identity, syscall.Signal) error { return ErrUnsupported }

func ChildAttrs() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }
