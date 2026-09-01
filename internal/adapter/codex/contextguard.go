package codex

// ContextGuard evidence is deliberately parsed separately from the interaction
// shaper. The fixtures in testdata/contextguard-*.json are sanitized frames
// shaped from `codex-cli 0.150.1`'s generated experimental JSON schema; the
// 0.151.0 schema (regenerated 2026-09-01) carries identical shapes for every
// method this parser accepts, and the 2026-09-01 live gate experiments
// exercised `thread/compact/start` against a real 0.151.0 app-server.
//
// Those gates came back NEGATIVE on the provider side (ADR-023 amendment 1):
// codex serializes nothing around compaction -- a compact sent mid-turn
// CANCELS the turn, and two concurrent compacts interrupt each other. The
// action descriptor therefore authorizes automatic dispatch only as a
// version-fenced capability claim; every concurrency guarantee is enforced by
// the daemon's dispatch lane (quiet revalidation inside the per-session
// composer lane, and never while a human holds the controls).

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/Nathandela/swarm/internal/adapter"
)

const maxContextGuardNotificationBytes = 64 << 10

const (
	contextGuardTokenUsageMethod      = "thread/tokenUsage/updated"
	contextGuardItemStartedMethod     = "item/started"
	contextGuardItemCompletedMethod   = "item/completed"
	contextGuardLegacyCompletedMethod = "thread/compacted"
	contextGuardItemType              = "contextCompaction"
)

// ParseContextGuardNotification maps exactly one bounded Codex JSON-RPC
// notification to exact occupancy or compaction lifecycle evidence. It accepts
// no partial/rounded/cumulative usage and returns no raw parser error.
func (codexAdapter) ParseContextGuardNotification(raw []byte, expectedThreadID string) (adapter.ContextGuardNotification, bool) {
	if len(raw) == 0 || len(raw) > maxContextGuardNotificationBytes || !adapter.IsCanonicalConversationID(expectedThreadID) {
		return adapter.ContextGuardNotification{}, false
	}
	root, ok := strictJSONObject(raw)
	// skeleton.rebuildFrame deliberately reassembles callbacks as {method,params}
	// with no jsonrpc member; accepting an invented envelope here would make the
	// production parser reject every actual backend notification.
	if !ok || !hasOnly(root, "method", "params") {
		return adapter.ContextGuardNotification{}, false
	}
	method := stringValue(root["method"])
	params, ok := objectValue(root["params"])
	if !ok {
		return adapter.ContextGuardNotification{}, false
	}
	switch method {
	case contextGuardTokenUsageMethod:
		return parseContextGuardUsage(params, expectedThreadID)
	case contextGuardItemStartedMethod:
		return parseContextGuardLifecycle(params, expectedThreadID, adapter.ContextGuardCompactionStarted, false)
	case contextGuardItemCompletedMethod:
		return parseContextGuardLifecycle(params, expectedThreadID, adapter.ContextGuardCompactionCompleted, false)
	case contextGuardLegacyCompletedMethod:
		// This intentionally narrow legacy shape is fixture-characterized. A
		// similarly named future method with extra fields fails closed.
		if !hasOnly(params, "threadId", "turnId") || !matchesThread(params, expectedThreadID) || stringValue(params["turnId"]) == "" {
			return adapter.ContextGuardNotification{}, false
		}
		return adapter.ContextGuardNotification{Kind: adapter.ContextGuardCompactionCompleted, ThreadID: expectedThreadID, Deprecated: true}, true
	default:
		return adapter.ContextGuardNotification{}, false
	}
}

func parseContextGuardUsage(params map[string]any, expectedThreadID string) (adapter.ContextGuardNotification, bool) {
	if !hasOnly(params, "threadId", "turnId", "tokenUsage") || !matchesThread(params, expectedThreadID) || stringValue(params["turnId"]) == "" {
		return adapter.ContextGuardNotification{}, false
	}
	usage, ok := objectValue(params["tokenUsage"])
	if !ok || !hasOnly(usage, "last", "total", "modelContextWindow") {
		return adapter.ContextGuardNotification{}, false
	}
	last, ok := objectValue(usage["last"])
	if !ok || !validTokenUsageBreakdown(last) {
		return adapter.ContextGuardNotification{}, false
	}
	total, ok := objectValue(usage["total"])
	if !ok || !validTokenUsageBreakdown(total) {
		return adapter.ContextGuardNotification{}, false
	}
	used, ok := uintValue(last["totalTokens"])
	if !ok {
		return adapter.ContextGuardNotification{}, false
	}
	limit, ok := uintValue(usage["modelContextWindow"])
	if !ok || limit == 0 {
		return adapter.ContextGuardNotification{}, false
	}
	return adapter.ContextGuardNotification{
		Kind: adapter.ContextGuardUsage, ThreadID: expectedThreadID,
		UsedTokens: used, ContextLimit: limit, Quality: adapter.ContextGuardExact,
	}, true
}

func parseContextGuardLifecycle(params map[string]any, expectedThreadID string, kind adapter.ContextGuardEventKind, deprecated bool) (adapter.ContextGuardNotification, bool) {
	timestampKey := "startedAtMs"
	if kind == adapter.ContextGuardCompactionCompleted {
		timestampKey = "completedAtMs"
	}
	if !hasOnly(params, "item", timestampKey, "threadId", "turnId") || !matchesThread(params, expectedThreadID) || stringValue(params["turnId"]) == "" {
		return adapter.ContextGuardNotification{}, false
	}
	if _, ok := uintValue(params[timestampKey]); !ok {
		return adapter.ContextGuardNotification{}, false
	}
	item, ok := objectValue(params["item"])
	if !ok || !hasOnly(item, "id", "type") || stringValue(item["id"]) == "" || stringValue(item["type"]) != contextGuardItemType {
		return adapter.ContextGuardNotification{}, false
	}
	return adapter.ContextGuardNotification{Kind: kind, ThreadID: expectedThreadID, Deprecated: deprecated}, true
}

func validTokenUsageBreakdown(object map[string]any) bool {
	if !hasOnly(object, "cachedInputTokens", "inputTokens", "outputTokens", "reasoningOutputTokens", "totalTokens") &&
		!hasOnly(object, "cachedInputTokens", "inputTokens", "outputTokens", "reasoningOutputTokens", "totalTokens", "cacheWriteInputTokens") {
		return false
	}
	for _, key := range []string{"cachedInputTokens", "inputTokens", "outputTokens", "reasoningOutputTokens", "totalTokens"} {
		if _, ok := uintValue(object[key]); !ok {
			return false
		}
	}
	if value, ok := object["cacheWriteInputTokens"]; ok {
		if _, ok := uintValue(value); !ok {
			return false
		}
	}
	return true
}

// ContextGuardAction describes the native action for the characterized
// 0.150.x and 0.151.x families (schema-identical for every accepted method).
// AutomaticDispatch is a capability claim only -- ADR-023 amendment 1 records
// that the provider itself serializes nothing, so the daemon's dispatch lane
// and effect-window gate own every concurrency guarantee -- and it is granted
// ONLY to the exact versions live-gated against a real provider (the
// allowlist below). Everything else in the characterized families stays
// observe-only: schema shape identity proves message shapes, not that
// thread/compact/start behaves identically. An uncharacterized version (0.152
// and beyond, until someone regenerates and compares its schema) yields no
// action at all, which downgrades the whole guard to unsupported rather than
// dispatching against unknown semantics.
func (codexAdapter) ContextGuardAction(version string) (adapter.ContextGuardAction, bool) {
	if !characterizedContextGuardVersion(version) {
		return adapter.ContextGuardAction{}, false
	}
	action := adapter.ContextGuardAction{
		Method: "thread/compact/start", ThreadIDParameter: "threadId",
		Support: adapter.ContextGuardObserveOnly,
	}
	if liveGatedContextGuardVersion(version) {
		action.AutomaticDispatch = true
		action.Support = adapter.ContextGuardAutomatic
	}
	return action, true
}

// liveGatedContextGuardVersions is the EXACT allowlist of versions whose
// thread/compact/start behavior was exercised against a live provider, not
// merely schema-compared. Compaction is destructive and non-idempotent, so a
// patch release is not trusted sight-unseen: extending this list requires
// regenerating and comparing the version's schema AND re-running the live
// gates recorded in docs/verification/context-guard.md. A version outside the
// list but inside a characterized family downgrades to observe-only.
var liveGatedContextGuardVersions = map[string]bool{
	"0.151.0": true, // 2026-09-01 live gates (both negative; daemon-side enforcement)
}

// ContextGuardContinuation shapes the post-compaction continuation enqueue
// (ADR-023 amendment 2) as codex's native thread/queue/add. Live-verified on
// 0.151.0 (2026-09-01, docs/design/context-guard-continuation.md): a queued
// submission DEFERS behind a running compaction and auto-starts ~31ms after
// contextCompaction completes, but DRAINS INSTANTLY (~20ms, becoming a turn)
// on an idle thread -- so the caller must enqueue only while its own
// compaction is provably running, or deliberately at latch. Fenced to the
// exact live-gated versions: queue-versus-compaction ordering is precisely
// the behavior a new version's live-gate rerun must re-verify.
// clientUserMessageId is provenance, NOT an idempotency key (two adds with
// the same id both run); at-most-once belongs to the daemon worker.
func (codexAdapter) ContextGuardContinuation(version, threadID, messageID, text string) (string, map[string]any, bool) {
	if !liveGatedContextGuardVersion(version) || threadID == "" || messageID == "" || text == "" {
		return "", nil, false
	}
	return "thread/queue/add", map[string]any{
		"threadId":            threadID,
		"clientUserMessageId": messageID,
		"input":               []map[string]any{{"type": "text", "text": text}},
	}, true
}

func liveGatedContextGuardVersion(version string) bool {
	return liveGatedContextGuardVersions[version]
}

func characterizedContextGuardVersion(version string) bool {
	p := strings.Split(version, ".")
	if len(p) != 3 || p[0] != "0" || (p[1] != "150" && p[1] != "151") || p[2] == "" {
		return false
	}
	for _, c := range p[2] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func matchesThread(object map[string]any, expected string) bool {
	return stringValue(object["threadId"]) == expected
}

func stringValue(value any) string {
	s, _ := value.(string)
	return s
}

func objectValue(value any) (map[string]any, bool) {
	m, ok := value.(map[string]any)
	return m, ok
}

func hasOnly(object map[string]any, keys ...string) bool {
	if len(object) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, ok := object[key]; !ok {
			return false
		}
	}
	return true
}

func uintValue(value any) (uint64, bool) {
	n, ok := value.(json.Number)
	if !ok || n == "" {
		return 0, false
	}
	for _, c := range n {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	v, err := strconv.ParseUint(string(n), 10, 64)
	return v, err == nil && v <= math.MaxInt64
}

// strictJSONObject decodes one object with bounded nesting and rejects duplicate
// keys at every level. encoding/json's ordinary map decode silently accepts
// duplicates, which is unsafe for provider provenance.
func strictJSONObject(raw []byte) (map[string]any, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	v, ok := strictJSONValue(dec, 0)
	if !ok {
		return nil, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, false
	}
	object, ok := objectValue(v)
	return object, ok
}

func strictJSONValue(dec *json.Decoder, depth int) (any, bool) {
	if depth > 32 {
		return nil, false
	}
	token, err := dec.Token()
	if err != nil {
		return nil, false
	}
	delim, compound := token.(json.Delim)
	if !compound {
		return token, true
	}
	switch delim {
	case '{':
		object := make(map[string]any)
		for dec.More() {
			keyToken, err := dec.Token()
			key, ok := keyToken.(string)
			if err != nil || !ok {
				return nil, false
			}
			if _, duplicate := object[key]; duplicate {
				return nil, false
			}
			value, ok := strictJSONValue(dec, depth+1)
			if !ok {
				return nil, false
			}
			object[key] = value
		}
		end, err := dec.Token()
		return object, err == nil && end == json.Delim('}')
	case '[':
		array := make([]any, 0)
		for dec.More() {
			value, ok := strictJSONValue(dec, depth+1)
			if !ok {
				return nil, false
			}
			array = append(array, value)
		}
		end, err := dec.Token()
		return array, err == nil && end == json.Delim(']')
	default:
		return nil, false
	}
}
