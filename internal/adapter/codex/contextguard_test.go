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
		{"above-schema-int64", []byte(`{"method":"thread/tokenUsage/updated","params":{"threadId":"018f47aa-2fcb-7b9f-8e3d-7f8b8a9c0d1e","turnId":"018f47aa-2fcb-7b9f-8e3d-7f8b8a9c0d1d","tokenUsage":{"last":{"cachedInputTokens":0,"inputTokens":0,"outputTokens":0,"reasoningOutputTokens":0,"totalTokens":9223372036854775808},"total":{"cachedInputTokens":0,"inputTokens":0,"outputTokens":0,"reasoningOutputTokens":0,"totalTokens":1},"modelContextWindow":1000}}}`), contextGuardThread},
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

func TestContextGuardActionIsAutomaticAtCharacterizedVersions(t *testing.T) {
	// ADR-023 amendment 1: automatic dispatch is a capability claim granted
	// ONLY to the exact versions live-gated against a real provider (0.151.0,
	// 2026-09-01). Compaction is destructive and non-idempotent, so even a
	// patch release of the gated minor is not trusted sight-unseen: everything
	// else in the characterized families -- 0.150.x entirely, and 0.151.x
	// patches never gated -- stays observe-only.
	s := contextGuardSource(t)
	for _, version := range []string{"0.151.0"} {
		a, ok := s.ContextGuardAction(version)
		if !ok || a.Method != "thread/compact/start" || a.ThreadIDParameter != "threadId" || !a.AutomaticDispatch || a.Support != adapter.ContextGuardAutomatic {
			t.Fatalf("%s action=%+v ok=%v; want the automatic native descriptor", version, a, ok)
		}
	}
	for _, version := range []string{"0.150.0", "0.150.7", "0.151.3"} {
		a, ok := s.ContextGuardAction(version)
		if !ok || a.Method != "thread/compact/start" || a.AutomaticDispatch || a.Support != adapter.ContextGuardObserveOnly {
			t.Fatalf("%s action=%+v ok=%v; want the observe-only descriptor (compact behavior never live-gated)", version, a, ok)
		}
	}
	// An uncharacterized version yields NO action: the guard downgrades to
	// unsupported rather than dispatching against unknown semantics.
	for _, version := range []string{"0.149.9", "0.152.0", "1.150.0", "0.150.x"} {
		if _, ok := s.ContextGuardAction(version); ok {
			t.Fatalf("uncharacterized Codex version %s exposed a ContextGuard action", version)
		}
	}
}

func FuzzContextGuardNotificationTotal(f *testing.F) {
	f.Add([]byte(`{"jsonrpc":"2.0","method":"thread/tokenUsage/updated","params":{"threadId":"018f47aa-2fcb-7b9f-8e3d-7f8b8a9c0d1e","tokenUsage":{"last":{"totalTokens":800}},"modelContextWindow":1000}}`), contextGuardThread)
	f.Fuzz(func(t *testing.T, raw []byte, thread string) {
		_, _ = contextGuardSource(t).ParseContextGuardNotification(raw, thread)
	})
}
