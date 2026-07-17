package version

import "testing"

// TestDefaultVersionIsDev asserts the unstamped default (a plain `go build`/`go
// run` with no -ldflags, e.g. local dev or `go test`) is the literal "dev" —
// never empty, never a stale leftover from a previous -ldflags build. Release
// builds override it via -ldflags "-X .../internal/version.Version=vX.Y.Z"
// (E13.2; wired by .goreleaser.yaml).
func TestDefaultVersionIsDev(t *testing.T) {
	if Version != "dev" {
		t.Fatalf("default Version = %q, want %q", Version, "dev")
	}
}
