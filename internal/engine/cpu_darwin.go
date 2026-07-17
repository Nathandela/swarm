//go:build darwin

package engine

import (
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// CPU sampling on darwin via the proc_info syscall (SYS_proc_info, the mechanism
// under libproc's proc_pidinfo) with the PROC_PIDTASKINFO flavor — cgo-free, using
// only golang.org/x/sys/unix. We read the task's cumulative user+system CPU time
// twice across cpuSampleWindow and report the rate. proc_taskinfo's cumulative
// counters advance more slowly than a scheduler-derived %cpu for a never-yielding
// spin loop, but they advance monotonically with real work, so an idle process
// reads ~0 and a busy one reads clearly positive — all the staleness guard needs.
//
// The syscall lives here; the fixed byte layout of the returned proc_taskinfo is
// parsed by the pure, unit-tested parseDarwinCPUNanos (cpuparse.go), so the exact
// offsets are verified in normal CI on every runner.
//
// PIN: SampleCPU is the production default the engine installs when
// Config.CPUSampler is nil; idle ~0, busy clearly positive.

// procInfoCallPIDInfo is the proc_info call number for proc_pidinfo.
const procInfoCallPIDInfo = 2

// procPIDTaskInfo is the PROC_PIDTASKINFO flavor.
const procPIDTaskInfo = 4

// SampleCPU reports pid's utilization as a percentage of one core.
func SampleCPU(pid int) (float64, error) {
	t0, err := procCPUNanos(pid)
	if err != nil {
		return 0, err
	}
	time.Sleep(cpuSampleWindow)
	t1, err := procCPUNanos(pid)
	if err != nil {
		return 0, err
	}
	return float64(t1-t0) / float64(cpuSampleWindow.Nanoseconds()) * 100.0, nil
}

// procCPUNanos returns pid's cumulative user+system CPU time in nanoseconds. It
// fills the fixed-size proc_taskinfo buffer via the syscall, then defers to the
// pure parser for the field extraction.
func procCPUNanos(pid int) (uint64, error) {
	var buf [taskInfoSize]byte
	r1, _, errno := unix.Syscall6(
		unix.SYS_PROC_INFO,
		procInfoCallPIDInfo,
		uintptr(pid),
		procPIDTaskInfo,
		0,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
	)
	if errno != 0 {
		return 0, errno
	}
	if int(r1) < len(buf) {
		return 0, fmt.Errorf("engine: proc_pidinfo(%d) short read: %d of %d bytes", pid, r1, len(buf))
	}
	return parseDarwinCPUNanos(buf[:])
}
