# swarm — Build Plan

**Status**: Approved (Gate 3, 2026-07-16)
**Source spec**: [system-spec.md](system-spec.md) (Draft 2) · ADR-001..004 · [audit-001](../verification/audit-001-system-spec.md) · [system invariants](../invariants/system-invariants.md)
**UI reference**: [docs/design/ui-preview.html](../design/ui-preview.html) — the interactive design preview (screens, keyboard dynamics, lifecycle, test strategy) is the canonical visual reference for Epics 7, 8, and 10. Published live at https://claude.ai/code/artifact/2959c9c2-1ab9-4ab1-ba35-e32d845ba0b7

Epics are ordered topologically; build top to bottom. Each epic is sized for one focused build session. Work is tracked in beads (one epic issue each, dependencies encoded).

**Milestone — walking skeleton (end of Epic 8):** launch a fake agent, see it grouped in the general view, attach (grid snapshot paint), type, detach, kill -9 the daemon and reconnect with nothing lost. The full architecture is proven before any real CLI adapter exists.

## Gap resolutions (decided at Gate 3, bind all epics)

| Gap | Decision |
|---|---|
| G1 Wire framing | Every message on both sockets is a length-prefixed frame with a 1-byte type tag (CONTROL = JSON payload, DATA_IN/DATA_OUT/SNAPSHOT = binary). "NDJSON" applies inside CONTROL payloads only. One demux, no ambiguity. Amends ADR-002. |
| G2 Shim wire schema | Same frame envelope; message set (`snapshot, stream, write, resize, signal, exit-report`) versioned in its own Go package, compat-tested old-shim × new-daemon (D-5 implies version skew here too). |
| G3 Shim lifetime after agent exit | Shim writes a final grid snapshot + exit side-file into the session dir, then exits. Completed rows render the persisted snapshot read-only; no lingering processes. |
| G4 Hook callback transport | Hook/event commands invoke `swarm hook <event>` (same binary), which posts to the daemon socket; session token + socket path injected via env at spawn. |
| G5 Callback replay | Callbacks carry token + monotonic sequence; daemon rejects non-increasing. |
| G6 meta.json single writer | The daemon is the only writer of meta.json. Shims write only transcript, final-snapshot, and exit side-files; the daemon merges side-files on observation/reconnect. |

---

## - [ ] Epic 0: Agentic codebase foundation

**Scope IN**: Finalized `AGENTS.md` (map, not manual) and `.claude/CLAUDE.md` (project rules: TDD, beads workflow, verification exit criteria); `docs/INDEX.md` navigation hub; vendor the agentic-codebase-manifesto into `docs/governance/`; ADR index; beads backlog loaded (these epics + deps); verification-pipeline scaffold and CI skeleton (lint/test gates activate in Epic 1); document N-7 limitation (sleep pauses agents).
**Scope OUT**: Any Go code.
**EARS**: N-5.
**Contracts**: none runtime; the docs hierarchy IS the contract for every later build session.
**Assumptions**: beads (`bd`) stays the tracker; manifesto contents are stable enough to vendor.
**Depends on**: —

## - [ ] Epic 1: Foundations — binary, status kernel, persistence, fake agent

**Scope IN**: Go module; single binary with role dispatch (`swarm`, `swarm daemon`, `swarm shim`, `swarm hook`); CGO_ENABLED=0 cross-compile (darwin/linux × arm64/amd64) wired into CI; shared dependency-free `status` package (process × turn × interaction types + group-derivation table — the one place these exist, imported by persistence, protocol, engine, TUI); persistence package per ADR-003 (meta.json atomic temp+rename, schema_version + migration primitive, roster rebuild-by-scan, retention/delete, `resumed_from`, env allowlist filter); `swarm-fake-agent` test binary (scripted: print, ask, idle, exit — drives every later test).
**Scope OUT**: any running daemon/shim logic (dispatch stubs).
**EARS**: N-4, S-2, R-1, R-3 (retention rule), parts of S-6 (allowlist).
**Contracts**: `status` package API (data-only); meta.json schema (data-only, schema_version-tagged); fake-agent script format (data-only).
**Implicit**: meta writes serialized per session (single goroutine writer — G6); atomicity = temp+rename same-fs.
**Assumptions**: JSON files sufficient at human session counts (ADR-003).
**Depends on**: Epic 0.

## - [ ] Epic 2: VT emulator — grid + snapshot

**Scope IN**: Evaluate/integrate a vt10x-class library (decision recorded as a short ADR); grid model (cells, attrs, cursor, modes, primary+alternate buffers); `Feed(bytes)`; serialized snapshot of the full terminal state; OSC 52/hostile-escape filtering in the snapshot path; golden-grid fixture tests + fuzz on split escape sequences/UTF-8 boundaries.
**Scope OUT**: PTY ownership, transcripts, sockets.
**EARS**: A-4 (snapshot content), N-6.
**Contracts**: `Emulator` Go API: `Feed`, `Snapshot() ([]byte)`, `Resize` (behavioral); snapshot byte format (data-only, versioned with G2 package).
**Implicit**: single-goroutine Feed (caller serializes); Snapshot atomic w.r.t. Feed.
**Assumptions**: an existing Go VT library covers alt-screen + modes well enough to extend rather than write from scratch — validate FIRST in this epic; if false, this epic re-scopes and the plan is revisited (highest-risk assumption in the plan).
**Depends on**: Epic 1.

## - [ ] Epic 3: Transcript capture

**Scope IN**: Append-only transcript writer: size cap, rotation, spinner/redraw-frame collapse before disk, 0600 perms, readable-prefix crash tolerance, disk-full degradation (drop tail, never block).
**Scope OUT**: PTY, emulator internals (consumes the same byte stream).
**EARS**: S-5 (transcript half), R-1 (transcript), N-3 (spinner churn).
**Contracts**: `Transcript` Go API (behavioral): `Write(bytes)`, rotation policy config.
**Implicit**: write path must never block the PTY drain (S9); fsync best-effort.
**Assumptions**: collapse heuristic = drop consecutive frames that only repaint (cursor-home + rewrite patterns); tuned against fixtures.
**Depends on**: Epic 1.

## - [ ] Epic 4: Shim process

**Scope IN**: `swarm shim` role: setsid; PTY master via creack/pty; spawn agent from argv array + captured env in requested cwd, own process group; per-session UDS serving G2 message set (`snapshot` from Epic-2 emulator, `stream`, `write`, `resize`, `signal` to process group TERM→grace→KILL); pipes PTY bytes → emulator + transcript; on agent exit: final snapshot + exit side-file, then exit (G3); always drains PTY regardless of daemon presence.
**Scope OUT**: daemon, client protocol, adapters.
**EARS**: D-2, D-3, S-1 (exec half), S-4, S-6 (spawn half), S-5 (assembly).
**Contracts**: daemon⇄shim wire (G2, behavioral): ops above + exit-report; snapshot-then-stream continuity (no gap/overlap) — invariant S10.
**Implicit**: shim owns PTY master/emulator/transcript/socket; bounded stream queue to daemon (drop frames when slow — grid stays authoritative); survives daemon death indefinitely.
**Assumptions**: fake agent (Epic 1) is a sufficient stand-in for real CLIs at this layer (PTY semantics are CLI-agnostic).
**Depends on**: Epics 2, 3.

## - [ ] Epic 5: Daemon core — lifecycle, registry, orchestration

**Scope IN**: `swarm daemon` role: detached start (setsid, stdio→log); flock-before-bind singleton + stale-socket unlink under lock; client-spawn retry/backoff to lock winner; registry rebuild from meta scan; reconnect to live shims (PID+start-time verify); mark gone → `lost`; merge shim side-files into meta (G6); launch orchestration (two-phase: persist meta → spawn shim → confirm) and kill/delete routing; max-session cap; state dir 0700; version-skew detection + `swarm daemon restart` command.
**Scope OUT**: client-facing protocol planes (Epic 6), status interpretation (Epic 10).
**EARS**: D-1, D-4, D-5, D-6, D-7, D-8, S-1 (orchestration), S-7, R-3 (delete).
**Contracts**: consumes G2 wire as client (behavioral); registry Go API for Epic 6 (behavioral); meta.json (data — sole writer).
**Implicit**: reconnect-before-lost ordering (never transiently mark a live shim lost); orchestration crash windows covered by S11 bijection tests.
**Assumptions**: flock + PID/start-time are sufficient identity primitives on macOS + Linux (verified by characterization tests in-epic).
**Depends on**: Epic 4 (wire schema; live shim for integration tests — unit tests mock it).

## - [ ] Epic 6: Client protocol + daemon API

**Scope IN**: G1 frame envelope codec (shared `wire` package); control ops (handshake w/ version+capability negotiation, list, launch [carries env], kill, delete, attach/detach, resize, subscribe); data frames (PTY I/O, snapshot); event fan-out with bounded per-client queues + slow-subscriber disconnect; exclusive controller lease + generation ids; server-side re-validation of all inputs; endpoint id + namespaced session ids on every message.
**Scope OUT**: TUI rendering; shim internals.
**EARS**: P-1..P-6, F-1, F-2, S-6 (RPC), D-8 (handshake behavior).
**Contracts**: THE low-reversibility surface — client⇄daemon protocol (behavioral, versioned, documented field-level in `docs/specifications/protocol.md` written in-epic); status groups computed daemon-side via the shared `status` package (TUI never re-derives).
**Implicit**: one reader goroutine per conn; all client writes via bounded queue; snapshot-before-live-frames ordering on attach; lease released on EOF (L3).
**Assumptions**: one socket with type-tagged frames beats two sockets (revisit only on profiling evidence — ADR-002).
**Depends on**: Epic 5.

## - [ ] Epic 7: TUI — general view + launch form

**Scope IN**: Bubble Tea app + protocol client + screen router (general/launch/attach sub-models — the router is the only shared shell); grouped general view ≤1 s event reflect; keyboard nav (↑/↓/j/k, Enter, Esc, Ctrl+X confirm, `n`); row rendering with heuristic last-line summary; in-TUI notification banner; launch form (cwd + `~` expansion, agent picker from detection + greyed/install-hint, options rendered from declarative adapter schema, initial prompt, worktree toggle placeholder); first-paint budget. Visual reference: **docs/design/ui-preview.html** (screens tab is the approved look).
**Scope OUT**: attach passthrough (Epic 8).
**EARS**: V-1..V-6, L-1..L-3, N-1.
**Contracts**: consumes Epic 6 protocol only (behavioral); teatest golden files are the acceptance record.
**Implicit**: builds/tests against a stub daemon implementing the protocol — no live-daemon dependency in unit tests.
**Assumptions**: hardcoded/fake adapter schema acceptable until Epic 9 lands the real one.
**Depends on**: Epic 6 (schema; stub for tests).

## - [ ] Epic 8: Attach path — walking skeleton milestone

**Scope IN**: attach sub-model: raw mode (IXON off) full passthrough; snapshot-paint-then-live; detach `Ctrl+q` (configurable; ADR-006) + termios restore on detach AND panic/signal; resize propagation under lease; one-line toggleable chrome; latency budget (<10 ms p95 added); completed-row read-only snapshot render (G3). End-to-end assembly: scenarios 2, 3, 7, 8, 9, 10, 16 green against the fake agent.
**Scope OUT**: real adapters, detection.
**EARS**: A-1..A-5, N-2, P-5 (client side).
**Contracts**: composition across Epics 4-6-7 — this epic's tests ARE the cross-layer contract tests for the attach flow (S2, S10).
**Implicit**: exactly-once snapshot/stream boundary; stale-generation input rejected end-to-end.
**Assumptions**: none new.
**Depends on**: Epics 4, 6, 7.

## - [ ] Epic 9: Adapter contract + characterization harness

**Scope IN**: the one `Adapter` interface (T-1: detect + version range, argv composition, declarative options schema, signal-source descriptors, resume + conversation-id extraction) — frozen with a conformance test suite BEFORE any real adapter; characterization harness (`swarm-char` dev tool): drives a real CLI in a PTY, records fixtures `{cli, version, scenario, pty_capture, hook_payloads[]}` (versioned fixture-corpus schema), emits the capability-matrix entry; a fixture-only reference adapter proving T-5 (zero core/protocol/TUI edits to add).
**Scope OUT**: Claude/Codex specifics; the runtime engine.
**EARS**: T-1, T-5, T-6.
**Contracts**: `Adapter` Go interface (behavioral — the anti-corruption boundary); fixture-corpus schema (data-only, versioned).
**Implicit**: adapters are stateless/goroutine-safe strategy objects; they own no fds, no disk, no sockets — core owns all lifecycle.
**Assumptions**: harness reuses Epic 2 emulator + Epic 4 PTY plumbing as libraries.
**Depends on**: Epics 2, 4 (as libraries), 1.

## - [ ] Epic 10: Status detection engine

**Scope IN**: engine consuming adapter signal-sources: typed-signal ingestion via `swarm hook` → daemon socket (G4; token + monotonic sequence, G5; per-invocation install only); grid-heuristic evaluation on output events + low-frequency fallback poll; staleness guard (no output ∧ no CPU while active → `unknown`); precedence rule (fresher typed signal beats heuristic); derivation to view groups via the shared `status` package; events into Epic 6 fan-out.
**Scope OUT**: any CLI-specific rule (lives in adapters).
**EARS**: T-2 (mechanism), T-3, T-4, V-2 (production side).
**Contracts**: callback message (behavioral, authenticated — S6); engine⇄adapter signal-descriptor API (behavioral).
**Implicit**: idempotent under duplicate/out-of-order hooks; single status writer per session (G6 chain).
**Assumptions**: process-CPU sampling is portable enough via /proc + proc_pidinfo (verified in-epic).
**Depends on**: Epics 6, 9.

## - [ ] Epic 11: Claude Code + Codex adapters (v1.0 agents)

**Scope IN**: For EACH of Claude Code and Codex, in order: (1) characterize via Epic 9 harness → fixtures + capability matrix committed; (2) build the adapter against its own fixtures (Claude: hooks PermissionRequest/Notification/Stop, per-invocation settings injection, `--resume` + conversation-id; Codex: typed turn/approval events per its app-server/exec interface, resume equivalent); (3) resume-as-new-session flow (R-2: offer on ended/lost rows, `resumed_from` link) — engine + TUI hook-up; grid-heuristic fallback rules per CLI. Split into 11a/11b mid-flight if one session overflows.
**Scope OUT**: Gemini, OpenCode, AGY (v1.1 — AGY only after characterization).
**EARS**: T-2 (real), T-7, R-2; scenarios 4, 5, 12.
**Contracts**: none new — proves Epic 9's interface under two different signal styles (that is the point).
**Assumptions**: current Claude Code hook semantics and Codex event surface as recorded by the harness (fixtures pin versions; drift = re-characterize, T-6).
**Depends on**: Epics 9, 10.

## - [ ] Epic 12: Worktree isolation

**Scope IN**: functional launch toggle: create `.swarm/worktrees/<id>` (validated id), branch `swarm/<id>`, error path for non-repo; teardown on delete (`git worktree remove` + prune); implemented as pre-launch/pre-delete hooks registered with daemon core (no inline branches in core control flow).
**EARS**: S-3, R-3 (teardown).
**Contracts**: launch/delete hook points on daemon core (behavioral).
**Assumptions**: plain `git` CLI available when the toggle is used (not a swarm runtime dependency).
**Depends on**: Epic 5 (hook points), Epic 7 (toggle UI).

## - [ ] Epic 13: Release packaging

**Scope IN**: goreleaser (or equivalent) release pipeline: 4 static targets, checksums; Homebrew tap formula; `go install` path verified; version stamping feeding the D-8 handshake; install docs.
**EARS**: N-4 (release half — CI matrix exists since Epic 1).
**Contracts**: none runtime.
**Assumptions**: private repo now — tap goes public at first public release; pipeline built and tested before that.
**Depends on**: Epic 8 (skeleton shippable), Epic 11 (v1.0 content).

## - [ ] Epic 14: Integration Verification

**Scope IN**: the full 18-row scenario table as an automated suite (fake agent + recorded fixtures; real-CLI smoke behind a flag); all invariants in `docs/invariants/system-invariants.md` asserted (S1-S12, L1-L3); failure injection: daemon kill -9 during spawn/write/attach, disk full, wedged subscriber, lease conflicts, PID reuse, protocol version skew, old-shim × new-daemon compat; perf budgets measured (N-1, N-2, N-3); fuzz suites (frame codec, VT feed) in CI.
**Depends on**: ALL epics.

### Contracts under test

| Contract | Source → Target | Type | IV scope |
|---|---|---|---|
| `status` package types + derivation | E1 → E5, E6, E7, E10 | Data-only | LIGHT: schema/derivation table tests |
| meta.json schema (+schema_version) | E1 → E5 | Data-only | LIGHT: round-trip + migration + torn-write |
| Fixture-corpus schema | E9 → E11 | Data-only | LIGHT: schema validation |
| Snapshot byte format | E2 → E4, E6, E8 | Data-only | LIGHT: round-trip vs emulator state |
| Emulator API (Feed/Snapshot/Resize) | E2 → E4, E9 | Behavioral | MEDIUM: golden grids + fuzz |
| Transcript writer | E3 → E4 | Behavioral | MEDIUM: cap/rotate/collapse/disk-full |
| daemon⇄shim wire (G2) | E4 → E5 | Behavioral | MEDIUM + old-shim × new-daemon compat matrix |
| client⇄daemon protocol | E6 → E7, E8 | Behavioral | MEDIUM: contract tests vs stub both sides |
| Adapter interface conformance | E9 → E11 | Behavioral | MEDIUM: conformance suite, two real adapters |
| Hook callback (token + sequence) | E10 → E11 | Behavioral | MEDIUM: auth/replay negative tests |
| Attach flow (lease/snapshot/raw/resize) | E4+E6+E7+E8 | Composition | FULL: end-to-end under load + failure injection |
| Session lifecycle (launch→kill/delete, crash windows) | E4+E5 | Composition | FULL: S11 bijection under crash injection |
| Daemon restart/upgrade survival | E4+E5+E6 | Composition | FULL: scenarios 10/11 soak |
| Status pipeline (signal→engine→fan-out→TUI) | E9+E10+E6+E7 | Composition | FULL: ≤1 s latency, staleness, precedence |

---

## Implementation Guidelines

Ground rules for whoever builds an epic from this plan -- not a separate phase or skill, just the discipline the build should follow.

- **TDD**: write failing tests before implementation, for each task. No mocked business logic. Tests describe expected behavior, not implementation detail.
- **Swarm, don't solo**: break the epic into right-sized tasks (one subagent can finish a task without needing another task's output mid-flight). Dispatch independent tasks together, in one message, so they run concurrently. Dependent tasks dispatch after their blocker returns.
- **Model choice is deliberate**: Opus for architecturally significant, ambiguous, or security-/correctness-critical work; Sonnet for well-specified, routine, mechanically parallelizable work. Don't default to one model for everything.
- **Context is not shared**: each dispatched subagent has no memory of prior conversation. Give it the task, the relevant files/interfaces, and its acceptance criteria explicitly.
- **Integration is not delegated**: wiring the pieces together and resolving overlap between subagent outputs is the orchestrator's own judgment call, not another task to dispatch.
- **Independent review before done**: every epic gets a review pass from an agent that did not implement it, checking correctness against acceptance criteria and interface contracts -- not restating what was built.
- **Verify before marking done**: run the project's build/test/lint commands and check them against the epic's acceptance criteria and interface contracts one by one.
- **Stop, don't push through**: if scope turns out unclear, tests fail for reasons that survive a couple of fix cycles, or review finds a critical security issue, stop and report rather than looping indefinitely.
