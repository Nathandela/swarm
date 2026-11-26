# Local differential PTY harness

Use Python 3.10 or later from a native macOS or Linux host. macOS runs each arm under `sandbox-exec`. Linux requires `/usr/bin/bwrap` with permission to create a network namespace, private devices, and a limited mount namespace. A host without process observation or a working guard returns an incomplete result.

```sh
scripts/verification/parity/swarm-parity run \
  --reference /absolute/path/to/go-reference/swarm \
  --candidate /absolute/path/to/candidate/swarm \
  --scenario terminal.fixture.attach --seed 194 --output /tmp/parity-run.json

scripts/verification/parity/swarm-parity replay --bundle /tmp/parity-run.json \
  --output /tmp/parity-replay.json
```

An omitted `--output` creates a private report directory and prints the receipt path. A replay repeats the seed and control on fresh arms. Use `--reference` and `--candidate` with replay to relocate identical builds. The harness verifies the staged executable hashes and requires matching fixture and driver hashes. Pass the same Go executable in both arms for calibration.

The fixture takes the place of Claude on the arm's private `PATH`. Each arm starts its own daemon and spawns one session using the public CLI, confirms the spawn receipt against the roster, then enters the board on a real controlling PTY. It attaches using Enter and verifies fixture output, exact worker input, live resize, alternate-screen restoration on Ctrl+Q, and the terminal mode after Escape. A kernel-observed process identity checks the same worker survived detach. The driver terminates the worker through the public CLI. Each supervisor verifies owned processes and cleans up remaining local sockets.

A receipt contains the input timeline, measured observations, first divergence, artifact hashes, environment allowlist, and cleanup result. The command resolves the launch ID within each arm and records later references as `$SESSION`. It records only selected board landmarks and the fixture echo bytes through its explicit end marker. It retains summaries of CLI streams where the content is immaterial to this scenario. Other terminal bytes and runtime files stay in private scratch. The harness removes scratch after verified cleanup and reports its location if owned processes survive.

```sh
scripts/verification/parity/test_parity.py
scripts/verification/parity/test_parity.py /absolute/path/to/go-reference/swarm
```

The second command calibrates Go/Go and checks three controls. The fixture adds one carriage return to the live output with `--negative-control echo`. The other controls hang or SIGKILL the arm driver while the real daemon, shim, and fixture are alive. Those controls deliberately produce failed receipts. The supervisor is the public command, independent of the per-arm drivers. Cleanup depends on that supervisor surviving. The first scenario covers attach and detach. Board launch, restart, resolved cells, screenshot comparison, and platform qualification are separate gates.

The board restart witness exercises the actual new-session form with the selected supervisor, then kills that daemon and verifies that the board reconnects to the same worker and preserved screen:

```sh
scripts/verification/parity/test_board.py \
  --supervisor /absolute/path/to/supervisor/swarm \
  --board /absolute/path/to/rust/swarm
```

`test_version_board.py --mode skew` runs two Rust builds with
`SWARM_BUILD_VERSION=0.29.23-rc.10` for the supervisor and `0.29.23-rc.2` for
the board. It verifies the newer-daemon notice, unchanged daemon and worker
PID/birth, and live fixture input after attach.

Use the pinned Go supervisor and then the Rust candidate supervisor. The witness gives Linux a single private namespace for the entire arm so killing the daemon alone cannot end the shim's namespace.

Pass `--cycle` to drive two running sessions through Ctrl+N, Kitty Ctrl+P, legacy Ctrl+P, detach and reattach. The driver checks each unique echo through the public `peek` command, the complete worker input log and the restored terminal settings. Both Go/Go and Rust/Rust passed this exercise on macOS arm64 and Linux x86_64.

For an independent cell-grid observer, pass `--tmux /absolute/path/to/tmux --grid-evidence /path/to/new.json`. This lane starts a fixture through the public CLI so the same scenario works with either board. Tmux drives the actual PTY and captures every fixture row before and after daemon death. The receipt preserves tmux's raw ANSI serialization and an expanded 80×23 grid with cell text, widths, continuation cells, attributes and resolved colors. It observes the cursor and modes through tmux, and verifies bracketed paste independently from the fixture's literal input log. The reserved Swarm chrome row is outside this fixture grid.

The pinned Go oracle preserves the live acute accent in `é` but loses it in the pre-restart shim projection and on reattach. Its cell difference remains in the receipt and the continuity check exits nonzero. The Rust snapshot preserves the combining cluster; compare its cells and modes with the Go live projection. Tmux capture does not expose every terminal state, including focus and alternate-scroll reporting.

The owner-approved reattach behavior retains U+0301 exactly. Run `python3 test_board.py --compare-grids GO_CAPTURE.json RUST_CAPTURE.json` to check all fixture cells and modes, including the one known difference in the pinned Go reattach grid.

To capture the launch form's directory and agent focus in a real tmux PTY, add
`--form-grid` to the tmux witness. Run it for the Go reference and a frozen Rust
build. The receipts retain styled cells and raw terminal captures at 80×24, 40×8
and 24×4. The third case verifies that Enter in the unreadable form leaves the
roster empty.

The `--automations` witness uses a Rust supervisor and board. It verifies a
read-only empty catalog, seeds a far-future fixture through `schedule create`,
and reads its list, detail and empty run history through both the CLI and the
board. It creates a paused draft from the board, edits its revision, resumes
it, records one manual run, opens that run's detail, pauses and archives it.
The selected fixture provider has no scheduled route, so the run records a
refusal without starting a worker. After shutdown, the board reports the
disconnection while the CLI can still read the local catalog offline.
The same witness discards a real pause reply, resumes the job from the CLI,
and retries the stale pause. It also discards a manual-run reply, restarts the
daemon and board, checks that `r` recovers the same occurrence, and verifies
that only explicit `R` creates another occurrence.

For exact comparisons, pass the same fresh `--form-root /tmp/pb-form-example`
for the two sequential `--form-grid` commands. The harness creates and removes
this private root separately per arm. It holds the fixture's version reply
until the cold directory frame is recorded, then waits for the selected
provider before recording the settled agent frame. Compare the two receipts
with `test_board.py --compare-forms GO.json RUST.json`. The comparator requires
the same workspace path and terminal engine, and reports cell differences
without substitutions.

The `--board-grid` mode populates an isolated roster through public launches,
sends synthetic Claude hook events, and tags a row. It captures the ordinary
board, fold/expand, direction keys, attention jump, wheel reveal, live resize,
More and Options at 120×24, 80×24, 40×8 and 24×4. Use the same
`--form-root` for the sequential arms and compare the receipts with
`--compare-rosters GO.json RUST.json`. It reports unmasked cell and terminal
mode differences, including animation, elapsed time and hidden cursor
positions. The capture and action witness can pass while the comparator still
reports rendering differences.

For exact More-menu comparisons, pass `--reference-board "$PINNED_GO_SWARM"`
with `--tmux` and `--grid-evidence`. The reference and candidate use the same
live owner, fixture identities, and working directory. It compares every
cell and captured terminal mode on initial display, pointer Up/Down, an
out-of-target click, and a resize that keeps the selected action. The raw
paired receipt keeps the actual IDs, fills, and cursor state.

The `--handoff-board` witness drives the supervised form against the exact
captured idle source, then drives the hands-off form from the same source's
permission state. With the Go owner it writes one synthetic native transcript,
verifies the successor points at it, and leaves the source running. An owner
without the hands-off capability must refuse visibly and keep the roster at
one session.

Pass `--handoff-grid` with `--reference-board`, `--tmux` and `--grid-evidence`
to compare the live handoff forms against the same owner and source. The
receipt includes styled cells and terminal modes for target selection, model
suggestions, both methods, modal keyboard handling and narrow transcript
consent. It compares every cell and records hidden paint cursor coordinates
separately.

## M11 real-path performance

Run matching optimized native executables on the same idle host. Use the
full candidate SHA. On Linux use a private executable RAM directory if
disk space is limited. The driver observes actual kernel process births
and reaps each arm's processes.

```sh
python3 -B scripts/verification/m11_performance.py \
  --reference "$PINNED_GO_SWARM" --candidate "$RUST_SWARM" \
  --reference-source "$GO_SOURCE_SHA" --candidate-source "$SOURCE_SHA" \
  --out "$FRESH_EVIDENCE_JSON" --quiet-host
```

The driver alternates 25 cold dashboard paints per arm at 120×200 with
50 real sessions and retains all samples. Timing begins before launching
the dashboard process and ends when all 50 distinct labels reach the
PTY. The Go in-process first-paint gate starts after protocol hello and has
a narrower scope. The echo path times one key and its canonical newline
over the owner protocol and real PTY with 64 warmups and 1,000 samples.
Aggregate idle CPU uses cumulative kernel times for one daemon and
three shims over 60 seconds. The output includes p50/p95/p99 and tails,
per-role RSS and descriptors around 100 attach cycles, binary/source
identities, host load and cleanup. Record actual host activity with the
evidence. Omit `--quiet-host` for a descriptive run during other work.
`--smoke` uses fewer trials and never produces a gate pass.
