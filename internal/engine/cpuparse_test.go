package engine

// F4 (E10.6): deterministic UNIT tests for BOTH platform CPU parsers, run in
// NORMAL CI on every runner (no build tag). The syscall/file read stays in the
// build-tagged cpu_{linux,darwin}.go; the byte/string parsing is extracted into
// the pure parseLinuxCPUTicks / parseDarwinCPUNanos so the exact layout is
// verified on both platforms rather than only under -tags integration.

import (
	"encoding/binary"
	"testing"
)

// A linux /proc/<pid>/stat line whose comm itself contains spaces AND parentheses
// must still yield the correct utime (field 14) + stime (field 15): the parse
// locates the LAST ')' that closes comm, then counts fields from there.
func TestParseLinuxCPUTicks_CommWithSpacesAndParens(t *testing.T) {
	// pid (comm) then fields resuming at state(3): index 0=state ... 11=utime, 12=stime.
	// comm = "weird (proc) name" exercises embedded spaces and parens.
	line := "4242 (weird (proc) name) R 1 2 3 4 5 6 7 8 9 10 111 222 extra fields ignored"
	got, err := parseLinuxCPUTicks([]byte(line))
	if err != nil {
		t.Fatalf("parseLinuxCPUTicks: %v", err)
	}
	if want := uint64(111 + 222); got != want {
		t.Fatalf("utime+stime = %d, want %d", got, want)
	}
}

// A trailing newline (real /proc lines carry one) must not disturb the parse.
func TestParseLinuxCPUTicks_TrailingNewline(t *testing.T) {
	line := "7 (sh) S 1 7 7 0 -1 0 0 0 0 0 5 9 0 0\n"
	got, err := parseLinuxCPUTicks([]byte(line))
	if err != nil {
		t.Fatalf("parseLinuxCPUTicks: %v", err)
	}
	if want := uint64(5 + 9); got != want {
		t.Fatalf("utime+stime = %d, want %d", got, want)
	}
}

// A malformed line with no ')' is rejected rather than silently mis-parsed.
func TestParseLinuxCPUTicks_Malformed(t *testing.T) {
	if _, err := parseLinuxCPUTicks([]byte("no parens here")); err == nil {
		t.Fatalf("parseLinuxCPUTicks on malformed input: got nil error, want failure")
	}
}

// The darwin proc_taskinfo buffer carries TotalUser at offset 16 and TotalSystem
// at offset 24 (both little-endian uint64 nanoseconds); the parser returns their
// sum.
func TestParseDarwinCPUNanos_Offsets(t *testing.T) {
	buf := make([]byte, taskInfoSize)
	binary.LittleEndian.PutUint64(buf[taskInfoTotalUserOff:], 1_000)
	binary.LittleEndian.PutUint64(buf[taskInfoTotalSystemOff:], 2_000)
	got, err := parseDarwinCPUNanos(buf)
	if err != nil {
		t.Fatalf("parseDarwinCPUNanos: %v", err)
	}
	if want := uint64(3_000); got != want {
		t.Fatalf("user+system = %d, want %d", got, want)
	}
}

// A short buffer (fewer than the two counters cover) is rejected, so a truncated
// syscall read can never be mistaken for idle.
func TestParseDarwinCPUNanos_ShortBuffer(t *testing.T) {
	if _, err := parseDarwinCPUNanos(make([]byte, taskInfoTotalSystemOff)); err == nil {
		t.Fatalf("parseDarwinCPUNanos on short buffer: got nil error, want failure")
	}
}
