# ADR-037: Modern Hermes sessions use the provider's shared native owner

- Status: Accepted; guarded shared-owner source and release qualification open
- Date: 2026-10-27
- Amends: Hermes classic-only transport decision in `docs/design/hermes-adapter.md`; ADR-001 and S-4/S5 for the ownership boundary of modern Hermes native work; S-2, R-2/R-3 and the status completion rule as described below.
- Preserves: the frozen pure Adapter interface, legacy classic Hermes launches, native tool approval authority, and other providers' lifecycle rules.

## Context

Hermes v0.22.5 routes its CLI/TUI sessions through a persistent native gateway.
Its canonical local session API separates `prompt.submit` from generation-bound
`approval.respond`. The modern TUI can display an external client's prompt and
the native approval it triggers. Swarm's existing classic Hermes adapter targets
the earlier process-owned CLI and its grid/identity format.

The provider gateway can serve several sessions and profiles, including clients
outside Swarm. A Swarm viewer's process exit therefore cannot prove that its
native jobs ended. Killing a shared gateway would reach other sessions. Private
per-worker homes or profile clones would introduce additional configuration and
credential custody, and upstream describes standalone per-profile gateways as a
temporary compatibility topology.

The owner authorized the shared-owner implementation and its conservative
unknown-work boundary during the whole-swarm Discord work. The architecture
decision is separate from deployed capability or completed qualification.

## Decision

Qualified new managed Hermes sessions use the provider's normal persistent
gateway and existing configured native home/profile. Native authentication is
performed through Hermes's normal flow once for that home. Swarm neither clones
profiles nor reads or copies credential files. Its shim owns the native TUI
viewer's PTY and process group; native agent jobs remain owned by Hermes.

The mode is selected only when native input activation is enabled and the
detected stable Hermes release is at least v0.22.5. Existing classic sessions
retain their running processes and require explicit refresh to gain the new
mode. Below-floor or disabled fresh launches retain the classic adapter path.
An explicitly resumed native session retains native ownership semantics even
if activation subsequently changes.

Swarm invokes `hermes [--profile PROFILE] gateway ensure` and validates its
machine-readable result. The native client and owner code SHA must agree. Each
canonical session is bound to the configured profile home, control home, owner
PID/start time, instance, authority epoch, selected provider version, and its
`local-` plus 64-hex identity. The private descriptor additionally binds the
Swarm worker ID, shim PID/start time and generation. Every native connection
revalidates the owner binding. A replaced owner is not silently substituted for
an existing worker binding.

The native authenticated control socket mints an interactive WebSocket ticket.
Tickets remain ephemeral. Launch origins are captured before environment
filtering; only normal nonsecret local-home selectors survive as Hermes
origins. Unsupported explicit remote transports are refused without retaining
their credential-bearing URL contents.

Fresh native session creation uses a request ID derived from the reserved Swarm
worker ID and submits no input. The viewer attaches with
`hermes chat --tui --resume <exact native ID>`. The provider's frozen directory
is checked at creation/resume. Native resume retains its saved home/profile and
working directory; it does not create a replacement worktree or silently
relocate the native discussion.

Semantic messages use only `prompt.submit`. Their durable Swarm intent pins the
native descriptor, text hash and external origin before crossing execution
custody. A native queued/started/terminal admission receipt is evidence of
submission. It is not evidence of model read, response delivery, or successful
task completion. A queued native receipt can have execution generation zero
until execution starts. Missing or uncertain acknowledgements are not replayed
through terminal keys or another conversation.

Native approval/question handling retains provider authority. External message
text, including approval-looking commands, is ordinary prompt content on this
path. Owner endorsement of a message does not answer a native tool approval.
Swarm adds no idle wait queue or agent/subagent capability restrictions; native
queue, steer and refusal behavior remains with the provider.

Native observation replaces the classic grid classifier for this mode. It reads
the exact canonical session plus the provider's existing process/subagent
projections. Unknown or unsupported projection state remains unknown. Daemon
restart reconnects to persisted bindings and reads state without creating a
native session or submitting a turn.

Explicit Swarm stop records the exact native stop intent durably before native
control. It cancels observed queued admission IDs, sends a generation-bound
foreground interrupt when applicable, and invokes session-scoped `process.stop`
for background processes and async delegations. The viewer is then stopped.
After viewer exit the shim repeats an authorized scoped stop when a stop intent
exists and detaches its own exact native subscription with an end reason.
Ordinary viewer closure detaches without inventing an explicit stop intent.
There is no `session.close` call or shared-gateway signal on these paths.

Cancellation acknowledgement and quiet registry snapshots do not constitute a
joined native terminal proof. A native viewer becoming unavailable therefore
sets `Status.NativeWorkUnknown`, reports Needs input, and retains its session and
workspace. Kill reports requested/uncertain instead of successful job
completion. Delete and automatic worktree cleanup remain blocked while joined
native completion cannot be established. This is an explicit open capability,
not a claim that the native descendants are contained by the viewer's group.

Native Hermes metadata uses schema 5 so older daemons cannot load it as a
classic PTY-owned session and perform unsafe completion/cleanup. Classic
22-character conversation IDs remain valid historical resume identities. The
new canonical ID validator is strict and performs no I/O in the adapter.

## Consequences

### Positive

All workers attached to one native home can reuse its normal authentication and
shared owner. Message ingress and native approvals have distinct semantic
operations. Worker stop cannot intentionally signal another session's native
owner, and uncertain native work remains visible with retryable metadata.

### Negative

The modern route requires the provider's TUI/runtime prerequisites and a
configured native home. Existing classic instances require refresh. A provider
restart invalidates the retained owner binding until an explicit recovery path
is taken. Joined completion and automatic cleanup remain unavailable where the
native APIs provide cancellation requests or snapshots without terminal proof.

The [Hermes native ingress evidence](../verification/discord-protocol/hermes-native-ingress.md)
separates historical v0.22.5 API/TUI characterization, assembled deterministic
core tests, and the remaining guarded real shared-owner qualification. No Linux,
production credential-refresh, installed-artifact or all-five release claim is
made by this ADR.

## Alternatives considered

PTY message submission depends on the active terminal widget and cannot
establish the required permission boundary. Per-worker gateway/profile copies
add authentication custody and depend on an upstream topology being retired.
Terminating the shared native owner would affect sibling sessions and external
native clients. These approaches are excluded from the managed native route.
