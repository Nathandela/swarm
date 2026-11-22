package main

import (
	"fmt"
	"io"

	"github.com/Nathandela/swarm/internal/setuptrust"
)

func runSetupTrust(args []string, out, errOut io.Writer) int {
	if len(args) != 2 || args[0] != "on" && args[0] != "off" && args[0] != "status" {
		fmt.Fprintln(errOut, "usage: swarm setup-trust on|off|status /absolute/workspace")
		return misuseExit
	}
	if args[0] != "status" {
		if _, err := setuptrust.Set(args[1], args[0] == "on"); err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
	}
	cwd, on := setuptrust.Workspace(args[1])
	if cwd == "" {
		fmt.Fprintln(errOut, "Specify an existing workspace using its absolute path.")
		return 1
	}
	status := "off"
	if on {
		status = "on"
	}
	fmt.Fprintf(out, "Automatic startup trust is %s for %s.\n", status, cwd)
	return 0
}
