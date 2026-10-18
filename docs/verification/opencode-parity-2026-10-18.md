# OpenCode integration parity and live verification

OpenCode can already run as a persistent Swarm worker, call the shared orchestration commands, and exchange prompts with other workers. It is **not yet at Claude Code or Codex parity for unattended orchestration**. Swarm cannot reliably recognize its finished turns, permission requests, or provider errors; passive supervision can remain pending, and abrupt OpenCode process death can leave Swarm unable to resume an otherwise retained native conversation.

The main missing layer is a session-bound OpenCode event and control integration. This is an implementation gap in Swarm, rather than an absence of suitable upstream APIs: the installed OpenCode HTTP/SSE server successfully exposed status, history, parent/child relationships, and a permission request/reply cycle during this investigation.

## Scope and versions

Verified on macOS arm64, 2026-10-18:

| Component | Version or source |
| --- | --- |
| Installed Swarm | 0.18.2, built with Go 1.26.9 |
| Investigated source | `3f07f3f6`, four documentation commits after `v0.18.2`; no runtime source difference from that tag |
| OpenCode | 1.19.3 |
| Claude Code | 2.1.296 |
| Codex | 0.163.0 |
| Test toolchain | Native Go 1.26.9; `/usr/local/bin/go` is an incompatible x86_64 executable on this host |

Seven independent Sol/Astra investigation agents covered adapter contracts, orchestration, monitoring/upstream APIs, adversarial validation, native HTTP probes, baseline comparison, and lifecycle evidence. Follow-up rounds tested hypotheses and corrected broad initial claims. Claude/Codex comparisons below describe their implemented paths; this investigation did not create fresh authenticated Claude or Codex workers.

All live workers and API servers used a disposable daemon, workspace, and OpenCode XDG store under `/private/tmp/swarm-oc-audit`. Production Swarm sessions were excluded. A wrapper supplied isolated XDG paths after Swarm's environment filtering and used the existing OpenRouter credential in process memory for bounded synthetic prompts. No owner provider configuration or billing settings were changed. Only selected synthetic evidence is retained in [evidence.json](opencode-parity-2026-10-18/evidence.json); complete native databases, credentials, and unrelated model output are excluded.

## Capability comparison

| Capability | Claude Code implementation | Codex implementation | OpenCode today |
| --- | --- | --- | --- |
| Persistent worker, terminal detach, Swarm daemon replacement | Shared shim/PTY lifecycle | Shared shim/PTY lifecycle | Shared lifecycle; actual native processes survived two forced daemon crashes |
| Spawn, list, terminal peek, terminal send, process-exit watch | Shared owner-tier CLI verbs | Same shared verbs | Available; real two-worker exchange passed in both directions |
| Turn completion and requests for attention | Hook events and grid fallback | Native app-server events and grid fallback | Busy footer heuristic; no reliable settled-idle, permission, question, or native error feed |
| Passive completion notification | Available when source and child satisfy safety/attention gates | Same gates | Finished child notification remained pending against an idle-looking OpenCode source |
| Structured conversation and tool capture | Hook-shaped capture; assistant prose normally arrives at Stop | Native item/event shaping, including assistant deltas; not every native item is shaped | No `InteractionSource`; terminal screen fallback only |
| Structured phone send, approval, Stop | Checked PTY composer/dialog interaction | Native turn and approval RPC | Withheld; explicit terminal fallback/control is available |
| Agent-authored context-file handoff | Generic `swarm handoff` | Generic `swarm handoff` | Generic command is available as source or target; automatic attention monitoring remains limited |
| Hands-off handoff from native transcript | Characterized source layout | Characterized source layout | OpenCode source explicitly refused; OpenCode can receive another provider's handoff |
| Native conversation identity | Authenticated hook identity | Backend thread identity | Normally obtained from the exit-screen command, rather than at launch |
| Resume after graceful provider exit | Implemented | Implemented | Passed: same native conversation, retained history, fresh successful answer |
| Recovery after abrupt provider death | Early identity/history recovery machinery | Early identity/history recovery machinery | Failed in the tested fresh session: native history existed, but Swarm had no captured ID |
| Model/agent discovery in launch form | Configured-model probing | Configured-model probing | Free-text model and agent fields; no enumeration or configured-default overlay |
| Automatic CLI refresh | Provider-specific, requires safe state/ID, excludes isolated worktrees | Same prerequisites plus backend readiness | Excluded; missing dependable idle/identity would also prevent safe refresh |
| Swarm ContextGuard on the installed versions | No adapter support | Installed 0.163.0 is outside the characterized 0.150.x/0.151.x range | No adapter support; this is not a feature the current installed baselines universally provide |
| External provider-session adoption | `swarm reattach --cli claude` | No equivalent in that command | No OpenCode adoption path |

The corresponding source boundaries are [OpenCode adapter](../../internal/adapter/opencode/opencode.go), [Claude adapter](../../internal/adapter/claude/claude.go), [Codex backend](../../internal/adapter/codex/backend.go), [interaction capture](../../internal/skeleton/interaction.go), [capability derivation](../../internal/skeleton/capability.go), [CLI refresh](../../internal/skeleton/cli_refresh.go), and [Codex ContextGuard gates](../../internal/adapter/codex/contextguard.go).

## Live results that matter for long-running swarms

### Workers can communicate, but completion monitoring misses the exchange

Two genuine OpenCode TUI workers, G and H, used `openrouter/openai/gpt-5.4-mini` with a disposable 1,024-output-token cap. Both first produced their requested readiness markers. G then invoked the Bash tool to execute `swarm send H --text ...`; H's native history contained the resulting user prompt and its exact assistant reply `OC_H_GOT_G`.

After a forced Swarm daemon crash and restart, H executed the reciprocal send. G's existing native conversation received the prompt and replied `OC_G_GOT_H_AFTER_RESTART`. The sender's tool completion, recipient's native user message, and recipient's native assistant response were checked separately. This establishes actual prompt exchange rather than merely accepting a PTY write.

During the first exchange, `swarm pilot ... watch G H --once --timeout 12s` returned its snapshot and then timed out with exit 2. Both successful replies were present in native history, while Swarm continued to report `Turn=active`, `Interaction=none`, `group=working`. `pilot view` could read the terminal fallback. The missing behavior is reliable turn/attention notification, not the ability to inspect a worker or detect process exit.

This follows the production paths: [generic send and peek](../../cmd/swarm/sendpeek.go), [PTY write semantics](../../internal/protocol/sendinput.go), [pilot reply detection](../../cmd/swarm/pilot.go), and [pilot watch filtering](../../cmd/swarm/pilot_watch.go). Native OpenCode subagents are separate from Swarm's `spawned_from` lineage; their independent native sessions are not imported into Swarm's roster or status model.

### A successful send is not a message-level acknowledgment

An earlier recipient B was displaying a native external-directory permission modal. Worker A successfully invoked `swarm send B`, and A's tool reported success. B's native history contained no corresponding new user prompt before it was stopped. The exact effect of those keystrokes on the modal was not established.

That attempt therefore counts as accepted terminal input, **not verified prompt receipt**. A local `swarm send` is a serialized PTY operation; it has no native OpenCode message ID, semantic acknowledgment, durable arbitrary-message inbox, or authenticated sender envelope. Existing state and controller safety measures do not turn PTY input into a provider message protocol. Local orchestration deliberately trusts same-user socket clients; hook authentication does not create a sandbox between workers.

The native model used by B also deviated from its tiny no-tools task and attempted unrelated configuration inspection. It was stopped, and subsequent disposable workers had narrower tool permissions. This is a model-behavior limit of that sample, not evidence of successful task completion or a new Swarm capability.

### Status can remain active indefinitely after a reply

The adapter declares `prompt-marker` and `busy-contains: esc interrupt`, with no conclusive idle or native event declaration. Current [grid polling](../../internal/skeleton/serve.go) samples every **500 ms**, not the 200 ms described in older evidence.

[Engine.OnOutput](../../internal/engine/engine.go) preserves the prior status on an inconclusive screen and refreshes `lastSignalAt` even for an unchanged snapshot. The silent-active fallback in `Tick` therefore need not fire while polling continues. It would also downgrade only to unknown, which still derives to working. A 30-second delay is not a reliable way to obtain idle.

The live permission modal had `Interaction=none`. Free-provider 403 errors and OpenRouter's initial token-credit 402 errors likewise remained working rather than becoming reliable provider-error attention. These are important failure cases for an unattended coordinator.

Submitted direct input can also retain `direct-input-unresolved.json` until a later known status-tuple change. Markers were observed after completed replies. [The direct-input safety bridge](../../internal/skeleton/directinput.go) intentionally fails closed; missing OpenCode status progress can therefore hold automatic actions as well as mislead display.

### Passive supervision stayed pending

A finite fixture child was launched through the actual installed Swarm handoff command, daemon, shim, lineage, and passive-supervisor machinery, with genuine OpenCode A as its source. The child exited and reached completed. More than one supervisor retry interval later, its roster still had `supervision_pending=true`; A remained active/none despite its visibly settled screen.

This mixed real/fixture test confirms the [source safety gate](../../internal/skeleton/supervision.go): delivery requires a running, idle source with no conflicting interaction or controller. It does not claim that passive supervision is absent for every provider, or that process-exit monitoring fails.

### Daemon crashes and provider crashes have different outcomes

The original OpenCode A remained alive for **631 seconds**. Two isolated Swarm daemons were killed with SIGKILL during the run. For the second crash, all three actual OpenCode child PIDs and process-start identities were recorded before and after; the native processes and their shims remained the same. G/H then completed the reciprocal prompt exchange. This verifies daemon replacement and subsequent communication on this host, not an overnight soak or machine-reboot survival.

After that run, only A's native OpenCode process was killed with SIGKILL. Its shim recorded exit, and `swarm watch --until completed ...` correctly detected it. Swarm's `conversation_id` stayed empty. `swarm pilot ... resume A` failed:

```text
cannot resume "ia4654bw5wiqn6iw": no captured conversation id
```

The native OpenCode database still contained the conversation and its history. The failure is Swarm's missing association/recovery, not destruction of native history. Concurrent conversations shared the same workspace, so guessing the newest session by cwd would be unsafe.

As the positive control, G was stopped with ordinary `swarm kill`. The exit-screen extractor captured its native ID, `pilot resume` created a successor using the same native ID, and a new `OC_RESUME_ALIVE` answer appeared in the existing native conversation. Ordinary graceful kill must not be equated with abrupt provider death. Early identity/recovery is tracked as `agents-tracker-rz40`. See [identity extraction](../../internal/adapter/opencode/opencode.go), [resume composition](../../internal/skeleton/api.go), and [provider-history recovery](../../internal/skeleton/resume_history.go).

## Native OpenCode API verification

Separate disposable `opencode serve` processes reported version 1.19.3. Their local `/doc` schema and actual responses were checked. These API results demonstrate available upstream mechanisms; they do **not** imply Swarm already uses them.

| Native operation | Observed result |
| --- | --- |
| `GET /global/health` | Healthy, version 1.19.3 |
| Create parent and child sessions; list/get/children | Explicit `parentID` relationship reconciled across responses; autonomous model delegation was not tested |
| `GET /event` and `/global/event` | Directory-scoped event and global wrapped event carried matching event IDs |
| `POST /session/{id}/prompt_async` | Returned 204; successful generation was verified separately through terminal assistant message and exact text |
| `session.status` | Actual busy and idle payloads; error runs also emitted `session.error` |
| `GET /session/status` | Busy session present; idle session absent from the map, rather than necessarily carrying an explicit idle entry |
| `GET /session/{id}/message` | User, assistant, and tool records reconciled; exact successful answer and tool exit checked |
| Permission request | Actual `permission.asked` event plus matching `GET /permission` pending request for `sleep 1` |
| `POST /permission/{id}/reply` with `reply: once` | HTTP 200; `permission.replied`, pending request removed, Bash exited 0, assistant returned `LIVE_PERMISSION_OK` |

On an error run, a terminal assistant update arrived after an idle event. On the successful answer run it preceded idle. An integration must reconcile message state and outcome rather than treating the first idle event as successful task completion. Event IDs were observed; durable SSE replay or recovery from missed events was not established.

The live permission event names were `permission.asked` and `permission.replied`. The schema also advertises v2 shapes, and an older session-specific permission route uses `response` rather than `reply`. Those alternatives were not live-qualified. Existing planning text referring to `permission.updated` should not be used as a version-pinned wire contract.

A probe using `tools: {bash: true}` promoted the session's Bash permission to allow, and the user-directed `/shell` route executed without a permission request. The successful ask/reply probe used an ask policy and omitted the prompt's `tools` field. Future message integration must preserve the user's permission policy when constructing requests. `always`, `reject`, native questions, abort races, compaction, and SSE replay remain separate acceptance tests.

The [official server documentation](https://docs.opencode.ai/docs/server/) describes standalone server and TUI-client arrangements. Swarm needs to bind the actual server/session it owns: starting an unrelated `serve` process beside a TUI is insufficient. Managed serve-plus-attach and discovery of the ordinary TUI's own endpoint are alternatives requiring characterization. The [plugin surface](https://docs.opencode.ai/docs/plugins/) is another possible event source; it is not currently wired by the Swarm adapter.

## Current setup findings

Swarm does not enumerate OpenCode models or agents, even though the CLI exposes both. The normal owner profile had an OpenRouter API credential and no resolved default `model` or `small_model`; the isolated free-model setup differed from that owner profile.

The first deprecated `opencode/cat-jam-free` requests reached Big Pickle. The model flag remained intact in both shim launch manifests; native model fallback is plausible, but its cause was not proved. A later active-model launch displayed its selected model correctly. This is not established as a Swarm flag-composition bug.

Several isolated free-model calls returned 403 with a free-tier client restriction. Initial authenticated OpenRouter mini calls returned 402 because OpenCode reserved up to 32,000 output tokens against the available credit limit. A **test-only** model override of `limit: {context: 400000, input: 272000, output: 1024}` allowed the successful exchange and API probes. No credit purchase was needed. A fresh process was required for the test server's provider model limit to reflect the changed configuration.

[Swarm's environment allowlist](../../internal/persist/env.go) retains `HOME`, `PATH`, and the explicit Anthropic/OpenAI API-key names, but drops XDG variables, arbitrary provider-key variables, and OpenCode-specific environment settings. That can change custom OpenCode setups. Ordinary home-file authentication is a different path; the test wrapper supplied isolation and the existing OpenRouter key after filtering.

## Two confirmed shared workflow bugs

These affect more than OpenCode and were recorded without changing runtime implementation.

1. **Generated manual watch command is rejected.** The [handoff template](../../internal/tui/templates/handoff-source.md.tmpl) emits `swarm watch SESSION --until ... --timeout ...`. [runWatch](../../cmd/swarm/agentverbs.go) parses a standard Go FlagSet, which stops at the first positional argument. The generated order immediately failed with exit 1. Flags first worked and timed out normally:

   ```sh
   swarm watch --until needs_input,ready_for_review,completed --timeout 10m SESSION
   ```

   Existing successful unit tests use flags first; template tests pin the incompatible text. Tracked as `agents-tracker-79si`.

2. **Supervised CLI handoff leaves an isolated source worktree.** In a disposable Git repository with an OpenCode-shaped fixture, source provider cwd was `ROOT/.swarm/worktrees/fixture-worktree-source`; the handoff child ran in `ROOT`. [runHandoff](../../cmd/swarm/handoff.go) copies roster `Cwd`, which is the launch root. The separate hands-off path already corrects to `ProviderCwd`. This was verified through real daemon/shim processes with a fixture provider, not a model performing repository work. Tracked as `agents-tracker-xl6k`.

## Work needed for the requested workflow

The useful first milestone is reliable unattended coordination, rather than another launch option.

1. **Session identity and authoritative lifecycle.** Bind a known OpenCode session to its actual managed process/server early; persist identity before useful work, consume native busy/idle/error/request events, and reconcile after daemon/server connection loss. This addresses completion, permission visibility, passive-supervisor readiness, and the abrupt-death recovery hole together. Extend the existing `agents-tracker-93a` and `agents-tracker-dwwv.6` work rather than inventing terminal substrings.
2. **Session-bound messages and history.** Add native prompt admission and outcome correlation, preserve permission policy, obtain structured message/tool history, and represent missed/unrecoverable stream segments explicitly. Validate concurrent workers in one cwd, busy recipients, open modals, repeated sends, cancellation, and reconnect. The existing generic PTY verbs remain useful, but do not supply these guarantees.
3. **Continuation and operational acceptance.** Resolve native history/export for hands-off OpenCode sources; test graceful and abrupt resume, cross-CLI handoffs, native child activity, and unattended monitoring. Add phone structured send/approval/stop after the transport is reliable. CLI refresh, account pooling, external adoption, and Swarm-driven compaction are separate later scopes.

The current backend path is Codex-specific: Unix socket readiness, WebSocket JSON-RPC initialization, and thread/turn methods. Adding an OpenCode descriptor alone will not create an HTTP/SSE transport. See [backend planning](../../internal/daemon/backend.go), [shim backend readiness](../../internal/shim/backend.go), and [backend connection/rejoin](../../internal/skeleton/backendconnect.go).

Two baseline limits should remain explicit. Codex 0.163.0 is outside Swarm's existing ContextGuard range; Claude has no ContextGuard adapter. Managed account enrollment is Linux-gated, while this investigation ran on macOS. Neither justifies claiming that every current Claude/Codex feature is available on this host or that OpenCode must implement account rotation to achieve useful orchestration parity.

## Verification and remaining boundaries

Passing checks:

- All adapter packages, full engine package, and ContextGuard package, uncached.
- Focused command parsing/spawn/send/peek/handoff/pilot, supervisor, shim survival, protocol, and end-to-end replay/detach/daemon-restart tests across five packages.
- Race-enabled focused adapter/command/supervisor/shim checks, plus the full engine package under the race detector.
- Assert-based disposable live checks for native-process survival, bidirectional prompt receipt/reply, passive pending state, worktree cwd, abrupt-resume refusal, graceful native-ID continuity, and native HTTP/SSE/permission results.

The selected commands, outputs, timestamps, and case assertions are in [evidence.json](opencode-parity-2026-10-18/evidence.json). Existing [OpenCode adapter tests](../../internal/adapter/opencode/opencode_test.go) and [fixture replay E2E](../../internal/e2e/replay_e2e_test.go) mostly characterize recorded 1.17.9 output; passing them alone would not establish 1.19.3 interoperability. The shared bounded-output/memory soak uses synthetic output for roughly 30 seconds, not a long native provider workload.

No runtime fixes or owner configuration changes were included. All disposable workers, both native API servers, and the isolated daemon were stopped after verification. Overnight/multi-day operation, machine sleep/reboot, remote Linux host, actual handset control, fresh Claude/Codex provider runs, native OpenCode subagent delegation, token-expiry/quota rotation, compaction continuation, rejection/always approval semantics, and network/SSE replay were not exercised. The ten-minute local process run establishes a narrower survival result. Production adoption should be judged against that boundary.
