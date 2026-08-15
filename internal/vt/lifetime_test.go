package vt

import (
	"runtime"
	"strings"
	"testing"
	"weak"
)

// vt-hang (Wave R0): pins the memory leak that was the real cause of CI's
// "fuzzing process hung or terminated unexpectedly" in internal/vt. Every
// Emulator ever constructed stayed live for the process's lifetime — including
// its scrollback, which a REP-dense stream fills to tens of megabytes — so a
// fuzz worker's heap grew without bound until the runtime aborted it (exit
// status 2). It looked like a hang because the per-input CPU cost was normal
// and the input the coordinator saved never reproduced on its own.
//
// See docs/verification/r0-red/vt-emulator-leak-red.txt for the measurements.

// closedEmulator builds an Emulator, drives the paths that make it expensive
// and that install the callbacks, closes it, and returns only a weak reference.
// The strong reference dies with the frame, so anything the weak pointer still
// resolves to afterwards is being retained by the package, not by this test.
func closedEmulator() weak.Pointer[Emulator] {
	e := NewEmulator(80, 24)
	// Set the window title and the cursor visibility: both arrive through the
	// xvt.Callbacks closures, the reference from the inner terminal back to the
	// wrapper. Then fill the scrollback so a retained emulator is expensive.
	e.Feed([]byte("\x1b]0;title\x07\x1b[?25l" + strings.Repeat("line\r\n", 64)))
	_ = e.Close()
	return weak.Make(e)
}

// TestEmulator_ClosedEmulatorIsReclaimed asserts the lifetime contract Close
// promises: once Close has returned and the caller has dropped its reference,
// the Emulator and everything it owns become collectable.
//
// The assertion is a weak pointer rather than a heap-size threshold, so it is
// exact and cannot flake on a loaded machine: weak.Pointer.Value returns nil
// exactly when the object has been collected. Several GC cycles are allowed
// because an object that still has a finalizer attached needs one cycle to run
// the finalizer and another to be freed.
func TestEmulator_ClosedEmulatorIsReclaimed(t *testing.T) {
	p := closedEmulator()
	for range 5 {
		runtime.GC()
		if p.Value() == nil {
			return
		}
	}
	t.Fatalf("a closed Emulator was still reachable after 5 GC cycles: it and its scrollback leak for the lifetime of the process")
}

// TestEmulator_ManyClosedEmulatorsDoNotAccumulate is the same contract stated
// as the resource bound that actually broke CI: constructing and closing many
// scrollback-filling emulators must not grow the live heap in proportion to how
// many were made. It is deliberately coarse — the leak it guards against
// retained ~34MB per emulator, so 32 of them moved the live heap by a gigabyte,
// three orders of magnitude clear of any GC-timing noise the bound has to
// tolerate.
func TestEmulator_ManyClosedEmulatorsDoNotAccumulate(t *testing.T) {
	const (
		emulators   = 32
		maxGrowthMB = 64
	)
	// A REP burst clamped to csiParamDigitCap still prints ~999 characters per
	// sequence, which is what fills the scrollback.
	burst := []byte("X" + strings.Repeat("\x1b[99999b", 64))

	liveHeapMB := func() float64 {
		runtime.GC()
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return float64(m.HeapAlloc) / (1 << 20)
	}

	before := liveHeapMB()
	for range emulators {
		e := NewEmulator(80, 24)
		e.Feed(burst)
		_ = e.Close()
	}
	if growth := liveHeapMB() - before; growth > maxGrowthMB {
		t.Fatalf("live heap grew %.0f MB across %d create/Feed/Close cycles (bound %d MB): closed emulators are being retained",
			growth, emulators, maxGrowthMB)
	}
}
