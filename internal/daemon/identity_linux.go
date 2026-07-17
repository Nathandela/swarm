//go:build linux

package daemon

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// processStartTime returns a stable, monotonic-per-process identity token for
// pid: field 22 (starttime, in clock ticks since boot) of /proc/<pid>/stat. It
// is stable across repeated reads of the same live process (so an identity match
// is deterministic) and distinct for two processes started at different instants
// (so PID reuse is detectable — S3/D-4). A dead or unknown pid yields an error.
//
// Field 22 is parsed by locating the ')' that closes comm (field 2), which may
// itself contain spaces or parentheses, then counting space-separated fields
// from there — the canonical robust /proc/stat parse.
func processStartTime(pid int) (int64, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	s := string(data)
	// comm (field 2) is wrapped in parentheses and may contain ')'; the real
	// field list resumes after the LAST ')'.
	rparen := strings.LastIndexByte(s, ')')
	if rparen < 0 || rparen+2 > len(s) {
		return 0, fmt.Errorf("processStartTime: malformed /proc/%d/stat", pid)
	}
	// After "(comm) " the fields are: state(3) ... starttime(22). Relative to the
	// text after ") ", field 3 is index 0, so starttime (field 22) is index 19.
	fields := strings.Fields(s[rparen+1:])
	const starttimeIdx = 19 // field 22 minus fields 1 and 2 already consumed
	if len(fields) <= starttimeIdx {
		return 0, fmt.Errorf("processStartTime: /proc/%d/stat has too few fields", pid)
	}
	v, err := strconv.ParseInt(fields[starttimeIdx], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("processStartTime: parse starttime: %w", err)
	}
	return v, nil
}
