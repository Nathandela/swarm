# swarm — Implementation Goals

**Status**: Draft 1 (pre-audit)
**Purpose**: The orchestration contract for building the 15 epics in [build-plan.md](build-plan.md). Each epic closes only when every one of its exit criteria is demonstrably true (a passing automated test, a produced artifact, or a recorded verification). The audit committee validates this document before implementation starts, and audits the finished system against it at the end.
**Sources**: [system-spec.md](system-spec.md) (EARS ids), [system-invariants.md](../invariants/system-invariants.md) (S1–S12, L1–L3), ADR-001..004, Gate 3 gap resolutions G1–G6.

## Global goals (hold across all epics)

- **GG-1 Walking skeleton**: by end of Epic 8, one scripted demo passes end-to-end: launch fake agent → grouped general view → attach (snapshot paint) → type → detach → `kill -9` daemon → restart → reconnect with zero loss.
- **GG-2 Invariant coverage**: by end of Epic 14, every invariant S1–S12 and L1–L3 is asserted by at least one automated test, named in a traceability list.
- **GG-3 EARS traceability**: every EARS requirement in the spec maps to at least one test or a documented, justified exclusion.
- **GG-4 Quality gates**: at every epic close: `go build ./...`, `go vet ./...`, `golangci-lint run`, `go test ./...` all green; `-race` on every package that spawns goroutines.
- **GG-5 TDD process**: for each behavior, tests are written before implementation; reviewers reject implementation-first work.
- **GG-6 v1.0 scope discipline**: adapters = Claude Code + Codex only, each characterized before implementation; no other CLI code in v1.0.
- **GG-7 Docs stay true**: AGENTS.md remains a map (not a manual); decision changes produce/amend an ADR; `docs/specifications/protocol.md` exists from Epic 6 on; drift between docs and code is a review-blocking defect.
- **GG-8 Swarm process**: every epic is built by subagent fleets (test-designer → implementer → independent reviewers) iterating until reviewers agree; each epic gets a final audit pass; each epic ends with `bd close`, commit, and push.

## Per-epic definitions of done

### Epic 0 — Agentic codebase foundation
Goal: the repo itself is the contract for every later build session.
- E0.1 `AGENTS.md` finalized as a map: entry points, doc links, build/test commands, beads workflow, verification exit criteria.
- E0.2 agentic-codebase-manifesto vendored into `docs/governance/` with provenance (source repo + commit).
- E0.3 CI skeleton exists and runs on push (lint/test jobs may be no-ops until Epic 1, but the pipeline is wired).
- E0.4 ADR index in `docs/adr/`; `docs/INDEX.md` updated.
- E0.5 N-7 limitation (host sleep pauses agents) documented where users will see it.
- E0.6 Beads backlog verified to match the build plan (15 epics, dependency graph).

### Epic 1 — Foundations: binary, status kernel, persistence, fake agent
Goal: one static binary, the shared status vocabulary, durable session state, and the test workhorse.
- E1.1 Single binary with role dispatch (`swarm`, `swarm daemon`, `swarm shim`, `swarm hook`); subcommands stubbed but routed.
- E1.2 CI cross-compiles CGO_ENABLED=0 for darwin/linux × arm64/amd64; artifacts are statically linked.
- E1.3 `status` package: process × turn × interaction types + group derivation exactly matching the spec's derivation table, table-driven test proves 1:1.
- E1.4 Persistence: meta.json atomic temp+rename same-fs; torn-write injection test observes only old-or-new, never torn (S8).
- E1.5 schema_version on every meta.json + a working migration primitive (test: v1 file read by v2 code).
- E1.6 Roster rebuildable by directory scan (test: delete roster.json → identical registry).
- E1.7 Retention/delete rule (R-3) and `resumed_from` field implemented and tested.
- E1.8 Env allowlist filter drops non-allowlisted variables (test with poisoned env).
- E1.9 `swarm-fake-agent` scripted binary (print/ask/idle/exit directives) with a smoke test driving all four.

### Epic 2 — VT emulator: grid + snapshot
Goal: a terminal grid that any attach can be painted from.
- E2.1 **First task**: library validation spike — chosen vt10x-class library passes an alt-screen TUI fixture; decision + result recorded as ADR-005. If it fails: STOP, report, re-scope (plan-level risk gate).
- E2.2 `Emulator` API (`Feed`, `Snapshot`, `Resize`) with primary+alternate buffers, cursor, attrs, modes.
- E2.3 Snapshot round-trip test: feed fixture → snapshot → restore in a fresh grid → grids identical (S10 content half).
- E2.4 OSC 52 and hostile escapes filtered from the snapshot path (test with malicious fixture, N-6).
- E2.5 Golden-grid fixtures including a real alt-screen TUI capture; fuzz harness on split escape sequences and UTF-8 boundaries wired into CI.
- E2.6 Snapshot byte format versioned alongside the G2 message package.

### Epic 3 — Transcript capture
Goal: bounded, crash-tolerant, never-blocking session logs.
- E3.1 Size cap + rotation tested at boundaries.
- E3.2 Spinner/redraw collapse verified against recorded fixtures (N-3): collapsed output ≪ raw churn, content preserved.
- E3.3 Files created 0600 (S-5); test asserts perms.
- E3.4 Crash tolerance: truncated file still yields readable prefix (test).
- E3.5 Disk-full injection: writer drops tail, never blocks or errors the drain path (S9 half).

### Epic 4 — Shim process
Goal: the PTY-owning survivor process (ADR-001 made real).
- E4.1 Shim setsids; agent spawned in its own process group from argv array + captured env in requested cwd; S4 test: metacharacter-laden args arrive as single argv elements, no shell exec observed.
- E4.2 Per-session UDS serves the G2 message set (`snapshot, stream, write, resize, signal, exit-report`) from a versioned Go package.
- E4.3 Snapshot-then-stream continuity: under active output load, attach delivers exactly one snapshot then live frames with no gap/overlap (S10).
- E4.4 `signal` op does TERM → grace → KILL on the process group; no descendant survives (S5).
- E4.5 On agent exit: final snapshot + exit side-file written to session dir, shim exits (G3).
- E4.6 Daemon-death survival: kill the daemon (or its test stand-in) → shim keeps draining PTY indefinitely; bounded stream queue drops frames when consumer is slow, grid stays authoritative (S1 shim half, S9).

### Epic 5 — Daemon core
Goal: lifecycle authority that can die and come back without losing anyone.
- E5.1 Flock-before-bind singleton; stale socket unlinked only under lock; two simultaneous starts → exactly one survives and clients reach it (S12); client retry/backoff to the winner.
- E5.2 Registry rebuilt from meta scan; reconnect to live shims via PID+start-time; mismatch → `lost`, zero signals sent (S3).
- E5.3 Reconnect-before-lost ordering: a live shim is never transiently marked lost (test).
- E5.4 Two-phase launch (persist meta → spawn shim → confirm) with crash injection in every window: no orphan shims, no phantom sessions (S11).
- E5.5 Side-file merge into meta on observation/reconnect; daemon is sole meta writer (G6; test: shim never writes meta).
- E5.6 Kill/delete routing incl. R-3 retention delete; max-session cap enforced.
- E5.7 State dir 0700 asserted; version-skew detection + `swarm daemon restart` command works.
- E5.8 The S1 core test passes: `kill -9` daemon → every agent PID alive → restarted daemon lists and reconnects all sessions (L2).

### Epic 6 — Client protocol + daemon API
Goal: the low-reversibility wire surface, documented and hostile-input-proof.
- E6.1 G1 frame envelope codec (shared `wire` package): length-prefix + type tag; round-trip tests + fuzz in CI.
- E6.2 `docs/specifications/protocol.md` written at field level, versioned; doc drift from code is review-blocking (GG-7).
- E6.3 Handshake with version + capability negotiation; incompatible-version path tested (D-8, F-1, F-2).
- E6.4 Exclusive controller lease with generation ids: concurrent-attach test shows zero stale-generation inputs applied (S2); lease released on client EOF (L3).
- E6.5 Event fan-out with bounded per-client queues; wedged-subscriber test: disconnected within bound, PTY drain unaffected (S9, L1 ≤1 s).
- E6.6 Server-side revalidation of every client-supplied field; negative-path tests for each op.
- E6.7 Snapshot-before-live-frames ordering on attach guaranteed at the protocol layer.
- E6.8 Endpoint id + namespaced session ids on every message (V2 forward-compat).
- E6.9 Status groups computed daemon-side via the shared `status` package; clients never re-derive.

### Epic 7 — TUI: general view + launch form
Goal: the approved look (ui-preview.html), keyboard-complete, event-live.
- E7.1 Screen router (general/launch/attach sub-models); only the router is shared shell.
- E7.2 General view: grouped rows (Needs input / Working / Ready for review / Completed), heuristic last-line summary, in-TUI notification banner; teatest golden files match the ui-preview screens tab.
- E7.3 Status-event → row update reflected ≤1 s against a stub daemon (V-2 client half).
- E7.4 Full keyboard map tested: ↑/↓/j/k, Enter, Esc, `n`, Ctrl+X with confirm.
- E7.5 Launch form: free-text cwd with `~` expansion + validation, agent picker from detection with greyed/install-hint entries, options rendered from declarative adapter schema, initial prompt field, worktree toggle placeholder.
- E7.6 First-paint budget (N-1) measured in a test.
- E7.7 All TUI unit tests run against a protocol stub — no live daemon required.

### Epic 8 — Attach path (walking skeleton milestone)
Goal: full raw passthrough that always gives the terminal back.
- E8.1 Attach = raw mode (IXON off), snapshot-paint-then-live, full passthrough.
- E8.2 Detach on `Ctrl+\` (configurable); termios restored on detach AND on panic/signal (test kills the client hard and asserts a sane terminal).
- E8.3 Resize propagation only under current lease generation.
- E8.4 One-line toggleable chrome; completed rows render persisted final snapshot read-only (G3).
- E8.5 Added latency <10 ms p95, measured.
- E8.6 Scenarios 2, 3, 7, 8, 9, 10, 16 automated and green against the fake agent.
- E8.7 GG-1 walking-skeleton demo script exists, passes, and is wired into CI.

### Epic 9 — Adapter contract + characterization harness
Goal: the anti-corruption boundary, frozen before any real adapter exists.
- E9.1 `Adapter` interface (detect + version range, argv composition, declarative options schema, signal-source descriptors, resume + conversation-id extraction) frozen with a conformance suite (T-1).
- E9.2 Adapters proven stateless strategy objects: own no fds/disk/sockets (conformance-enforced where testable, review-enforced otherwise).
- E9.3 `swarm-char` harness drives a real CLI in a PTY and records versioned fixtures `{cli, version, scenario, pty_capture, hook_payloads[]}` (T-6).
- E9.4 Fixture-corpus schema versioned + schema-validated in CI.
- E9.5 Fixture-only reference adapter added with ZERO diffs to core/protocol/TUI — proven by inspection of the adding commit (T-5).
- E9.6 Capability-matrix entry emitted per characterized CLI.

### Epic 10 — Status detection engine
Goal: status that is authenticated, fresh, and never confidently wrong.
- E10.1 `swarm hook <event>` posts to daemon socket; token + socket path injected via env at spawn (G4); per-invocation install only.
- E10.2 S6 negative tests all no-op: tokenless, foreign-token, replayed-sequence, post-session-end callbacks (G5).
- E10.3 Staleness guard: no output ∧ no CPU beyond threshold while `active` → `unknown` (S7).
- E10.4 Precedence: fresher typed signal beats heuristic; heuristic never overrides fresher typed signal (test both directions).
- E10.5 Idempotent under duplicate and out-of-order hook deliveries.
- E10.6 CPU sampling works on macOS (proc_pidinfo) and Linux (/proc) — verified in-epic on both (CI covers Linux; macOS run recorded).
- E10.7 Status changes flow into Epic 6 fan-out; end-to-end ≤1 s to subscribers (V-2, L1).

### Epic 11 — Claude Code + Codex adapters
Goal: two real CLIs, each characterized first, proving the adapter boundary under two signal styles.
- E11.1 Claude Code: characterization fixtures + capability matrix committed BEFORE adapter code.
- E11.2 Claude Code adapter: hooks (PermissionRequest/Notification/Stop) via per-invocation settings injection; `--resume` + conversation-id extraction; passes conformance + its fixtures.
- E11.3 Codex: characterization fixtures + capability matrix committed BEFORE adapter code.
- E11.4 Codex adapter: typed turn/approval events per its interface; resume equivalent; passes conformance + its fixtures.
- E11.5 Resume-as-new-session flow (R-2): offered on ended/lost rows, `resumed_from` link recorded, engine + TUI wired.
- E11.6 Grid-heuristic fallback rules per CLI, tested against fixtures.
- E11.7 Scenarios 4, 5, 12 green.
- E11.8 Zero modifications to core/protocol/TUI packages in adapter commits (T-5 re-proven twice).

### Epic 12 — Worktree isolation
Goal: opt-in isolated worktrees without touching core control flow.
- E12.1 Launch toggle creates `.swarm/worktrees/<validated-id>` + branch `swarm/<id>`; non-repo → clear error (S-3).
- E12.2 Delete tears down (`git worktree remove` + prune) (R-3).
- E12.3 Implemented as pre-launch/pre-delete hooks on daemon core; review asserts no inline worktree branches in core.

### Epic 13 — Release packaging
Goal: installable by name, versioned end-to-end.
- E13.1 Release pipeline produces 4 static artifacts + checksums (dry-run in CI).
- E13.2 Version stamped at build feeds the D-8 handshake (test: binary reports the stamped version).
- E13.3 Homebrew tap formula + `go install` path verified; install docs written.

### Epic 14 — Integration Verification
Goal: the whole system proven against everything above.
- E14.1 All 18 spec scenarios automated (fake agent + recorded fixtures; real-CLI smoke behind a flag).
- E14.2 GG-2 satisfied: every invariant S1–S12, L1–L3 asserted; traceability list committed.
- E14.3 Failure injection suite: daemon kill -9 during spawn/write/attach; disk full; wedged subscriber; lease conflicts; PID reuse; protocol version skew; old-shim × new-daemon compat matrix.
- E14.4 Perf budgets measured and recorded: N-1 first paint, N-2 attach latency, N-3 churn handling.
- E14.5 Fuzz suites (frame codec, VT feed) running in CI.
- E14.6 Every row of the build plan's contracts-under-test table names its covering test(s).
- E14.7 GG-3 EARS traceability matrix committed.

## Orchestration protocol (how epics get built)

1. Epic opens: `bd update <id> --claim`; orchestrator decomposes into right-sized tasks.
2. Fleet dance per task cluster: test-designer agent(s) write failing tests → implementer agent(s) make them pass → independent reviewer agent(s) (different agents, Opus for correctness-critical) check against this document's exit criteria and the epic's contracts. Loop until reviewers approve.
3. Orchestrator integrates (never delegated), runs GG-4 gates, walks the epic's exit criteria one by one.
4. Final epic audit by a reviewer agent that saw none of the implementation; fixes dispatched if needed.
5. `bd close <id>`, commit, push. Parallelizable epics (e.g. 2‖3, 7 partially ‖ shim work) may run as concurrent fleets when their dependency edges allow.
6. After Epic 14: full audit-committee tour against this document; iterate until the committee agrees all goals are met.
