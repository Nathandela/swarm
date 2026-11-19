package shim

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// Scheduled launches receive a private pipe as the first exec.Cmd.ExtraFiles
// entry. The daemon alone holds the writer. EOF without the release byte means
// the daemon aborted or died before admitting this particular shim.
const (
	LaunchIdentityGateFlag = "require-launch-identity"
	LaunchIdentityGateFD   = 3
	LaunchIdentityRelease  = byte(1)
)

// AwaitLaunchIdentity consumes the inherited pipe before any worker or backend
// can start. Closing it here also keeps the descriptor out of every child.
func AwaitLaunchIdentity() error {
	pipe := os.NewFile(LaunchIdentityGateFD, "launch identity gate")
	if pipe == nil {
		return errors.New("launch identity gate: missing inherited pipe")
	}
	defer func() { _ = pipe.Close() }()
	info, err := pipe.Stat()
	if err != nil {
		return fmt.Errorf("launch identity gate: %w", err)
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		return errors.New("launch identity gate: expected inherited pipe")
	}
	var release [1]byte
	if _, err := io.ReadFull(pipe, release[:]); err != nil {
		return fmt.Errorf("launch identity gate closed before release: %w", err)
	}
	if release[0] != LaunchIdentityRelease {
		return errors.New("launch identity gate: invalid release")
	}
	return nil
}
