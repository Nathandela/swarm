package skeleton

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/protocol"
)

func TestCodexManagerCallbackNeverWaitsForFeedLock(t *testing.T) {
	feed := newBackendFeed()
	d := &Daemon{}
	b := codexProofFixture()
	feed.manager.mu.Lock()
	done := make(chan struct{})
	go func() {
		d.captureCodexManagerFrame(b.Session, feed, "thread/status/changed", codexManagerFrame("thread/status/changed", b.NativeSessionID, map[string]any{"status": map[string]any{"type": "idle"}}))
		close(done)
	}()
	select {
	case <-done:
		feed.manager.mu.Unlock()
	case <-time.After(100 * time.Millisecond):
		feed.manager.mu.Unlock()
		<-done
		t.Fatal("native reader waited for the manager feed lock")
	}
}

func TestCodexManagerPendingCustodyBlocksAuthorityWithoutBlockingReader(t *testing.T) {
	sk, b, feed := newCodexManagerSendRig(t)
	r, err := sk.codexManagerRegistry(b.Session, b)
	if err != nil {
		t.Fatal(err)
	}
	c := feed.manager.actor.Load()
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	if err := c.Enqueue(0, func(ctx context.Context) error {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	<-entered
	done := make(chan struct{})
	go func() {
		sk.captureCodexManagerFrame(b.Session, feed, "thread/status/changed", codexManagerFrame("thread/status/changed", b.NativeSessionID, map[string]any{"status": map[string]any{"type": "idle"}}))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("native reader waited for blocked custody IO")
	}
	if r.ReadyForInput() {
		t.Fatal("pending native evidence was reported ready")
	}
	if _, err := r.Snapshot(); !errors.Is(err, managerproof.ErrUnavailable) {
		t.Fatalf("pending proof snapshot: %v", err)
	}
	writes, began := 0, false
	sk.backend.live[b.Session].conn = &codexManagerTestConn{call: func(context.Context, string, any, any) error { writes++; return nil }}
	lane := sk.composerLaneFor(b.Session)
	barrier := lane.enter()
	defer lane.leave()
	_, err = sk.sendCodexManager(b.Session, "pending", protocol.ComposerSendReq{Text: "must wait", ManagerAdmission: &protocol.ManagerAdmission{Binding: b, Nonce: strings.Repeat("b", 64)}}, func() error { began = true; return nil }, lane, barrier)
	if err == nil || writes != 0 || began {
		t.Fatalf("pending proof crossed native write: err=%v writes=%d begin=%v", err, writes, began)
	}
}

func TestCodexManagerCustodyQueuedDuringBeginPreventsNativeWrite(t *testing.T) {
	sk, b, feed := newCodexManagerSendRig(t)
	writes := 0
	sk.backend.live[b.Session].conn = &codexManagerTestConn{call: func(context.Context, string, any, any) error { writes++; return nil }}
	const op = "during-begin"
	if _, _, err := sk.core.ClaimComposerOperation(op, protocol.OpSemanticSend, b.Session, b.SessionInstance, "hash"); err != nil {
		t.Fatal(err)
	}
	lane := sk.composerLaneFor(b.Session)
	barrier := lane.enter()
	defer lane.leave()
	code, err := sk.sendCodexManager(b.Session, op, protocol.ComposerSendReq{Text: "must not write stale proof", ManagerAdmission: &protocol.ManagerAdmission{Binding: b, Nonce: strings.Repeat("c", 64)}}, func() error {
		if err := sk.core.BeginComposerOperation(op); err != nil {
			return err
		}
		sk.captureCodexManagerFrame(b.Session, feed, "thread/status/changed", codexManagerFrame("thread/status/changed", b.NativeSessionID, map[string]any{"status": map[string]any{"type": "idle"}}))
		return nil
	}, lane, barrier)
	if code != protocol.CodeOutcomeUnknown || !errors.Is(err, protocol.ErrComposerOutcomeUnknown) || writes != 0 {
		t.Fatalf("queued evidence crossed write barrier: %s %v writes=%d", code, err, writes)
	}
}

func TestCodexManagerDirtyOwnerPreventsInitialDialAndLeavesNativeAlive(t *testing.T) {
	sk, b, _ := newCodexManagerSendRig(t)
	server := newR7R4Server(t)
	conn, _, err := sk.dialSessionBackend(b.Session, b.SessionInstance, daemon.BackendChannel{SocketPath: server.sock})
	if conn != nil {
		_ = conn.Close()
	}
	if !errors.Is(err, managerproof.ErrQuarantined) {
		t.Fatalf("dirty owner was reattached: %v", err)
	}
	server.mu.Lock()
	connections := len(server.conns)
	server.mu.Unlock()
	if connections != 0 {
		t.Fatal("unclaimed manager reached native backend dial")
	}
	meta, _ := sk.core.Get(b.Session)
	if err := syscall.Kill(meta.ShimPID, 0); err != nil {
		t.Fatal("quarantine stopped existing native session")
	}
}

func TestCodexManagerTerminalDetailWaitsForCallClosureThenReleases(t *testing.T) {
	rig := newResumeIdentityBackendRig(t)
	b := codexProofFixture()
	b.Session = resumeIdentityLocal
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	id, err := managerproof.InitializeReplay(dir, b.Session, b.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := managerproof.OpenReplay(dir, b.Session, b.ConversationID, id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = replay.Close() }()
	if err := replay.Claim(b.ObservationGeneration); err != nil {
		t.Fatal(err)
	}
	state, err := newCodexManagerState(b, replay)
	if err != nil {
		t.Fatal(err)
	}
	intent := codexManagerIntent{Binding: b, Nonce: strings.Repeat("a", 64), OperationID: "terminal-detail", TextHash: codexManagerTextHash("input")}
	if _, _, err := rig.core.ClaimComposerOperation(intent.OperationID, protocol.OpSemanticSend, b.Session, b.SessionInstance, "hash"); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(composerOriginIntent{Version: 1, CodexManager: &intent})
	if err := rig.core.BindSemanticRequest(intent.OperationID, raw); err != nil {
		t.Fatal(err)
	}
	if err := rig.core.BeginComposerOperation(intent.OperationID); err != nil {
		t.Fatal(err)
	}
	if err := state.prepare(intent); err != nil {
		t.Fatal(err)
	}
	tool := func(method string) []byte {
		return codexManagerFrame(method, b.NativeSessionID, map[string]any{"turnId": "turn", "item": map[string]any{"type": "mcpToolCall", "id": "call", "server": "swarm_voice", "tool": "publish", "arguments": json.RawMessage(`{"text":"hello"}`)}})
	}
	for _, frame := range [][]byte{codexManagerTurnFrame("turn/started", b.NativeSessionID, "turn", "inProgress"), codexManagerUserFrame(b.NativeSessionID, "turn", "message", intent.Nonce, "input"), tool("item/started"), codexManagerTurnFrame("turn/completed", b.NativeSessionID, "turn", "completed")} {
		if err := state.observe(frame); err != nil {
			t.Fatal(err)
		}
	}
	if err := rig.sk.retainCodexManagerState(state); err != nil {
		t.Fatal(err)
	}
	if len(state.intents) != 1 || len(state.messages) != 1 {
		t.Fatal("terminal turn discarded unresolved native call detail")
	}
	if err := state.observe(tool("item/completed")); err != nil {
		t.Fatal(err)
	}
	if err := rig.sk.retainCodexManagerState(state); err != nil {
		t.Fatal(err)
	}
	if len(state.intents)+len(state.messages)+len(state.evidence)+len(state.unretained) != 0 {
		t.Fatal("durably terminal native detail remained against live manager capacity")
	}
	if err := state.recordReceipt(intent.Nonce, "turn"); err != nil {
		t.Fatalf("late native ACK lost known terminal receipt: %v", err)
	}
	if len(state.intents)+len(state.evidence) != 0 {
		t.Fatal("late ACK resurrected terminal proof detail")
	}
	receipt, err := rig.sk.semanticOperation(b.Session, intent.OperationID)
	if err != nil || receipt.State != "completed" || receipt.NativeTurnID != "turn" {
		t.Fatalf("released terminal history was lost: %+v %v", receipt, err)
	}
}

func TestCodexManagerPrivateTerminalHistorySurvivesQuarantine(t *testing.T) {
	sk, b, feed := newCodexManagerSendRig(t)
	state := feed.manager.published.Load()
	c := feed.manager.actor.Load()
	intent := codexManagerIntent{Binding: b, Nonce: strings.Repeat("d", 64), OperationID: "history-after-loss", TextHash: codexManagerTextHash("input")}
	if _, _, err := sk.core.ClaimComposerOperation(intent.OperationID, protocol.OpSemanticSend, b.Session, b.SessionInstance, "hash"); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(composerOriginIntent{Version: 1, CodexManager: &intent})
	if err := c.Do(context.Background(), len(raw), func(context.Context) error {
		if err := sk.core.BindSemanticRequest(intent.OperationID, raw); err != nil {
			return err
		}
		if err := sk.core.BeginComposerOperation(intent.OperationID); err != nil {
			return err
		}
		return state.prepare(intent)
	}); err != nil {
		t.Fatal(err)
	}
	for _, frame := range [][]byte{codexManagerTurnFrame("turn/started", b.NativeSessionID, "history-turn", "inProgress"), codexManagerUserFrame(b.NativeSessionID, "history-turn", "history-message", intent.Nonce, "input"), codexManagerTurnFrame("turn/completed", b.NativeSessionID, "history-turn", "completed")} {
		var header struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(frame, &header)
		sk.captureCodexManagerFrame(b.Session, feed, header.Method, frame)
	}
	waitCodexCustody(t, feed)
	for _, lost := range []bool{false, true} {
		if lost {
			discardCodexManagerFeed(feed)
		}
		receipt, err := sk.codexManagerTerminal(context.Background(), b.Session, b, intent.Nonce)
		if err != nil || receipt.NativeTurnID != "history-turn" || receipt.Reason != "answer" {
			t.Fatalf("negative terminal history lost after observer loss=%v: %+v %v", lost, receipt, err)
		}
	}
	changed := b
	changed.ObservationGeneration = "foreign-observer"
	if _, err := sk.codexManagerTerminal(context.Background(), b.Session, changed, intent.Nonce); err == nil {
		t.Fatal("terminal history was rebound as a new observation")
	}
	if _, err := state.registry.Snapshot(); !errors.Is(err, managerproof.ErrStale) {
		t.Fatalf("history read restored positive authority: %v", err)
	}
}
