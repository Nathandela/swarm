# ADR-036: A local Discord connection addresses the whole Swarm

- Status: Accepted; integrated release gates open
- Date: 2026-10-27
- Authority: owner decisions and implementation/release authorization in the development conversation
- Affects: local Discord connector, semantic message provenance, qualified native input, owner controls

## Context

Colleagues need to discuss work with each other's existing coding agents from Discord. Each person retains their own machines, native provider accounts, tools and agent sessions. A laptop and a VM are separate places. The owner explicitly chose unrestricted personal coding agents with attributed external messages, simple message endorsement, separate native tool approval, deliberate publication and an optional inexpensive receptionist.

The bot must address all registered local discussions by default. A separate hosted orchestration service, a new model API account, per-agent publication workflow, formal work-package approval and an extra idle-task protection queue do not serve this contract.

## Decision

### Place and identity

One connector runs as `swarm discord run`, using outbound Discord Gateway/REST connections and a private local Unix socket. Its configuration, token file, request ledger and installation ID belong to that local place. Native launchd/systemd user services can supervise it. The terminal and local supervisor remain independently usable when Discord disconnects.

Integration and agent names are arbitrary user-chosen labels. Installation/application/Discord user IDs and exact Swarm session instances establish identity. Renames retain receipts, opt-outs and joined discussions. Genuine resume lineage can move a logical binding to one current successor; ambiguity refuses routing. No host name, owner name or host type is a product principal.

Activation covers current and future registered local discussions, with explicit include/exclude overrides. Unsupported surviving shims remain visible but refuse unsafe input and explain the owner-selected refresh requirement. Activation never silently restarts them.

### Authority and conversation

Guild, channel or verified thread-parent scope and optional Discord user allowlists are checked before agent effects. Direct messages are admitted for the configured owner or explicitly allowed users. Merely seeing a message, following a thread, or observing another bot never invokes a model.

Addressing a unique agent, choosing an autocomplete result, using a message context command or selecting a routing choice produces an attributed native request. Explicitly joined discussions contribute bounded attributed recent context. The connector does not crawl personal conversations, unrelated channels or historical transcripts.

Requests distinguish original author, actual invoker and owner endorser. Authority is `external`, `owner_direct` or `owner_endorsed`. A colleague's request is framed as external discussion. Only the configured Discord owner can endorse the original message as their instruction. There is no formal plan-approval stage and no integration queue waiting for a busy agent to become idle. The provider retains its normal admission, steering, queueing and refusal behavior.

This framing is an instruction to an unrestricted coding agent, not a filesystem or confidentiality sandbox. The owner accepts that admitted colleagues can ask that agent to investigate with its normal tools and subagents. Native tool permissions remain separate exact provider decisions. Message endorsement never answers a permission prompt.

### Existing agents and the receptionist

The bot is ordinary Go code for routing, policy, durable state and UI. Existing coding-agent CLIs do the model work. The optional receptionist uses the installed Codex/Claude CLI and its native account, selecting a discovered Luna model or Haiku unless configured otherwise. Deterministic routing and ordinary chatter require no receptionist inference. Validated suggestions cannot grant owner authority or select foreign/excluded agents. An immediate directory remains usable if the receptionist is unavailable.

### Context and publication

The native message carries a compact opaque reference and original attribution. `swarm discord context <ref>` retrieves the exact immutable source and attributed context through the private local socket. Large context is paged instead of split into multiple native turns. Delegation carries the same reference and attribution; it does not manufacture a native child identity.

Agents deliberately publish with `swarm discord reply <ref> --body-file <file>`. The reference fixes the original audience. Terminal output, native transcripts and tool results are not mirrored. The default publication ID is `final`; additional progress messages need explicit distinct IDs. Allowed mentions are suppressed on both create and edit.

### Durability and native admission

The private bounded bbolt ledger records immutable event identity and exact operation before effects. Same-event content or authority changes conflict. References expire; unresolved guards survive longer. Capacity exhaustion refuses new work before effects. Opt-outs are checked at admission and again before each outgoing chunk.

Lost acknowledgements remain uncertain. Native recovery reads the original exact receipt without resubmitting. Discord publication retains exact UTF-16-bounded chunks and stable nonces; nonce enforcement is a temporary provider aid, not permanent exactly-once delivery. Retrying a final reply with a changed body conflicts. No uncertain effect is blindly replayed after restart.

`message-origin` is an additive negotiated semantic capability. Every origin field is included in the canonical operation digest; legacy omitted-origin hashes remain unchanged. Transport `source=owner` describes the local socket caller and does not prove that an external Discord author is the owner. Native history gets provenance only through exact provider correlation, never similar terminal text.

Qualified native ingress must submit text through the provider's own message API. A grid check followed by Enter is insufficient because a permission dialog can appear between those operations. Claude's per-invocation mod and agy's authenticated same-session agentapi registration use session-owned artifacts. Codex and OpenCode use their existing structured message paths. Modern Hermes requires a separate native-owner lifecycle decision. No new raw-key fallback is allowed.

## Consequences

### Positive

No hosted backend, inbound public listener, new provider credential or second investigation framework is required. Each colleague can independently connect a place. Owners retain simple control, external authors remain visible, and ordinary conversation costs no inference.

### Negative

An unrestricted agent can disclose information if it follows a malicious external instruction. Discord access scope and trusted-team membership are meaningful security choices. Revocation cannot undo a native action or publication that already occurred.

Provider capabilities and exact qualified versions differ. Existing workers may need deliberate refresh or native registration. Expired context is unavailable. Recent discussion context is bounded and process-local. Uncertain native or publication outcomes can require investigation rather than automatic retry. Logical ledger limits do not promise a physical filesystem quota.

## Alternatives considered

A shared hosted bot with cross-machine routing adds central credentials, infrastructure and ownership boundaries the initial product does not need. One bot per agent adds Discord administration and identity clutter. A separate read-only investigator is unnecessary when the owner deliberately shares unrestricted native agents. Plan approval and protective busy queues contradict the owner's selected flow. Raw terminal injection cannot establish permission separation.

## Verification

[Integration evidence](../verification/discord-integration.md) tracks actual tests, independent reviews and outstanding release/installation gates. [Operator instructions](../ops/discord.md) describe setup and day-to-day flows. Mock Discord protocol evidence is recorded as mock evidence, as explicitly authorized by the owner; it does not claim a live Discord deployment.
