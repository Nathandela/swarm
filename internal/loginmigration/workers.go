package loginmigration

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
)

type oldProcess struct {
	PID   int   `json:"pid"`
	Start int64 `json:"start_time"`
}

func stoppedIdentity(p oldProcess) bool {
	return p.PID == 0 && p.Start == 0 || p.PID > 0 && p.Start > 0 && !live(p.PID, p.Start)
}

type oldJob struct {
	ID         string     `json:"id"`
	Provider   string     `json:"provider"`
	Generation uint64     `json:"generation"`
	State      string     `json:"state"`
	Worker     oldProcess `json:"worker"`
	Consumed   bool       `json:"custody_consumed"`
}

func terminalJob(state string) bool {
	switch state {
	case "complete", "cancelled", "canceled", "failed", "expired", "admitted":
		return true
	}
	return false
}

func inventoryEnrollmentWorkers(state string) (map[string]bool, []string, error) {
	providers := map[string]bool{}
	homes := []string{}
	var document struct {
		Jobs map[string]oldJob `json:"jobs"`
	}
	if err := readJSON(filepath.Join(state, "accounts", "enrollment.json"), &document); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	jobs := document.Jobs
	for id, job := range jobs {
		if !hexID(id, 32) || job.ID != "" && job.ID != id || !terminalJob(job.State) {
			return nil, nil, fmt.Errorf("%w: old sign-in job %s is %s", ErrUnsupported, id, job.State)
		}
		if !stoppedIdentity(job.Worker) {
			return nil, nil, ErrLiveWriter
		}
		if job.Provider != "" {
			if job.Provider != "claude" && job.Provider != "codex" {
				return nil, nil, ErrUnsupported
			}
			providers[job.Provider] = true
		}
	}
	base := filepath.Join(state, "accounts", "jobs")
	entries, err := os.ReadDir(base)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		id := entry.Name()
		if !entry.IsDir() || !hexID(id, 32) {
			return nil, nil, ErrUnsupported
		}
		seen[id] = true
		var cfg struct {
			Schema     int    `json:"schema_version"`
			StateRoot  string `json:"state_root"`
			JobID      string `json:"job_id"`
			Generation uint64 `json:"generation"`
			Provider   string `json:"provider"`
			Candidate  string `json:"candidate_id"`
			Profile    string `json:"candidate_profile_generation"`
		}
		path := filepath.Join(base, id)
		if err = readJSON(filepath.Join(path, "worker-config.json"), &cfg); err != nil {
			return nil, nil, ErrUnsupported
		}
		if cfg.Schema != 1 && cfg.Schema != 2 || cfg.StateRoot != state || cfg.JobID != id || cfg.Generation == 0 || (cfg.Provider != "claude" && cfg.Provider != "codex") {
			return nil, nil, ErrUnsupported
		}
		profile := cfg.Profile
		if profile == "" {
			profile = cfg.Candidate
		}
		if !hexID(profile, 32) {
			return nil, nil, ErrUnsupported
		}
		providers[cfg.Provider] = true
		homes = append(homes, filepath.Join(state, "accounts", "profiles", profile))
		var progress struct {
			Schema        int          `json:"schema_version"`
			JobID         string       `json:"job_id"`
			Generation    uint64       `json:"generation"`
			Phase         string       `json:"phase"`
			Worker        oldProcess   `json:"worker"`
			Runner        oldProcess   `json:"runner"`
			Children      []oldProcess `json:"children"`
			Stopped       bool         `json:"writers_stopped"`
			NativeStopped bool         `json:"native_writers_stopped"`
		}
		if err = readJSON(filepath.Join(path, "worker-progress.json"), &progress); err != nil {
			return nil, nil, ErrLiveWriter
		}
		if progress.Schema != 1 || progress.JobID != id || progress.Generation != cfg.Generation {
			return nil, nil, ErrUnsupported
		}
		if job, exists := jobs[id]; exists && (job.Generation != cfg.Generation || job.Worker != progress.Worker || job.Provider != "" && job.Provider != cfg.Provider) {
			return nil, nil, ErrUnsupported
		}
		if !progress.Stopped || !progress.NativeStopped || !stoppedIdentity(progress.Worker) || !stoppedIdentity(progress.Runner) {
			return nil, nil, ErrLiveWriter
		}
		for _, child := range progress.Children {
			if !stoppedIdentity(child) {
				return nil, nil, ErrLiveWriter
			}
		}
		if progress.Phase != "ready" && progress.Phase != "failed" && progress.Phase != "canceled" {
			return nil, nil, ErrUnsupported
		}
	}
	for id, job := range jobs {
		if seen[id] {
			continue
		}
		// Only the old manager's durable consumed-custody mark permits an absent
		// worker directory after a worker was spawned; PID death alone cannot.
		if job.Worker.PID != 0 && (!job.Consumed || (job.State != "admitted" && job.State != "cancelled")) {
			return nil, nil, ErrLiveWriter
		}
	}
	return providers, homes, nil
}
func inventoryCheckWorkers(state string) error {
	base := filepath.Join(state, "accounts", "checks")
	entries, err := os.ReadDir(base)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !hexID(entry.Name(), 32) {
			return ErrUnsupported
		}
		var ref struct {
			Schema     int        `json:"schema_version"`
			Generation string     `json:"generation"`
			Worker     oldProcess `json:"worker"`
		}
		raw, err := readFile(filepath.Join(base, entry.Name(), "worker.json"), maxJSON, true)
		if err != nil {
			return err
		}
		if validJSON(raw) != nil || json.Unmarshal(raw, &ref) != nil || ref.Schema != 2 || ref.Generation != entry.Name() {
			return ErrUnsupported
		}
		if !stoppedIdentity(ref.Worker) {
			return ErrLiveWriter
		}
		var stopped struct {
			Schema  int             `json:"schema_version"`
			Ref     json.RawMessage `json:"ref"`
			Native  oldProcess      `json:"native"`
			Stopped bool            `json:"writers_stopped"`
		}
		if err = readJSON(filepath.Join(base, entry.Name(), "stopped.json"), &stopped); err != nil || stopped.Schema != ref.Schema || !stopped.Stopped || !stoppedIdentity(stopped.Native) {
			return ErrLiveWriter
		}
		var a, b any
		if json.Unmarshal(raw, &a) != nil || json.Unmarshal(stopped.Ref, &b) != nil || !reflect.DeepEqual(a, b) {
			return ErrUnsupported
		}
	}
	return nil
}
