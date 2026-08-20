package skeleton

// WAVE R7, CLOSING REVIEW -- THE EXIT CRITERION ON THE ASSEMBLED TOPOLOGY.
//
// Round 4's fences for Rulings 1-3 drive the real joinSessionBackend against an IN-PROCESS
// WebSocket app-server. That is the right fence for the join's arms, and it is not the exit
// criterion: it holds the daemon's own goroutine and its own socket, so nothing in it proves
// that the ASSEMBLED machine -- real core, real shim process, real backend process, real UDS --
// reaches the same state. This file drives the assembly, on the ordinary fresh path:
//
//	Core().Launch("codex") -> the real shim binary -> a real `codex app-server` process
//	  -> the real appserver client's dial and boot handshake -> the go-ahead -> the real agent
//	  process as a client of that socket -> `thread/started` -> the join
//	  -> coreAPI.ComposerSend -> turn/start ACROSS THE UDS -> the rollout -> the subscription
//
// THE DOUBLE HAD TO BECOME FAITHFUL FIRST, and that is itself a review finding.
// cmd/swarm-fake-codex answered EVERY request with an empty result, `thread/resume` included --
// so on the assembled path a FRESH launch looked exactly like a thread with prior history, and
// the one recorded integration constraint (r1-codex-gate.md:112-115: `no rollout found for
// thread id` until the thread's first turn starts) was modelled nowhere outside the in-process
// double. The fake now refuses `thread/resume` until a `turn/start` has created the rollout,
// which is what makes the assertions below mean anything.

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/shim"
)

// r7CloseLaunch brings up the whole topology and returns the launched session. backendReady is
// left at the PRODUCTION default (45 s) on purpose: the deadline round 3 degraded on is the
// deadline this probe must outlive.
func r7CloseLaunch(t *testing.T) (*Daemon, persist.Meta, string) {
	t.Helper()
	codexBin := buildFakeCodex(t)
	t.Setenv("PATH", filepath.Dir(codexBin)+string(os.PathListSeparator)+os.Getenv("PATH"))

	sk := assemble(t)
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
	return sk, m, filepath.Join(sk.stateDir, m.ID, "codex.sock")
}

// r7CloseProbe asks the backend PROCESS what it was asked, over its own UDS.
func r7CloseProbe(t *testing.T, sock string) (turnStarts int, rollout bool) {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}}
	resp, err := client.Get("http://backend/probe")
	if err != nil {
		t.Fatalf("probe the backend process: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		TurnStarts int  `json:"turnStarts"`
		Rollout    bool `json:"rollout"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode backend probe: %v", err)
	}
	return body.TurnStarts, body.Rollout
}

// TestR7Close_AFreshAssembledCodexSessionTakesAPhoneSendWithNoGapAndNoDegrade is the wave's
// exit criterion reduced to the one thing a user does first: launch, and type before the agent
// has ever run a turn.
func TestR7Close_AFreshAssembledCodexSessionTakesAPhoneSendWithNoGapAndNoDegrade(t *testing.T) {
	sk, m, sock := r7CloseLaunch(t)

	awaitTrue(t, 40*time.Second, "the assembled daemon never registered a message sink for a "+
		"freshly launched Codex session. resolveMessageSink refuses structured_unsupported "+
		"without one, so the phone cannot start the very turn that would create the rollout",
		func() bool { _, ok := sk.sessionBackendFor(m.ID); return ok })

	// The fake app-server models the RECORDED constraint, so this is the real situation: a
	// thread exists, no turn has run, and no resume can succeed yet.
	if _, rollout := r7CloseProbe(t, sock); rollout {
		t.Fatal("the backend process reports a rollout before any turn started; the double is not " +
			"modelling the one recorded integration constraint and this probe proves nothing")
	}
	if sk.backendSubscribed(m.ID) {
		t.Fatal("the daemon reports a live thread subscription before any turn has run")
	}

	if gaps := r7Gaps(t, sk, m.ID); len(gaps) != 0 {
		t.Fatalf("a fresh Codex launch journalled %d structured_gap(s): %v. The phone reads any "+
			"gap as `no message sink` and hides the composer, so a false gap here removes the "+
			"composer from every ordinary healthy session", len(gaps), gaps)
	}
	if sk.sessionDegraded(m.ID) {
		t.Fatal("a fresh, healthy Codex session was DURABLY degraded")
	}

	// THE SEND, on a thread that has had no turns.
	code, err := sk.api.ComposerSend(sk.api.endpointID, "devA:01JR7CLOSE00000000000000", protocol.ComposerSendReq{
		Session: protocol.NamespacedID(sk.api.endpointID, m.ID),
		Text:    "ship it",
	})
	if code != "" || err != nil {
		t.Fatalf("composer_send on a fresh assembled Codex session = code %q err %v", code, err)
	}

	// It crossed the socket as an RPC, which the receiving PROCESS reports.
	starts, rollout := r7CloseProbe(t, sock)
	if starts != 1 {
		t.Fatalf("the backend process received %d turn/start; the phone's words must reach the "+
			"thread as an app-server request, not as keystrokes", starts)
	}
	if !rollout {
		t.Fatal("the turn did not create the rollout in the backend process")
	}

	// And the subscription lands afterwards, without anything being reconstructed.
	awaitTrue(t, 30*time.Second, "the thread subscription never landed after the phone's own turn "+
		"created the rollout; a sink whose item stream never arrives is a silent bridge",
		func() bool { return sk.backendSubscribed(m.ID) })

	if gaps := r7Gaps(t, sk, m.ID); len(gaps) != 0 {
		t.Fatalf("the healthy fresh path journalled %d structured_gap(s): %v", len(gaps), gaps)
	}
	if sk.sessionDegraded(m.ID) {
		t.Fatal("a healthy Codex session that took a phone send was durably degraded")
	}
}

// TestR7Close_KillingTheBackendPROCESSMidSessionStillTearsHonestly is the other half of
// Ruling 3, and the one a reviewer must check: availability must not have been bought by making
// the degrade unreachable. A REAL loss -- the app-server process killed under a live, subscribed
// session -- still emits a structured_gap and still marks the session durably degraded.
func TestR7Close_KillingTheBackendPROCESSMidSessionStillTearsHonestly(t *testing.T) {
	sk, m, sock := r7CloseLaunch(t)
	dir := filepath.Join(sk.stateDir, m.ID)

	awaitTrue(t, 40*time.Second, "no message sink on a fresh assembled Codex session",
		func() bool { _, ok := sk.sessionBackendFor(m.ID); return ok })

	// Land the subscription first, exactly as round 4's watcher fence does: while the
	// subscription is still retrying its own hard-failure arm would notice the dead connection
	// too, and a test that raced the two would pass with no watcher at all.
	if code, err := sk.api.ComposerSend(sk.api.endpointID, "devA:01JR7CLOSE00000000000002", protocol.ComposerSendReq{
		Session: protocol.NamespacedID(sk.api.endpointID, m.ID), Text: "ship it",
	}); code != "" || err != nil {
		t.Fatalf("composer_send = code %q err %v", code, err)
	}
	awaitTrue(t, 30*time.Second, "the subscription never landed", func() bool { return sk.backendSubscribed(m.ID) })
	if gaps := r7Gaps(t, sk, m.ID); len(gaps) != 0 {
		t.Fatalf("control: the healthy path journalled %d structured_gap(s): %v", len(gaps), gaps)
	}
	if starts, _ := r7CloseProbe(t, sock); starts != 1 {
		t.Fatalf("the backend process received %d turn/start", starts)
	}

	// THE REAL LOSS: the app-server process dies under a live session.
	info, ok := shim.ReadBackendInfo(dir)
	if !ok || info.PID <= 0 {
		t.Fatalf("the shim recorded no live backend for this session: %+v", info)
	}
	if err := syscall.Kill(info.PID, syscall.SIGKILL); err != nil {
		t.Fatalf("kill the backend process %d: %v", info.PID, err)
	}

	awaitTrue(t, 30*time.Second, "the app-server PROCESS died mid-session and the transcript "+
		"simply STOPPED; nothing marks the discontinuity and the phone keeps offering a composer "+
		"over a dead channel", func() bool { return len(r7Gaps(t, sk, m.ID)) > 0 })
	if !sk.sessionDegraded(m.ID) {
		t.Fatal("a session whose app-server process died was not durably degraded. Ruling 3 must " +
			"not have bought availability by making the degrade unreachable: a PROVEN structural " +
			"gap is exactly what the one-way marker is for")
	}
	if _, ok := sk.sessionBackendFor(m.ID); ok {
		t.Error("the dead connection is still registered as this session's message sink")
	}
}

// TestR7Close_AnOwnerWhoThinksPastTheProductionDeadlineStillHasAComposer is Ruling 3 on the
// assembled topology, at the PRODUCTION deadline rather than a shortened test one: round 3
// degraded this session permanently, and the degrade is one-way.
func TestR7Close_AnOwnerWhoThinksPastTheProductionDeadlineStillHasAComposer(t *testing.T) {
	if testing.Short() {
		t.Skip("this probe idles past the 45 s production join deadline on purpose")
	}
	sk, m, sock := r7CloseLaunch(t)

	awaitTrue(t, 40*time.Second, "no message sink on a fresh assembled Codex session",
		func() bool { _, ok := sk.sessionBackendFor(m.ID); return ok })

	// The owner thinks, for longer than backendReadyDeadline. No turn starts, so no rollout
	// appears, so no resume can succeed -- and none of that is evidence of anything torn.
	time.Sleep(50 * time.Second)

	if sk.sessionDegraded(m.ID) {
		t.Fatal("thinking for longer than the 45 s production join deadline PERMANENTLY degraded a " +
			"healthy session; markSessionDegraded is one-way and durable (ADR-017)")
	}
	if gaps := r7Gaps(t, sk, m.ID); len(gaps) != 0 {
		t.Fatalf("an idle healthy session journalled %d structured_gap(s): %v", len(gaps), gaps)
	}
	if _, ok := sk.sessionBackendFor(m.ID); !ok {
		t.Fatal("the message sink was dropped because no turn had started")
	}

	code, err := sk.api.ComposerSend(sk.api.endpointID, "devA:01JR7CLOSE00000000000001", protocol.ComposerSendReq{
		Session: protocol.NamespacedID(sk.api.endpointID, m.ID),
		Text:    "ship it",
	})
	if code != "" || err != nil {
		t.Fatalf("composer_send after a 50 s think = code %q err %v", code, err)
	}
	if starts, _ := r7CloseProbe(t, sock); starts != 1 {
		t.Fatalf("the backend process received %d turn/start after the idle", starts)
	}
	awaitTrue(t, 30*time.Second, "the subscription gave up during the idle; it must retry for the "+
		"life of the session", func() bool { return sk.backendSubscribed(m.ID) })
}
