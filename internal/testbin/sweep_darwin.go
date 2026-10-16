package testbin

import (
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

func processIDs() []int {
	processes, _ := unix.SysctlKinfoProcSlice("kern.proc.all")
	var pids []int
	for _, process := range processes {
		if process.Eproc.Ucred.Uid == uint32(os.Getuid()) {
			pids = append(pids, int(process.Proc.P_pid))
		}
	}
	return pids
}

func processExecutable(pid int) (string, error) {
	// proc_pidpath's kernel interface; unlike argv, it cannot name an unrelated executable.
	const procInfoCallPIDInfo, procPIDPathInfo = 2, 11
	var path [4096]byte // PROC_PIDPATHINFO_MAXSIZE
	_, _, errno := unix.Syscall6(
		unix.SYS_PROC_INFO, //nolint:staticcheck // x/sys/unix has no proc_pidpath wrapper
		procInfoCallPIDInfo, uintptr(pid), procPIDPathInfo, 0, uintptr(unsafe.Pointer(&path[0])), uintptr(len(path)),
	)
	if errno != 0 {
		return "", errno
	}
	return unix.ByteSliceToString(path[:]), nil
}
