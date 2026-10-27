package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/shimwire"
)

// Local descriptor keeps the counterfactual compatible with the held original
// host draft, which did not yet export the strict decoder. No authority mock.
type claudeDispatchBound struct {
	Version               int                            `json:"v"`
	Kind                  string                         `json:"kind"`
	Identity              shimwire.ClaudePrimaryIdentity `json:"identity"`
	SessionInstance       string                         `json:"session_instance"`
	NativeUserUUID        string                         `json:"native_user_uuid"`
	TextSHA256            string                         `json:"text_sha256"`
	TextBytes             int                            `json:"text_bytes"`
	ExpectedOwnerRevision uint64                         `json:"expected_owner_revision"`
	OwnerRevision         uint64                         `json:"owner_revision"`
}

func claudeDispatchFresh(t *testing.T) (*Daemon, shimwire.ClaudeMessageRequest, claudeDispatchBound) {
	t.Helper()
	d := openDaemon(t, daemonConfig(t))
	identity := claudeFixtureStopTarget(t, d)
	deadline := time.Now().Add(5 * time.Second)
	var snapshot shimwire.ClaudePrimarySnapshot
	for {
		var err error
		snapshot, err = d.ClaudeFixtureSnapshot(identity.SessionID)
		if err == nil && snapshot.Healthy && !snapshot.PendingPartial && snapshot.ObservedSequence > 0 && snapshot.ObservedSequence == snapshot.DurableSequence {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatal("real fixture never reached captured init", err)
		}
		time.Sleep(time.Millisecond)
	}
	text := "one typed leaf message"
	digest := sha256.Sum256([]byte(text))
	request := shimwire.ClaudeMessageRequest{Version: 1, OperationID: "own-native-operation", NativeUserUUID: "1389ef09-4c19-4d50-8fdd-1fc95bdcfd4a", Text: text, Identity: identity, ExpectedOwnerRevision: 0, OwnerRevision: 1, ThroughStreamSequence: snapshot.ObservedSequence, ReaderRevision: snapshot.ReaderRevision}
	bound := claudeDispatchBound{1, "claude_fixture_message", identity, "owned-instance", request.NativeUserUUID, hex.EncodeToString(digest[:]), len(text), 0, 1}
	return d, request, bound
}

func claudeDispatchRecord(t *testing.T, d *Daemon, request shimwire.ClaudeMessageRequest, bound claudeDispatchBound, recordSession, recordInstance string) []byte {
	t.Helper()
	raw, err := json.Marshal(bound)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.ClaimComposerOperation(request.OperationID, "semantic_send", recordSession, recordInstance, "bound-request-hash"); err != nil {
		t.Fatal(err)
	}
	if err := d.BindSemanticRequest(request.OperationID, raw); err != nil {
		t.Fatal(err)
	}
	if err := d.BeginComposerOperation(request.OperationID); err != nil {
		t.Fatal(err)
	}
	return raw
}

func claudeDispatchNoEffects(t *testing.T, d *Daemon, request shimwire.ClaudeMessageRequest) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(d.cfg.StateDir, request.Identity.SessionID, "claude-fixture-transcript.jsonl"))
	if err != nil || len(raw) != 0 {
		t.Fatal("refused typed leaf changed genuine native transcript", err)
	}
	if !d.writeMu.TryLock() {
		t.Fatal("dispatch refusal retained writer custody")
	}
	d.writeMu.Unlock()
	deadline := time.Now().Add(time.Second)
	for {
		p, err := d.ClaudeFixtureSnapshot(request.Identity.SessionID)
		if err == nil {
			if p.OwnerRevision != 0 {
				t.Fatal("refused typed leaf attempted native input")
			}
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatal("refused leaf lost actual primary", err)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestClaudeChatDispatchBindsEveryDescriptorAndWireField(t *testing.T) {
	for _, kind := range []string{"version", "kind", "descriptor-digest", "descriptor-size", "wire-text", "wire-uuid", "wire-owner", "wire-expected", "wire-primary", "wire-session", "wire-native", "wire-shim", "wire-version", "wire-operation", "record-instance", "record-session"} {
		t.Run(kind, func(t *testing.T) {
			d, request, bound := claudeDispatchFresh(t)
			session, instance := request.Identity.SessionID, bound.SessionInstance
			switch kind {
			case "version":
				bound.Version = 2
			case "kind":
				bound.Kind = "other-native-input"
			case "descriptor-digest":
				bound.TextSHA256 = hex.EncodeToString(make([]byte, 32))
			case "descriptor-size":
				bound.TextBytes++
			case "wire-text":
				request.Text = "different exact text"
			case "wire-uuid":
				request.NativeUserUUID = "2389ef09-4c19-4d50-8fdd-1fc95bdcfd4a"
			case "wire-owner":
				request.OwnerRevision++
			case "wire-expected":
				request.ExpectedOwnerRevision++
			case "wire-primary":
				request.Identity.PrimaryStartTime++
			case "wire-session":
				request.Identity.SessionID = "another-session"
			case "wire-native":
				request.Identity.NativeSessionID = "2389ef09-4c19-4d50-8fdd-1fc95bdcfd4a"
			case "wire-shim":
				request.Identity.ShimStartTime++
			case "wire-version":
				request.Version = 2
			case "record-instance":
				instance = "different-instance"
			case "record-session":
				session = "different-session"
			}
			raw := claudeDispatchRecord(t, d, request, bound, session, instance)
			if kind == "wire-operation" {
				request.OperationID = "different-operation"
			}
			guard, err := d.PrepareClaudeFixtureDispatch(bound.Identity.SessionID, request, raw)
			if guard != nil {
				guard.Close()
			}
			if err == nil {
				t.Error("descriptor/wire counterfactual was prepared")
			}
			// Restore the observed target for the no-effect oracle; mutated expected
			// identity is not independently treated as process authority.
			request.Identity = bound.Identity
			claudeDispatchNoEffects(t, d, request)
		})
	}
}

func TestClaudeChatDispatchRefusesAnyOtherNativeMessageOperation(t *testing.T) {
	for _, kind := range []string{"prepared-empty", "prepared-lower-arbitrary", "executing-lower-arbitrary", "unknown-lower-arbitrary", "failed-empty", "completed-lower-arbitrary"} {
		t.Run(kind, func(t *testing.T) {
			d, request, bound := claudeDispatchFresh(t)
			if kind != "prepared-empty" && kind != "failed-empty" {
				request.ExpectedOwnerRevision, request.OwnerRevision = 1, 2
				bound.ExpectedOwnerRevision, bound.OwnerRevision = 1, 2
			}
			raw := claudeDispatchRecord(t, d, request, bound, request.Identity.SessionID, bound.SessionInstance)
			const other = "arbitrary-prior-operation"
			if _, _, err := d.ClaimComposerOperation(other, "semantic_send", request.Identity.SessionID, bound.SessionInstance, "other-request-hash"); err != nil {
				t.Fatal(err)
			}
			if kind != "prepared-empty" && kind != "failed-empty" {
				if err := d.BindSemanticRequest(other, []byte(`{"owner_revision":1,"arbitrary":true}`)); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "executing-lower-arbitrary", "unknown-lower-arbitrary", "completed-lower-arbitrary":
				if err := d.BeginComposerOperation(other); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "unknown-lower-arbitrary" {
				if err := d.idem.ResolveOutcomeUnknown(other); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "failed-empty" {
				if err := d.idem.Fail(other, nil); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "completed-lower-arbitrary" {
				if err := d.idem.Complete(other, []byte(`{"ok":true}`)); err != nil {
					t.Fatal(err)
				}
			}
			guard, err := d.PrepareClaudeFixtureDispatch(request.Identity.SessionID, request, raw)
			if guard != nil {
				guard.Close()
			}
			if err == nil {
				t.Error("other native operation was silently skipped")
			}
			claudeDispatchNoEffects(t, d, request)
		})
	}
}

func TestClaudeChatDispatchClosedAndUsedHandlesCannotWrite(t *testing.T) {
	t.Run("close-before-write", func(t *testing.T) {
		d, request, bound := claudeDispatchFresh(t)
		raw := claudeDispatchRecord(t, d, request, bound, request.Identity.SessionID, bound.SessionInstance)
		guard, err := d.PrepareClaudeFixtureDispatch(request.Identity.SessionID, request, raw)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(guard.Close)
		guard.Close()
		guard.Close()
		result, err := guard.Write()
		if err == nil || result.Status != "refused" {
			t.Error("closed host handle retained write authority")
		}
		claudeDispatchNoEffects(t, d, request)
	})
	t.Run("one-shot-even-after-refusal", func(t *testing.T) {
		d, request, bound := claudeDispatchFresh(t)
		raw := claudeDispatchRecord(t, d, request, bound, request.Identity.SessionID, bound.SessionInstance)
		guard, err := d.PrepareClaudeFixtureDispatch(request.Identity.SessionID, request, raw)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(guard.Close)
		d.mu.Lock()
		first, err := guard.Write()
		d.mu.Unlock()
		if err == nil || first.Status != "refused" {
			t.Fatal("busy current-row proof fabricated dispatch")
		}
		second, err := guard.Write()
		guard.Close()
		if err == nil || second.Status != "refused" {
			t.Error("used host handle acquired a later write")
		}
		claudeDispatchNoEffects(t, d, request)
	})
	t.Run("concurrent-close-write", func(t *testing.T) {
		d, request, bound := claudeDispatchFresh(t)
		raw := claudeDispatchRecord(t, d, request, bound, request.Identity.SessionID, bound.SessionInstance)
		guard, err := d.PrepareClaudeFixtureDispatch(request.Identity.SessionID, request, raw)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(guard.Close)
		start := make(chan struct{})
		written := make(chan shimwire.ClaudeMessageResult, 1)
		closed := make(chan struct{})
		go func() { <-start; r, _ := guard.Write(); written <- r }()
		go func() { <-start; guard.Close(); close(closed) }()
		close(start)
		result := <-written
		<-closed
		if result.Status != "written" && result.Status != "refused" {
			t.Fatal("unexpected concurrent lifetime result", result.Status)
		}
		if next, err := guard.Write(); err == nil || next.Status != "refused" {
			t.Error("closed concurrent handle remained usable")
		}
		if !d.writeMu.TryLock() {
			t.Fatal("concurrent Close retained writer custody")
		}
		d.writeMu.Unlock()
	})
}

func TestClaudeChatDispatchOwnBoundOperationUsesRealDialect(t *testing.T) {
	d, request, bound := claudeDispatchFresh(t)
	raw := claudeDispatchRecord(t, d, request, bound, request.Identity.SessionID, bound.SessionInstance)
	// Launch inventory is deliberately not a native-message census member.
	if _, _, err := d.idem.Prepare("legitimate-launch", "launch", request.Identity.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := d.idem.Complete("legitimate-launch", nil); err != nil {
		t.Fatal(err)
	}
	guard, err := d.PrepareClaudeFixtureDispatch(request.Identity.SessionID, request, raw)
	if err != nil {
		t.Fatal("own bound input refused", err)
	}
	t.Cleanup(guard.Close)
	result, err := guard.Write()
	guard.Close()
	if err != nil || result.Status != "written" {
		t.Fatal("real typed transport did not write", err, result.Status)
	}
	if !d.writeMu.TryLock() {
		t.Fatal("completed handle did not release custody")
	}
	d.writeMu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(filepath.Join(d.cfg.StateDir, request.Identity.SessionID, "claude-fixture-transcript.jsonl"))
		if err == nil {
			var row struct {
				Type string `json:"type"`
				UUID string `json:"uuid"`
			}
			for i, b := range data {
				if b == '\n' {
					if json.Unmarshal(data[:i], &row) == nil && row.Type == "user" && row.UUID == request.NativeUserUUID {
						return
					}
					break
				}
			}
		}
		if !time.Now().Before(deadline) {
			t.Fatal("transport receipt had no genuine fixture input witness")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestClaudeChatDispatchFreshnessRefusesBeforeNativeWrite(t *testing.T) {
	for _, kind := range []string{"reader", "sequence"} {
		t.Run(kind, func(t *testing.T) {
			d, request, bound := claudeDispatchFresh(t)
			raw := claudeDispatchRecord(t, d, request, bound, request.Identity.SessionID, bound.SessionInstance)
			if kind == "reader" {
				request.ReaderRevision++
			} else {
				request.ThroughStreamSequence++
			}
			guard, err := d.PrepareClaudeFixtureDispatch(request.Identity.SessionID, request, raw)
			if err != nil {
				t.Fatal("durable descriptor unexpectedly failed before live freshness check", err)
			}
			t.Cleanup(guard.Close)
			result, err := guard.Write()
			guard.Close()
			if err == nil || result.Status != "refused" {
				t.Error("stale live snapshot reached input")
			}
			claudeDispatchNoEffects(t, d, request)
		})
	}
}
