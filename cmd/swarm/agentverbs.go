package main

// The agent-facing READ verbs of ADR-010 Amendment 1 (A4/A5 Phase 1): `swarm ls`,
// `swarm watch`, and `swarm kill`. They are thin wrappers over the OpList /
// OpSubscribe / OpKill client calls that already exist, so Phase 1 changes no
// protocol. Shelling out to `swarm` is the lowest common denominator every target
// CLI supports with zero per-CLI configuration (D1).

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// agentClient is the narrow daemon surface these verbs use — only List, Subscribe
// and Kill. Keeping it narrow (the internal/tui precedent) makes every verb unit
// testable with no daemon and no socket; *protocol.Client satisfies it as-is.
type agentClient interface {
	List() ([]protocol.SessionView, error)
	Subscribe() (<-chan protocol.Event, error)
	Kill(id string) error
}

// watchTimeoutExit is the exit code a watch that reached its deadline without a
// match returns. It is distinct from the generic error exit so a caller can tell
// "still working" from "the command failed".
const watchTimeoutExit = 2

// watchDefaultTimeout bounds a watch that is never satisfied. Long enough for a
// real agent turn, short enough that a forgotten watch does not outlive its session.
const watchDefaultTimeout = 10 * time.Minute

// untilChange is the --until value that matches ANY event for the watched session,
// whatever its group.
const untilChange = "change"

// runLS is `swarm ls [--json]`: the session roster, either as a minimal human table
// or, with --json, as the FULL []protocol.SessionView (A4) — raw status dimensions,
// the server-derived group, activity times and summary — so an agent reads the whole
// row rather than a digest it would have to guess the shape of.
func runLS(args []string, c agentClient, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print the full session roster as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	sessions, err := c.List()
	if err != nil {
		fmt.Fprintf(stderr, "ls: %v\n", err)
		return 1
	}

	if *asJSON {
		if sessions == nil {
			sessions = []protocol.SessionView{} // an empty roster is [], never null
		}
		if err := writeJSON(stdout, sessions); err != nil {
			fmt.Fprintf(stderr, "ls: %v\n", err)
			return 1
		}
		return 0
	}

	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tAGENT\tGROUP\tNAME")
	for _, s := range sessions {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", s.ID, s.Agent, s.Group, s.Name)
	}
	tw.Flush()
	return 0
}

// runWatch is `swarm watch <session> [--until ...] [--timeout d]`: it blocks until
// the session satisfies the predicate, then prints that SessionView as JSON. The
// initial List is consulted first, because a session that has ALREADY reached the
// target group may never emit another event. Reaching the deadline is
// watchTimeoutExit, distinct from the argument and daemon failures that exit 1.
func runWatch(args []string, c agentClient, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	until := fs.String("until", string(status.GroupNeedsInput), "wait for needs_input, ready_for_review, completed, or change")
	timeout := fs.Duration("timeout", watchDefaultTimeout, "give up after this long")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "watch: need exactly one session id: swarm watch <session>")
		return 1
	}
	id := fs.Arg(0)

	matcher, err := parseWatchUntil(*until)
	if err != nil {
		fmt.Fprintf(stderr, "watch: %v\n", err)
		return 1
	}

	// Subscribe BEFORE the initial List so an event cannot slip through the gap
	// between the snapshot and the stream.
	events, err := c.Subscribe()
	if err != nil {
		fmt.Fprintf(stderr, "watch: %v\n", err)
		return 1
	}
	sessions, err := c.List()
	if err != nil {
		fmt.Fprintf(stderr, "watch: %v\n", err)
		return 1
	}
	current, ok := findSession(sessions, id)
	if !ok {
		// A typo'd or already-deleted id fails fast; blocking until the timeout would
		// be indistinguishable from "the session is still working".
		fmt.Fprintf(stderr, "watch: no such session %q\n", id)
		return 1
	}
	if matcher.matchesSnapshot(current) {
		return printView(stdout, stderr, current)
	}

	deadline := time.After(*timeout)
	for {
		select {
		case ev, open := <-events:
			if !open {
				fmt.Fprintln(stderr, "watch: the daemon closed the event stream")
				return 1
			}
			if ev.Session.ID != id || !matcher.matchesEvent(ev.Session) {
				continue
			}
			return printView(stdout, stderr, ev.Session)
		case <-deadline:
			fmt.Fprintf(stderr, "watch: timeout after %s waiting for %s to reach %q\n", *timeout, id, *until)
			return watchTimeoutExit
		}
	}
}

// runKill is `swarm kill <session>`: the one-line OpKill wrapper (A4).
func runKill(args []string, c agentClient, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "kill: need exactly one session id: swarm kill <session>")
		return 1
	}
	id := args[0]

	if err := c.Kill(id); err != nil {
		fmt.Fprintf(stderr, "kill: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "killed %s\n", id)
	return 0
}

// watchMatcher decides when a watch is satisfied. `change` matches any event for the
// session and so never matches the initial snapshot: a snapshot is the state the
// caller already has, not something the session did.
type watchMatcher struct {
	anyChange bool
	group     status.Group
}

func (m watchMatcher) matchesSnapshot(v protocol.SessionView) bool {
	return !m.anyChange && v.Group == m.group
}

func (m watchMatcher) matchesEvent(v protocol.SessionView) bool {
	return m.anyChange || v.Group == m.group
}

// parseWatchUntil resolves the --until value into its matcher, naming the offending
// value when it is not one of the four the verb accepts.
func parseWatchUntil(until string) (watchMatcher, error) {
	if until == untilChange {
		return watchMatcher{anyChange: true}, nil
	}
	switch g := status.Group(until); g {
	case status.GroupNeedsInput, status.GroupReadyForReview, status.GroupCompleted:
		return watchMatcher{group: g}, nil
	}
	return watchMatcher{}, fmt.Errorf("unknown --until value %q: want needs_input, ready_for_review, completed, or change", until)
}

// findSession returns the roster row with the given namespaced id.
func findSession(sessions []protocol.SessionView, id string) (protocol.SessionView, bool) {
	for _, s := range sessions {
		if s.ID == id {
			return s, true
		}
	}
	return protocol.SessionView{}, false
}

// printView writes the one SessionView a matching watch reports on stdout.
func printView(stdout, stderr io.Writer, v protocol.SessionView) int {
	if err := writeJSON(stdout, v); err != nil {
		fmt.Fprintf(stderr, "watch: %v\n", err)
		return 1
	}
	return 0
}

// writeJSON emits v as indented JSON, the form an agent both parses and reads.
func writeJSON(stdout io.Writer, v any) error {
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// dispatchAgentVerb is the live path behind the narrow interface: it resolves the
// daemon socket (SWARM_DAEMON_SOCK, else the state dir default — the same resolution
// clientConfig does for every other client role), dials it, and runs one verb against
// the real protocol client. Unlike dialClient it does NOT auto-start a daemon: these
// verbs report on sessions that already exist, so a socket nothing answers is a fact
// to surface, not a daemon to spawn.
func dispatchAgentVerb(run func([]string, agentClient, io.Writer, io.Writer) int, args, caps []string, stdout, stderr io.Writer) int {
	cc, err := clientConfig()
	if err != nil {
		fmt.Fprintf(stderr, "swarm: %v\n", err)
		return 1
	}
	client, err := protocol.Dial(cc.SocketPath, caps)
	if err != nil {
		fmt.Fprintf(stderr, "swarm: %v\n", err)
		return 1
	}
	defer client.Close()
	return run(args, client, stdout, stderr)
}
