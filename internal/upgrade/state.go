package upgrade

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// State is one run's durable outcome, written on EVERY run -- including the
// refusals and the offline skips -- because the unit exit code is deliberately
// lossy (a deferral is green) and this file is what `swarm doctor` and the TUI's
// quiet line read instead (committee C3: a month of failed downloads must not be
// invisible).
type State struct {
	CheckedAt time.Time `json:"checked_at"`
	Installed string    `json:"installed"`
	Latest    string    `json:"latest,omitempty"`
	Owner     string    `json:"owner,omitempty"`
	// Outcome: "current", "staged", "offline", "busy", or "refused-dev",
	// "refused-owner", "refused-downgrade", "failed-<step>". The fetch half's
	// outcome ONLY -- activation (R3) records its own, separately, because
	// merging them is how a checksum failure once hid behind a green converge.
	Outcome string `json:"outcome"`
	Detail  string `json:"detail,omitempty"`
	// StagedVersion is set while a verified build sits in the staging dir
	// awaiting activation; "" otherwise.
	StagedVersion string `json:"staged_version,omitempty"`
}

// StatePath is where the transaction records each run. cmd/swarm's doctor reads
// the same function, so reader and writer cannot drift on the location.
func StatePath(stateDir string) string { return filepath.Join(stateDir, "upgrade.json") }

// nowFn is time.Now, a variable only so a test can pin the stamp.
var nowFn = time.Now

// writeState is atomic (temp + rename) and 0600 like every state-dir artifact.
func writeState(stateDir string, s State) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(stateDir, "upgrade.json.tmp*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	return os.Rename(name, StatePath(stateDir))
}

// ReadState loads the last run's record; os.ErrNotExist when none ever ran.
func ReadState(stateDir string) (State, error) {
	data, err := os.ReadFile(StatePath(stateDir))
	if err != nil {
		return State{}, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}, err
	}
	return s, nil
}
