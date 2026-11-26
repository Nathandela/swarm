# Darwin populated-roster parity QA

This receipt compares the retained board navigation journey at integration
`00be1bc88a21834a597bffa73d2cd0b5979175b9` with signed Go v0.29.23. The
fixture uses a private Go owner and public fixture sessions. It captures
initial, folded, expanded, Down, Up, attention-jump and End selection at
120×24, 80×24, 40×8 and 24×4, plus a synchronized wide-screen pair.

## Exact-build comparison

The Rust candidate was built from the integration source with the same reported
version as the Go reference. This removes the expected mixed-version warning
from the comparison:

```sh
TMPDIR=/private/tmp/swarm-board-qa-tmp \
CARGO_TARGET_DIR=/private/tmp/swarm-board-qa-00be1bc8-target \
SWARM_BUILD_VERSION=0.29.23 cargo build --locked -p swarm

python3 scripts/verification/parity/test_board.py \
  --static-grid --tmux "$(command -v tmux)" \
  --grid-evidence /private/tmp/swarm-rust-board-qa-00be1bc8-versionmatched-retry.json \
  --supervisor /private/tmp/swarm-go-v0.29.23.0pHHOiz1/swarm-darwin_all \
  --reference-board /private/tmp/swarm-go-v0.29.23.0pHHOiz1/swarm-darwin_all \
  --board /private/tmp/swarm-board-qa-00be1bc8-target/debug/swarm
```

The comparison passed with zero changed cells or terminal modes. The receipt
records 15 hidden cursor-coordinate differences. Cursor visibility matched,
so these are paint-cursor positions while hidden; all displayed text, color,
selection, folding, attention navigation and other captured modes matched.
Fixture cleanup reported zero survivors and removed one tmux socket.

| Artifact | SHA-256 |
|---|---|
| Go v0.29.23 executable | `180d1083123a44325ab878543124539ac0ee8863983abfb14f4a933b32ab7549` |
| Rust candidate executable | `e2aab9b7c3dce3f6d9e5754f7e9d6f4c2b063af03340fb1cba682b1d78f8a68e` |
| Passing parity receipt | `5fe1f09be2da0e9cbefd43171edfa8702e98785066a6759524a87ed6e5489203` |

The receipt is at
`/private/tmp/swarm-rust-board-qa-00be1bc8-versionmatched-retry.json`.

## Mixed-version diagnostic

The signed Rust v0.1.0 executable was also compared against the same Go
reference. Its first visible difference was the expected
`daemon 0.29.23 differs from swarm 0.1.0` notice and its `0.1.0` footer version.
At compact sizes the notice takes roster viewport space. The exact diagnostic
receipt is at `/private/tmp/swarm-rust-board-qa-ded21c92.json`.

| Artifact | SHA-256 |
|---|---|
| Signed Rust candidate from `ded21c92e384ac8560827ce11d3567da0ed190a6` | `a76e48c77b50c2c37506d15825f50f11511678d0c87d4fc34c50283e9d847158` |
| Mixed-version parity receipt | `cdf20954369f12f2fade334a8495eccf9a512703419848e2b9f761d2cc687b6e` |

## First-attempt timeout

The first same-version attempt timed out waiting for `80x24 selected:amber`
while opening the Go reference board, before the Rust candidate arm began.
`capture()` uses `wait_for()` with its five-second default. The runner's
`finally` block removed its private fixture directory, and this path does not
persist the last pane capture on timeout. As a result, the failed attempt has
no frame or process trace that distinguishes a late Go first paint from a
selection/readiness mismatch. The fixture process had exited; an isolated
30-second tmux anchor sleep was observed and exited after its timeout.

A fresh same-version retry completed the full capture and cleanup successfully.
That establishes the journey is reproducible and supports a transient startup
delay as a possibility, but does not prove that diagnosis. No harness change
was made because the original timeout's intermediate frame was not retained.
