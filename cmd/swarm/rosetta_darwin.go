//go:build darwin

package main

import "golang.org/x/sys/unix"

// rosettaTranslated reports whether this process is an x86_64 binary running under
// Rosetta 2 on Apple Silicon (sysctl sysctl.proc_translated == 1). Such a swarm
// makes codex's env-node shebang resolve the x64 CLI package that npm never
// installs on arm64, crashing the version probe and the real PTY launch (bead
// 8c0). The sysctl is absent on genuine Intel Macs, which read as not translated.
func rosettaTranslated() bool {
	v, err := unix.SysctlUint32("sysctl.proc_translated")
	return err == nil && v == 1
}
