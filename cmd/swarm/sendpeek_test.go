package main

// FAILING-FIRST suite for ADR-010 Amendment 1 A2/A3 Phase 3 PIECE 3: the two agent-facing
// STEERING verbs.
//
//	swarm send <session> (--text s [--no-submit] | --key enter|esc|ctrl-c|tab|up|down)
//	swarm peek <session> [--lines N]
//
// FROZEN API — the Phase 1/2 pattern, a narrow stub-friendly daemon surface widened by the
// two methods these verbs need, so both are unit testable with no daemon and no socket:
//
//	type agentClient interface {
//	    List() ([]protocol.SessionView, error)
//	    Subscribe() (<-chan protocol.Event, error)
//	    Kill(id string) error
//	    Launch(protocol.LaunchReq) (id, name string, err error)
//	    SendInput(id string, req protocol.SendInputReq) error          // == protocol.Client.SendInput
//	    TerminalSnapshot(id string) (*protocol.TerminalSnapshot, error) // == protocol.Client.TerminalSnapshot
//	}
//	func runSend(args []string, c agentClient, stdout, stderr io.Writer) int
//	func runPeek(args []string, c agentClient, stdout, stderr io.Writer) int
//
// ARGUMENT ORDER. Both verbs take the SESSION FIRST and parse the rest as flags
// (`swarm send local/a1 --text "..."`) — the form ADR-010 A2/A3 and the usage block write.
// Go's flag package stops at the first non-flag argument, so the id is taken off the front
// before parsing; a first argument that looks like a flag means no session was named and
// is misuse.
//
// FROZEN BEHAVIOR:
//   - send: EXACTLY ONE of --text/--key. --text submits by default (the point of the verb
//     is a delivered message); --no-submit leaves the text unsent and is meaningless — so
//     refused — with --key. --key values come from the protocol's closed vocabulary
//     (protocol.KeySequence), never a second copy of the list. Success is SILENT, exit 0:
//     an agent scripting a fan-out reads nothing back, and stdout stays free for the
//     verbs that do print.
//   - peek: prints the rendered lines, one per line, in order; --lines N prints the LAST N
//     (a screen's tail is what a steering agent reads). N <= 0 is misuse.
//   - Misuse exits 2 (runSpawn's convention) and calls the daemon ZERO times; a daemon
//     failure exits 1 with the cause on stderr.
//
// RED today: runSend, runPeek and the two agentClient methods do not exist, so the package
// fails to compile ("undefined-only" red). TestUsage_ListsSteeringVerbs fails afterwards
// until cmd/swarm/main.go's usage and dispatch carry the verbs.

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// The two methods below keep the Phase 1/2 fakes satisfying the WIDENED interface (their
// `var _ agentClient` assertions live in agentverbs_test.go / spawn_test.go). The steering
// tests use fakeSteerClient, which records; a call landing here is a wiring mistake, so it
// reports one.
func (f *fakeAgentClient) SendInput(string, protocol.SendInputReq) error {
	return errStub("fakeAgentClient.SendInput: the steering tests use fakeSteerClient")
}

func (f *fakeAgentClient) TerminalSnapshot(string) (*protocol.TerminalSnapshot, error) {
	return nil, errStub("fakeAgentClient.TerminalSnapshot: the steering tests use fakeSteerClient")
}

// sendCall is one SendInput the verb issued: the whole point of the send tests is the
// exact (session, request) pair the flags build.
type sendCall struct {
	id  string
	req protocol.SendInputReq
}

// fakeSteerClient records both steering calls and can inject a daemon-side failure.
type fakeSteerClient struct {
	*fakeAgentClient

	mu sync.Mutex

	sends   []sendCall
	sendErr error

	peeked  []string
	snap    *protocol.TerminalSnapshot
	peekErr error
}

func newFakeSteerClient() *fakeSteerClient {
	return &fakeSteerClient{fakeAgentClient: newFakeAgentClient()}
}

func (f *fakeSteerClient) SendInput(id string, req protocol.SendInputReq) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sends = append(f.sends, sendCall{id: id, req: req})
	return f.sendErr
}

func (f *fakeSteerClient) TerminalSnapshot(id string) (*protocol.TerminalSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.peeked = append(f.peeked, id)
	if f.peekErr != nil {
		return nil, f.peekErr
	}
	return f.snap, nil
}

func (f *fakeSteerClient) sendCalls() []sendCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sendCall(nil), f.sends...)
}

func (f *fakeSteerClient) peekCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.peeked...)
}

var _ agentClient = (*fakeSteerClient)(nil)

// onlySend returns the single SendInput the verb must have issued.
func onlySend(t *testing.T, c *fakeSteerClient) sendCall {
	t.Helper()
	got := c.sendCalls()
	if len(got) != 1 {
		t.Fatalf("SendInput called %d times, want exactly 1", len(got))
	}
	return got[0]
}

// ---------------------------------------------------------------------------
// swarm send
// ---------------------------------------------------------------------------

// TestRunSend_TextSubmitsByDefault: `swarm send <id> --text "..."` delivers the message AND
// runs it. Success is silent — nothing on stdout, nothing on stderr, exit 0.
func TestRunSend_TextSubmitsByDefault(t *testing.T) {
	c := newFakeSteerClient()
	var stdout, stderr bytes.Buffer

	if exit := runSend([]string{"local/a1", "--text", "run the failing test"}, c, &stdout, &stderr); exit != 0 {
		t.Fatalf("runSend exit = %d, want 0; stderr=%q", exit, stderr.String())
	}
	got := onlySend(t, c)
	if got.id != "local/a1" {
		t.Errorf("SendInput session = %q, want %q", got.id, "local/a1")
	}
	want := protocol.SendInputReq{Text: "run the failing test", Submit: true}
	if got.req != want {
		t.Errorf("SendInput request = %+v, want %+v (text submits by default)", got.req, want)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Errorf("a successful send printed stdout=%q stderr=%q; success is silent", stdout.String(), stderr.String())
	}
}

// TestRunSend_NoSubmitLeavesTheTextUnsent: --no-submit is the "type it, do not run it" case.
func TestRunSend_NoSubmitLeavesTheTextUnsent(t *testing.T) {
	c := newFakeSteerClient()
	var stdout, stderr bytes.Buffer

	if exit := runSend([]string{"local/a1", "--text", "draft", "--no-submit"}, c, &stdout, &stderr); exit != 0 {
		t.Fatalf("runSend exit = %d, want 0; stderr=%q", exit, stderr.String())
	}
	want := protocol.SendInputReq{Text: "draft"}
	if got := onlySend(t, c).req; got != want {
		t.Errorf("SendInput request = %+v, want %+v", got, want)
	}
}

// TestRunSend_KeyVocabulary: every name in the protocol's closed vocabulary is accepted and
// passed through verbatim (the daemon owns the name -> bytes mapping; the verb never
// restates it), with no text and no submit flag.
func TestRunSend_KeyVocabulary(t *testing.T) {
	for _, key := range []string{"enter", "esc", "ctrl-c", "tab", "up", "down"} {
		t.Run(key, func(t *testing.T) {
			c := newFakeSteerClient()
			var stdout, stderr bytes.Buffer

			if exit := runSend([]string{"local/a1", "--key", key}, c, &stdout, &stderr); exit != 0 {
				t.Fatalf("runSend --key %s exit = %d, want 0; stderr=%q", key, exit, stderr.String())
			}
			want := protocol.SendInputReq{Key: key}
			if got := onlySend(t, c).req; got != want {
				t.Errorf("SendInput request = %+v, want %+v", got, want)
			}
		})
	}
}

// TestRunSend_Misuse: every argument error exits 2 and reaches the daemon ZERO times. A
// verb that can inject keystrokes must refuse an ambiguous instruction rather than guess
// which half of it the caller meant.
func TestRunSend_Misuse(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"no arguments", nil},
		{"session but no instruction", []string{"local/a1"}},
		{"no session", []string{"--text", "hi"}},
		{"both text and key", []string{"local/a1", "--text", "hi", "--key", "enter"}},
		{"no-submit with a key", []string{"local/a1", "--key", "enter", "--no-submit"}},
		{"unknown key", []string{"local/a1", "--key", "f13"}},
		{"key wrong case", []string{"local/a1", "--key", "Enter"}},
		{"unknown flag", []string{"local/a1", "--text", "hi", "--now"}},
		{"extra positional", []string{"local/a1", "local/b2", "--text", "hi"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newFakeSteerClient()
			var stdout, stderr bytes.Buffer

			if exit := runSend(tc.args, c, &stdout, &stderr); exit != 2 {
				t.Fatalf("runSend(%v) exit = %d, want 2 (argument misuse)", tc.args, exit)
			}
			if n := len(c.sendCalls()); n != 0 {
				t.Fatalf("misuse still issued %d SendInput calls; want 0", n)
			}
			if stderr.Len() == 0 {
				t.Error("misuse printed nothing on stderr; the caller must be told what was wrong")
			}
		})
	}
}

// TestRunSend_EmptyTextIsMisuse: `--text ""` names no message. It exits 2 rather than
// writing an empty frame into somebody's session.
func TestRunSend_EmptyTextIsMisuse(t *testing.T) {
	c := newFakeSteerClient()
	var stdout, stderr bytes.Buffer

	if exit := runSend([]string{"local/a1", "--text", ""}, c, &stdout, &stderr); exit != 2 {
		t.Fatalf("runSend with empty --text exit = %d, want 2", exit)
	}
	if n := len(c.sendCalls()); n != 0 {
		t.Fatalf("empty --text still issued %d SendInput calls; want 0", n)
	}
}

// TestRunSend_TextMayLookLikeFlagsOrSpanLines: the message is a value, not a command line.
func TestRunSend_TextMayLookLikeFlagsOrSpanLines(t *testing.T) {
	const msg = "--not-a-flag\nsecond line\twith a tab"
	c := newFakeSteerClient()
	var stdout, stderr bytes.Buffer

	if exit := runSend([]string{"local/a1", "--text", msg}, c, &stdout, &stderr); exit != 0 {
		t.Fatalf("runSend exit = %d, want 0; stderr=%q", exit, stderr.String())
	}
	if got := onlySend(t, c).req.Text; got != msg {
		t.Errorf("SendInput text = %q, want %q verbatim", got, msg)
	}
}

// TestRunSend_DaemonError: a refusal from the daemon is exit 1 with the cause on stderr —
// distinct from the exit 2 that means the command line itself was wrong.
func TestRunSend_DaemonError(t *testing.T) {
	c := newFakeSteerClient()
	c.sendErr = errFakeDaemon
	var stdout, stderr bytes.Buffer

	if exit := runSend([]string{"local/a1", "--text", "hi"}, c, &stdout, &stderr); exit != 1 {
		t.Fatalf("runSend with a failing daemon exit = %d, want 1", exit)
	}
	if !strings.Contains(stderr.String(), errFakeDaemon.Error()) {
		t.Errorf("stderr = %q, want it to name the daemon failure %q", stderr.String(), errFakeDaemon)
	}
	if stdout.Len() != 0 {
		t.Errorf("a failed send printed %q on stdout; want nothing", stdout.String())
	}
}

// ---------------------------------------------------------------------------
// swarm peek
// ---------------------------------------------------------------------------

// peekSnap builds a rendered snapshot fixture.
func peekSnap(lines ...string) *protocol.TerminalSnapshot {
	return &protocol.TerminalSnapshot{Session: "a1", Lines: lines, Cols: 80, Rows: len(lines)}
}

// TestRunPeek_PrintsRenderedLines: the whole screen, one line per line, in order, exit 0.
func TestRunPeek_PrintsRenderedLines(t *testing.T) {
	c := newFakeSteerClient()
	c.snap = peekSnap("first", "second", "third")
	var stdout, stderr bytes.Buffer

	if exit := runPeek([]string{"local/a1"}, c, &stdout, &stderr); exit != 0 {
		t.Fatalf("runPeek exit = %d, want 0; stderr=%q", exit, stderr.String())
	}
	if got, want := stdout.String(), "first\nsecond\nthird\n"; got != want {
		t.Errorf("peek output = %q, want %q", got, want)
	}
	if got := c.peekCalls(); len(got) != 1 || got[0] != "local/a1" {
		t.Errorf("TerminalSnapshot calls = %v, want exactly one for %q", got, "local/a1")
	}
}

// TestRunPeek_LinesPrintsTheTail: --lines N is the tail of the screen — the part a steering
// agent reads before deciding what to send. N past the screen height prints what there is.
func TestRunPeek_LinesPrintsTheTail(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"tail of two", []string{"local/a1", "--lines", "2"}, "second\nthird\n"},
		{"tail of one", []string{"local/a1", "--lines", "1"}, "third\n"},
		{"more than the screen", []string{"local/a1", "--lines", "99"}, "first\nsecond\nthird\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newFakeSteerClient()
			c.snap = peekSnap("first", "second", "third")
			var stdout, stderr bytes.Buffer

			if exit := runPeek(tc.args, c, &stdout, &stderr); exit != 0 {
				t.Fatalf("runPeek(%v) exit = %d, want 0; stderr=%q", tc.args, exit, stderr.String())
			}
			if got := stdout.String(); got != tc.want {
				t.Errorf("peek output = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRunPeek_EmptyScreenIsNotAFailure: a session that has printed nothing yet is a fact to
// report, not an error to script around.
func TestRunPeek_EmptyScreenIsNotAFailure(t *testing.T) {
	c := newFakeSteerClient()
	c.snap = peekSnap()
	var stdout, stderr bytes.Buffer

	if exit := runPeek([]string{"local/a1"}, c, &stdout, &stderr); exit != 0 {
		t.Fatalf("runPeek on an empty screen exit = %d, want 0; stderr=%q", exit, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("empty screen printed %q; want nothing", stdout.String())
	}
}

// TestRunPeek_Misuse: exit 2, and the daemon is never called.
func TestRunPeek_Misuse(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"no arguments", nil},
		{"no session", []string{"--lines", "5"}},
		{"zero lines", []string{"local/a1", "--lines", "0"}},
		{"negative lines", []string{"local/a1", "--lines", "-3"}},
		{"non-numeric lines", []string{"local/a1", "--lines", "many"}},
		{"unknown flag", []string{"local/a1", "--tail", "5"}},
		{"extra positional", []string{"local/a1", "local/b2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newFakeSteerClient()
			c.snap = peekSnap("first")
			var stdout, stderr bytes.Buffer

			if exit := runPeek(tc.args, c, &stdout, &stderr); exit != 2 {
				t.Fatalf("runPeek(%v) exit = %d, want 2 (argument misuse)", tc.args, exit)
			}
			if n := len(c.peekCalls()); n != 0 {
				t.Fatalf("misuse still issued %d TerminalSnapshot calls; want 0", n)
			}
			if stderr.Len() == 0 {
				t.Error("misuse printed nothing on stderr")
			}
		})
	}
}

// TestRunPeek_DaemonError: exit 1 with the cause named, nothing on stdout.
func TestRunPeek_DaemonError(t *testing.T) {
	c := newFakeSteerClient()
	c.peekErr = errFakeDaemon
	var stdout, stderr bytes.Buffer

	if exit := runPeek([]string{"local/a1"}, c, &stdout, &stderr); exit != 1 {
		t.Fatalf("runPeek with a failing daemon exit = %d, want 1", exit)
	}
	if !strings.Contains(stderr.String(), errFakeDaemon.Error()) {
		t.Errorf("stderr = %q, want it to name the daemon failure %q", stderr.String(), errFakeDaemon)
	}
	if stdout.Len() != 0 {
		t.Errorf("a failed peek printed %q on stdout; want nothing", stdout.String())
	}
}

// ---------------------------------------------------------------------------
// Dispatch
// ---------------------------------------------------------------------------

// TestUsage_ListsSteeringVerbs: the verbs are discoverable. `swarm` with an unknown
// subcommand prints the usage block, which must document both.
func TestUsage_ListsSteeringVerbs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exit := dispatch([]string{"bogus"}, &stdout, &stderr); exit != 2 {
		t.Fatalf("dispatch(bogus) exit = %d, want 2", exit)
	}
	out := stderr.String()
	for _, want := range []string{"swarm send", "swarm peek", "--text", "--key", "--lines"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage does not document %q; got:\n%s", want, out)
		}
	}
}
