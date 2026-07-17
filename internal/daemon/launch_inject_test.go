package daemon

// F6 (E10.1/G4): the launch path injects, POST allowlist-filter, the four
// per-session hook variables the agent's `swarm hook` needs to authenticate to the
// daemon — session id, a fresh random token, the daemon socket, and the monotonic
// counter file. They are added AFTER FilterEnv (S-2) deliberately: the filter would
// otherwise strip them. Registering the token with a running engine is Epic 8's
// assembly; here we prove the injection + token randomness.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/hookclient"
	"github.com/Nathandela/swarm/internal/persist"
)

// injectHookEnv appends the four hook vars after the allowlist filter, so a
// stripped var stays stripped while the injected ones survive.
func TestInjectHookEnv_PostFilter(t *testing.T) {
	const secret = "SWARMLEAKCANARY=leak" // not allowlisted
	const allowed = "CONDA_PREFIX=/x"     // allowlisted (persist/env.go)
	filtered := persist.FilterEnv([]string{secret, allowed})

	got := injectHookEnv(filtered, "sid-1", "tok-abc", "/run/d.sock", "/state/sid-1/hook.seq")

	if lineIndex(got, secret) >= 0 || strings.Contains(strings.Join(got, "\n"), "SWARMLEAKCANARY") {
		t.Fatalf("non-allowlisted var survived injection: %v", got)
	}
	for _, want := range []string{
		allowed,
		hookclient.EnvSessionID + "=sid-1",
		hookclient.EnvToken + "=tok-abc",
		hookclient.EnvSocket + "=/run/d.sock",
		hookclient.EnvSequenceFile + "=/state/sid-1/hook.seq",
	} {
		if lineIndex(got, want) < 0 {
			t.Fatalf("injected env missing %q; got %v", want, got)
		}
	}
}

func TestNewHookToken_RandomPerSession(t *testing.T) {
	a, err := newHookToken()
	if err != nil {
		t.Fatalf("newHookToken: %v", err)
	}
	b, err := newHookToken()
	if err != nil {
		t.Fatalf("newHookToken: %v", err)
	}
	if a == "" || b == "" {
		t.Fatalf("empty token(s): %q %q", a, b)
	}
	if a == b {
		t.Fatalf("token not random: both invocations returned %q", a)
	}
	if len(a) < 32 {
		t.Fatalf("token %q too short to be a meaningful secret", a)
	}
}

// End-to-end: a real Launch delivers all four hook vars to the agent's ACTUAL
// environment (the env-dump agent writes exactly the env it was given), the
// non-allowlisted var never reaches it, and a second session gets a distinct token.
func TestLaunch_InjectsHookEnvToAgent(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)

	const secret = "SWARMLEAKCANARY=leak-9x"
	tok1 := launchEnvDumpAndReadToken(t, d, cfg, secret)

	// A second launch must inject a DIFFERENT random token.
	tok2 := launchEnvDumpAndReadToken(t, d, cfg, secret)
	if tok1 == "" || tok2 == "" {
		t.Fatalf("agent did not receive a hook token: %q %q", tok1, tok2)
	}
	if tok1 == tok2 {
		t.Fatalf("two sessions received the same token %q; tokens must be random per session", tok1)
	}
}

// launchEnvDumpAndReadToken launches an env-dump agent, asserts the four hook vars
// reached its real env and the secret did not, and returns the injected token.
func launchEnvDumpAndReadToken(t *testing.T, d *Daemon, cfg Config, secret string) string {
	t.Helper()
	envFile := filepath.Join(t.TempDir(), "agent-env.txt")
	spec := LaunchSpec{
		AgentType: "fake",
		Argv:      []string{selfExe(t), markerEnvDump, envFile},
		Cwd:       t.TempDir(),
		ClientEnv: []string{"PATH=" + os.Getenv("PATH"), secret},
		Cols:      80,
		Rows:      24,
	}
	m, err := d.Launch(spec)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	t.Cleanup(func() { _ = d.Kill(m.ID) })

	waitFile(t, envFile, pollTimeout)
	raw, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read agent env: %v", err)
	}
	dump := string(raw)

	if strings.Contains(dump, "SWARMLEAKCANARY") {
		t.Fatalf("non-allowlisted var leaked to the agent:\n%s", dump)
	}
	if got := envValue(dump, hookclient.EnvSessionID); got != m.ID {
		t.Fatalf("%s=%q, want %q", hookclient.EnvSessionID, got, m.ID)
	}
	if got := envValue(dump, hookclient.EnvSocket); got != cfg.SocketPath {
		t.Fatalf("%s=%q, want %q", hookclient.EnvSocket, got, cfg.SocketPath)
	}
	wantSeq := filepath.Join(cfg.StateDir, m.ID, "hook.seq")
	if got := envValue(dump, hookclient.EnvSequenceFile); got != wantSeq {
		t.Fatalf("%s=%q, want %q", hookclient.EnvSequenceFile, got, wantSeq)
	}
	return envValue(dump, hookclient.EnvToken)
}

// lineIndex returns the index of an exact KEY=VALUE line in env, or -1.
func lineIndex(env []string, want string) int {
	for i, e := range env {
		if e == want {
			return i
		}
	}
	return -1
}

// envValue extracts the value of key from a KEY=VALUE-per-line env dump.
func envValue(dump, key string) string {
	for _, line := range strings.Split(dump, "\n") {
		if strings.HasPrefix(line, key+"=") {
			return line[len(key)+1:]
		}
	}
	return ""
}
