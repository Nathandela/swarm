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
// reason. On success it does not return. Hardened per the R2/R3 audit: the
// slots' card is REQUIRED (a rollback whose compatibility cannot be read is
// refused, never risked -- codex finding 5), the wire guard applies in the
// rollback direction too (finding 3: wire skew strands sessions either way),
// ownership is re-checked (finding 6), and a successful rollback CONSUMES its
// slots -- a second invocation finds nothing to restore instead of overwriting
// the hold with the restored version and re-arming the bad release (finding
// 5's treadmill).
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

	if owner := ClassifyOwner(opts.BinPath); owner != OwnerSelf {
		s.Outcome, s.Detail = "refused-owner", ownerDelegate(owner)
		return s, recordState(opts.StateDir, &s)
	}

	// The slots' card, REQUIRED: it is what makes both guards below readable.
	var prevCard CompatManifest
	data, err := os.ReadFile(filepath.Join(prev, "compat.json"))
	if err != nil || json.Unmarshal(data, &prevCard) != nil || prevCard.Schema == 0 {
		s.Outcome = "refused-card"
		s.Detail = "the rollback slots carry no readable compatibility card; restoring an unverifiable build is refused (restore by hand if you must)"
		return s, recordState(opts.StateDir, &s)
	}

	// The schema guard, fail-closed: an unreadable meta is a refusal, not a
	// skip -- unknown state must not permit a potentially bricking rollback.
	need, id, serr := maxPersistedSchema(opts.StateDir)
	if serr != nil {
		s.Outcome, s.Detail = "refused-schema", serr.Error()
		return s, recordState(opts.StateDir, &s)
	}
	if need > prevCard.Schema {
		s.Outcome = "refused-schema"
		s.Detail = fmt.Sprintf("session %s persists schema v%d; the rollback build supports v%d and would refuse it at Open -- end and delete newer-schema sessions first", id, need, prevCard.Schema)
		return s, recordState(opts.StateDir, &s)
	}

	// The wire guard, rollback direction: restoring wire A under live wire-B
	// shims (or a live daemon) strands sessions exactly as the forward case.
	if reason := wireDefer(opts, prevCard, nil); reason != "" {
		s.Outcome, s.Detail = "deferred-wirebump", reason
		return s, recordState(opts.StateDir, &s)
	}

	// The hold: the version being rolled AWAY FROM must not come back tonight.
	if err := os.WriteFile(holdPath(opts.StateDir), []byte("v"+versionVersion()+"\n"), 0o600); err != nil {
		s.Outcome, s.Detail = "failed-hold", err.Error()
		return s, recordState(opts.StateDir, &s)
	}

	targets := []string{opts.BinPath}
	if remote := filepath.Join(filepath.Dir(opts.BinPath), "swarm-remote"); fileExists(remote) {
		if !fileExists(filepath.Join(prev, "swarm-remote")) {
			s.Outcome, s.Detail = "failed-install", "the install has swarm-remote but the rollback slots do not; refusing a mixed pair"
			return s, recordState(opts.StateDir, &s)
		}
		targets = append(targets, remote)
	}
	if err := installPair(prev, targets); err != nil {
		s.Outcome, s.Detail = "failed-install", err.Error()
		return s, recordState(opts.StateDir, &s)
	}

	// The restore is on disk: the daemon must converge onto it, and the pending
	// marker makes that durable across every kill window, same as activation.
	if err := os.WriteFile(pendingPath(opts.StateDir), []byte(prevTag+"\n"), 0o600); err != nil {
		s.Outcome, s.Detail = "failed-pending", err.Error()
		return s, recordState(opts.StateDir, &s)
	}
	// Consume the slots: this transition is spent, and a second rollback must
	// answer ErrNothingToRollBack rather than re-derive a hold from the now-
	// restored version.
	if err := os.RemoveAll(prev); err != nil {
		s.Outcome, s.Detail = "failed-consume", err.Error()
		return s, recordState(opts.StateDir, &s)
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

// maxPersistedSchema scans metas plainly for the highest schema_version on
// disk. Fail-closed: an unreadable state dir or an existing-but-undecodable
// meta is an ERROR, because unknown state must refuse, not permit.
func maxPersistedSchema(stateDir string) (int, string, error) {
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		return 0, "", fmt.Errorf("cannot read the state dir to check persisted schemas: %w", err)
	}
	max, id := 0, ""
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(stateDir, e.Name(), "meta.json")
		if _, statErr := os.Stat(path); statErr != nil {
			continue // not a session dir
		}
		m, ok := readMetaFile(path)
		if !ok {
			return 0, "", fmt.Errorf("session record %s exists but cannot be read; unknown schema state refuses", e.Name())
		}
		if m.SchemaVersion > max {
			max, id = m.SchemaVersion, m.ID
		}
	}
	return max, id, nil
}

// versionVersion is the running build's version, indirected once so rollback's
// messages and hold agree with activation's card.
func versionVersion() string { return strings.TrimPrefix(currentVersionTag(), "v") }
