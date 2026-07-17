//go:build race

package tui

// raceEnabled is true when this test binary was built with `go test -race`. See
// firstpaint_gate_test.go's package doc comment for why the Epic 14 N-1 perf gate
// needs to know this.
const raceEnabled = true
