package main

// E13.2 — `swarm version` (and `--version`) print the build-time stamped
// version. TestDispatch_VersionSubcommand is the fast unit path (no exec,
// default "dev" build). TestVersionSubcommand_ReportsLdflagsStampedVersion is
// the exec-level proof that a REAL -ldflags stamp (as .goreleaser.yaml applies
// at release build time) actually reaches the printed output — it builds a
// real `swarm` binary with a stamped version and runs it. It is skipped only
// when the go toolchain itself is unavailable, never in CI (where it always
// is).

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestDispatch_VersionSubcommand asserts both `version` and `--version` route
// to the version role, exit 0, and print something identifiable as swarm's
// version line (the unstamped default build says "dev").
func TestDispatch_VersionSubcommand(t *testing.T) {
	for _, arg := range []string{"version", "--version"} {
		t.Run(arg, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			exit := dispatch([]string{arg}, &stdout, &stderr)
			if exit != 0 {
				t.Fatalf("dispatch(%q) exit = %d, want 0 (stderr: %s)", arg, exit, stderr.String())
			}
			if !strings.Contains(stdout.String(), "swarm") || !strings.Contains(stdout.String(), "dev") {
				t.Fatalf("dispatch(%q) stdout = %q, want it to report swarm's (default \"dev\") version", arg, stdout.String())
			}
		})
	}
}

// TestVersionSubcommand_ReportsLdflagsStampedVersion builds cmd/swarm with
// -ldflags stamping internal/version.Version to a distinctive test value, runs
// the resulting binary's `version` subcommand, and asserts the stamped value
// (not "dev") appears in its output — proof the release ldflags wiring
// (.goreleaser.yaml) actually reaches `swarm version` end to end.
func TestVersionSubcommand_ReportsLdflagsStampedVersion(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}

	const stamped = "vTEST.1.2"
	bin := filepath.Join(t.TempDir(), "swarm")
	ldflags := "-X github.com/Nathandela/swarm/internal/version.Version=" + stamped
	build := exec.Command("go", "build", "-ldflags", ldflags, "-o", bin, "github.com/Nathandela/swarm/cmd/swarm")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build swarm with ldflags: %v", err)
	}

	var out bytes.Buffer
	run := exec.Command(bin, "version")
	run.Stdout = &out
	run.Stderr = os.Stderr
	if err := run.Run(); err != nil {
		t.Fatalf("run swarm version: %v", err)
	}
	if !strings.Contains(out.String(), stamped) {
		t.Fatalf("swarm version output = %q, want it to contain the ldflags-stamped %q", out.String(), stamped)
	}
}
