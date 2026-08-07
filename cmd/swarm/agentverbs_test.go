package main

// FAILING-FIRST suite for ADR-010 Amendment 1 / A4-A5 Phase 1: the agent-facing READ
// verbs `swarm ls [--json]`, `swarm watch <id>`, and `swarm kill <id>`. Phase 1 changes
// no protocol: the verbs are thin wrappers over the OpList/OpSubscribe/OpKill client
// calls that already exist.
//
// These tests are written against the FROZEN API the implementer must provide (the
// internal/tui precedent — a narrow, stub-friendly daemon surface so every verb is unit
// testable with no daemon and no socket, tui.go:31):
//
//	type agentClient interface {                        // narrow: only what the verbs use
//	    List() ([]protocol.SessionView, error)
//	    Subscribe() (<-chan protocol.Event, error)
//	    Kill(id string) error
//	}
//	func runLS(args []string, c agentClient, stdout, stderr io.Writer) int
//	func runWatch(args []string, c agentClient, stdout, stderr io.Writer) int
//	func runKill(args []string, c agentClient, stdout, stderr io.Writer) int
//
// main.go's dispatch stays thin: it resolves the socket (SWARM_DAEMON_SOCK, else the
// daemon default) and dials, then hands the *protocol.Client to these run functions.
//
// Frozen behavior:
//   - `ls`          minimal table (id, agent, group, name); `--json` marshals the FULL
//                   []protocol.SessionView (raw status dims, server-derived group, last
//                   activity, summary) so an agent reads the whole row, not a digest.
//   - `watch <id>`  consults the initial List first and exits at once when the predicate
//                   already holds; otherwise filters the Subscribe stream for that id.
//                   On match: the FINAL SessionView as JSON on stdout, exit 0. Timeout:
//                   exit 2 with a message on stderr. Bad args / unknown session: exit 1.
//                   `--until change` matches any event for that session.
//   - `kill <id>`   wraps Client.Kill and prints a confirmation; a daemon error is exit 1.
//
// RED today: none of runLS/runWatch/runKill (nor the agentClient interface) exist, so the
// package fails to compile on the undefined production symbols.

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// ---------------------------------------------------------------------------
// Fake agentClient — the only client this suite ever uses (no daemon, no socket).
// ---------------------------------------------------------------------------

type fakeAgentClient struct {
	mu       sync.Mutex
	sessions []protocol.SessionView
	listErr  error

	events chan protocol.Event
	subErr error

	killed  []string
	killErr error
}

// newFakeAgentClient returns a fake whose subscribe stream is open and empty: a verb
// that waits on events without a match blocks until its own deadline, never on a
// closed channel.
func newFakeAgentClient(sessions ...protocol.SessionView) *fakeAgentClient {
	return &fakeAgentClient{sessions: sessions, events: make(chan protocol.Event, 8)}
}

func (f *fakeAgentClient) List() ([]protocol.SessionView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]protocol.SessionView, len(f.sessions))
	copy(out, f.sessions)
	return out, nil
}

func (f *fakeAgentClient) Subscribe() (<-chan protocol.Event, error) {
	if f.subErr != nil {
		return nil, f.subErr
	}
	return f.events, nil
}

func (f *fakeAgentClient) Kill(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killed = append(f.killed, id)
	return f.killErr
}

// The fake must satisfy the narrow interface the verbs are written against — that
// interface is the whole testability contract (no daemon, no socket).
var _ agentClient = (*fakeAgentClient)(nil)

func (f *fakeAgentClient) killedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.killed...)
}

// emit queues one status-change event on the subscribe stream.
func (f *fakeAgentClient) emit(v protocol.SessionView) { f.events <- protocol.Event{Session: v} }

// fixedTime is a stable, whole-second UTC instant so JSON round-trips compare exactly.
var fixedTime = time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)

// view builds one roster row in the given group, with the raw status dimensions that
// derive it, so the --json assertions see a fully populated SessionView.
func view(id, agent, name string, g status.Group) protocol.SessionView {
	st := status.Status{Process: status.ProcessRunning, Turn: status.TurnActive, Interaction: status.InteractionNone}
	switch g {
	case status.GroupNeedsInput:
		st = status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionPrompt}
	case status.GroupReadyForReview:
		st = status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone}
	case status.GroupCompleted:
		st = status.Status{Process: status.ProcessExited, Turn: status.TurnIdle, Interaction: status.InteractionNone}
	}
	return protocol.SessionView{
		EndpointID:   "local",
		ID:           id,
		Agent:        agent,
		Name:         name,
		Cwd:          "/Users/Nathan/Code/swarm",
		Status:       st,
		Group:        g,
		LastActivity: fixedTime,
		CreatedAt:    fixedTime,
		Summary:      "summary of " + id,
	}
}

// lineWith returns the first line of out containing needle, or "" when there is none.
func lineWith(out, needle string) string {
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, needle) {
			return ln
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// swarm ls
// ---------------------------------------------------------------------------

// TestRunLS_Table pins the minimal human table: a header naming the four columns and
// one line per session carrying its id, agent, group and name. An empty roster prints
// the header alone and still exits 0 (an agent scripting against it must not have to
// special-case "no sessions" as a failure).
func TestRunLS_Table(t *testing.T) {
	cases := []struct {
		name     string
		sessions []protocol.SessionView
	}{
		{
			name: "populated roster",
			sessions: []protocol.SessionView{
				view("local/a1", "claude", "refactor", status.GroupNeedsInput),
				view("local/b2", "codex", "docs", status.GroupWorking),
				view("local/c3", "gemini", "", status.GroupCompleted),
			},
		},
		{name: "empty roster"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			client := newFakeAgentClient(c.sessions...)
			var stdout, stderr bytes.Buffer
			if exit := runLS(nil, client, &stdout, &stderr); exit != 0 {
				t.Fatalf("runLS exit = %d, want 0; stderr=%q", exit, stderr.String())
			}
			out := stdout.String()
			for _, col := range []string{"ID", "AGENT", "GROUP", "NAME"} {
				if !strings.Contains(out, col) {
					t.Errorf("table header missing column %q; got:\n%s", col, out)
				}
			}
			for _, s := range c.sessions {
				row := lineWith(out, s.ID)
				if row == "" {
					t.Fatalf("no table row for session %q; got:\n%s", s.ID, out)
				}
				if !strings.Contains(row, s.Agent) {
					t.Errorf("row %q missing agent %q", row, s.Agent)
				}
				if !strings.Contains(row, string(s.Group)) {
					t.Errorf("row %q missing group %q", row, s.Group)
				}
				if s.Name != "" && !strings.Contains(row, s.Name) {
					t.Errorf("row %q missing name %q", row, s.Name)
				}
			}
		})
	}
}

// TestRunLS_JSON pins the machine-readable shape an agent parses: a JSON ARRAY of the
// FULL SessionView (A4), not a reduced projection. It decodes back into the protocol
// type to compare values, and separately into a generic map to prove the whole field
// set — raw status dims, server-derived group, activity times, summary — survives.
func TestRunLS_JSON(t *testing.T) {
	sessions := []protocol.SessionView{
		view("local/a1", "claude", "refactor", status.GroupNeedsInput),
		view("local/b2", "codex", "docs", status.GroupWorking),
	}
	client := newFakeAgentClient(sessions...)

	var stdout, stderr bytes.Buffer
	if exit := runLS([]string{"--json"}, client, &stdout, &stderr); exit != 0 {
		t.Fatalf("runLS([--json]) exit = %d, want 0; stderr=%q", exit, stderr.String())
	}

	var got []protocol.SessionView
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("--json output is not a []SessionView: %v; got:\n%s", err, stdout.String())
	}
	if len(got) != len(sessions) {
		t.Fatalf("--json returned %d sessions, want %d", len(got), len(sessions))
	}
	for i, want := range sessions {
		g := got[i]
		if g.ID != want.ID || g.Agent != want.Agent || g.Name != want.Name || g.Cwd != want.Cwd {
			t.Errorf("session %d identity = %+v, want id/agent/name/cwd of %+v", i, g, want)
		}
		if g.Group != want.Group {
			t.Errorf("session %d group = %q, want %q", i, g.Group, want.Group)
		}
		if g.Status != want.Status {
			t.Errorf("session %d raw status = %+v, want %+v", i, g.Status, want.Status)
		}
		if g.Summary != want.Summary {
			t.Errorf("session %d summary = %q, want %q", i, g.Summary, want.Summary)
		}
		if !g.LastActivity.Equal(want.LastActivity) || !g.CreatedAt.Equal(want.CreatedAt) {
			t.Errorf("session %d times = (%v, %v), want (%v, %v)", i, g.LastActivity, g.CreatedAt, want.LastActivity, want.CreatedAt)
		}
	}

	var raw []map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		t.Fatalf("--json output is not an array of objects: %v", err)
	}
	for _, key := range []string{"id", "agent", "name", "cwd", "status", "group", "last_activity", "created_at", "summary", "endpoint_id"} {
		if _, ok := raw[0][key]; !ok {
			t.Errorf("--json object is missing SessionView field %q; got keys %v", key, mapKeys(raw[0]))
		}
	}
}

func mapKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestRunLS_ListError pins that a daemon-side List failure is an error exit with the
// cause on stderr, never an empty table that reads as "no sessions".
func TestRunLS_ListError(t *testing.T) {
	client := newFakeAgentClient()
	client.listErr = errFakeDaemon

	var stdout, stderr bytes.Buffer
	if exit := runLS(nil, client, &stdout, &stderr); exit != 1 {
		t.Fatalf("runLS with a failing List exit = %d, want 1; stdout=%q", exit, stdout.String())
	}
	if !strings.Contains(stderr.String(), errFakeDaemon.Error()) {
		t.Errorf("stderr = %q, want the daemon error %q", stderr.String(), errFakeDaemon)
	}
}

// errFakeDaemon is the injected daemon-side failure the error paths surface.
var errFakeDaemon = errStub("daemon is not answering")

type errStub string

func (e errStub) Error() string { return string(e) }

// ---------------------------------------------------------------------------
// swarm watch
// ---------------------------------------------------------------------------

// TestRunWatch_ImmediateMatch pins the initial-List check: when the session ALREADY
// satisfies the predicate, watch prints that SessionView and exits 0 at once instead of
// waiting for an event that may never come (a completed session emits nothing further).
// The generous --timeout plus the elapsed bound is what makes "immediately" testable:
// an implementation that only ever waits on the stream would burn the full timeout and
// exit 2.
func TestRunWatch_ImmediateMatch(t *testing.T) {
	cases := []struct {
		until string
		group status.Group
	}{
		{"needs_input", status.GroupNeedsInput},
		{"ready_for_review", status.GroupReadyForReview},
		{"completed", status.GroupCompleted},
	}
	for _, c := range cases {
		t.Run(c.until, func(t *testing.T) {
			target := view("local/a1", "claude", "refactor", c.group)
			client := newFakeAgentClient(view("local/z9", "codex", "other", status.GroupWorking), target)

			var stdout, stderr bytes.Buffer
			start := time.Now()
			exit := runWatch([]string{"--until", c.until, "--timeout", "3s", "local/a1"}, client, &stdout, &stderr)
			elapsed := time.Since(start)

			if exit != 0 {
				t.Fatalf("runWatch exit = %d, want 0; stderr=%q", exit, stderr.String())
			}
			if elapsed > time.Second {
				t.Errorf("runWatch took %v on an already-satisfied predicate; want an immediate return", elapsed)
			}
			got := decodeView(t, stdout.Bytes())
			if got.ID != target.ID {
				t.Errorf("printed session id = %q, want %q", got.ID, target.ID)
			}
			if got.Group != c.group {
				t.Errorf("printed group = %q, want %q", got.Group, c.group)
			}
		})
	}
}

// TestRunWatch_MatchAfterEvents pins the stream filter: events for OTHER sessions and
// non-matching events for the watched session are ignored, and the FINAL (matching)
// SessionView is what lands on stdout — not the stale row from the initial List.
func TestRunWatch_MatchAfterEvents(t *testing.T) {
	cases := []struct {
		name  string
		until string
		// events are pushed in order; the last one is the expected match.
		events []protocol.SessionView
	}{
		{
			name:  "group predicate",
			until: "needs_input",
			events: []protocol.SessionView{
				view("local/z9", "codex", "other", status.GroupNeedsInput),  // wrong session
				view("local/a1", "claude", "refactor", status.GroupWorking), // right session, wrong group
				view("local/a1", "claude", "refactor", status.GroupNeedsInput),
			},
		},
		{
			// --until change: ANY event for that session matches, even one whose group
			// is unchanged (the summary moved, so the session did something).
			name:  "change matches any event for the session",
			until: "change",
			events: []protocol.SessionView{
				view("local/z9", "codex", "other", status.GroupCompleted), // wrong session
				view("local/a1", "claude", "refactor", status.GroupWorking),
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// The initial List does NOT satisfy the predicate, so watch must reach the stream.
			client := newFakeAgentClient(view("local/a1", "claude", "refactor", status.GroupWorking))
			for _, e := range c.events {
				client.emit(e)
			}

			var stdout, stderr bytes.Buffer
			exit := runWatch([]string{"--until", c.until, "--timeout", "3s", "local/a1"}, client, &stdout, &stderr)
			if exit != 0 {
				t.Fatalf("runWatch exit = %d, want 0; stderr=%q", exit, stderr.String())
			}
			want := c.events[len(c.events)-1]
			got := decodeView(t, stdout.Bytes())
			if got.ID != want.ID {
				t.Errorf("printed session id = %q, want %q", got.ID, want.ID)
			}
			if got.Group != want.Group {
				t.Errorf("printed group = %q, want %q", got.Group, want.Group)
			}
			if got.Summary != want.Summary {
				t.Errorf("printed summary = %q, want the final event's %q", got.Summary, want.Summary)
			}
		})
	}
}

// TestRunWatch_Timeout pins the distinct timeout exit: no matching event before the
// deadline is exit code 2 (not 0, not the generic error 1) with an explanation on
// stderr, and nothing that could be mistaken for a SessionView on stdout.
func TestRunWatch_Timeout(t *testing.T) {
	client := newFakeAgentClient(view("local/a1", "claude", "refactor", status.GroupWorking))
	// One non-matching event so the timeout is proven against a live stream, not a dead one.
	client.emit(view("local/a1", "claude", "refactor", status.GroupWorking))

	var stdout, stderr bytes.Buffer
	exit := runWatch([]string{"--until", "completed", "--timeout", "80ms", "local/a1"}, client, &stdout, &stderr)
	if exit != 2 {
		t.Fatalf("runWatch on timeout exit = %d, want 2; stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
	if !strings.Contains(strings.ToLower(stderr.String()), "timeout") && !strings.Contains(strings.ToLower(stderr.String()), "timed out") {
		t.Errorf("stderr = %q, want a timeout explanation", stderr.String())
	}
	if strings.Contains(stdout.String(), "\"id\"") {
		t.Errorf("stdout = %q, want no SessionView on a timeout", stdout.String())
	}
}

// TestRunWatch_BadArgs pins exit 1 for every argument-level refusal, including an
// unknown session id: watch resolves the target against the initial List, so a typo'd
// or already-deleted id fails fast rather than blocking until the timeout (which would
// be indistinguishable from "still working").
func TestRunWatch_BadArgs(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string // substring the message must carry
	}{
		{"no session id", []string{"--until", "needs_input", "--timeout", "2s"}, "session"},
		{"unknown --until value", []string{"--until", "bogus", "--timeout", "2s", "local/a1"}, "bogus"},
		{"unknown session", []string{"--until", "needs_input", "--timeout", "2s", "local/nope"}, "local/nope"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			client := newFakeAgentClient(view("local/a1", "claude", "refactor", status.GroupWorking))
			var stdout, stderr bytes.Buffer
			start := time.Now()
			exit := runWatch(c.args, client, &stdout, &stderr)
			if exit != 1 {
				t.Fatalf("runWatch(%v) exit = %d, want 1; stdout=%q stderr=%q", c.args, exit, stdout.String(), stderr.String())
			}
			if elapsed := time.Since(start); elapsed > time.Second {
				t.Errorf("runWatch(%v) took %v; a bad-args refusal must be immediate", c.args, elapsed)
			}
			if !strings.Contains(stderr.String(), c.want) {
				t.Errorf("stderr = %q, want a message naming %q", stderr.String(), c.want)
			}
		})
	}
}

// decodeView parses the single SessionView object a matching watch prints on stdout.
func decodeView(t *testing.T, b []byte) protocol.SessionView {
	t.Helper()
	var v protocol.SessionView
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("watch stdout is not a SessionView object: %v; got:\n%s", err, string(b))
	}
	return v
}

// ---------------------------------------------------------------------------
// swarm kill
// ---------------------------------------------------------------------------

// TestRunKill pins the one-line OpKill wrapper: the id reaches the client verbatim and
// a confirmation naming it lands on stdout; a daemon refusal is exit 1 with the cause on
// stderr and no confirmation; a missing id is an argument refusal that never dials.
func TestRunKill(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		client := newFakeAgentClient(view("local/a1", "claude", "refactor", status.GroupWorking))
		var stdout, stderr bytes.Buffer
		if exit := runKill([]string{"local/a1"}, client, &stdout, &stderr); exit != 0 {
			t.Fatalf("runKill exit = %d, want 0; stderr=%q", exit, stderr.String())
		}
		if got := client.killedIDs(); len(got) != 1 || got[0] != "local/a1" {
			t.Fatalf("Kill calls = %v, want exactly [local/a1]", got)
		}
		if !strings.Contains(stdout.String(), "local/a1") {
			t.Errorf("stdout = %q, want a confirmation naming the killed session", stdout.String())
		}
	})

	t.Run("daemon error", func(t *testing.T) {
		client := newFakeAgentClient()
		client.killErr = errFakeDaemon
		var stdout, stderr bytes.Buffer
		if exit := runKill([]string{"local/a1"}, client, &stdout, &stderr); exit != 1 {
			t.Fatalf("runKill with a failing Kill exit = %d, want 1; stdout=%q", exit, stdout.String())
		}
		if !strings.Contains(stderr.String(), errFakeDaemon.Error()) {
			t.Errorf("stderr = %q, want the daemon error %q", stderr.String(), errFakeDaemon)
		}
		if strings.Contains(stdout.String(), "local/a1") {
			t.Errorf("stdout = %q, want no confirmation when the kill failed", stdout.String())
		}
	})

	t.Run("missing session id", func(t *testing.T) {
		client := newFakeAgentClient()
		var stdout, stderr bytes.Buffer
		if exit := runKill(nil, client, &stdout, &stderr); exit != 1 {
			t.Fatalf("runKill(nil) exit = %d, want 1; stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
		}
		if got := client.killedIDs(); len(got) != 0 {
			t.Errorf("Kill calls = %v, want none on an argument refusal", got)
		}
		if !strings.Contains(stderr.String(), "session") {
			t.Errorf("stderr = %q, want a message naming the missing session argument", stderr.String())
		}
	})
}

// ---------------------------------------------------------------------------
// dispatch surface
// ---------------------------------------------------------------------------

// TestUsage_ListsAgentVerbs pins that the three Phase 1 verbs are documented in the
// top-level usage string, so an agent that runs bare `swarm` discovers them.
func TestUsage_ListsAgentVerbs(t *testing.T) {
	for _, verb := range []string{"ls", "watch", "kill"} {
		if !strings.Contains(usage, "swarm "+verb) {
			t.Errorf("usage does not document `swarm %s`; got:\n%s", verb, usage)
		}
	}
}
