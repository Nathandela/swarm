package skeleton

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/idempotency"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/shimwire"
)

func claudeEvidenceIntent() claudeFixtureIntent {
	h := sha256.Sum256([]byte("exact text"))
	return daemon.ClaudeFixtureIntent{Version: 1, Kind: "claude_fixture_message", Identity: shimwire.ClaudePrimaryIdentity{SessionID: "fixture", RequiredRuntime: "claude-chat", NativeSessionID: "1f700d8f-d3e3-42c1-9ab0-dcdb22f27772", ShimPID: 10, ShimStartTime: 20, PrimaryPID: 30, PrimaryStartTime: 40}, SessionInstance: "fixture-instance", NativeUserUUID: "1389ef09-4c19-4d50-8fdd-1fc95bdcfd4a", TextSHA256: hex.EncodeToString(h[:]), TextBytes: 10, OwnerRevision: 1}
}

func TestClaudeChatEvidenceFirstWriteThenMonotoneEchoCheckpoint(t *testing.T) {
	d := assemble(t)
	intent := claudeEvidenceIntent()
	for _, order := range []string{"ack-first", "echo-first", "lost-ack-first"} {
		t.Run(order, func(t *testing.T) {
			op := order
			bound, _ := json.Marshal(intent)
			if _, _, err := d.core.ClaimComposerOperation(op, "semantic_send", "fixture", intent.SessionInstance, "hash"); err != nil {
				t.Fatal(err)
			}
			if err := d.core.BindSemanticRequest(op, bound); err != nil {
				t.Fatal(err)
			}
			if err := d.core.BeginComposerOperation(op); err != nil {
				t.Fatal(err)
			}
			s := &claudeFixtureState{operation: op, intent: intent, evidence: claudeFixtureEvidence{Version: 1, Kind: "claude_fixture_message", OperationID: op, Intent: intent, State: "prepared", RawHashes: []string{strings.Repeat("a", 64)}}}
			apply := func(state string, seq uint64) {
				t.Helper()
				hash := ""
				if seq > 0 {
					hash = strings.Repeat("b", 64)
				}
				if err := d.checkpointClaudeFixture(s, state, seq, "", hash); err != nil {
					t.Fatalf("initial/merged checkpoint refused: %v", err)
				}
			}
			switch order {
			case "ack-first":
				apply("submitted", 0)
				apply("admitted", 2)
			case "echo-first":
				apply("admitted", 2)
				apply("submitted", 0)
			case "lost-ack-first":
				apply("uncertain", 0)
				apply("admitted", 2)
			}
			rec, _ := d.core.ComposerOperation(op)
			got := d.claudeFixtureReceipt("fixture", op, rec)
			if got.State != "admitted" || got.NativeMessageID != intent.NativeUserUUID {
				t.Fatal("ACK order erased exact native admission", got)
			}
		})
	}
}

func TestClaudeChatReceiptRejectsUnknownRetainedEvidence(t *testing.T) {
	intent := claudeEvidenceIntent()
	bound, _ := json.Marshal(intent)
	valid := claudeFixtureEvidence{Version: 1, Kind: "claude_fixture_message", OperationID: "op", Intent: intent, State: "completed", EchoSequence: 2, AssistantSequence: 3, ResultSequence: 4, AssistantUUID: "79b79d77-e62d-44b9-8e24-94e908f08935", RawHashes: []string{strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), strings.Repeat("d", 64)}}
	d := &Daemon{}
	for _, tc := range []struct {
		name   string
		mutate func(*claudeFixtureEvidence)
	}{
		{"valid", func(*claudeFixtureEvidence) {}},
		{"future-version", func(e *claudeFixtureEvidence) { e.Version = 2 }},
		{"wrong-kind", func(e *claudeFixtureEvidence) { e.Kind = "unowned" }},
		{"unknown-state", func(e *claudeFixtureEvidence) { e.State = "anything" }},
		{"missing-echo", func(e *claudeFixtureEvidence) { e.EchoSequence = 0 }},
		{"wrong-assistant", func(e *claudeFixtureEvidence) { e.AssistantUUID = e.Intent.NativeUserUUID }},
		{"missing-result", func(e *claudeFixtureEvidence) { e.ResultSequence = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := valid
			tc.mutate(&e)
			raw, _ := json.Marshal(e)
			rec := idempotency.Record{OperationID: "op", Action: "semantic_send", SessionID: "fixture", SessionInstance: intent.SessionInstance, Phase: idempotency.PhaseCompleted, Request: bound, Evidence: raw}
			got := d.claudeFixtureReceipt("fixture", "op", rec)
			if tc.name == "valid" && got.State != "completed" {
				t.Fatal("valid durable proof refused", got)
			}
			if tc.name != "valid" && got.State != "uncertain" {
				t.Fatal("unsupported retained evidence promoted", got)
			}
		})
	}
}

func TestClaudeChatReceiptKnownPrewriteRefusalIsRejectedOnlyWithExactOutcome(t *testing.T) {
	intent := claudeEvidenceIntent()
	bound, _ := json.Marshal(intent)
	d := &Daemon{}
	for _, tc := range []struct {
		name  string
		phase idempotency.Phase
		code  protocol.ErrorCode
		want  string
	}{{"known-refusal", idempotency.PhaseFailed, protocol.CodeInputBusy, "rejected"}, {"unknown-cause", idempotency.PhaseFailed, protocol.CodeOutcomeUnknown, "uncertain"}, {"unresolved", idempotency.PhaseExecuting, protocol.CodeInputBusy, "uncertain"}} {
		t.Run(tc.name, func(t *testing.T) {
			out, _ := json.Marshal(map[string]any{"ok": false, "code": tc.code, "message": "fixture chat input custody is unavailable"})
			got := d.claudeFixtureReceipt("fixture", "op", idempotency.Record{OperationID: "op", Action: "semantic_send", SessionID: "fixture", SessionInstance: intent.SessionInstance, Phase: tc.phase, Request: bound, Outcome: out})
			if got.State != tc.want {
				t.Fatal("wrong refusal classification", got)
			}
		})
	}
}
