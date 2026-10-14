package shim

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/shimwire"
)

// TestBackendReady_AHealthyBackendSlowerThanTwentySecondsIsStillServed pins the default readiness
// budget against the live measurement: codex app-server start-to-servable rose to 2-8 s on a
// healthy host and past 20 s under host memory/IO stalls (25 never-servable launches since
// 2026-09-04, each degraded for the life of the session). A backend that binds after 25 s must
// be served under the DEFAULT budget instead of being killed.
func TestBackendReady_AHealthyBackendSlowerThanTwentySecondsIsStillServed(t *testing.T) {
	if testing.Short() {
		t.Skip("waits 25 s for a deliberately slow backend")
	}
	dir := r7ShortSessionDir(t)
	sock := filepath.Join(dir, "codex.sock")
	cfg := &BackendConfig{
		Program:    "/bin/sh",
		Args:       []string{"-c", `sleep 25; exec "$0"`, selfExe(t)},
		SocketPath: sock,
		Env: []string{
			r7BackendEnvVar + "=" + r7BackendBind,
			r7BackendEnvVar + "_SOCK=" + sock,
			"PATH=" + os.Getenv("PATH"),
		},
		// ReadyTimeout deliberately unset: production never sets it.
	}
	b, err := startBackend(cfg, dir)
	if b != nil {
		t.Cleanup(func() {
			_ = syscall.Kill(-b.pgid, syscall.SIGKILL)
			<-b.dead
		})
	}
	if err != nil {
		t.Fatalf("startBackend with the default budget: %v; a healthy backend this slow is the "+
			"measured stall case and must not degrade the session for its whole life", err)
	}
}

// TestBackendReady_AFailedBackendStartsTheAgentWithoutWaitingForAGoAhead pins the degraded
// start delay at the readiness budget alone. Once the backend has failed and been contained,
// nothing can send a backend_attach: the daemon sends one only after dialling that backend's
// socket. Waiting out the go-ahead budget too only kept the terminal blank.
func TestBackendReady_AFailedBackendStartsTheAgentWithoutWaitingForAGoAhead(t *testing.T) {
	cfg := r7BackendCfg(t, r7BackendNeverBinds, nil)
	cfg.Backend.ReadyTimeout = 300 * time.Millisecond
	cfg.Backend.GoAheadTimeout = 30 * time.Second
	start := time.Now()
	done := runShimAsync(cfg)

	c := dialShim(t, cfg.SocketPath)
	c.startReader()
	c.hello(shimwire.Version)
	c.attach()
	got := c.waitObserved("INFO_DONE", 20*time.Second)
	elapsed := time.Since(start)
	_ = c.conn.Close()
	waitRun(t, done, 40*time.Second)
	if got == "" || elapsed > 10*time.Second {
		t.Fatalf("degraded agent ran after %s (seen=%t); with a 300 ms readiness budget it must not "+
			"wait out the 30 s go-ahead budget for an attach nothing can send", elapsed, got != "")
	}
	if DegradedAgentDelay != defaultBackendReadyTimeout {
		t.Errorf("DegradedAgentDelay = %s, want the readiness budget %s alone", DegradedAgentDelay, defaultBackendReadyTimeout)
	}
}
