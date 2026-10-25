# Recorded folder-trust gate (bead swarm-1mq)

CLI version: 2.1.258
Capture date: 2026-09-02
Captured by: the real `claude --model haiku` in a fresh `git init` directory under a PTY sized
100x30, raw output rendered by this repo's own emulator (`vt.NewEmulator(100, 30)`), the process
killed before any answer so nothing was trusted. Evidence: `docs/verification/claude-trust-gate.md`.

CLI version: 2.1.310
Capture date: 2026-10-25
Captured from the native CLI in an isolated synthetic startup diagnostic at 150x44, then replayed
offline with `vt.NewEmulator(150, 44)`, matching the diagnostic PTY's configured window size. No terminal
input, model request, or resume was issued. Native macOS pin:
`ffb64331bc0ee1b69efb7c2c428bd3ab516bc31b75bf60886b356c97c1c25ffd`.
The local capture SHA-256 is `8596afc7d5ee695161c03019956c5c4ed701259dfb5419bd44a9a060cb8bee0a`.
The synthetic capture still renders `Accessing workspace:` directly under the box rule. The
`Quick safety check:` question is additional dialog text; it does not replace that title. The
options remain exact and adjacent, and the selected option is `No, exit`.

`trust-dialog-2.1.258.snap.json` is a VERBATIM `vt.Snap` -- the exact snapshot bytes the daemon's
output tap carries. The `.txt` beside it is that grid's visible text, for human reading only.
`trust-dialog-2.1.310.snap.json` is generated only from the synthetic capture above at its recorded
150x44 PTY size; its `.txt` is the same grid's visible text for human reading.

## Recorded versions

| Version | Fixture | Options as rendered |
|---|---|---|
| 2.1.231 | `../permdialog/neg-trust-dialog-2.1.231.snap.json` | `❯ 1. Yes, I trust this folder` / `2. No, exit` |
| 2.1.258 | `trust-dialog-2.1.258.snap.json` | `❯ No, exit` / `Yes, I trust this folder` |
| 2.1.310 | `trust-dialog-2.1.310.snap.json` | `❯ No, exit` / `Yes, I trust this folder` |

2.1.258 dropped the digits and PRESELECTS "No, exit", so a bare Enter -- the accept key of
2.1.231 -- now exits the CLI with status 1. `LaunchGateKeys` reads the marker's row off the grid
and answers with a cursor move only when one is needed: Down then Enter on 2.1.258, a bare Enter
on 2.1.231. Version 2.1.310 adds the `Quick safety check:` question while retaining
`Accessing workspace:` under the box rule, so the existing recognizer already recognizes the
captured grid and chooses Down then Enter. No recognizer change is needed for this capture.

The 2.1.310 fixture must use the diagnostic's 150x44 PTY size. An earlier 80x24 rendering was
invalid: it distorted the column-jump output and hid the existing box title. It is not evidence
that the recognizer refused the actual captured screen.

Trust is not inherited from a trusted parent directory: the 2.1.258 grid was captured in a fresh
child of `/home/ubuntu/data`, whose own trust had been accepted long before.
