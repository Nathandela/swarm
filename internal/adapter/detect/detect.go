// Package detect is the host-I/O side of adapter detection (E9.1 / E9.2). It
// holds the ONE exec-based adapter.HostProber implementation so the pure
// adapter contract package stays genuinely fd/exec-free: internal/adapter
// declares the HostProber interface and the core adapter.Detect routine, while
// the LookPath and the version exec live HERE. Detection callers (the launch
// form / Epic 11) build a Host and hand it to adapter.Detect.
package detect

import "os/exec"

// Host is the real adapter.HostProber: it resolves binaries on PATH and runs
// them, owning the fds and child process that the pure adapter contract must
// not. It is stateless and safe to share by value.
type Host struct{}

// LookPath resolves name to a program path via the process PATH.
func (Host) LookPath(name string) (string, error) {
	return exec.LookPath(name)
}

// Run executes path with args and returns its stdout. A non-zero exit or a
// spawn failure is returned as the error (stdout captured so far still comes
// back for best-effort parsing).
func (Host) Run(path string, args []string) (string, error) {
	out, err := exec.Command(path, args...).Output()
	return string(out), err
}
