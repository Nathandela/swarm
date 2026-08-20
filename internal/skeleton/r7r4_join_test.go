package skeleton

// WAVE R7, ROUND 4 -- THE BEHAVIOURAL FENCES FOR joinSessionBackend AND watchSessionBackend,
// every arm of both, driven through the REAL production functions against a REAL WebSocket
// JSON-RPC app-server over a REAL UDS. Bead: agents-tracker-hggx.8. ADR-013 §R7.2e/§R7.7
// (revision 3), ADR-017.
//
// WHY THIS FILE EXISTS. Round 3's review found the wave's two remaining blockers living in the
// one function that carries the wave, and found them there BECAUSE NOTHING DROVE IT: every
// §R7.7 lifecycle test called noteBackendUnavailable / noteBackendLost DIRECTLY
// (r7_lifecycle_test.go:87, :145, :234), so the four failure arms of joinSessionBackend and
// both arms of watchSessionBackend were reachable only from production, where no test looked.
// That is the wave's recurring defect class, third instance: a helper was exercised and the
// composition was not.
//
// THE TWO BLOCKERS THESE TESTS WOULD HAVE CAUGHT, stated as the fences below:
//
//	Ruling 1 -- A JOIN THAT MISSED NOTHING IS NOT A GAP. On a fresh launch there is no rollout
//	  because NO TURN HAS HAPPENED (RECORDED, r1-codex-gate.md:112-115), so a resume that had to
//	  retry missed NOTHING. Round 3 emitted structured_gap `backend_joined_late` on exactly that
//	  path -- a FACTUALLY FALSE tear -- and the phone reads any gap as "no message sink"
//	  (TranscriptPanel.kt:331 -> SessionDetailPanel.kt:770 -> Composer.kt:89), so the composer
//	  vanished from the ordinary healthy session. The honest test for lateness is not "did I
//	  retry"; it is "was there history I could not read", and the FIRST resume attempt answers
//	  it exactly: a rollout that already exists proves a turn already ran.
//
//	Ruling 2 -- THE MESSAGE SINK IS THE CONNECTION, NOT THE SUBSCRIPTION. Round 3 registered the
//	  backend only AFTER the resume succeeded, and the resume cannot succeed until a turn starts,
//	  and no turn can start because resolveMessageSink finds no backend and refuses
//	  structured_unsupported. A closed loop on the ordinary fresh launch: the wave's exit
//	  criterion was structurally unreachable.
//
//	Ruling 3 -- A HEALTHY BACKEND IS NEVER PERMANENTLY DEGRADED. "No turn within 45 s" proves
//	  only that the owner is thinking. Round 3 answered it with markSessionDegraded, which
//	  ADR-017 makes ONE-WAY and DURABLE.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/coder/websocket"
)

// ---------------------------------------------------------------------------
// A REAL app-server: a WebSocket endpoint at /rpc over a UDS, exactly as
// r1-codex-fixtures/ws-handshake.txt records the real one, with the ONE recorded
// integration constraint modelled faithfully -- `thread/resume` fails with
// `no rollout found for thread id` until a turn has started.
// ---------------------------------------------------------------------------

type r7r4Server struct {
	t    *testing.T
	sock string

	mu sync.Mutex
	// announce is the thread id sent as `thread/started` once a client has completed
	// `initialized`. Empty means the agent never created a thread.
	announce string
	// rolloutExists gates `thread/resume` on the RECORDED pre-first-turn failure. It is
	// flipped by a `turn/start`, which is exactly when the real app-server creates the
	// rollout file (r1-codex-gate.md:112-115).
	rolloutExists bool
	// hardErr, when set, is returned for every `thread/resume` instead of the rollout race.
	hardErr string
	calls   []string
	conns   []*websocket.Conn
	closed  bool
}

func newR7R4Server(t *testing.T) *r7r4Server {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "swr7r4")
	if err != nil {
		t.Fatalf("short temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	s := &r7r4Server{t: t, sock: filepath.Join(dir, "codex.sock")}
	ln, err := net.Listen("unix", s.sock)
	if err != nil {
		t.Fatalf("bind fake app-server: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/rpc", func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns = append(s.conns, ws)
		s.mu.Unlock()
		ctx := context.Background()
		for {
			_, data, err := ws.Read(ctx)
			if err != nil {
				return
			}
			s.handle(ctx, ws, data)
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return s
}

func (s *r7r4Server) handle(ctx context.Context, ws *websocket.Conn, data []byte) {
	var fr struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if json.Unmarshal(data, &fr) != nil {
		return
	}
	s.mu.Lock()
	s.calls = append(s.calls, fr.Method)
	announce, rollout, hard := s.announce, s.rolloutExists, s.hardErr
	s.mu.Unlock()

	switch fr.Method {
	case "initialize":
		s.reply(ctx, ws, fr.ID, `{"userAgent":{"name":"codex"}}`)
	case "initialized":
		if announce != "" {
			// The AGENT creates the session's one thread; the server announces it to every
			// attached client (RECORDED: frame-samples.json frame 4, received by an observer
			// that started none of its own).
			s.notify(ctx, ws, fmt.Sprintf(
				`{"method":"thread/started","params":{"thread":{"id":%q,"preview":"","status":{"type":"idle"}}}}`,
				announce))
		}
	case "thread/loaded/list":
		s.reply(ctx, ws, fr.ID, fmt.Sprintf(`{"data":[%q]}`, announce))
	case "thread/resume":
		switch {
		case hard != "":
			s.replyErr(ctx, ws, fr.ID, -32000, hard)
		case !rollout:
			// VERBATIM from errors-observed.json.
			s.replyErr(ctx, ws, fr.ID, -32600, "no rollout found for thread id "+announce)
		default:
			s.reply(ctx, ws, fr.ID, fmt.Sprintf(`{"thread":{"id":%q,"preview":"earlier work"}}`, announce))
		}
	case "turn/start":
		// The rollout file is created when the thread's FIRST TURN starts. This is the ONE
		// recorded integration constraint, and modelling it is what makes these tests probe
		// the real deadlock rather than a convenient one.
		s.mu.Lock()
		s.rolloutExists = true
		s.mu.Unlock()
		s.reply(ctx, ws, fr.ID, `{"turn":{"id":"01a0033b-d0be-77e1-88e7-584ddeea562d","items":[],"itemsView":"notLoaded","status":"inProgress"}}`)
	default:
		if len(fr.ID) > 0 {
			s.reply(ctx, ws, fr.ID, `{}`)
		}
	}
}

func (s *r7r4Server) reply(ctx context.Context, ws *websocket.Conn, id json.RawMessage, result string) {
	if len(id) == 0 {
		return
	}
	_ = ws.Write(ctx, websocket.MessageText,
		[]byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":%s}`, id, result)))
}

func (s *r7r4Server) replyErr(ctx context.Context, ws *websocket.Conn, id json.RawMessage, code int, msg string) {
	if len(id) == 0 {
		return
	}
	body, _ := json.Marshal(msg)
	_ = ws.Write(ctx, websocket.MessageText,
		[]byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":%d,"message":%s}}`, id, code, body)))
}

// notify sends one unsolicited notification on this connection.
func (s *r7r4Server) notify(ctx context.Context, ws *websocket.Conn, frame string) {
	_ = ws.Write(ctx, websocket.MessageText, []byte(frame))
}

// push sends one unsolicited notification to every attached client, which is how the real
// server delivers a thread's item stream.
func (s *r7r4Server) push(frame string) {
	s.mu.Lock()
	conns := append([]*websocket.Conn(nil), s.conns...)
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.Write(context.Background(), websocket.MessageText, []byte(frame))
	}
}

// die closes every attached connection, which is §R7.7 case 3: the app-server died mid-session.
func (s *r7r4Server) die() {
	s.mu.Lock()
	conns := append([]*websocket.Conn(nil), s.conns...)
	s.closed = true
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.Close(websocket.StatusGoingAway, "app-server died")
	}
}

func (s *r7r4Server) setRollout(v bool) {
	s.mu.Lock()
	s.rolloutExists = v
	s.mu.Unlock()
}

func (s *r7r4Server) setHardErr(msg string) {
	s.mu.Lock()
	s.hardErr = msg
	s.mu.Unlock()
}

// counted reports how many times method reached this server.
func (s *r7r4Server) counted(method string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, m := range s.calls {
		if m == method {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// The rig: a Codex-shaped session whose backend is the server above, joined through the REAL
// joinSessionBackend.
// ---------------------------------------------------------------------------

type r7r4Rig struct {
	sk         *Daemon
	srv        *r7r4Server
	local      string
	namespaced string
}

// newR7R4Rig launches a session and returns it UNJOINED: each test decides what the app-server
// does before it calls the real join.
func newR7R4Rig(t *testing.T, announce string) *r7r4Rig {
	t.Helper()
	sk := assemble(t)
	// Bounds the DIAL and the wait for `thread/started` only. It never bounds the thread
	// subscription any more -- that is Ruling 3.
	sk.backendReady = 3 * time.Second
	ad := &r7CodexAdapter{Adapter: newPlainAdapter().Adapter}
	sk.adapterFor = func(string) (adapter.Adapter, bool) { return ad, true }
	m := launchFake(t, sk, r7StdinScript)
	srv := newR7R4Server(t)
	srv.mu.Lock()
	srv.announce = announce
	srv.mu.Unlock()
	return &r7r4Rig{
		sk: sk, srv: srv, local: m.ID,
		namespaced: protocol.NamespacedID(sk.api.endpointID, m.ID),
	}
}

// join drives the REAL production function, with the REAL channel a shim would have minted.
func (r *r7r4Rig) join() {
	r.sk.joinSessionBackend(r.local, daemon.BackendChannel{
		SocketPath: r.srv.sock,
		AgentArgs:  []string{"--remote", "unix://" + r.srv.sock},
	})
}

func (r *r7r4Rig) send(t *testing.T, text, opID string) (protocol.ErrorCode, error) {
	t.Helper()
	return r.sk.api.ComposerSend(r.sk.api.endpointID, opID, protocol.ComposerSendReq{
		Session: r.namespaced, Text: text,
	})
}

// awaitSink polls until the session has a live message sink, which is what the composer needs
// and what Ruling 2 makes independent of the subscription.
func (r *r7r4Rig) awaitSink(t *testing.T, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if _, ok := r.sk.sessionBackendFor(r.local); ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the session has NO message sink after %s. resolveMessageSink refuses "+
		"structured_unsupported without one, so the phone cannot start the very turn that would "+
		"create the rollout the subscription is waiting for: a closed loop on the ordinary "+
		"fresh launch, and the wave's exit criterion is structurally unreachable", within)
}

// noGap fails if any structured_gap reached the journal, quoting the reasons. The phone reads
// gaps off the TRANSCRIPT to decide whether to show a composer at all, so a false gap is not an
// inconvenience: it removes the composer from a perfectly healthy session.
func (r *r7r4Rig) noGap(t *testing.T, why string) {
	t.Helper()
	gaps := r7Gaps(t, r.sk, r.local)
	if len(gaps) == 0 {
		return
	}
	var reasons []string
	for _, g := range gaps {
		s, _ := g["reason"].(string)
		reasons = append(reasons, s)
	}
	t.Fatalf("%s -- but %d structured_gap(s) were journalled: %v", why, len(gaps), reasons)
}

// ---------------------------------------------------------------------------
// RULING 1 + RULING 2 -- the ordinary fresh launch
// ---------------------------------------------------------------------------

// TestR7R4_AFreshLaunchNEVERGapsAndTheComposerDrivesTheThreadBeforeAnyTurnExists is the wave's
// exit criterion on the ordinary path, and the single test that fails against round 3 for BOTH
// blocking reasons at once.
//
// THE RECORDED SITUATION, modelled exactly: the daemon is a connected client before the agent
// exists, the agent creates the thread, the server announces it -- and `thread/resume` cannot
// succeed yet, because the rollout file is created when the thread's FIRST TURN STARTS and no
// turn has started. Nothing has been missed: there is nothing to miss.
//
// MUTATION FENCE. Move registerBackend back below the subscription (round 3's order) and the
// send below refuses structured_unsupported. Restore the `late -> emitBackendGap` arm and noGap
// fails. Either mutation fails this test.
func TestR7R4_AFreshLaunchNEVERGapsAndTheComposerDrivesTheThreadBeforeAnyTurnExists(t *testing.T) {
	r := newR7R4Rig(t, fakeCodexThreadID)
	go r.join()

	r.awaitSink(t, 20*time.Second)
	r.noGap(t, "a fresh Codex launch has a thread that has run NO turn, so a join that could not "+
		"resume yet has missed NOTHING; emitting a gap there is not merely inconvenient, it is "+
		"factually false, and the false statement is what removes the composer")
	if r.sk.sessionDegraded(r.local) {
		t.Fatal("a fresh, healthy Codex session was DURABLY DEGRADED. markSessionDegraded is " +
			"one-way by ADR-017's design and is reserved for a PROVEN structural gap")
	}

	// The composer drives the thread BEFORE any turn exists -- which is the only way a turn ever
	// comes to exist on a phone-first session.
	if code, err := r.send(t, "ship it", "devA:01JR7R4SEND0000000000000"); code != "" || err != nil {
		t.Fatalf("composer_send on a healthy fresh session = code %q err %v; the sink is the "+
			"CONNECTION, and this connection is initialized and usable for turn/start", code, err)
	}
	if n := r.srv.counted("turn/start"); n != 1 {
		t.Fatalf("the app-server received %d turn/start; the phone's message must reach the "+
			"thread as an RPC", n)
	}

	// ORDERING HAZARD (Ruling 2's own question). That turn started BEFORE the subscription was
	// live. It is still captured, because the subscription retries and the server streams the
	// running thread to a client that resumes mid-turn (RECORDED: the R1 observer joined a turn
	// already in flight and received its item stream). The daemon does not reconstruct anything.
	awaitTrue(t, 20*time.Second, "the subscription never landed after the turn created the "+
		"rollout; a sink whose stream never arrives is a silent bridge", func() bool {
		return r.srv.counted("thread/resume") >= 2 && r.subscribed()
	})
	r.srv.push(r7RecordedTurnCompletedFrame)
	items := awaitItems(t, r.sk, r.local, 1)
	if len(items) == 0 {
		t.Fatal("nothing the app-server streamed after the subscription landed reached the journal")
	}
}

// subscribed reports whether the daemon's thread subscription is live for this session.
func (r *r7r4Rig) subscribed() bool { return r.sk.backendSubscribed(r.local) }

// TestR7R4_AUserWhoThinksLongerThanTheJoinDeadlineIsNEVERPermanentlyDegraded is Ruling 3, and
// it is the blocker's own reproduction: a user who thinks for longer than the join window got a
// PERMANENTLY degraded session while the app-server was perfectly healthy.
//
// MUTATION FENCE: bound the subscription by d.backendDeadline() again and follow the timeout
// with noteBackendUnavailable (round 3's arm). This test fails on the degrade AND on the gap.
func TestR7R4_AUserWhoThinksLongerThanTheJoinDeadlineIsNEVERPermanentlyDegraded(t *testing.T) {
	r := newR7R4Rig(t, fakeCodexThreadID)
	r.sk.backendReady = 500 * time.Millisecond
	go r.join()
	r.awaitSink(t, 20*time.Second)

	// The owner thinks. No turn starts, so no rollout appears, for well past the join deadline.
	time.Sleep(2 * time.Second)

	if r.sk.sessionDegraded(r.local) {
		t.Fatal("thinking for longer than the join deadline PERMANENTLY degraded a healthy " +
			"session. `no turn started within the window` proves nothing except that the user is " +
			"thinking; markSessionDegraded is one-way and durable and is reserved for a PROVEN " +
			"structural gap (ADR-017)")
	}
	r.noGap(t, "a session whose owner has not typed yet has no tear in its transcript")
	if _, ok := r.sk.sessionBackendFor(r.local); !ok {
		t.Fatal("the message sink was dropped because no turn had started. The sink is the " +
			"CONNECTION; the subscription is a separate concern that may still be pending")
	}
	// The subscription is still trying, for the life of the session.
	before := r.srv.counted("thread/resume")
	r.srv.setRollout(true)
	awaitTrue(t, 20*time.Second, "the subscription gave up. It must retry for the LIFE OF THE "+
		"SESSION with bounded backoff: the owner may think for a minute and then type",
		func() bool { return r.subscribed() })
	if after := r.srv.counted("thread/resume"); after <= before {
		t.Errorf("thread/resume was attempted %d times before the rollout appeared and %d after; "+
			"the retry must outlive the join deadline", before, after)
	}
}

// TestR7R4_AThreadThatHadALREADYRunTurnsBeforeTheJoinIsAnHonestGap is Ruling 1's OTHER arm, and
// it is the case round 3's heuristic had exactly backwards.
//
// A rollout that ALREADY EXISTS at the first resume attempt proves the thread has already run a
// turn -- and a client that has not resumed does not receive that thread's item stream
// (RECORDED: the R1 observer received the stream only AFTER joining). So this daemon holds a
// transcript that begins mid-conversation, and history must say so. That is a `codex resume`
// -shaped session; round 3 gapped the FRESH one and stayed silent on this one.
//
// MUTATION FENCE: drop the priorHistory arm from joinSessionBackend and this test fails.
func TestR7R4_AThreadThatHadALREADYRunTurnsBeforeTheJoinIsAnHonestGap(t *testing.T) {
	r := newR7R4Rig(t, fakeCodexThreadID)
	r.srv.setRollout(true) // the thread has history: its rollout file exists already
	go r.join()

	gaps := r7AwaitGap(t, r.sk, r.local)
	if len(gaps) == 0 {
		t.Fatal("the daemon joined a thread that had ALREADY run turns and said nothing. It never " +
			"read those turns -- a client receives a thread's items only after it resumes -- so " +
			"this transcript begins mid-conversation, which is the tear ADR-017 forbids bridging " +
			"silently")
	}
	if reason, _ := gaps[0]["reason"].(string); reason != gapBackendPriorHistory {
		t.Errorf("the gap's reason is %q, want %q; a reason nobody can distinguish is a gap "+
			"nobody can explain", reason, gapBackendPriorHistory)
	}
	// The tear is in the HISTORY, not in the channel: this backend is healthy and its sink works.
	r.awaitSink(t, 10*time.Second)
	if r.sk.sessionDegraded(r.local) {
		t.Error("a session whose backend is healthy was durably degraded because its transcript " +
			"is missing older history. The gap is a boundary record; the degrade is a capability " +
			"verdict, and the capability is intact")
	}
}

// ---------------------------------------------------------------------------
// MEDIUM 4 -- every remaining arm of joinSessionBackend, at the real call site
// ---------------------------------------------------------------------------

// TestR7R4_ADialThatNeverSucceedsGapsDegradesAndRELEASESTheShimAnyway is arm 1. The release is
// half the assertion: a daemon that could not dial must not leave the owner with a terminal
// that never starts.
//
// MUTATION FENCE: delete noteBackendUnavailable from the dial-failure arm and this fails.
func TestR7R4_ADialThatNeverSucceedsGapsDegradesAndRELEASESTheShimAnyway(t *testing.T) {
	r := newR7R4Rig(t, fakeCodexThreadID)
	r.sk.backendReady = 400 * time.Millisecond

	r.sk.joinSessionBackend(r.local, daemon.BackendChannel{SocketPath: "/tmp/r7r4-nothing-serves.sock"})

	if len(r7AwaitGap(t, r.sk, r.local)) == 0 {
		t.Fatal("a backend that never connected journalled NO structured_gap; this session will " +
			"never have a structured plane and the phone would keep offering a composer")
	}
	if !r.sk.sessionDegraded(r.local) {
		t.Error("a session whose backend never connected was not degraded; this is exactly the " +
			"case the durable one-way marker is for")
	}
	if _, ok := r.sk.sessionBackendFor(r.local); ok {
		t.Error("a session whose dial failed holds a message sink")
	}
}

// TestR7R4_AThreadNeverAnnouncedGapsDegradesAndCLOSESTheConnection is arm 2: the dial and the
// boot handshake succeeded, and no agent ever created a thread.
//
// MUTATION FENCE: delete the conn.Close() from that arm and the connection count assertion
// fails; delete noteBackendUnavailable and the gap assertion fails.
func TestR7R4_AThreadNeverAnnouncedGapsDegradesAndCLOSESTheConnection(t *testing.T) {
	r := newR7R4Rig(t, "") // the agent never creates a thread
	r.sk.backendReady = 700 * time.Millisecond

	r.join()

	if len(r7AwaitGap(t, r.sk, r.local)) == 0 {
		t.Fatal("a session whose agent never announced a thread journalled NO structured_gap")
	}
	if !r.sk.sessionDegraded(r.local) {
		t.Error("a session with no thread at all was not degraded")
	}
	if _, ok := r.sk.sessionBackendFor(r.local); ok {
		t.Error("a session with no thread holds a message sink; every turn/* would name nothing")
	}
	awaitTrue(t, 10*time.Second, "the daemon abandoned the join without CLOSING the connection; "+
		"its read loop and its UDS are owned by nobody", func() bool {
		return r.srv.livingConns() == 0
	})
}

// livingConns reports how many client connections this server still holds open.
func (s *r7r4Server) livingConns() int {
	s.mu.Lock()
	conns := append([]*websocket.Conn(nil), s.conns...)
	s.mu.Unlock()
	alive := 0
	for _, c := range conns {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		err := c.Ping(ctx)
		cancel()
		if err == nil || errors.Is(err, context.DeadlineExceeded) {
			alive++
		}
	}
	return alive
}

// TestR7R4_AHardResumeFailureAtTheJoinRefusesToRegisterADeafBackend is arm 3: `thread/resume`
// failed with something that is NOT the recorded rollout race, so this daemon will never
// receive this thread's items. Registering the sink anyway would be a composer that sends into
// a transcript that can never show the answer -- the silent bridge, inverted.
//
// MUTATION FENCE: accept any resume error as retryable and this test fails on the sink.
func TestR7R4_AHardResumeFailureAtTheJoinRefusesToRegisterADeafBackend(t *testing.T) {
	r := newR7R4Rig(t, fakeCodexThreadID)
	r.srv.setHardErr("thread is owned by another client")

	r.join()

	if _, ok := r.sk.sessionBackendFor(r.local); ok {
		t.Fatal("the daemon registered a message sink on a thread it can never read. Every " +
			"composer send would succeed and its answer would never appear")
	}
	if len(r7AwaitGap(t, r.sk, r.local)) == 0 {
		t.Error("a join that failed for a reason that is NOT the recorded rollout race journalled " +
			"no structured_gap")
	}
	if !r.sk.sessionDegraded(r.local) {
		t.Error("a session whose thread can never be read was not degraded")
	}
	if n := r.srv.counted("thread/resume"); n != 1 {
		t.Errorf("thread/resume was attempted %d times on a hard failure; only the RECORDED "+
			"pre-first-turn failure means WAIT, and retrying anything else is a session that "+
			"hangs instead of degrading", n)
	}
}

// TestR7R4_ASubscriptionThatFailsHARDAfterRegistrationTearsHonestly is the retry loop's own
// failure arm, which exists only because Ruling 3 made the loop outlive the join. A sink whose
// stream can never arrive must not stay registered silently.
//
// MUTATION FENCE: make the loop `continue` on a hard error and this test fails.
func TestR7R4_ASubscriptionThatFailsHARDAfterRegistrationTearsHonestly(t *testing.T) {
	r := newR7R4Rig(t, fakeCodexThreadID)
	go r.join()
	r.awaitSink(t, 20*time.Second)

	r.srv.setHardErr("thread is gone")
	awaitTrue(t, 20*time.Second, "a subscription that can NEVER be established left the session "+
		"holding a sink whose stream never arrives -- the composer keeps working and the "+
		"transcript never moves, which is the silent bridge ADR-017 forbids", func() bool {
		_, live := r.sk.sessionBackendFor(r.local)
		return !live
	})
	if len(r7AwaitGap(t, r.sk, r.local)) == 0 {
		t.Error("no structured_gap covered a subscription that can never be established")
	}
	if !r.sk.sessionDegraded(r.local) {
		t.Error("a session whose items can never be captured was not degraded; that IS a proven " +
			"structural gap")
	}
}

// ---------------------------------------------------------------------------
// MEDIUM 4 -- both arms of watchSessionBackend, at the real call site
// ---------------------------------------------------------------------------

// TestR7R4_TheWatcherGapsTheTailWhenTheAppServerDiesMidSession is §R7.7 case 3, driven by the
// connection's own end rather than by calling noteBackendLost.
//
// MUTATION FENCE: delete `go d.watchSessionBackend(id, conn)` from joinSessionBackend, or drop
// the noteBackendLost call from the watcher, and this test fails.
func TestR7R4_TheWatcherGapsTheTailWhenTheAppServerDiesMidSession(t *testing.T) {
	r := newR7R4Rig(t, fakeCodexThreadID)
	go r.join()
	r.awaitSink(t, 20*time.Second)
	r.noGap(t, "control: the join itself must be silent")

	// THE SUBSCRIPTION MUST BE LIVE FIRST, and that is what makes this a fence on the WATCHER.
	// While the subscription is still retrying, its own hard-failure arm would notice the dead
	// connection too, and a test that raced the two would pass with no watcher at all (PROBED:
	// deleting `go d.watchSessionBackend` left the first form of this test green).
	r.srv.setRollout(true)
	awaitTrue(t, 20*time.Second, "the subscription never landed", func() bool { return r.subscribed() })

	r.srv.die()

	if len(r7AwaitGap(t, r.sk, r.local)) == 0 {
		t.Fatal("the app-server died mid-session and the transcript simply STOPPED. Nothing marks " +
			"the discontinuity, and the phone keeps offering a composer over a dead channel")
	}
	if !r.sk.sessionDegraded(r.local) {
		t.Error("a session whose app-server died was not degraded; the structured plane is over " +
			"and that is a REAL loss")
	}
	if _, ok := r.sk.sessionBackendFor(r.local); ok {
		t.Error("the dead connection is still registered as this session's message sink")
	}
}

// TestR7R4_TheWatcherIsSILENTForASessionThatAlreadyEnded is the watcher's other arm. A session
// that ENDED is not a session that was TORN: gapping here would put a false tear at the end of
// every completed Codex session.
//
// MUTATION FENCE: delete the sessionBackendFor guard from watchSessionBackend and this fails.
func TestR7R4_TheWatcherIsSILENTForASessionThatAlreadyEnded(t *testing.T) {
	r := newR7R4Rig(t, fakeCodexThreadID)
	go r.join()
	r.awaitSink(t, 20*time.Second)
	r.srv.setRollout(true)
	awaitTrue(t, 20*time.Second, "the subscription never landed", func() bool { return r.subscribed() })

	r.sk.endSession(r.local) // the ordinary end: the registration is dropped and the conn closed
	r.srv.die()
	time.Sleep(500 * time.Millisecond)

	for _, g := range r7Gaps(t, r.sk, r.local) {
		if reason, _ := g["reason"].(string); len(reason) >= len(gapBackendLost) &&
			reason[:len(gapBackendLost)] == gapBackendLost {
			t.Fatalf("a session that ENDED normally was reported as a TORN transcript (%q). Every "+
				"completed Codex session would carry a false tear at its end", reason)
		}
	}
}
