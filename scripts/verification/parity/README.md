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

Use the pinned Go supervisor and then the Rust candidate supervisor. The witness gives Linux a single private namespace for the entire arm so killing the daemon alone cannot end the shim's namespace.
