package upgrade

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/version"
)

// The activation transaction (lifecycle R3): take a VERIFIED staged build and
// make it the installed one, at a safe point, ending in the one line the shell
// prototype proved load-bearing -- exec the NEW binary's converge, because a
// converge run by the old process compares the daemon against its own stale
// compiled-in version and answers "already converged" while converging nothing
// (committee: codex 4, Opus B1, Fable C1).
//
// Order of the gates, and why each sits where it does:
//
//  1. The WIRE guard runs BEFORE anything is installed. The daemon spawns every
//     shim from the path this would overwrite, so a wire-bumped binary under a
//     live old daemon breaks every NEW session -- the compat matrix pins that
//     cell as ProcessLost. "Install, then let converge defer" was the draft the
//     committee killed; the rule is STAGE, DO NOT INSTALL (Opus B4).
//  2. The staged binary answers `version` before it is installed, or a corrupt
//     or wrong-arch build bricks the install with the rollback command inside
//     the brick (Gemini finding 2).
//  3. The outgoing binaries are copied to the rollback slots -- in the STATE
//     dir, never beside a brew-linked binary (Opus C6) -- with this build's own
//     compatibility card, so a rollback can refuse an unsafe downgrade before
//     touching anything (Opus A4).
//  4. Install is copy-to-.new-then-rename INSIDE the target directory (same
//     filesystem by construction; the running daemon's inode is untouched),
//     with a bounded sudo -n fallback for a root-owned target dir.
//  5. The handoff execs the installed binary's `daemon restart --unattended`,
//     with a loop-guard marker. ADR-020 owns everything after that line.

// execFn is syscall.Exec, a seam so activation is testable to the brink of the
// handoff -- a test substitutes a recorder; production never does.
var execFn = syscall.Exec

// handoffGuardEnv marks the exec'd converge as an upgrade handoff. The new
// binary's `daemon restart --unattended` ignores it (it is converge, not
// upgrade), but a future caller that ever routed back into activation would
// stop at the guard instead of exec-looping.
const handoffGuardEnv = "SWARM_UPGRADE_HANDOFF"

// sanityTimeout bounds the staged binary's version probe.
const sanityTimeout = 10 * time.Second

// ErrNothingStaged is Activate's answer when no verified build awaits.
var ErrNothingStaged = errors.New("upgrade: nothing staged; run `swarm upgrade --stage` first")

// ActivateOptions configures activation.
type ActivateOptions struct {
	StateDir string
	BinPath  string // the installed binary to replace (os.Executable, unresolved)
	// Installed is the running binary's version (internal/version.Version): a
	// staged tag equal to it is an interrupted activation's leftover, answered
	// "current" instead of re-running the transaction against the already-
	// installed build -- which would rebuild the rollback slots from it and
	// destroy the true originals (R2/R3 audit, Fable M3b).
	Installed string
	// DaemonAlive reports whether a daemon currently holds the singleton lock.
	// The wire guard needs it: ZERO running sessions does not mean no daemon,
	// and a live old daemon's next launch under a wire-bumped install is the
	// pinned ProcessLost cell (R2/R3 audit, codex finding 2). nil is treated as
	// "assume alive" -- fail closed, never open.
	DaemonAlive func() bool
}

// pendingPath is the durable installed-but-not-yet-converged phase marker
// (codex finding 1): activation writes it BEFORE anything is installed, and
// only a converge that completes -- run as the installed binary, on activation
// night or any later one -- clears it. Without it, a converge that defers
// around working sessions (the ORDINARY night) left the old daemon running
// forever: the next run read "current" from the new on-disk binary and exited.
func pendingPath(stateDir string) string {
	return filepath.Join(stateDir, "upgrade", "pending-converge")
}

// PendingConverge reports the version whose install awaits a confirmed
// converge; "" when none.
func PendingConverge(stateDir string) string {
	data, err := os.ReadFile(pendingPath(stateDir))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// ClearPendingConverge marks the install converged. The caller clears ONLY on
// a converge that exited converged (or found no daemon to converge, which the
// next client start resolves onto the installed binary).
func ClearPendingConverge(stateDir string) error {
	err := os.Remove(pendingPath(stateDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Activate installs the staged build and execs its converge. ON SUCCESS IT
// DOES NOT RETURN -- the process becomes the new binary's
// `daemon restart --unattended`. Every other outcome returns with the state
// recorded: "deferred-wirebump" (staged kept, nothing installed), "failed-*"
// or ErrNothingStaged.
func Activate(opts ActivateOptions) (State, error) {
	s := State{}
	unlock, err := lockUpgrade(opts.StateDir)
	if err != nil {
		s.Outcome, s.Detail = "busy", err.Error()
		return s, recordState(opts.StateDir, &s)
	}
	// The unlock matters only on the non-exec paths; exec replaces the process
	// and the flock dies with it, which is exactly the window the lock covers.
	defer unlock()

	stage := StageDir(opts.StateDir)
	tagBytes, err := os.ReadFile(filepath.Join(stage, "VERSION"))
	if err != nil {
		return s, ErrNothingStaged
	}
	tag := strings.TrimSpace(string(tagBytes))
	s.StagedVersion = tag

	if opts.Installed != "" && strings.TrimPrefix(tag, "v") == strings.TrimPrefix(opts.Installed, "v") {
		// The staged build IS the installed build: an interrupted activation's
		// leftover. The stage is spent; the pending marker (if any) keeps the
		// converge retried by the unattended path.
		_ = os.RemoveAll(stage)
		s.Outcome, s.Detail, s.StagedVersion = "current", fmt.Sprintf("%s is already installed; leftover stage cleared", tag), ""
		return s, recordState(opts.StateDir, &s)
	}

	// Gate 0: ownership, re-checked HERE and not only at stage (codex finding
	// 6): days can pass between the two, and a path that became a package
	// manager's in between must not be overwritten behind its books.
	if owner := ClassifyOwner(opts.BinPath); owner != OwnerSelf {
		s.Outcome, s.Detail = "refused-owner", ownerDelegate(owner)
		return s, recordState(opts.StateDir, &s)
	}

	// Gate 1: the wire guard, pure disk reads (persisted shim wire versions vs
	// the staged card) plus daemon liveness. Unknowns gate conservatively.
	card, cardErr := readStagedManifest(stage)
	if reason := wireDefer(opts, card, cardErr); reason != "" {
		s.Outcome, s.Detail = "deferred-wirebump", reason
		return s, recordState(opts.StateDir, &s)
	}

	// Gate 1b: the schema guard, BOTH directions of travel (Fable M1): rollback
	// already refuses restoring a build that cannot load the persisted metas,
	// and an --allow-downgrade activation is the same brick through the front
	// door. A readable card is required whenever any session meta exists.
	if need, id, serr := maxPersistedSchema(opts.StateDir); serr != nil {
		s.Outcome, s.Detail = "refused-schema", serr.Error()
		return s, recordState(opts.StateDir, &s)
	} else if need > 0 {
		if cardErr != nil {
			s.Outcome, s.Detail = "refused-card", "sessions are persisted here and the staged archive carries no readable compatibility card; an unverifiable install is refused"
			return s, recordState(opts.StateDir, &s)
		}
		if need > card.Schema {
			s.Outcome = "refused-schema"
			s.Detail = fmt.Sprintf("session %s persists schema v%d; the staged build supports v%d and would refuse it at Open", id, need, card.Schema)
			return s, recordState(opts.StateDir, &s)
		}
	}

	// Gate 2: the staged binary must answer for itself before it is installed.
	stagedBin := filepath.Join(stage, "swarm")
	if err := sanityCheck(stagedBin, tag); err != nil {
		s.Outcome, s.Detail = "failed-sanity", err.Error()
		return s, recordState(opts.StateDir, &s)
	}

	// Gate 3: rollback slots -- written ONCE per transition. A retry of an
	// interrupted activation must not rebuild them from a half-upgraded
	// installation, destroying the only true originals (codex finding 4): with
	// the pending marker standing, existing slots are the originals and stay.
	if PendingConverge(opts.StateDir) == "" || !fileExists(filepath.Join(PrevDir(opts.StateDir), "VERSION")) {
		if err := writeRollbackSlots(opts); err != nil {
			s.Outcome, s.Detail = "failed-backup", err.Error()
			return s, recordState(opts.StateDir, &s)
		}
	}

	// The durable phase FIRST: any death between here and a completed converge
	// leaves the marker, and every later unattended run retries the converge
	// until it confirms (codex finding 1's kill windows).
	if err := os.WriteFile(pendingPath(opts.StateDir), []byte(tag+"\n"), 0o600); err != nil {
		s.Outcome, s.Detail = "failed-pending", err.Error()
		return s, recordState(opts.StateDir, &s)
	}

	// The install target is the RESOLVED path -- the same file ownership was
	// classified on; installing over a symlink would replace the link itself
	// with a regular file (Fable L7).
	if resolved, rerr := filepath.EvalSymlinks(opts.BinPath); rerr == nil {
		opts.BinPath = resolved
	}

	// Gate 4: install the PAIR as one step as far as the filesystem allows:
	// every copy lands as .new beside its target first, then the renames run
	// back to back -- and a target swarm-remote with no staged sibling REFUSES
	// rather than silently leaving a mixed pair (codex finding 4).
	targets := []string{opts.BinPath}
	remote := filepath.Join(filepath.Dir(opts.BinPath), "swarm-remote")
	if fileExists(remote) {
		if !fileExists(filepath.Join(stage, "swarm-remote")) {
			s.Outcome, s.Detail = "failed-install", "the install has swarm-remote but the staged archive does not; refusing a mixed pair"
			return s, recordState(opts.StateDir, &s)
		}
		targets = append(targets, remote)
	}
	if err := installPair(stage, targets); err != nil {
		// A failure INSIDE the rename sequence can leave a mixed pair; the slots
		// are complete by construction at this point, so restore them rather
		// than strand a half-upgraded machine with a live old daemon (Fable H2).
		for _, dst := range targets {
			_ = installFile(filepath.Join(PrevDir(opts.StateDir), filepath.Base(dst)), dst)
		}
		s.Outcome, s.Detail = "failed-install", err.Error()+" (previous binaries restored from the rollback slots)"
		return s, recordState(opts.StateDir, &s)
	}

	// The staged build is now the installed build; the staging dir's job is done.
	_ = os.RemoveAll(stage)

	// The outcome is durable BEFORE the handoff -- and a state-write failure
	// must NOT abort it: the install already happened, and skipping the exec
	// would strand the old daemon behind a new binary (Fable H2's third
	// window). StagedVersion clears here: nothing awaits activation any more
	// (Fable L1's doctor misreport).
	s.Outcome, s.Detail, s.StagedVersion = "activated", fmt.Sprintf("installed %s; handing off to its converge", tag), ""
	_ = recordState(opts.StateDir, &s)

	env := append(os.Environ(), handoffGuardEnv+"=1")
	if err := execFn(opts.BinPath, []string{"swarm", "daemon", "restart", "--unattended"}, env); err != nil {
		s.Outcome, s.Detail = "failed-handoff", err.Error()
		return s, recordState(opts.StateDir, &s)
	}
	return s, nil // unreachable in production: exec replaced the process
}

// wireDefer answers "" when installing the incoming build (described by card)
// is safe, or the reason to stage-and-wait. Hardened per the R2/R3 audit
// (codex finding 2): an UNREADABLE session meta defers -- unknown gates
// closed, never open -- and a wire difference defers while a daemon merely
// HOLDS THE LOCK, sessions or none, because a live old daemon's next launch
// would exec the installed new shim (the pinned ProcessLost cell).
func wireDefer(opts ActivateOptions, card CompatManifest, cardErr error) string {
	sameWire := cardErr == nil && card.Shimwire == shimwire.Version
	daemonAlive := true // nil probe = assume alive: fail closed
	if opts.DaemonAlive != nil {
		daemonAlive = opts.DaemonAlive()
	}

	entries, err := os.ReadDir(opts.StateDir)
	if err != nil {
		return fmt.Sprintf("cannot read the state dir to check live sessions: %v", err)
	}
	var running []persist.Meta
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(opts.StateDir, e.Name(), "meta.json")
		if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
			continue // not a session dir (journal/, devices/, upgrade/, ...)
		}
		m, ok := readMetaFile(path)
		if !ok {
			return fmt.Sprintf("session record %s exists but cannot be read; unknown session state gates closed", e.Name())
		}
		if m.Status.Process == status.ProcessRunning {
			running = append(running, m)
		}
	}

	if !sameWire {
		// A wire change (or an unknown card, which must be assumed to be one)
		// tolerates NO live daemon: zero sessions is not zero daemon, and the
		// daemon spawns its next shim from the path this install overwrites.
		if daemonAlive {
			if cardErr != nil {
				return "the staged archive carries no compatibility card and a daemon is running; stop the daemon (swarm daemon restart happens on the idle night) before an unverifiable install"
			}
			return fmt.Sprintf("the staged build speaks shim wire v%d, this machine v%d, and a daemon is running; installing now would strand its next launch (ProcessLost) -- the idle night handles it", card.Shimwire, shimwire.Version)
		}
		for _, m := range running {
			return fmt.Sprintf("session %s is live across a wire change; end it first", m.ID)
		}
		return ""
	}

	// Same wire: live sessions and a live daemon are converge's ordinary
	// business (defer-around-work happens THERE, per ADR-020); nothing to gate.
	return ""
}

// readMetaFile is the same plain read doctor uses: never through the store,
// which would create and chmod.
func readMetaFile(path string) (persist.Meta, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return persist.Meta{}, false
	}
	var m persist.Meta
	if json.Unmarshal(data, &m) != nil {
		return persist.Meta{}, false
	}
	return m, true
}

// sanityCheck runs the staged binary's own `version` and requires the staged
// tag in its answer: a corrupt, truncated or wrong-arch build fails HERE, while
// the previous binary is still the installed one.
func sanityCheck(stagedBin, tag string) error {
	ctx, cancel := context.WithTimeout(context.Background(), sanityTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, stagedBin, "version")
	cmd.WaitDelay = time.Second
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("the staged binary cannot run: %v (%s)", err, strings.TrimSpace(out.String()))
	}
	want := strings.TrimPrefix(tag, "v")
	for _, tok := range strings.Fields(out.String()) {
		if tok == want {
			return nil
		}
	}
	return fmt.Errorf("the staged binary answers %q, not the staged version %s", strings.TrimSpace(out.String()), want)
}

// PrevDir holds the rollback slots.
func PrevDir(stateDir string) string { return filepath.Join(stateDir, "upgrade", "prev") }

// writeRollbackSlots copies the OUTGOING binaries and this build's own
// compatibility card into the state dir, so `swarm upgrade --rollback` can
// refuse an unsafe downgrade with pure reads (Opus A4) and restore without the
// release host.
func writeRollbackSlots(opts ActivateOptions) error {
	prev := PrevDir(opts.StateDir)
	if err := os.RemoveAll(prev); err != nil {
		return err
	}
	if err := os.MkdirAll(prev, 0o700); err != nil {
		return err
	}
	if err := copyFile(opts.BinPath, filepath.Join(prev, "swarm"), 0o755); err != nil {
		return err
	}
	if remote := filepath.Join(filepath.Dir(opts.BinPath), "swarm-remote"); fileExists(remote) {
		if err := copyFile(remote, filepath.Join(prev, "swarm-remote"), 0o755); err != nil {
			return err
		}
	}
	card, err := json.MarshalIndent(CurrentManifest(currentVersionTag()), "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(prev, "compat.json"), card, 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(prev, "VERSION"), []byte(currentVersionTag()+"\n"), 0o600)
}

// installPair copies EVERY target's payload to .new first and only then runs
// the renames back to back, so the widest failure modes (missing payload, full
// disk, permissions) surface before the first target changes and the mixed-pair
// window narrows to the renames themselves -- which the pending marker's retry
// then covers (codex finding 4).
func installPair(stage string, targets []string) error {
	for _, dst := range targets {
		if err := stageBeside(filepath.Join(stage, filepath.Base(dst)), dst); err != nil {
			for _, cleanup := range targets {
				_ = removeMaybeSudo(cleanup + ".new")
			}
			return err
		}
	}
	for _, dst := range targets {
		if err := renameInto(dst); err != nil {
			return fmt.Errorf("%s: %w", dst, err)
		}
	}
	return nil
}

// installFile is copy-to-.new-then-rename INSIDE dst's directory (same
// filesystem by construction, so the rename is atomic and the running inode is
// untouched), with a bounded sudo -n fallback when the directory is not ours
// to write (the /usr/local/bin reality of a hand-installed fleet, R5's install
// script prefers a user-writable dir precisely to retire this fallback).
func installFile(src, dst string) error {
	if err := stageBeside(src, dst); err != nil {
		return err
	}
	return renameInto(dst)
}

// stageBeside lands src as dst.new (direct copy, or sudo -n when the dir is
// not ours to write).
func stageBeside(src, dst string) error {
	tmp := dst + ".new"
	err := copyFile(src, tmp, 0o755)
	if err == nil {
		return nil
	}
	if !errors.Is(err, os.ErrPermission) {
		_ = os.Remove(tmp)
		return err
	}
	_ = os.Remove(tmp)
	return runSudo("install", "-m", "0755", src, tmp)
}

// renameInto moves dst.new over dst (direct, or sudo -n).
func renameInto(dst string) error {
	tmp := dst + ".new"
	err := os.Rename(tmp, dst)
	if err == nil {
		return nil
	}
	if !errors.Is(err, os.ErrPermission) {
		return err
	}
	return runSudo("mv", "-f", tmp, dst)
}

// removeMaybeSudo clears a staged .new best-effort on the abort path.
func removeMaybeSudo(path string) error {
	if err := os.Remove(path); err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return runSudo("rm", "-f", path)
}

// runSudo is one bounded passwordless-sudo exec, the root-owned-target
// fallback the install script's users live with until R5's user-dir default.
func runSudo(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), sanityTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sudo", append([]string{"-n"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("sudo -n %s: %v (%s) -- the target dir is not writable and passwordless sudo is unavailable; move the install to a user-writable dir or grant sudo -n", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// currentVersionTag is the outgoing build's tag form ("v" + its stamped
// version; a dev build yields "vdev", informational only in the rollback card).
func currentVersionTag() string { return "v" + version.Version }

func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}
