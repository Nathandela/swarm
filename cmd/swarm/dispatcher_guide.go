package main

import (
	_ "embed"
	"fmt"
	"io"
)

//go:embed skills/swarm-dispatcher/SKILL.md
var dispatcherSkill string

//go:embed skills/swarm-dispatcher/guide.txt
var dispatcherGuide string

func runDispatcherGuide(args []string, stdout, stderr io.Writer) int {
	switch {
	case len(args) == 0:
		_, _ = fmt.Fprint(stdout, dispatcherGuide)
	case len(args) == 1 && args[0] == "--skill":
		_, _ = fmt.Fprint(stdout, dispatcherSkill)
	case len(args) == 1 && (args[0] == "-h" || args[0] == "--help"):
		_, _ = fmt.Fprintln(stdout, "swarm dispatcher-guide [--skill]")
	default:
		_, _ = fmt.Fprintln(stderr, "usage: swarm dispatcher-guide [--skill]")
		return misuseExit
	}
	return 0
}
