//go:build !race

package engine

// Timeline-sweep granularity for normal builds: byte-exact inside agy's busy
// window (the false-idle safety property demands every prefix), 64-byte steps
// for opencode (no idle rule; see gridrules_fixture_test.go). The race-tagged
// twin coarsens both: the sweep is single-goroutine and deterministic, so
// -race adds no race coverage to it, only a ~10x per-step cost that pushed the
// package past go test's 600s bound; the engine's concurrent paths get their
// -race coverage from the rest of the suite, and the explicit hard-frame
// assertions stay byte-exact in both modes.
const (
	timelineFineStepAgy      = 1
	timelineFineStepOpencode = 64
)
