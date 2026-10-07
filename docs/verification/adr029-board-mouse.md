# ADR-029 — mouse selection on the session board: evidence

Decision: [ADR-029](../adr/ADR-029-board-mouse-selection.md). Tests:
`internal/tui/board_mouse_test.go` (18 tests, `TestMouse_*`).

## RED (failing-first, GG-5)

Commit `45ea085e` carries the tests, the ADR, and the inert clock seam (`rootModel.now`,
read by nothing yet) and no behaviour. Run against that commit
(`go test ./internal/tui/ -run TestMouse_ -count=1 -v`):

| Test | RED result |
|---|---|
| `BoardAsksForCellMotionReports` | FAIL: `board MouseMode = 0, want MouseModeCellMotion` |
| `LeftClickSelectsTheRowUnderThePointer` | FAIL: `click on the review row selected "endpoint/n1", want endpoint/r1` |
| `DoubleClickAttachesOnTheSecondRelease` | FAIL: `runner called 0 times, want 1` |
| `DoubleClickOnAnEndedRowBannersLikeEnter` | FAIL: no "session has ended" banner |
| `SlowSecondClickOnlySelects` | FAIL: `selection = "endpoint/n1", want endpoint/w1` |
| `ThirdClickStartsOver` | FAIL: `precondition: the double-click attaches once, got 0 calls` |
| `QuickClicksOnTwoRowsAreTwoSelections` | FAIL: `selection = "endpoint/n1", want ... endpoint/r1` |
| `WheelMovesTheSelectionWithoutWrapping` | FAIL: `wheel down moved to "endpoint/n1", want endpoint/w1` |
| `HitTestFollowsTheBanner` | FAIL: banner precondition (a click could not select the ended row) |
| `HitTestFollowsTagSections` | FAIL: `click on beta-row selected "endpoint/a"` |
| `OffWhereverTheBoardIsNotInPlainNavigation` | green on arrival (guard: nothing asks for the mouse yet) |
| `OffOnTheAttachScreen` | green on arrival (guard) |
| `ClicksOnChromeChangeNothing` | green on arrival (guard) |
| `DoubleClickReleasedOnAnotherRowCancels` | green on arrival (guard) |
| `OtherButtonsAreIgnored` | green on arrival (guard) |
| `ClippedRowsAreNotClickable` | green on arrival (guard) |
| `EventsDuringAConfirmAreDropped` | green on arrival (guard) |
| `EventsOnAFormAreDropped` | green on arrival (guard) |

The eight guards pin what the implementation must NOT do, so they are green before it exists
and are recorded as such rather than claimed as RED (E15.3). Every negative test applies its
mouse events through `quiet`, which fails on any returned command, so a stray attach cannot
pass unseen.

## GREEN

- `go test -race -count=1 ./internal/tui/`: ok, all 18 `TestMouse_*` plus the existing suite,
  golden files included, which pins that `view()`'s move onto the shared layout walker left
  the board's bytes unchanged.
- `go build ./...`, `go vet ./...`, `golangci-lint run ./...`: clean.
- `go test -race -count=1 ./...` on the owner's machine: 74 packages ok, including
  `internal/tui`. Three failed for environmental reasons, none in a package this change touches:
  - `internal/hookclient` and `cmd/swarm` (`TestRunHook_*`): the run was launched from inside a
    live swarm session, so `SWARM_SHIM_HOOK_SOCK` and the other hook variables were set; the
    hookclient test says so itself ("the test needs it genuinely unset"). Re-run with every
    `SWARM_*` variable unset, both are ok.
  - `mobile` (`TestPBBIND2_*`): `gobind` is not installed on this machine; CI installs the
    pinned version (`ci.yml`, "Install the pinned gomobile and gobind").

## Live smoke (real pty, real terminal bytes)

This branch's `swarm` ran in a Python pty against an isolated daemon (its own
`SWARM_DAEMON_STATE`, every inherited `SWARM_*` variable stripped) with one fake-agent session.
SGR mouse reports were written to the pty, and the TUI's raw output was checked:

| Check | Result |
|---|---|
| Board asks for reports (`CSI ?1002h`, `CSI ?1006h`) at start | PASS |
| Real double-click on the session row attached it (agent screen drawn) | PASS |
| `CSI ?1002l` emitted before the agent's screen (byte 19 vs 550 after the click) | PASS |
| Tracking not re-enabled while attached | PASS |
| Tracking back on after `ctrl+q` | PASS |
| Off on the launch form, back on at the board | PASS |
| Off when the TUI exits | PASS |
