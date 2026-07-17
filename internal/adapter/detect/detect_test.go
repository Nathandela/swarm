package detect

// E9.1 / E9.2 — the exec-based HostProber lives OUTSIDE the pure contract
// package, and adapter.Detect drives it. These tests prove Host satisfies the
// interface and that the full detection path (LookPath + Run + the adapter's
// pure ParseVersion) yields a correct Detection against a real on-PATH binary
// (the `go` toolchain, always present when the test suite runs).

import (
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
