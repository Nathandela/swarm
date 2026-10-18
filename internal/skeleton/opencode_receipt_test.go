package skeleton

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/Nathandela/swarm/internal/idempotency"
	"github.com/Nathandela/swarm/internal/opencodebridge"
	"github.com/Nathandela/swarm/internal/protocol"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func nativeReceiptFixture() (openCodeSendIntent, json.RawMessage) {
	hash := sha256.Sum256([]byte("exact\ntext"))
	intent := openCodeSendIntent{Identity: opencodebridge.Identity{Version: 1, WorkerID: "worker", RootID: "ses_" + strings.Repeat("a", 32), Directory: "/project", SessionInstance: strings.Repeat("b", 32), BackendGeneration: strings.Repeat("b", 32), NativeCreatedAtMS: 123}, MessageID: "msg_" + strings.Repeat("c", 32), TextHash: hex.EncodeToString(hash[:]), NotBeforeMS: 500}
	raw, _ := json.Marshal(map[string]any{"info": map[string]any{"id": intent.MessageID, "sessionID": intent.Identity.RootID, "role": "user", "agent": "plan", "model": map[string]string{"providerID": "opencode", "modelID": "deepseek-v4.1-flash-free", "variant": "max"}, "time": map[string]int64{"created": 501}}, "parts": []any{map[string]any{"id": "prt_owned", "sessionID": intent.Identity.RootID, "messageID": intent.MessageID, "type": "text", "text": "exact\ntext"}}})
	return intent, raw
}
func TestOpenCodeReceiptRequiresExactNativeUserAndActualTuple(t *testing.T) {
	intent, raw := nativeReceiptFixture()
	receipt, err := qualifyOpenCodeAdmission("semantic:owned", intent, raw)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.MessageID != intent.MessageID || receipt.Identity != intent.Identity || receipt.Agent != "plan" || receipt.Model.ModelID != "deepseek-v4.1-flash-free" || receipt.Variant != "max" || receipt.SelectionBasis != "native_message_receipt" {
		t.Fatal("actual native receipt was reconstructed from defaults")
	}
	for _, change := range []struct{ old, new string }{{`"role":"user"`, `"role":"assistant"`}, {`"created":501`, `"created":499`}, {`"text":"exact\ntext"`, `"text":"other"`}, {`"type":"text"`, `"type":"file"`}, {`"agent":"plan"`, `"agent":""`}, {`"text":"exact\ntext"`, `"synthetic":true,"text":"exact\ntext"`}, {`"modelID":"deepseek-v4.1-flash-free"`, `"modelID":""`}, {`"sessionID":"ses_`, `"sessionID":"ses_x`}, {`"messageID":"msg_`, `"messageID":"msg_x`}} {
		if _, err := qualifyOpenCodeAdmission("semantic:owned", intent, json.RawMessage(strings.Replace(string(raw), change.old, change.new, 1))); err == nil {
			t.Fatalf("conflicting row admitted: %s", change.old)
		}
	}
}

func TestOpenCodeLostReplyReconcilesExactRowWithoutReposting(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	f.dropReply.Store(true)
	id, err := protocol.NewSemanticOperationID()
	if err != nil {
		t.Fatal(err)
	}
	if err = f.client.SemanticSend(id, f.request); err == nil {
		t.Fatal("lost response became success")
	}
	f.rowAvailable.Store(true)
	receipt, err := f.client.SemanticOperation(f.request.Session, id)
	if err != nil || receipt.State != "admitted" || receipt.Model != "fixture/fixture-model" || receipt.Variant != "max" || receipt.SelectionBasis != "native_message_receipt" {
		t.Fatalf("exact native reconciliation: %+v %v", receipt, err)
	}
	if err = f.client.SemanticSend(id, f.request); err != nil {
		t.Fatal(err)
	}
	if f.calls.Load() != 1 {
		t.Fatal("reconciliation resubmitted native request")
	}
}
func TestOpenCodeLostReplyRejectsConflictingNativeText(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	f.dropReply.Store(true)
	id, _ := protocol.NewSemanticOperationID()
	_ = f.client.SemanticSend(id, f.request)
	raw := f.nativeRow.Load().(json.RawMessage)
	f.nativeRow.Store(json.RawMessage(strings.Replace(string(raw), "exact native message", "different native text", 1)))
	f.rowAvailable.Store(true)
	result, err := f.client.SemanticOperation(f.request.Session, id)
	if err != nil || result.State != "uncertain" || result.Agent != "" {
		t.Fatalf("conflicting row borrowed admission/model: %+v %v", result, err)
	}
	if f.calls.Load() != 1 {
		t.Fatal("conflict caused retry")
	}
}
func TestOpenCodeTerminalCheckpointSurvivesViewEvictionAndDaemonRestart(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	id, _ := protocol.NewSemanticOperationID()
	if err := f.client.SemanticSend(id, f.request); err != nil {
		t.Fatal(err)
	}
	rec, _ := f.d.core.ComposerOperation(id)
	var intent openCodeSendIntent
	if json.Unmarshal(rec.Request, &intent) != nil {
		t.Fatal("missing exact request")
	}
	view := OpenCodeView{Root: intent.Identity.RootID, Generation: intent.Identity.BackendGeneration, Inputs: []OpenCodeInput{{MessageID: intent.MessageID, Classified: true, At: intent.NotBeforeMS + 1, Outcome: OpenCodeOutcomeCompleted, OutcomeSource: OpenCodeOutcomeSourceLive, OutcomeAt: intent.NotBeforeMS + 2}}}
	f.d.checkpointOpenCodeOutcomes(f.binding.WorkerID, view)
	view.Inputs[0].OutcomeSource = OpenCodeOutcomeSourceImported
	f.d.checkpointOpenCodeOutcomes(f.binding.WorkerID, view)
	view.Inputs = nil
	f.d.checkpointOpenCodeOutcomes(f.binding.WorkerID, view)
	receipt, err := f.client.SemanticOperation(f.request.Session, id)
	if err != nil || receipt.State != "completed" || receipt.OutcomeSource != "live" {
		t.Fatalf("eviction lost terminal evidence: %+v %v", receipt, err)
	}
	old := f.d
	_ = f.client.Close()
	if err = old.Close(); err != nil {
		t.Fatal(err)
	}
	replacement, err := Serve(Config{StateDir: old.stateDir, SocketPath: old.socketPath, LockPath: filepath.Join(old.stateDir, "d.lock"), LogPath: filepath.Join(old.stateDir, "d.log"), ShimBinary: swarmBin, FakeAgentBin: fakeAgentBin, MaxSessions: 16, PollInterval: 50 * time.Millisecond, StalenessThreshold: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	f.d = replacement
	t.Cleanup(func() { _ = replacement.Close() })
	f.client, err = protocol.Dial(replacement.SocketPath(), []string{protocol.CapSemanticSend})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err = f.client.SemanticOperation(f.request.Session, id)
	if err != nil || receipt.State != "completed" || receipt.NativeMessageID != intent.MessageID || receipt.Model != "fixture/fixture-model" {
		t.Fatalf("restart lost exact terminal proof: %+v %v", receipt, err)
	}
	if f.calls.Load() != 1 {
		t.Fatal("restart replayed native input")
	}
}

func TestOpenCodeObservedTerminalRetainsAdmissionWhenDispatchReplyWasLost(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	f.dropReply.Store(true)
	id, _ := protocol.NewSemanticOperationID()
	_ = f.client.SemanticSend(id, f.request)
	rec, _ := f.d.core.ComposerOperation(id)
	var intent openCodeSendIntent
	_ = json.Unmarshal(rec.Request, &intent)
	in := OpenCodeInput{PlainTextInput: true, MessageID: intent.MessageID, Classified: true, Text: f.request.Text, At: intent.NotBeforeMS + 1, Agent: "build", ProviderID: "fixture", ModelID: "fixture-model", Variant: "max", Outcome: OpenCodeOutcomeFailed, OutcomeSource: OpenCodeOutcomeSourceLive, OutcomeAt: intent.NotBeforeMS + 2}
	view := OpenCodeView{Root: intent.Identity.RootID, Generation: intent.Identity.BackendGeneration, Inputs: []OpenCodeInput{in}}
	f.d.checkpointOpenCodeOutcomes(f.binding.WorkerID, view)
	view.Inputs = nil
	f.d.checkpointOpenCodeOutcomes(f.binding.WorkerID, view)
	receipt, err := f.client.SemanticOperation(f.request.Session, id)
	if err != nil || receipt.State != "failed" || receipt.NativeMessageID != intent.MessageID || receipt.Model != "fixture/fixture-model" || receipt.OutcomeSource != "live" {
		t.Fatalf("lostreplyterminal notretained: %+v %v", receipt, err)
	}
	if f.calls.Load() != 1 {
		t.Fatal("terminalcheckpoint resubmitted")
	}
}

func TestOpenCodeEvidenceReadsEarlierRawReceiptWithoutChangingProof(t *testing.T) {
	intent, raw := nativeReceiptFixture()
	receipt, err := qualifyOpenCodeAdmission("semantic:owned", intent, raw)
	if err != nil {
		t.Fatal(err)
	}
	legacy, _ := json.Marshal(receipt)
	evidence, err := readOpenCodeEvidence(legacy)
	if err != nil || evidence.Version != 1 || evidence.Receipt.MessageID != intent.MessageID || evidence.Receipt.Agent != "plan" {
		t.Fatalf("earlier raw receipt was lost: %+v %v", evidence, err)
	}
}

func TestOpenCodeExecutingReplayReconcilesWithoutAnotherNativeWrite(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	f.dropReply.Store(true)
	id, _ := protocol.NewSemanticOperationID()
	if f.client.SemanticSend(id, f.request) == nil {
		t.Fatal("lost response claimed success")
	}
	f.rowAvailable.Store(true)
	if err := f.client.SemanticSend(id, f.request); err != nil {
		t.Fatal(err)
	}
	rec, _ := f.d.core.ComposerOperation(id)
	if rec.Phase != idempotency.PhaseCompleted || f.calls.Load() != 1 {
		t.Fatal("executing replay did not resolve through readonly exact history")
	}
}
func TestOpenCodeExpiredOperationCanReconcileReadOnly(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	id := fmt.Sprintf("semantic:%d:%s", time.Now().Add(-2*time.Hour).UnixMilli(), strings.Repeat("e", 32))
	hash := sha256.Sum256([]byte(f.request.Text))
	intent := openCodeSendIntent{Identity: f.identity, MessageID: openCodeMessageID(id, f.identity), TextHash: hex.EncodeToString(hash[:]), NotBeforeMS: time.Now().UnixMilli()}
	if _, _, err := f.d.core.ClaimComposerOperation(id, protocol.OpSemanticSend, f.binding.WorkerID, f.request.SessionInstance, "hash"); err != nil {
		t.Fatal(err)
	}
	descriptor, _ := json.Marshal(intent)
	if err := f.d.core.BindSemanticRequest(id, descriptor); err != nil {
		t.Fatal(err)
	}
	if err := f.d.core.BeginComposerOperation(id); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"info": map[string]any{"id": intent.MessageID, "sessionID": intent.Identity.RootID, "role": "user", "agent": "plan", "model": map[string]string{"providerID": "fixture", "modelID": "fixture-model"}, "time": map[string]int64{"created": intent.NotBeforeMS + 1}}, "parts": []any{map[string]any{"id": "prt_exact", "sessionID": intent.Identity.RootID, "messageID": intent.MessageID, "type": "text", "text": f.request.Text}}})
	f.nativeRow.Store(json.RawMessage(raw))
	f.rowAvailable.Store(true)
	receipt, err := f.client.SemanticOperation(f.request.Session, id)
	if err != nil || receipt.State != "admitted" || receipt.NativeMessageID != intent.MessageID || receipt.Agent != "plan" || f.calls.Load() != 0 {
		t.Fatalf("expired readonly recovery refused or submitted: %+v %v", receipt, err)
	}
	if f.client.SemanticSend(id, f.request) == nil || f.calls.Load() != 0 {
		t.Fatal("expired receipt granted fresh execution")
	}
}
