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

	// Gate 1: the wire guard, pure disk reads (persisted shim wire versions vs
	// the staged card). Unknowns gate conservatively.
	if reason := wireBumpDefer(opts.StateDir, stage); reason != "" {
		s.Outcome, s.Detail = "deferred-wirebump", reason
		return s, recordState(opts.StateDir, &s)
	}

	// Gate 2: the staged binary must answer for itself before it is installed.
	stagedBin := filepath.Join(stage, "swarm")
	if err := sanityCheck(stagedBin, tag); err != nil {
		s.Outcome, s.Detail = "failed-sanity", err.Error()
		return s, recordState(opts.StateDir, &s)
	}

	// Gate 3: rollback slots, in the state dir, with the OUTGOING build's card
	// (this process's own constants -- it IS the outgoing build).
	if err := writeRollbackSlots(opts); err != nil {
		s.Outcome, s.Detail = "failed-backup", err.Error()
		return s, recordState(opts.StateDir, &s)
	}

	// Gate 4: install, stage-then-rename inside the target dir.
	targets := []string{opts.BinPath}
	if remote := filepath.Join(filepath.Dir(opts.BinPath), "swarm-remote"); fileExists(remote) && fileExists(filepath.Join(stage, "swarm-remote")) {
		targets = append(targets, remote)
	}
	for _, dst := range targets {
		src := filepath.Join(stage, filepath.Base(dst))
		if err := installFile(src, dst); err != nil {
			s.Outcome, s.Detail = "failed-install", fmt.Sprintf("%s: %v", dst, err)
			return s, recordState(opts.StateDir, &s)
		}
	}

	// The staged build is now the installed build; the staging dir's job is done.
	_ = os.RemoveAll(stage)

	// The outcome is durable BEFORE the handoff: exec never returns, and a state
	// that said nothing would read as a night that never ran (committee C3's
	// separate-outcomes rule -- the converge writes its own story to its log).
	s.Outcome, s.Detail = "activated", fmt.Sprintf("installed %s; handing off to its converge", tag)
	if err := recordState(opts.StateDir, &s); err != nil {
		return s, err
	}

	env := append(os.Environ(), handoffGuardEnv+"=1")
	if err := execFn(opts.BinPath, []string{"swarm", "daemon", "restart", "--unattended"}, env); err != nil {
		s.Outcome, s.Detail = "failed-handoff", err.Error()
		return s, recordState(opts.StateDir, &s)
	}
	return s, nil // unreachable in production: exec replaced the process
}

// wireBumpDefer answers "" when installing the staged build is safe for every
// live session, or the reason to stage-and-wait. Pure disk reads.
func wireBumpDefer(stateDir, stage string) string {
	manifest, merr := readStagedManifest(stage)
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		return fmt.Sprintf("cannot read the state dir to check live sessions: %v", err)
	}
	var running []persist.Meta
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, ok := readMetaFile(filepath.Join(stateDir, e.Name(), "meta.json"))
		if ok && m.Status.Process == status.ProcessRunning {
			running = append(running, m)
		}
	}
	if len(running) == 0 {
		return "" // nothing live; even an unknown card cannot strand a session
	}
	if merr != nil {
		return fmt.Sprintf("the staged archive carries no compatibility card and %d session(s) are live; end them or wait for an idle night", len(running))
	}
	for _, m := range running {
		wire := m.ShimWireVersion
		if wire == 0 {
			// Pre-R3 session: its wire version was never persisted. It was
			// spawned by SOME past build; all releases to date speak wire 1, so
			// the running binary's own constant is the honest stand-in -- and if
			// the staged card bumps past it, defer.
			wire = shimwire.Version
		}
		if wire != manifest.Shimwire {
			return fmt.Sprintf("session %s speaks shim wire v%d, the staged build speaks v%d; installing would strand every new session under the live daemon (ProcessLost) -- end the sessions or wait for an idle night", m.ID, wire, manifest.Shimwire)
		}
	}
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
	if want := strings.TrimPrefix(tag, "v"); !strings.Contains(out.String(), want) {
		return fmt.Errorf("the staged binary answers %q, not the staged version %s", strings.TrimSpace(out.String()), want)
	}
	return nil
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

// installFile is copy-to-.new-then-rename INSIDE dst's directory (same
// filesystem by construction, so the rename is atomic and the running inode is
// untouched), with a bounded sudo -n fallback when the directory is not ours
// to write (the /usr/local/bin reality of a hand-installed fleet, R5's install
// script prefers a user-writable dir precisely to retire this fallback).
func installFile(src, dst string) error {
	tmp := dst + ".new"
	err := copyFile(src, tmp, 0o755)
	if err == nil {
		if err = os.Rename(tmp, dst); err == nil {
			return nil
		}
	}
	if !errors.Is(err, os.ErrPermission) {
		_ = os.Remove(tmp)
		return err
	}
	_ = os.Remove(tmp)
	for _, argv := range [][]string{
		{"sudo", "-n", "install", "-m", "0755", src, tmp},
		{"sudo", "-n", "mv", "-f", tmp, dst},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), sanityTimeout)
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		out, cerr := cmd.CombinedOutput()
		cancel()
		if cerr != nil {
			return fmt.Errorf("%s: %v (%s) -- the target dir is not writable and passwordless sudo is unavailable; move the install to a user-writable dir or grant sudo -n", strings.Join(argv, " "), cerr, strings.TrimSpace(string(out)))
		}
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
