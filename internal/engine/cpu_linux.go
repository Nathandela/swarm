//go:build linux

package engine

import (
	"fmt"
	"os"
	"time"
)

// CPU sampling on linux via /proc/<pid>/stat — cgo-free, standard library only.
// We read the process's cumulative utime (field 14) + stime (field 15), in clock
// ticks, twice across cpuSampleWindow and report the rate over the window.
//
// PIN: SampleCPU is the production default the engine installs when
// Config.CPUSampler is nil; idle ~0, busy clearly positive.

// clkTck is the kernel's USER_HZ. It is 100 on every mainstream linux build
// (sysconf(_SC_CLK_TCK)); reading it exactly would require cgo, and the guard
// only needs idle~0 vs busy-positive, which a fixed 100 delivers.
const clkTck = 100

// SampleCPU reports pid's utilization as a percentage of one core.
func SampleCPU(pid int) (float64, error) {
	t0, err := procCPUTicks(pid)
	if err != nil {
		return 0, err
	}
	time.Sleep(cpuSampleWindow)
	t1, err := procCPUTicks(pid)
	if err != nil {
		return 0, err
	}
	return float64(t1-t0) / clkTck / cpuSampleWindow.Seconds() * 100.0, nil
}

// procCPUTicks returns pid's cumulative utime+stime in clock ticks. The syscall
// (reading /proc/<pid>/stat) lives here; the robust field parse — which locates
// the ')' closing the comm field even when comm holds spaces or parentheses — is
// the pure, unit-tested parseLinuxCPUTicks.
func procCPUTicks(pid int) (uint64, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	return parseLinuxCPUTicks(data)
}
