package engine

// Pure, platform-independent parsers for the two CPU sources (E10.6). The
// syscall and file read live in the build-tagged cpu_linux.go / cpu_darwin.go;
// the byte/string parsing is factored out here so it is unit-testable in NORMAL
// CI on every runner, not only under -tags integration on its native platform.

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
)

// procTaskInfo (sys/proc_info.h) byte layout the darwin PROC_PIDTASKINFO syscall
// fills. Only the two cumulative CPU-time counters are read; the rest pad the
// struct to its exact 96-byte size, which the syscall's short-read guard checks.
//
//	offset  field            type
//	0       pti_virtual_size uint64
//	8       pti_resident..   uint64
//	16      pti_total_user   uint64  <- cumulative user CPU time, nanoseconds
//	24      pti_total_system uint64  <- cumulative system CPU time, nanoseconds
//	...     (threads/faults/csw/... ) padding to 96 bytes
const (
	taskInfoTotalUserOff   = 16
	taskInfoTotalSystemOff = 24
	taskInfoSize           = 96
)

// parseDarwinCPUNanos returns pid's cumulative user+system CPU time (nanoseconds)
// from a proc_taskinfo buffer. Darwin runs little-endian on every supported arch.
func parseDarwinCPUNanos(buf []byte) (uint64, error) {
	if len(buf) < taskInfoTotalSystemOff+8 {
		return 0, fmt.Errorf("engine: proc_taskinfo short buffer: %d bytes", len(buf))
	}
	user := binary.LittleEndian.Uint64(buf[taskInfoTotalUserOff:])
	system := binary.LittleEndian.Uint64(buf[taskInfoTotalSystemOff:])
	return user + system, nil
}

// parseLinuxCPUTicks returns pid's cumulative utime+stime (clock ticks) from a
// /proc/<pid>/stat line. It locates the ')' that closes the comm field — which
// may itself contain spaces or parentheses — and counts fields from there, the
// canonical robust parse.
func parseLinuxCPUTicks(data []byte) (uint64, error) {
	s := string(data)
	rparen := strings.LastIndexByte(s, ')')
	if rparen < 0 || rparen+2 > len(s) {
		return 0, fmt.Errorf("engine: malformed /proc stat line")
	}
	// After "(comm) " the fields resume at state (field 3) as index 0, so field N
	// is index N-3: utime (14) -> 11, stime (15) -> 12.
	fields := strings.Fields(s[rparen+1:])
	const utimeIdx, stimeIdx = 11, 12
	if len(fields) <= stimeIdx {
		return 0, fmt.Errorf("engine: /proc stat line has too few fields")
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
