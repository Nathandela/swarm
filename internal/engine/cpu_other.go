//go:build !darwin && !linux

package engine

import (
	"fmt"
	"runtime"
)

// SampleCPU has no real implementation off darwin/linux; it reports an error so
// the engine (which treats a sample error as "activity unknown") never claims a
// process is idle on an unsupported platform. The production daemon runs on linux
// or darwin (E10.6); this keeps the module buildable everywhere.
func SampleCPU(pid int) (float64, error) {
	return 0, fmt.Errorf("engine: SampleCPU unsupported on %s", runtime.GOOS)
}
