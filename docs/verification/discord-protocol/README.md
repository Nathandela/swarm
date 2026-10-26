# Discord provenance and native input evidence

Date: 2026-10-26. This records source-level protocol work and isolated fixtures. It is not five-provider runtime qualification or a deployment record.

## Additive protocol contract

Clients request `semantic-send` and `message-origin` in hello. `ComposerSendReq.Origin` is an optional `MessageOrigin` containing `kind`, `installation_id`, `request_id`, `actor_id`, `actor_name`, `guild_id`, `channel_id`, `message_id`, `endorser_id`, and `requester_id`. The original quoted author and actual invoker remain distinct.

Kinds are `external`, `owner_direct`, and `owner_endorsed`. Installation, request, and actor IDs are required; endorsement requires an endorser, and other kinds prohibit that field. Each field is at most 512 UTF-8 bytes and rejects control characters. This is an authenticated caller's provenance declaration; Discord actor/owner verification belongs to the bot. Transport `source=owner` is not proof of human owner authority.

The client refuses an origin-bearing send if the daemon did not negotiate `message-origin`; the server also refuses an unnegotiated origin before custody/provider I/O. The canonical signed/idempotent content digest binds every origin field, including an explicit domain field. An omitted origin preserves the existing digest exactly, including legacy phone signatures.

The non-OpenCode composer copies origin before recording pending input and binds it in the durable operation's immutable request descriptor. Status/receipts return retained origin even when a provider can prove submission only. Origin is attached to a native history item only with exact correlation: Codex's echoed client message ID, or OpenCode's retained native intent plus verified message/session identity, creation boundary, and full text hash. Claude's existing time-bounded exact-text correlation cannot prove origin: it keeps its existing transport attribution and operation correlation but adds no origin. Unknown imported native messages remain unattributed. Exact owned OpenCode rows can reconcile imported history without submitting again.

## TDD and fixtures

Failing-first protocol tests compiled against missing origin schema/capability, then passed after the additive implementation. Connected skeleton tests compiled against missing pending/receipt/intent fields, then passed after propagation. They cover immutable caller copies, exact echo, no authority from text coincidence, retained OpenCode receipts, exact imported history, and daemon restart without replay.

The old-shim regression initially returned success after two raw writes although its hello did not prove atomic submission. The fix requires the current upstream's `SupportsMessageSubmit` before durable Begin; the fallback raw writes were removed. Unsupported survivors return `structured_unsupported` with explicit session refresh guidance. A daemon restart preserves existing shims and cannot upgrade their capability. No automatic restart is used.

Validated on this branch:

- `go test ./internal/protocol/...` — pass.
- Connected skeleton composer, phone, OpenCode, FIFO, exact-origin and old-shim regressions — pass (67.6 seconds).
- `go test -race ./internal/protocol/... ./internal/skeleton -run 'TestMessageOrigin|TestSemanticClientRefusesOrigin|TestSemanticOrigin|TestOpenCode.*Origin|TestOwnerSemanticSendKeepsOwner|TestComposerOldShim|TestComposerQueue_KeystrokeBeginAndSubmit' -count=1` — pass.
- `go vet ./internal/protocol/... ./internal/skeleton/...` — pass.
- `go build ./internal/protocol/... ./internal/skeleton/...` — pass.
- `golangci-lint run ./internal/protocol/... ./internal/skeleton/...` — pass (0 issues).

Full repository, independent review, actual installed-provider matrix, and release/deployment gates belong to integration; these lane results do not substitute for them.

## Five-provider admission boundary

Current native input evidence is recorded separately for [Claude](claude-native-input.md) and [agy](agy-native-input.md). It includes exact guarded Mac and VM versions and permission-dialog separation. Their input-only native paths do not imply structured-history or attention parity. The current [whole-feature evidence](../discord-integration.md) records remaining integration/release gates. The observations below describe the initial protocol checkpoint and are retained as history, not current support claims.

The existing `TypeSubmit` transaction serializes input writers and refuses a known/unknown owner draft. It does not coordinate with the provider's own event loop and cannot prove that a native modal will not open between typing and Enter. A grid/status check followed by Enter cannot close that race. The initial protocol-only checkpoint preserved the existing Claude path and did not establish native modal admission proof; the later session-owned mod and its fixture provide that separate evidence.

At that initial checkpoint, agy and Hermes exposed neither `KeystrokeComposer` nor a live structured message backend in Swarm. Neither could be enabled by pretending that grid status was admission authority, and degraded Codex/Claude were not routed through a new input fallback.

Read-only host evidence:

- `command -v agy`: `/Users/Nathan/.local/bin/agy`; `agy --version`: `1.4.0`.
- `agy --help`: print-mode `--input-format stream-json` reads one NDJSON message per line and requires `--output-format stream-json`; this is a separately launched noninteractive process, not evidence that an existing TUI session accepts native messages.
- `agy --help`: `--remote-control` creates a remote connection for the CLI session on startup. `agy help remote-control` lists daemon `start`, `status`, `stop`; `agy remote-control start --help` exposes only machine `--name` and login-scoped `--session`. No same-session message admission API is documented by these local help surfaces. No daemon start/stop was run.
- `command -v hermes`: no installed binary. The repository's [Hermes transport decision](../../design/hermes-adapter.md#typed-transport-decision) identifies Gateway and ACP as distinct structured transports requiring ownership, readiness, authentication, cancellation, and capability mapping.

That investigation required a provider-coordinated, same-session admission API. The implemented agy agentapi route and modern Hermes shared-owner work follow that boundary. A new headless conversation or a second process resumed against history does not establish safe control of an existing native session. All-five integrated release qualification remains open until the whole-feature gates are recorded.
