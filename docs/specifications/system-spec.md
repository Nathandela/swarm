# swarm — System Specification

**Spec ID**: 0001
**Status**: Approved (Gate 2, 2026-07-16)
**Author**: Nathan Delacrétaz (with Claude)
**Created**: 2026-07-16
**Revised**: Draft 2, after audit committee report `docs/verification/audit-001-system-spec.md`; remote threat model recorded under F-2 per ADR-007 B133 (2026-07-31)

## Goal

`swarm` is a terminal application that centralizes every coding-agent CLI session on a machine into one Agent View-style dashboard: sessions run in the background under a supervisor architecture (surviving terminal close **and daemon crash/upgrade**), are grouped by status, and are launched, attached, and killed entirely by keyboard.

## Context

Inspired by Claude Code's Agent View (see `docs/research/agent_view_landscape.md`), but agent-agnostic. V2 adds mobile remote control; v1 keeps the protocol evolvable for that without claiming remote-readiness (remote's auth/threat model is ADR-007 — see F-2 for the decided boundary). The codebase follows the agentic-codebase-manifesto. Four foundational decisions are recorded as ADR-001..004.

## Domain glossary

| Term | Meaning |
|---|---|
| Session | One tracked instance of an agent CLI in a working directory |
| Daemon | Per-user supervisor: registry, protocol server, detection, event fan-out |
| Shim | Tiny per-session process owning the PTY master; survives the daemon (ADR-001) |
| Adapter | Per-CLI module: detection, spawn args, options schema, status signals, resume |
| Roster | Rebuildable on-disk index of sessions; per-session metadata is the truth (ADR-003) |
| General view | TUI home screen listing sessions grouped by status |
| Attach / Detach | Full-screen raw PTY passthrough / return to general view |
| Client | Anything speaking the daemon protocol (TUI now, mobile in V2) |
| Grid | The VT-emulated screen state (cells, cursor, modes) a shim maintains per session |

## Process architecture (ADR-001)

```
client (TUI) ── UDS, control+data protocol ── daemon ── per-session UDS ── shim ── PTY ── agent CLI
```

- The **shim** owns: PTY master, VT emulator (grid), transcript append, a per-session socket.
- The **daemon** owns: roster, launch/kill orchestration, adapters, status engine, client protocol, event fan-out. Clients never talk to shims directly.
- Daemon crash or upgrade: shims and agents continue; a restarting daemon rediscovers shims from per-session metadata and reconnects. Nothing is lost.

## Session status model

Three orthogonal dimensions (audit finding: one enum conflates unrelated facts):

- **process**: `running` | `exited(code)` | `lost` (shim/process gone, e.g. reboot)
- **turn**: `active` (agent computing) | `idle` (waiting on user) | `unknown` (detection inconclusive or stale)
- **interaction**: `none` | `prompt` (free-text input expected) | `permission` (approval requested) | `unknown`

Derived view groups:

| Group | Rule |
|---|---|
| Needs input | running ∧ idle ∧ (permission ∨ prompt-after-question) |
| Working | running ∧ (active ∨ turn unknown, marked `?`) |
| Ready for review | running ∧ idle ∧ turn-completed |
| Completed | exited ∨ lost |

## EARS Requirements

### Daemon and shim lifecycle (D)

- **D-1** (Event) WHEN any `swarm` client command runs and no daemon is alive, swarm SHALL start the daemon detached (setsid, stdio to log file) and connect, transparently.
- **D-2** (Ubiquitous) Each session's PTY master SHALL be owned by a dedicated shim process, not by the daemon; sessions SHALL be independent of any terminal and of the daemon's lifetime.
- **D-3** (Event) WHEN the terminal that launched swarm closes, running sessions SHALL continue unaffected.
- **D-4** (Event) WHEN the daemon starts, it SHALL rebuild its registry from per-session metadata, reconnect to live shims, and mark sessions whose shim is gone as `lost` — verifying process identity by PID **plus process start time** (PID-reuse safe).
- **D-5** (Unwanted) IF the daemon crashes or is upgraded, THEN running sessions SHALL continue under their shims and be reconnectable by the next daemon with no data loss.
- **D-6** (Ubiquitous) The daemon socket SHALL live under a 0700 state directory with 0600 permissions; a restarting daemon SHALL take a flock-based singleton lock **before** binding and unlink stale sockets only under that lock.
- **D-7** (Unwanted) IF a second daemon instance starts, THEN it SHALL fail to take the lock and exit; the client that spawned it SHALL retry connecting to the winner (bounded backoff).
- **D-8** (Event) WHEN client and daemon protocol versions are incompatible at handshake, swarm SHALL surface a clear error and offer `swarm daemon restart` — which is safe (D-5) and SHALL say so.
- **D-9** (Ubiquitous) The daemon SHALL keep a single durable append-only event journal (session lifecycle and status-group transitions), fsync'd before each cursor is acked and surviving a crash/upgrade (D-5); and every **remote** mutating operation SHALL be idempotent, keyed by an `operation_id` whose durable two-phase record makes a replay return the cached outcome and execute nothing. ADR-007 is the authority on the journal, idempotency, and remote-access model.

### Sessions (S)

- **S-1** (Event) WHEN the user submits the launch form, the daemon SHALL spawn a shim which execs the agent CLI in a fresh PTY in the requested working directory, with adapter-composed **argv arrays only** (no shell interpretation of any user-supplied field), in its own process group.

  > See ADR-007 B144 (2026-08-15): this is the owner-tier form-driven launch. A phone-initiated remote launch is a separate operation over a daemon-authored preset (`session_launch`), never free cwd/argv/env from the phone.
- **S-2** (Ubiquitous) Each session SHALL persist: id, agent type, working directory, launch options, captured environment, created-at, status dimensions, last-activity, shim PID + start time, agent-native conversation id when the adapter exposes one, exit code when ended, `schema_version`.
- **S-3** (Optional, later epic) WHERE the worktree toggle was enabled at launch, the daemon SHALL create a git worktree under `.swarm/worktrees/<name-slug>` with branch `swarm/<name-slug>-<id>` when the session has a usable name, adding the validated `<id>` to the path only on a name collision and falling back to `<id>` for an unnamed session (error if not a git repo), and SHALL tear the exact persisted worktree path down (`git worktree remove` + prune) when the session is deleted.
- **S-4** (Event) WHEN the user kills a session, the shim SHALL signal the agent's **process group** (SIGTERM, grace, SIGKILL) so descendant processes (MCP servers, shells) do not leak; outcome recorded.
- **S-5** (Ubiquitous) The shim SHALL maintain the session's VT grid (emulated screen: cells, cursor, alternate-screen, modes) and an append-only transcript with a size cap and rotation; repeated redraw frames (spinners) SHALL be collapsed before hitting disk.
- **S-6** (Ubiquitous) The launch RPC SHALL carry the client's environment (allowlist-filtered); the shim SHALL spawn the agent with that environment, never the daemon's inherited one.

  > AMENDED BY ADR-007 B144 (2026-08-15): owner tier only. The remote tier's `session_launch` composes environment from daemon-authored launch-preset policy, never a phone-supplied env.
- **S-7** (Ubiquitous) The daemon SHALL enforce a configurable max concurrent session count and reject launches over it with a clear inline error.
- **S-8** (Event) WHEN a named Claude session is launched or resumed, Swarm SHALL pass the label through Claude's native name flag. WHEN a named Codex thread becomes available or either side renames it, Swarm SHALL synchronize the label over Codex's structured app-server naming methods without injecting terminal keystrokes; provider-originated labels SHALL be sanitized before persistence.

### Client protocol (P) (ADR-002)

- **P-1** (Ubiquitous) Client-daemon communication SHALL use one UDS connection carrying two planes: a **control plane** (newline-delimited JSON-RPC-style messages, versioned, capability-negotiated at handshake) and a **data plane** (length-prefixed binary frames for PTY bytes), multiplexed with defined framing and a max frame size.
- **P-2** (Ubiquitous) Control operations: handshake, list, launch, kill, delete, attach/detach, resize, subscribe. Data frames: PTY input, PTY output, grid snapshot.
- **P-3** (Event) WHEN a session's status dimensions change, the daemon SHALL push the session's latest committed state to all subscribed clients as a full-state snapshot event; consecutive changes MAY coalesce to the latest state (level-triggered, ADR-008 — was per-change edge delivery pre-v0.5). A slow or dead subscriber SHALL be disconnected (bounded outbound queue), never blocking PTY draining or persistence.
- **P-4** (Unwanted) IF a client disconnects mid-attach, THEN the session continues and the daemon releases the stream and lease cleanly.
- **P-5** (Ubiquitous) Attach SHALL use an **exclusive controller lease** (input + resize authority, lease generation id on every input/resize message; stale-lease messages rejected). One controller per session in v1; observer mode is a later capability.
- **P-6** (Ubiquitous) The daemon SHALL re-validate every client-supplied input server-side (paths, options, ids) regardless of client checks.

### General view (V)

- **V-1** (Ubiquitous) The general view SHALL list all sessions grouped as Needs input / Working / Ready for review / Completed (derivation table above). Within each group, sessions SHALL be ordered by the time they entered that group, newest first; attachment and unrelated metadata changes SHALL NOT reorder them.
- **V-2** (Event) WHEN a status event arrives, the general view SHALL reflect it within 1 second without user action.
- **V-3** (Ubiquitous) Navigation SHALL be keyboard-only: ↑/↓ (and j/k) move selection across groups, Enter attaches, Esc backs out/quits, Ctrl+X kills (one-key confirm), `n` opens the launch form.
- **V-4** (Ubiquitous) Each row SHALL show: agent name, working directory (shortened), status, elapsed/last-activity time, and a one-line last-output summary derived heuristically from the grid (no LLM call).
- **V-5** (Event) WHEN a session enters Needs input or Ready for review as observed in the delivered status stream, the general view SHALL surface an in-TUI notification (highlight + transient banner). A state coalesced away before delivery (held for less than one sampling window, ADR-008) does not banner; both banner-worthy states are human-paced waits, so in practice they always persist to delivery. OS notifications are v1.x.
- **V-6** (Ubiquitous) The aesthetic SHALL be minimal, Claude Code-like: no mouse required, subtle color, no decoration without information.

### Launch flow (L)

- **L-1** (Ubiquitous) The launch form SHALL collect: working directory (free text, `~` expansion), agent (from detected CLIs), model/options rendered from the adapter's **declarative options schema**, optional initial prompt, worktree toggle (when the epic ships).
- **L-2** (Ubiquitous) The agent picker SHALL offer only CLIs detected on PATH (with version check against the adapter's supported range); supported-but-missing CLIs appear greyed with an install hint.
- **L-3** (Unwanted) IF the working directory is not a directory, THEN the form SHALL show an inline error and refuse to launch. IF it does not exist, THEN the first submit SHALL refuse to launch, show edit-distance-ranked sibling-directory suggestions when available, and require an immediately consecutive submit to create the unchanged path; choosing a suggestion SHALL use the existing directory instead. The daemon re-validates the resulting path (P-6).

> See ADR-007 B144 (2026-08-15): L-1..L-3 describe the owner-tier form. A phone-initiated remote launch chooses a daemon-authored preset and an initial prompt (`launch_presets`, `session_launch`) and supplies no free cwd/argv/env; its exit criteria are the playbook waves (implementation-goals.md Epic 15).

### Attach (A)

- **A-1** (Event) WHEN the user attaches, the TUI SHALL enter raw mode (IXON off) full-screen passthrough: every keystroke forwarded except the detach key; ANSI output rendered untouched (alternate screen, colors, cursor control — N-6).
- **A-2** (Ubiquitous) The detach key SHALL default to `Ctrl+q` (configurable), returning to the general view while the session continues. The attach input filter SHALL recognize both the legacy control byte and Kitty CSI-u keypress encodings across arbitrary read boundaries, SHALL NOT recognize either form inside bracketed paste, and SHALL forward all non-detach input byte-for-byte. (Revised from `Ctrl+\` per ADR-006: `Ctrl+\` is near-untypeable on Swiss/QWERTZ/AZERTY layouts; `Ctrl+q` is layout-friendly and, since raw mode clears IXON per A-1, its XON byte carries no flow-control collision.)
- **A-3** (Event) WHEN the controlling client's terminal resizes, the daemon SHALL propagate the size to the session PTY (resize authority follows the attach lease, P-5).
- **A-4** (Event) WHEN attaching, the client SHALL receive a **serialized grid snapshot** (current emulated screen, both buffers, cursor, modes) followed by the live stream — never raw historical bytes, never a blank screen.
- **A-5** (Ubiquitous) Attach chrome SHALL be at most one thin line (session name + detach hint), toggleable off.

### Adapters and status detection (T)

- **T-1** (Ubiquitous) Each supported agent SHALL be described by an adapter implementing one interface: binary detection + version range, launch-arg composition, declarative options schema (so the TUI renders options without code changes, keeping T-5 honest), status signal sources, resume capability + native conversation-id extraction.
- **T-2** (Ubiquitous) Status detection SHALL prefer **typed signals** wherever the CLI offers them: Claude Code hooks (PermissionRequest/Notification/Stop), Codex app-server / typed events. OpenCode exposes an HTTP+SSE server (`opencode serve`, `GET /event`) carrying a single `session.status` event with a busy/idle/retry payload, plus separate permission/question request objects — not the per-transition typed events this engine's exact-event-name mapping expects today; v1.1 drives OpenCode via adapter-declared grid heuristics (T-3) instead, with typed-event wiring left as future work requiring a payload-to-turn subtype contract of its own. agy's future typed sources are print-mode `--output-format stream-json` (not usable for interactive sessions) and the file-configured `.agents/hooks.json` (not argv-injectable — wiring it would mutate workspace config, out of scope for v1.1); both are unimplemented in the v1.1 adapter, which is heuristic-only. Hermes Agent's classic CLI is likewise heuristic-only: its shell hooks are profile/config-file declarations rather than per-invocation status events, so swarm SHALL NOT mutate them into the user's configuration. The Hermes adapter forces `--cli` and declares a dedicated, fixture-backed grid signature for active, idle, approval and clarification frames; its JSON-RPC TUI Gateway remains future structured-backend work. Consequence: OpenCode declares no idle rule, so a settled OpenCode turn preserves its last committed status (ADR-007) until T-4's staleness guard downgrades it to `unknown`; either way it remains grouped under Working and never triggers the ready-for-review notification; busy-only signal quality is a known v1.1 limitation. Hook/callback delivery to the daemon SHALL be authenticated with a per-session token; hook installation SHALL be per-invocation (flags/env/project-local), never a non-atomic mutation of the user's global config.
- **T-3** (Ubiquitous) Grid-based heuristics (reading the emulated screen, never the raw byte stream) SHALL be the fallback for CLIs or states without typed signals; evaluated on output events with a low-frequency fallback poll.
- **T-4** (Unwanted) IF no output AND no process CPU activity occurs for a threshold while turn=`active`, THEN turn SHALL become `unknown` (staleness guard: never confidently wrong). An inconclusive grid evaluation on live output PRESERVES the previously committed turn rather than mapping to `unknown` (revised per ADR-007: absence of evidence is not evidence of change; the silence-based staleness guard above is the evidence-based downgrade path).
- **T-5** (Ubiquitous) Adding a new adapter SHALL require no changes to daemon core, protocol, or TUI.
- **T-6** (Ubiquitous) BEFORE an adapter is built, a **characterization harness** SHALL record the CLI's real behavior (PTY fixtures per state, hook/event payloads, version) into the fixture corpus; the adapter's capability-matrix entry (signals, resume, options) is the harness's output and the adapter's acceptance baseline.
- **T-7** (Ubiquitous) v1.0 shipped Claude Code and Codex adapters. v1.1 shipped agy (Antigravity CLI, Google's Gemini CLI successor — Gemini CLI itself stops serving non-Enterprise requests 2026-06-18) and OpenCode adapters. The next adapter expansion ships Hermes Agent's classic CLI on macOS arm64 and Linux (amd64/arm64), characterized from Hermes 0.20.6; Intel macOS is unsupported. vibe (Mistral) was evaluated and dropped by decision (2026-07-18); see docs/design/cli-trio-adapters.md appendix for the rationale.

### ContextGuard (C) (ADR-023)

- **C-1** (Optional) WHERE the owner enables ContextGuard, the daemon SHALL remember one global integer threshold from 40 through 95 inclusive (default 80; disabled by default) and evaluate exact active-context occupancy at `used >= threshold`.
- **C-2** (Ubiquitous) Every observation and lifecycle edge SHALL be bound to the current session instance, backend feed, provider thread, settings revision, source sequence, and capture time. Missing, malformed, stale, cumulative-only, uncharacterized, or unsupported evidence SHALL cause zero automatic provider actions.
- **C-3** (Unwanted) IF a compaction outcome is ambiguous across a write, connection loss, event loss, or daemon restart, THEN the guard SHALL enter a durable no-retry hold; a replacement session instance starts a fresh cycle, while a daemon restart of the same instance preserves the safety latch.
- **C-4** (Ubiquitous) ContextGuard SHALL NOT change provider launch flags, native auto-compaction settings, status-line settings, or harness configuration files. Settings and sanitized live state are owner-tier only; callbacks SHALL perform no parsing, persistence, or provider I/O.
- **C-5** (Temporary rollout constraint) Codex telemetry and native compaction lifecycle are observed only for a running app-server version characterized from its initialize response. Automatic dispatch SHALL remain disabled until independent concurrent-turn and concurrent-manual-compaction gates prove non-interference and at-most-once behavior. Claude, OpenCode, AGY, and Hermes remain unavailable until independently characterized.

### Persistence (R) (ADR-003)

- **R-1** (Ubiquitous) State SHALL live under `$XDG_STATE_HOME/swarm` (fallback `~/.local/state/swarm`), 0700: `sessions/<id>/meta.json` (source of truth, atomic temp+rename writes, `schema_version`) + transcript files (0600, capped, rotated); `roster.json` is a rebuildable index only.
- **R-2** (Optional) WHERE an adapter supports resume and a native conversation id was captured (S-2), an ended/lost session SHALL offer relaunch-with-resume as a **new session** linked via `resumed_from`. WHERE an ended/lost Codex or Claude session predates durable capture, an explicit resume SHALL first attempt one bounded, trusted-home, fail-closed recovery of a unique provider-history identity and persist the winner write-once before composing argv. Missing, malformed, unsafe, unreadable, or ambiguous history SHALL refuse the resume and SHALL NOT fall through to a fresh launch. Providers without a characterized history format SHALL retain the captured-id-only behavior.
- **R-3** (Ubiquitous) Completed sessions SHALL remain listed until the user deletes them (Ctrl+X on a completed row deletes; deletion tears down worktrees per S-3 when present).
- **R-4** (Event) WHEN the owner explicitly imports provider-managed background sessions, swarm SHALL adopt only canonical native conversation identities through the adapter's resume command, preserve that identity in session metadata, and reuse an existing matching provider/identity row on retries. Potentially live sessions SHALL require an explicit takeover that stops the provider's background supervisor before the first swarm launch; settled sessions SHALL require an explicit include-settled option. Interactive provider sessions SHALL NOT be imported. External resume SHALL be owner-tier-only and capability-negotiated so an older daemon cannot silently treat the request as a fresh launch.
- **R-5** (Event) WHEN the owner requests a **hands-off handoff** of a source session, swarm SHALL compose the successor's launch **in the daemon** from **pointers only** — the source's canonical native conversation id, its transcript path, its resolved agent working directory, its agent name, and its swarm session id — injecting no transcript content, digest, summary, or filter recipe, and SHALL signal the source in no way (no input, no stop, no kill). The source's status SHALL NOT gate the request: a rate-limited session is indistinguishable from a healthy idle one, so status MAY suggest the method and SHALL NOT select it (ADR-010 Amendment 4). A missing or non-canonical conversation identity, an unsupported source agent, a transcript path that escapes the provider's anchored history root, and a transcript absent at launch time SHALL each refuse **by name** and launch nothing; a refusal SHALL NEVER degrade to a context-free launch. The successor SHALL be linked by `spawned_from` (the parent's local id) with `spawn_intent=handoff` and an empty supervision mode, the source's lifecycle SHALL be untouched, and the composed prompt SHALL state that the source may still be running in the same checkout. WHERE the target provider differs from the source's, the launch SHALL require an explicit owner confirmation, since the successor is instructed to read a transcript authored under another vendor. Hands-off handoff SHALL be owner-tier-only and capability-negotiated so an older daemon cannot silently perform a bare launch.

### Non-functional (N)

- **N-1** TUI first paint SHALL be < 100 ms with the daemon running and ≤ 50 sessions listed (p95, Apple Silicon / modern x86).
- **N-2** Attach passthrough SHALL add < 10 ms added local latency p95 (measured keystroke→PTY write).
- **N-3** The daemon and shims SHALL be event-driven with no busy polling; PTYs are always drained (a full PTY buffer must never block an agent), spinner churn is collapsed before disk, and client-idle CPU is near zero.
- **N-4** Distribution SHALL be a single static binary (CGO_ENABLED=0) per platform (macOS arm64/x86_64, Linux x86_64/arm64) via Homebrew tap and `go install`; zero runtime dependencies (no tmux). The binary contains client, daemon, and shim roles.
- **N-5** The repository SHALL follow the agentic-codebase-manifesto reference architecture (vendored in `docs/governance/`).
- **N-6** Attach SHALL pass through alternate screen, colors, and cursor control faithfully; mouse passthrough optional for v1. Terminal-escape hygiene: OSC 52 clipboard writes and similar hostile sequences are filtered in the snapshot path.
- **N-7** Known accepted limitation: machine sleep pauses agents (all architectures); a caffeinate-style keep-awake is a possible v1.x option.

### V2-forward constraints (F)

- **F-1** The protocol SHALL be versioned and capability-negotiated from the first release; messages carry an endpoint id and namespaced session ids so multi-daemon clients need no schema break.
- **F-2** Remote transport (mobile, relay, multi-machine) is **not** claimed v1-ready: it requires its own ADR covering identity, pairing/auth, E2EE/relay trust, reconnect cursors, and idempotency. v1's obligation is only: no UDS-specific assumptions in message schemas. (That ADR now exists — ADR-007 — and its threat model, decided in B133, is recorded below.)

  > AMENDED BY ADR-018 (2026-08-15): multi-machine ships in the first complete remote product (RC-D8), not a further deferral within V2 as earlier remote planning had cut it; this line's v1 boundary is unaffected — remote transport, multi-machine included, stays outside the terminal app's v1.

  **Remote threat model (added per ADR-007 B133, 2026-07-31; the adversary list amended by ADR-015, 2026-08-15).** The trust boundary is the wire between the phone and the machine. The declared adversary is the relay, the network path, FCM/Google (which reads every push payload it carries), the Swarm-operated push gateway (ADR-015 P4 — a colluding relay and gateway are held in scope together, playbook §11.3), and any MITM between the endpoints; the phone and whoever holds it are trusted, exactly as the machine and its owner-uid user are. There is **no phone-side user authentication** — no biometric, PIN, per-use gate or content lock; the pairing-time SAS comparison is the only human-in-the-loop security step in the product, and it is what defeats a relay MITM. The two-tier wake/content key split is kept as a transport defence: FCM reads the push payload, so the push path holds the content-free wake key only, and that rule is enforced at the **sender**, in the machine-side remote gateway (`swarm-remote`), never the Swarm push gateway. Stated without gloss: on Android `FirebaseMessagingService` runs in the app process, so the phone-side half of the tier boundary was only ever Keystore auth-gating plus code discipline, not OS isolation — with auth-gating removed, **code discipline is the only phone-side enforcement**, and the defence the split rests on is the sender-side rule. Accepted residual risk (B133): a stolen unlocked phone gives its holder full control of agents that edit code on the machine; the only surviving mitigation is `swarm remote off` / device revoke, issued from the machine, which makes that kill switch load-bearing in a way it was not.

## Architecture diagrams

### System context

```mermaid
flowchart TB
  subgraph clients["clients"]
    TUI["swarm TUI (Bubble Tea)"]
    MOB["mobile (V2, ADR-007)"]
  end
  subgraph daemon["swarm daemon"]
    API["protocol server: NDJSON control + binary data frames"]
    REG["registry (rebuildable)"]
    DET["status engine"]
    ADP["adapters: claude, codex · agy, opencode · hermes"]
  end
  subgraph shims["per-session shims (survive daemon)"]
    SH1["shim: PTY + VT grid + transcript"]
    SH2["shim: PTY + VT grid + transcript"]
  end
  A1["claude"] --- SH1
  A2["codex"] --- SH2
  TUI <--> API
  MOB -.-> API
  API --> REG & DET
  DET --> ADP
  API <--> SH1 & SH2
  SH1 & SH2 --> DISK[("$XDG_STATE_HOME/swarm: meta.json per session, transcripts")]
```

### Session lifecycle (process × turn × interaction, illustrative)

```mermaid
stateDiagram-v2
  [*] --> starting: launch (shim spawns agent)
  starting --> active: first grid activity
  starting --> exited: early exit / bad flags
  active --> idle_permission: typed permission event / grid heuristic
  active --> idle_review: turn-complete signal
  idle_permission --> active: input forwarded
  idle_review --> active: new prompt
  active --> unknown: staleness guard (T-4)
  unknown --> active: signal recovers
  unknown --> exited: process exit
  active --> exited: process exit
  idle_permission --> exited: process exit
  idle_review --> exited: process exit
  active --> lost: shim gone (reboot)
  idle_permission --> lost
  idle_review --> lost
  unknown --> lost
  exited --> [*]: deleted
  lost --> [*]: deleted
  lost --> starting: relaunch / resume (R-2)
```

### Attach (sequence)

```mermaid
sequenceDiagram
  participant U as User
  participant T as TUI
  participant D as Daemon
  participant S as Shim
  participant A as Agent CLI
  U->>T: Enter on session row
  T->>D: attach(id) — takes controller lease
  D->>S: snapshot request
  S-->>D: serialized grid (screen, cursor, modes)
  D-->>T: grid snapshot frame → painted instantly
  A-->>S: live output
  S-->>D: output frames + grid updates
  D-->>T: binary data frames (raw passthrough)
  U->>T: keystrokes (raw mode)
  T->>D: input frames (lease id checked)
  D->>S: write PTY
  U->>T: Ctrl+q
  T->>D: detach — lease released
  Note over S,A: session continues under shim
```

## Scenario table

| # | Given | When | Then | Reqs |
|---|---|---|---|---|
| 1 | No daemon running | User runs `swarm` | Daemon auto-starts; view < 100 ms after daemon up | D-1, N-1 |
| 2 | Launch form, valid dir | Pick claude + model, Enter | Shim spawns agent, appears under Working | S-1, L-1, V-1 |
| 3 | Session working | Terminal closed | Agent keeps running; reopening swarm shows it | D-2, D-3 |
| 4 | Claude session running | Claude requests permission | needs_input ≤ 1 s via authenticated hook; row highlighted + banner | T-2, P-3, V-2, V-5 |
| 5 | Codex session running | Turn lifecycle event fires | Status from typed event, not parsing | T-2 |
| 6 | Any session, no typed signal | CLI idle at prompt | Grid heuristic flags idle; staleness guard prevents false working | T-3, T-4 |
| 7 | Session selected | Enter | Grid snapshot painted instantly, typing reaches agent | A-1, A-4, P-5 |
| 8 | Attached, including an agent that enabled Kitty keyboard enhancements | Ctrl+q | Back to general view; session continues; pasted Ctrl+q bytes remain session input | A-2 |
| 9 | Attached | Terminal resized | Agent reflows (lease holds resize authority) | A-3, P-5 |
| 10 | Sessions running | Daemon killed -9, restarted | Shims kept agents alive; daemon reconnects; nothing lost | D-4, D-5 |
| 11 | Sessions running | `brew upgrade swarm` + daemon restart | Same as 10 — upgrade is safe and says so | D-5, D-8 |
| 12 | Machine rebooted | swarm reopened | Sessions marked lost, transcripts intact, resume offered where supported | D-4, R-2 |
| 13 | Launch form | Nonexistent dir | Close sibling suggestions appear; arrows choose one, or a second consecutive Enter confirms creation; daemon re-validates | L-3, P-6 |
| 14 | agy not installed | Launch form opened | Greyed with install hint | L-2 |
| 15 | Spinner runs overnight | — | Transcript capped/rotated; near-zero client-idle CPU | S-5, N-3 |
| 16 | Second client attaches | — | Lease refused/transferred explicitly; stale input rejected | P-5 |
| 17 | Agent spawns MCP servers | Kill session | Whole process group terminated, nothing leaks | S-4 |
| 18 | Launch from terminal with venv + API key | Agent runs | Agent sees the launching terminal's environment | S-6 |

## Constraints

- Go ≥ 1.22, Bubble Tea; PTY via creack/pty; VT emulation via an established Go library (hinted: vt10x-class) — evaluated in ADR-002 work, never hand-rolled parsing in adapters.
- Argv-array spawning only; no shell string composition anywhere (ADR-004).
- Protocol and on-disk schema are low-reversibility: changes require an ADR.
- Adapters are the high-volatility zone: signal rules live per-adapter, backed by the characterization fixture corpus (T-6); core never contains CLI-specific logic.

## Out of scope (v1)

OS notifications (v1.x), mobile/remote transport + auth (V2, ADR-007), multi-machine (V2, ADR-018), Windows, mouse passthrough, observer attach mode, session sharing, sandboxing, keep-awake (v1.x candidate).

> AMENDED BY ADR-018 (2026-08-15): multi-machine is in scope for V2's first complete remote product (RC-D8); it is not a further-deferred phase within V2, which is how earlier remote planning (`remote-phaseB-requirements.md` §5) had it.
