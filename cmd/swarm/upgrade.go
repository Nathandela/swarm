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
	aopts := upgrade.ActivateOptions{StateDir: cc.StateDir, BinPath: exe}

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
		// recorded), then activate ONLY when something new is staged or a prior
		// night's stage still awaits -- and let activation's own gates defer.
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
// in upgrade.json for doctor; tomorrow retries. NOTE these codes never collide
// with dispatch's usage-2 (Opus B2): upgrade answers 0 or 1, and the future
// scheduler calls this package in-process, never through an old binary's argv.
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
