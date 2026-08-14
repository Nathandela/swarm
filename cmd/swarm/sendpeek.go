package main

// The agent-facing STEERING verbs of ADR-010 Amendment 1 (A2/A3, Phase 3): `swarm send`
// and `swarm peek`. Like the Phase 1/2 verbs they are thin wrappers over the protocol
// client — `send` over the owner-tier `send_input` op, `peek` over the now owner-tier-
// reachable `terminal_snapshot` render.
//
// Both take the SESSION FIRST and parse the rest as flags. Go's flag package stops at the
// first non-flag argument, so the id is taken off the front before parsing; a first
// argument that looks like a flag means no session was named and is misuse.

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/Nathandela/swarm/internal/protocol"
)

// misuseExit is the exit code for an argument error, distinct from the exit 1 that means
// the command line was fine and the daemon refused (the runSpawn convention).
const misuseExit = 2

// runSend is `swarm send <session> (--text s [--no-submit] | --key enter|esc|ctrl-c|tab|up|down)`.
// EXACTLY ONE of --text/--key: a verb that can inject keystrokes into somebody else's
// session must refuse an ambiguous instruction rather than guess which half was meant.
// --text submits by default (a delivered message is the point of the verb); --no-submit
// leaves it unsent and is meaningless with --key. Success is SILENT, exit 0: an agent
// scripting a fan-out reads nothing back, and stdout stays free for the verbs that print.
func runSend(args []string, c agentClient, stdout, stderr io.Writer) int {
	id, rest, ok := takeSessionID(args)
	if !ok {
		_, _ = fmt.Fprintln(stderr, "send: need a session id first: swarm send <session> (--text s [--no-submit] | --key name)")
		return misuseExit
	}
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	fs.SetOutput(stderr)
	text := fs.String("text", "", "message to type into the session")
	noSubmit := fs.Bool("no-submit", false, "leave the text unsent instead of submitting it")
	key := fs.String("key", "", "one named key: enter, esc, ctrl-c, tab, up, down")
	if err := fs.Parse(rest); err != nil {
		return misuseExit
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "send: unexpected argument %q; the message goes in --text\n", fs.Arg(0))
		return misuseExit
	}

	var req protocol.SendInputReq
	switch {
	case (*text == "") == (*key == ""):
		_, _ = fmt.Fprintln(stderr, "send: need exactly one of --text or --key")
		return misuseExit
	case *key != "":
		if *noSubmit {
			_, _ = fmt.Fprintln(stderr, "send: --no-submit only applies to --text")
			return misuseExit
		}
		// The daemon owns the name -> bytes mapping; validating against the same closed
		// vocabulary (protocol.KeySequence) keeps the verb from carrying a second copy of
		// the list that could drift from it.
		if _, known := protocol.KeySequence(*key); !known {
			_, _ = fmt.Fprintf(stderr, "send: unknown --key %q: want enter, esc, ctrl-c, tab, up or down\n", *key)
			return misuseExit
		}
		req.Key = *key
	default:
		req.Text, req.Submit = *text, !*noSubmit
	}

	if err := c.SendInput(id, req); err != nil {
		_, _ = fmt.Fprintf(stderr, "send: %v\n", err)
		return 1
	}
	return 0
}

// runPeek is `swarm peek <session> [--lines N]`: the session's current screen, rendered
// and sanitized daemon-side, one line per line in order. --lines N prints the LAST N — a
// screen's tail is what a steering agent reads before deciding what to send — and N past
// the screen height prints what there is. A session that has printed nothing yet is a fact
// to report (exit 0, no output), not an error to script around.
func runPeek(args []string, c agentClient, stdout, stderr io.Writer) int {
	id, rest, ok := takeSessionID(args)
	if !ok {
		_, _ = fmt.Fprintln(stderr, "peek: need a session id first: swarm peek <session> [--lines N]")
		return misuseExit
	}
	fs := flag.NewFlagSet("peek", flag.ContinueOnError)
	fs.SetOutput(stderr)
	lines := fs.Int("lines", 0, "print only the last N lines (default: the whole screen)")
	if err := fs.Parse(rest); err != nil {
		return misuseExit
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "peek: unexpected argument %q\n", fs.Arg(0))
		return misuseExit
	}
	// The zero default means "the whole screen", so an EXPLICIT --lines 0 is misuse and
	// must be told apart from the flag being absent.
	explicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "lines" {
			explicit = true
		}
	})
	if explicit && *lines <= 0 {
		_, _ = fmt.Fprintf(stderr, "peek: --lines must be a positive count, got %d\n", *lines)
		return misuseExit
	}

	snap, err := c.TerminalSnapshot(id)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "peek: %v\n", err)
		return 1
	}
	if snap == nil {
		_, _ = fmt.Fprintln(stderr, "peek: the daemon returned no snapshot")
		return 1
	}
	out := snap.Lines
	if *lines > 0 && len(out) > *lines {
		out = out[len(out)-*lines:]
	}
	for _, line := range out {
		_, _ = fmt.Fprintln(stdout, line)
	}
	return 0
}

// takeSessionID splits the leading session id off a session-first verb's arguments. An
// empty argument list, or a first argument that looks like a flag, means no session was
// named — the verb must refuse rather than steer or read whatever it can find.
func takeSessionID(args []string) (id string, rest []string, ok bool) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return "", nil, false
	}
	return args[0], args[1:], true
}
