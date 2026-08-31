//go:build realcli

package skeleton

// THE REAL-PTY HALF of Slice 0, which the audit committee named first among the tests owed
// before the slice is called complete: docs/specifications/chat-surface-plan.md §12, "A real
// Claude PTY test parks an owner draft between the phone's check and its write: the send
// refuses and the draft survives byte-exact." Bead: agents-tracker-bzfe. Evidence:
// docs/verification/chat-surface.md, "Owed before this is called complete".
//
// TWO GATES, BOTH REQUIRED, and this file never runs at `go test ./...` or in CI:
//
//	the `//go:build realcli` tag, and the SWARM_REALCLI=1 environment opt-in
//	SWARM_REALCLI=1 go test -tags realcli -run TestSlice0RealCLI -v ./internal/skeleton
//
// That is internal/smoke's convention verbatim (realcli_test.go), and it is the convention
// because THIS FILE IS BILLABLE: the second subtest submits one prompt to a real Claude.
//
// WHY THE FAKE AGENT CANNOT ANSWER THIS. s0_writerserialise_test.go proves the mechanism
// against internal/fakeagent, which reads lines and echoes them. A real Claude is a full-screen
// TUI: it emits terminal QUERIES at startup (device attributes, cursor position, capability
// strings) and the shim's emulator ANSWERS them, writing bytes back into the very PTY whose
// input the quiescence guard tracks. If those replies were treated as somebody typing,
// the logical line would never be clean again and EVERY phone message to EVERY real Claude
// session would be refused input_busy forever -- a total failure of the feature that no fake can
// reveal, because no fake asks the questions. That is what the first subtest exists for, and
// it is why the two arms live in one file: the refusal is only trustworthy if the acceptance
// is real.
//
// WHAT IS OBSERVED, AND HOW. The draft's survival is read off the session's RENDERED GRID
// (vt.SnapText over a fresh attach's snapshot), not off the raw frame stream: a TUI redraws
// its composer with styling between the characters, so the raw bytes are the wrong place to
// ask "is the line still exactly what the owner typed". The grid is the CLI's own screen as
// the emulator resolved it, which is precisely the thing a merged write would corrupt.

import (
	"encoding/json"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/claude"
	"github.com/Nathandela/swarm/internal/adapter/detect"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/vt"
)

// realCLIOptIn enforces the runtime gate. Without it every subtest SKIPS, so a tagged build
// still cannot spend money by accident (internal/smoke/realcli_test.go's requireOptIn).
func realCLIOptIn(t *testing.T) {
	t.Helper()
	if os.Getenv("SWARM_REALCLI") != "1" {
		t.Skip("the real-CLI PTY test is BILLABLE and opt-in: set SWARM_REALCLI=1 to run it deliberately")
	}
}

// realClaude locates the host's actual claude binary through the PRODUCTION prober, so a
// missing or out-of-range CLI is reported as what it is rather than stood in for.
func realClaude(t *testing.T) adapter.Detection {
	t.Helper()
	det := adapter.Detect(claude.New(), detect.Host{})
	switch {
	case !det.Found:
		t.Skipf("no real claude binary is reachable on this host (%s is not on PATH). "+
			"This test refuses to substitute a fake: the whole reason it exists is that a real "+
			"CLI answers terminal queries and a fake does not", claude.New().Binary())
	case det.Version == "":
		t.Skipf("claude was found at %s but did not report a version (%s); the harness will not "+
			"guess what it is driving", det.Path, det.ProbeErr)
	case !det.InRange:
		t.Skipf("claude %s at %s is outside the adapter's supported range; this test drives the "+
			"CLI this adapter claims to support and nothing else", det.Version, det.Path)
	}
	return det
}

// realRig is one live claude session behind the real shim, with the owner attached.
type realRig struct {
	sk      *Daemon
	local   string
	session string
	att     *protocol.Attachment
	cols    int
	rows    int
}

// launchRealClaude launches the real CLI through the ordinary daemon path -- Core().Launch,
// the real shim, the real PTY -- so the quiescence guard under test is the shipped one and not
// a re-implementation. It waits for the TUI to settle before returning.
func launchRealClaude(t *testing.T, det adapter.Detection) *realRig {
	t.Helper()
	sk := assemble(t)

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("cwd: %v", err)
	}
	// The package directory, inside this repository, on purpose: a fresh temp directory is
	// exactly what makes claude open its trust prompt, and a modal that swallows keystrokes
	// would make every assertion below meaningless for a reason unrelated to the subject.
	if v := os.Getenv("SWARM_REALCLI_CWD"); v != "" {
		cwd = v
	}

	env := os.Environ()
	if os.Getenv("TERM") == "" {
		env = append(env, "TERM=xterm-256color")
	}
	const cols, rows = 100, 30
	m, err := sk.Core().Launch(daemon.LaunchSpec{
		AgentType: "claude",
		Argv:      []string{det.Path},
		Cwd:       cwd,
		ClientEnv: env,
		Cols:      cols,
		Rows:      rows,
	})
	if err != nil {
		t.Fatalf("launch the real claude: %v", err)
	}
	t.Cleanup(func() {
		if m.ShimPID > 0 {
			_ = syscall.Kill(m.ShimPID, syscall.SIGTERM)
		}
	})

	session := protocol.NamespacedID(sk.api.endpointID, m.ID)
	oc := dialClient(t, sk)
	att, err := oc.Attach(session)
	if err != nil {
		t.Fatalf("owner attach: %v", err)
	}
	t.Cleanup(func() { _ = att.Detach() })

	// The capability record a claude session carries. Pinned rather than left to the lazy
	// derive so requireStructuredComposer cannot answer differently between the two sends
	// below -- which would put a refusal in front of this file's subject that has nothing to
	// do with it.
	sk.registerSessionCapabilities(m.ID, protocol.SessionCapabilities{
		Provider: "claude", ProviderVersion: det.Version, AdapterRevision: "realcli",
		StructuredChat: true, TerminalFallback: false,
	})

	r := &realRig{sk: sk, local: m.ID, session: session, att: att, cols: cols, rows: rows}
	r.awaitQuiet(t, 4*time.Second, 5*time.Second, 120*time.Second)
	return r
}

// awaitQuiet drains the session's frames until the CLI has stopped painting for `quiet`,
// having drained for at least `floor` and no longer than `bound`. It is how readiness is
// decided WITHOUT guessing at a string in Claude's interface: the version that changes its
// composer hint would silently turn a string match into a timeout, and a timeout that looks
// like a product failure is worse than no test.
func (r *realRig) awaitQuiet(t *testing.T, quiet, floor, bound time.Duration) string {
	t.Helper()
	var buf []byte
	deadline := time.After(bound)
	minimum := time.After(floor)
	floored := false
	idle := time.NewTimer(quiet)
	defer idle.Stop()
	for {
		select {
		case f, ok := <-r.att.Frames():
			if !ok {
				t.Fatalf("the session's frames closed while waiting for the CLI to settle; "+
					"claude exited before it was driven. Drained %q", string(buf))
			}
			buf = append(buf, f...)
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(quiet)
		case <-minimum:
			floored = true
		case <-idle.C:
			if floored {
				return string(buf)
			}
			idle.Reset(quiet)
		case <-deadline:
			if len(buf) == 0 {
				t.Fatalf("the real claude painted nothing within %s; it is not running, or it is "+
					"waiting on something this harness cannot see", bound)
			}
			return string(buf)
		}
	}
}

// screen re-attaches and renders the session's CURRENT grid as plain text. A fresh attach is
// how a live snapshot is obtained -- Attachment.Snapshot is the one taken when that attachment
// opened -- and copresence pins that a second owner attach coexists with the first.
func (r *realRig) screen(t *testing.T) []string {
	t.Helper()
	oc := dialClient(t, r.sk)
	att, err := oc.Attach(r.session)
	if err != nil {
		t.Fatalf("re-attach for a live snapshot: %v", err)
	}
	defer func() { _ = att.Detach() }()
	var snap vt.Snap
	if err := json.Unmarshal(att.Snapshot(), &snap); err != nil {
		t.Fatalf("decode the session snapshot: %v", err)
	}
	return vt.SnapText(&snap)
}

// send drives composerSend on the coreAPI seam, exactly as remote_chat.go does.
func (r *realRig) send(text, opID string) (protocol.ErrorCode, error) {
	return r.sk.api.ComposerSend(r.sk.api.endpointID, opID, protocol.ComposerSendReq{
		Session: r.session, ExpectedTurn: "", Text: text,
	})
}

// TestSlice0RealCLI_ARealClaudePTYIsNotFalselyBusy is the acceptance arm, and it is the one
// only a real CLI can answer.
//
// BILLABLE: it submits one prompt. The prompt is deliberately trivial and overridable
// (SWARM_REALCLI_CLAUDE_PROMPT), because what is being proved is that the message CROSSES,
// not what comes back.
//
// A real Claude has, by the time this runs, asked the terminal a series of questions and been
// answered by the shim's emulator through ptyWriter.Write. Those replies must not count: they
// are the shim answering the agent's own queries, not somebody typing. If they counted, the
// input line would be permanently dirty and the refusal arm below would pass for entirely the
// wrong reason.
func TestSlice0RealCLI_ARealClaudePTYIsNotFalselyBusy(t *testing.T) {
	realCLIOptIn(t)
	det := realClaude(t)
	r := launchRealClaude(t, det)

	prompt := os.Getenv("SWARM_REALCLI_CLAUDE_PROMPT")
	if prompt == "" {
		prompt = "reply with the single word ok"
	}
	code, err := r.send(prompt, "devA:01JS0REALCLICLEAN000000")
	if code != "" || err != nil {
		t.Fatalf("a phone send to a FRESHLY LAUNCHED real claude was refused: code %q err %v.\n"+
			"Nobody has typed into this PTY. What HAS been written to it is the emulator's answers "+
			"to the CLI's own startup queries, and counting those as a draft would refuse every "+
			"phone message to every real claude session for the life of the session.", code, err)
	}

	// The words are on the CLI's own screen: the message crossed, whole.
	deadline := time.Now().Add(60 * time.Second)
	for {
		if strings.Contains(strings.Join(r.screen(t), "\n"), prompt) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the send answered OK but %q never appeared on the session's screen.\n"+
				"Screen:\n%s", prompt, strings.Join(r.screen(t), "\n"))
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// TestSlice0RealCLI_ADeletedOwnerDraftDoesNotLatchInputBusy is the real-editor counterpart
// of internal/shim's deterministic key-stream tests. It is BILLABLE only because proving the
// line became eligible requires one harmless phone prompt to cross. The owner types a draft,
// deletes every rune using the real Claude editor, and only then sends from the phone surface.
// The pre-v0.13.12 byte counter refused this forever: deletes added to the dirty count instead
// of removing logical input.
func TestSlice0RealCLI_ADeletedOwnerDraftDoesNotLatchInputBusy(t *testing.T) {
	realCLIOptIn(t)
	det := realClaude(t)
	r := launchRealClaude(t, det)

	const draft = "throw this away"
	if err := r.att.Input([]byte(draft)); err != nil {
		t.Fatalf("owner types a draft: %v", err)
	}
	deadline := time.Now().Add(120 * time.Second)
	for !strings.Contains(strings.Join(r.screen(t), "\n"), draft) {
		if time.Now().After(deadline) {
			t.Skipf("the real Claude editor never displayed the draft %q; precondition not reached. Screen:\n%s",
				draft, strings.Join(r.screen(t), "\n"))
		}
		time.Sleep(250 * time.Millisecond)
	}
	if err := r.att.Input([]byte(strings.Repeat("\x7f", len([]rune(draft))))); err != nil {
		t.Fatalf("owner deletes the draft: %v", err)
	}
	deadline = time.Now().Add(30 * time.Second)
	for strings.Contains(strings.Join(r.screen(t), "\n"), draft) {
		if time.Now().After(deadline) {
			t.Fatalf("the real Claude editor still displays deleted draft %q. Screen:\n%s",
				draft, strings.Join(r.screen(t), "\n"))
		}
		time.Sleep(250 * time.Millisecond)
	}

	prompt := "reply with the single word ok"
	code, err := r.send(prompt, "devA:01JS0REALCLIDELETED0000")
	if code != "" || err != nil {
		t.Fatalf("phone send after the owner deleted the whole real-Claude draft = code %q err %v, want delivered", code, err)
	}
	deadline = time.Now().Add(60 * time.Second)
	for {
		if strings.Contains(strings.Join(r.screen(t), "\n"), prompt) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("send answered OK but %q never appeared. Screen:\n%s", prompt, strings.Join(r.screen(t), "\n"))
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// TestSlice0RealCLI_AnOwnerDraftInARealClaudeSurvivesAPhoneSend is the committee's own
// sentence, against the CLI it was written about.
//
// NOT BILLABLE: nothing is ever submitted. The owner parks a draft, the phone's send is
// refused, and the draft is read back off the screen. The owner never presses return, so no
// prompt reaches the model.
func TestSlice0RealCLI_AnOwnerDraftInARealClaudeSurvivesAPhoneSend(t *testing.T) {
	realCLIOptIn(t)
	det := realClaude(t)
	r := launchRealClaude(t, det)

	// A draft with spaces in it, on purpose: a merge that appended the phone's words would
	// leave a line that still LOOKS like prose, and a single token would make the assertion
	// weaker than the defect.
	const draft = "half a thought"
	const phoneText = "shipitnow"

	if err := r.att.Input([]byte(draft)); err != nil {
		t.Fatalf("owner types a draft: %v", err)
	}
	// The CLI must have DRAWN it before the send, or the race under test is not the one
	// described: the point is a draft the phone's check cannot see, already on the line.
	// GENEROUSLY BOUNDED, because this is the harness settling and not the subject. A real
	// claude launched while a previous one is still shutting down takes appreciably longer to
	// start accepting keystrokes, and a deadline tuned to a quiet machine turns that into a
	// failure that reads like a product defect. The test is manual and billable; waiting is
	// the cheapest thing it does.
	deadline := time.Now().Add(120 * time.Second)
	for !strings.Contains(strings.Join(r.screen(t), "\n"), draft) {
		if time.Now().After(deadline) {
			// SKIPPED, NOT FAILED, and the difference is the whole reason this branch is
			// written out rather than left as a timeout. Everything below asserts what happens
			// to a draft the CLI is HOLDING; if the CLI never took the keystrokes there is no
			// draft, so there is nothing to assert and nothing has been shown about the
			// refusal. Reporting that as a failure would put a red mark on the product for a
			// harness that could not reach its own precondition -- observed when a previous
			// real-CLI subtest's session is still shutting down on the same machine. The
			// screen is printed so a human can see what the CLI was showing instead.
			t.Skipf("the draft %q never appeared on the real claude's screen within 120s, so the "+
				"CLI never took the keystrokes (a trust prompt, a TUI still starting, a machine "+
				"still busy with a previous session). THE SUBJECT OF THIS TEST WAS NOT "+
				"EXERCISED. Screen:\n%s", draft, strings.Join(r.screen(t), "\n"))
		}
		time.Sleep(250 * time.Millisecond)
	}

	code, err := r.send(phoneText, "devA:01JS0REALCLIDRAFT000000")
	if code != protocol.CodeInputBusy {
		t.Fatalf("a phone send against a real claude holding an owner draft = code %q err %v, "+
			"want %q.\nB13, against the CLI it was written about: the phone's text is APPENDED to "+
			"the owner's line and the carriage return submits the concatenation -- a message "+
			"nobody wrote, sent to a real model.", code, err, protocol.CodeInputBusy)
	}

	// THE DRAFT SURVIVES BYTE-EXACT, and the phone's words are nowhere.
	screen := r.screen(t)
	joined := strings.Join(screen, "\n")
	if !strings.Contains(joined, draft) {
		t.Fatalf("the owner's draft %q is no longer on the screen after a REFUSED send.\n"+
			"Screen:\n%s\nA refusal writes nothing; a refusal that disturbed the line would be "+
			"worse than the merge it was preventing.", draft, joined)
	}
	if strings.Contains(joined, phoneText) {
		t.Fatalf("the refused message %q is on the real claude's screen.\nScreen:\n%s\n"+
			"The refusal promises that nothing was typed. Bytes on that line are bytes the model "+
			"will read the moment the owner presses return.", phoneText, joined)
	}
	// Nor any prefix of it, which is what a partial write would leave.
	for n := len(phoneText) - 1; n >= 4; n-- {
		if strings.Contains(joined, phoneText[:n]) {
			t.Fatalf("the refused message's first %d bytes (%q) are on the screen: the write was "+
				"not refused, it was TRUNCATED.\nScreen:\n%s", n, phoneText[:n], joined)
		}
	}
}
