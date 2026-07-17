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
// PIN: SampleCPU is the production default the engine installs when
// Config.CPUSampler is nil; idle ~0, busy clearly positive.

// procInfoCallPIDInfo is the proc_info call number for proc_pidinfo.
const procInfoCallPIDInfo = 2

// procPIDTaskInfo is the PROC_PIDTASKINFO flavor.
const procPIDTaskInfo = 4

// procTaskInfo mirrors darwin's struct proc_taskinfo (sys/proc_info.h). Only the
// two cumulative CPU-time fields (nanoseconds) are read; the remaining fields pad
// the layout to its exact 96-byte size, which the syscall validates.
type procTaskInfo struct {
	VirtualSize   uint64
	ResidentSize  uint64
	TotalUser     uint64 // cumulative user CPU time, nanoseconds
	TotalSystem   uint64 // cumulative system CPU time, nanoseconds
	ThreadsUser   uint64
	ThreadsSystem uint64
	Policy        int32
	Faults        int32
	Pageins       int32
	CowFaults     int32
	MessagesSent  int32
	MessagesRecv  int32
	SyscallsMach  int32
	SyscallsUnix  int32
	Csw           int32
	Threadnum     int32
	Numrunning    int32
	Priority      int32
}

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

// procCPUNanos returns pid's cumulative user+system CPU time in nanoseconds.
func procCPUNanos(pid int) (uint64, error) {
	var ti procTaskInfo
	size := unsafe.Sizeof(ti)
	r1, _, errno := unix.Syscall6(
		unix.SYS_PROC_INFO,
		procInfoCallPIDInfo,
		uintptr(pid),
		procPIDTaskInfo,
		0,
		uintptr(unsafe.Pointer(&ti)),
		size,
	)
	if errno != 0 {
		return 0, errno
	}
	if int(r1) < int(size) {
		return 0, fmt.Errorf("engine: proc_pidinfo(%d) short read: %d of %d bytes", pid, r1, size)
	}
	return ti.TotalUser + ti.TotalSystem, nil
}
