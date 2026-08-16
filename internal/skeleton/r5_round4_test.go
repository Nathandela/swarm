package skeleton

// FAILING-FIRST (TDD RED, GG-5) for the Wave R5 round-4 review fix-pack (bead
// agents-tracker-hggx.6), BLOCKER 1's PROOF BAR: the wave's primary verb could not
// launch ANY real provider on a real machine.
//
// internal/protocol/remote_launch.go composes the remote launch spec with
// ClientEnv: nil (correct -- ADR-007 D8 forbids phone-supplied env), and coreAPI.Launch
// resolves the adapter's bare argv0 through lookPathIn, which reads PATH from THAT env
// and nothing else. With nil env there is no PATH, so `claude` and `codex` both failed
// with "resolve <agent> binary: agent binary <agent> not found on the agent PATH" --
// every session_launch naming a production preset refused at cc.srv.d.Launch(spec).
// D8's OTHER half ("env comes from daemon policy") did not exist.
//
// The existing R5 skeleton suite could not see this: it bypassed coreAPI.Launch with a
// pre-supplied Argv AND a ClientEnv carrying PATH. This test takes the path the phone
// actually takes -- the REAL assembled coreAPI, a PRODUCTION-shaped preset spec
// (AgentType "claude", options, initial prompt), ClientEnv nil, fakeAgentBin "" (the
// production gate armed) -- against a real agent binary reachable only from the
// DAEMON's own PATH.
//
// It must fail (launch refused: binary not found on the agent PATH) until the
// daemon-policy env lands.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
)

// TestR5Round4_RemoteLaunchOfAProductionPresetResolvesOnTheDaemonsOwnPath is the
// orchestrator's stated proof bar for BLOCKER 1.
func TestR5Round4_RemoteLaunchOfAProductionPresetResolvesOnTheDaemonsOwnPath(t *testing.T) {
	buildBinaries(t) // needs the ambient PATH; runs before the PATH is re-pointed

	// A real executable named exactly what the claude adapter puts in argv[0], on a
	// directory that exists ONLY in the daemon process environment -- never in any
	// spec the phone can influence.
	binDir := t.TempDir()
	agentBin := filepath.Join(binDir, "claude")
	if err := os.WriteFile(agentBin, []byte("#!/bin/sh\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	dir, err := os.MkdirTemp("/tmp", "swskr4")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	core, err := daemon.Open(daemon.Config{
		StateDir:    dir,
		SocketPath:  filepath.Join(dir, "d.sock"),
		LockPath:    filepath.Join(dir, "d.lock"),
		LogPath:     filepath.Join(dir, "d.log"),
		ShimBinary:  swarmBin,
		MaxSessions: 8,
	})
	if err != nil {
		t.Fatalf("daemon.Open: %v", err)
	}
	t.Cleanup(func() { _ = core.Close() })

	// fakeAgentBin "" == a REAL install: the GG-6 production gate is armed and no
	// dev/test escape hatch is available to this launch.
	api := newCoreAPI(core, "", "mach1")

	// Exactly what internal/protocol/remote_launch.go composes from a resolved preset.
	spec := daemon.LaunchSpec{
		AgentType:     "claude",
		Cwd:           t.TempDir(),
		ClientEnv:     nil,
		Cols:          80,
		Rows:          24,
		Options:       map[string]string{"model": "sonnet"},
		OperationID:   "devA:01JR4PROOF",
		InitialPrompt: "fix the flaky test",
	}
	m, err := api.Launch(spec)
	if err != nil {
		t.Fatalf("remote-shaped launch of a production preset refused: %v\n"+
			"The phone contributes no env by design (ADR-007 D8), so argv0 resolution and the "+
			"agent's own environment must both come from DAEMON POLICY -- the daemon's process "+
			"environment through the existing allowlist. Without it the wave's primary verb "+
			"cannot launch any real provider.", err)
	}
	t.Cleanup(func() { _ = core.Kill(m.ID) })

	if !envHasPrefix(m.Env, "PATH=") {
		t.Errorf("launched session env = %v, want a PATH from daemon policy: the agent process "+
			"itself needs one, not just the argv0 lookup", m.Env)
	}
	if home := os.Getenv("HOME"); home != "" && !envHasPrefix(m.Env, "HOME=") {
		t.Errorf("launched session env = %v, want the daemon's HOME: an agent CLI with no HOME "+
			"cannot find its own credentials", m.Env)
	}
}

// TestR5Round4_ProductionLaunchStillFailsClearlyWhenTheBinaryIsAbsent: the fix must not
// turn a missing provider into a silent success. With the daemon's PATH pointing
// nowhere useful, the launch still refuses with the binary-not-found reason.
func TestR5Round4_ProductionLaunchStillFailsClearlyWhenTheBinaryIsAbsent(t *testing.T) {
	buildBinaries(t)
	t.Setenv("PATH", t.TempDir()) // an empty PATH directory: no provider anywhere

	dir, err := os.MkdirTemp("/tmp", "swskr4x")
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	core, err := daemon.Open(daemon.Config{
		StateDir:    dir,
		SocketPath:  filepath.Join(dir, "d.sock"),
		LockPath:    filepath.Join(dir, "d.lock"),
		LogPath:     filepath.Join(dir, "d.log"),
		ShimBinary:  swarmBin,
		MaxSessions: 8,
	})
	if err != nil {
		t.Fatalf("daemon.Open: %v", err)
	}
	t.Cleanup(func() { _ = core.Close() })

	api := newCoreAPI(core, "", "mach1")
	m, err := api.Launch(daemon.LaunchSpec{
		AgentType: "codex", Cwd: t.TempDir(), ClientEnv: nil, Cols: 80, Rows: 24,
		Options: map[string]string{}, OperationID: "devA:01JR4ABSENT",
	})
	if err == nil {
		_ = core.Kill(m.ID)
		t.Fatal("a launch with no codex binary anywhere on the daemon PATH succeeded; a missing " +
			"provider must stay a clear refusal")
	}
	if !strings.Contains(err.Error(), "not found on the agent PATH") {
		t.Errorf("refusal = %v, want the binary-not-found reason", err)
	}
}

// envHasPrefix reports whether env carries an entry with the given KEY= prefix.
func envHasPrefix(env []string, prefix string) bool {
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return true
		}
	}
	return false
}
