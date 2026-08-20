package skeleton

// WAVE R7, ROUND 2 -- the fences for the five BLOCKING review findings, written failing-first
// against round 1's shipped code. Bead: agents-tracker-hggx.8. ADR-013 §R7.2e/§R7.5/§R7.7.
//
// EVERY TEST HERE DRIVES THE REAL COMPOSED SEQUENCE, because every finding it fences is an
// instance of the same recurring defect class: a helper was exercised and the composition was
// not. Round 1's mid-turn steer test asserted `expectedTurnId != ""` -- which the WRONG id
// satisfies -- and its approval-retirement test drove only the TERMINAL-answered ordering, so
// the phone-answered one, the ordering M4.3 exists for, was never run. Both are corrected by
// driving the RECORDED frames through the REAL pump, the REAL codex shaper and the REAL
// coreAPI, and asserting on the VALUES that reach the socket rather than on their presence.
//
// THE RECORDED FRAMES BELOW ARE VERBATIM from docs/verification/r1-codex-fixtures/. Their turn
// id `01a0033b-d0be-77e1-88e7-584ddeea562d` is a UUIDv7 the app-server minted; a daemon turn id
// is a 26-character ULID (interaction.go's newTurnID). The two can never collide, which is what
// makes these assertions decisive rather than incidental.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/protocol"
)

// The CLI's own ids, as recorded.
const (
	r7NativeTurnID   = "01a0033b-d0be-77e1-88e7-584ddeea562d"
	r7NativeThreadID = "01a00339-a80e-72a0-966f-116427b6b9ce"
)

// r7RecordedUserMessageFrame is frame-samples.json's `item/started` for a userMessage, verbatim.
// It is the frame that OPENS a turn, and it carries the turn id every later turn operation must
// name.
const r7RecordedUserMessageFrame = `{"method":"item/started","params":{"item":{"type":"userMessage","id":"01a0033b-d17f-7070-9744-a3fb14dee165","clientId":null,"content":[{"type":"text","text":"Count from 1 to 40. Put each number on its own line and write one full sentence of trivia about each number. Take your time.","text_elements":[]}]},"threadId":"01a00339-a80e-72a0-966f-116427b6b9ce","turnId":"01a0033b-d0be-77e1-88e7-584ddeea562d","startedAtMs":1786760647039}}`

// r7RecordedThreadStartedFrame is frame-samples.json's `thread/started`, verbatim. The R1 gate
// received it on the OBSERVER's connection for a thread the TUI created -- which is the whole
// mechanism §R7.2e's no-flag topology rests on.
const r7RecordedThreadStartedFrame = `{"method":"thread/started","params":{"thread":{"id":"01a00339-a80e-72a0-966f-116427b6b9ce","sessionId":"01a00339-a80e-72a0-966f-116427b6b9ce","preview":"","ephemeral":false,"historyMode":"paginated","modelProvider":"openai","createdAt":1786760505,"status":{"type":"idle"},"cliVersion":"0.147.0","source":"vscode","canAcceptDirectInput":true,"threadSource":"user","turns":[]}}}`

// r7RecordedResolvedFrame is frame-samples.json's `serverRequest/resolved`, verbatim. The gate
// recorded the server emitting it AFTER THE OBSERVER ITSELF replied {"decision":"accept"} --
// so the answering client is told too, which is what makes retirement-by-observation available
// on the phone-answered ordering and not only the terminal-answered one.
const r7RecordedResolvedFrame = `{"method":"serverRequest/resolved","params":{"threadId":"01a00335-9a50-79e2-8253-e08861d67c4d","requestId":0}}`

// r7OpenNativeTurn opens a turn THE WAY THE REAL STREAM DOES: the recorded frame goes into the
// REAL producer-edge pump, which hands it to the REAL codex shaper, which the REAL capture seam
// journals. It returns the daemon's minted turn id for that turn.
//
// Nothing here hand-builds an adapter.Interaction, and that is the point: TurnRef has to
// survive the whole chain -- frame -> shaper -> shapeItem -> turnIDLocked -> nativeTurns -- or
// the steer below has nothing to name.
func r7OpenNativeTurn(t *testing.T, r *r7ComposerRig) string {
	t.Helper()
	already := len(interactionItems(t, r.sk, r.local))
	r.sk.ingestBackendFrame(r.local, []byte(r7RecordedUserMessageFrame), time.Now().UnixMilli())
	r.sk.flushBackendFrames(r.local)
	items := awaitItems(t, r.sk, r.local, already+1)
	for i := len(items) - 1; i >= 0; i-- {
		if items[i]["kind"] != adapter.KindUserMessage {
			continue
		}
		turn, _ := items[i]["turn_id"].(string)
		if turn == "" {
			t.Fatalf("the recorded userMessage frame opened no turn: %v", items[i])
		}
		return turn
	}
	t.Fatal("the recorded userMessage frame never reached the journal")
	return ""
}

// ---------------------------------------------------------------------------
// BLOCKING 1 -- turn/steer and turn/interrupt must name the CLI'S OWN turn id
// ---------------------------------------------------------------------------

// TestR7Fix_MidTurnSteerNamesTheCLIsOwnTurnIdAndNeverTheDaemonsMintedULID is M4.4's headline
// verb through its real composition.
//
// PROBED AGAINST ROUND 1: the same sequence sent
// expectedTurnId="01M0EZR7QB3ANMVBNVNNF8CJC8" -- a daemon ULID -- against the recorded frame's
// own native "01a0033b-d0be-77e1-88e7-584ddeea562d". The generated binding says expectedTurnId
// is a "Required active turn id precondition. The request fails when it does not match the
// currently active turn", so EVERY mid-turn phone send was rejected by a real app-server.
//
// The assertion is EQUALITY WITH THE RECORDED VALUE, not non-emptiness: round 1's fence
// asserted `!= ""`, which the wrong id satisfies, and that is what hid the defect.
func TestR7Fix_MidTurnSteerNamesTheCLIsOwnTurnIdAndNeverTheDaemonsMintedULID(t *testing.T) {
	r := newR7ComposerRig(t, true)
	daemonTurn := r7OpenNativeTurn(t, r)

	code, err := r.send(t, daemonTurn, "actually, stop", "devA:01JSTEERFIX00000000000000")
	if err != nil || code != "" {
		t.Fatalf("mid-turn send on a session whose turn the CLI named was refused: code %q err %v", code, err)
	}

	params := r7CallParams(t, r.backend, "turn/steer")
	got, _ := params["expectedTurnId"].(string)
	if got != r7NativeTurnID {
		t.Errorf("turn/steer expectedTurnId = %q, want the CLI's OWN turn id %q. The app-server "+
			"checks this against ITS turn table; an id this daemon minted matches nothing there, "+
			"so every mid-turn phone send is rejected", got, r7NativeTurnID)
	}
	if got == daemonTurn {
		t.Errorf("turn/steer carried the DAEMON's minted turn id %q; that is the defect, not the fix", got)
	}
	if params["threadId"] != r7NativeThreadID {
		t.Errorf("turn/steer threadId = %v, want the session's thread %q", params["threadId"], r7NativeThreadID)
	}
	r.assertPTYUntouched(t)
}

// TestR7Fix_InterruptNamesTheCLIsOwnTurnIdAndNeverTheDaemonsMintedULID is the same defect on the
// verb where it is WORSE.
//
// turn/interrupt against an id the server never minted answers
// {"code":-32600,"message":"no active turn to interrupt"} (RECORDED: errors-observed.json), and
// benignInterruptError -- correctly, for its own case -- reports that to the phone as SUCCESS.
// So round 1's Stop stopped nothing and said it worked.
func TestR7Fix_InterruptNamesTheCLIsOwnTurnIdAndNeverTheDaemonsMintedULID(t *testing.T) {
	r := newR7ComposerRig(t, true)
	daemonTurn := r7OpenNativeTurn(t, r)

	code, err := r.sk.api.InterruptTurn(r.sk.api.endpointID, "devA:01JSTOPFIX000000000000000",
		protocol.TurnInterruptReq{Session: r.session, ExpectedTurn: daemonTurn})
	if err != nil || code != "" {
		t.Fatalf("interrupt on a session whose turn the CLI named was refused: code %q err %v", code, err)
	}

	params := r7CallParams(t, r.backend, "turn/interrupt")
	got, _ := params["turnId"].(string)
	if got != r7NativeTurnID {
		t.Errorf("turn/interrupt turnId = %q, want the CLI's OWN turn id %q. A Stop that names a "+
			"turn the server never minted is answered `no active turn to interrupt`, which this "+
			"daemon reports to the phone as success", got, r7NativeTurnID)
	}
	if got == daemonTurn {
		t.Errorf("turn/interrupt carried the DAEMON's minted turn id %q", got)
	}
	r.assertPTYUntouched(t)
}

// TestR7Fix_ASteerWithNoCLITurnIdIsREFUSEDRatherThanSentWithAMintedOne is the honest-degrade
// arm. A turn opened by an interaction that sourced NO native turn id cannot be steered: there
// is no id to send. ADR-017's posture is that the absence is surfaced, never bridged -- and the
// bridge here would be sending the daemon's own id, which is exactly what round 1 did.
func TestR7Fix_ASteerWithNoCLITurnIdIsREFUSEDRatherThanSentWithAMintedOne(t *testing.T) {
	r := newR7ComposerRig(t, true)
	// A user_message with NO TurnRef: an adapter that sources no turn identity at all.
	r.adapter.items = []adapter.Interaction{{
		Kind: adapter.KindUserMessage, Text: "count to forty", Source: adapter.SourceOwner, Ref: "um-1",
	}}
	turn := r6OpenTurn(t, r.sk, r.local, "count to forty", len(interactionItems(t, r.sk, r.local)))

	code, err := r.send(t, turn, "actually, stop", "devA:01JSTEERNONE0000000000000")
	if err == nil && code == "" {
		t.Fatal("a mid-turn send on a turn the CLI never named reported SUCCESS; whatever it sent, " +
			"the server cannot have applied it")
	}
	for _, m := range methodsOf(r.backend) {
		if m == "turn/steer" || m == "turn/start" {
			t.Errorf("a REFUSED send still dispatched %s; the whole point of refusing is that "+
				"nothing reaches the agent", m)
		}
	}
	r.assertPTYUntouched(t)
}

// TestR7Fix_AnInterruptWithNoCLITurnIdIsREFUSEDRatherThanReportedAsSuccess is the interrupt
// sibling, and it is the one that closes the false-success hole: with no native id the refusal
// must reach the phone rather than a swallowed `no active turn to interrupt`.
func TestR7Fix_AnInterruptWithNoCLITurnIdIsREFUSEDRatherThanReportedAsSuccess(t *testing.T) {
	r := newR7ComposerRig(t, true)
	r.adapter.items = []adapter.Interaction{{
		Kind: adapter.KindUserMessage, Text: "count to forty", Source: adapter.SourceOwner, Ref: "um-1",
	}}
	turn := r6OpenTurn(t, r.sk, r.local, "count to forty", len(interactionItems(t, r.sk, r.local)))

	code, err := r.sk.api.InterruptTurn(r.sk.api.endpointID, "devA:01JSTOPNONE00000000000000",
		protocol.TurnInterruptReq{Session: r.session, ExpectedTurn: turn})
	if code == "" && err == nil {
		t.Fatal("an interrupt on a turn the CLI never named reported SUCCESS. A Stop that stopped " +
			"nothing and said it worked is the worst outcome available here: the owner believes " +
			"the agent is stopped and it is still running")
	}
	for _, m := range methodsOf(r.backend) {
		if m == "turn/interrupt" {
			t.Error("a REFUSED interrupt still dispatched turn/interrupt")
		}
	}
	r.assertPTYUntouched(t)
}

// ---------------------------------------------------------------------------
// BLOCKING 2 -- a PHONE-answered approval must clear its own card
// ---------------------------------------------------------------------------

// TestR7Fix_ThePHONEAnsweredApprovalIsRetiredByTheServersOwnResolvedBroadcast drives M4.3's
// headline round-trip IN ITS REAL ORDER: phone approve -> RPC reply -> the server's
// serverRequest/resolved for that same request id -> the card is gone.
//
// PROBED AGAINST ROUND 1: `d.approvals[local]` was STILL NON-NIL five seconds after the
// recorded broadcast was ingested. applyNativeDecision's takeServerRequest consumed BOTH the
// answerability entry and the id->ref mapping, so retireResolvedRequest looked the broadcast up
// and found nothing. The owner's card stayed live until the IS-LIFE-2 expiry sweep.
//
// Round 1's fence drove only the TERMINAL-answered ordering (resolved arrives with no prior
// phone answer), which passes either way -- so the ordering M4.3 exists for was unfenced.
func TestR7Fix_ThePHONEAnsweredApprovalIsRetiredByTheServersOwnResolvedBroadcast(t *testing.T) {
	r := newR7BackendRig(t)

	code, err := r7Approve(t, r, "accept")
	if code != "" || err != nil {
		t.Fatalf("the phone's approve was refused: code %q err %v", code, err)
	}
	if _, _, ok := r.backend.lastResponse(); !ok {
		t.Fatal("no JSON-RPC reply was written for the phone's approve")
	}

	// The server's own broadcast, verbatim, for the request id the rig registered.
	r.sk.ingestBackendFrame(r.local, []byte(r7RecordedResolvedFrame), time.Now().UnixMilli())
	r.sk.flushBackendFrames(r.local)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r.sk.itemMu.Lock()
		pending := r.sk.approvals[r.local]
		r.sk.itemMu.Unlock()
		if pending == nil {
			r.assertNothingWasTyped(t)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the card the PHONE answered was still pending after the server broadcast " +
		"serverRequest/resolved for it. The owner's phone shows a live approve button for a " +
		"request that is over, until the expiry sweep marks it expired minutes later")
}

// TestR7Fix_ThePHONEAnsweredResolutionIsAttributedToThePhone is the second half of the same
// round-trip: retiring the card is necessary, and attributing it correctly is what makes the
// history true. The daemon SENT the answer, so the resolution carries what it sent and `phone`.
func TestR7Fix_ThePHONEAnsweredResolutionIsAttributedToThePhone(t *testing.T) {
	r := newR7BackendRig(t)
	if code, err := r7Approve(t, r, "accept"); code != "" || err != nil {
		t.Fatalf("the phone's approve was refused: code %q err %v", code, err)
	}
	r.sk.ingestBackendFrame(r.local, []byte(r7RecordedResolvedFrame), time.Now().UnixMilli())
	r.sk.flushBackendFrames(r.local)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, it := range interactionItems(t, r.sk, r.local) {
			if it["kind"] != adapter.KindApprovalResolved {
				continue
			}
			if by, _ := it["by"].(string); by != byPhone {
				t.Errorf("the resolution of a PHONE-answered approval says by=%q, want %q", by, byPhone)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("no approval_resolved item was ever journalled for the phone's own answer")
}

// ---------------------------------------------------------------------------
// BLOCKING 3 -- ONE thread, the AGENT's, not a second one the daemon created
// ---------------------------------------------------------------------------

// TestR7Fix_ThePumpAdoptsTheAgentsOwnThreadFromTheRecordedThreadStartedFrame is the mechanism
// that replaces `thread/start`: the app-server announces the thread the AGENT created to every
// attached client, and the daemon takes it from there.
func TestR7Fix_ThePumpAdoptsTheAgentsOwnThreadFromTheRecordedThreadStartedFrame(t *testing.T) {
	r := newR7ComposerRig(t, false)

	if _, ok := r.sk.adoptedThread(r.local); ok {
		t.Fatal("a session adopted a thread before any thread/started arrived")
	}
	r.sk.ingestBackendFrame(r.local, []byte(r7RecordedThreadStartedFrame), time.Now().UnixMilli())
	r.sk.flushBackendFrames(r.local)

	got, ok := r.sk.adoptedThread(r.local)
	if !ok || got != r7NativeThreadID {
		t.Fatalf("adopted thread = %q (ok=%v), want the agent's own %q", got, ok, r7NativeThreadID)
	}

	// FIRST WINS: §R7.10 pins one app-server per session, so a second announcement is either a
	// fork or a bug, and repointing a live session's composer at it is the harm.
	second := strings.ReplaceAll(r7RecordedThreadStartedFrame, r7NativeThreadID, "01a00999-dead-beef-0000-000000000000")
	r.sk.ingestBackendFrame(r.local, []byte(second), time.Now().UnixMilli())
	r.sk.flushBackendFrames(r.local)
	if got, _ := r.sk.adoptedThread(r.local); got != r7NativeThreadID {
		t.Errorf("a SECOND thread/started repointed the session to %q", got)
	}
}

// TestR7Fix_NoProductionPathEverCallsThreadStart is the structural half of BLOCKING 3, and it
// is scoped to the file that holds the app-server conversation rather than to the repo.
//
// Round 1's dialSessionBackend called `thread/start {}` and kept that thread id, while the
// agent booted with `--remote unix://SOCK` and created its OWN. Every turn/start, turn/steer,
// turn/interrupt and every approval the daemon answered named a thread the owner was not
// looking at: two conversations on one socket, and the wave's exit criterion -- the terminal
// and the phone driving the SAME Codex thread -- structurally unreachable.
func TestR7Fix_NoProductionPathEverCallsThreadStart(t *testing.T) {
	src, err := os.ReadFile("backendconnect.go")
	if err != nil {
		t.Fatalf("read backendconnect.go: %v", err)
	}
	body := string(src)
	if strings.Contains(body, `"thread/start"`) {
		t.Error("backendconnect.go calls `thread/start`. The daemon must not create a thread: the " +
			"agent creates the session's one thread and the daemon adopts it from thread/started, " +
			"or the two surfaces are on two conversations")
	}
	// ...and the join it DOES make is the recorded rejoin, inside resumeThreadOnce.
	i := strings.Index(body, "func (d *Daemon) resumeThreadOnce(")
	if i < 0 {
		t.Fatal("resumeThreadOnce is gone; the thread join has no named home")
	}
	j := strings.Index(body[i:], "\n}\n")
	if j < 0 || !strings.Contains(body[i:i+j], `"thread/resume"`) {
		t.Error("resumeThreadOnce does not call thread/resume. Joining a thread this connection did " +
			"not create is what the R1 gate recorded as the way a second client receives the item " +
			"stream and may drive turns on it")
	}
}

// TestR7Fix_TheThreadJoinRetriesOnlyTheRecordedRolloutRace pins the retry PREDICATE inside the
// loop that must apply it.
//
// ROUND 3's OTHER HALF OF THIS TEST IS DELETED, NOT WEAKENED, and the deletion is the point.
// It asserted that joinSessionBackend emits `gapBackendJoinedLate` when the resume needed
// retries, on the reasoning that "a resume that needed retries succeeded only BECAUSE a turn was
// already under way". That inference runs backwards: `no rollout found` is returned *because* no
// turn has begun, so a join that had to retry missed NOTHING. Round 4's review ruled the gap
// factually false -- and it was the false statement that removed the composer from every healthy
// session. Its honest replacement (a rollout that ALREADY existed on the FIRST attempt) is fenced
// behaviourally, not by grep, in TestR7R4_AThreadThatHadALREADYRunTurnsBeforeTheJoinIsAnHonestGap.
//
// SCOPED TO THE FUNCTION THAT MUST MAKE THE CALL (hard rule 4, review round 3 MEDIUM 3). The
// round-2 form grepped the WHOLE FILE for "no rollout found", which isMissingRollout's own
// declaration satisfies. The BEHAVIOURAL fences are
// TestR7R3_ATransportFaultOnTheThreadJoinFAILSFASTRatherThanRetryingTheWholeWindow,
// TestR7R3_TheRecordedRolloutRaceIsRETRIEDWithoutClaimingAnythingWasMissed and
// TestR7R4_AUserWhoThinksLongerThanTheJoinDeadlineIsNEVERPermanentlyDegraded.
func TestR7Fix_TheThreadJoinRetriesOnlyTheRecordedRolloutRace(t *testing.T) {
	src, err := os.ReadFile("backendconnect.go")
	if err != nil {
		t.Fatalf("read backendconnect.go: %v", err)
	}
	body := string(src)
	k := strings.Index(body, "func (d *Daemon) subscribeSessionThread(")
	if k < 0 {
		t.Fatal("subscribeSessionThread is gone; the thread subscription has no retry loop")
	}
	l := strings.Index(body[k:], "\n}\n")
	sub := body[k : k+l]
	if !strings.Contains(sub, "isMissingRollout(err)") {
		t.Error("the subscription loop retries on something other than the RECORDED pre-first-turn " +
			"failure. Retrying a transport fault for the life of the session is a session that " +
			"hangs instead of degrading")
	}
	if !strings.Contains(sub, "d.noteBackendLost(") {
		t.Error("the subscription loop leaves a session holding a sink whose stream can NEVER " +
			"arrive. The composer keeps working and the transcript never moves: the silent bridge")
	}
}

// ---------------------------------------------------------------------------
// BLOCKING 4 -- a daemon restart must REJOIN or GAP, never silently tear
// ---------------------------------------------------------------------------

// TestR7Fix_ARestartWhoseBackendIsGoneGAPSAndDEGRADESRatherThanTearingSilently is the failing
// half of §R7.7's three cases, and the one round 1 left with neither arm firing.
//
// After any `swarm daemon restart`, round 1's registerSession returned at its nil-core guard,
// nothing called noteBackendUnavailable or noteBackendLost, and markSessionDegraded never
// fired -- while the phone derives its composer from `structuredChat = !transcript.structureTorn`.
// The transcript stopped mid-conversation with NO boundary record, the phone still rendered a
// composer, and every send was refused AFTER the tap. That is the silent bridge ADR-017 forbids.
func TestR7Fix_ARestartWhoseBackendIsGoneGAPSAndDEGRADESRatherThanTearingSilently(t *testing.T) {
	r := newR7ComposerRig(t, false)

	// The shim outlived the daemon; its app-server did not. The socket path names nothing.
	r.sk.backendReady = 300 * time.Millisecond
	r.sk.rejoinSessionBackend(r.local, daemon.BackendChannel{SocketPath: "/tmp/r7-no-such-backend.sock"})

	if !r.sk.sessionDegraded(r.local) {
		t.Error("a session whose backend could not be rejoined was NOT degraded. The phone reads " +
			"the composer's availability off the transcript, so without this it keeps offering one " +
			"and every send is refused after the tap")
	}
	if !r7HasGap(t, r.sk, r.local) {
		t.Error("no structured_gap was emitted for a session whose structured plane ended at the " +
			"restart. A transcript that simply stops is the silent bridge ADR-017 forbids")
	}
}

// TestR7Fix_ServeCatchesUpTheBackendsOfSESSIONSItAdoptedAtReconcile pins the WIRING, because
// the behaviour above is unreachable without a caller. registerSession cannot be that caller:
// it runs inside daemon.Open, before Serve has assigned d.core. startHookDrainsForRunning is
// the established post-Open catch-up for the identical problem on the hook channel, and this
// is its twin four lines below it.
func TestR7Fix_ServeCatchesUpTheBackendsOfSESSIONSItAdoptedAtReconcile(t *testing.T) {
	src, err := os.ReadFile("serve.go")
	if err != nil {
		t.Fatalf("read serve.go: %v", err)
	}
	// SCOPED TO Serve's OWN BODY (review round 4, MEDIUM 4). The round-3 form searched from the
	// startHookDrainsForRunning anchor to END OF FILE, so ANY later caller anywhere below it --
	// including one in a function Serve never reaches -- satisfied it. The call has to be in the
	// function that runs once at assembly, or the catch-up never happens.
	body := string(src)
	s := strings.Index(body, "func Serve(cfg Config) (*Daemon, error) {")
	if s < 0 {
		t.Fatal("Serve is gone; the daemon has no assembly")
	}
	e := strings.Index(body[s:], "\n}\n")
	if e < 0 {
		t.Fatal("Serve has no end; the scoping this test depends on cannot be applied")
	}
	serveBody := body[s : s+e]
	if !strings.Contains(serveBody, "d.startHookDrainsForRunning()") {
		t.Fatal("the established post-Open catch-up is gone from Serve")
	}
	if !strings.Contains(serveBody, "d.connectBackendsForRunning()") {
		t.Error("Serve never catches up the session backends it adopted at reconcile. Every " +
			"`swarm daemon restart` then tears a live Codex session's structured plane with no " +
			"rejoin, no gap and no degrade")
	}
}

// r7HasGap reports whether a structured_gap boundary reached the JOURNAL -- which is where
// the phone reads it from (`structuredChat = !transcript.structureTorn`), so the journal is
// the only place its presence means anything.
func r7HasGap(t *testing.T, sk *Daemon, local string) bool {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if countJournalStructuredGaps(t, sk, local) > 0 {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// ---------------------------------------------------------------------------
// The composer echo, unchanged by the turn-id fix
// ---------------------------------------------------------------------------

// TestR7Fix_TheNativeTurnIdIsMachineSideAndNeverReachesTheWire. TurnRef is the third
// machine-side correlation key beside Ref and ClientRef: IS-APR-1 leaves exactly ONE id on the
// wire, and the phone names the daemon's `turn_id` in expected_turn. A CLI's UUIDv7 leaking
// into a journalled item would give the phone two turn vocabularies and no way to tell them
// apart.
func TestR7Fix_TheNativeTurnIdIsMachineSideAndNeverReachesTheWire(t *testing.T) {
	r := newR7ComposerRig(t, true)
	r7OpenNativeTurn(t, r)

	for _, it := range interactionItems(t, r.sk, r.local) {
		body, err := json.Marshal(it)
		if err != nil {
			t.Fatalf("re-encode item: %v", err)
		}
		if strings.Contains(string(body), r7NativeTurnID) {
			t.Errorf("the CLI's own turn id %q reached the wire in %s. Only the daemon's minted "+
				"turn_id belongs there", r7NativeTurnID, body)
		}
	}
}

// ---------------------------------------------------------------------------
// MEDIUM 8 #7 -- SINGLE-WRITER, fenced where it is actually ENFORCED
// ---------------------------------------------------------------------------

// TestR7Fix_ASessionWithABackendIsRegisteredWithNOHookToken closes the second vacuous fence
// round 1 disclosed.
//
// The engine-side test passed a Callback whose Token was EMPTY, so the refusal it asserted came
// from the engine's own empty-token check and held no matter what token the SESSION was
// registered with -- minting one changed nothing it could see. The property §R7.3 actually asks
// for lives HERE, in registerSession: a session with a backend must be registered with NO hook
// token, so HandleCallback is structurally unusable for it and one high-water namespace can
// never have two typed producers (whose failure mode is a SILENT DROP, not a warning).
//
// This drives the REAL registerSession against a session the REAL core reports as having a
// backend, and then presents the engine with the token it was handed. The mutation is deleting
// the suppression in registerSession, and it fires here because the callback carries the token.
func TestR7Fix_ASessionWithABackendIsRegisteredWithNOHookToken(t *testing.T) {
	sk := assemble(t)
	m := launchFake(t, sk, r7StdinScript)

	// The session's persisted launch config now declares a backend, which is exactly what
	// daemon.SessionBackend reads (an ABSENT backend_socket_path is a session without one).
	r7DeclareBackend(t, sk, m.ID, "/tmp/r7-single-writer.sock")

	const token = "tok-single-writer"
	sk.registerSession(m, token)

	err := sk.eng.HandleCallback(engine.Callback{
		SessionID: m.ID, Token: token, Event: "turn/started", Sequence: 1,
	})
	if err == nil {
		t.Fatal("HandleCallback ACCEPTED a callback bearing the session's OWN token for a session " +
			"that has a BACKEND. Its typed status is driven by the in-process pump " +
			"(Engine.ApplyTypedEvent), and a second producer on one per-dimension high-water " +
			"namespace does not warn -- it silently DROPS whichever event loses (§R7.3)")
	}

	// ...and the pump's own seam still works for that session, or the suppression has simply
	// turned typed status off rather than moved its writer.
	if aerr := sk.eng.ApplyTypedEvent(m.ID, "turn/started", nil); aerr != nil {
		t.Errorf("ApplyTypedEvent on the same session: %v; suppressing the hook token must move "+
			"the writer, not remove it", aerr)
	}
}

// TestR7Fix_ASessionWITHOUTABackendKeepsItsHookToken is the other half, and it is what stops
// the fence above from being satisfiable by a daemon that simply stopped minting tokens.
// Claude's whole structured plane rides the hook channel.
func TestR7Fix_ASessionWITHOUTABackendKeepsItsHookToken(t *testing.T) {
	sk := assemble(t)
	m := launchFake(t, sk, r7StdinScript)

	const token = "tok-hook-session"
	sk.registerSession(m, token)

	if err := sk.eng.HandleCallback(engine.Callback{
		SessionID: m.ID, Token: token, Event: "Stop", Sequence: 1,
	}); err != nil {
		t.Fatalf("HandleCallback REFUSED a correctly-tokened callback for a session with NO "+
			"backend: %v. Every Claude session is this case, and its status would stop moving", err)
	}
}

// r7DeclareBackend writes the session's persisted launch config with a backend socket path, so
// the REAL daemon.SessionBackend reports one. It is the same file the core writes at spawn.
func r7DeclareBackend(t *testing.T, sk *Daemon, id, socketPath string) {
	t.Helper()
	path := filepath.Join(sk.stateDir, id, "shim-launch.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var lc map[string]any
	if err := json.Unmarshal(data, &lc); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	lc["backend_socket_path"] = socketPath
	out, err := json.Marshal(lc)
	if err != nil {
		t.Fatalf("encode %s: %v", path, err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if _, ok := sk.core.SessionBackend(id); !ok {
		t.Fatalf("the core still reports no backend for %s after declaring one in %s", id, path)
	}
}
