//go:build darwin

package daemon

import "github.com/Nathandela/swarm/internal/procstart"

// processStartTime returns a stable, monotonic-per-process identity token for
// pid: the process creation time in microseconds since the epoch, read from the
// kernel's kinfo_proc via sysctl(kern.proc.pid). It is stable across repeated
// reads of the same live process (so an identity match is deterministic) and
// distinct for two processes started at different instants (so PID reuse is
// detectable — S3/D-4). A dead or unknown pid yields an error.
//
// THE PLATFORM READ MOVED to internal/procstart in Wave R7, and this is now a
// one-line delegation, because internal/shim must produce the IDENTICAL value for
// the backend process it owns (its backend.json, ADR-013 §R7.2c) and cannot import
// this package -- the dependency runs daemon -> shim. Two copies of the read would
// be a fact the two sides could silently disagree about, and the disagreement's
// symptom is a daemon that reaps a healthy app-server or adopts a stranger's.
func processStartTime(pid int) (int64, error) { return procstart.StartTime(pid) }
