package skeleton

// WAVE R7, ROUND 3 -- the MACHINE-SIDE topology, driven through its REAL composition, plus the
// three round-2 findings whose fences were source-greps or nothing at all. Bead:
// agents-tracker-hggx.8. ADR-013 §R7.2e, §R7.7.
//
// WHY THIS FILE EXISTS AT ALL. Round 2's review probed one line and found the wave's single
// most load-bearing statement unfenced: `backendconnect.go` sends the shim the AGENT ARGUMENTS
// (`--remote unix://SOCK`) that make the codex agent a client of the app-server the shim
// spawned. Changing that argument to nil left the ENTIRE internal/skeleton package green
// (285 s, every test) -- with the TUI and the daemon on two unconnected planes, no
// `thread/started` ever reaching the daemon, and the wave's exit criterion structurally
// unreachable. The shim half was fenced; the daemon's decision to send them was fenced by
// nothing, because NO TEST ANYWHERE ASSEMBLED daemon -> shim -> app-server -> agent.
//
// THE ASSEMBLY IS THE TEST. TestR7R3_TheAgentIsLaunchedAsACLIENTOfTheAppServerTheShimSpawned
// launches a session through the REAL core, which spawns the REAL shim binary, which spawns a
// REAL backend process, which serves a REAL WebSocket JSON-RPC endpoint over a REAL UDS, which
// the REAL appserver client dials, whose go-ahead travels the REAL shimwire control socket,
// after which the REAL agent process is exec'd with the arguments in question -- and only then
// does the fake app-server announce the thread the daemon adopts. Every link is production
// code; the only doubles are the two ends that would otherwise cost the owner money (hard
// rule 10): `cmd/swarm-fake-codex` stands in for `codex` in BOTH its modes, resolved by the
// core's own PATH search because the file is literally named `codex`.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/appserver"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/shim"
)

// ---------------------------------------------------------------------------
// The fake `codex`: one binary, two modes, built once per test binary run.
// ---------------------------------------------------------------------------

var (
	fakeCodexOnce sync.Once
	fakeCodexBin  string
	fakeCodexErr  error
)

// buildFakeCodex compiles cmd/swarm-fake-codex to a file NAMED `codex`, so the core's own
// resolution (adapter.ResolveBackend -> backendProber.LookPath -> the codex adapter's bare
// program name) finds it exactly as it would find the real binary. Nothing in the test tells
// the daemon where the backend is.
func buildFakeCodex(t *testing.T) string {
	t.Helper()
	fakeCodexOnce.Do(func() {
		dir, err := os.MkdirTemp("/tmp", "swr7codex")
		if err != nil {
			fakeCodexErr = err
			return
		}
		out := filepath.Join(dir, "codex")
		cmd := exec.Command("go", "build", "-o", out, "github.com/Nathandela/swarm/cmd/swarm-fake-codex")
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fakeCodexErr = err
			return
		}
		fakeCodexBin = out
	})
	if fakeCodexErr != nil {
		t.Skipf("cannot build the fake codex binary: %v", fakeCodexErr)
	}
	return fakeCodexBin
}

// fakeCodexThreadID is the thread cmd/swarm-fake-codex announces WHEN THE AGENT ATTACHES. The
// daemon must end up holding exactly this id, and it can only learn it from the server's own
// broadcast.
const fakeCodexThreadID = "01997f00-face-7000-8000-00000000cde0"

// r7RecordedTurnCompletedFrame is frame-samples.json's `turn/completed`, verbatim. It names the
// same turn the recorded userMessage frame opens, and its terminal status is what IS-ENV-1
// closes a turn on.
const r7RecordedTurnCompletedFrame = `{"method":"turn/completed","params":{"threadId":"01a00339-a80e-72a0-966f-116427b6b9ce","turn":{"id":"01a0033b-d0be-77e1-88e7-584ddeea562d","items":[{"type":"agentMessage","id":"msg_0974dc2bafd212c4016a7fcdd4620c819188a770bcca8faf1e","text":"STEERED","phase":"final_answer","memoryCitation":null}],"itemsView":"summary","status":"completed","error":null,"startedAt":1786760646,"completedAt":1786760660,"durationMs":13672}},"emittedAtMs":1786760660565}`

// r7RolloutError builds the RECORDED pre-first-turn resume failure, verbatim from
// errors-observed.json: {"code":-32600,"message":"no rollout found for thread id <uuid>"}.
func r7RolloutError(threadID string) error {
	return &appserver.RPCError{Code: -32600, Message: "no rollout found for thread id " + threadID}
}

// ---------------------------------------------------------------------------
// BLOCKING 1 -- the assembled topology
// ---------------------------------------------------------------------------

// TestR7R3_TheAgentIsLaunchedAsACLIENTOfTheAppServerTheShimSpawned is M4.1 end to end.
//
// THE THREE ASSERTIONS ARE THREE DIFFERENT WITNESSES of the same one fact, because the fact is
// the wave:
//
//  1. the shim really spawned a backend and recorded it (backend.json names a LIVE pid and the
//     socket inside the session dir);
//  2. the AGENT's own argv, as it printed it on its PTY into the session transcript, carries
//     `--remote unix://<that socket>` -- which is the daemon's go-ahead arriving, verbatim,
//     at the process that must act on it;
//  3. the daemon holds the thread the app-server announced, and the app-server announces it
//     ONLY when the agent attaches -- so a daemon that adopted a thread is a daemon whose
//     agent really became a client of that server.
//
// MUTATION (review BLOCKING 1's own probe): change `SendBackendAttach(id, ch.AgentArgs)` to
// `SendBackendAttach(id, nil)` and assertions 2 and 3 both fail.
func TestR7R3_TheAgentIsLaunchedAsACLIENTOfTheAppServerTheShimSpawned(t *testing.T) {
	codexBin := buildFakeCodex(t)
	// The daemon resolves the backend program through the SAME PATH search Detect uses. Putting
	// the fake first is the whole of the substitution: no seam, no injection, no test knob.
	t.Setenv("PATH", filepath.Dir(codexBin)+string(os.PathListSeparator)+os.Getenv("PATH"))

	sk := assemble(t)
	// Bound the join so a BROKEN topology fails in seconds rather than idling out the
	// 45 s production deadline. It never shortens a SUCCESSFUL path.
	sk.backendReady = 20 * time.Second

	m, err := sk.Core().Launch(daemon.LaunchSpec{
		AgentType: "codex",
		Argv:      []string{codexBin, "agent"},
		Cwd:       t.TempDir(),
		ClientEnv: []string{"PATH=" + os.Getenv("PATH")},
		Cols:      80,
		Rows:      24,
	})
	if err != nil {
		t.Fatalf("core Launch of a codex session: %v", err)
	}
	t.Cleanup(func() {
		if m.ShimPID > 0 {
			_ = syscall.Kill(m.ShimPID, syscall.SIGTERM)
		}
	})

	dir := filepath.Join(sk.stateDir, m.ID)
	sock := filepath.Join(dir, "codex.sock")

	// 1. THE SHIM OWNS THE BACKEND. The core planned it from the session's own adapter and
	//    never derived it; the shim spawned it and recorded it the moment its socket served.
	var info shim.BackendInfo
	awaitTrue(t, 30*time.Second, "the shim never recorded a live session backend", func() bool {
		bi, ok := shim.ReadBackendInfo(dir)
		if !ok || bi.PID <= 0 || syscall.Kill(bi.PID, 0) != nil {
			return false
		}
		info = bi
		return true
	})
	if info.SocketPath != sock {
		t.Errorf("backend.json names socket %q; the session's backend endpoint is %q, a sibling of "+
			"hook.sock inside the session dir (§R7.2b)", info.SocketPath, sock)
	}

	// 2. THE AGENT WAS RELEASED AS A CLIENT OF THAT SOCKET. The transcript is the agent's own
	//    report of its own argv, written on its PTY: nothing between the shim's exec and this
	//    string can be faked by the daemon.
	want := "--remote unix://" + sock
	transcript := filepath.Join(dir, "transcript.log")
	awaitTrue(t, 30*time.Second, fmt.Sprintf(
		"the agent's argv never carried %q. The daemon's go-ahead is the ONLY thing that puts it "+
			"there, and without it the TUI and the daemon are on two unconnected planes: no "+
			"thread/started ever reaches this daemon, the phone drives a conversation the owner "+
			"cannot see, and the wave's exit criterion is structurally unreachable", want),
		func() bool {
			body, _ := os.ReadFile(transcript)
			return strings.Contains(string(body), want)
		})

	// 3. THE DAEMON JOINED THE AGENT'S OWN THREAD. The fake app-server announces `thread/started`
	//    only when the AGENT attaches, so this assertion cannot pass on a session whose agent
	//    was launched without `--remote`.
	awaitTrue(t, 30*time.Second, "the daemon never registered a live backend for the session", func() bool {
		_, ok := sk.sessionBackendFor(m.ID)
		return ok
	})
	b, _ := sk.sessionBackendFor(m.ID)
	if b.threadID != fakeCodexThreadID {
		t.Errorf("the daemon joined thread %q; the app-server announced %q when the AGENT created "+
			"it. A daemon on any other thread is a second conversation on one socket (§R7.2e)",
			b.threadID, fakeCodexThreadID)
	}
}

// awaitTrue polls cond until it holds or the bound elapses, failing with why. The topology
// test's every observation is of another PROCESS, so every one of them is eventual.
func awaitTrue(t *testing.T, within time.Duration, why string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal(why)
}

// ---------------------------------------------------------------------------
// MEDIUM 3 -- the thread join's retry rule, fenced by BEHAVIOUR (round 4: subscribeThread
// split into resumeThreadOnce + subscribeSessionThread)
// ---------------------------------------------------------------------------

// r7ResumeConn is a backend double whose `thread/resume` answers a SCRIPTED sequence of errors
// and then succeeds. It exists because the property under test is a control-flow rule -- which
// errors are retried -- and a source-grep for the error string is satisfied by the string's own
// declaration (review MEDIUM 3, and hard rule 4).
type r7ResumeConn struct {
	mu    sync.Mutex
	errs  []error // consumed one per Call; exhausted means success
	calls int
}

func (c *r7ResumeConn) Call(_ context.Context, method string, _, _ any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if method != "thread/resume" {
		return nil
	}
	c.calls++
	if len(c.errs) == 0 {
		return nil
	}
	err := c.errs[0]
	c.errs = c.errs[1:]
	return err
}

func (c *r7ResumeConn) Respond(context.Context, json.RawMessage, any) error { return nil }
func (c *r7ResumeConn) Close() error                                        { return nil }

func (c *r7ResumeConn) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// TestR7R3_ATransportFaultOnTheThreadJoinFAILSFASTRatherThanRetryingTheWholeWindow pins the
// half of the rule that has no positive symptom: everything EXCEPT the recorded
// `no rollout found` must return immediately.
//
// MUTATION: make resumeThreadOnce loop on any error. The join then retries a dead transport,
// which is a session that HANGS instead of degrading -- and this test fails on both the call
// count and the elapsed time.
func TestR7R3_ATransportFaultOnTheThreadJoinFAILSFASTRatherThanRetryingTheWholeWindow(t *testing.T) {
	sk := assemble(t)
	sk.backendReady = 5 * time.Second

	conn := &r7ResumeConn{errs: []error{errors.New("write unix @->codex.sock: broken pipe")}}
	start := time.Now()
	subscribed, err := sk.resumeThreadOnce(conn, "01a00339-a80e-72a0-966f-116427b6b9ce")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("resumeThreadOnce reported SUCCESS on a transport fault; the daemon would then " +
			"register a backend whose connection is dead")
	}
	if subscribed {
		t.Error("a transport fault was reported as a live subscription; the daemon would believe " +
			"it is receiving an item stream that will never arrive")
	}
	if n := conn.count(); n != 1 {
		t.Errorf("thread/resume was attempted %d times on a transport fault; exactly 1 is the rule. "+
			"Only the RECORDED pre-first-turn failure (`no rollout found`) means WAIT", n)
	}
	if elapsed > 2*time.Second {
		t.Errorf("resumeThreadOnce spent %s on a transport fault against a %s deadline. Retrying a "+
			"transport fault is a session that hangs instead of degrading", elapsed, sk.backendReady)
	}
}

// TestR7R3_TheRecordedRolloutRaceIsRETRIEDWithoutClaimingAnythingWasMissed is the other arm,
// and round 4 CORRECTS ITS SECOND ASSERTION rather than keeping it.
//
// Its round-3 form asserted the retry was reported LATE, i.e. that a gap was owed. That reading
// is backwards: `no rollout found` is returned BECAUSE no turn has begun, so a join that had to
// wait for one missed nothing at all. What must hold now is that the wait is silent -- the
// subscription lands and NOTHING claims the transcript is torn.
func TestR7R3_TheRecordedRolloutRaceIsRETRIEDWithoutClaimingAnythingWasMissed(t *testing.T) {
	sk := assemble(t)
	sk.backendReady = 5 * time.Second

	// The RECORDED error, verbatim from errors-observed.json.
	missing := r7RolloutError("01a00339-a80e-72a0-966f-116427b6b9ce")
	conn := &r7ResumeConn{errs: []error{missing, missing}}

	// Attempt one: the recorded pre-first-turn failure. It is WAIT, not FAIL.
	subscribed, err := sk.resumeThreadOnce(conn, "01a00339-a80e-72a0-966f-116427b6b9ce")
	if subscribed || !isMissingRollout(err) {
		t.Fatalf("the recorded pre-first-turn failure was not recognized: subscribed=%v err=%v", subscribed, err)
	}
	// The retry loop is what turns that into a join, and it holds a REGISTERED session for the
	// life of the session -- so it exits at once here, with no session registered, rather than
	// spinning. Its behavioural fence over a real app-server is
	// TestR7R4_AUserWhoThinksLongerThanTheJoinDeadlineIsNEVERPermanentlyDegraded.
	// This unit test has no core session/instance; install only the retry loop's
	// bookkeeping fixture directly. Production registration now requires an exact
	// current instance because it is also a structured-sink proof boundary.
	sk.backend.mu.Lock()
	if sk.backend.live == nil {
		sk.backend.live = map[string]*sessionBackend{}
	}
	sk.backend.live["r7r3-rollout"] = &sessionBackend{
		threadID: "01a00339-a80e-72a0-966f-116427b6b9ce", conn: conn,
	}
	sk.backend.mu.Unlock()
	sk.subscribeSessionThread("r7r3-rollout", conn, "01a00339-a80e-72a0-966f-116427b6b9ce")

	if !sk.backendSubscribed("r7r3-rollout") {
		t.Fatal("the subscription loop gave up on the recorded pre-first-turn race. The rollout " +
			"file is created when the thread's FIRST TURN starts; until then no resume can " +
			"succeed no matter how well-formed, and giving up is giving up on the owner thinking")
	}
	if n := conn.count(); n != 3 {
		t.Errorf("thread/resume was attempted %d times; 2 recorded failures then 1 success is 3", n)
	}
	if countJournalStructuredGaps(t, sk, "r7r3-rollout") > 0 {
		t.Error("waiting for the first turn was reported as a structured_gap. Nothing was missed: " +
			"the resume could not succeed because no turn had happened, and a gap there is " +
			"factually false -- and it is what removes the composer from a healthy session")
	}
}

// ---------------------------------------------------------------------------
// MEDIUM 4 -- ending a session must CLOSE its backend connection
// ---------------------------------------------------------------------------

// TestR7R3_EndingASessionCLOSESItsBackendConnection closes the last "nobody owns this fd" hole
// of the same shape as the SIGKILL residual.
//
// Round 2's endSession dropped the REGISTRATION (forgetBackendPump/forgetBackend) and left the
// *appserver.Client open: its readLoop goroutine and its UDS lived until the app-server itself
// died, and watchSessionBackend returned at its sessionBackendFor guard rather than reaping
// anything. Bounded by the shim's group kill, so a leak WINDOW rather than a permanent one --
// but a connection nobody owns is exactly what R7 spent a round closing everywhere else.
//
// MUTATION: drop the Close from forgetBackend. This test fails.
func TestR7R3_EndingASessionCLOSESItsBackendConnection(t *testing.T) {
	r := newR7ComposerRig(t, true)

	r.sk.endSession(r.local)

	if r.backend.closes() == 0 {
		t.Error("endSession dropped the session's backend registration but never CLOSED the " +
			"connection. Its read loop and its socket outlive the session they belong to, and " +
			"nothing else in the daemon owns that fd")
	}
	if _, ok := r.sk.sessionBackendFor(r.local); ok {
		t.Error("the ended session still has a live backend registration; every op that needed it " +
			"must now refuse rather than reach a connection nobody owns")
	}
}

// ---------------------------------------------------------------------------
// MEDIUM 1 -- a daemon that rejoins MID-TURN must hold that turn
// ---------------------------------------------------------------------------

// TestR7R3_ADaemonThatJoinsMIDTURNHoldsTheRunningTurnRatherThanReadingAsIdle is review
// MEDIUM 1, and the consequence is the one M4.4 exists to prevent.
//
// PROBED AGAINST ROUND 2: a turn opened ONLY on a `user_message`, and the native id was noted
// only `if turn != ""`. After a daemon restart mid-turn -- the path round 2 deliberately opened
// with connectBackendsForRunning -- the daemon held neither. The agent's `item/started`
// userMessage fired before this daemon existed, so:
//
//   - the phone rendered an IDLE session (no open turn, no `turn_id` on any item);
//   - a composer send therefore carried `expected_turn: ""`; the daemon must ignore that stale
//     render context for message routing and steer the adopted current native turn, never start
//     a competing one;
//   - and Stop was impossible, because interruptTurn refuses an empty expected_turn.
//
// The in-code comment claimed the opposite in as many words ("it is what keeps the native id
// available for a turn whose opening frame this daemon never saw (a rejoin mid-turn)").
//
// MUTATION: restore `turn := d.turnIDs[sessionID]` with no adoption arm. The first assertion
// fails immediately.
func TestR7R3_ADaemonThatJoinsMIDTURNHoldsTheRunningTurnRatherThanReadingAsIdle(t *testing.T) {
	r := newR7ComposerRig(t, true)

	// The ONLY frame this daemon ever sees is one from the middle of a running turn: the
	// opening userMessage fired before it existed. The frame is the RECORDED delta shape and
	// carries the CLI's own turnId, which is all the daemon has to go on.
	r.sk.ingestBackendFrame(r.local, r7DeltaFrame("01a0033b-d1c0-7000-b000-000000000001", "counting: 1"), time.Now().UnixMilli())
	r.sk.flushBackendFrames(r.local)
	items := awaitItems(t, r.sk, r.local, 1)

	turn, _ := items[len(items)-1]["turn_id"].(string)
	if turn == "" {
		t.Fatal("the daemon holds NO turn for a turn that is demonstrably running. The phone then " +
			"renders an idle session, its composer send takes the turn/start branch and QUEUES A " +
			"SECOND CONCURRENT TURN, and Stop is impossible because interrupt refuses an empty " +
			"expected_turn")
	}

	// The send the phone would make against an idle-looking render is accepted and routed by
	// current daemon state. expected_turn is advisory for messages; no second turn is started.
	code, err := r.send(t, "", "and what about 41?", "op-r3-second-turn")
	if err != nil || code != "" {
		t.Fatalf("an advisory send from an idle-looking render was refused: code %q err %v", code, err)
	}
	for _, c := range r.backend.recorded() {
		if c.Method == "turn/start" {
			t.Error("turn/start was dispatched into a session whose turn is still running")
		}
	}

	// A send that names the adopted turn also steers it, carrying the CLI'S OWN id -- so both
	// advisory render contexts converge on the same authoritative current state.
	if code, err := r.send(t, turn, "and what about 41?", "op-r3-steer"); err != nil {
		t.Fatalf("a send naming the adopted turn was refused: %v (code %q)", err, code)
	}
	calls := r.backend.recorded()
	last := calls[len(calls)-1]
	if last.Method != "turn/steer" {
		t.Fatalf("a mid-turn send dispatched %q; a running turn is STEERED", last.Method)
	}
	var params struct {
		ExpectedTurnID string `json:"expectedTurnId"`
	}
	if err := json.Unmarshal(last.Params, &params); err != nil {
		t.Fatalf("decode steer params: %v", err)
	}
	if params.ExpectedTurnID != r7NativeTurnID {
		t.Errorf("the steer named expectedTurnId %q; the running turn is the CLI's own %q",
			params.ExpectedTurnID, r7NativeTurnID)
	}
}

// TestR7R3_AFrameOfAnALREADYCLOSEDTurnDoesNotReopenIt is the negative that keeps the adoption
// above from being a turn that never ends. IS-ENV-1's closure rule is the daemon's own, and a
// trailing frame of a turn this daemon SAW CLOSE must not resurrect it.
func TestR7R3_AFrameOfAnALREADYCLOSEDTurnDoesNotReopenIt(t *testing.T) {
	r := newR7ComposerRig(t, true)

	// A full turn, opened and closed the ordinary way.
	r7OpenNativeTurn(t, r)
	r.sk.ingestBackendFrame(r.local, []byte(r7RecordedTurnCompletedFrame), time.Now().UnixMilli())
	r.sk.flushBackendFrames(r.local)
	awaitTrue(t, 5*time.Second, "the recorded turn/completed never closed the daemon's turn", func() bool {
		r.sk.itemMu.Lock()
		defer r.sk.itemMu.Unlock()
		return r.sk.turnIDs[r.local] == ""
	})

	// A trailing frame of that same, finished turn.
	r.sk.ingestBackendFrame(r.local, r7DeltaFrame("01a0033b-d1c0-7000-b000-000000000002", "postscript"), time.Now().UnixMilli())
	r.sk.flushBackendFrames(r.local)
	time.Sleep(300 * time.Millisecond)

	r.sk.itemMu.Lock()
	reopened := r.sk.turnIDs[r.local]
	r.sk.itemMu.Unlock()
	if reopened != "" {
		t.Errorf("a frame of a turn this daemon already CLOSED reopened it as %q. The session then "+
			"looks busy forever, every composer send is steered into a turn that has ended, and "+
			"the phone can never start a new one", reopened)
	}
}
