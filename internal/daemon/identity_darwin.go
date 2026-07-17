//go:build darwin

package daemon

import "golang.org/x/sys/unix"

// processStartTime returns a stable, monotonic-per-process identity token for
// pid: the process creation time in microseconds since the epoch, read from the
// kernel's kinfo_proc via sysctl(kern.proc.pid). It is stable across repeated
// reads of the same live process (so an identity match is deterministic) and
// distinct for two processes started at different instants (so PID reuse is
// detectable — S3/D-4). A dead or unknown pid yields an error.
func processStartTime(pid int) (int64, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, err
	}
	tv := kp.Proc.P_starttime
	return int64(tv.Sec)*1_000_000 + int64(tv.Usec), nil
}
