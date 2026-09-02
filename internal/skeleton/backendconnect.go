package skeleton

// THE PRODUCTION WIRING of Wave R7's backend topology (Mirror M4.1; ADR-013 §R7.2b/d/e):
// planning the side process from the session's adapter, dialing its WebSocket JSON-RPC
// endpoint, joining the thread, releasing the shim's go-ahead, and pumping its frames.
//
// THIS IS THE ONE FILE THAT HOLDS BOTH HALVES, and it has to be, for the reason ADR-001 gives:
// internal/daemon owns lifecycle and resolves no adapter, internal/adapter describes and
// owns no fd, internal/appserver owns one socket and knows nothing about sessions. Only the
// assembly has all three, and skeleton.Daemon IS the assembled daemon, so nothing is smuggled
// up a layer by putting the join here.
//
// THE ORDERING IS THE POINT (§R7.2e). The shim binds its control socket, spawns the backend,
// waits for the socket to be servable, writes backend.json -- and BLOCKS. The daemon dials the
// backend, completes initialize/initialized, joins a thread, and only THEN sends
// `backend_attach`. So the daemon is a connected client BEFORE THE AGENT PROCESS EXISTS, which
// is what makes it impossible to miss a `thread/started` and removes the cold-start rollout
// race rather than retrying around it. A go-ahead that never arrives spawns the agent anyway,
// degraded -- the handshake is not a new way to hang.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/registry"
	"github.com/Nathandela/swarm/internal/appserver"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/status"
)

// backendReadyPoll bounds how long the assembly waits for the shim's backend to become
// dialable, and how often it retries. The shim has its own ReadyTimeout on the same event; this
// one covers the shim's spawn plus that wait, so it is deliberately the longer of the two.
const (
	backendReadyDeadline = 45 * time.Second
	backendReadyInterval = 100 * time.Millisecond
)

// backendDeadline is the ready/join bound this daemon actually uses. It exists ONLY so a test
// can drive the degrade paths without waiting out a 45 s dial retry; production never sets it
// (Config carries no knob) and the default is the constant above.
func (d *Daemon) backendDeadline() time.Duration {
	if d.backendReady > 0 {
		return d.backendReady
	}
	return backendReadyDeadline
}

// backendProber is the HostProber the core's backend resolution uses. It is the SAME PATH
// search Detect uses for an agent binary, bound to the agent's own environment rather than the
// daemon's, so the program this resolves is the program the agent would itself run.
type backendProber struct{ env []string }

func (p backendProber) LookPath(name string) (string, error) { return lookPathIn(name, p.env) }

// Run is never called on this path: ResolveBackend LookPaths and nothing else. It is present
// because HostProber is one interface, and it fails loudly rather than pretending.
func (backendProber) Run(string, []string) (string, error) {
	return "", errBackendProberRun
}

// errBackendProberRun names the unreachable arm above.
var errBackendProberRun = errBackendProbe("appserver planning never runs a program; it only resolves one")

type errBackendProbe string

func (e errBackendProbe) Error() string { return string(e) }

// planSessionBackend is daemon.Config.BackendPlanner: the assembly's answer to "does this
// session need a side process, and what is it".
//
// It is where the adapter contract's own checks run (adapter.ResolveBackend): obligation 9a --
// Program is a NAME, resolved through the prober, refused outright if it contains a path
// separator -- and obligation 9c -- no argument may name an absolute path outside the session
// dir. 9c is the only one a malicious or merely buggy adapter cannot talk its way past,
// because the core performs it on data it does not trust.
//
// An adapter that proves no BackendSource answers (nil, nil), which is the ORDINARY case and
// never a defect: most CLIs need no backend at all.
func (d *Daemon) planSessionBackend(agentType, sessionDir, socketPath string, agentEnv []string) (*daemon.BackendSpec, error) {
	ad, ok := registry.New(agentType)
	if !ok {
		return nil, nil
	}
	src, ok := adapter.AsBackendSource(ad)
	if !ok {
		return nil, nil // ADR-010 §5's posture: absence is a signal, not a defect
	}
	spec := adapter.BackendSpec{SocketPath: socketPath}
	// Obligation 8, checked before anything is resolved: a DECLARED backend that names no
	// program is a session whose agent attaches to a socket nobody serves.
	if err := adapter.CheckBackendPlan(src, spec); err != nil {
		return nil, err
	}
	env := agentEnv
	if env == nil {
		env = d.launchEnv()
	}
	plan, ok, err := adapter.ResolveBackend(src, backendProber{env: env}, spec, sessionDir)
	if err != nil || !ok {
		return nil, err
	}
	return &daemon.BackendSpec{
		Program:   plan.Program,
		Args:      plan.Args,
		AgentArgs: plan.AgentArgs,
	}, nil
}

// launchEnv is the FALLBACK environment the backend's program name is resolved against when
// a caller supplies none: the daemon's own, filtered by the launch-environment policy. The
// core's launch path always supplies the session's resolved agent env (the client's when one
// was sent, daemon policy otherwise), so on that path the program this resolves is exactly
// the program the agent itself runs -- a daemon whose own PATH cannot see the agent binary
// no longer plans no backend for a session whose agent resolves fine.
func (d *Daemon) launchEnv() []string { return daemon.PolicyEnv(nil) }

// connectSessionBackend is started for every session the core reports as LAUNCHED. For a
// session with NO backend it returns immediately, which is every session of every other CLI.
func (d *Daemon) connectSessionBackend(id string) {
	if d.core == nil {
		return
	}
	ch, ok := d.core.SessionBackend(id)
	if !ok {
		return // no backend was planned for this session; nothing to join
	}
	go d.joinSessionBackend(id, ch)
}

// connectBackendsForRunning is the POST-ASSEMBLY CATCH-UP for sessions this daemon adopted at
// reconcile, and it is review BLOCKING 4's fix. It is startHookDrainsForRunning's twin, four
// lines below it in Serve, for the identical reason.
//
// WHAT IT REPLACES. daemon.Open runs reconcile synchronously and fires registerSession BEFORE
// Serve has assigned d.core, so connectSessionBackend returned at its nil guard and the
// backend was never rejoined. Nothing then called noteBackendUnavailable or noteBackendLost
// either -- so after any `swarm daemon restart` a live Codex session's transcript stopped
// mid-conversation with NO boundary record. At that time the phone derived composer state from
// `structuredChat = !transcript.structureTorn`, so it still rendered the composer as usable and
// every send was refused AFTER the tap. Current Android instead keeps the shell permanently and
// takes send authority from the machine-authored capability state. The missing boundary was still
// precisely the silent bridge ADR-017 forbids and the harm noteBackendUnavailable prevents.
func (d *Daemon) connectBackendsForRunning() {
	if d.core == nil {
		return
	}
	for _, m := range d.core.List() {
		if m.Status.Process != status.ProcessRunning {
			continue
		}
		ch, ok := d.core.SessionBackend(m.ID)
		if !ok {
			continue
		}
		go d.rejoinSessionBackend(m.ID, ch)
	}
}

// joinSessionBackend performs the whole §R7.2e handshake for one FRESHLY LAUNCHED session.
//
// THE ORDER IS THE FIX FOR REVIEW BLOCKING 3. The daemon dials and completes
// initialize/initialized BEFORE the agent exists -- that is what the go-ahead buys -- and then
// starts NO THREAD OF ITS OWN. It releases the agent, and the agent creates the session's one
// thread; the server announces it with `thread/started` to every attached client (RECORDED),
// the pump adopts it, and every turn/* and every approval from then on names THAT thread. One
// conversation, two surfaces, which is the wave's exit criterion expressed as an assignment.
//
// EVERY FAILURE PATH STILL RELEASES THE SHIM. A daemon that could not dial must not leave the
// owner with a terminal that never starts, so the go-ahead is sent either way -- with the
// backend's agent arguments when the dial succeeded, and with NONE when it did not, which
// launches the agent exactly as a pre-R7 Codex session (no --remote, no structured plane) and
// is accompanied by an honest structured_gap.
//
// AND THE JOIN IS TWO FACTS, NOT ONE (review round 4, ADR-013 §R7.2e revision 4). The
// CONNECTION is the message sink and is registered as soon as it is usable; the thread
// SUBSCRIPTION is a separate concern that may still be pending, and pends without a gap and
// without a degrade for as long as the session lives. Every arm below is fenced behaviourally
// in r7r4_join_test.go, which drives THIS function against a real WebSocket app-server -- the
// absence of exactly that is why round 3 shipped two blockers in these twenty lines.
func (d *Daemon) joinSessionBackend(id string, ch daemon.BackendChannel) {
	expectedInstance, _ := d.sessionInstance(id)
	conn, feed, err := d.dialSessionBackend(id, expectedInstance, ch)
	if err != nil {
		log.Printf("skeleton: session %s backend unavailable: %v", id, err)
		// §R7.7 case 1: this session will NEVER have a structured plane. The gap is emitted
		// AT LAUNCH because the phone reads gaps off the transcript to decide whether to show
		// a composer at all -- without it the owner types and the refusal arrives after the tap.
		d.noteBackendUnavailableForInstance(id, expectedInstance)
		if aerr := d.core.SendBackendAttach(id, nil); aerr != nil {
			log.Printf("skeleton: release session %s without a backend: %v", id, aerr)
		}
		return
	}
	// THE GO-AHEAD, sent before there is a thread: the agent is the party that creates it.
	if aerr := d.core.SendBackendAttach(id, ch.AgentArgs); aerr != nil {
		log.Printf("skeleton: release session %s with its backend: %v", id, aerr)
	}
	threadID, ok := d.awaitAdoptedThread(id, d.backendDeadline())
	if !ok {
		_ = conn.Close()
		log.Printf("skeleton: session %s never announced a thread within %s", id, d.backendDeadline())
		d.noteBackendUnavailableForInstance(id, expectedInstance)
		return
	}
	// THE FIRST RESUME ATTEMPT IS THE ONE THAT CARRIES INFORMATION (review round 4, RULING 1),
	// and it is the only place the daemon can learn whether it is joining history.
	//
	//	it FAILED with `no rollout found` -> the thread has run NO TURN (the rollout file is
	//	  created when the first turn starts, RECORDED r1-codex-gate.md:112-115). There is
	//	  nothing to miss, so there is no gap: retry, quietly, for as long as the session lives.
	//	it SUCCEEDED immediately            -> a rollout ALREADY EXISTED, so this thread has
	//	  already run at least one turn, and a client receives a thread's items only AFTER it
	//	  resumes -- so those turns are history this daemon could not read. THAT is the gap.
	//	anything else                       -> this daemon will never read this thread.
	subscribed, serr := d.resumeThreadOnce(conn, threadID)
	if serr != nil && !isMissingRollout(serr) {
		_ = conn.Close()
		log.Printf("skeleton: session %s could not join thread %s: %v", id, threadID, serr)
		d.noteBackendUnavailableForInstance(id, expectedInstance)
		return
	}
	// RULING 2: THE MESSAGE SINK IS THE CONNECTION, NOT THE SUBSCRIPTION. Registered here --
	// as soon as the connection is initialized and usable for turn/start -- and NOT after the
	// subscription, because the ability to SEND does not depend on having read history.
	// Round 3 registered below the resume, and the resume cannot succeed until a turn starts,
	// and no turn can start because resolveMessageSink finds no backend: a closed loop that
	// made the wave's exit criterion structurally unreachable on the ORDINARY fresh launch.
	if !d.registerBackendFeedForInstance(id, expectedInstance, threadID, conn, feed) {
		_ = conn.Close()
		d.noteBackendUnavailableForInstance(id, expectedInstance)
		return
	}
	go d.watchSessionBackend(id, conn, feed.epoch)
	if subscribed {
		if !d.markBackendSubscribedForFeed(id, feed.epoch) {
			return
		}
		// RULING 1's honest arm, and the only success path that still emits a gap.
		d.emitBackendGapForInstance(id, expectedInstance, gapBackendPriorHistory)
		return
	}
	go d.subscribeSessionThread(id, conn, threadID, feed.epoch)
}

// rejoinSessionBackend is §R7.7 CASE 2: the daemon went away and came back, and the shim and
// its app-server survived it (ADR-001, the whole reason the backend is shim-owned).
//
// It differs from the launch join in the two ways the situation differs. There is NO go-ahead
// to send -- the shim consumed its one at the previous daemon's launch and the agent has been
// running since -- and there is NO `thread/started` to wait for, because it fired before this
// daemon existed. The thread is discovered instead with `thread/loaded/list`, which the R1
// gate recorded returning {"data": ["<threadId>", ...]} as plain id strings, and which the
// gate used for exactly this purpose: finding the TUI's thread from a second client.
//
// A SUCCESSFUL REJOIN EMITS NOTHING AND DEGRADES NOTHING (noteBackendRejoined), because
// reconnection alone proves neither a missed interval nor a gap boundary. A real later
// boundary may disable chat and a freshly proved exact sink may recover it, but an ordinary
// daemon restart must not invent the boundary in the first place.
func (d *Daemon) rejoinSessionBackend(id string, ch daemon.BackendChannel) {
	if _, ok := d.sessionBackendFor(id); ok {
		return // already joined by the launch path; a rejoin would open a second connection
	}
	expectedInstance, _ := d.sessionInstance(id)
	conn, feed, err := d.dialSessionBackend(id, expectedInstance, ch)
	if err != nil {
		// The shim outlived this daemon but its backend did not, or its socket is gone. The
		// current structured plane is unavailable and history must say so: without this the
		// transcript simply stops while the phone keeps offering an unproved composer. A later
		// exact-instance backend proof may recover future sends; it cannot erase the marker.
		log.Printf("skeleton: session %s backend could not be rejoined: %v", id, err)
		d.noteBackendUnavailableForInstance(id, expectedInstance)
		return
	}
	threadID, terr := d.discoverRejoinThread(id, expectedInstance, conn)
	if terr != nil {
		_ = conn.Close()
		log.Printf("skeleton: session %s backend has no rejoinable thread: %v", id, terr)
		d.noteBackendUnavailableForInstance(id, expectedInstance)
		return
	}
	subscribed, serr := d.resumeThreadOnce(conn, threadID)
	if serr != nil && !isMissingRollout(serr) {
		_ = conn.Close()
		log.Printf("skeleton: session %s could not rejoin thread %s: %v", id, threadID, serr)
		d.noteBackendUnavailableForInstance(id, expectedInstance)
		return
	}
	legacyIdentity, current := d.rejoinIdentityIsCurrent(id, expectedInstance, threadID)
	if !current {
		_ = conn.Close()
		// A changed instance is stale work and this scoped call is a no-op. A different
		// durable identity on the SAME instance is a current-session ownership conflict,
		// so it must withdraw the unproved composer visibly.
		d.noteBackendUnavailableForInstance(id, expectedInstance)
		return
	}
	if !d.registerBackendFeedForInstance(id, expectedInstance, threadID, conn, feed) {
		_ = conn.Close()
		d.noteBackendUnavailableForInstance(id, expectedInstance)
		return
	}
	// A pre-persistence session can still be rejoined only under the legacy exactly-one loaded
	// thread rule. Persist that identity after the exact-instance sink is installed, behind the
	// replacement fence; already-persisted sessions need no adoption and therefore cannot poison
	// backend.adopted from an old in-flight reconnect.
	if legacyIdentity && !d.adoptRejoinedThreadForInstance(id, expectedInstance, threadID) {
		_ = d.forgetBackendForFeed(id, feed.epoch)
		d.noteBackendUnavailableForInstance(id, expectedInstance)
		return
	}
	go d.watchSessionBackend(id, conn, feed.epoch)
	// A REJOIN NEVER GAPS ON PRIOR HISTORY, AND THAT SILENTLY BRIDGES THE DOWNTIME WINDOW.
	// Stated honestly (pre-commit correction, 2026-08-20), because the earlier wording here --
	// "this thread's earlier turns were captured by the daemon that launched it and are in the
	// journal already" -- IS FALSE, and falsely in the direction that matters:
	//
	//	turns that ran WHILE A DAEMON WAS ATTACHED   -> in the journal, nothing is lost
	//	turns that ran WHILE NO DAEMON WAS RUNNING   -> captured by NOBODY. The agent kept
	//	  working against the surviving shim, the app-server recorded them in its own rollout,
	//	  and this daemon resumes AFTER them -- a client receives a thread's items only from the
	//	  point it resumes. Those turns are absent from the journal and this path says nothing.
	//
	// SO WHY IS IT STILL SILENT? Capability transitions now separate the permanent visible
	// history marker from current sink authority, but this rejoin still has no evidence that
	// the downtime contained a turn and no exact boundary to publish. Emitting a gap on every
	// ordinary daemon restart would manufacture missing history. This arm therefore stays
	// silent for evidence, not because recovery is impossible.
	//
	// WHAT WOULD CLOSE IT: backfilling the missed interval with `thread/read {includeTurns:true}`
	// and gapping only what the backfill could not recover. That is ADR-013's Q4, and it is open
	// because the `itemsView` that call returns in practice is UNRECORDED -- a `summary` view is
	// lossy for a long turn, so the shape of the honest answer is not yet known.
	// §R7.7 case 2's rule stands: a successful rejoin is not a PROVEN gap, and this daemon cannot
	// tell from a rejoin alone whether the downtime window contained any turn at all.
	if subscribed {
		d.markBackendSubscribedForFeed(id, feed.epoch)
		return
	}
	go d.subscribeSessionThread(id, conn, threadID, feed.epoch)
}

// awaitAdoptedThread blocks until the pump has seen the agent's `thread/started`, or the
// bound elapses.
func (d *Daemon) awaitAdoptedThread(id string, within time.Duration) (string, bool) {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if threadID, ok := d.adoptedThread(id); ok {
			return threadID, true
		}
		time.Sleep(backendReadyInterval)
	}
	return "", false
}

// resumeThreadOnce makes EXACTLY ONE `thread/resume` attempt against a thread this connection
// did not create. It reports whether the subscription is now live.
//
// `thread/resume` on a RUNNING thread is a rejoin, which the server's own schema states ("If
// thread_id identifies a running thread, app-server rejoins that thread") and which the R1
// gate exercised end to end: after it, the second client received the full item stream and
// could start, steer and interrupt turns on that thread and answer its approvals.
//
// ONE ATTEMPT, and the retry policy lives with its callers, because the two callers want
// different things from the SAME error. `no rollout found for thread id` means WAIT rather
// than FAIL -- the rollout file is created when the thread's first turn starts, and until then
// no resume can succeed however well-formed. Every OTHER error is terminal: a transport fault
// retried forever is a session that hangs instead of degrading.
//
// It takes the backendConn INTERFACE rather than the concrete client so its rule can be driven
// by behaviour: which errors are retried is a control-flow property, and a source-grep for the
// error string is satisfied by that string's own declaration (review round 3 MEDIUM 3).
func (d *Daemon) resumeThreadOnce(conn backendConn, threadID string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), backendCallTimeout)
	defer cancel()
	var resumed struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := conn.Call(ctx, "thread/resume", map[string]any{
		"threadId":     threadID,
		"excludeTurns": true,
	}, &resumed); err != nil {
		return false, err
	}
	if !adapter.IsCanonicalConversationID(resumed.Thread.ID) || resumed.Thread.ID != threadID {
		return false, errBackendProbe("thread/resume returned a different or invalid thread identity")
	}
	return true, nil
}

// The subscription retry's bounded backoff. It starts at the poll interval and doubles to a
// ceiling, so a session whose owner is thinking costs one request every few seconds rather than
// ten a second, and one that starts a turn is joined within that ceiling.
const backendSubscribeMaxBackoff = 5 * time.Second

// subscribeSessionThread keeps trying to join the session's thread FOR THE LIFE OF THE SESSION,
// and it is review round 4's RULING 3.
//
// WHAT IT REPLACES, AND WHY. Round 3 bounded the join by d.backendDeadline() (45 s in
// production) and answered the timeout with noteBackendUnavailable -- markSessionDegraded,
// which ADR-017 makes ONE-WAY and DURABLE. But `no rollout found` is returned until the
// thread's FIRST TURN STARTS, and a fresh session has no turn until the owner types: "no turn
// within 45 s" proves nothing except that the user is thinking. A user who thought for
// 45 seconds got a PERMANENTLY degraded session while the app-server was perfectly healthy.
//
// WHAT BOUNDS IT NOW: the session itself. The loop exits when the session's backend
// registration is gone -- which forgetBackend does on endSession, on noteBackendLost, and from
// the arm below -- so it cannot outlive the session it belongs to, and it holds no timer that
// would keep a dead session alive. Between attempts the backoff doubles to
// backendSubscribeMaxBackoff.
//
// THE HARD-FAILURE ARM IS A REAL LOSS, and it degrades honestly. A resume that fails for any
// reason OTHER than the recorded rollout race will never succeed, so this daemon holds a sink
// whose item stream can never arrive: the composer would keep working while the transcript
// never moved, which is the silent bridge ADR-017 forbids.
func (d *Daemon) subscribeSessionThread(id string, conn backendConn, threadID string, feedEpoch ...string) {
	expectedFeed := ""
	if len(feedEpoch) > 0 {
		expectedFeed = feedEpoch[0]
	}
	wait := backendReadyInterval
	for {
		timer := time.NewTimer(wait)
		<-timer.C
		if !d.backendFeedCurrent(id, expectedFeed) {
			return // the session ended, or this exact connection was reaped/replaced
		}
		ok, err := d.resumeThreadOnce(conn, threadID)
		if ok {
			d.markBackendSubscribedForFeed(id, expectedFeed)
			return
		}
		if !d.backendFeedCurrent(id, expectedFeed) {
			return // a replacement landed while this RPC was in flight
		}
		if !isMissingRollout(err) {
			log.Printf("skeleton: session %s can never subscribe to thread %s: %v", id, threadID, err)
			reason := "the thread subscription could not be established: " + err.Error()
			if expectedFeed == "" {
				d.noteBackendLost(id, reason)
			} else {
				d.noteBackendLostForFeed(id, expectedFeed, reason)
			}
			return
		}
		if wait < backendSubscribeMaxBackoff {
			wait *= 2
			if wait > backendSubscribeMaxBackoff {
				wait = backendSubscribeMaxBackoff
			}
		}
	}
}

// isMissingRollout reports the RECORDED pre-first-turn resume failure
// (errors-observed.json: {"code":-32600,"message":"no rollout found for thread id <uuid>"}).
func isMissingRollout(err error) bool {
	var rpcErr *appserver.RPCError
	if !errors.As(err, &rpcErr) {
		return false
	}
	return strings.Contains(rpcErr.Message, "no rollout found")
}

// discoverLoadedThread finds the one thread this session's app-server is holding.
//
// RECORDED SHAPE (r1-codex-gate.md:96): thread/loaded/list returns {"data": ["<threadId>"...]},
// plain id strings. EXACTLY ONE is required: §R7.10 pins one app-server per session, so two
// threads means this daemon cannot tell which one the owner is looking at, and picking wrong
// puts the phone on a conversation nobody is having. Refusing is the honest answer, and it
// degrades to a structured_gap rather than to a guess.
func (d *Daemon) discoverLoadedThread(conn backendConn) (string, error) {
	loaded, err := loadBackendThreads(conn)
	if err != nil {
		return "", err
	}
	return selectOnlyLoadedThread(loaded)
}

// discoverRejoinThread selects the provider thread owned by this exact Swarm session
// instance. A Codex app-server can legitimately hold the main thread plus guardian or
// sub-agent threads, so the loaded list is an inventory, not an ownership proof. The
// canonical conversation id already persisted in Meta is that proof, but only when the
// provider confirms that it is present exactly once in the current inventory.
//
// Legacy sessions with no persisted identity retain the old fail-closed rule: exactly one
// canonical loaded thread may be selected. A malformed persisted value is not silently
// treated as absent because doing so could route the phone to the wrong conversation.
func (d *Daemon) discoverRejoinThread(id, expectedInstance string, conn backendConn) (string, error) {
	loaded, err := loadBackendThreads(conn)
	if err != nil {
		return "", err
	}
	persisted, exists, err := d.persistedRejoinIdentity(id, expectedInstance)
	if err != nil {
		return "", err
	}
	if !exists {
		return selectOnlyLoadedThread(loaded)
	}
	matches := 0
	for _, candidate := range loaded {
		if candidate == persisted {
			matches++
		}
	}
	if matches != 1 {
		return "", errBackendProbe(fmt.Sprintf(
			"the persisted conversation appears %d times in this session's loaded thread inventory; exactly one is required",
			matches))
	}
	return persisted, nil
}

func (d *Daemon) persistedRejoinIdentity(id, expectedInstance string) (string, bool, error) {
	if expectedInstance == "" || d.core == nil {
		return "", false, errBackendProbe("the session instance is unavailable during backend rejoin")
	}
	if current, ok := d.sessionInstance(id); !ok || current != expectedInstance {
		return "", false, errBackendProbe("the session instance changed during backend rejoin")
	}
	m, ok := d.core.Get(id)
	if !ok {
		return "", false, errBackendProbe("the session disappeared during backend rejoin")
	}
	if m.ConversationID == "" {
		return "", false, nil
	}
	if !adapter.IsCanonicalConversationID(m.ConversationID) {
		return "", false, errBackendProbe("the persisted conversation identity is invalid")
	}
	return m.ConversationID, true, nil
}

// rejoinIdentityIsCurrent is the post-resume half of the ownership fence. It prevents an
// in-flight old connection from being adopted after either session replacement or a change
// to the durable provider identity.
func (d *Daemon) rejoinIdentityIsCurrent(id, expectedInstance, threadID string) (legacy, current bool) {
	persisted, exists, err := d.persistedRejoinIdentity(id, expectedInstance)
	if err != nil {
		return false, false
	}
	if !exists {
		return true, true
	}
	return false, persisted == threadID
}

// adoptRejoinedThreadForInstance preserves the legacy one-thread migration without letting a
// rejoin captured from an old session instance persist its provider identity into a replacement.
// recordSessionInstance uses the same outer fence, so the comparison and adoption are one
// replacement transaction.
func (d *Daemon) adoptRejoinedThreadForInstance(id, expectedInstance, threadID string) bool {
	if d.contextGuards != nil {
		d.contextGuards.replaceMu.Lock()
		defer d.contextGuards.replaceMu.Unlock()
	}
	if current, ok := d.sessionInstance(id); !ok || current != expectedInstance {
		return false
	}
	d.adoptBackendThread(id, threadID)
	adopted, ok := d.adoptedThread(id)
	if !ok || adopted != threadID || d.core == nil {
		return false
	}
	// SetConversationID is deliberately write-once and reports a competing prior value as
	// a successful no-op. Read the durable fact back: the in-memory adoption is authority
	// only when metadata now names this exact provider thread.
	m, ok := d.core.Get(id)
	return ok && m.ConversationID == threadID
}

func loadBackendThreads(conn backendConn) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), backendCallTimeout)
	defer cancel()
	var loaded struct {
		Data []string `json:"data"`
	}
	if err := conn.Call(ctx, "thread/loaded/list", map[string]any{}, &loaded); err != nil {
		return nil, err
	}
	return loaded.Data, nil
}

func selectOnlyLoadedThread(loaded []string) (string, error) {
	if len(loaded) != 1 {
		return "", errBackendProbe(fmt.Sprintf(
			"this session's app-server holds %d threads; exactly one is expected, and guessing "+
				"which one the owner is looking at is not a choice this daemon may make",
			len(loaded)))
	}
	if !adapter.IsCanonicalConversationID(loaded[0]) {
		// The raw provider value is untrusted and may contain prose or control
		// characters. Refuse it before thread/resume, routing registration, or any
		// log call, and expose only a stable generic diagnostic.
		return "", errBackendProbe("thread/loaded/list returned an invalid thread identity")
	}
	return loaded[0], nil
}

// dialSessionBackend waits for the shim's backend to be servable, upgrades to its WebSocket
// JSON-RPC endpoint and completes the boot handshake. IT STARTS NO THREAD: which thread this
// session is on is the AGENT's to decide, and the two callers above learn it in the two ways
// their situations allow.
func (d *Daemon) dialSessionBackend(id, expectedInstance string, ch daemon.BackendChannel) (*appserver.Client, *backendFeed, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d.backendDeadline())
	defer cancel()
	feed := newBackendFeed()

	var conn *appserver.Client
	var err error
	for {
		conn, err = appserver.Dial(ctx, ch.SocketPath, appserver.Options{
			// Every frame goes to the ONE pump, which owns the batching, the typed-status
			// application, the thread adoption and the item path (backend.go). A notification
			// and a server-request differ only in whether an id rides along, and the pump
			// reads that off the frame itself, so both arrive here as the same verbatim bytes.
			OnNotify: func(method string, params json.RawMessage) {
				at := time.Now()
				frame := rebuildFrame(method, nil, params)
				d.ingestBackendFrameForFeed(id, expectedInstance, feed, method, frame, at)
			},
			OnRequest: func(rid json.RawMessage, method string, params json.RawMessage) {
				at := time.Now()
				frame := rebuildFrame(method, rid, params)
				d.ingestBackendFrameForFeed(id, expectedInstance, feed, method, frame, at)
			},
		})
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return nil, nil, err
		case <-time.After(backendReadyInterval):
		}
	}

	// The recorded boot handshake, in the recorded order (frame-samples.json): initialize with
	// experimentalApi, then the `initialized` notification.
	var initRes json.RawMessage
	if cerr := conn.Call(ctx, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "swarm", "title": "swarm", "version": "1"},
		"capabilities": map[string]any{"experimentalApi": true, "requestAttestation": false},
	}, &initRes); cerr != nil {
		_ = conn.Close()
		return nil, nil, cerr
	}
	if nerr := conn.Notify(ctx, "initialized", map[string]any{}); nerr != nil {
		_ = conn.Close()
		return nil, nil, nerr
	}
	// The running app-server reports its own spawn-bound user agent. ContextGuard
	// parses the semantic version from this value instead of re-probing whatever
	// `codex` binary happens to be on the daemon's current PATH. Older/object-shaped
	// replies remain valid backends but leave the optional guard unsupported.
	var initialized struct {
		UserAgent json.RawMessage `json:"userAgent"`
	}
	if json.Unmarshal(initRes, &initialized) == nil {
		feed.userAgent = parseContextGuardUserAgent(initialized.UserAgent)
	}
	return conn, feed, nil
}

func parseContextGuardUserAgent(raw json.RawMessage) string {
	var userAgent string
	if json.Unmarshal(raw, &userAgent) != nil {
		return ""
	}
	return userAgent
}

// watchSessionBackend turns the connection's own end into §R7.7 case 3. The backend died
// mid-session: the session ends (the SHIM fires that, from its own edge) and history must be
// honest about the tail it never captured.
func (d *Daemon) watchSessionBackend(id string, conn *appserver.Client, feedEpoch string) {
	<-conn.Done()
	d.noteBackendLostForFeed(id, feedEpoch, "the app-server connection closed")
}

// rebuildFrame reassembles the verbatim JSON-RPC frame the pump and the adapter both expect.
// The client hands its callbacks the decoded members rather than the bytes, and the WHOLE
// FRAME is what ADR-013 §R7.3 puts in HookPayload.Raw -- which is what makes
// r1-codex-fixtures/frame-samples.json the golden vector set for the shaper.
func rebuildFrame(method string, id, params json.RawMessage) []byte {
	frame := map[string]json.RawMessage{}
	name, err := json.Marshal(method)
	if err != nil {
		return nil
	}
	frame["method"] = name
	if len(id) > 0 {
		frame["id"] = id
	}
	if len(params) > 0 {
		frame["params"] = params
	}
	out, err := json.Marshal(frame)
	if err != nil {
		return nil
	}
	return out
}
