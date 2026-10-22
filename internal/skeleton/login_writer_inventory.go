package skeleton

import (
	"fmt"
	"github.com/Nathandela/swarm/internal/processcontain"
)

func verifyProviderNativeWritersStopped(homes, sessionIDs []string) error {
	writers, err := processcontain.EnvironmentWriters(homes, sessionIDs)
	if err != nil {
		return fmt.Errorf("login switch: native writer inventory is unavailable: %w", err)
	}
	if len(writers) > 0 {
		return fmt.Errorf("login switch: process %d still owns this provider's native credentials; end it before switching", writers[0].PID)
	}
	return nil
}
