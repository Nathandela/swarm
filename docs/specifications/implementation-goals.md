# swarm — Implementation Goals

**Status**: Approved (Draft 2 post [audit-002](../verification/audit-002-implementation-goals.md); independent verification confirmed 35/35 committee findings resolved)
**Purpose**: The orchestration contract for building the 15 epics in [build-plan.md](build-plan.md). Each epic closes only when every one of its exit criteria is demonstrably true (a passing automated test, a produced artifact, or a recorded verification). The finished system is audited against this document.
**Sources**: [system-spec.md](system-spec.md) (EARS ids), [system-invariants.md](../invariants/system-invariants.md) (S1–S12, L1–L3), ADR-001..004, Gate 3 gap resolutions G1–G6.

## Global goals (hold across all epics)

- **GG-1 Walking skeleton**: by end of Epic 8, one scripted demo passes end-to-end: launch fake agent → grouped general view → attach (snapshot paint) → type → detach → `kill -9` daemon → restart → reconnect with zero loss. *Zero loss means*: every agent process alive, authoritative grid recovered (client grid == shim grid), meta.json intact, transcript intact per retention policy. Transient DATA_OUT frames dropped while no client was attached are not loss.
- **GG-2 Invariant coverage**: by end of Epic 14, every invariant S1–S12 and L1–L3 is asserted by at least one automated test, named in a traceability list.
- **GG-3 EARS traceability**: every EARS requirement maps to at least one test, or an exclusion approved via ADR or audit waiver — never a bare "documented justification".
- **GG-4 Quality gates**: at every epic close: `go build ./...`, `go vet ./...`, `golangci-lint run`, `go test ./...` all green; `-race` on every package that spawns goroutines.
- **GG-5 TDD process**: tests written before implementation for each behavior; the failing-first run is evidenced (ordered commits or recorded test output kept with the epic's evidence file); reviewers reject implementation-first work.
- **GG-6 v1.0 scope discipline**: adapters = Claude Code + Codex only, each characterized before implementation; no other CLI code in v1.0.
- **GG-7 Docs stay true**: AGENTS.md remains a map; decision changes produce/amend an ADR; `docs/specifications/protocol.md` exists from Epic 6 on, and CI diffs its field table against the `wire` package's exported types (drift fails the build).
- **GG-8 Swarm process**: code epics are built by subagent fleets per the orchestration protocol below; docs-only or mechanical epics may use a single implementer, but independent review is never waived; each epic ends with `bd close`, commit, and push.

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
- E1.1 Single *distributed* binary with role dispatch (`swarm`, `swarm daemon`, `swarm shim`, `swarm hook`); subcommands stubbed but routed. (`swarm-fake-agent`, later `swarm-char`, are separate dev/test binaries, never shipped.)
- E1.2 CI cross-compiles CGO_ENABLED=0 for darwin/linux × arm64/amd64; artifacts are statically linked.
- E1.3 `status` package: process × turn × interaction types + group derivation exactly matching the spec's derivation table; table-driven test proves 1:1.
- E1.4 Persistence: meta.json atomic temp+rename same-fs; process-crash injection mid-write observes only old-or-new, never torn (S8 crash model = process crash, not power loss — recorded in ADR-003); one corrupted session file never prevents scanning the rest (S8 isolation half).
- E1.4b meta.json round-trip test asserts presence and correct (de)serialization of every S-2 field: id, agent type, cwd, launch options, captured env, created-at, status dimensions, last-activity, shim PID+start-time, conversation id, exit code, schema_version.
- E1.5 schema_version on every meta.json + working migration primitive (test: v1 file read by v2 code).
- E1.6 Roster rebuildable by directory scan (test: delete roster.json → identical registry).
- E1.7 Retention/delete rule (R-3) and `resumed_from` field implemented and tested.
- E1.8 Env allowlist: normative allowlist documented (PATH, HOME, SHELL, locale, TERM, venv/conda vars, provider credential patterns — the list lives with the code); filter test with poisoned env asserts drops AND that allowlisted values pass.
- E1.9 `swarm-fake-agent` scripted binary (print/ask/idle/exit directives) with a smoke test driving all four.

### Epic 2 — VT emulator: grid + snapshot
Goal: a terminal grid that any attach can be painted from.
- E2.1 **First task**: library validation spike — chosen vt10x-class library passes an alt-screen TUI fixture; decision + result recorded as ADR-005. If it fails: STOP, report, re-scope (plan-level risk gate).
- E2.2 `Emulator` API (`Feed`, `Snapshot`, `Resize`) with primary+alternate buffers, cursor, attrs, modes.
- E2.3 Snapshot fidelity test: feed fixture → serialize snapshot → decode and compare against the emulator's known state (no production Restore API required; the client-side painter lands in Epic 8).
- E2.4 Hostile-escape filtering in the snapshot path, against a normative fixture list (OSC 52 clipboard write, at minimum; the fixture file is the filter's contract) (N-6).
- E2.5 Golden-grid fixtures including a real alt-screen TUI capture; fuzz harness on split escape sequences and UTF-8 boundaries wired into CI.
- E2.6 Snapshot byte format versioned alongside the G2 message package.

### Epic 3 — Transcript capture
Goal: bounded, crash-tolerant, never-blocking session logs.
- E3.1 Size cap + rotation tested at boundaries.
- E3.2 Spinner/redraw collapse: a fixture of K repaint frames yields ≤ C disk writes (K and C fixed by the fixture), and post-collapse content preserves the final frame byte-identically (N-3 churn half).
- E3.3 Files created 0600 (S-5); test asserts perms.
- E3.4 Crash tolerance: truncated file still yields readable prefix (test).
- E3.5 Disk-full injection: writer drops tail, never blocks or errors the drain path (S9 half).

### Epic 4 — Shim process
Goal: the PTY-owning survivor process (ADR-001 made real).
- E4.1 Shim setsids; agent spawned in its own process group from argv array + captured env in requested cwd. S4 test: metacharacter-laden args arrive as single argv elements, no shell exec observed. S-6 differential test: daemon running under env A, launch carrying client env B → spawned agent's environment == filtered(B), never A.
- E4.2 Per-session UDS serves the G2 message set (`snapshot, stream, write, resize, signal, exit-report`) from a versioned Go package.
- E4.3 Snapshot-then-stream continuity: under active output load, attach delivers exactly one snapshot then live frames with no gap/overlap (S10).
- E4.4 `signal` op does TERM → grace → KILL on the session process group; no process remaining in that group survives (S5). (Descendants that deliberately escape into a new session are out of scope — process groups are the approved containment boundary.) Exit outcome reported via exit-report (S-4 recording lands daemon-side, E5.6).
- E4.5 On agent exit: final snapshot + exit side-file written to session dir, shim exits (G3).
- E4.6 Daemon-death survival: kill the daemon stand-in → shim continues draining the PTY for a bounded soak (≥30 s under active output) with no growth in memory beyond the bounded queue; slow/absent consumer → frames dropped, grid stays authoritative (S1 shim half, S9).

### Epic 5 — Daemon core
Goal: lifecycle authority that can die and come back without losing anyone.
- E5.1 Flock-before-bind singleton; stale socket unlinked only under lock; two simultaneous starts → exactly one survives and clients reach it (S12); client retry/backoff to the winner.
- E5.2 Registry rebuilt from meta scan; reconnect to live shims via PID+start-time; mismatch → `lost`, zero signals sent (S3).
- E5.3 Reconnect-before-lost ordering: a live shim is never transiently marked lost (test).
- E5.4 Two-phase launch (persist meta → spawn shim → confirm) with crash injection at each phase boundary: no orphan shims, no phantom sessions (S11).
- E5.5 Side-file merge into meta on observation/reconnect; daemon is sole meta writer (G6; test: shim never writes meta).
- E5.6 Kill/delete routing: kill outcome persisted to meta (S-4); R-3 retention delete; configurable max-session cap enforced with a clear inline error to the client, tested at a non-default cap value (S-7).
- E5.7 Permission sweep: with a permissive umask forced, every artifact is created with correct mode and re-checked after replacement — state dir 0700; daemon socket, per-session shim sockets, meta.json, roster.json, final snapshots, exit side-files, daemon log 0600 (ADR-004, D-6).
- E5.8 The S1 core test passes: `kill -9` daemon → every agent PID alive → restarted daemon lists and reconnects all sessions (L2).
- E5.9 D-1 auto-start: a client command finding no live daemon spawns one detached (setsid, stdio→log) and connects transparently; test: cold state dir → `swarm list` succeeds with no pre-started daemon (scenario 1).
- E5.10 Version-skew smoke: an old-shim × new-daemon pair from adjacent builds interoperates (list + reconnect); full compat matrix lands at E14.3.
- E5.11 Version-skew detection + `swarm daemon restart`: incompatible daemon is detected, the client's error names the fix, and the restart command performs it (D-8 UX half).

### Epic 6 — Client protocol + daemon API
Goal: the low-reversibility wire surface, documented and hostile-input-proof.
- E6.1 G1 frame envelope codec (shared `wire` package): length-prefix + type tag; a defined maximum frame size enforced *before* allocation; tests for oversized declared length, partial header, truncated payload, unknown type tag, and length overflow; round-trip tests + fuzz in CI; CONTROL frames carry JSON, DATA_IN/DATA_OUT/SNAPSHOT carry opaque binary — demux tested explicitly; test asserts the same envelope runs on both client⇄daemon and daemon⇄shim sockets (G1).
- E6.2 `docs/specifications/protocol.md` written at field level, versioned; CI drift check per GG-7.
- E6.3 Handshake with version + capability negotiation; incompatible-version path returns a clear error that names `swarm daemon restart` AND states the restart is safe / loses no live sessions — test asserts the safety statement in the error text (D-8's "SHALL say so", F-1).
- E6.4 Exclusive controller lease with generation ids: concurrent-attach test shows zero stale-generation inputs applied (S2); lease AND its event stream/subscription released on client EOF, next attach succeeds (P-4, L3).
- E6.5 Event fan-out with bounded per-client queues; wedged-subscriber test: disconnected within bound, PTY drain unaffected (S9, L1).
- E6.6 Server-side revalidation of every client-supplied field; negative-path tests for each op.
- E6.7 Snapshot-before-live-frames ordering on attach guaranteed at the protocol layer.
- E6.8 Endpoint id + namespaced session ids on every message where the fields apply (V2 forward-compat).
- E6.8b F-2 check: recorded schema review confirms no message type references a UDS-specific construct (fd passing, socket path, local-only address) — transport-neutral by construction.
- E6.9 Status groups computed daemon-side via the shared `status` package; clients never re-derive.

### Epic 7 — TUI: general view + launch form
Goal: the approved look (ui-preview.html), keyboard-complete, event-live.
- E7.1 Screen router (general/launch/attach sub-models); only the router is shared shell.
- E7.2 General view: grouped rows (Needs input / Working / Ready for review / Completed); every row shows agent name, shortened cwd, status, elapsed/last-activity time, and grid-derived last-line summary (V-4 — teatest goldens must demonstrate each field); highlight + notification banner fire for BOTH Needs-input and Ready-for-review transitions (V-5); goldens match the ui-preview screens tab.
- E7.3 Status-event → row update reflected ≤500 ms against a stub daemon (client half of the L1 ≤1 s composite; server half in E10.7; composition asserted at E14).
- E7.4 Full keyboard map tested: ↑/↓/j/k, Enter, Esc, `n`, Ctrl+X with confirm — Ctrl+X kills running sessions, deletes completed/lost ones, and the confirm prompt states which (R-3).
- E7.5 Launch form: free-text cwd with `~` expansion; invalid cwd → launch refused with inline error (L-3); agent picker greys both not-installed AND out-of-supported-version-range CLIs with install/upgrade hint (L-2); options rendered from declarative adapter schema; initial prompt field; worktree toggle placeholder.
- E7.6 First-paint test asserts p95 < 100 ms with 50 sessions listed; fails if exceeded (N-1).
- E7.7 All TUI unit tests run against a protocol stub — no live daemon required.

### Epic 8 — Attach path (walking skeleton milestone)
Goal: full raw passthrough that always gives the terminal back.
- E8.1 Attach = raw mode (IXON off), snapshot-paint-then-live, full passthrough.
- E8.2 Detach on `Ctrl+q` (configurable; ADR-006). PTY-based integration test verifies termios restoration after: normal detach, Go panic, SIGINT, SIGTERM, SIGHUP. SIGKILL restoration is not claimed (impossible without a wrapper process — accepted limitation, documented).
- E8.3 Resize propagation only under current lease generation.
- E8.4 One-line toggleable chrome; completed rows render persisted final snapshot read-only (G3).
- E8.5 Added latency: keystroke echo round-trip p95 < 10 ms over ≥1000 samples on the CI runner class, asserted (N-2); method recorded with the results.
- E8.6 Scenarios 2, 3, 7, 8, 9, 10, 16 automated and green against the fake agent, PLUS an attach-under-daemon-kill injection (daemon killed mid-attach → shim unaffected, client detaches sanely, re-attach after restart works).
- E8.7 GG-1 walking-skeleton demo script exists, passes, is wired into CI, and is flagged for human (Nathan) acceptance before the epic closes.

### Epic 9 — Adapter contract + characterization harness
Goal: the anti-corruption boundary, frozen before any real adapter exists.
- E9.1 `Adapter` interface (detect + version range, argv composition, declarative options schema, signal-source descriptors, resume + conversation-id extraction) frozen with a conformance suite (T-1).
- E9.2 Adapter statelessness: conformance suite exercises adapters through the interface only; detection is descriptor-based (adapters supply pure `Binary`/`VersionArgs`/`ParseVersion`; the core `Detect(a, HostProber)` owns the LookPath/exec). An automated source grep over the contract package (`internal/adapter`) AND every adapter package, outside tests, bans `os.Open`/`os.OpenFile`/`os.Create`/`os.CreateTemp`/`os.ReadFile`/`os.WriteFile`/`os.ReadDir`/`os.MkdirAll`/`io/ioutil`/`net.Listen`/`net.Dial`/`net.Dialer`/`net.ListenConfig`/`exec.Command`/`exec.LookPath`/`syscall.Open`/`syscall.Socket` — zero hits (fixture disk I/O lives in `internal/adapter/fixtureio`, the detection exec in `internal/adapter/detect`, both harness-side). Result recorded in the epic evidence file.
- E9.3 `swarm-char` harness drives a real CLI in a PTY and records versioned fixtures `{cli, version, scenario, pty_capture, hook_payloads[]}` (T-6).
- E9.4 Fixture-corpus schema versioned + schema-validated in CI.
- E9.5 Fixture-only reference adapter added such that no package outside `adapters/<name>/` and the adapter registration table changes — verified by package-dependency check, not commit archaeology (T-5).
- E9.6 Capability-matrix entry emitted per characterized CLI.

### Epic 10 — Status detection engine
Goal: status that is authenticated, fresh, and never confidently wrong.
- E10.1 `swarm hook <event>` posts to daemon socket; token + socket path injected via env at spawn (G4); per-invocation install only.
- E10.2 S6 negative tests all no-op: tokenless, foreign-token, replayed-sequence, post-session-end callbacks (G5).
- E10.3 Staleness guard: no output ∧ no CPU beyond threshold while `active` → `unknown` (S7).
- E10.4 Precedence: fresher typed signal beats heuristic; heuristic never overrides fresher typed signal (test both directions).
- E10.5 Idempotent under duplicate and out-of-order hook deliveries.
- E10.6 CPU sampling: Linux (/proc) and macOS (proc_pidinfo) paths BOTH unit-tested in CI (linux + macos runners); real-process integration test on each.
- E10.7 Status changes reach Epic 6 subscribers ≤500 ms from signal arrival (server half of L1; composite ≤1 s asserted at E14).
- E10.8 Grid-heuristic evaluator: runs on output events plus a low-frequency fallback poll at a stated bounded frequency; no busy-polling (idle-CPU assertion); inconclusive detection maps deterministically to `unknown` (T-3, T-4; positive-idle case = scenario 6).

### Epic 11 — Claude Code + Codex adapters
Goal: two real CLIs, each characterized first, proving the adapter boundary under two signal styles.
- E11.1 Claude Code: characterization fixtures + capability matrix committed BEFORE adapter code.
- E11.2 Claude Code adapter: hooks (PermissionRequest/Notification/Stop) via per-invocation settings injection; `--resume` + conversation-id extraction; passes conformance + its fixtures.
- E11.3 Codex: characterization fixtures + capability matrix committed BEFORE adapter code.
- E11.4 Codex adapter: typed turn/approval events per its interface; resume equivalent; passes conformance + its fixtures.
- E11.5 Resume-as-new-session flow (R-2): offered on ended/lost rows, `resumed_from` link recorded. The generic engine/TUI resume wiring is core work inside this epic, landed separately from adapter packages.
- E11.6 Grid-heuristic fallback rules per CLI, tested against fixtures; out-of-supported-version fixture proves L-2 greying end-to-end.
- E11.7 Scenarios 4, 5, 12 green.
- E11.8 Adapter packages contain no core edits: same package-dependency check as E9.5, applied to both adapters (T-5).

### Epic 12 — Worktree isolation
Goal: opt-in isolated worktrees without touching core control flow.
- E12.1 Launch toggle creates `.swarm/worktrees/<validated-id>` + branch `swarm/<id>`; non-repo → clear error (S-3).
- E12.2 Delete tears down (`git worktree remove` + prune) (R-3).
- E12.3 Implemented as pre-launch/pre-delete hooks on daemon core; review asserts no inline worktree branches in core.

### Epic 13 — Release packaging
Goal: installable by name, versioned end-to-end.
- E13.1 Release pipeline produces 4 static artifacts + checksums (dry-run in CI); `ldd`/`otool` check proves zero runtime dependencies (N-4).
- E13.2 Version stamped at build feeds the D-8 handshake (test: binary reports the stamped version).
- E13.3 Homebrew tap formula + `go install` path verified; install docs written.

### Epic 14 — Integration Verification
Goal: the whole system proven against everything above.
- E14.1 All 18 spec scenarios automated (fake agent + recorded fixtures; real-CLI smoke behind a flag). A required test that is skipped, quarantined, or flag-disabled fails this criterion.
- E14.2 GG-2 satisfied: every invariant S1–S12, L1–L3 asserted; traceability list committed.
- E14.3 Failure injection suite: daemon kill -9 during spawn/write/attach; disk full; wedged subscriber; lease conflicts; PID reuse; protocol version skew; old-shim × new-daemon compat matrix; daemon restart/upgrade soak = 50 consecutive kill/restart cycles with zero session loss (scenarios 10/11).
- E14.4 Perf budgets asserted against failing thresholds, not recorded: N-1 first paint p95 <100 ms @50 sessions; N-2 attach echo p95 <10 ms; N-3 spinner-churn collapse per E3.2 AND idle CPU (daemon+shims, ≥60 s idle) below a stated near-zero threshold with no busy-poll.
- E14.5 Fuzz suites (frame codec, VT feed) running in CI.
- E14.6 Contracts evidence manifest: for every contracts-under-test row, the manifest names the covering tests AND demonstrates they satisfy that row's declared LIGHT/MEDIUM/FULL scope; gaps fail the epic.
- E14.7 GG-3 EARS traceability matrix committed.
- E14.8 End-to-end L1 composite: signal → engine → fan-out → TUI render ≤1 s under load (contracts row 14).

## Orchestration protocol (how epics get built)

1. Epic opens: `bd update <id> --claim`; orchestrator decomposes into right-sized tasks. Shared-package interfaces (`status`, `wire`, snapshot format) are frozen — contract tests green — before any dependent fleet starts in parallel.
2. Fleet dance per task cluster: test-designer agent(s) write failing tests → implementer agent(s) make them pass → independent reviewer agent(s) check against this document AND re-derive coverage: the reviewer confirms every EARS id on the epic's build-plan `EARS:` line maps to a criterion and a test (a missing criterion is a finding, not out-of-scope). Loop until reviewers approve.
3. **Stop, don't push through**: a task cluster surviving 2+ review-fix cycles without convergence, or any critical security finding, stops the loop and escalates to the orchestrator — and to Nathan if the orchestrator cannot resolve it (build-plan Implementation Guidelines).
4. Orchestrator integrates (never delegated), runs GG-4 gates, walks the epic's exit criteria one by one, and records the walk (criterion → test/artifact) in a per-epic evidence file under `docs/verification/`.
5. Final epic audit by a reviewer that saw none of the implementation. For security/correctness-critical epics (1, 4, 5, 6, 9, 10, 11) this audit includes at least one cross-model reviewer (codex), not only Claude-family agents; fixes dispatched if needed.
6. If an epic legitimately discovers the spec or plan is wrong, the change goes through an ADR + an amendment to this document — never silent criterion drift.
7. `bd close <id>`, commit, push. Parallelizable epics may run as concurrent fleets when dependency edges allow, each in its own worktree or on disjoint packages.
8. After Epic 14: full audit-committee tour against this document; iterate on findings. After 3 tours without committee agreement, escalate to Nathan rather than looping.
