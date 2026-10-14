package shim

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
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
