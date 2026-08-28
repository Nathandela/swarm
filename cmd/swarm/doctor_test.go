package main

// `swarm doctor`'s two load-bearing properties (lifecycle plan R1):
//
//  1. THE NON-SPAWNING NEGATIVE CONTROL. Every other client verb ensures a
//     daemon (D-1); a cron'd `swarm ls` once rewrote a good daemon.env from a
//     bare environment that way. A diagnostic tool that could cause the incident
//     it diagnoses is worse than none, so doctor run against a state dir with no
//     daemon must leave it exactly as found: no daemon, no socket, no pidfile,
//     no daemon.env.
//
//  2. The degraded-backend surfacing: a persisted session whose meta carries
//     backend_plan_error is a FAIL finding naming the reason, and doctor exits 1.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/persist"
)

// doctorEnv points the client config at a fresh, daemon-less state dir. The
// paths ride the same SWARM_DAEMON_* overrides every client role honors.
func doctorEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SWARM_DAEMON_STATE", dir)
	t.Setenv("SWARM_DAEMON_SOCK", filepath.Join(dir, "d.sock"))
	t.Setenv("SWARM_DAEMON_LOCK", filepath.Join(dir, "d.lock"))
	t.Setenv("SWARM_DAEMON_LOG", filepath.Join(dir, "d.log"))
	return dir
}

func TestDoctorNeverSpawnsADaemon(t *testing.T) {
	dir := doctorEnv(t)
	var out, errb bytes.Buffer
	code := runDoctor(nil, &out, &errb)
	if code != 0 {
		t.Fatalf("doctor on an idle machine = exit %d, stderr %q; an idle machine is healthy", code, errb.String())
	}
	for _, f := range []string{"d.sock", "d.lock", "daemon.pid", "daemon.env"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			t.Errorf("doctor created %s -- it spawned or touched a daemon it must only observe", f)
		}
	}
	if !strings.Contains(out.String(), "no daemon running") {
		t.Errorf("doctor output does not state the idle daemon fact:\n%s", out.String())
	}
}

func TestDoctorFailsOnAPersistedBackendPlanError(t *testing.T) {
	dir := doctorEnv(t)
	// A degraded session, exactly as launch() persists it.
	sessDir := filepath.Join(dir, "brokensession")
	if err := os.MkdirAll(sessDir, 0o700); err != nil {
		t.Fatal(err)
	}
	m := persist.Meta{
		ID:               "brokensession",
		AgentType:        "codex",
		BackendPlanError: `backend Program "codex" does not resolve on PATH`,
	}
	writeMetaFile(t, sessDir, m)

	var out, errb bytes.Buffer
	code := runDoctor([]string{"--json"}, &out, &errb)
	if code != 1 {
		t.Fatalf("doctor with a degraded session = exit %d, want 1; stderr %q", code, errb.String())
	}
	var findings []doctorFinding
	if err := json.Unmarshal(out.Bytes(), &findings); err != nil {
		t.Fatalf("doctor --json output does not decode: %v\n%s", err, out.String())
	}
	found := false
	for _, f := range findings {
		if f.Check == "session:brokensession" {
			found = true
			if f.Status != "fail" {
				t.Errorf("degraded session finding status = %q, want fail", f.Status)
			}
			if !strings.Contains(f.Detail, "does not resolve on PATH") {
				t.Errorf("finding does not carry the persisted reason: %q", f.Detail)
			}
		}
	}
	if !found {
		t.Errorf("no finding for the degraded session in %s", out.String())
	}
}

// writeMetaFile persists a meta the way the store does, via the persist API, so
// the test cannot drift from the durable format.
func writeMetaFile(t *testing.T, sessDir string, m persist.Meta) {
	t.Helper()
	store, err := persist.NewStore(filepath.Dir(sessDir))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(m); err != nil {
		t.Fatal(err)
	}
}
