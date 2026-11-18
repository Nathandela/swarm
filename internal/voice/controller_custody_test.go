package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/status"
)

type controllerCustodyRuntime struct {
	*controllerRuntimeFixture
	lostSession  string
	next         protocol.ManagerRuntimeStatus
	resolveCalls int
}

func (r *controllerCustodyRuntime) ManagerRuntime(session, instance string) (protocol.ManagerRuntimeStatus, error) {
	if session == r.lostSession {
		return protocol.ManagerRuntimeStatus{}, managerproof.ErrQuarantined
	}
	if session == r.next.Binding.Session && instance == r.next.Binding.SessionInstance {
		return r.next, nil
	}
	return r.controllerRuntimeFixture.ManagerRuntime(session, instance)
}
func TestControllerCustodyLossIsDurableAndCannotRenewSameSessionObserver(t *testing.T) {
	c, s, r, b := provenControllerFixture(t)
	custody := &controllerCustodyRuntime{controllerRuntimeFixture: r, lostSession: b.SessionID}
	c.runtime = custody
	if err := c.Tick(context.Background()); !errors.Is(err, managerproof.ErrQuarantined) {
		t.Fatalf("typed custody loss disappeared: %v", err)
	}
	st, _ := s.Snapshot()
	a := st.Admissions[admissionKey(b.ConversationID, 1)]
	if !st.Conversations[b.ConversationID].CustodyLost || a.Phase != "uncertain" || a.SettledAt.IsZero() == false {
		t.Fatal("lost native input became settled or quarantine was not durable")
	}
	next := b
	next.ObservationGeneration = "new-observer"
	if _, err := s.RebindManager(b, next); !errors.Is(err, managerproof.ErrQuarantined) {
		t.Fatal("same-session observation renewal bypassed custody quarantine", err)
	}
	custody.lostSession = ""
	if err := c.Tick(context.Background()); !errors.Is(err, managerproof.ErrQuarantined) {
		t.Fatal("a later positive snapshot cleared permanent same-session quarantine", err)
	}
	if len(r.sent) != 1 {
		t.Fatal("custody loss replayed the old input")
	}
}
func TestProtocolControllerPreservesTypedCustodyLostStatus(t *testing.T) {
	socket := controllerWireServer(t, "machine", func(protocol.Control) protocol.Control {
		return protocol.Control{Op: protocol.OpError, ErrorCode: protocol.CodeManagerCustodyLost, Error: "fresh manager session required"}
	})
	client, err := NewProtocolControllerClient(context.Background(), socket, "machine")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.ManagerRuntime("machine/manager", "instance"); !errors.Is(err, managerproof.ErrQuarantined) {
		t.Fatal("actionable custody error became an ordinary unavailable receipt", err)
	}
}

func TestControllerRestartWithSameLiveCustodianKeepsNativeProofAndNeverReplays(t *testing.T) {
	c, s, r, b := provenControllerFixture(t)
	before, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	original := before.Admissions[admissionKey(b.ConversationID, 1)]
	dir := filepath.Dir(s.db.Path())
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	current, err := reopened.BindManager(b)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := NewController(reopened, r, current, c.refs)
	if err != nil {
		t.Fatal(err)
	}
	if err = resumed.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = resumed.ManagerCall(context.Background(), ManagerToolCall{Name: "publish", Arguments: json.RawMessage(`{"output_id":"after-restart","sequence":1,"text":"Still working","final":false}`)}); err != nil {
		t.Fatal("same retained live custodian lost positive proof", err)
	}
	after, err := reopened.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	saved := after.Admissions[admissionKey(b.ConversationID, 1)]
	if len(r.sent) != 1 || saved.NativeInputID != original.NativeInputID || saved.OperationID != original.OperationID || saved.Phase != "running" || after.Conversations[b.ConversationID].LastAdmission != 1 || after.Conversations[b.ConversationID].CustodyLost {
		t.Fatal("controller restart invented a new native input or observer")
	}
}

func (r *controllerCustodyRuntime) ResolveManagerCall(ctx context.Context, req protocol.ManagerCallReq) (protocol.ManagerCallProof, error) {
	r.resolveCalls++
	return r.controllerRuntimeFixture.ResolveManagerCall(ctx, req)
}

func TestControllerFreshSessionReplacementPreservesUncertaintyAndRejectsDelayedOldMCP(t *testing.T) {
	c, s, base, b := provenControllerFixture(t)
	workers := &controllerWorkerFixture{launchErr: errors.New("lost reply"), outcome: schema.OperationOutcomeView{State: schema.OutcomeUnknown}}
	c.workers = workers
	if _, err := c.ManagerCall(context.Background(), ManagerToolCall{Name: "swarm_create", Arguments: json.RawMessage(`{"action_id":"uncertain-worker","agent":"codex","cwd":"/tmp","prompt":"work"}`)}); !errors.Is(err, ErrControllerUncertain) {
		t.Fatal(err)
	}
	nativeHistory := filepath.Join(t.TempDir(), "native-history.jsonl")
	const history = "retained native provider history; not controller-owned\n"
	if err := os.WriteFile(nativeHistory, []byte(history), 0600); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Snapshot()
	original := before.Admissions[admissionKey(b.ConversationID, 1)]
	custody := &controllerCustodyRuntime{controllerRuntimeFixture: base, lostSession: b.SessionID}
	c.runtime = custody
	if err := c.Tick(context.Background()); !errors.Is(err, managerproof.ErrQuarantined) {
		t.Fatal(err)
	}
	next := b
	next.SessionID = "machine/fresh"
	next.SessionInstance = "fresh-instance"
	next.NativeGeneration = "fresh-native"
	next.ObservationGeneration = "fresh-observer"
	custody.next = protocol.ManagerRuntimeStatus{Binding: runtimeBinding(next), ReadyForInput: true, BridgeSocket: c.refs.Socket, AuthFile: c.refs.AuthFile, Turn: protocol.ManagerNativeTurn{State: "idle"}}
	oldRow := protocol.SessionView{ID: b.SessionID, Agent: b.Provider, Role: "manager", ManagerConversationID: b.ConversationID, Status: status.Status{Process: status.ProcessRunning}, Capabilities: &schema.SessionCapabilities{SessionInstance: b.SessionInstance}}
	newRow := protocol.SessionView{ID: next.SessionID, Agent: next.Provider, Role: "manager", ManagerConversationID: next.ConversationID, Status: status.Status{Process: status.ProcessRunning}, Capabilities: &schema.SessionCapabilities{SessionInstance: next.SessionInstance}}
	workers.rows = []protocol.SessionView{oldRow, newRow}
	if _, err := c.ReplaceManager(context.Background(), next, c.refs); !errors.Is(err, ErrControllerUncertain) {
		t.Fatal("running old manager was treated as ended", err)
	}
	workers.rows[0].Status.Process = status.ProcessLost
	if _, err := c.ReplaceManager(context.Background(), next, c.refs); !errors.Is(err, ErrControllerUncertain) {
		t.Fatal("mere loss was treated as old session exit", err)
	}
	workers.rows[0].Status.Process = status.ProcessExited
	replacement, err := c.ReplaceManager(context.Background(), next, c.refs)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := s.Snapshot()
	old := after.Admissions[admissionKey(b.ConversationID, 1)]
	if !old.NativeBindingRetired || old.Phase != "uncertain" || !old.SettledAt.IsZero() || old.OperationID != original.OperationID || old.NativeInputID != original.NativeInputID || string(old.Request) != string(original.Request) || after.Conversations[b.ConversationID].RetiredThrough != 0 {
		t.Fatal("replacement changed old provenance, replayed input or inferred task success")
	}
	for _, a := range after.Actions {
		if a.State != "executing" {
			t.Fatal("replacement discarded an uncertain worker effect")
		}
	}
	content, err := os.ReadFile(nativeHistory)
	if err != nil || string(content) != history {
		t.Fatal("native provider history was removed or rewritten")
	}
	if workers.launches != 1 || len(base.sent) != 1 {
		t.Fatal("replacement auto-launched or replayed work")
	}
	if err = replacement.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(base.sent) != 2 || base.sent[1].Session != next.SessionID || !strings.Contains(base.sent[1].Text, "old session exited") {
		t.Fatal("new scope did not begin with explicit recovery-only context")
	}
	nonce := base.sent[1].ManagerAdmission.Nonce
	custody.next.ReadyForInput = false
	custody.next.Turn = protocol.ManagerNativeTurn{State: "running", NativeInputID: nonce, NativeTurnID: "fresh-turn"}
	// The private bridge also binds inherited SWARM_SESSION_ID, not just its credential.
	handler := newManagerHandler(b.ConversationID, strings.Repeat("a", 64))
	handler.bind("fresh", replacement.ManagerCall)
	envelope := ManagerBridgeRequest{Version: 1, ConversationID: b.ConversationID, SessionID: "session", Call: ManagerToolCall{Name: "publish", Arguments: json.RawMessage(`{"output_id":"late","sequence":1,"text":"stale","final":true}`), Meta: map[string]any{"callId": "old-native-call"}}}
	raw, _ := json.Marshal(envelope)
	request := httptest.NewRequest(http.MethodPost, "http://voice/manager/call", bytes.NewReader(raw))
	request.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 64))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || custody.resolveCalls != 0 {
		t.Fatal("delayed old inherited SessionID borrowed fresh manager authority")
	}
	// Even a forged fresh envelope cannot make the retired native input active again.
	custody.proof = protocol.ManagerCallProof{Main: true, NativeInputID: original.NativeInputID, NativeTurnID: original.NativeTurnID, NativeCallID: "old-native-call"}
	if _, err = replacement.ManagerCall(context.Background(), envelope.Call); !errors.Is(err, ErrStale) {
		t.Fatal("old proof borrowed fresh runtime turn", err)
	}
	custody.next.Turn = protocol.ManagerNativeTurn{State: "running", NativeInputID: original.NativeInputID, NativeTurnID: original.NativeTurnID}
	if _, err = replacement.ManagerCall(context.Background(), envelope.Call); !errors.Is(err, ErrExpired) {
		t.Fatal("retired native namespace became active under a contradictory source snapshot", err)
	}
	state, _ := s.Snapshot()
	if len(state.Publications) != 0 {
		t.Fatal("old delayed MCP published under fresh manager")
	}
	custody.next.Turn = protocol.ManagerNativeTurn{State: "completed", NativeInputID: nonce, NativeTurnID: "fresh-turn"}
	custody.next.ReadyForInput = true
	if err = replacement.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = replacement.SubmitText(Utterance{ID: "explicit-new-work", DeviceID: "device", VoiceSessionID: "fresh-voice", MediaEpoch: 2}, "new deliberate work"); err != nil {
		t.Fatal(err)
	}
	if err = replacement.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, _ = s.Snapshot()
	if len(base.sent) != 3 || base.sent[2].Session != next.SessionID || base.sent[2].Text != "new deliberate work" || state.Conversations[b.ConversationID].RetiredThrough != 0 {
		t.Fatal("fresh explicit work was blocked or an old unresolved guard was retired")
	}
}

type controllerCustodyRace struct {
	*controllerRuntimeFixture
	proofError, historicalError error
}

func (r *controllerCustodyRace) ResolveManagerCall(ctx context.Context, req protocol.ManagerCallReq) (protocol.ManagerCallProof, error) {
	if r.proofError != nil {
		return protocol.ManagerCallProof{}, r.proofError
	}
	return r.controllerRuntimeFixture.ResolveManagerCall(ctx, req)
}
func (r *controllerCustodyRace) ManagerInputStatus(context.Context, protocol.ManagerRuntimeBinding, string) (protocol.ManagerNativeTurn, error) {
	return protocol.ManagerNativeTurn{}, r.historicalError
}
func TestControllerCustodyLossDuringProofOrHistoryCannotUseEarlierPositiveSnapshot(t *testing.T) {
	for _, phase := range []string{"proof", "history"} {
		t.Run(phase, func(t *testing.T) {
			c, s, base, b := provenControllerFixture(t)
			racing := &controllerCustodyRace{controllerRuntimeFixture: base}
			if phase == "proof" {
				racing.proofError = managerproof.ErrQuarantined
			} else {
				racing.historicalError = managerproof.ErrQuarantined
			}
			c.runtime = racing
			var err error
			if phase == "proof" {
				_, err = c.ManagerCall(context.Background(), ManagerToolCall{Name: "publish", Arguments: json.RawMessage(`{"output_id":"raced","sequence":1,"text":"must not publish","final":true}`)})
			} else {
				err = c.Tick(context.Background())
			}
			if !errors.Is(err, managerproof.ErrQuarantined) {
				t.Fatalf("custody race became ordinary status: %v", err)
			}
			st, _ := s.Snapshot()
			if !st.Conversations[b.ConversationID].CustodyLost || len(st.Publications) != 0 || st.Admissions[admissionKey(b.ConversationID, 1)].Phase != "uncertain" {
				t.Fatal("earlier positive snapshot survived lost custody")
			}
		})
	}
}

func TestControllerSubmittedReceiptCannotDemoteProvenRunningInput(t *testing.T) {
	c, s, r, b := provenControllerFixture(t)
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.status.Turn = protocol.ManagerNativeTurn{State: "unknown"}
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	a := st.Admissions[admissionKey(b.ConversationID, 1)]
	if a.Phase != "running" || a.NativeTurnID != "main-turn" || len(r.sent) != 1 {
		t.Fatalf("stale submitted receipt demoted proven input: %+v", a)
	}
}
