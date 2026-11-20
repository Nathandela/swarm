package main

import (
	_ "embed"
	"fmt"
	"io"
)

//go:embed skills/swarm-dispatcher/SKILL.md
var dispatcherSkill string

const dispatcherGuide = `Acknowledge the request briefly in your own words through the publish tool.
Start work with ordinary swarm spawn on this supervisor. Keep the same worker for follow-ups.

In the worker's prompt, request both notifications:
  When blocked, run swarm teammate message --kind needs-input --text "your question".
  Read the answer with swarm teammate reply --id MESSAGE_ID --wait 90s.
  When finished, run swarm teammate message --kind complete --text "result and evidence".
  Record the evidence in the native final reply too.

Keep each returned note ID. Retry an uncertain send with the same ID, kind and text.
Return after dispatch. Answer a worker question through the reply action in its wake.
Check the evidence from the worker's native turn or inspect the same session with swarm peek.
Cite what you observed. If only the note is available, attribute the claim to the worker. Publish the result in the same conversation.
`

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
