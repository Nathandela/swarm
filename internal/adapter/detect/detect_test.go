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
