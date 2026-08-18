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
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// agentClient is the narrow daemon surface these verbs use — the read ops plus the two
// Phase 3 steering ops. Keeping it narrow (the internal/tui precedent) makes every verb
// unit testable with no daemon and no socket; *protocol.Client satisfies it as-is.
type agentClient interface {
	List() ([]protocol.SessionView, error)
	Subscribe() (<-chan protocol.Event, error)
	Kill(id string) error
	Launch(protocol.LaunchReq) (id, name string, err error)
	SendInput(id string, req protocol.SendInputReq) error
	TerminalSnapshot(id string) (*protocol.TerminalSnapshot, error)
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
		_, _ = fmt.Fprintf(stderr, "ls: %v\n", err)
		return 1
	}

	if *asJSON {
		if sessions == nil {
			sessions = []protocol.SessionView{} // an empty roster is [], never null
		}
		if err := writeJSON(stdout, sessions); err != nil {
			_, _ = fmt.Fprintf(stderr, "ls: %v\n", err)
			return 1
		}
		return 0
	}

	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tAGENT\tGROUP\tNAME")
	for _, s := range sessions {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", s.ID, s.Agent, s.Group, s.Name)
	}
	_ = tw.Flush()
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
	until := fs.String("until", string(status.GroupNeedsInput), "wait for one or more comma-separated states: needs_input, ready_for_review, completed; or change")
	timeout := fs.Duration("timeout", watchDefaultTimeout, "give up after this long")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() != 1 {
		_, _ = fmt.Fprintln(stderr, "watch: need exactly one session id: swarm watch <session>")
		return 1
	}
	id := fs.Arg(0)

	matcher, err := parseWatchUntil(*until)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "watch: %v\n", err)
		return 1
	}

	// Subscribe BEFORE the initial List so an event cannot slip through the gap
	// between the snapshot and the stream.
	events, err := c.Subscribe()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "watch: %v\n", err)
		return 1
	}
	sessions, err := c.List()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "watch: %v\n", err)
		return 1
	}
	current, ok := findSession(sessions, id)
	if !ok {
		// A typo'd or already-deleted id fails fast; blocking until the timeout would
		// be indistinguishable from "the session is still working".
		_, _ = fmt.Fprintf(stderr, "watch: no such session %q\n", id)
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
				_, _ = fmt.Fprintln(stderr, "watch: the daemon closed the event stream")
				return 1
			}
			if ev.Session.ID != id || !matcher.matchesEvent(ev.Session) {
				continue
			}
			return printView(stdout, stderr, ev.Session)
		case <-deadline:
			_, _ = fmt.Fprintf(stderr, "watch: timeout after %s waiting for %s to reach %q\n", *timeout, id, *until)
			return watchTimeoutExit
		}
	}
}

// runKill is `swarm kill <session>`: the one-line OpKill wrapper (A4).
func runKill(args []string, c agentClient, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		_, _ = fmt.Fprintln(stderr, "kill: need exactly one session id: swarm kill <session>")
		return 1
	}
	id := args[0]

	if err := c.Kill(id); err != nil {
		_, _ = fmt.Fprintf(stderr, "kill: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "killed %s\n", id)
	return 0
}

// watchMatcher decides when a watch is satisfied. `change` matches any event for the
// session and so never matches the initial snapshot: a snapshot is the state the
// caller already has, not something the session did.
type watchMatcher struct {
	anyChange bool
	groups    map[status.Group]struct{}
}

func (m watchMatcher) matchesSnapshot(v protocol.SessionView) bool {
	if m.anyChange {
		return false
	}
	_, ok := m.groups[v.Group]
	return ok
}

func (m watchMatcher) matchesEvent(v protocol.SessionView) bool {
	if m.anyChange {
		return true
	}
	_, ok := m.groups[v.Group]
	return ok
}

// parseWatchUntil resolves the --until value into its matcher, naming the offending
// value when it is not one of the four the verb accepts.
func parseWatchUntil(until string) (watchMatcher, error) {
	parts := strings.Split(until, ",")
	if len(parts) == 1 && strings.TrimSpace(parts[0]) == untilChange {
		return watchMatcher{anyChange: true}, nil
	}
	groups := make(map[status.Group]struct{}, len(parts))
	for _, raw := range parts {
		value := strings.TrimSpace(raw)
		if value == "" {
			return watchMatcher{}, fmt.Errorf("invalid --until value %q: empty state in comma-separated list", until)
		}
		if value == untilChange {
			return watchMatcher{}, fmt.Errorf("invalid --until value %q: change cannot be combined with status states", until)
		}
		switch g := status.Group(value); g {
		case status.GroupNeedsInput, status.GroupReadyForReview, status.GroupCompleted:
			groups[g] = struct{}{}
		default:
			return watchMatcher{}, fmt.Errorf("unknown --until state %q in %q: want needs_input, ready_for_review, completed, or change", value, until)
		}
	}
	return watchMatcher{groups: groups}, nil
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
		_, _ = fmt.Fprintf(stderr, "watch: %v\n", err)
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
		_, _ = fmt.Fprintf(stderr, "swarm: %v\n", err)
		return 1
	}
	client, err := protocol.Dial(cc.SocketPath, caps)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "swarm: %v (no daemon running? start swarm first)\n", err)
		return 1
	}
	defer func() { _ = client.Close() }()
	return run(args, client, stdout, stderr)
}
