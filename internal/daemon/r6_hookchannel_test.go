package daemon

// R6 REVIEW FIX-PACK ROUND 1 (FAILING-FIRST, TDD RED, GG-5), HIGH 3 + HIGH 4: the launch
// path is what makes playbook §6.1's structured channel EXIST. Before this, shimSpawnConfig
// carried no hook_socket_path and no drain token, so the shim never bound its listener,
// `swarm hook` never had EnvHookSocket to feed hookclient.PostSmart, and the shim's own
// DRAIN gate never ran because nothing minted a value for it.
//
// THE SEAMS THIS FILE PINS (undefined symbols -> compile-fail RED):
//
//	// launch.go
//	type shimSpawnConfig struct{ ...; HookSocketPath, HookDrainToken string }
//	func hookSocketPath(stateDir, id string) string   // deterministic, like shimSocketPath
//	func injectHookEnv(filtered []string, id, token, sock, seqFile, hookSock string, capture []string) []string
//	type HookChannel struct{ SocketPath, DrainToken, CursorPath string }
//	func (d *Daemon) SessionHookChannel(id string) (HookChannel, bool)

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/hookclient"
	"github.com/Nathandela/swarm/internal/persist"
)

// The agent's `swarm hook` reaches the shim's socket only if the launch path injects it,
// POST-filter like every other per-session hook variable (FilterEnv would strip it).
func TestInjectHookEnv_CarriesTheShimHookSocket(t *testing.T) {
	filtered := persist.FilterEnv([]string{"CONDA_PREFIX=/x"})
	got := injectHookEnv(filtered, "sid-1", "tok-abc", "/run/d.sock", "/state/sid-1/hook.seq", "/state/sid-1/hook.sock", nil)

	if lineIndex(got, hookclient.EnvHookSocket+"=/state/sid-1/hook.sock") < 0 {
		t.Fatalf("injected env missing %s; got %v", hookclient.EnvHookSocket, got)
	}
}

func TestNewHookDrainToken_RandomPerSession(t *testing.T) {
	a, err := newHookDrainToken()
	if err != nil {
		t.Fatalf("newHookDrainToken: %v", err)
	}
	b, err := newHookDrainToken()
	if err != nil {
		t.Fatalf("newHookDrainToken: %v", err)
	}
	if a == "" || a == b || len(a) < 32 {
		t.Fatalf("drain token is not a fresh per-session secret: %q vs %q", a, b)
	}
}

// The whole channel, as the daemon actually writes it at spawn: a real Launch, then the
// 0600 shim-launch.json read back. The drain token has to live THERE and only there --
// shims outlive daemons, so a restarted daemon must recover the token it minted rather
// than lock itself out of its own sessions' spools.
func TestLaunch_MintsTheHookChannelAndPersistsItWithTheLaunchConfig(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	m, _ := launchAnnounce(t, d)

	ch, ok := d.SessionHookChannel(m.ID)
	if !ok {
		t.Fatalf("SessionHookChannel(%s) not found after a launch", m.ID)
	}
	if want := hookSocketPath(d.cfg.StateDir, m.ID); ch.SocketPath != want {
		t.Fatalf("SessionHookChannel.SocketPath = %q, want the deterministic %q", ch.SocketPath, want)
	}
	if ch.DrainToken == "" {
		t.Fatalf("SessionHookChannel.DrainToken is empty: the shim's DRAIN gate never runs")
	}
	if ch.CursorPath == "" || !strings.HasPrefix(ch.CursorPath, d.sessionDir(m.ID)) {
		t.Fatalf("SessionHookChannel.CursorPath = %q, want a path inside the session dir %q", ch.CursorPath, d.sessionDir(m.ID))
	}

	raw, err := os.ReadFile(filepath.Join(d.sessionDir(m.ID), shimLaunchConfigFile))
	if err != nil {
		t.Fatalf("read the persisted launch config: %v", err)
	}
	if !strings.Contains(string(raw), `"hook_socket_path"`) || !strings.Contains(string(raw), `"hook_drain_token"`) {
		t.Fatalf("the persisted launch config carries no hook channel: %s", raw)
	}
	// The DRAIN secret must never travel in the agent's own environment: the agent (and
	// every hook script it spawns) is the least-trusted party, and DRAIN is destructive
	// (FoldSeq compacts on the caller's say-so) and read-everything.
	var lc shimSpawnConfig
	if err := json.Unmarshal(raw, &lc); err != nil {
		t.Fatalf("decode the persisted launch config: %v", err)
	}
	for _, kv := range lc.Env {
		if strings.Contains(kv, ch.DrainToken) {
			t.Fatalf("the drain token reached the agent env entry %q", kv)
		}
	}
	if lineIndex(lc.Env, hookclient.EnvHookSocket+"="+ch.SocketPath) < 0 {
		t.Fatalf("agent env carries no %s=%s: `swarm hook` would keep taking the daemon fallback", hookclient.EnvHookSocket, ch.SocketPath)
	}
}

// A pre-R6 session dir (a launch config written before this field existed) reports no
// channel rather than a fabricated one -- the compat default the shim itself uses.
func TestSessionHookChannel_AbsentForAPreR6LaunchConfig(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	dir := d.sessionDir("old-session")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, shimLaunchConfigFile), []byte(`{"session_id":"old-session","env":["PATH=/usr/bin"]}`), 0o600); err != nil {
		t.Fatalf("write a pre-R6 launch config: %v", err)
	}
	if ch, ok := d.SessionHookChannel("old-session"); ok {
		t.Fatalf("SessionHookChannel over a pre-R6 launch config = (%+v, true), want not-found", ch)
	}
}
