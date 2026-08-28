package main

// `swarm upgrade` -- the update transaction's CLI face (lifecycle plan R2:
// check and STAGE only; activation is R3's, so nothing here ever touches the
// running binary or the daemon). Like doctor, this verb never starts a daemon:
// it composes from clientConfig and the upgrade package alone.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Nathandela/swarm/internal/converge"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/upgrade"
	"github.com/Nathandela/swarm/internal/version"
)

func runUpgrade(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	fs.SetOutput(stderr)
	stage := fs.Bool("stage", false, "download, verify and stage the latest release (never activates)")
	activate := fs.Bool("activate", false, "install the staged release and hand off to its converge")
	rollback := fs.Bool("rollback", false, "restore the previous release and hold the current one from the nightly")
	unattended := fs.Bool("unattended", false, "stage, then activate when safe: the scheduler's whole run")
	allowDowngrade := fs.Bool("allow-downgrade", false, "permit staging a release older than the installed version")
	asJSON := fs.Bool("json", false, "print the outcome as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if os.Getenv("SWARM_UPGRADE_HANDOFF") != "" {
		// The activation handoff execs `daemon restart`, never this verb; a
		// caller that somehow routed back here mid-handoff stops at the guard
		// instead of exec-looping.
		_, _ = fmt.Fprintln(stderr, "upgrade: refusing to run inside an activation handoff")
		return 1
	}
	cc, err := clientConfig()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "upgrade: %v\n", err)
		return 1
	}
	exe, err := os.Executable()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "upgrade: %v\n", err)
		return 1
	}
	opts := upgrade.Options{
		StateDir:       cc.StateDir,
		BinPath:        exe,
		Installed:      version.Version,
		AllowDowngrade: *allowDowngrade,
	}
	aopts := upgrade.ActivateOptions{
		StateDir: cc.StateDir,
		BinPath:  exe,
		// The wire guard's daemon-liveness probe: a socket that answers, or a
		// held lock behind one that does not. Zero sessions is not zero daemon
		// (R2/R3 audit, codex finding 2).
		DaemonAlive: func() bool {
			if _, err := os.Stat(cc.SocketPath); err != nil {
				return false
			}
			if _, herr := converge.HelloVia(cc.SocketPath)(); herr == nil {
				return true
			}
			return !daemon.LockFree(cc)
		},
	}
	// Test seam only: the hermetic cmd-level tests point the transaction at a
	// fixture release server. Production never sets it.
	if base := os.Getenv("SWARM_UPGRADE_BASE_URL"); base != "" {
		opts.BaseURL = base
	}

	switch {
	case *rollback:
		st, err := upgrade.Rollback(aopts)
		// Reached only when the rollback did NOT exec (a refusal or failure):
		// on success the process became the restored binary's converge.
		return reportUpgradeState(st, err, *asJSON, stdout, stderr)
	case *activate:
		st, err := upgrade.Activate(aopts)
		return reportUpgradeState(st, err, *asJSON, stdout, stderr)
	case *unattended:
		// The scheduler's whole run: stage (every refusal already quiet and
		// recorded), then activate when something is staged -- and FIRST, finish
		// what an earlier night left half-done. An install whose converge
		// deferred (the ordinary night: sessions were working) or died leaves
		// the durable pending marker, and without this retry the old daemon ran
		// forever behind a binary that reads "current" (R2/R3 audit, codex
		// finding 1). The retry runs converge IN-PROCESS, which is correct
		// exactly here: the binary on disk was already replaced, so this
		// process IS the installed build.
		if pending := upgrade.PendingConverge(cc.StateDir); pending != "" {
			if code := runDaemonRestartUnattended(stderr); code == converge.ExitConverged {
				if err := upgrade.ClearPendingConverge(cc.StateDir); err != nil {
					_, _ = fmt.Fprintf(stderr, "upgrade: clear pending-converge: %v\n", err)
				}
			} else {
				_, _ = fmt.Fprintf(stdout, "pending-converge: %s installed, converge deferred again (exit %d); retrying next run\n", pending, code)
			}
		}
		st, err := upgrade.Stage(context.Background(), opts)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "upgrade: %v\n", err)
			return 1
		}
		if st.Outcome != "staged" {
			return reportUpgradeState(st, nil, *asJSON, stdout, stderr)
		}
		ast, err := upgrade.Activate(aopts)
		return reportUpgradeState(ast, err, *asJSON, stdout, stderr)
	}

	if !*stage {
		dec, err := upgrade.Check(context.Background(), opts)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "upgrade: %v\n", err)
			return 1
		}
		if *asJSON {
			if err := writeJSON(stdout, dec); err != nil {
				return 1
			}
			return 0
		}
		_, _ = fmt.Fprintf(stdout, "installed %s, latest %s: would %s (%s)\n",
			version.Version, orUnknown(dec.Latest), dec.Action, dec.Reason)
		return 0
	}

	st, err := upgrade.Stage(context.Background(), opts)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "upgrade: %v\n", err)
		return 1
	}
	if *asJSON {
		if err := writeJSON(stdout, st); err != nil {
			return 1
		}
	} else {
		_, _ = fmt.Fprintf(stdout, "%s: %s\n", st.Outcome, st.Detail)
	}
	return upgradeExitCode(st.Outcome)
}

// reportUpgradeState prints a transaction outcome and maps it to the exit
// contract. Reached on activation/rollback only when NO exec happened.
func reportUpgradeState(st upgrade.State, err error, asJSON bool, stdout, stderr io.Writer) int {
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "upgrade: %v\n", err)
		return 1
	}
	if asJSON {
		if werr := writeJSON(stdout, st); werr != nil {
			return 1
		}
	} else {
		_, _ = fmt.Fprintf(stdout, "%s: %s\n", st.Outcome, st.Detail)
	}
	return upgradeExitCode(st.Outcome)
}

// upgradeExitCode is the exit contract, decided (committee C3): red only when
// a run that SHOULD have acted could not. The declines -- current, refusals,
// busy, offline, held, a wire-bump deferral -- are the design working, recorded
// in upgrade.json for doctor; tomorrow retries.
//
// THE CONTRACT'S HONEST BOUNDARY (audit, Fable M4): these 0/1 codes apply to
// every run that RETURNS. A successful --activate/--rollback/--unattended
// handoff never returns -- the process becomes the installed binary's
// `daemon restart --unattended`, whose converge codes (0 converged, 1 failed,
// 2 deferred, 3 refused) are what the invoker observes, exactly as the timer
// runbook documents for that verb. A deferral exit-2 after activation is
// ROUTINE (sessions were working; the pending marker retries) and must never
// be read as failure -- nor answered by re-running rollback.
func upgradeExitCode(outcome string) int {
	switch outcome {
	case "staged", "current", "busy", "offline", "held",
		"refused-owner", "refused-dev", "refused-downgrade", "refused-schema",
		"deferred-wirebump", "rolled-back", "activated":
		return 0
	default:
		return 1
	}
}

func orUnknown(s string) string {
	if s == "" {
		return "(unresolved)"
	}
	return s
}
