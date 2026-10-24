package loginworker

import (
	"errors"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/processcontain"
)

// A native child can escape its process group on macOS. The isolated home is
// still its writer identity; contain it before publishing a terminal proof.
func drainNativeWriters(cfg Config) error {
	return drainNativeWritersWithInventory(NativeHome(cfg.StateRoot, cfg.JobID), processcontain.EnvironmentWriters)
}

func drainNativeWritersWithInventory(home string, inventory func([]string, []string) ([]processcontain.Identity, error)) error {
	deadline := time.Now().Add(3 * time.Second)
	for {
		writers, err := inventory([]string{home}, nil)
		if err != nil && !errors.Is(err, processcontain.ErrUnavailable) {
			return ErrUnavailable
		}
		if err == nil {
			if len(writers) == 0 {
				return nil
			}
			for _, writer := range writers {
				if writer.Alive() {
					if err := syscall.Kill(writer.PID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
						return ErrUnavailable
					}
				}
			}
		}
		if time.Now().After(deadline) {
			return ErrUnavailable
		}
		time.Sleep(20 * time.Millisecond)
	}
}
