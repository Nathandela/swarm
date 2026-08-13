package skeleton

// FAILING-FIRST (TDD RED, GG-5) for Mirror M1.2 -- APPLY BY INJECTION
// (docs/specifications/mirror-program.md section 3, bead agents-tracker-dwwv.2.2).
//
// WHAT WAS THERE BEFORE. approveInteraction validated the ADR-007 D7 binding tuple, emitted
// approval_resolved, and APPLIED NOTHING -- its own header said so: "What is still the
// PRODUCER's and not this function's is APPLYING the decision". So a phone tap dismissed the
// card on every surface while the CLI stayed blocked on a dialog nobody had answered. The card
// lied.
//
// WHAT M1.2 SHIPS INSTEAD, and why in this shape. mirror-program.md section 3 REJECTED the
// held-hook alternative on CO-PRESENCE grounds: while a PermissionRequest hook is undecided the
// CLI has not drawn its own dialog, so holding it hides the terminal's prompt -- which violates
// the program's central ruling that both rooms stay live. The dialog therefore appears in the
// terminal exactly as today, and the phone's Allow/Deny is applied by TYPING THE DIALOG'S OWN
// KEYS into the PTY the daemon already owns:
//
//	validate the tuple (unchanged)  -> the offered decision's verdict has a key at all
//	  -> the LIVE grid still shows a dialog the session's adapter has a recorded key map for
//	  -> write that dialog's keys into the PTY through the shared session tap
//	  -> reply accepted. NOTHING is resolved here.
//
// THE GATE IS THE WHOLE POINT. If the owner answered at the terminal a beat earlier the dialog
// is gone, and a "1" typed into what is now the composer is USER INPUT the agent will act on.
// So a grid that does not positively show the dialog is a REFUSAL, never a keystroke.
//
// RESOLUTION COMES ONLY FROM OBSERVATION. The tap emits no approval_resolved: the record lands
// when the daemon SEES the dialog leave (interaction-schema.md IS-LIFE-2's existing paths), and
// carries `by: phone` because the daemon knows it typed the answer itself. A dialog that does
// not move is surfaced by a watchdog note rather than by a resolution nobody observed.
//
// THE FIXTURES ARE THE REAL SCREEN. Each fake session REPAINTS one recorded claude 2.1.231 grid
// (M1.1, internal/adapter/claude/testdata/permdialog) byte for byte -- the snapshot is rendered
// back to ANSI and written into the PTY -- so the recognizer under test reads exactly the cells
// the daemon's tap read off the real CLI. The fake then blocks on stdin like the real CLI does,
// and REPORTS WHAT IT READ, which is what turns "no stray keystroke" into an observation
// instead of an absence.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/claude"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/vt"
)

// permDialogFixtures is M1.1's recorded-grid directory, read from here for the same reason
// interaction_chain_e2e_test.go reads the claude interaction corpus from here: the fixture is
// the CLI's, and copying it would let the copy drift from the screen it claims to be.
const permDialogFixtures = "../adapter/claude/testdata/permdialog"

// recordedGrid names one fixture and a string from its LAST painted row, used to drain an
// attachment past the repaint so everything seen afterwards is new output.
type recordedGrid struct{ fixture, lastRow string }

var (
	bashDialogGrid = recordedGrid{"bash-approval-2.1.231", "ctrl+e to explain"}
	editDialogGrid = recordedGrid{"edit-approval-2.1.231", "Tab to amend"}
	composerGrid   = recordedGrid{"neg-composer-idle-2.1.231", "manual mode on"}
)

// gridScript builds a fake-agent script that reproduces one recorded grid in a real PTY:
//
//	print <ansi>   the snapshot rendered back to ANSI -- absolute CUP per row, no newlines --
//	               so the emulator on the other side of the tap holds the RECORDED cells
//	ask <cup>      park the cursor on the bottom row (the escape paints nothing) and BLOCK on
//	               stdin, exactly as the real CLI blocks on its dialog. Whatever line it
//	               eventually reads is echoed back as `got: <line>`, which is how a test sees
//	               what did -- or did not -- reach the session's stdin.
func gridScript(t *testing.T, g recordedGrid) (script string, cols, rows int) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(permDialogFixtures, g.fixture+".snap.json"))
	if err != nil {
		t.Fatalf("read recorded grid %s: %v", g.fixture, err)
	}
	snap, err := vt.DecodeSnapshot(raw)
	if err != nil {
		t.Fatalf("decode recorded grid %s: %v", g.fixture, err)
	}
	ansi := vt.RenderSnapshotClipped(snap, 0, 0)
	if strings.ContainsAny(string(ansi), "\r\n") {
		t.Fatalf("the rendered grid carries a newline, which would split the one-line script directive")
	}
	return "print " + string(ansi) + "\nask \x1b[" + strconv.Itoa(snap.Rows) + ";1H\nidle 600s\n", snap.Cols, snap.Rows
}

// launchFakeSized is launchFake with the terminal size the recorded grid was captured at. A
// 100x30 grid replayed into an 80x24 PTY wraps, and a wrapped box rule is not a box rule.
func launchFakeSized(t *testing.T, sk *Daemon, script string, cols, rows int) persist.Meta {
	t.Helper()
	spath := filepath.Join(t.TempDir(), "script.txt")
	if err := os.WriteFile(spath, []byte(script), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	m, err := sk.Core().Launch(daemon.LaunchSpec{
		AgentType: "fake",
		Argv:      []string{fakeAgentBin, spath},
		Cwd:       t.TempDir(),
		ClientEnv: []string{"PATH=" + os.Getenv("PATH")},
		Cols:      cols,
		Rows:      rows,
	})
	if err != nil {
		t.Fatalf("core Launch: %v", err)
	}
	t.Cleanup(func() {
		if m.ShimPID > 0 {
			_ = syscall.Kill(m.ShimPID, syscall.SIGTERM)
		}
	})
	return m
}

// claudeApproval is the approval_request the claude adapter really shapes for a Bash
// permission (internal/adapter/claude/interaction.go approvalFrom): the CLI's OWN two decision
// ids, each carrying the polarity the adapter classified AT CAPTURE. The polarity is what
// selects the keys, so an approval built without it is a different test (see the verdictless
// arm below).
func claudeApproval(ref string) adapter.Interaction {
	return adapter.Interaction{
		Kind: adapter.KindApprovalRequest, Status: adapter.StatusInProgress, Ref: ref,
		Summary: "Bash touch /tmp/swarm-m1-one.marker", Mode: adapter.ModeCard,
		Action: adapter.ToolAction{Type: "execute", Command: "touch /tmp/swarm-m1-one.marker"},
		Decisions: []adapter.DecisionChoice{
			{ID: "allow", Label: "Yes", Verdict: adapter.VerdictAllow},
			{ID: "deny", Label: "No", Verdict: adapter.VerdictDeny},
		},
	}
}

// openApprovalFrom captures one pending approval_request from a given shaped interaction and
// returns the item as journalled (openApprovalOn's shape, with the interaction handed in).
func openApprovalFrom(t *testing.T, sk *Daemon, session string, in adapter.Interaction) map[string]any {
	t.Helper()
	sk.captureInteractions(session, newCaptureAdapter(in), adapter.HookPayload{Event: "PermissionRequest"})
	for _, item := range awaitItems(t, sk, session, 1) {
		if item["kind"] == adapter.KindApprovalRequest && itemString(t, item, "status") == adapter.StatusInProgress {
			return item
		}
	}
	t.Fatalf("no pending approval_request reached the journal for %s", session)
	return nil
}

// injectRig is one session showing one recorded grid, with one pending card and an owner
// attachment already drained past the repaint.
type injectRig struct {
	sk      *Daemon
	local   string
	session string
	att     *protocol.Attachment
	item    map[string]any
}

// awaitGrid polls the session's own snapshot -- the very bytes the daemon's tap reads -- until
// the recorded grid has landed. It reads the SNAPSHOT and not an attachment's frame stream on
// purpose: a paint that finished before the attach lives in the attach's seed, so waiting on
// frames would be waiting on a race.
func awaitGrid(t *testing.T, sk *Daemon, local, want string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	last := ""
	for time.Now().Before(deadline) {
		if raw, err := sk.api.SampleSnapshot(local); err == nil {
			if snap, derr := vt.DecodeSnapshot(raw); derr == nil {
				last = gridText(snap)
				if strings.Contains(last, want) {
					return
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the session never painted the recorded grid (looking for %q); its screen is:\n%s", want, last)
}

// gridText renders one snapshot's visible rows as text.
func gridText(snap *vt.Snap) string {
	var b strings.Builder
	for _, line := range snap.Lines {
		for _, r := range line.Runs {
			b.WriteString(r.Text)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// newInjectRig assembles the daemon with the REAL claude adapter as its resolver -- the
// recognizer under test is the shipped one, not a double -- launches a session repainting the
// named recorded grid, waits for that paint, attaches as the owner, and opens one pending
// approval. The attach comes AFTER the paint so that everything on its frame stream from here
// on is output the injection provoked.
func newInjectRig(t *testing.T, g recordedGrid, in adapter.Interaction) *injectRig {
	t.Helper()
	sk := assemble(t)
	sk.adapterFor = func(string) (adapter.Adapter, bool) { return claude.New(), true }

	script, cols, rows := gridScript(t, g)
	m := launchFakeSized(t, sk, script, cols, rows)
	session := protocol.NamespacedID(sk.api.endpointID, m.ID)
	awaitGrid(t, sk, m.ID, g.lastRow)

	oc := dialClient(t, sk)
	att, err := oc.Attach(session)
	if err != nil {
		t.Fatalf("owner attach: %v", err)
	}
	t.Cleanup(func() { _ = att.Detach() })
	return &injectRig{sk: sk, local: m.ID, session: session, att: att,
		item: openApprovalFrom(t, sk, m.ID, in)}
}

// readBack flushes the session's line discipline with a bare newline and returns what the fake
// CLI reports it read. The keys M1.1 recorded carry NO Enter -- each selects and submits on its
// own -- so a fake blocked on a LINE only reports once the test supplies the terminator itself.
// What comes back is therefore exactly the bytes that were already sitting in the session's
// stdin: `got: 1` when the daemon typed the allow key, `got:` when it typed nothing.
func (r *injectRig) readBack(t *testing.T) string {
	t.Helper()
	if err := r.att.Input([]byte("\n")); err != nil {
		t.Fatalf("flush the session's line discipline: %v", err)
	}
	ok, drained := awaitFrames(r.att, "got:", 20*time.Second)
	if !ok {
		t.Fatalf("the fake CLI never reported what it read from its stdin; drained %q", drained)
	}
	i := strings.Index(drained, "got:")
	line := drained[i:]
	if j := strings.IndexAny(line, "\r\n"); j >= 0 {
		line = line[:j]
	}
	return strings.TrimSpace(strings.TrimPrefix(line, "got:"))
}

// assertNothingWasTyped is the stray-keystroke fence, and it is an OBSERVATION: the fake reports
// an EMPTY line, so the refusal really did leave the session's stdin untouched.
func (r *injectRig) assertNothingWasTyped(t *testing.T) {
	t.Helper()
	if got := r.readBack(t); got != "" {
		t.Errorf("the session's stdin held %q after a REFUSED approve; want nothing. A refusal that "+
			"still typed is the exact hazard the grid gate exists for -- a key pressed into a "+
			"dismissed dialog lands in the composer and the agent acts on it", got)
	}
}

// assertNoResolutionYet fails if any approval_resolved was journalled. The tap does not resolve;
// only observation does (mirror-program.md section 3, step 3).
func (r *injectRig) assertNoResolutionYet(t *testing.T) {
	t.Helper()
	time.Sleep(500 * time.Millisecond) // several append windows: a wrong resolution would have landed
	for _, it := range interactionItems(t, r.sk, r.local) {
		if it["kind"] == adapter.KindApprovalResolved {
			t.Fatalf("an approval_resolved was journalled by the TAP: %v. Resolution comes ONLY from "+
				"observing the dialog leave -- a resolution emitted on the tap is a card claiming an "+
				"outcome nobody watched, which is what M1.2 exists to stop", it)
		}
	}
}

// ---- the loop: a phone tap types the dialog's own keys ---------------------

// TestApproveInjection_AnAllowTypesTheRecordedDialogsAllowKeyIntoThePTY is the wave's headline.
// Everything in the chain is real: the recorded claude grid, the shipped recognizer, the shared
// session tap, and a live PTY that reports what landed on its stdin.
func TestApproveInjection_AnAllowTypesTheRecordedDialogsAllowKeyIntoThePTY(t *testing.T) {
	r := newInjectRig(t, bashDialogGrid, claudeApproval("req-allow"))

	code, err := r.sk.approveInteraction(r.sk.api.endpointID, "op-allow",
		approveFor(t, r.sk, r.local, r.item, "allow"))
	if err != nil {
		t.Fatalf("a correctly-bound allow onto a session whose grid SHOWS the recorded dialog was "+
			"refused %q: %v", code, err)
	}
	if code != "" {
		t.Errorf("an accepted approve carries error_code %q; a code is a REFUSAL reason (R-PROT.7)", code)
	}
	if got := r.readBack(t); got != "1" {
		t.Errorf("the session's stdin received %q; want %q -- the allow key M1.1 recorded off "+
			"claude 2.1.231, which selects option 1 AND submits it in one keystroke", got, "1")
	}
	r.assertNoResolutionYet(t)
}

// TestApproveInjection_ADenyTypesTheRecordedDialogsDenyKeyIntoThePTY is the same path with the
// other polarity, and it is not symmetry for its own sake: the deny key is a DIFFERENT recorded
// byte, and a mapping that collapsed the two would refuse a tool the owner meant to allow (or,
// far worse, run one the owner refused).
func TestApproveInjection_ADenyTypesTheRecordedDialogsDenyKeyIntoThePTY(t *testing.T) {
	r := newInjectRig(t, editDialogGrid, claudeApproval("req-deny"))

	if code, err := r.sk.approveInteraction(r.sk.api.endpointID, "op-deny",
		approveFor(t, r.sk, r.local, r.item, "deny")); err != nil {
		t.Fatalf("a correctly-bound deny was refused %q: %v", code, err)
	}
	if got := r.readBack(t); got != "3" {
		t.Errorf("the session's stdin received %q; want %q -- the deny key M1.1 recorded, which is "+
			"ABSOLUTE (a live run answered 3 while option 1 was highlighted and the tool was refused)",
			got, "3")
	}
	r.assertNoResolutionYet(t)
}

// ---- the races, both directions --------------------------------------------

// TestApproveInjection_AGridThatNoLongerShowsTheDialogIsRefusedAndTypesNothing is the
// TERMINAL-FIRST race: the owner answered at the machine a beat before the phone's tap arrived,
// so the screen is back to the composer. The tuple is still perfectly valid -- nothing has
// resolved it yet -- and it is the GRID alone that must stop the keystroke.
func TestApproveInjection_AGridThatNoLongerShowsTheDialogIsRefusedAndTypesNothing(t *testing.T) {
	r := newInjectRig(t, composerGrid, claudeApproval("req-gone"))

	code, err := r.sk.approveInteraction(r.sk.api.endpointID, "op-late",
		approveFor(t, r.sk, r.local, r.item, "allow"))
	if err == nil {
		t.Fatal("an approve was applied to a session whose grid shows the IDLE COMPOSER, not a " +
			"dialog. mirror-program.md section 3: the injection is gated on the live grid still " +
			"showing the permission dialog, precisely so a terminal answer a beat earlier cannot " +
			"turn the phone's tap into a keystroke in the composer")
	}
	if code != protocol.CodeStaleApproval {
		t.Errorf("error_code = %q; want %q -- the card the phone holds is no longer the machine's "+
			"state, which is exactly what stale_approval says to a retry policy (D10)", code, protocol.CodeStaleApproval)
	}
	r.assertNothingWasTyped(t)
	r.assertNoResolutionYet(t)
}

// TestApproveInjection_AnAlreadyResolvedApprovalIsRefusedAndTypesNothing is the same race read
// off the OTHER gate: the owner's answer has already been observed, so the request is resolved
// even though this session's grid still shows a dialog. The tuple check must fire FIRST -- a
// resolved request answered again would type a key into whatever dialog happens to be up now.
func TestApproveInjection_AnAlreadyResolvedApprovalIsRefusedAndTypesNothing(t *testing.T) {
	r := newInjectRig(t, bashDialogGrid, claudeApproval("req-answered"))

	// The machine's own observation: the session was waiting on the permission, and now is not.
	r.sk.emitStatus(r.local, status.Status{
		Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionPermission})
	r.sk.emitStatus(r.local, status.Status{
		Process: status.ProcessRunning, Turn: status.TurnActive, Interaction: status.InteractionNone})
	awaitResolution(t, r.sk, r.local, itemString(t, r.item, "item_id"))

	code, err := r.sk.approveInteraction(r.sk.api.endpointID, "op-twice",
		approveFor(t, r.sk, r.local, r.item, "allow"))
	if err == nil {
		t.Fatal("an approve for an ALREADY RESOLVED request was applied. IS-LIFE-2 spends exactly " +
			"one resolution per request, and typing a key for a second one presses whatever button " +
			"is on screen now")
	}
	if code != protocol.CodeStaleApproval {
		t.Errorf("error_code = %q; want %q", code, protocol.CodeStaleApproval)
	}
	r.assertNothingWasTyped(t)
}

// TestApproveInjection_ASecondTapBeforeTheFirstIsObservedTypesNothingMore closes the window
// that opened the day the tap stopped resolving. A re-delivered approve used to be refused
// because the first one had already resolved the request; now the resolution waits on an
// observation, so for that whole interval the request is still pending -- and a second
// injection would press a second key, which lands in the composer the instant the dialog goes.
func TestApproveInjection_ASecondTapBeforeTheFirstIsObservedTypesNothingMore(t *testing.T) {
	r := newInjectRig(t, bashDialogGrid, claudeApproval("req-twice"))
	req := approveFor(t, r.sk, r.local, r.item, "allow")

	if _, err := r.sk.approveInteraction(r.sk.api.endpointID, "op-first", req); err != nil {
		t.Fatalf("the first approve was refused: %v", err)
	}
	code, err := r.sk.approveInteraction(r.sk.api.endpointID, "op-second", req)
	if err == nil {
		t.Fatal("a re-delivered approve was applied a second time. IS-LIFE-2 spends one resolution " +
			"per request, and one keystroke is what answers one dialog")
	}
	if code != protocol.CodeStaleApproval {
		t.Errorf("error_code = %q; want %q -- a replayed approve is the case D10's stale_approval "+
			"names, and no OperationClaimer dedup stands in front of this op", code, protocol.CodeStaleApproval)
	}
	if got := r.readBack(t); got != "1" {
		t.Errorf("the session's stdin received %q; want exactly one %q. Two keys for one dialog is "+
			"one key too many, and the extra one is typed wherever focus lands next", got, "1")
	}
}

// TestApproveInjection_ADecisionWithNoVerdictCannotBeTypedAndIsRefused. The keys are selected by
// the decision's grant/refuse POLARITY, which is the one thing about a decision the adapter
// normalizes (§3.5 keeps the ids the CLI's own). A decision that carries no polarity has no key
// on the dialog, and picking one would be exactly the guess the verdict exists to remove.
func TestApproveInjection_ADecisionWithNoVerdictCannotBeTypedAndIsRefused(t *testing.T) {
	verdictless := claudeApproval("req-verdictless")
	verdictless.Decisions = []adapter.DecisionChoice{{ID: "allow", Label: "Yes"}, {ID: "deny", Label: "No"}}
	r := newInjectRig(t, bashDialogGrid, verdictless)

	code, err := r.sk.approveInteraction(r.sk.api.endpointID, "op-unmapped",
		approveFor(t, r.sk, r.local, r.item, "allow"))
	if err == nil {
		t.Fatal("a decision carrying no verdict was applied. Nothing says which key answers it, and " +
			"a daemon that picked one would be inventing the owner's intent")
	}
	if code != protocol.CodeInvalidField {
		t.Errorf("error_code = %q; want %q -- the decision as offered cannot be applied, which is a "+
			"permanent property of the request and not a stale card", code, protocol.CodeInvalidField)
	}
	r.assertNothingWasTyped(t)
}

// ---- resolution comes from observation, and carries the phone ---------------

// TestApproveInjection_TheResolutionLandsOnObservationAttributedToThePhone. The tap resolves
// nothing; the record lands when the daemon SEES the session leave the waiting state. It is
// attributed to the PHONE because the daemon typed the answer itself and knows which key it
// pressed -- the alternative, `answered_locally` by the owner, is a claim about a person who
// was not there.
func TestApproveInjection_TheResolutionLandsOnObservationAttributedToThePhone(t *testing.T) {
	r := newInjectRig(t, bashDialogGrid, claudeApproval("req-observed"))
	itemID := itemString(t, r.item, "item_id")

	if _, err := r.sk.approveInteraction(r.sk.api.endpointID, "op-observed",
		approveFor(t, r.sk, r.local, r.item, "deny")); err != nil {
		t.Fatalf("a correctly-bound deny was refused: %v", err)
	}
	r.assertNoResolutionYet(t)

	// The dialog leaves: the machine's own observation, the only thing that resolves anything.
	r.sk.emitStatus(r.local, status.Status{
		Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionPermission})
	r.sk.emitStatus(r.local, status.Status{
		Process: status.ProcessRunning, Turn: status.TurnActive, Interaction: status.InteractionNone})

	res := awaitResolution(t, r.sk, r.local, itemID)
	if res["decision"] != "denied" {
		t.Errorf("decision = %v; want \"denied\" -- the daemon typed the DENY key, so the outcome it "+
			"observed is a refusal (§3.6)", res["decision"])
	}
	if res["by"] != "phone" {
		t.Errorf("by = %v; want \"phone\". The daemon applied the phone's answer itself; attributing "+
			"the resolution to the owner would put a decision in the mouth of somebody who never "+
			"touched the keyboard (§3.6)", res["by"])
	}
	if res["operation_id"] != "op-observed" {
		t.Errorf("operation_id = %v; want \"op-observed\" -- the phone's own idempotency key, which is "+
			"how a screen tells its own tap from somebody else's answer (§3.6)", res["operation_id"])
	}
}

// TestApproveInjection_ATerminalSideDenyResolvesAnsweredLocallyByOwner is M1.3's owner-side leg
// of this same loop (bead agents-tracker-dwwv.2.3): the SAME recorded dialog, but this time the
// OWNER types the deny key directly into the session through their own attach -- no approve ever
// arrives, so `ap.applied` stays empty and noteInteractionStatus's owner branch fires instead of
// the phone one.
//
// WHY THE DECISION IS answered_locally AND NOT denied, recorded here because a reader expecting
// the deny key's own name would otherwise call this a bug. interaction-schema.md §3.6 (IS-RES-1)
// reserves `allowed`/`denied` for a decision classified from the VERDICT the daemon itself
// supplied -- true only on the phone path, where `ap.applied` records what the daemon typed
// BEFORE it typed it. The owner path has no such source: the daemon observes only that the
// session's interaction dimension LEFT the waiting state, never which button on the dialog was
// pressed to cause it. §3.6 says this in so many words: "The four remaining values are
// daemon-observed and carry no verdict."
//
// A hook that would supply that ground truth exists on paper: claude 2.1.231 ships a
// PermissionDenied event, and M1.3 characterized it against the real, installed CLI --
// interactive "No" (twice), a `permissions.deny` rule under `--permission-mode manual`, and the
// same rule under `--permission-mode auto`. It never once fired for an interactive dialog in any
// of the four runs. `strings` on the installed 2.1.231 binary confirms why: the event's only real
// call site gates on `decisionReason.type=="classifier" && decisionReason.classifier=="auto-mode"`
// -- a fully different code path from the TUI "No" a human presses -- and the binary's own
// embedded schema names the event "Emitted when a tool call is auto-denied WITHOUT an
// interactive permission prompt (e.g. auto-mode classifier, dontAsk mode, headless-agent
// auto-deny, or a deny rule)". The full record, including the raw hook dumps from all four runs,
// is docs/verification/mirror-m1.md's M1.3 section. There is therefore no ground truth to attach
// on this path, and inventing one would be exactly the guess IS-TOOL-2 forbids.
func TestApproveInjection_ATerminalSideDenyResolvesAnsweredLocallyByOwner(t *testing.T) {
	r := newInjectRig(t, bashDialogGrid, claudeApproval("req-terminal-deny"))
	itemID := itemString(t, r.item, "item_id")

	// The OWNER, not the phone: bytes typed straight into the session's PTY through the same
	// attach a real terminal app would hold -- M1.1's recorded deny digit for this grid.
	if err := r.att.Input([]byte("3")); err != nil {
		t.Fatalf("owner input: %v", err)
	}

	// No approveInteraction call anywhere in this test -- the phone never touched this request,
	// so nothing should resolve until the machine's own status observation says the dialog left.
	r.assertNoResolutionYet(t)

	// The machine's own observation: the session was waiting on the permission, and now is not
	// -- exactly what a real claude session reports once its screen redraws past the dialog
	// (M1.1's negative fixture, composerGrid, is that redraw).
	r.sk.emitStatus(r.local, status.Status{
		Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionPermission})
	r.sk.emitStatus(r.local, status.Status{
		Process: status.ProcessRunning, Turn: status.TurnActive, Interaction: status.InteractionNone})

	res := awaitResolution(t, r.sk, r.local, itemID)
	if res["decision"] != "answered_locally" {
		t.Errorf("decision = %v; want \"answered_locally\" (§3.6/IS-RES-1) -- the daemon never typed "+
			"this request's answer itself, so it has no verdict to classify allowed/denied from, only "+
			"the fact that the machine answered", res["decision"])
	}
	if res["by"] != "owner" {
		t.Errorf("by = %v; want \"owner\" -- nobody typed this from a phone, and attributing it to "+
			"\"phone\" would credit a tap that never arrived", res["by"])
	}
	if _, has := res["operation_id"]; has {
		t.Errorf("operation_id = %v present on an owner resolution; §3.6 echoes it only when a phone "+
			"ActionApprove drove the resolution", res["operation_id"])
	}
}

// TestApproveInjection_AWatchdogNotesADialogThatDidNotMove. The honest failure mode of applying
// by keystroke is that the keystroke lands and NOTHING happens -- a CLI version whose key map
// moved, or a dialog that swallowed the byte. Silence there is the worst outcome: the phone's
// card stays up with no explanation. So the daemon looks again a moment later and, finding the
// same dialog still on screen and the request still unresolved, puts the session's status on the
// transcript. It does NOT resolve the card: nothing was observed to resolve.
func TestApproveInjection_AWatchdogNotesADialogThatDidNotMove(t *testing.T) {
	restore := injectWatchdogDelay
	injectWatchdogDelay = 250 * time.Millisecond
	t.Cleanup(func() { injectWatchdogDelay = restore })

	r := newInjectRig(t, bashDialogGrid, claudeApproval("req-stuck"))
	if _, err := r.sk.approveInteraction(r.sk.api.endpointID, "op-stuck",
		approveFor(t, r.sk, r.local, r.item, "allow")); err != nil {
		t.Fatalf("a correctly-bound allow was refused: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, it := range interactionItems(t, r.sk, r.local) {
			if it["kind"] == adapter.KindSessionStatus {
				r.assertNoResolutionYet(t)
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the injected keys changed nothing and the daemon said nothing. A dialog still on "+
		"screen %s after the daemon typed at it is the one outcome the phone cannot see for "+
		"itself, and a card left silent is indistinguishable from a card being worked on. Items: %v",
		injectWatchdogDelay, interactionItems(t, r.sk, r.local))
}
