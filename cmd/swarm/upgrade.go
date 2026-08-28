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
	// The exit-code contract, decided (committee C3): red only when a run that
	// SHOULD have staged could not. current/refused/busy/offline are the design
	// declining, recorded in upgrade.json for doctor; tomorrow retries.
	switch st.Outcome {
	case "staged", "current", "busy", "offline", "refused-owner", "refused-dev", "refused-downgrade":
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
