package skeleton

// These are composition tests for daemon-restart rejoin. They drive the real
// rejoinSessionBackend through a real Unix socket and WebSocket JSON-RPC peer. The
// persisted conversation id is the only durable fact tying a Swarm session to the
// provider thread its terminal owns; thread/loaded/list is only an inventory and may
// legitimately contain other loaded threads.

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/codex"
	"github.com/Nathandela/swarm/internal/appserver"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/coder/websocket"
)

const (
	rejoinPersistedTarget = "01a00339-a80e-72a0-966f-116427b6b9ce"
	rejoinUnrelatedThread = "01a00999-dead-beef-0000-000000000000"
)

type persistedRejoinServer struct {
	sock string

	mu          sync.Mutex
	loaded      []string
	resumed     []string
	resumeGate  chan struct{}
	resumeStart chan struct{}
	resumeReply *string
	startOnce   sync.Once
	gateOnce    sync.Once
}

func newPersistedRejoinServer(t *testing.T, loaded ...string) *persistedRejoinServer {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "swrejoin-")
	if err != nil {
		t.Fatalf("short fake app-server dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	s := &persistedRejoinServer{
		sock:        filepath.Join(dir, "codex.sock"),
		loaded:      append([]string(nil), loaded...),
		resumeStart: make(chan struct{}),
	}
	ln, err := net.Listen("unix", s.sock)
	if err != nil {
		t.Fatalf("bind fake app-server: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/rpc", func(w http.ResponseWriter, r *http.Request) {
		ws, acceptErr := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if acceptErr != nil {
			return
		}
		defer func() { _ = ws.CloseNow() }()
		for {
			_, body, readErr := ws.Read(context.Background())
			if readErr != nil {
				return
			}
			s.handle(ws, body)
		}
	})
	httpServer := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = httpServer.Serve(ln) }()
	t.Cleanup(func() {
		s.releaseResume()
		_ = httpServer.Close()
	})
	return s
}

func (s *persistedRejoinServer) handle(ws *websocket.Conn, body []byte) {
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if json.Unmarshal(body, &req) != nil {
		return
	}
	switch req.Method {
	case "initialize":
		s.reply(ws, req.ID, map[string]any{"userAgent": map[string]any{"name": "codex"}})
	case "thread/loaded/list":
		s.mu.Lock()
		loaded := append([]string(nil), s.loaded...)
		s.mu.Unlock()
		s.reply(ws, req.ID, map[string]any{"data": loaded})
	case "thread/resume":
		var params struct {
			ThreadID string `json:"threadId"`
		}
		if json.Unmarshal(req.Params, &params) != nil {
			return
		}
		s.mu.Lock()
		s.resumed = append(s.resumed, params.ThreadID)
		gate := s.resumeGate
		s.mu.Unlock()
		s.startOnce.Do(func() { close(s.resumeStart) })
		if gate != nil {
			<-gate
		}
		s.reply(ws, req.ID, map[string]any{
			"thread": map[string]any{"id": s.responseThreadID(params.ThreadID), "preview": "existing work"},
		})
	default:
		if len(req.ID) != 0 {
			s.reply(ws, req.ID, map[string]any{})
		}
	}
}

func (s *persistedRejoinServer) responseThreadID(requested string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resumeReply != nil {
		return *s.resumeReply
	}
	return requested
}

func (s *persistedRejoinServer) respondToResumeAs(id string) {
	s.mu.Lock()
	s.resumeReply = &id
	s.mu.Unlock()
}

func (s *persistedRejoinServer) reply(ws *websocket.Conn, id json.RawMessage, result any) {
	if len(id) == 0 {
		return
	}
	body, err := json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  any             `json:"result"`
	}{JSONRPC: "2.0", ID: id, Result: result})
	if err != nil {
		return
	}
	_ = ws.Write(context.Background(), websocket.MessageText, body)
}

func (s *persistedRejoinServer) blockResume() {
	s.mu.Lock()
	s.resumeGate = make(chan struct{})
	s.mu.Unlock()
}

func (s *persistedRejoinServer) releaseResume() {
	s.mu.Lock()
	gate := s.resumeGate
	s.mu.Unlock()
	if gate != nil {
		s.gateOnce.Do(func() { close(gate) })
	}
}

func (s *persistedRejoinServer) resumedThreads() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.resumed...)
}

type persistedRejoinRig struct {
	sk    *Daemon
	srv   *persistedRejoinServer
	local string
}

type missingRolloutCountingConn struct {
	mu    sync.Mutex
	calls int
	seen  chan int
}

func newMissingRolloutCountingConn() *missingRolloutCountingConn {
	return &missingRolloutCountingConn{seen: make(chan int, 8)}
}

func (c *missingRolloutCountingConn) Call(_ context.Context, method string, _, _ any) error {
	if method != "thread/resume" {
		return nil
	}
	c.mu.Lock()
	c.calls++
	n := c.calls
	c.mu.Unlock()
	select {
	case c.seen <- n:
	default:
	}
	return &appserver.RPCError{Code: -32600, Message: "no rollout found for thread id"}
}

func (c *missingRolloutCountingConn) Respond(context.Context, json.RawMessage, any) error {
	return nil
}

func (c *missingRolloutCountingConn) Close() error { return nil }

func newPersistedRejoinRig(t *testing.T, loaded ...string) *persistedRejoinRig {
	t.Helper()
	sk := assemble(t)
	sk.backendReady = 3 * time.Second
	ad := &r7CodexAdapter{Adapter: newPlainAdapter().Adapter}
	sk.setAdapterForTest(func(string) (adapter.Adapter, bool) { return ad, true })
	m := launchFake(t, sk, r7StdinScript)
	return &persistedRejoinRig{
		sk: sk, srv: newPersistedRejoinServer(t, loaded...), local: m.ID,
	}
}

func (r *persistedRejoinRig) persistConversation(t *testing.T, id string) {
	t.Helper()
	if err := r.sk.core.SetConversationID(r.local, id); err != nil {
		t.Fatalf("persist conversation id: %v", err)
	}
	m, ok := r.sk.core.Get(r.local)
	if !ok || m.ConversationID != id {
		t.Fatalf("persisted ConversationID = %q (session found=%t), want %q", m.ConversationID, ok, id)
	}
}

func (r *persistedRejoinRig) rejoin() {
	r.sk.rejoinSessionBackend(r.local, daemon.BackendChannel{SocketPath: r.srv.sock})
}

// A loaded list is not an ownership proof. When it contains multiple valid threads,
// the durable canonical id must select the exact conversation this Swarm session owns.
func TestRejoinPersistedIdentity_SelectsExactConversationFromSeveralLoadedThreads(t *testing.T) {
	r := newPersistedRejoinRig(t, rejoinUnrelatedThread, rejoinPersistedTarget)
	r.persistConversation(t, rejoinPersistedTarget)
	beforeGaps := countJournalStructuredGaps(t, r.sk, r.local)

	r.rejoin()

	if got := r.srv.resumedThreads(); len(got) != 1 || got[0] != rejoinPersistedTarget {
		t.Fatalf("thread/resume targets = %v, want exactly persisted conversation %q", got, rejoinPersistedTarget)
	}
	backend, ok := r.sk.sessionBackendFor(r.local)
	if !ok || backend.threadID != rejoinPersistedTarget {
		t.Fatalf("registered backend = (%v, %t), want thread %q", backend, ok, rejoinPersistedTarget)
	}
	if after := countJournalStructuredGaps(t, r.sk, r.local); after != beforeGaps {
		t.Fatalf("successful exact rejoin emitted %d new structured gap(s), want none", after-beforeGaps)
	}
}

func TestRejoinPersistedIdentity_LegacySingleThreadStillRejoinsAndPersistsIdentity(t *testing.T) {
	r := newPersistedRejoinRig(t, rejoinPersistedTarget)

	r.rejoin()

	backend, ok := r.sk.sessionBackendFor(r.local)
	if !ok || backend.threadID != rejoinPersistedTarget {
		t.Fatalf("legacy single-thread backend = (%v, %t), want thread %q", backend, ok, rejoinPersistedTarget)
	}
	m, ok := r.sk.core.Get(r.local)
	if !ok || m.ConversationID != rejoinPersistedTarget {
		t.Fatalf("legacy rejoin persisted ConversationID = %q (found=%t), want %q", m.ConversationID, ok, rejoinPersistedTarget)
	}
}

// The legacy selector observes an empty identity before registration, but another authenticated
// producer may win the write-once metadata latch before adoption. The in-memory adopted map is
// not proof that the durable id was stored: SetConversationID deliberately returns nil when an
// already-populated value makes the write a no-op.
func TestAdoptRejoinedThreadForInstance_DurableIdentityConflictFailsClosed(t *testing.T) {
	r := newPersistedRejoinRig(t, rejoinPersistedTarget)
	instance, ok := r.sk.sessionInstance(r.local)
	if !ok {
		t.Fatal("launched session has no instance")
	}
	r.persistConversation(t, rejoinUnrelatedThread)

	if r.sk.adoptRejoinedThreadForInstance(r.local, instance, rejoinPersistedTarget) {
		t.Fatal("legacy adoption reported success although durable metadata already names another thread")
	}
	m, ok := r.sk.core.Get(r.local)
	if !ok || m.ConversationID != rejoinUnrelatedThread {
		t.Fatalf("durable identity after conflict = %q (found=%t), want existing %q", m.ConversationID, ok, rejoinUnrelatedThread)
	}
}

// Registration's first instance check cannot authorize a later map write. If replacement and
// its successor install while the stale attempt is paused at that boundary, the stale attempt
// must neither overwrite the successor nor delete it from the rollback arm.
func TestRegisterBackendForInstance_StaleCannotOverwriteOrDeleteSuccessor(t *testing.T) {
	r := newPersistedRejoinRig(t, rejoinPersistedTarget)
	oldInstance, ok := r.sk.sessionInstance(r.local)
	if !ok {
		t.Fatal("launched session has no instance")
	}
	oldReachedInstall := make(chan struct{})
	releaseOld := make(chan struct{})
	var signalOnce sync.Once
	r.sk.backend.beforeRegisterInstall = func(_ string, expectedInstance string) {
		if expectedInstance != oldInstance {
			return
		}
		signalOnce.Do(func() { close(oldReachedInstall) })
		<-releaseOld
	}

	staleResult := make(chan bool, 1)
	go func() {
		staleResult <- r.sk.registerBackendFeedForInstance(
			r.local, oldInstance, rejoinPersistedTarget, newR7FakeBackend(), newBackendFeed())
	}()
	select {
	case <-oldReachedInstall:
	case <-time.After(5 * time.Second):
		t.Fatal("stale registration never reached the install boundary")
	}

	m, ok := r.sk.core.Get(r.local)
	if !ok {
		t.Fatal("launched session disappeared")
	}
	replacement := mintSessionInstance()
	if err := r.sk.recordSessionInstance(r.local, replacement, m.ShimPID, m.ShimStartTime); err != nil {
		t.Fatalf("replace session instance: %v", err)
	}
	successorConn := newR7FakeBackend()
	successorFeed := newBackendFeed()
	if !r.sk.registerBackendFeedForInstance(
		r.local, replacement, rejoinPersistedTarget, successorConn, successorFeed) {
		t.Fatal("successor registration was refused")
	}

	close(releaseOld)
	select {
	case accepted := <-staleResult:
		if accepted {
			t.Fatal("stale registration was accepted after replacement")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stale registration did not finish")
	}

	backend, ok := r.sk.sessionBackendFor(r.local)
	if !ok || backend.conn != successorConn || backend.feed != successorFeed || backend.sessionInstance != replacement {
		t.Fatalf("live backend after stale rollback = (%+v, %t), want untouched successor for %q", backend, ok, replacement)
	}
}

// Replacing one physical app-server feed on the same session instance transfers ownership of
// every connection-scoped resource. The old socket, ContextGuard feed and JSON-RPC request ids
// cannot remain attached to the session id: request ids are meaningful only on the connection
// that received them, so carrying one into the successor can answer an unrelated request there.
func TestRegisterBackendFeedForInstance_SameInstanceReplacementRetiresOldGeneration(t *testing.T) {
	sk := assemble(t)
	sk.setAdapterForTest(func(string) (adapter.Adapter, bool) { return codex.New(), true })
	m := launchFake(t, sk, r7StdinScript)
	instance, ok := sk.sessionInstance(m.ID)
	if !ok {
		t.Fatal("launched session has no instance")
	}

	oldConn := newR7FakeBackend()
	oldFeed := newBackendFeed()
	oldFeed.userAgent = "swarm-contextguard-probe/0.150.1 (Mac OS; arm64)"
	if !sk.registerBackendFeedForInstance(m.ID, instance, rejoinPersistedTarget, oldConn, oldFeed) {
		t.Fatal("old feed registration was refused")
	}
	sk.contextGuards.mu.Lock()
	oldGuard := sk.contextGuards.sessions[m.ID]
	sk.contextGuards.mu.Unlock()
	if oldGuard == nil || oldGuard.key.BackendEpoch != oldFeed.epoch {
		t.Fatalf("control: old ContextGuard = %+v, want feed %q", oldGuard, oldFeed.epoch)
	}
	const oldRef = "old-feed-approval"
	oldID := json.RawMessage(`701`)
	sk.noteServerRequest(m.ID, oldRef, oldID)

	successorConn := newR7FakeBackend()
	successorFeed := newBackendFeed()
	successorFeed.userAgent = "swarm-contextguard-probe/0.150.1 (Mac OS; arm64)"
	if !sk.registerBackendFeedForInstance(
		m.ID, instance, rejoinPersistedTarget, successorConn, successorFeed,
	) {
		t.Fatal("same-instance successor registration was refused")
	}

	backend, live := sk.sessionBackendFor(m.ID)
	if !live || backend.conn != successorConn || backend.feed != successorFeed ||
		backend.sessionInstance != instance {
		t.Fatalf("live backend after replacement = (%+v, %t), want exact successor", backend, live)
	}
	if got := oldConn.closes(); got != 1 {
		t.Errorf("old connection close count after replacement = %d, want exactly 1", got)
	}
	select {
	case <-oldGuard.done:
		// Desired: successor ContextGuard registration retired the exact old worker.
	default:
		t.Error("old ContextGuard worker remained live after successor registration")
	}
	sk.contextGuards.mu.Lock()
	currentGuard := sk.contextGuards.sessions[m.ID]
	sk.contextGuards.mu.Unlock()
	if currentGuard == nil || currentGuard.key.BackendEpoch != successorFeed.epoch {
		t.Errorf("current ContextGuard = %+v, want successor feed %q", currentGuard, successorFeed.epoch)
	}
	oldFeed.guardMu.Lock()
	oldDiscarded := oldFeed.guardDiscarded
	oldFeed.guardMu.Unlock()
	if !oldDiscarded {
		t.Error("replaced ContextGuard feed was not discarded")
	}

	// Drive the actual native-decision path. A stale id surviving in requests would be sent
	// through sessionBackendFor -- now the successor -- despite belonging to oldConn.
	err, handled := sk.applyNativeDecision(m.ID, oldRef, "accept")
	if !handled || err == nil {
		t.Errorf("old request routed after replacement: handled=%t err=%v; want native refusal", handled, err)
	}
	if id, _, responded := successorConn.lastResponse(); responded {
		t.Errorf("successor answered old connection's JSON-RPC id %s", id)
	}
	if ref, found := sk.serverRequestRef(m.ID, oldID); found {
		t.Errorf("old byID entry survived replacement and resolved as %q", ref)
	}

	// The callback closure in dialSessionBackend captures the old feed and enters the scoped
	// pump boundary. Reproduce that closure after replacement: retirement must reject a
	// request arriving late from the old read loop, rather than repopulating the clean maps.
	const staleRef = "late-old-feed-approval"
	staleID := json.RawMessage(`702`)
	staleFrame := rebuildFrame("item/fileChange/requestApproval", staleID,
		json.RawMessage(`{"itemId":"`+staleRef+`"}`))
	sk.ingestBackendFrameForFeed(m.ID, instance, oldFeed,
		"item/fileChange/requestApproval", staleFrame, time.Now())
	err, handled = sk.applyNativeDecision(m.ID, staleRef, "accept")
	if !handled || err == nil {
		t.Errorf("late old-feed callback routed after replacement: handled=%t err=%v; want native refusal", handled, err)
	}
	if id, _, responded := successorConn.lastResponse(); responded {
		t.Errorf("successor answered a late old-feed callback's JSON-RPC id %s", id)
	}
	backend, live = sk.sessionBackendFor(m.ID)
	if !live || backend.conn != successorConn || backend.feed != successorFeed {
		t.Fatalf("old cleanup/callback removed successor backend: (%+v, %t)", backend, live)
	}
}

const noteUnavailableLegacyDeadlockChild = "SWARM_NOTE_UNAVAILABLE_LEGACY_DEADLOCK_CHILD"

// noteBackendUnavailableForInstance takes the ContextGuard replacement fence before it degrades
// capabilities. Capability input migration may need recordSessionInstance for a pid-only legacy
// side-file, which takes the same non-reentrant mutex. Run the exact production path in a child
// process so the regression is time-bounded even while the broken implementation deadlocks.
func TestNoteBackendUnavailableForInstance_LegacyPIDOnlyMigrationDoesNotDeadlock(t *testing.T) {
	if readyPath := os.Getenv(noteUnavailableLegacyDeadlockChild); readyPath != "" {
		r := newPersistedRejoinRig(t, rejoinPersistedTarget)
		m, ok := r.sk.core.Get(r.local)
		if !ok || m.ShimPID == 0 || m.ShimStartTime == 0 {
			t.Fatalf("child control: modern shim metadata = %+v, found=%t", m, ok)
		}
		const legacyInstance = "0123456789abcdef0123456789abcdef"
		r.sk.capStore.mu.Lock()
		instancePath := r.sk.sessionStatePathLocked(r.local, sessionInstanceFile)
		delete(r.sk.capStore.instances, r.local)
		delete(r.sk.capStore.incarnations, r.local)
		r.sk.capStore.mu.Unlock()
		if err := os.WriteFile(instancePath,
			[]byte(legacyInstance+" "+strconv.Itoa(m.ShimPID)), 0o600); err != nil {
			t.Fatalf("plant pid-only legacy instance: %v", err)
		}
		expected, ok := r.sk.sessionInstance(r.local)
		if !ok || expected != legacyInstance {
			t.Fatalf("child control: loaded instance = %q, %t; want legacy %q", expected, ok, legacyInstance)
		}
		if err := os.WriteFile(readyPath, []byte("ready"), 0o600); err != nil {
			t.Fatalf("signal child readiness: %v", err)
		}
		r.sk.noteBackendUnavailableForInstance(r.local, expected)
		return
	}

	readyPath := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command(os.Args[0],
		"-test.run=^TestNoteBackendUnavailableForInstance_LegacyPIDOnlyMigrationDoesNotDeadlock$",
		"-test.count=1")
	cmd.Env = append(os.Environ(), noteUnavailableLegacyDeadlockChild+"="+readyPath)
	type childResult struct {
		out []byte
		err error
	}
	result := make(chan childResult, 1)
	go func() {
		out, err := cmd.CombinedOutput()
		result <- childResult{out: out, err: err}
	}()

	setupDeadline := time.NewTimer(30 * time.Second)
	defer setupDeadline.Stop()
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	ready := false
	for !ready {
		select {
		case got := <-result:
			// A successful child can reach the call and exit between readiness polls.
			// Its durable marker still proves that setup reached the production path.
			if _, err := os.Stat(readyPath); err != nil {
				t.Fatalf("deadlock child exited before reaching the production call: %v\n%s", got.err, got.out)
			}
			if got.err != nil {
				t.Fatalf("legacy migration production path failed: %v\n%s", got.err, got.out)
			}
			return
		case <-setupDeadline.C:
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			got := <-result
			t.Fatalf("deadlock child never became ready: %v\n%s", got.err, got.out)
		case <-poll.C:
			_, err := os.Stat(readyPath)
			ready = err == nil
		}
	}

	select {
	case got := <-result:
		if got.err != nil {
			t.Fatalf("legacy migration production path failed: %v\n%s", got.err, got.out)
		}
	case <-time.After(3 * time.Second):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		got := <-result
		t.Fatalf("noteBackendUnavailableForInstance deadlocked while migrating a pid-only instance: %v\n%s",
			got.err, got.out)
	}
}

// Removing an exact failed feed and publishing its loss must be one fenced lifecycle
// transition. Otherwise a successor can become authoritative in the interval and the old
// watcher's finishBackendLost can withdraw the successor's capability and scar its history.
func TestNoteBackendLostForFeed_RemovedOldFeedCannotDegradeRegisteredSuccessor(t *testing.T) {
	r := newPersistedRejoinRig(t, rejoinPersistedTarget)
	oldInstance, ok := r.sk.sessionInstance(r.local)
	if !ok {
		t.Fatal("launched session has no instance")
	}
	oldFeed := newBackendFeed()
	if !r.sk.registerBackendFeedForInstance(
		r.local, oldInstance, rejoinPersistedTarget, newR7FakeBackend(), oldFeed,
	) {
		t.Fatal("control: old feed registration was refused")
	}
	awaitSessionCapability(t, r.sk, r.local, true)

	removed := make(chan struct{})
	releaseLoss := make(chan struct{})
	var pauseOnce sync.Once
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseLoss) }) })
	r.sk.backend.afterFeedRemovalBeforeLoss = func(local, expectedEpoch string) {
		if local != r.local || expectedEpoch != oldFeed.epoch {
			return
		}
		pauseOnce.Do(func() { close(removed) })
		<-releaseLoss
	}
	lostDone := make(chan struct{})
	go func() {
		defer close(lostDone)
		r.sk.noteBackendLostForFeed(r.local, oldFeed.epoch, "old feed failed")
	}()
	select {
	case <-removed:
	case <-time.After(5 * time.Second):
		t.Fatal("old feed loss never paused after exact removal")
	}

	m, ok := r.sk.core.Get(r.local)
	if !ok {
		t.Fatal("launched session disappeared")
	}
	replacement := mintSessionInstance()
	if err := r.sk.recordSessionInstance(
		r.local, replacement, m.ShimPID, m.ShimStartTime,
	); err != nil {
		t.Fatalf("replace session instance: %v", err)
	}
	successorConn := newR7FakeBackend()
	successorFeed := newBackendFeed()
	if !r.sk.registerBackendFeedForInstance(
		r.local, replacement, rejoinPersistedTarget, successorConn, successorFeed,
	) {
		t.Fatal("successor registration was refused")
	}
	got := awaitSessionCapability(t, r.sk, r.local, true)
	if got.SessionInstance != replacement {
		t.Fatalf("control: successor capability belongs to %q, want %q", got.SessionInstance, replacement)
	}
	beforeGaps := countJournalStructuredGaps(t, r.sk, r.local)

	releaseOnce.Do(func() { close(releaseLoss) })
	select {
	case <-lostDone:
	case <-time.After(5 * time.Second):
		t.Fatal("old feed loss did not finish")
	}

	backend, live := r.sk.sessionBackendFor(r.local)
	if !live || backend.conn != successorConn || backend.feed != successorFeed ||
		backend.sessionInstance != replacement {
		t.Fatalf("successor backend after old loss = (%+v, %t), want untouched replacement", backend, live)
	}
	got, ok = r.sk.sessionCapabilities(r.local)
	if !ok || got.SessionInstance != replacement || !got.StructuredChat ||
		got.TerminalFallback || got.TerminalControl {
		t.Fatalf("successor capability after old loss = (%+v, %t), want structured chat live", got, ok)
	}
	if r.sk.sessionDegraded(r.local) {
		t.Fatal("old feed loss degraded the already-registered successor")
	}
	if after := countJournalStructuredGaps(t, r.sk, r.local); after != beforeGaps {
		t.Fatalf("old feed loss emitted %d gap(s) into successor history, want none", after-beforeGaps)
	}
}

// A missing-rollout retry belongs to one physical feed, not merely to a session id. Once a
// successor replaces that feed, the old retry loop must return without issuing another RPC on
// the obsolete connection.
func TestSubscribeSessionThread_ObsoleteMissingRolloutFeedStopsWhenReplaced(t *testing.T) {
	r := newPersistedRejoinRig(t, rejoinPersistedTarget)
	instance, ok := r.sk.sessionInstance(r.local)
	if !ok {
		t.Fatal("launched session has no instance")
	}
	oldConn := newMissingRolloutCountingConn()
	oldFeed := newBackendFeed()
	if !r.sk.registerBackendFeedForInstance(
		r.local, instance, rejoinPersistedTarget, oldConn, oldFeed,
	) {
		t.Fatal("control: old feed registration was refused")
	}
	subscriptionDone := make(chan struct{})
	go func() {
		defer close(subscriptionDone)
		r.sk.subscribeSessionThread(r.local, oldConn, rejoinPersistedTarget, oldFeed.epoch)
	}()
	select {
	case n := <-oldConn.seen:
		if n != 1 {
			t.Fatalf("first old-feed resume call = %d, want 1", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("old feed never attempted its first missing-rollout resume")
	}

	successorConn := newR7FakeBackend()
	successorFeed := newBackendFeed()
	if !r.sk.registerBackendFeedForInstance(
		r.local, instance, rejoinPersistedTarget, successorConn, successorFeed,
	) {
		t.Fatal("successor feed registration was refused")
	}

	select {
	case <-subscriptionDone:
		// Desired: replacement, rather than another old-connection RPC, ended the loop.
	case n := <-oldConn.seen:
		// Ensure the current broken loop can terminate before failing the assertion, so this
		// RED test does not intentionally strand a goroutine in the broader suite.
		r.sk.forgetBackendForFeed(r.local, successorFeed.epoch)
		select {
		case <-subscriptionDone:
		case <-time.After(2 * time.Second):
			t.Fatal("obsolete subscription did not stop after successor cleanup")
		}
		t.Fatalf("obsolete feed issued resume call %d after replacement, want loop exit", n)
	case <-time.After(2 * time.Second):
		r.sk.forgetBackendForFeed(r.local, successorFeed.epoch)
		t.Fatal("obsolete subscription neither exited nor retried")
	}

	backend, live := r.sk.sessionBackendFor(r.local)
	if !live || backend.conn != successorConn || backend.feed != successorFeed {
		t.Fatalf("successor backend after old subscription exit = (%+v, %t), want untouched successor", backend, live)
	}
}

func TestRejoinPersistedIdentity_ResumeResponseMustConfirmTheSelectedThread(t *testing.T) {
	for _, reply := range []string{"", rejoinUnrelatedThread} {
		t.Run(reply, func(t *testing.T) {
			r := newPersistedRejoinRig(t, rejoinPersistedTarget)
			r.persistConversation(t, rejoinPersistedTarget)
			r.srv.respondToResumeAs(reply)

			r.rejoin()

			if backend, ok := r.sk.sessionBackendFor(r.local); ok {
				t.Fatalf("unconfirmed resume identity registered backend %+v", backend)
			}
			if !r.sk.sessionDegraded(r.local) {
				t.Fatal("unconfirmed resume identity did not fail closed")
			}
			if gaps := countJournalStructuredGaps(t, r.sk, r.local); gaps != 1 {
				t.Fatalf("unconfirmed resume identity emitted %d gaps, want one", gaps)
			}
		})
	}
}

// A visible history marker is permanent, but it is not a permanent send ban. The
// initialized current-instance backend is the existing machine-local proof that may
// restore structured chat for future messages without erasing or repeating the gap.
func TestRejoinPersistedIdentity_RecoversDegradedCurrentInstanceWithoutAnotherGap(t *testing.T) {
	r := newPersistedRejoinRig(t, rejoinUnrelatedThread, rejoinPersistedTarget)
	r.persistConversation(t, rejoinPersistedTarget)
	instance, ok := r.sk.sessionInstance(r.local)
	if !ok {
		t.Fatal("launched session has no instance")
	}
	old := newR7FakeBackend()
	if !r.sk.registerBackendForInstance(r.local, instance, rejoinPersistedTarget, old) {
		t.Fatal("control: initial current-instance sink was refused")
	}
	awaitSessionCapability(t, r.sk, r.local, true)
	r.sk.markSessionDegraded(r.local)
	awaitSessionCapability(t, r.sk, r.local, false)
	r.sk.forgetBackend(r.local)
	beforeGaps := countJournalStructuredGaps(t, r.sk, r.local)

	r.rejoin()

	got := awaitSessionCapability(t, r.sk, r.local, true)
	if got.SessionInstance != instance || got.TerminalFallback || got.TerminalControl {
		t.Fatalf("recovered capability = %+v, want structured chat only for instance %q", got, instance)
	}
	if !r.sk.sessionDegraded(r.local) {
		t.Fatal("successful sink recovery erased the durable visible history marker")
	}
	if after := countJournalStructuredGaps(t, r.sk, r.local); after != beforeGaps {
		t.Fatalf("exact rejoin repeated an already-visible boundary: gaps %d -> %d", beforeGaps, after)
	}
	if got := r.srv.resumedThreads(); len(got) != 1 || got[0] != rejoinPersistedTarget {
		t.Fatalf("recovery resumed %v, want exact persisted conversation %q", got, rejoinPersistedTarget)
	}
}

// A durable gap belongs to the conversation's visible history, but its raw capability record
// belongs to one exact session instance. Replacement must author its own record before committing
// its exact initialized-backend proof; treating any old raw record as sufficient leaves the new
// instance permanently unable to recover structured chat.
func TestRegisterBackendForInstance_DegradedOldRecordDoesNotBlockReplacementRecovery(t *testing.T) {
	r := newPersistedRejoinRig(t, rejoinPersistedTarget)
	oldInstance, ok := r.sk.sessionInstance(r.local)
	if !ok {
		t.Fatal("launched session has no instance")
	}
	if !r.sk.registerBackendForInstance(r.local, oldInstance, rejoinPersistedTarget, newR7FakeBackend()) {
		t.Fatal("control: old-instance backend registration was refused")
	}
	awaitSessionCapability(t, r.sk, r.local, true)
	r.sk.markSessionDegraded(r.local)
	awaitSessionCapability(t, r.sk, r.local, false)
	r.sk.forgetBackend(r.local)

	m, ok := r.sk.core.Get(r.local)
	if !ok {
		t.Fatal("launched session disappeared")
	}
	replacement := mintSessionInstance()
	if err := r.sk.recordSessionInstance(
		r.local, replacement, m.ShimPID, m.ShimStartTime,
	); err != nil {
		t.Fatalf("replace session instance: %v", err)
	}
	if !r.sk.registerBackendForInstance(
		r.local, replacement, rejoinPersistedTarget, newR7FakeBackend(),
	) {
		t.Fatal("replacement's exact initialized backend was refused")
	}

	got, ok := r.sk.sessionCapabilities(r.local)
	if !ok {
		t.Fatal("replacement backend authored no capability record")
	}
	if got.SessionInstance != replacement || !got.StructuredChat ||
		got.TerminalFallback || got.TerminalControl {
		t.Fatalf(
			"replacement capability = %+v, want structured-chat-only authority for current instance %q; "+
				"an old degraded raw record must not suppress replacement authoring",
			got, replacement,
		)
	}
}

// A changed instance is stale work and remains silent; a changed identity on the SAME instance is
// different. It proves this rejoin selected a thread the durable owner no longer names, so the
// current session must fail closed visibly rather than retain structured_chat with no backend.
func TestRejoinPersistedIdentity_SameInstanceConflictDuringResumeFailsClosed(t *testing.T) {
	r := newPersistedRejoinRig(t, rejoinPersistedTarget)
	instance, ok := r.sk.sessionInstance(r.local)
	if !ok {
		t.Fatal("launched session has no instance")
	}
	if !r.sk.registerBackendForInstance(
		r.local, instance, rejoinPersistedTarget, newR7FakeBackend(),
	) {
		t.Fatal("control: pre-restart backend registration was refused")
	}
	awaitSessionCapability(t, r.sk, r.local, true)
	r.sk.forgetBackend(r.local)
	beforeGaps := countJournalStructuredGaps(t, r.sk, r.local)

	r.srv.blockResume()
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.rejoin()
	}()
	select {
	case <-r.srv.resumeStart:
	case <-time.After(5 * time.Second):
		t.Fatal("legacy rejoin never reached thread/resume")
	}
	// No replacement occurred: only this instance's write-once durable identity changed while
	// the resume response was outstanding.
	r.persistConversation(t, rejoinUnrelatedThread)
	r.srv.releaseResume()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("identity-conflicted rejoin did not return")
	}

	if backend, live := r.sk.sessionBackendFor(r.local); live {
		t.Fatalf("identity-conflicted rejoin registered backend %+v", backend)
	}
	if !r.sk.sessionDegraded(r.local) {
		t.Fatal("same-instance identity conflict left structured chat authoritative with no backend")
	}
	if after := countJournalStructuredGaps(t, r.sk, r.local); after != beforeGaps+1 {
		t.Fatalf("same-instance identity conflict emitted %d gap(s), want exactly one", after-beforeGaps)
	}
	got, ok := r.sk.sessionCapabilities(r.local)
	if !ok || got.SessionInstance != instance || got.StructuredChat ||
		got.TerminalFallback || got.TerminalControl {
		t.Fatalf("capability after same-instance conflict = (%+v, %t), want fail-closed instance %q", got, ok, instance)
	}
}

// Ambiguity remains fail-closed when there is no trustworthy durable selector.
// This includes corrupt/legacy metadata: an arbitrary stored token is not authority
// to choose one member of a multi-thread inventory.
func TestRejoinPersistedIdentity_AmbiguousInventoryWithoutCanonicalSelectorFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name      string
		persisted string
	}{
		{name: "missing persisted identity"},
		{name: "invalid persisted identity", persisted: "thread-not-a-canonical-id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newPersistedRejoinRig(t, rejoinUnrelatedThread, rejoinPersistedTarget)
			if tc.persisted != "" {
				// The fake provider deliberately allows an uncharacterized legacy value
				// into Meta. Rejoin must validate the selector independently before use.
				r.persistConversation(t, tc.persisted)
			}

			r.rejoin()

			if got := r.srv.resumedThreads(); len(got) != 0 {
				t.Fatalf("ambiguous inventory dispatched thread/resume to %v; want no guess", got)
			}
			if backend, ok := r.sk.sessionBackendFor(r.local); ok {
				t.Fatalf("ambiguous inventory registered backend %+v", backend)
			}
			if !r.sk.sessionDegraded(r.local) {
				t.Fatal("ambiguous rejoin failure was not surfaced as unavailable")
			}
			if gaps := countJournalStructuredGaps(t, r.sk, r.local); gaps != 1 {
				t.Fatalf("ambiguous rejoin emitted %d structured gaps, want one disclosed failure", gaps)
			}
		})
	}
}

// The instance captured before dialing owns every later outcome. If replacement wins
// while thread/resume is in flight, the stale attempt may neither install its socket nor
// report backend_unavailable against the replacement.
func TestRejoinPersistedIdentity_ReplacementDuringResumeCannotTouchReplacement(t *testing.T) {
	r := newPersistedRejoinRig(t, rejoinPersistedTarget)
	r.persistConversation(t, rejoinPersistedTarget)
	r.srv.blockResume()
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.rejoin()
	}()
	select {
	case <-r.srv.resumeStart:
	case <-time.After(5 * time.Second):
		t.Fatal("rejoin never reached thread/resume")
	}

	replacement := mintSessionInstance()
	if err := r.sk.recordSessionInstance(r.local, replacement, 999999); err != nil {
		t.Fatalf("replace session instance: %v", err)
	}
	r.srv.releaseResume()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stale rejoin did not return after resume completed")
	}

	if backend, ok := r.sk.sessionBackendFor(r.local); ok {
		t.Fatalf("old instance registered a backend on the replacement: %+v", backend)
	}
	if r.sk.sessionDegraded(r.local) {
		t.Fatal("old instance's rejected rejoin degraded the replacement")
	}
	if gaps := countJournalStructuredGaps(t, r.sk, r.local); gaps != 0 {
		t.Fatalf("old instance's rejected rejoin emitted %d gap(s) against the replacement", gaps)
	}
}
