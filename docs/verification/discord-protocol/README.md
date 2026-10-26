# Discord provenance and native input evidence

Date: 2026-10-26. This records source-level protocol work and isolated fixtures. It is not five-provider runtime qualification or a deployment record.

## Additive protocol contract

Clients request `semantic-send` and `message-origin` in hello. `ComposerSendReq.Origin` is an optional `MessageOrigin` containing `kind`, `installation_id`, `request_id`, `actor_id`, `actor_name`, `guild_id`, `channel_id`, `message_id`, and `endorser_id`.

Kinds are `external`, `owner_direct`, and `owner_endorsed`. Installation, request, and actor IDs are required; endorsement requires an endorser, and other kinds prohibit that field. Each field is at most 512 UTF-8 bytes and rejects control characters. This is an authenticated caller's provenance declaration; Discord actor/owner verification belongs to the bot. Transport `source=owner` is not proof of human owner authority.

The client refuses an origin-bearing send if the daemon did not negotiate `message-origin`; the server also refuses an unnegotiated origin before custody/provider I/O. The canonical signed/idempotent content digest binds every origin field, including an explicit domain field. An omitted origin preserves the existing digest exactly, including legacy phone signatures.

The non-OpenCode composer copies origin before recording pending input and binds it in the durable operation's immutable request descriptor. Status/receipts return retained origin even when a provider can prove submission only. Origin is attached to a native history item only with exact correlation: Codex's echoed client message ID, or OpenCode's retained native intent plus verified message/session identity, creation boundary, and full text hash. Claude's existing time-bounded exact-text correlation cannot prove origin: it keeps its existing transport attribution and operation correlation but adds no origin. Unknown imported native messages remain unattributed. Exact owned OpenCode rows can reconcile imported history without submitting again.

## TDD and fixtures

Failing-first protocol tests compiled against missing origin schema/capability, then passed after the additive implementation. Connected skeleton tests compiled against missing pending/receipt/intent fields, then passed after propagation. They cover immutable caller copies, exact echo, no authority from text coincidence, retained OpenCode receipts, exact imported history, and daemon restart without replay.

The old-shim regression initially returned success after two raw writes although its hello did not prove atomic submission. The fix requires the current upstream's `SupportsMessageSubmit` before durable Begin; the fallback raw writes were removed. Unsupported survivors return `structured_unsupported` with explicit session refresh guidance. A daemon restart preserves existing shims and cannot upgrade their capability. No automatic restart is used.

## Five-provider admission boundary

The existing `TypeSubmit` transaction serializes input writers and refuses a known/unknown owner draft. It does not coordinate with the provider's own event loop and cannot prove that a native modal will not open between typing and Enter. A grid/status check followed by Enter cannot close that race. The existing Claude path is preserved; this work does not upgrade that evidence to native modal admission proof.

agy and Hermes currently expose neither `KeystrokeComposer` nor a live structured message backend in Swarm. Neither is enabled by pretending that grid status is admission authority, and degraded Codex/Claude are not routed through a new input fallback.

Read-only host evidence:

- `command -v agy`: `/Users/Nathan/.local/bin/agy`; `agy --version`: `1.4.0`.
- `agy --help`: print-mode `--input-format stream-json` reads one NDJSON message per line and requires `--output-format stream-json`; this is a separately launched noninteractive process, not evidence that an existing TUI session accepts native messages.
- `agy --help`: `--remote-control` creates a remote connection for the CLI session on startup. `agy help remote-control` lists daemon `start`, `status`, `stop`; `agy remote-control start --help` exposes only machine `--name` and login-scoped `--session`. No same-session message admission API is documented by these local help surfaces. No daemon start/stop was run.
- `command -v hermes`: no installed binary. The repository's [Hermes transport decision](../../design/hermes-adapter.md#typed-transport-decision) identifies Gateway and ACP as distinct structured transports requiring ownership, readiness, authentication, cancellation, and capability mapping.

The remaining implementation route needs a provider-coordinated, same-session admission API: investigate agy's Remote Control transport and Hermes Gateway/ACP with isolated native fixtures. A new headless conversation or a second process resumed against history does not establish safe control of the existing native session. All-five admission and runtime qualification remain open.
