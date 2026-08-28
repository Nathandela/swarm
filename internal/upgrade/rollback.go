package upgrade

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Rollback (lifecycle R3): restore the binaries Activate set aside, with the
// two guards the committee demanded of it. A rollback without a HOLD undoes
// itself within 24h -- the nightly re-stages the very release that was just
// backed out (Fable H1) -- so restoring writes the bad version into the hold
// file, which Stage consults. And a rollback across a persistence-schema bump
// bricks the daemon it restores (an old build refuses future-schema metas at
// Open), so it is REFUSED from the slots' own compatibility card before
// anything is touched (Opus A4).

// ErrNothingToRollBack is Rollback's answer when no slots exist.
var ErrNothingToRollBack = errors.New("upgrade: no rollback slots; nothing was ever activated from here")

// holdPath is the version Stage must skip: written by rollback, cleared by the
// next release that isn't it.
func holdPath(stateDir string) string { return filepath.Join(stateDir, "upgrade", "hold") }

// HeldVersion reads the hold; "" when none.
func HeldVersion(stateDir string) string {
	data, err := os.ReadFile(holdPath(stateDir))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// Rollback restores the slot binaries over BinPath and execs the RESTORED
// binary's converge -- the same handoff rule as activation, for the same
// reason. On success it does not return.
func Rollback(opts ActivateOptions) (State, error) {
	s := State{}
	unlock, err := lockUpgrade(opts.StateDir)
	if err != nil {
		s.Outcome, s.Detail = "busy", err.Error()
		return s, recordState(opts.StateDir, &s)
	}
	defer unlock()

	prev := PrevDir(opts.StateDir)
	tagBytes, err := os.ReadFile(filepath.Join(prev, "VERSION"))
	if err != nil {
		return s, ErrNothingToRollBack
	}
	prevTag := strings.TrimSpace(string(tagBytes))

	// The schema guard: every persisted meta must be loadable by the build being
	// restored, or the rolled-back daemon opens onto a board it half-refuses,
	// half-omits. The slots' card says what that build supports; the metas on
	// disk say what they need. Pure reads, refused before anything moves.
	var prevCard CompatManifest
	if data, err := os.ReadFile(filepath.Join(prev, "compat.json")); err == nil {
		_ = json.Unmarshal(data, &prevCard)
	}
	if prevCard.Schema > 0 {
		if need, id := maxPersistedSchema(opts.StateDir); need > prevCard.Schema {
			s.Outcome = "refused-schema"
			s.Detail = fmt.Sprintf("session %s persists schema v%d; the rollback build supports v%d and would refuse it at Open -- end and delete newer-schema sessions first", id, need, prevCard.Schema)
			return s, recordState(opts.StateDir, &s)
		}
	}

	// The hold: the version being rolled AWAY FROM must not come back tonight.
	if err := os.WriteFile(holdPath(opts.StateDir), []byte("v"+versionVersion()+"\n"), 0o600); err != nil {
		s.Outcome, s.Detail = "failed-hold", err.Error()
		return s, recordState(opts.StateDir, &s)
	}

	targets := []string{opts.BinPath}
	if remote := filepath.Join(filepath.Dir(opts.BinPath), "swarm-remote"); fileExists(remote) && fileExists(filepath.Join(prev, "swarm-remote")) {
		targets = append(targets, remote)
	}
	for _, dst := range targets {
		if err := installFile(filepath.Join(prev, filepath.Base(dst)), dst); err != nil {
			s.Outcome, s.Detail = "failed-install", fmt.Sprintf("%s: %v", dst, err)
			return s, recordState(opts.StateDir, &s)
		}
	}

	s.Outcome = "rolled-back"
	s.Detail = fmt.Sprintf("restored %s over v%s; v%s is held from the nightly until a newer release ships", prevTag, versionVersion(), versionVersion())
	if err := recordState(opts.StateDir, &s); err != nil {
		return s, err
	}
	env := append(os.Environ(), handoffGuardEnv+"=1")
	if err := execFn(opts.BinPath, []string{"swarm", "daemon", "restart", "--unattended"}, env); err != nil {
		s.Outcome, s.Detail = "failed-handoff", err.Error()
		return s, recordState(opts.StateDir, &s)
	}
	return s, nil // unreachable in production
}

// maxPersistedSchema scans metas plainly for the highest schema_version on disk.
func maxPersistedSchema(stateDir string) (int, string) {
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		return 0, ""
	}
	max, id := 0, ""
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, ok := readMetaFile(filepath.Join(stateDir, e.Name(), "meta.json"))
		if ok && m.SchemaVersion > max {
			max, id = m.SchemaVersion, m.ID
		}
	}
	return max, id
}

// versionVersion is the running build's version, indirected once so rollback's
// messages and hold agree with activation's card.
func versionVersion() string { return strings.TrimPrefix(currentVersionTag(), "v") }
