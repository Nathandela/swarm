//go:build !race

package e2e

// raceEnabled is true when this test binary was built with `go test -race`. The
// Epic 14 N-2 live-shim latency gate (latency_gate_test.go) uses it to assert a
// looser sanity ceiling under the race detector — whose instrumentation distorts
// sub-10ms timing — while the authoritative non-race run still enforces the real
// 10ms budget.
const raceEnabled = false
