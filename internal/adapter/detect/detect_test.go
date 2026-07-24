package detect

// E9.1 / E9.2 — the exec-based HostProber lives OUTSIDE the pure contract
// package, and adapter.Detect drives it. These tests prove Host satisfies the
// interface and that the full detection path (LookPath + Run + the adapter's
// pure ParseVersion) yields a correct Detection against a real on-PATH binary
// (the `go` toolchain, always present when the test suite runs).

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
)

// goAdapter is a tiny adapter whose sole purpose is to describe the `go` binary
// so Detect can run end-to-end against a real executable.
type goAdapter struct{ adapter.Adapter }

func (goAdapter) Binary() string        { return "go" }
func (goAdapter) VersionArgs() []string { return []string{"version"} }
func (goAdapter) ParseVersion(out string) (string, bool) {
	// `go version` prints e.g. "go version go1.24.2 darwin/arm64".
	for _, f := range strings.Fields(out) {
		if v := strings.TrimPrefix(f, "go"); v != f && strings.Contains(v, ".") {
			return v, true
		}
	}
	return "", false
}
func (goAdapter) SupportedVersions() adapter.VersionConstraint {
	return adapter.VersionConstraint{Min: "1.0.0", Max: "9999.0.0"}
}

func TestHost_SatisfiesHostProber(t *testing.T) {
	var _ adapter.HostProber = Host{}
}

func TestDetect_EndToEndAgainstRealBinary(t *testing.T) {
	det := adapter.Detect(goAdapter{}, Host{})
	if !det.Found {
		t.Skip("go binary not found on PATH; cannot exercise the real detection path")
	}
	if det.Path == "" {
		t.Error("Found but Path is empty")
	}
	if det.Version == "" {
		t.Errorf("Found go but parsed no version (Detection %+v)", det)
	}
	if !det.InRange {
		t.Errorf("go version %q reported out of the wide-open range", det.Version)
	}
}

func TestDetect_NotFound(t *testing.T) {
	// A binary name that cannot resolve on PATH yields the zero Detection.
	got := adapter.Detect(missingBinaryAdapter{}, Host{})
	if got.Found || got.Path != "" || got.Version != "" || got.InRange {
		t.Errorf("a nonexistent binary reported a non-zero Detection: %+v", got)
	}
}

// missingBinaryAdapter names a binary that will not resolve on PATH.
type missingBinaryAdapter struct{ goAdapter }

func (missingBinaryAdapter) Binary() string { return "swarm-nonexistent-binary-xyzzy" }

// stderrVersionAdapter describes a "CLI" whose version banner prints to STDERR:
// `sh -c 'echo ... 1>&2'`. It proves Host.Run captures stderr (CombinedOutput),
// so such a CLI is Found AND versioned rather than found-but-unversioned.
type stderrVersionAdapter struct{ goAdapter }

func (stderrVersionAdapter) Binary() string { return "sh" }
func (stderrVersionAdapter) VersionArgs() []string {
	return []string{"-c", "echo stderrtool 4.5.6 1>&2"}
}
func (stderrVersionAdapter) ParseVersion(out string) (string, bool) {
	for _, f := range strings.Fields(out) {
		if strings.Count(f, ".") == 2 {
			return f, true
		}
	}
	return "", false
}

// hangAdapter names a version probe that never returns (`sh -c 'sleep 30'`), so
// Host.Run must abandon it at probeTimeout rather than block the caller — the P0
// launch-form freeze was an unbounded version exec on the UI hot path.
type hangAdapter struct{ goAdapter }

func (hangAdapter) Binary() string        { return "sh" }
func (hangAdapter) VersionArgs() []string { return []string{"-c", "sleep 30"} }

func TestHostRun_BoundsAHangingProbe(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on PATH; cannot exercise the timeout path")
	}
	start := time.Now()
	det := adapter.Detect(hangAdapter{}, Host{})
	elapsed := time.Since(start)

	if elapsed > probeTimeout+3*time.Second {
		t.Fatalf("a hanging version probe was not bounded: took %s (probeTimeout %s)", elapsed, probeTimeout)
	}
	if !det.Found {
		t.Error("the binary resolves on PATH, so it must be Found even when the version probe times out")
	}
	if det.Version != "" {
		t.Errorf("a timed-out probe must yield no version, got %q", det.Version)
	}
}

// crashingProbeAdapter names a "CLI" whose version probe exits NON-ZERO after
// printing a diagnostic to stderr — the codex-on-Apple-Silicon case (bead 8c0):
// `codex --version` throws "Missing optional dependency ... Reinstall Codex" and
// exits 1. Detect must plumb that captured first line into Detection.ProbeErr
// instead of discarding it, so the launch picker can show the CLI's own cause.
type crashingProbeAdapter struct{ goAdapter }

func (crashingProbeAdapter) Binary() string { return "sh" }
func (crashingProbeAdapter) VersionArgs() []string {
	return []string{"-c", "echo 'Missing optional dependency (@openai/codex-darwin-x64). Reinstall Codex.' 1>&2; exit 1"}
}

func TestDetect_CapturesProbeDiagnosticOnNonZeroExit(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on PATH; cannot exercise the crashing-probe path")
	}
	det := adapter.Detect(crashingProbeAdapter{}, Host{})
	if !det.Found {
		t.Fatal("the binary resolves on PATH, so it must be Found even when the version probe crashes")
	}
	if det.Version != "" {
		t.Errorf("a crashed probe must yield no version, got %q", det.Version)
	}
	if !strings.Contains(det.ProbeErr, "Reinstall Codex") {
		t.Errorf("captured probe diagnostic lost on non-zero exit: ProbeErr = %q, want it to contain %q", det.ProbeErr, "Reinstall Codex")
	}
	// Only the FIRST non-empty line is kept, so a multi-line crash stays a one-line reason.
	if strings.Contains(det.ProbeErr, "\n") {
		t.Errorf("ProbeErr must be a single line, got %q", det.ProbeErr)
	}
}

func TestHostRun_CapturesStderrVersion(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on PATH; cannot exercise the stderr-capture path")
	}
	det := adapter.Detect(stderrVersionAdapter{}, Host{})
	if !det.Found {
		t.Fatal("sh not Found")
	}
	if det.Version != "4.5.6" {
		t.Errorf("Version = %q, want 4.5.6 (a version printed to stderr must still be captured — Run must use CombinedOutput)", det.Version)
	}
	if !det.InRange {
		t.Errorf("4.5.6 reported out of the wide-open range: %+v", det)
	}
}

// slowVersionAdapter names a version probe that sleeps ~3s before printing its
// banner (`sh -c 'sleep 3; echo ...'`): slower than a cold-starting Node CLI but
// well within a real, non-hung run. R-A1: probeTimeout must be raised from 2s to
// 5s so this probe's version is still captured rather than abandoned.
type slowVersionAdapter struct{ goAdapter }

func (slowVersionAdapter) Binary() string { return "sh" }
func (slowVersionAdapter) VersionArgs() []string {
	return []string{"-c", "sleep 3; echo slowtool 7.8.9"}
}
func (slowVersionAdapter) ParseVersion(out string) (string, bool) {
	for _, f := range strings.Fields(out) {
		if strings.Count(f, ".") == 2 {
			return f, true
		}
	}
	return "", false
}

func TestHostRun_ToleratesA3sProbe(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on PATH; cannot exercise the raised-timeout path")
	}
	start := time.Now()
	det := adapter.Detect(slowVersionAdapter{}, Host{})
	if elapsed := time.Since(start); elapsed > 6*time.Second {
		t.Fatalf("probe took %s; exceeded the test's runtime budget", elapsed)
	}
	if !det.Found {
		t.Fatal("sh not Found")
	}
	if det.Version != "7.8.9" {
		t.Errorf("Version = %q, want 7.8.9 (a ~3s probe must complete within the raised probeTimeout, not be abandoned at the old 2s bound)", det.Version)
	}
}
