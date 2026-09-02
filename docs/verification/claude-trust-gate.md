# The folder-trust launch gate (ADR-025, bead swarm-1mq)

Live-verified on tf-wks-nathan, 2026-09-02: claude 2.1.258, swarm 0.13.18 (investigation) and
the `feat/claude-trust-gate` build (fix). Every screen quoted below is a real grid; nothing is
hand-written.

## The defect, as investigated before any code changed

A new claude session in a repository the CLI had never been trusted in died at the folder-trust
dialog. Measured through the production daemon:

| Probe | Action on the dialog | Outcome |
|---|---|---|
| fresh git repo, positional prompt | Enter | claude exit 1; session `completed`; no conversation id; `hook.seq` empty; nothing trusted |
| fresh git repo, positional prompt | Down, Enter | trust written; prompt submitted; reply received; `hook.seq` 2 (UserPromptSubmit, Stop) |

Three facts explain it:

1. **claude 2.1.258 inverted the dialog.** 2.1.231 rendered `❯ 1. Yes, I trust this folder` /
   `2. No, exit` (fixture `internal/adapter/claude/testdata/permdialog/neg-trust-dialog-2.1.231`).
   2.1.258 renders `❯ No, exit` / `Yes, I trust this folder`, unnumbered, "No" preselected
   (fixture `internal/adapter/claude/testdata/trustdialog/trust-dialog-2.1.258`). A control run
   in a plain PTY outside swarm showed the same default, so it is the CLI's, not the harness's.
2. **Trust is not inherited.** `/home/ubuntu/data` was trusted; a fresh child repository still
   asked. Six repositories in the owner's Claude config carried a project entry with
   `hasTrustDialogAccepted: false`, the trace of launches that died there.
3. **The engine read the dialog as idle.** `evaluateClaudeGrid` matched `❯ No, exit` as a
   composer row and knew no hint for `Enter to confirm · Esc to cancel`, so the board said
   `ready_for_review` and `swarm watch --until needs_input` never fired. An agent-driven spawn
   into a new repository therefore stalled silently with its prompt undelivered.

No native skip exists: the 2.1.258 binary's own text for its remote-control mode says to "run
`claude` there once to accept the trust dialog" (`strings` over the binary; `--help`).

## The fix

- `adapter.LaunchGateAnswerer`: an optional extension returning the keys that accept a CLI's
  own startup gate, read off the grid (`internal/adapter/launchgate.go`).
- `claudeAdapter.LaunchGateKeys` (`internal/adapter/claude/trustdialog.go`): recognizes the
  dialog by its title under the box rule, both option labels, and exactly one selection marker;
  answers `\r` when the marker is on Yes, `CSI B \r` (Down, Enter) when Yes is the row below the
  marker, `CSI A \r` when above. Every other recorded grid, a grid with no marker, and a grid
  missing the Yes row are refusals.
- `Daemon.answerLaunchGate` (`internal/skeleton/launchgate.go`), called from `sampleGrid` after
  the engine's `OnOutput`: when the sampled grid shows the gate, join the session's shared tap
  readWrite, re-read the keys off THAT subscription's seeded grid (inject.go's rule), write
  them once through `ControlKeys`, record the session as answered. Bounded to running sessions
  by `endSession`.
- One more claude dialog hint, `Enter to confirm · Esc to cancel`, so an unanswered gate reads
  as `needs_input`.
- The realcli smoke harness (`internal/smoke/permdialog_test.go`) answers the gate through the
  adapter instead of typing `1`, which 2.1.258 ignores.

## Failing first (GG-5)

`docs/verification/claude-trust-gate-red/`:

- `adapter-claude-red.txt`: build failed on `LaunchGateKeys` and `adapter.AsLaunchGateAnswerer`
  being undefined.
- `engine-red.txt`: both recorded dialogs read `(idle, none, conclusive=true)`; want
  `(idle, permission, true)`.
- `skeleton-red.txt`: `sk.launchGateAnswered undefined`.

## Green

- `go test ./internal/adapter/ ./internal/adapter/claude/ ./internal/engine/`: ok.
- `go test -count=1 ./internal/skeleton/ -run 'LaunchGate|ApproveInjection'`: 13 PASS against
  live PTYs (the three new tests and the ten M1.2 injection tests whose helper gained a
  fixture-directory field).
- `go test -race` on the three packages' relevant tests: ok.
- `go build ./...`, `go vet ./...`, `go vet -tags realcli ./internal/smoke/`: ok.
- `golangci-lint run ./...`: 0 issues.
- `go test -count=1 ./...`: every package ok except `cmd/swarm` and `internal/hookclient`, whose
  hook tests refuse an environment carrying a live session's `SWARM_*` variables (this run was
  driven from inside a swarm session). Re-run with those six variables unset: both ok.

Linked-worktree note: `go build` in the worktree needs `-buildvcs=false` (the known VCS-stamping
walk past the worktree's `.git` file), and the skeleton harness's nested `go build` inherits it
through `GOFLAGS`; without it those tests skip as "cannot build integration binaries".

## Live use cases, against an isolated daemon built from the branch

State dir `/tmp/swtg` (a scratchpad-length path exceeds the 108-byte Unix socket limit and the
shim fails to bind, which is unrelated to this change), four claude sessions, `haiku`.

| Use case | Launch | Gate answered by daemon | Result |
|---|---|---|---|
| U1 new repo, positional prompt (`swarm spawn`) | 80x24 | yes, `09:19:40` | prompt submitted, `PONG`, `hook.seq` 2, `hasTrustDialogAccepted: true` |
| U2 new repo, no prompt (the TUI's LaunchReq shape) | 100x30 | yes, `09:19:40` | idle composer; a typed turn then ran, `hook.seq` 2 |
| U3 second session in U1's repo, now trusted | 80x24 | no gate seen, nothing typed | prompt submitted, `PONG` |
| U4 second session in U2's repo | 80x24 | yes, `09:23` | prompt submitted, `PONG`, flag now `true` |

The daemon's stderr for the run carries exactly three lines, one per answered gate
(`skeleton: session <id>: answered the claude launch gate`), and none for U3.

U2's flag read `false` after its gate was accepted and even after a full turn, while U1's read
`true`. U1 and U2 were answered in the same second, and each claude process rewrites the whole
of `~/.claude.json` from its own in-memory copy, so the later writer restored U2's startup-time
entry. That is the CLI's own config race, not a harness defect; the consequence is that the
next session in that directory sees the gate once more, and U4 shows the daemon answers it
again and the flag then persists. The board never showed U2 as needing input during the
lost-flag window, because the daemon had already answered.

## Residue, stated

- The recognizer does not compare the dialog's displayed path with the session's cwd (ADR-025
  names why); a nested interactive `claude` for another directory inside the session's own PTY
  would be answered too.
- `swarm ls --json` reports `exit_code: null` for a session whose `meta.json` carries `1`
  (observed on the investigation probe; not touched here).
- The completed `trust-probe` rows from the investigation remain in the owner's production
  roster; the CLI has no delete verb.
