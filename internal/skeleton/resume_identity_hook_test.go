package skeleton

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/claude"
	"github.com/Nathandela/swarm/internal/engine"
)

const hookConversationID = "1389ef09-4c19-4d50-8fdd-1fc95bdcfd4a"

func marshalHookCallback(t *testing.T, cb engine.Callback) []byte {
	t.Helper()
	raw, err := json.Marshal(cb)
	if err != nil {
		t.Fatalf("marshal hook callback: %v", err)
	}
	return raw
}

func TestResumeIdentity_IngestAuthenticatesBeforePersistingAndThenShapes(t *testing.T) {
	sk := assemble(t)
	m := launchFake(t, sk, "print HOOK-IDENTITY\nidle 60s\n")
	token := hookTokenFor(t, sk.stateDir, m.ID)
	sk.adapterFor = func(string) (adapter.Adapter, bool) { return claude.New(), true }
	body := json.RawMessage(`{"session_id":"` + hookConversationID +
		`","last_assistant_message":"authenticated identity still shapes"}`)

	bad := marshalHookCallback(t, engine.Callback{
		SessionID: m.ID, Token: "not-the-session-token", Sequence: 1, Event: "Stop", Raw: body,
	})
	if err := sk.ingestHookBytes(bad); err == nil {
		t.Fatal("ingestHookBytes accepted a foreign hook token")
	}
	if got, _ := sk.Core().Get(m.ID); got.ConversationID != "" {
		t.Fatalf("unauthenticated callback persisted ConversationID %q", got.ConversationID)
	}
	if items := interactionItems(t, sk, m.ID); len(items) != 0 {
		t.Fatalf("unauthenticated callback shaped %d item(s): %v", len(items), items)
	}

	good := marshalHookCallback(t, engine.Callback{
		SessionID: m.ID, Token: token, Sequence: 1, Event: "Stop", Raw: body,
	})
	if err := sk.ingestHookBytes(good); err != nil {
		t.Fatalf("ingest authenticated callback: %v", err)
	}
	if got, _ := sk.Core().Get(m.ID); got.ConversationID != hookConversationID {
		t.Fatalf("authenticated callback persisted ConversationID %q, want %q", got.ConversationID, hookConversationID)
	}
	items := awaitItems(t, sk, m.ID, 1)
	if got := itemString(t, items[0], "text"); got != "authenticated identity still shapes" {
		t.Fatalf("shaped text = %q, want callback content", got)
	}
}

// TestResumeIdentity_HookPersistenceErrorDoesNotSuppressShapingAndSameIDRetries separates two
// obligations: identity storage is best-effort for this callback, while interaction shaping is
// still required; the next event carrying the same id retries the write and makes it durable.
func TestResumeIdentity_HookPersistenceErrorDoesNotSuppressShapingAndSameIDRetries(t *testing.T) {
	sk := assemble(t)
	m := launchFake(t, sk, "print HOOK-IDENTITY-RETRY\nidle 60s\n")
	token := hookTokenFor(t, sk.stateDir, m.ID)
	sk.adapterFor = func(string) (adapter.Adapter, bool) { return claude.New(), true }

	sessionDir := filepath.Join(sk.stateDir, m.ID)
	hold := sessionDir + ".hold"
	if err := os.Rename(sessionDir, hold); err != nil {
		t.Fatalf("move session dir: %v", err)
	}
	if err := os.WriteFile(sessionDir, []byte("blocks Store.Save"), 0o600); err != nil {
		t.Fatalf("install write blocker: %v", err)
	}
	restored := false
	t.Cleanup(func() {
		if !restored {
			_ = os.Remove(sessionDir)
			_ = os.Rename(hold, sessionDir)
		}
	})

	firstBody := json.RawMessage(`{"session_id":"` + hookConversationID +
		`","last_assistant_message":"shape despite identity write failure"}`)
	first := marshalHookCallback(t, engine.Callback{
		SessionID: m.ID, Token: token, Sequence: 1, Event: "Stop", Raw: firstBody,
	})
	if err := sk.ingestHookBytes(first); err != nil {
		t.Fatalf("identity persistence failure suppressed callback ingest: %v", err)
	}
	if got, _ := sk.Core().Get(m.ID); got.ConversationID != "" {
		t.Fatalf("blocked persistence stored ConversationID %q", got.ConversationID)
	}
	items := awaitItems(t, sk, m.ID, 1)
	if got := itemString(t, items[0], "text"); got != "shape despite identity write failure" {
		t.Fatalf("first shaped text = %q", got)
	}

	if err := os.Remove(sessionDir); err != nil {
		t.Fatalf("remove write blocker: %v", err)
	}
	if err := os.Rename(hold, sessionDir); err != nil {
		t.Fatalf("restore session dir: %v", err)
	}
	restored = true

	secondBody := json.RawMessage(`{"session_id":"` + hookConversationID +
		`","last_assistant_message":"same identity retries"}`)
	second := marshalHookCallback(t, engine.Callback{
		SessionID: m.ID, Token: token, Sequence: 2, Event: "Stop", Raw: secondBody,
	})
	if err := sk.ingestHookBytes(second); err != nil {
		t.Fatalf("same-id retry callback: %v", err)
	}
	if got, _ := sk.Core().Get(m.ID); got.ConversationID != hookConversationID {
		t.Fatalf("same-id retry persisted %q, want %q", got.ConversationID, hookConversationID)
	}
	_ = awaitItems(t, sk, m.ID, 2)

	const conflictingID = "2389ef09-4c19-4d50-8fdd-1fc95bdcfd4b"
	thirdBody := json.RawMessage(`{"session_id":"` + conflictingID +
		`","last_assistant_message":"later identity cannot repoint"}`)
	third := marshalHookCallback(t, engine.Callback{
		SessionID: m.ID, Token: token, Sequence: 3, Event: "Stop", Raw: thirdBody,
	})
	if err := sk.ingestHookBytes(third); err != nil {
		t.Fatalf("conflicting-id callback: %v", err)
	}
	if got, _ := sk.Core().Get(m.ID); got.ConversationID != hookConversationID {
		t.Fatalf("later hook id repointed write-once ConversationID to %q", got.ConversationID)
	}
}

// TestResumeIdentity_ConcurrentHookAndBackendCaptureConverge exercises the daemon's write-once
// metadata RMW under -race. This is a write-seam race rather than a realistic same-provider
// producer pair: both authoritative seams report the same UUID, and neither may lose it,
// deadlock, or leave an empty durable value.
func TestResumeIdentity_ConcurrentHookAndBackendCaptureConverge(t *testing.T) {
	sk := assemble(t)
	m := launchFake(t, sk, "print HOOK-BACKEND-RACE\nidle 60s\n")
	token := hookTokenFor(t, sk.stateDir, m.ID)
	sk.adapterFor = func(string) (adapter.Adapter, bool) { return claude.New(), true }
	body := json.RawMessage(`{"session_id":"` + hookConversationID + `"}`)
	raw := marshalHookCallback(t, engine.Callback{
		SessionID: m.ID, Token: token, Sequence: 1, Event: "Notification", Raw: body,
	})

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 1)
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		errs <- sk.ingestHookBytes(raw)
	}()
	go func() {
		defer wg.Done()
		<-start
		sk.adoptBackendThread(m.ID, hookConversationID)
	}()
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("authenticated hook ingest: %v", err)
		}
	}
	if got, _ := sk.Core().Get(m.ID); got.ConversationID != hookConversationID {
		t.Fatalf("concurrent producers persisted %q, want %q", got.ConversationID, hookConversationID)
	}
}
