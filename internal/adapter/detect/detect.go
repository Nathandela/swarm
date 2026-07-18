// Package detect is the host-I/O side of adapter detection (E9.1 / E9.2). It
// holds the ONE exec-based adapter.HostProber implementation so the pure
// adapter contract package stays genuinely fd/exec-free: internal/adapter
// declares the HostProber interface and the core adapter.Detect routine, while
// the LookPath and the version exec live HERE. Detection callers (the launch
// form / Epic 11) build a Host and hand it to adapter.Detect.
package detect

import (
	"context"
	"os/exec"
	"time"
)

// probeTimeout bounds a single version probe. A version banner returns in
// milliseconds; a Node-based CLI that cold-starts or wedges could otherwise hang
// indefinitely, and detection runs on the launch-form path, so an unbounded exec
// froze the whole UI (the P0 field-test bug). Detection is best-effort: a probe
// that overruns is abandoned and the binary is reported Found-but-unversioned.
const probeTimeout = 2 * time.Second

// Host is the real adapter.HostProber: it resolves binaries on PATH and runs
// them, owning the fds and child process that the pure adapter contract must
// not. It is stateless and safe to share by value.
type Host struct{}

// LookPath resolves name to a program path via the process PATH.
func (Host) LookPath(name string) (string, error) {
	return exec.LookPath(name)
}

// Run executes path with args and returns its combined stdout+stderr. Many CLIs
// print their version banner to STDERR, so capturing only stdout would find the
// binary but leave it unversioned; CombinedOutput captures both. A non-zero exit
// or a spawn failure is returned as the error (output captured so far still comes
// back for best-effort parsing). The exec is bounded by probeTimeout so a hanging
// CLI is abandoned rather than blocking the caller.
func (Host) Run(path string, args []string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	// WaitDelay closes the output pipes shortly after the context kill. Without
	// it, CombinedOutput blocks until every pipe HOLDER exits: a probe that
	// spawned a child (linux /bin/sh runs the command as a child; macOS bash
	// execs it) leaves an orphan holding the pipe and the "bounded" probe hangs
	// for the orphan's lifetime — the exact freeze this timeout exists to stop.
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	return string(out), err
}
