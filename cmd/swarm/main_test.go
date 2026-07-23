package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/daemon"
)

// v0.3 — detectAgents derives a human-readable unavailability reason from the raw
// Detection so the launch picker can explain why an agent cannot launch. A usable
// (found + in-range) agent and a plainly not-installed one carry no reason (the
// latter keeps the existing install-hint behavior); a found-but-unusable agent
// carries the specific cause.
func TestUnavailabilityReason(t *testing.T) {
	cases := []struct {
		name string
		det  adapter.Detection
		want string
	}{
		{"usable in-range", adapter.Detection{Found: true, Version: "1.5.0", InRange: true}, ""},
		{"not installed", adapter.Detection{Found: false}, ""},
		{"found but version probe failed", adapter.Detection{Found: true, Version: "", InRange: false}, "version probe failed - reinstall?"},
		{"found but out of range", adapter.Detection{Found: true, Version: "3.0.0", InRange: false}, "unsupported version 3.0.0"},
	}
	for _, c := range cases {
		if got := unavailabilityReason(c.det); got != c.want {
			t.Errorf("%s: unavailabilityReason = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestDispatch(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantExit   int
		wantStderr string
	}{
		{
			// No args opens the TUI (F1). Under `go test` stdout is not a terminal,
			// so the interactive-terminal guard fires with a clear error and a
			// non-zero exit — never a panic or a half-drawn screen. The real TUI path
			// (live daemon + PTY) is exercised by TestTUI_OpensAndRestoresOverPTY.
			name:       "no args, no tty, reports not-a-terminal",
			args:       []string{},
			wantExit:   1,
			wantStderr: "not a terminal",
		},
		{
			name:       "daemon subcommand routes to stub",
			args:       []string{"daemon"},
			wantExit:   1,
			wantStderr: "daemon: not implemented",
		},
		{
			name:       "shim subcommand without --config prints usage",
			args:       []string{"shim"},
			wantExit:   2,
			wantStderr: "usage",
		},
		{
			name:       "hook subcommand routes to stub",
			args:       []string{"hook"},
			wantExit:   1,
			wantStderr: "hook: not implemented",
		},
		{
			name:       "unknown subcommand prints usage",
			args:       []string{"bogus"},
			wantExit:   2,
			wantStderr: "usage",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			gotExit := dispatch(tt.args, &stdout, &stderr)
			if gotExit != tt.wantExit {
				t.Errorf("dispatch(%v) exit = %d, want %d", tt.args, gotExit, tt.wantExit)
			}
			got := strings.ToLower(stderr.String())
			want := strings.ToLower(tt.wantStderr)
			if !strings.Contains(got, want) {
				t.Errorf("dispatch(%v) stderr = %q, want substring %q", tt.args, stderr.String(), tt.wantStderr)
			}
		})
	}
}

// A1 — skeletonConfigFromEnv must wire a NEW opt-in env var,
// SWARM_DAEMON_REMOTE_SOCK, into skeleton.Config.RemoteSocketPath so `swarm
// daemon` actually stands up the dedicated REMOTE-tier unix socket
// (protocol.ServeRemoteWithID, gated on cfg.RemoteSocketPath != "" in
// internal/skeleton/serve.go). The literal env var name is used directly
// here (rather than a daemon.EnvRemoteSocket constant) because the constant
// does not exist yet — this test must fail on the assertion, not on a
// missing symbol.
const testEnvRemoteSocket = "SWARM_DAEMON_REMOTE_SOCK"

// TestSkeletonConfigFromEnv_WiresRemoteSocketFromEnv pins the opt-in case:
// when SWARM_DAEMON_REMOTE_SOCK is set, it must flow through to
// Config.RemoteSocketPath verbatim.
func TestSkeletonConfigFromEnv_WiresRemoteSocketFromEnv(t *testing.T) {
	t.Setenv(daemon.EnvStateDir, t.TempDir())
	const want = "/tmp/swarm-remote-test.sock"
	t.Setenv(testEnvRemoteSocket, want)

	cfg, ok := skeletonConfigFromEnv()
	if !ok {
		t.Fatal("skeletonConfigFromEnv() ok = false, want true")
	}
	if cfg.RemoteSocketPath != want {
		t.Errorf("cfg.RemoteSocketPath = %q, want %q", cfg.RemoteSocketPath, want)
	}
}

// TestSkeletonConfigFromEnv_RemoteSocketEmptyByDefault pins the secure
// default: with SWARM_DAEMON_REMOTE_SOCK unset, Config.RemoteSocketPath must
// be "" so remote control stays off unless explicitly opted into.
func TestSkeletonConfigFromEnv_RemoteSocketEmptyByDefault(t *testing.T) {
	t.Setenv(daemon.EnvStateDir, t.TempDir())

	cfg, ok := skeletonConfigFromEnv()
	if !ok {
		t.Fatal("skeletonConfigFromEnv() ok = false, want true")
	}
	if cfg.RemoteSocketPath != "" {
		t.Errorf("cfg.RemoteSocketPath = %q, want empty (remote control must default off)", cfg.RemoteSocketPath)
	}
}
