package skeleton

// FAILING-FIRST (TDD RED, GG-5) for the LAUNCH GATE (bead swarm-1mq, ADR-025): the daemon
// answers claude's folder-trust dialog itself, through the shared session tap, with the keys
// the session's adapter reads off the live grid.
//
// WHY THE DAEMON AND NOT THE OWNER. The launch already names the directory: the owner typed
// it into the launch form, or an agent chose it for a spawn under the daemon's own policy.
// claude 2.1.258 preselects "No, exit" on that dialog, so a reflexive Enter exits the CLI
// with status 1 and the session is dead before its first prompt; an agent-driven spawn has
// nobody at the glass at all and stalls with its initial prompt undelivered. Answering is
// what makes "launch in this directory" mean what it says.
//
// THE GATE IS THE SAME ONE M1.2 USES. The keys are typed only after a readWrite tap
// subscription's OWN seeded grid still shows the dialog, so a dialog the owner answered a
// beat earlier is never typed at; and at most once per session, so a CLI that ignores the
// keys gets a needs_input row (the engine's own reading of that screen) instead of a
// keystroke every poll.
//
// THE FIXTURE IS THE REAL SCREEN, painted into a live PTY by the fake agent exactly as
// approval_inject_test.go paints M1.1's grids, and the fake reports what reached its stdin.

import (
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/claude"
	"github.com/Nathandela/swarm/internal/protocol"
)

// trustGateGrid is the recorded claude 2.1.258 folder-trust dialog, "No, exit" preselected.
var trustGateGrid = recordedGrid{
	fixture: "trust-dialog-2.1.258", lastRow: "Enter to confirm",
	dir: "../adapter/claude/testdata/trustdialog",
}

// launchGateRig paints the trust gate into a fake session and attaches as the owner so the
// fake's report of its stdin can be read back. resolver decides which adapter the daemon
// consults; nil keeps the assembly's own (which knows no adapter for the fake agent).
func launchGateRig(t *testing.T, resolver func(string) (adapter.Adapter, bool)) (*Daemon, string, *protocol.Attachment) {
	t.Helper()
	sk := assemble(t)
	if resolver != nil {
		sk.setAdapterForTest(resolver)
	}
	script, cols, rows := gridScript(t, trustGateGrid)
	m := launchFakeSized(t, sk, script, cols, rows)
	awaitGrid(t, sk, m.ID, trustGateGrid.lastRow)
	att, err := dialClient(t, sk).Attach(protocol.NamespacedID(sk.api.endpointID, m.ID))
	if err != nil {
		t.Fatalf("owner attach: %v", err)
	}
	t.Cleanup(func() { _ = att.Detach() })
	return sk, m.ID, att
}

// stdinReport flushes the fake's line discipline and returns the line it reports having
// read, exactly as injectRig.readBack does.
func stdinReport(t *testing.T, att *protocol.Attachment) string {
	t.Helper()
	if err := att.Input([]byte("\n")); err != nil {
		t.Fatalf("flush the session's line discipline: %v", err)
	}
	ok, drained := awaitFrames(att, "got:", 20*time.Second)
	if !ok {
		t.Fatalf("the fake CLI never reported what it read from its stdin; drained %q", drained)
	}
	line := drained[strings.Index(drained, "got:"):]
	if j := strings.IndexAny(line, "\r\n"); j >= 0 {
		line = line[:j]
	}
	return strings.TrimPrefix(line, "got:")
}

func claudeResolver(string) (adapter.Adapter, bool) { return claude.New(), true }

func TestLaunchGate_OneTapPollAnswersTheFolderTrustDialogWithDownThenEnter(t *testing.T) {
	sk, id, att := launchGateRig(t, claudeResolver)

	sk.sampleGrid(id)

	got := stdinReport(t, att)
	if !strings.Contains(got, "\x1b[B") {
		t.Fatalf("the session's stdin held %q after one tap poll on the trust gate; want the Down arrow that moves the marker to \"Yes, I trust this folder\" (the Enter that follows is the line terminator)", got)
	}
	if !sk.launchGateAnswered(id) {
		t.Fatal("the daemon typed the answer but did not record the session as answered; the next poll would type it again")
	}
}

func TestLaunchGate_AnAnsweredSessionIsNeverTypedAtAgain(t *testing.T) {
	sk, id, att := launchGateRig(t, claudeResolver)
	sk.markLaunchGateAnswered(id)

	sk.sampleGrid(id)

	if got := stdinReport(t, att); strings.TrimSpace(got) != "" {
		t.Fatalf("the session's stdin held %q on a second sighting of the gate; want nothing -- a CLI that ignored the answer gets a needs_input row, not a keystroke every poll", got)
	}
}

func TestLaunchGate_AnAdapterWithoutAGateAnswerTypesNothing(t *testing.T) {
	sk, id, att := launchGateRig(t, nil)

	sk.sampleGrid(id)

	if got := stdinReport(t, att); strings.TrimSpace(got) != "" {
		t.Fatalf("the session's stdin held %q although its agent has no recorded gate answer; want nothing", got)
	}
	if sk.launchGateAnswered(id) {
		t.Fatal("a session nothing was typed at was recorded as answered")
	}
}
