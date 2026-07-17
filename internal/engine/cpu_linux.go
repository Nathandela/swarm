//go:build linux

package engine

import (
	"fmt"
	"os"
	"strconv"
	"strings"
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

// procCPUTicks returns pid's cumulative utime+stime in clock ticks. It parses
// /proc/<pid>/stat by locating the ')' that closes the comm field (which may
// itself contain spaces or parentheses) and counting fields from there — the
// canonical robust parse, matching internal/daemon's start-time reader.
func procCPUTicks(pid int) (uint64, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	s := string(data)
	rparen := strings.LastIndexByte(s, ')')
	if rparen < 0 || rparen+2 > len(s) {
		return 0, fmt.Errorf("engine: malformed /proc/%d/stat", pid)
	}
	// After "(comm) " the fields resume at state (field 3) as index 0, so field N
	// is index N-3: utime (14) -> 11, stime (15) -> 12.
	fields := strings.Fields(s[rparen+1:])
	const utimeIdx, stimeIdx = 11, 12
	if len(fields) <= stimeIdx {
		return 0, fmt.Errorf("engine: /proc/%d/stat has too few fields", pid)
	}
	utime, err := strconv.ParseUint(fields[utimeIdx], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("engine: parse utime: %w", err)
	}
	stime, err := strconv.ParseUint(fields[stimeIdx], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("engine: parse stime: %w", err)
	}
	return utime + stime, nil
}
