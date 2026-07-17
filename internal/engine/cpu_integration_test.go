//go:build integration

package engine

// E10.6: the REAL production CPU sampler (proc_pidinfo on darwin, /proc on
// linux). Build-tagged so it runs only under `go test -tags integration`; CI runs
// it on both the linux and macOS runners so BOTH platform paths are exercised
// against real processes. It spawns a genuinely busy process and an idle one,
// samples each via engine.SampleCPU, and asserts busy activity clearly exceeds
// idle. Thresholds are generous because real sampling is inherently noisy.
//
// PIN: SampleCPU is the production default the engine installs when
// Config.CPUSampler is nil; its return is a per-process utilization where idle
// is ~0 and a busy process is clearly positive (unit is the sampler's own — the
// assertions are unit-agnostic beyond "busy > idle" and "idle ~ 0").

import (
	"os/exec"
	"runtime"
	"testing"
	"time"
)

func TestSampleCPURealProcesses(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skipf("SampleCPU unsupported on %s", runtime.GOOS)
	}

	busy := exec.Command("sh", "-c", "while :; do :; done")
	if err := busy.Start(); err != nil {
		t.Fatalf("start busy process: %v", err)
	}
	defer func() {
		_ = busy.Process.Kill()
		_, _ = busy.Process.Wait()
	}()

	idle := exec.Command("sleep", "30")
	if err := idle.Start(); err != nil {
		t.Fatalf("start idle process: %v", err)
	}
	defer func() {
		_ = idle.Process.Kill()
		_, _ = idle.Process.Wait()
	}()

	// Let both processes accumulate a sampling window.
	time.Sleep(500 * time.Millisecond)

	busyCPU, err := SampleCPU(busy.Process.Pid)
	if err != nil {
		t.Fatalf("SampleCPU(busy pid=%d): %v", busy.Process.Pid, err)
	}
	idleCPU, err := SampleCPU(idle.Process.Pid)
	if err != nil {
		t.Fatalf("SampleCPU(idle pid=%d): %v", idle.Process.Pid, err)
	}

	if !(busyCPU > idleCPU) {
		t.Fatalf("busy CPU (%.4f) not greater than idle CPU (%.4f)", busyCPU, idleCPU)
	}
	if busyCPU <= 0 {
		t.Fatalf("busy CPU = %.4f, want clearly nonzero", busyCPU)
	}
	if idleCPU > 5.0 {
		t.Fatalf("idle CPU = %.4f, want ~zero", idleCPU)
	}
}
