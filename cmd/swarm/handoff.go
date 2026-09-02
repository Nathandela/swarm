package main

// `swarm handoff` is the first-class supervised handoff entry point. It is a
// deliberately thin owner-tier wrapper over the already-shipped List + Launch
// protocol: the source agent authors context, this command snapshots it into a
// private temporary directory of its own, and the daemon launches a linked child.
// Monitoring remains explicit through `swarm watch`, `peek`, and `send`, or is
// delegated to the daemon's passive supervisor (ADR-010 Amendment 3 C1).

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Nathandela/swarm/internal/hookclient"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

const handoffUsage = `usage: swarm handoff --cli <agent> [--model m] [--name n]
                     [--supervision passive|manual|none] --context-file <file>

  Launch a new Swarm session from an agent-authored context document. The command
  must run inside a live Swarm-managed source session. It prints the child session
  id on stdout. With --supervision passive (the default) the daemon wakes the source
  when the child needs attention; manual supervises with swarm watch, swarm peek,
  and swarm send; none leaves supervision to the human.
`

func runHandoff(args []string, c agentClient, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("handoff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprint(stderr, handoffUsage)
		fs.PrintDefaults()
	}
	cli := fs.String("cli", "", "agent CLI to receive the handoff (required)")
	model := fs.String("model", "", "model to launch the target agent with")
	name := fs.String("name", "", "target session label (default: the daemon's)")
	contextFile := fs.String("context-file", "", "agent-authored handoff context file (required)")
	supervision := fs.String("supervision", protocol.SupervisionPassive, "supervision mode: passive, manual or none")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return misuseExit
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "handoff: unexpected argument %q\n", fs.Arg(0))
		return misuseExit
	}
	if *cli == "" {
		_, _ = fmt.Fprintln(stderr, "handoff: --cli is required")
		return misuseExit
	}
	if *contextFile == "" {
		_, _ = fmt.Fprintln(stderr, "handoff: --context-file is required")
		return misuseExit
	}
	switch *supervision {
	case protocol.SupervisionPassive, protocol.SupervisionManual, protocol.SupervisionNone:
	default:
		_, _ = fmt.Fprintf(stderr, "handoff: --supervision %q is not one of passive, manual, none\n", *supervision)
		return misuseExit
	}

	parent := os.Getenv(hookclient.EnvSessionID)
	if parent == "" {
		_, _ = fmt.Fprintf(stderr, "handoff: %s is not set; run this command inside a Swarm-managed session\n", hookclient.EnvSessionID)
		return 1
	}
	sessions, err := c.List()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "handoff: list source session: %v\n", err)
		return 1
	}
	source, ok := findSourceSession(sessions, parent)
	if !ok {
		_, _ = fmt.Fprintf(stderr, "handoff: source session %q is not in the current Swarm roster\n", parent)
		return 1
	}
	if source.Status.Process != status.ProcessRunning {
		_, _ = fmt.Fprintf(stderr, "handoff: source session %q is not running\n", parent)
		return 1
	}

	dest, err := copyHandoff(*contextFile)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "handoff: copy context %q: %v\n", *contextFile, err)
		return 1
	}
	initial := "You are receiving a supervised Swarm handoff. Read and follow the context in " + dest + ". Verify the repository's current state before changing anything."
	req := protocol.LaunchReq{
		Agent:         *cli,
		Name:          *name,
		Cwd:           source.Cwd,
		Env:           os.Environ(),
		Cols:          spawnCols,
		Rows:          spawnRows,
		InitialPrompt: initial,
		SpawnedFrom:   parent,
		SpawnIntent:   protocol.SpawnIntentHandoff,
		Supervision:   *supervision,
	}
	if *model != "" {
		req.Options = map[string]string{"model": *model}
	}

	id, canonical, err := c.Launch(req)
	if err != nil {
		_ = os.RemoveAll(filepath.Dir(dest)) // the private directory is the copy's
		_, _ = fmt.Fprintf(stderr, "handoff: launch target: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintln(stdout, id)
	_, _ = fmt.Fprintf(stderr, "handed off to %s (%s)\n", canonical, id)
	return 0
}

func findSourceSession(sessions []protocol.SessionView, source string) (protocol.SessionView, bool) {
	for _, s := range sessions {
		if s.ID == source {
			return s, true
		}
		_, local, ok := protocol.ParseID(s.ID)
		if ok && local == source {
			return s, true
		}
	}
	return protocol.SessionView{}, false
}
