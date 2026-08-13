# Recorded permission-dialog grids (Mirror M1.1)

CLI version: 2.1.231
Capture date: 2026-08-13
Captured by: `internal/smoke/permdialog_test.go` (`//go:build realcli`, SWARM_REALCLI=1)
Bead: agents-tracker-dwwv.2.1 · Evidence: `docs/verification/mirror-m1.md`

Every `*.snap.json` here is a VERBATIM `vt.Snap` -- the exact snapshot bytes the daemon's
output tap carries -- rendered by this repo's own emulator from a real `claude` session in a
PTY at 100x30. The `*.txt` beside each one is that grid's visible text, for human reading only;
the tests read the JSON.

The version is part of every filename. A newer CLI does not overwrite these: it is recorded
alongside, and `RecognizePermissionDialog` is re-driven against both. `TestPermissionDialog
Fixtures_AreVersionStamped` fails if a fixture name stops carrying the version this README
declares, so a re-record cannot quietly lose its stamp.

## Positive fixtures (the recognizer must match these)

| File | What it is | Options as rendered |
|---|---|---|
| `bash-approval-2.1.231.snap.json` | Bash tool approval, `touch /tmp/swarm-m1-one.marker` | `❯ 1. Yes` / `2. Yes, and always allow access to /tmp/ from this project` / `3. No` |
| `edit-approval-2.1.231.snap.json` | Edit tool approval, one-line diff on `note.txt` | `❯ 1. Yes` / `2. Yes, allow all edits during this session (shift+tab)` / `3. No` |

Option 1 is preselected in both. The question row differs per variant -- Bash asks
`Do you want to proceed?`, Edit asks `Do you want to make this edit to note.txt?` -- so it is
NOT what the recognizer anchors on.

## Negative fixtures (the recognizer must refuse these)

| File | What it is | Why it is here |
|---|---|---|
| `neg-composer-idle-2.1.231.snap.json` | idle composer after an answered dialog | the ordinary at-rest screen |
| `neg-working-2.1.231.snap.json` | mid-output, `✶ Blanching… (4s · ↓ 74 tokens)` | a turn in flight is not a decision point |
| `neg-trust-dialog-2.1.231.snap.json` | the folder-trust dialog | the adversarial one: a modal, numbered, `❯`-preselected dialog that is NOT a tool approval. Answering it with an approval key map would type into the wrong dialog. |

## A rendering artifact these fixtures preserve on purpose

Option 2 reads garbled in both positive fixtures (`2. Yes,mandealways allowpaccessttoytmp/ ...`).
That is not a transcription error: claude paints a row as words separated by `CSI n G` column
jumps and never clears the cells it jumps over, trusting its own model of what those cells
already hold. Where this emulator's cell content differs from that model, the stale character
shows through. The rows written CONTIGUOUSLY (`Bash command`, `Edit file`, `❯ 1. Yes`, `3. No`,
each followed by an erase-to-end-of-line) are unaffected -- which is exactly why the recognizer
anchors on those and on nothing else. The artifact is filed as its own follow-up; these
fixtures keep it because it is what the daemon really sees.
