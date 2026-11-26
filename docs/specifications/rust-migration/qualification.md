# Differential qualification and debugging

A release claim is a row with an observed result on each required target. A passing Rust unit suite cannot establish that a user can find a worker in a busy board, attach to its existing conversation after a restart, and receive exactly one answer in Discord. Qualify that journey at the installed public entry points, then use narrower tests to explain any mismatch. Apply this procedure locally on operator-controlled machines. This chapter specifies future gates.

## Build the oracle before the candidate

1. Record repository SHAs for [both references](README.md), clean status, toolchain, dependency locks, binaries and helper hashes. Build each from a separate checkout. Tag each scenario with its oracle. Record native CLI version, model/admission mode, terminal emulator and OS build. Save an offline fixture from a synthetic discussion. An actual human conversation does not belong in the regression corpus.
2. Discover all retained entry points via the Go dispatcher, embedded guides, board screens/menus, local services, packaging and docs. Exercise both success and refusal. Record command flags, stdout/stderr, exit class, JSON fields including absent/null, terminal cell geometry and side effects. Audit hidden internal verb dependencies before retiring a public verb. Resolve differences between shipped binary, main source and later call branch in the scenario record.
3. Build one external harness that accepts absolute binary paths. Use sibling private roots for state, sockets, lock files, provider homes, logs, worktrees, timer units and local endpoints. Pass a minimal environment from an allowlist. Both sides use the same timezone, locale, terminal geometry, color level, resource envelope and scripted input. Per-arm install paths, Discord identities and native conversation IDs must remain distinct. Share build caches only. Ordinary fixture runs have no live account or production service access.
4. Run each input independently against the oracle and candidate. A Rust renderer spike can compare complete cells through Ratatui’s TestBackend buffer assertion. Its memory buffer cannot observe a raw-mode handoff or a blank physical screen after detach. [TestBackend documentation](https://docs.rs/ratatui/latest/ratatui/backend/struct.TestBackend.html) Use separate real foreground PTYs for the actual board and worker. Drive OS key bytes, tty resize and termination. Wait on an externally observable condition with a deadline before the next action. The driver owns wall-clock orchestration; pure transition tests can advance an injected clock. Put pause barriers at named fault points in fixture peers. Never let Rust pass by skipping a step the Go reference found hard to drive.
5. Calibrate the comparator with Go/Go on independent roots before Go/Rust. Prove sensitivity with one deliberate change to each observation channel, e.g. a missing receipt, wrong selected cell, child left alive, or extra carriage return. Require a first-divergence report pinpointing that change. Two arms sharing a broken parser can agree. Cross-check critical observations against a second observer such as the kernel, provider fixture input log, Discord receiver or actual terminal emulator.
6. Put an external reaper above each arm. Register child identity and birth time, detect descendants that detach, scope cleanup to the run root, and inspect resources after normal exit, panic, timeout and driver SIGKILL. A watchdog outside the driver must handle the last case. Assert no remaining owned sessions, sockets, file descriptors held by a surviving fixture, or scheduled callbacks. Preserve scratch on diagnostic failure with a size cap. The [Go supervisor](https://github.com/Nathandela/swarm/blob/f26648d69086e329132ac7849954d2f29a895387/internal/testbin/supervise.go) provides the starting model.

## One executable scenario record

The following **proposed** record illustrates the minimum information. Resolve referred actions to public inputs or a named OS operation. Each implementation lane contributes rows to one catalog. The harness should reject unknown fields, duplicate IDs, missing oracles, empty assertions and unregistered normalization rules.

```yaml
id: terminal.restart.attached-worker
oracle: go-main-f26648d6
requirements: [S1, S3, S10, L2]
targets: [macos-native, linux-native]
fixture: two-step-echo
seed: 194
geometry: [80, 24]
steps:
  - launch_from_board: {agent: fixture, name: amber}
  - await_roster: {name: amber, lifecycle: running}
  - attach_selected: {}
  - type_bytes_hex: "6f6e650d"
  - await_grid_text: "received one"
  - save_identity: [shim_pid_and_birth, worker_pid_and_birth]
  - terminate_verified_daemon: SIGKILL
  - reopen_board: {}
  - await_roster: {name: amber, lifecycle: running}
  - attach_selected: {}
  - await_grid_text: "received one"
  - type_bytes_hex: "74776f0d"
  - await_grid_text: "received two"
assert:
  - same_saved_processes_within_arm
  - ordered_worker_input_exactly_once
  - attach_snapshot_before_live_output
  - terminal_restored_after_quit
evidence: [driver_events, child_input, grid_cells, frame_order, process_tree]
```

Map ephemeral IDs by first observation and retain their correspondence throughout the run. Compare timestamps by causal order and the specified deadlines. Normalize sandbox path prefixes at known fields. Preserve field presence, provider thread continuity, generation changes, cell widths, whitespace in payloads, effect counts and the distinction between “accepted” and “completed.” Sort only sets the public contract leaves unordered. Every normalization rule names the field, justification and counterexample that must still fail.

## Observable gates

| Boundary | Required scenarios | Independent observation |
|---|---|---|
| CLI and local automation | Success, malformed input, unsupported operation, no daemon, stale handle, machine JSON, non-interactive terminal, broken pipe, autostart versus diagnostic-only verbs | Exit code, separate byte streams, schema, process tree before/after and count of actual effects |
| Board and attach | Full-size and compact launch, menus, edit/cancel, attention, active/ended rows, two sessions with similar names, kill/delete confirmation, resize during a modal, paste, wide/combining glyphs, mouse, detach/cycle, death and reconnect | Cell text/style/width, selected identity, cursor visibility/location, alternate screen, mouse/keyboard modes, child input bytes and restored shell |
| Session/storage | Simultaneous launches and attaches, stale lease, PID reuse, supervisor or shim crash, lost ACK, disk full, full subscriber, corrupted metadata, previous schema, renamed/removed checkout | Process identity, durable records, monotonic sequence, file modes, exactly one effect, retained unknown outcome or documented refusal |
| Provider operations | Pinned fixtures per provider, version floor, capability absence, typed vs raw input, history/resume ID, native request decision, interrupt under load, refresh and shared-owner survival | Provider-observed bytes/API effects, receipt and roster state from a new client, provenance of native conversation |
| Scheduled work | Wall-clock jump, DST fold/gap, missed run and concurrent manual claim, offline daemon, workspace conflict, quota fallback, uncertain prior attempt | Claim identity, job and attempt state, number of launches, workspace preservation and published completion |
| Discussion, files and calls | Duplicate/reordered inbound event; DM vs guild; concurrent worker return; stalled acknowledgement; upload/reopen; consent, withdrawal, join/rejoin, media outage; mixed text/file/voice run | Isolated receiver counts, content hash, scope, worker identity, consent expiry and listener-observed audio from actual call clients |
| Distribution and withdrawal | Each platform artifact, fresh install, binary substitution, interrupted download/staging, denied signature, upgrade while busy, downgrade after new work, old service units and removed commands | Artifact and installed binary hashes, executable paths, running instances, state/receipt continuity, absence of new remote endpoints |
| Stress and resources | Rapid attach/quit, scroll/repaint, continuous provider output, disconnected consumers, dispatcher bursts, filesystem pressure, overnight idle | Per-role RSS/CPU, FD/process counts, IO bytes, queue high-water marks, retained bytes and time to recover |

A provider's hosted response text and timing may vary. Use fixtures for byte-exact comparison. For designated live sessions compare semantic evidence such as one native conversation, one accepted operation ID, one scoped reply and valid allowed transitions. Interleave A/B attempts in independent test accounts or discussions. Designate the call listener and second participant before the trial. Record explicit consent and qualitative artifacts only within the chosen test session. Cross-provider UI availability, a native input contract and an encrypted live call each require their own evidence.

## Side-by-side visual and agent walk

Pin terminal app/version, font/fallback, render scale, palette, TERM, locale, window decorations, animation phase and cursor blink. Include macOS and Linux native terminals plus a narrow SSH PTY. Cover 80×24, wide desktop, the actual responsive breakpoints and their adjacent widths, and the small sizes from the [terminal matrix](terminal-runtime.md). Record geometry in cells and pixels. Choose fixture text that exposes wide glyph truncation, combining characters, highlighting, cursor contrast and alignment after scroll.

Capture both PTY byte streams, resolved grids and synchronized PNGs at named landmarks. A board-renderer unit snapshot can locate a widget fault; the external grid and screenshot also exercise input, terminal modes, alternate-screen handoff and width policy. Compare visible cells and styles exactly for deterministic arrangements. Use image overlays at fixed settings to locate font/color/cursor discrepancies. Mask only recorded variable regions, e.g. timestamps that cannot be frozen, with a documented reason and area. Save the original images. An animation or anti-aliasing difference must receive an explicit, reproducible disposition before accepting the gate. Capture sequential frames during resize/attach to catch flash and stale paint hidden in the settled image. The [Go repaint investigation](https://github.com/Nathandela/swarm/blob/f26648d69086e329132ac7849954d2f29a895387/docs/verification/tui-repaint-investigation.md) is an existing starting point.

Give an independent operator agent outcome-based prompts without key hints or expected screenshots. Examples include finding a worker awaiting input among similar rows, returning from attach without sending a byte, approving the intended pending request, resuming a discussion after restart, and following a Discord file to its owning checkout. Randomize arm order and labels. Record attempted keys/clicks, missteps, focus changes, screenshot landmarks, time and completion. Compare both arms against the intended target and durable effect. Replay every agent-reported difference with the deterministic driver. Keep usable evidence from an unfamiliar operator separately from scripted parity, so a familiar operator cannot conceal a discoverability regression.

## Failure localization, flakes and performance

Each failed run emits one compact, scrubbed reproduction bundle. Include the scenario/seed, SHA and hashes, exact command/environment allowlist, OS/arch, input timeline, last good landmark and first mismatch, neighboring wire events, expected/actual grid or receipt, fixture effects, annotated screenshot pair, process tree, cleanup result and artifact hashes. Emit a JSON summary with an explicit result per prerequisite and scenario. Keep unrestricted raw data ephemeral in the isolated test root. Verified scrubbed fixtures can be retained in Git; large sanitized evidence can be referenced by content hash and retention location. Logs must omit credentials, private files, BYO speech keys, unredacted native text and raw real audio.

When a mismatch occurs, first rerun the preserved seed on fresh independent roots with Go/Go calibration; retain the initial failure. Reduce to the shortest valid input/event prefix. Inject barriers around the first differing effect. Triage as reference nondeterminism, harness/oracle bug, candidate defect, documented baseline limitation or explicitly approved product change. Store a minimal regression, a fault owner and a reviewed evidence link. Re-recording the golden requires running the pinned oracle and showing a human-readable diff. Required scenarios with unexplained failures, missing environments or skipped assertions remain open. Never use a majority-vote pass.

For ordering-sensitive failures, generate seeded operation permutations against the state machine, then execute a reduced schedule with subprocess barriers. Include cancellation before commit, after commit, after send and before acknowledgement. Fragment every representative frame at each byte boundary where tractable; sample longer traces with recorded seeds. Exercise arbitrary valid and invalid terminal data with memory/size bounds. Use fault-injection on disposable local fixtures. For every resource leak or flaky test, show the old fixture failing deliberately and an independent repeat of the repair. Crates using unsafe process/terminal code require focused platform coverage, an encapsulated safety argument and tool-supported dynamic checks where applicable.

Run latency separately from sanitizers, screenshots, compiler workloads and instrumentation. Ship the existing uninstrumented real-path gates of first paint at 50 sessions (25 trials, <100 ms p95), real keystroke echo (1,000 post-warmup samples, <10 ms p95), and aggregate idle CPU (daemon plus three shims for 60 s, ≤5% of one core). Preserve raw samples and report p50/p95/p99 plus tail outliers. Measure matched release builds on the same idle host in alternating order. Distinguish first launch, warm attach and steady-state. Compare per-role peak/steady RSS, bytes written per workload, disk growth after retention, output drain under blocked subscribers and open handles before/after 100 attach cycles. Fix absolute budgets for additional measurements from M0 Go runs and measured host repeatability *before* viewing candidate results. Record excluded noise sources. Existing [Linux measurements](https://github.com/Nathandela/swarm/blob/f26648d69086e329132ac7849954d2f29a895387/docs/verification/lightweight-storage-linux.md) give context, not a fresh baseline.

For a proposed release minimum, run a 24-hour mixed local soak on each OS family with idle, busy, disconnected and resumed workers, and a complete day of schedule ticks. Track samples by process role, investigate growth slope after warmup and assert cleanup returns to baseline. Run timing-sensitive lifecycle seeds at least 100 times per OS, scrambled order, recording each run. Select a longer soak when a retained operation spans the trial. Report skipped, blocked, failed and passed separately along with the preceding run history. A hundred passes have limited power against rare failures; forced interleavings and negative controls carry distinct evidence.

### Approved combining-character correction

On 25 November 2026 the owner selected preservation of the acute accent in `é` after reattach. The strict fixture compares the full Go **live** grid with both Rust captures and checks the single known Go-restart difference at zero-based cell (4,1). It records the original Go and Rust data. Any other cell, attribute, width, color, cursor or observed mode difference fails. This exception applies to the pinned Go fixture and the Rust reattach contract.

## Local commands and release record

From a clean checkout of the pinned Go reference on a disposable local host with a working shared Go cache, the following are **existing** targeted probes. Run timing probes separately.

```sh
go test -count=1 ./internal/e2e -run '^TestE2E_ReplayProductionPath_AgyOpencode$'
go test -count=1 ./internal/tui -run '^TestFirstPaintGate_RealDaemon_FiftySessions_P95$'
go test -count=1 ./internal/e2e -run '^TestE2E_LiveShimKeystrokeEchoLatencyP95$'
go test -count=1 ./internal/daemon -run '^TestDaemonIdleCPUNearZero$'
```

These commands are **proposed interfaces** once M1 provides a local verification executable. A missing reference, missing target or unavailable credential produces a named incomplete result. The protected live lane runs only against designated test services.

```sh
cargo fmt --all -- --check
cargo clippy --locked --workspace --all-targets -- -D warnings
cargo test --locked --workspace
cargo build --locked --release
swarm-parity run --reference "$REFERENCE_BIN" --candidate "$CANDIDATE_BIN" --scenario terminal.restart.attached-worker --seed 194
swarm-parity replay --bundle "$FAILURE_BUNDLE"
```

Run retained Go gates and applicable race tests for the reference, with the exact build/test-selection flags recorded. A fresh local workspace must build from committed locks. A second engineer or agent should be able to take a saved bundle to another equipped host, reproduce the event order, and identify whether the same observation fails. The result ledger identifies operator, target, OS version, architecture, build hashes, setup mode, native client identity, actual record counts, missing cases, elapsed time, artifact hashes and independent review.

Perform full OS-dependent runtime qualification on native macOS and Linux. Run installed-archive smoke on each promised macOS arm64/x86_64 and Linux arm64/x86_64 target; explicitly record whether the host was native, emulated or translated and restrict claims accordingly. Require a real platform run wherever the implementation selects a different platform path or binary dependency. An overnight run, live provider/call check, computer-use review, performance suite and rollback rehearsal are separate entries. The release record ties them to the same candidate and matching helper hashes, with no automatic CI or GitHub runner. Requalify affected entries after any candidate change.
