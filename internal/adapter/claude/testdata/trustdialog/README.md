# Recorded folder-trust gate (bead swarm-1mq)

CLI version: 2.1.258
Capture date: 2026-09-02
Captured by: the real `claude --model haiku` in a fresh `git init` directory under a PTY sized
100x30, raw output rendered by this repo's own emulator (`vt.NewEmulator(100, 30)`), the process
killed before any answer so nothing was trusted. Evidence: `docs/verification/claude-trust-gate.md`.

`trust-dialog-2.1.258.snap.json` is a VERBATIM `vt.Snap` -- the exact snapshot bytes the daemon's
output tap carries. The `.txt` beside it is that grid's visible text, for human reading only.

## What changed between the two recorded versions

| Version | Fixture | Options as rendered |
|---|---|---|
| 2.1.231 | `../permdialog/neg-trust-dialog-2.1.231.snap.json` | `❯ 1. Yes, I trust this folder` / `2. No, exit` |
| 2.1.258 | `trust-dialog-2.1.258.snap.json` | `❯ No, exit` / `Yes, I trust this folder` |

2.1.258 dropped the digits and PRESELECTS "No, exit", so a bare Enter -- the accept key of
2.1.231 -- now exits the CLI with status 1. `LaunchGateKeys` reads the marker's row off the grid
and answers with a cursor move only when one is needed: Down then Enter on 2.1.258, a bare Enter
on 2.1.231. The rows the recognizer anchors on ("Accessing workspace:" under the box rule, the two
option labels, the marker) are written contiguously by the CLI and are unaffected by the
column-jump rendering artifact the permdialog README describes.

Trust is not inherited from a trusted parent directory: this grid was captured in a fresh child
of `/home/ubuntu/data`, whose own trust had been accepted long before.
