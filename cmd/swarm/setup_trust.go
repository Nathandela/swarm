package main

import (
	"fmt"
	"io"
	"path/filepath"

	"github.com/Nathandela/swarm/internal/setuptrust"
)

func runSetupTrust(args []string, out, errOut io.Writer) int {
	profile := len(args) == 3 && args[0] == "on" && args[2] == "--profile-hooks"
	if len(args) != 2 && !profile || args[0] != "on" && args[0] != "off" && args[0] != "status" || !filepath.IsAbs(args[1]) {
		_, _ = fmt.Fprintln(errOut, "usage: swarm setup-trust on /absolute/workspace [--profile-hooks] | off|status /absolute/workspace")
		return misuseExit
	}
	if args[0] != "status" {
		cwd, err := setuptrust.Set(args[1], args[0] == "on", profile)
		if err != nil {
			_, _ = fmt.Fprintln(errOut, err)
			return 1
		}
		if args[0] == "off" {
			_, _ = fmt.Fprintf(out, "Automatic startup trust is off for %s. Earlier approvals remain in the provider.\n", cwd)
			return 0
		}
	}
	consent, on := setuptrust.Get(args[1])
	if !on {
		_, _ = fmt.Fprintf(out, "Automatic startup trust is off for %s.\n", args[1])
		return 0
	}
	_, _ = fmt.Fprintf(out, "Automatic startup trust is on for %s. Codex saves folder and project hook approvals in its profile.\n", consent.Workspace)
	if consent.ProfileHooks {
		_, _ = fmt.Fprintln(out, "Codex also saves approvals for new or changed user and plugin hooks; they apply wherever that profile loads them.")
	}
	return 0
}
