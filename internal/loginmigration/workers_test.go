package loginmigration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/procstart"
)

func TestEnrollmentCustodyRequiresExactStoppedGeneration(t *testing.T) {
	for _, kind := range []string{"stopped", "orphan-stopped", "runner", "child", "uncontained", "missing-progress", "generation", "missing-custody", "consumed"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			id := strings.Repeat("d", 32)
			start, err := procstart.StartTime(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			job := map[string]any{"id": id, "provider": "codex", "state": "failed", "generation": 1, "worker": oldProcess{}}
			progress := map[string]any{"schema_version": 1, "job_id": id, "generation": 1, "phase": "failed", "worker": oldProcess{}, "writers_stopped": true, "native_writers_stopped": true}
			switch kind {
			case "runner":
				progress["runner"] = oldProcess{PID: os.Getpid(), Start: start}
			case "child":
				progress["children"] = []oldProcess{{PID: os.Getpid(), Start: start}}
			case "uncontained":
				progress["native_writers_stopped"] = false
			case "generation":
				progress["generation"] = 2
			case "missing-custody", "consumed":
				job["worker"] = oldProcess{PID: 99999999, Start: 1}
				if kind == "consumed" {
					job["custody_consumed"] = true
					job["state"] = "admitted"
				}
			}
			if kind != "orphan-stopped" {
				put(t, filepath.Join(f.state, "accounts", "enrollment.json"), map[string]any{"schema_version": 1, "jobs": map[string]any{id: job}})
			}
			if kind != "missing-custody" && kind != "consumed" {
				put(t, filepath.Join(f.state, "accounts", "jobs", id, "worker-config.json"), map[string]any{"schema_version": 2, "state_root": f.state, "job_id": id, "generation": 1, "provider": "codex", "candidate_profile_generation": strings.Repeat("b", 32)})
				if kind != "missing-progress" {
					put(t, filepath.Join(f.state, "accounts", "jobs", id, "worker-progress.json"), progress)
				}
			}
			_, _, err = inventoryEnrollmentWorkers(f.state)
			allowed := kind == "stopped" || kind == "orphan-stopped" || kind == "consumed"
			if allowed && err != nil {
				t.Fatal(err)
			}
			if !allowed && err == nil {
				t.Fatal("unproven native worker accepted")
			}
		})
	}
}
func TestForgedCleanupCannotEraseCredentialBeforeDurableImport(t *testing.T) {
	f := newFixture(t)
	before, err := os.ReadFile(filepath.Join(f.profile, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	plan, staged, err := inventory(f.state)
	if err != nil {
		t.Fatal(err)
	}
	plan.Phase = "cleanup"
	for path, raw := range staged {
		put(t, filepath.Join(f.state, path), raw)
	}
	put(t, filepath.Join(f.state, "logins", "migration.json"), plan)
	if err = RunLocked(f.state); err == nil {
		t.Fatal("cleanup accepted without durable imported destination")
	}
	after, err := os.ReadFile(filepath.Join(f.profile, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("source credential changed")
	}
}
