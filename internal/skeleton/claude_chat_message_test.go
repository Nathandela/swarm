package skeleton

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/shimwire"
)

func launchClaudeMessageFixture(t *testing.T, d *Daemon) persist.Meta {
	t.Helper()
	m, err := d.api.Launch(daemon.LaunchSpec{AgentType: "fake", Cwd: t.TempDir(), Options: map[string]string{"claude_interface": "swarm-chat"}, ClientEnv: []string{"PATH=" + os.Getenv("PATH")}, OperationID: "fixture-message-launch"})
	if m.ID != "" {
		t.Cleanup(func() { _ = d.core.Kill(m.ID) })
	}
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r, e := shim.ReadHookSpoolFileResponse(filepath.Join(d.stateDir, m.ID, shim.HookSpoolFile), 0)
		if e == nil {
			for _, rec := range r.Records {
				var env struct {
					Raw []byte `json:"raw"`
				}
				var frame struct{ Type, Subtype string }
				if json.Unmarshal(rec.Body, &env) == nil && json.Unmarshal(env.Raw, &frame) == nil && frame.Type == "system" && frame.Subtype == "init" {
					return m
				}
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("fixture init did not reach actual capture")
	return m
}

func TestClaudeChatExactOwnUUIDBeforeShimAttemptHasNoEffects(t *testing.T) {
	d := assemble(t)
	m := launchClaudeMessageFixture(t, d)
	d.startHookDrain(m.ID)
	state := d.claudeFixtureState(m.ID)
	if state == nil || state.drainer == nil {
		t.Fatal("fresh consumer unavailable")
	}
	if _, _, err := state.drainer.DrainOnce(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := d.core.ClaudeFixtureSnapshot(m.ID)
	if err != nil || snapshot.OwnerRevision != 0 {
		t.Fatal("unwritten target precondition", err)
	}
	instance, ok := d.sessionInstance(m.ID)
	if !ok {
		t.Fatal("instance absent")
	}
	const text = "owned UUID is not a write"
	hash := sha256.Sum256([]byte(text))
	uuid, err := newClaudeFixtureUUID()
	if err != nil {
		t.Fatal(err)
	}
	intent := claudeFixtureIntent{Version: 1, Kind: "claude_fixture_message", Identity: snapshot.Identity, SessionInstance: instance, NativeUserUUID: uuid, TextSHA256: hex.EncodeToString(hash[:]), TextBytes: len(text), OwnerRevision: 1}
	op := "unwritten-fixture-operation"
	bound, _ := json.Marshal(intent)
	if _, _, err = d.core.ClaimComposerOperation(op, "semantic_send", m.ID, instance, "hash"); err != nil {
		t.Fatal(err)
	}
	if err = d.eng.ObserveExecutionOwnerRevision(m.ID, 0, 1); err != nil {
		t.Fatal(err)
	}
	if err = d.core.BindSemanticRequest(op, bound); err != nil {
		t.Fatal(err)
	}
	if err = d.core.BeginComposerOperation(op); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	state.operation, state.intent, state.ownerRevision = op, intent, 1
	state.evidence = claudeFixtureEvidence{Version: 1, Kind: "claude_fixture_message", OperationID: op, Intent: intent, State: "prepared", RawHashes: []string{state.initHash}}
	state.mu.Unlock()
	// An untrusted envelope can know the durable UUID while the owned shim has
	// neither consumed this output prefix nor attempted its native input write.
	frame, _ := json.Marshal(map[string]any{"type": "user", "uuid": uuid, "session_id": m.ConversationID, "parent_tool_use_id": nil, "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": text}}}})
	envelope, _ := json.Marshal(map[string]any{"v": 1, "origin": "claude_primary", "native_session_id": m.ConversationID, "primary": snapshot.Identity, "read_monotonic_ns": 1, "stream_sequence": 2, "raw": frame})
	before, err := d.core.JournalReadFrom(0)
	if err != nil {
		t.Fatal(err)
	}
	err = d.consumeClaudeFixture(state, envelope)
	after, e := d.core.JournalReadFrom(0)
	record, _ := d.core.ComposerOperation(op)
	if err == nil || e != nil || after.Cursor != before.Cursor || len(record.Evidence) != 0 || state.sequence != 1 {
		t.Fatal("UUID alone gained admission without a shim attempt", err, e)
	}
}

func TestClaudeChatSavedIntentRequiresCurrentDurableExecutionRevision(t *testing.T) {
	for _, kind := range []string{"missing-revision", "wrong-incarnation", "wrong-conversation", "pending", "invalid"} {
		t.Run(kind, func(t *testing.T) {
			d := assemble(t)
			m := launchClaudeMessageFixture(t, d)
			client := dialClient(t, d, protocol.CapSemanticSend, protocol.CapJournal)
			id := protocol.NamespacedID(d.api.endpointID, m.ID)
			if _, err := client.NativeInteractionHistory(schema.InteractionHistoryReq{Native: true, Session: id, Limit: 200}); err != nil {
				t.Fatal(err)
			}
			snapshot, err := d.core.ClaudeFixtureSnapshot(m.ID)
			if err != nil || snapshot.OwnerRevision != 0 {
				t.Fatal("unwritten primary precondition", err)
			}
			instance, ok := d.sessionInstance(m.ID)
			if !ok {
				t.Fatal("instance absent")
			}
			const text = "prepared intent has no durable revision proof"
			op, err := protocol.NewSemanticOperationID()
			if err != nil {
				t.Fatal(err)
			}
			uuid, err := newClaudeFixtureUUID()
			if err != nil {
				t.Fatal(err)
			}
			hash := sha256.Sum256([]byte(text))
			intent := claudeFixtureIntent{Version: 1, Kind: "claude_fixture_message", Identity: snapshot.Identity, SessionInstance: instance, NativeUserUUID: uuid, TextSHA256: hex.EncodeToString(hash[:]), TextBytes: len(text), OwnerRevision: 1}
			bound, _ := json.Marshal(intent)
			request := protocol.ComposerSendReq{Session: id, SessionInstance: instance, Text: text}
			if _, _, err = d.core.ClaimComposerOperation(op, "semantic_send", m.ID, instance, hex.EncodeToString(schema.ComposerSendContentHash(&request))); err != nil {
				t.Fatal(err)
			}
			if err = d.core.BindSemanticRequest(op, bound); err != nil {
				t.Fatal(err)
			}
			if kind != "missing-revision" {
				saved, e := d.eng.ExecutionSnapshot(m.ID)
				if e != nil {
					t.Fatal(e)
				}
				saved.OwnerRevision = 1
				switch kind {
				case "wrong-incarnation":
					saved.Incarnation = "different-incarnation"
				case "wrong-conversation":
					saved.ConversationID = "79b79d77-e62d-44b9-8e24-94e908f08935"
				case "pending":
					saved.Incomplete = "persistence_pending"
				case "invalid":
					saved.Version = 0
				}
				err = d.eng.RegisterExecutionSession(m.ID, "", m.ShimPID, nil, m.Status, engine.ExecutionIdentity{Incarnation: saved.Incarnation, ConversationID: saved.ConversationID, OwnerRevision: 1}, &saved)
				if kind != "invalid" && err != nil {
					t.Fatal("controlled snapshot setup", err)
				}
				if kind == "invalid" && err == nil {
					t.Fatal("invalid snapshot unexpectedly restored")
				}
			}
			sendErr := client.SemanticSend(op, request)
			after, e := d.core.ClaudeFixtureSnapshot(m.ID)
			if e != nil {
				t.Fatal("lost primary before no-effect oracle", e)
			}
			rows, e := os.ReadFile(filepath.Join(d.stateDir, m.ID, "claude-fixture-transcript.jsonl"))
			if e != nil {
				t.Fatal(e)
			}
			if sendErr == nil || after.OwnerRevision != 0 || len(rows) != 0 {
				t.Fatal("retained descriptor bypassed durable execution scope", kind, sendErr, after.OwnerRevision)
			}
		})
	}
}

func TestClaudeChatEnvelopeIdentityAliasesHaveNoFoldEffects(t *testing.T) {
	for _, kind := range []string{"duplicate-origin", "alias-origin", "alias-shim"} {
		t.Run(kind, func(t *testing.T) {
			d := assemble(t)
			m := launchClaudeMessageFixture(t, d)
			spool, err := shim.ReadHookSpoolFileResponse(filepath.Join(d.stateDir, m.ID, shim.HookSpoolFile), 0)
			if err != nil || len(spool.Records) != 1 {
				t.Fatal("init precondition", err)
			}
			body := spool.Records[0].Body
			switch kind {
			case "duplicate-origin":
				body = bytes.Replace(body, []byte(`"origin":"claude_primary"`), []byte(`"origin":"hook","origin":"claude_primary"`), 1)
			case "alias-origin":
				body = bytes.Replace(body, []byte(`"origin":"claude_primary"`), []byte(`"origin":"hook","Origin":"claude_primary"`), 1)
			case "alias-shim":
				body = bytes.Replace(body, []byte(fmt.Sprintf(`"shim_pid":%d`, m.ShimPID)), []byte(fmt.Sprintf(`"shim_pid":0,"Shim_PID":%d`, m.ShimPID)), 1)
			}
			if bytes.Equal(body, spool.Records[0].Body) {
				t.Fatal("counterfactual did not change source")
			}
			state := d.claudeFixtureState(m.ID)
			if state == nil {
				t.Fatal("fresh-only consumer absent")
			}
			before, err := d.core.JournalReadFrom(0)
			if err != nil {
				t.Fatal(err)
			}
			err = d.consumeClaudeFixture(state, body)
			after, e := d.core.JournalReadFrom(0)
			if err == nil || e != nil || after.Cursor != before.Cursor || state.sequence != 0 || state.initialized {
				t.Fatal("ambiguous envelope gained fold/journal authority", err, e)
			}
		})
	}
}

func TestClaudeChatFixtureRejectsOriginWithoutEffects(t *testing.T) {
	d := assemble(t)
	m := launchClaudeMessageFixture(t, d)
	client := dialClient(t, d, protocol.CapSemanticSend, protocol.CapJournal)
	id := protocol.NamespacedID(d.api.endpointID, m.ID)
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := client.NativeInteractionHistory(schema.InteractionHistoryReq{Native: true, Session: id, Limit: 200})
		if err == nil {
			break
		}
		if !strings.Contains(err.Error(), errClaudeFixturePending.Error()) || time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	instance, ok := d.sessionInstance(m.ID)
	if !ok {
		t.Fatal("fixture has no current instance")
	}
	req := protocol.ComposerSendReq{Session: id, SessionInstance: instance, Text: "attributed fixture message", Origin: &protocol.MessageOrigin{Kind: "external", InstallationID: "fixture", RequestID: "request", ActorID: "colleague"}}
	if err := req.Origin.Validate(); err != nil {
		t.Fatal(err)
	}
	const operation = "fixture-origin-refusal"
	if _, _, err := d.core.ClaimComposerOperation(operation, protocol.OpSemanticSend, m.ID, instance, hex.EncodeToString(schema.ComposerSendContentHash(&req))); err != nil {
		t.Fatal(err)
	}
	beforeRecord, _ := d.core.ComposerOperation(operation)
	beforeExecution, err := d.eng.ExecutionSnapshot(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforePrimary, err := d.core.ClaudeFixtureSnapshot(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(d.stateDir, m.ID, "claude-fixture-transcript.jsonl")
	beforeTranscript, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	began := false
	code, sendErr := d.api.SemanticSendTransactional(d.api.endpointID, operation, req, func() error { began = true; return d.core.BeginComposerOperation(operation) })
	afterRecord, ok := d.core.ComposerOperation(operation)
	afterExecution, executionErr := d.eng.ExecutionSnapshot(m.ID)
	afterPrimary, primaryErr := d.core.ClaudeFixtureSnapshot(m.ID)
	afterTranscript, transcriptErr := os.ReadFile(path)
	if code != protocol.CodeStructuredUnsupported || sendErr == nil || began || !ok || executionErr != nil || primaryErr != nil || transcriptErr != nil {
		t.Fatalf("origin refusal crossed admission: code=%q send=%v begin=%v record=%v execution=%v primary=%v transcript=%v", code, sendErr, began, ok, executionErr, primaryErr, transcriptErr)
	}
	if len(beforeRecord.Request) != 0 || len(beforeRecord.Evidence) != 0 || len(afterRecord.Request) != 0 || len(afterRecord.Evidence) != 0 || afterRecord.Phase != beforeRecord.Phase || afterExecution.OwnerRevision != beforeExecution.OwnerRevision || afterPrimary.OwnerRevision != beforePrimary.OwnerRevision || !bytes.Equal(afterTranscript, beforeTranscript) {
		t.Fatal("refused origin changed request, evidence, revision or native transcript")
	}
	state := d.claudeFixtureState(m.ID)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.operation != "" || state.evidence.OperationID != "" || state.ownerRevision != beforeExecution.OwnerRevision {
		t.Fatal("refused origin acquired fixture operation custody")
	}
}

func TestClaudeChatMessageConnectedEchoResultHistory(t *testing.T) {
	d := assemble(t)
	m := launchClaudeMessageFixture(t, d)
	c := dialClient(t, d, protocol.CapSemanticSend, protocol.CapJournal)
	id := protocol.NamespacedID(d.api.endpointID, m.ID)
	instance, ok := d.sessionInstance(m.ID)
	if !ok {
		t.Fatal("fixture has no current session instance")
	}
	op, err := protocol.NewSemanticOperationID()
	if err != nil {
		t.Fatal(err)
	}
	const text = "owned fixture message with a newline\nand exact content"
	if err := c.SemanticSend(op, protocol.ComposerSendReq{Session: id, SessionInstance: instance, Text: text}); err != nil {
		t.Fatalf("connected fixture semantic send refused: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var receipt protocol.SemanticMessageReceipt
	for time.Now().Before(deadline) {
		receipt, err = c.SemanticOperation(id, op)
		if err == nil && receipt.State == "completed" {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if err != nil || receipt.State != "completed" || receipt.NativeSessionID != m.ConversationID || receipt.NativeMessageID == "" || receipt.Agent != "fake" || receipt.OutcomeSource != "claude_primary_fixture" {
		t.Fatalf("no exact admitted fixture result: receipt=%+v err=%v", receipt, err)
	}
	page, err := c.NativeInteractionHistory(schema.InteractionHistoryReq{Native: true, Session: id, Limit: 200})
	if err != nil || !page.Floor || page.Scope == "" || len(page.Items) != 3 {
		t.Fatalf("genuine fixture history was not connected: page=%+v err=%v", page, err)
	}
	joined, _ := json.Marshal(page.Items)
	if !bytes.Contains(joined, []byte("owned fixture message")) || !bytes.Contains(joined, []byte("fixture reply:")) {
		t.Fatal("history omitted actual user/assistant content")
	}
	r, err := d.eng.ExecutionSnapshot(m.ID)
	if err != nil || r.OwnerRevision == 0 || !r.UnknownActors || r.Incomplete != "dev_fake_chat_subset" {
		t.Fatalf("message invented complete workflow custody: snapshot=%+v err=%v", r, err)
	}
	if att, e := c.Attach(id); e == nil {
		_ = att.Detach()
		t.Fatal("semantic readiness released raw fallback")
	}
	spool, err := shim.ReadHookSpoolFileResponse(filepath.Join(d.stateDir, m.ID, shim.HookSpoolFile), 0)
	if err != nil || len(spool.Records) == 0 {
		t.Fatal("retained primary identity missing", err)
	}
	var captured struct {
		Primary shimwire.ClaudePrimaryIdentity `json:"primary"`
	}
	if json.Unmarshal(spool.Records[0].Body, &captured) != nil || captured.Primary.PrimaryPID <= 0 {
		t.Fatal("primary identity missing")
	}
	stop := dialClient(t, d)
	if err := stop.Kill(id); err != nil {
		t.Fatal("owner Stop failed", err)
	}
	ended := func(pid int, birth int64) bool {
		current, e := procstart.StartTime(pid)
		if e == nil && current > 0 && current != birth {
			return true
		}
		return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && time.Now().Before(m.CreatedAt.Add(11*time.Second)) {
		if ended(captured.Primary.PrimaryPID, captured.Primary.PrimaryStartTime) && ended(m.ShimPID, m.ShimStartTime) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("owner Stop did not positively end both identities before the fixture's 12s autoexit")
}

func TestClaudeChatFixtureInitPublicationLagStaysPending(t *testing.T) {
	identity := claudeEvidenceIntent().Identity
	for _, kind := range []string{"exact", "durable-lag", "observed-lag", "partial", "foreign", "ahead", "unhealthy", "owner"} {
		t.Run(kind, func(t *testing.T) {
			snapshot := shimwire.ClaudePrimarySnapshot{Identity: identity, Healthy: true, ObservedSequence: 1, DurableSequence: 1}
			switch kind {
			case "durable-lag":
				snapshot.DurableSequence = 0
			case "observed-lag":
				snapshot.ObservedSequence = 0
			case "partial":
				snapshot.PendingPartial = true
			case "foreign":
				snapshot.Identity.ShimPID++
			case "ahead":
				snapshot.ObservedSequence = 2
				snapshot.DurableSequence = 2
			case "unhealthy":
				snapshot.Healthy = false
			case "owner":
				snapshot.OwnerRevision = 1
			}
			err := claudeFixtureInitPublication(snapshot, identity, 1)
			if kind == "exact" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			pending := kind == "durable-lag" || kind == "observed-lag" || kind == "partial"
			if pending != errors.Is(err, errClaudeFixturePending) || !pending && !errors.Is(err, errClaudeFixtureMessage) {
				t.Fatal("fixture publication classification changed authority", kind, err)
			}
		})
	}
}
