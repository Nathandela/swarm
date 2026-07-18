//go:build race

package engine

// Race-build twin of timelinestep_norace_test.go: coarser sweep steps keep the
// package inside go test's 600s bound under -race (~10x per-step cost). The
// byte-exact correctness sweep runs in normal builds; hard-frame assertions
// remain byte-exact here too (snapAtOffset renders exact prefixes).
const (
	timelineFineStepAgy      = 32
	timelineFineStepOpencode = 256
)
