package codex

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
)

const contextGuardThread = "018f47aa-2fcb-7b9f-8e3d-7f8b8a9c0d1e"

// These are sanitized shapes from the installed Codex 0.150.1 generated schema,
// not assertions about an observed runtime action response.
func contextGuardFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func contextGuardSource(t *testing.T) adapter.ContextGuardSource {
	t.Helper()
	s, ok := adapter.AsContextGuardSource(New())
	if !ok {
		t.Fatal("Codex does not expose the optional pure ContextGuard source")
	}
	return s
}

func TestContextGuardTokenUsageUsesLastTotalTokensOnly(t *testing.T) {
	raw := contextGuardFixture(t, "contextguard-token-usage.json")
	if bytes.Contains(raw, []byte(`"jsonrpc"`)) {
		t.Fatal("fixture is not the production rebuildFrame {method,params} envelope")
	}
	e, ok := contextGuardSource(t).ParseContextGuardNotification(raw, contextGuardThread)
	if !ok {
		t.Fatal("recorded/sanitized exact token usage was rejected")
	}
	if e.Kind != adapter.ContextGuardUsage || e.Quality != adapter.ContextGuardExact || e.ThreadID != contextGuardThread {
		t.Fatalf("usage event = %+v", e)
	}
	if e.UsedTokens != 800 || e.ContextLimit != 1000 {
		t.Fatalf("usage=%d/%d, want last.totalTokens 800/1000 (never tokenUsage.total)", e.UsedTokens, e.ContextLimit)
	}
}

func TestContextGuardLifecycleEvidence(t *testing.T) {
	s := contextGuardSource(t)
	for _, tc := range []struct {
		fixture string
		kind    adapter.ContextGuardEventKind
		legacy  bool
	}{
		{"contextguard-item-started.json", adapter.ContextGuardCompactionStarted, false},
		{"contextguard-item-completed.json", adapter.ContextGuardCompactionCompleted, false},
		{"contextguard-thread-compacted.json", adapter.ContextGuardCompactionCompleted, true},
	} {
		e, ok := s.ParseContextGuardNotification(contextGuardFixture(t, tc.fixture), contextGuardThread)
		if !ok || e.Kind != tc.kind || e.Deprecated != tc.legacy {
			t.Errorf("%s = (%+v,%v), want kind=%s deprecated=%v", tc.fixture, e, ok, tc.kind, tc.legacy)
		}
	}
}

func TestContextGuardParserFailsClosed(t *testing.T) {
	s := contextGuardSource(t)
	for _, tc := range []struct {
		name string
		raw  []byte
		id   string
	}{
		{"wrong-thread", contextGuardFixture(t, "contextguard-token-usage.json"), "018f47aa-2fcb-7b9f-8e3d-7f8b8a9c0d1f"},
		{"duplicate-key", []byte(`{"jsonrpc":"2.0","method":"thread/tokenUsage/updated","method":"thread/tokenUsage/updated","params":{}}`), contextGuardThread},
		{"negative", []byte(`{"jsonrpc":"2.0","method":"thread/tokenUsage/updated","params":{"threadId":"018f47aa-2fcb-7b9f-8e3d-7f8b8a9c0d1e","tokenUsage":{"last":{"totalTokens":-1}},"modelContextWindow":1000}}`), contextGuardThread},
		{"overflow", []byte(`{"jsonrpc":"2.0","method":"thread/tokenUsage/updated","params":{"threadId":"018f47aa-2fcb-7b9f-8e3d-7f8b8a9c0d1e","tokenUsage":{"last":{"totalTokens":18446744073709551616}},"modelContextWindow":1000}}`), contextGuardThread},
		{"fractional", []byte(`{"jsonrpc":"2.0","method":"thread/tokenUsage/updated","params":{"threadId":"018f47aa-2fcb-7b9f-8e3d-7f8b8a9c0d1e","tokenUsage":{"last":{"totalTokens":1.5}},"modelContextWindow":1000}}`), contextGuardThread},
		{"zero-window", []byte(`{"jsonrpc":"2.0","method":"thread/tokenUsage/updated","params":{"threadId":"018f47aa-2fcb-7b9f-8e3d-7f8b8a9c0d1e","tokenUsage":{"last":{"totalTokens":1}},"modelContextWindow":0}}`), contextGuardThread},
		{"null-window", []byte(`{"method":"thread/tokenUsage/updated","params":{"threadId":"018f47aa-2fcb-7b9f-8e3d-7f8b8a9c0d1e","turnId":"018f47aa-2fcb-7b9f-8e3d-7f8b8a9c0d1d","tokenUsage":{"last":{"cachedInputTokens":0,"inputTokens":0,"outputTokens":0,"reasoningOutputTokens":0,"totalTokens":1},"total":{"cachedInputTokens":0,"inputTokens":0,"outputTokens":0,"reasoningOutputTokens":0,"totalTokens":1},"modelContextWindow":null}}}`), contextGuardThread},
		{"missing-required-last-field", []byte(`{"method":"thread/tokenUsage/updated","params":{"threadId":"018f47aa-2fcb-7b9f-8e3d-7f8b8a9c0d1e","turnId":"018f47aa-2fcb-7b9f-8e3d-7f8b8a9c0d1d","tokenUsage":{"last":{"inputTokens":0,"outputTokens":0,"reasoningOutputTokens":0,"totalTokens":1},"total":{"cachedInputTokens":0,"inputTokens":0,"outputTokens":0,"reasoningOutputTokens":0,"totalTokens":1},"modelContextWindow":1000}}}`), contextGuardThread},
		{"total-without-last", []byte(`{"jsonrpc":"2.0","method":"thread/tokenUsage/updated","params":{"threadId":"018f47aa-2fcb-7b9f-8e3d-7f8b8a9c0d1e","tokenUsage":{"total":999999},"modelContextWindow":1000}}`), contextGuardThread},
		{"unknown-item-type", []byte(`{"jsonrpc":"2.0","method":"item/started","params":{"threadId":"018f47aa-2fcb-7b9f-8e3d-7f8b8a9c0d1e","item":{"type":"agentMessage"}}}`), contextGuardThread},
		{"malformed", []byte(`{"jsonrpc":`), contextGuardThread},
		{"oversized", bytes.Repeat([]byte{'x'}, 64<<10), contextGuardThread},
	} {
		if e, ok := s.ParseContextGuardNotification(tc.raw, tc.id); ok {
			t.Errorf("%s accepted %+v", tc.name, e)
		}
	}
}

func TestContextGuardActionIsObserveOnlyAtCharacterizedVersion(t *testing.T) {
	s := contextGuardSource(t)
	a, ok := s.ContextGuardAction("0.150.7")
	if !ok || a.Method != "thread/compact/start" || a.ThreadIDParameter != "threadId" || a.AutomaticDispatch || a.Support != adapter.ContextGuardObserveOnly {
		t.Fatalf("0.150 action=%+v ok=%v; native descriptor must remain observe-only", a, ok)
	}
	if _, ok := s.ContextGuardAction("0.151.0"); ok {
		t.Fatal("uncharacterized Codex version exposed a ContextGuard action")
	}
}

func FuzzContextGuardNotificationTotal(f *testing.F) {
	f.Add([]byte(`{"jsonrpc":"2.0","method":"thread/tokenUsage/updated","params":{"threadId":"018f47aa-2fcb-7b9f-8e3d-7f8b8a9c0d1e","tokenUsage":{"last":{"totalTokens":800}},"modelContextWindow":1000}}`), contextGuardThread)
	f.Fuzz(func(t *testing.T, raw []byte, thread string) {
		_, _ = contextGuardSource(t).ParseContextGuardNotification(raw, thread)
	})
}
