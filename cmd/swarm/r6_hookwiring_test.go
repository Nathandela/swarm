package main

// R6 (bd agents-tracker-hggx.7) FAILING-FIRST (TDD RED, GG-5) tests for cmd/swarm's OWNED
// half of playbook §6.1's structured-capture survival boundary: the `swarm hook` subcommand
// and the shim launch-config wiring, and NOTHING else in this file touches remote/relay/init
// code paths (the assignment's explicit boundary).
//
// requirement 7, verbatim: "the swarm hook CLI keeps working against old shims during the
// transition (feature-detect the shim socket, fall back to the daemon socket, honest about
// which path served)". hookclient.PostSmart (internal/hookclient/r6_shimpost_test.go) is
// where that policy lives; this file pins that runHook actually CALLS it -- reading
// hookclient.EnvHookSocket the same way it already reads EnvSocket/EnvToken/EnvSequenceFile/
// EnvCapture -- rather than continuing to call the old bare hookclient.Post. Every EXISTING
// hook_capture_test.go case sets no EnvHookSocket, so it continues to exercise the daemon-
// only path unchanged; this file is additive.
//
// THE SEAMS THIS FILE PINS:
//
//	// runHook (main.go) reads hookclient.EnvHookSocket and calls hookclient.PostSmart(shimSock,
//	// daemonSock, cb) in place of today's bare hookclient.Post(daemonSock, cb). An empty
//	// EnvHookSocket (unset -- every existing test, and any pre-R6 daemon) makes PostSmart
//	// behave exactly like Post, by PostSmart's own contract.
//
//	// shimLaunchConfig (main.go) gains a field:
//	//     HookSocketPath string `json:"hook_socket_path"`
//	// and the pure mapping runShim already computes inline is extracted so it is testable
//	// without running a real shim process (setsid, a real agent, ...):
//	func shimConfigFromLaunch(lc shimLaunchConfig) shim.Config

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/hookclient"
)

// shimHookSink is hookSink's twin for the shim's hook socket: it additionally writes the
// single ack byte hookclient.PostToShim reads, so a post against it is a durably-accepted
// shim path exactly as a real shim's hook socket would answer.
func shimHookSink(t *testing.T) (sock string, next func() engine.Callback) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "swhookshim")
	if err != nil {
		t.Fatalf("socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock = filepath.Join(dir, "h.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen %s: %v", sock, err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	got := make(chan engine.Callback, 4)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			cb, derr := hookclient.Decode(conn)
			if derr == nil {
				_, _ = conn.Write([]byte{1}) // the shim's single ack byte
				got <- cb
			}
			_ = conn.Close()
		}
	}()
	return sock, func() engine.Callback {
		t.Helper()
		select {
		case cb := <-got:
			return cb
		case <-time.After(10 * time.Second):
			t.Fatal("no hook callback reached the shim sink")
			return engine.Callback{}
		}
	}
}

// assertNothingArrivesAt fails the test if a callback reaches sock within a short grace
// window -- proving the OTHER transport was never touched, not merely that this one wasn't
// asserted on.
func assertNothingArrivesAt(t *testing.T, next func() engine.Callback, label string) {
	t.Helper()
	done := make(chan engine.Callback, 1)
	go func() { done <- next() }()
	select {
	case cb := <-done:
		t.Errorf("a callback (%+v) reached the %s socket; want it untouched", cb, label)
	case <-time.After(300 * time.Millisecond):
	}
}

// ---------------------------------------------------------------------------
// (7) compat: runHook prefers the shim hook socket and never touches the daemon
// socket when the shim durably accepts the post.
// ---------------------------------------------------------------------------

func TestRunHook_PrefersTheShimSocketAndNeverTouchesTheDaemonWhenAcked(t *testing.T) {
	shimSock, nextShim := shimHookSink(t)
	daemonSock, nextDaemon := hookSink(t)

	t.Setenv(hookclient.EnvSessionID, "sid-r6")
	t.Setenv(hookclient.EnvToken, "tok-r6")
	t.Setenv(hookclient.EnvSocket, daemonSock)
	t.Setenv(hookclient.EnvHookSocket, shimSock)
	t.Setenv(hookclient.EnvSequenceFile, filepath.Join(t.TempDir(), "hook.seq"))

	if code := runHook([]string{"Stop"}, strings.NewReader(""), io.Discard); code != 0 {
		t.Fatalf("runHook exit code %d; want 0", code)
	}

	cb := nextShim()
	if cb.SessionID != "sid-r6" || cb.Event != "Stop" {
		t.Errorf("shim received %+v, want the posted callback", cb)
	}
	assertNothingArrivesAt(t, nextDaemon, "daemon")
}

// ---------------------------------------------------------------------------
// (7) compat: EnvHookSocket unset is the existing, unchanged behavior — every hook_capture_
// test.go case already proves this at the capture-carriage level; this pins it at the
// transport-selection level explicitly, so a future change to runHook's dispatch cannot
// silently start dialing a shim socket nobody configured.
// ---------------------------------------------------------------------------

func TestRunHook_EnvHookSocketUnset_GoesStraightToTheDaemon(t *testing.T) {
	daemonSock, nextDaemon := hookSink(t)
	hookEnv(t, daemonSock, nil) // EnvHookSocket deliberately left unset

	if code := runHook([]string{"Stop"}, strings.NewReader(""), io.Discard); code != 0 {
		t.Fatalf("runHook exit code %d; want 0", code)
	}
	cb := nextDaemon()
	if cb.Event != "Stop" {
		t.Errorf("daemon received %+v, want event=Stop", cb)
	}
}

// ---------------------------------------------------------------------------
// (7) compat: an old shim mid-upgrade — EnvHookSocket points at a socket that plainly is not
// there — must still let the hook succeed, against the daemon.
// ---------------------------------------------------------------------------

func TestRunHook_ShimSocketGone_FallsBackToTheDaemon_OldShimDuringUpgrade(t *testing.T) {
	daemonSock, nextDaemon := hookSink(t)
	t.Setenv(hookclient.EnvSessionID, "sid-r6b")
	t.Setenv(hookclient.EnvToken, "tok-r6b")
	t.Setenv(hookclient.EnvSocket, daemonSock)
	t.Setenv(hookclient.EnvHookSocket, filepath.Join(t.TempDir(), "no-such-hook-sock"))
	t.Setenv(hookclient.EnvSequenceFile, filepath.Join(t.TempDir(), "hook.seq"))

	if code := runHook([]string{"Stop"}, strings.NewReader(""), io.Discard); code != 0 {
		t.Fatalf("runHook exit code %d against an absent shim hook socket; want 0 (the CLI must keep working against old shims during the transition)", code)
	}
	cb := nextDaemon()
	if cb.SessionID != "sid-r6b" || cb.Event != "Stop" {
		t.Errorf("daemon received %+v, want the posted callback (fallback must still carry it whole)", cb)
	}
}

// ---------------------------------------------------------------------------
// shim env wiring: shimLaunchConfig's hook_socket_path field reaches shim.Config.
// ---------------------------------------------------------------------------

func TestShimConfigFromLaunch_CarriesHookSocketPath(t *testing.T) {
	lc := shimLaunchConfig{
		SessionID:      "s1",
		Argv:           []string{"/bin/true"},
		Cwd:            "/tmp",
		SocketPath:     "/tmp/c.sock",
		HookSocketPath: "/tmp/h.sock",
		SessionDir:     "/tmp/sess",
		Cols:           80,
		Rows:           24,
		GraceMS:        1000,
	}
	cfg := shimConfigFromLaunch(lc)
	if cfg.HookSocketPath != "/tmp/h.sock" {
		t.Errorf("shim.Config.HookSocketPath = %q, want %q", cfg.HookSocketPath, "/tmp/h.sock")
	}
	// A light regression pin: the fields runShim already mapped inline must still map.
	if cfg.SessionID != lc.SessionID || cfg.SocketPath != lc.SocketPath || cfg.SessionDir != lc.SessionDir {
		t.Errorf("shimConfigFromLaunch dropped an existing field: got %+v from %+v", cfg, lc)
	}
}

// R6 REVIEW FIX-PACK (security, HIGH -- corrected). The ORIGINAL version of this
// test pinned shimConfigFromLaunch deriving HookToken from the agent's own injected
// env (hookclient.EnvToken): a confirmed vulnerability, since that value reaches the
// agent process (and every hook script or child it spawns) by design, letting the
// least-trusted party in the system present it and issue a destructive, read-
// everything DRAIN. The corrected contract: HookToken comes ONLY from the dedicated
// lc.HookDrainToken field, which lc.Env never carries and the agent never sees.
func TestShimConfigFromLaunch_DerivesHookTokenFromDedicatedField(t *testing.T) {
	lc := shimLaunchConfig{
		SessionID:      "s1",
		Argv:           []string{"/bin/true"},
		Env:            []string{"PATH=/usr/bin", hookclient.EnvToken + "=tok-agent-side", "OTHER=1"},
		HookDrainToken: "tok-drain-only",
	}
	cfg := shimConfigFromLaunch(lc)
	if cfg.HookToken != "tok-drain-only" {
		t.Errorf("shim.Config.HookToken = %q, want %q (the dedicated drain token)", cfg.HookToken, "tok-drain-only")
	}
	if cfg.HookToken == "tok-agent-side" {
		t.Fatalf("shim.Config.HookToken equals the agent's own hookclient.EnvToken value -- DRAIN would be gated by a secret the agent process itself holds")
	}
}

func TestShimConfigFromLaunch_NoDrainTokenMeansEmptyHookToken(t *testing.T) {
	// An old launch config (predates this field), or one a future wave has not yet
	// wired a daemon-minted value into: the compat default is "no token configured",
	// not a fabricated one -- and CRUCIALLY never a fallback onto lc.Env's own
	// hook-post token, even though that value is sitting right there.
	lc := shimLaunchConfig{
		SessionID: "s1", Argv: []string{"/bin/true"},
		Env: []string{"PATH=/usr/bin", hookclient.EnvToken + "=tok-agent-side"},
	}
	cfg := shimConfigFromLaunch(lc)
	if cfg.HookToken != "" {
		t.Errorf("shim.Config.HookToken = %q, want empty when the launch config carries no dedicated drain token", cfg.HookToken)
	}
}

// ---------------------------------------------------------------------------
// R6 REVIEW FIX-PACK ROUND 1 (LOW 9): "honest about which path served" (requirement 7)
// was API-only -- PostSmart returned HookPath and runHook threw it away, so an operator
// debugging a partial upgrade had no way to tell whether the shim or the daemon carried
// a post. The interesting case is the only one worth a line: a shim socket WAS
// configured and the daemon carried the post anyway.
// ---------------------------------------------------------------------------

func TestRunHook_FallbackDespiteAConfiguredShimSocketIsReported(t *testing.T) {
	daemonSock, nextDaemon := hookSink(t)
	t.Setenv(hookclient.EnvSessionID, "sid-low9")
	t.Setenv(hookclient.EnvToken, "tok-low9")
	t.Setenv(hookclient.EnvSocket, daemonSock)
	t.Setenv(hookclient.EnvHookSocket, filepath.Join(t.TempDir(), "no-such-hook-sock"))
	t.Setenv(hookclient.EnvSequenceFile, filepath.Join(t.TempDir(), "hook.seq"))

	var stderr strings.Builder
	if code := runHook([]string{"Stop"}, strings.NewReader(""), &stderr); code != 0 {
		t.Fatalf("runHook exit code %d; want 0 (a fallback is not a failure)", code)
	}
	_ = nextDaemon()
	if !strings.Contains(stderr.String(), string(hookclient.HookPathDaemon)) {
		t.Fatalf("runHook said nothing about which path served (stderr=%q); a post that fell back to the daemon despite a configured shim socket is exactly the partial-upgrade state requirement 7 asks to be honest about", stderr.String())
	}
}

func TestRunHook_ShimServedPathStaysQuiet(t *testing.T) {
	shimSock, nextShim := shimHookSink(t)
	daemonSock, _ := hookSink(t)
	t.Setenv(hookclient.EnvSessionID, "sid-low9b")
	t.Setenv(hookclient.EnvToken, "tok-low9b")
	t.Setenv(hookclient.EnvSocket, daemonSock)
	t.Setenv(hookclient.EnvHookSocket, shimSock)
	t.Setenv(hookclient.EnvSequenceFile, filepath.Join(t.TempDir(), "hook.seq"))

	var stderr strings.Builder
	if code := runHook([]string{"Stop"}, strings.NewReader(""), &stderr); code != 0 {
		t.Fatalf("runHook exit code %d; want 0", code)
	}
	_ = nextShim()
	if stderr.String() != "" {
		t.Fatalf("runHook wrote %q on the ordinary shim-served path; a CLI the agent spawns for every hook must stay silent when nothing is wrong", stderr.String())
	}
}

func TestShimConfigFromLaunch_EmptyHookSocketPathMeansDisabled(t *testing.T) {
	// An old (pre-R6) daemon writes a shim-launch.json with no hook_socket_path key at all;
	// json.Unmarshal leaves the Go zero value, and that empty string is what
	// r6_hooksocket_test.go's TestHookSocket_EmptyPathDisablesTheListener already pins as
	// "the listener is not created at all" — this test only pins that the WIRING preserves
	// the zero value rather than substituting a default path.
	lc := shimLaunchConfig{SessionID: "s1", Argv: []string{"/bin/true"}, SocketPath: "/tmp/c.sock"}
	cfg := shimConfigFromLaunch(lc)
	if cfg.HookSocketPath != "" {
		t.Errorf("shim.Config.HookSocketPath = %q, want empty when the launch config omits it", cfg.HookSocketPath)
	}
}
