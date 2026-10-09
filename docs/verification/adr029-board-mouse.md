# ADR-029 — mouse selection on the session board: evidence

Decision: [ADR-029](../adr/ADR-029-board-mouse-selection.md). Tests:
`internal/tui/board_mouse_test.go` (`TestMouse_*`).

D3 was revised before merge, from a timed double-click to "a click on the selected row
opens it". The first round below is the original RED/GREEN; the D3 revision has its own
section after it.

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

## D3 revision: a click on the selected row opens it

Why: from Termius on iOS, a probe that requested each tracking mode in turn (1000, 1002,
1003 with SGR, and 1000 legacy) recorded a tap as a press and a release with 0 ms between
them, and every double-tap as a `tab` key with no position. A timed double-click can never
fire there. The owner chose the clock-free rule.

RED, commit `64c6bed8` (tests and the ADR revision, against the double-click code):

| Test | RED result |
|---|---|
| `ClickOnTheSelectedRowAttachesOnRelease` | FAIL: `click on the selected running row: runner called 0 times, want 1` |
| `ClickOnARowSelectedByKeyboardOpensIt` | FAIL: `runner got [], want one attach of endpoint/w1` |
| `ClickOnTheSelectedEndedRowBannersLikeEnter` | FAIL: no "session has ended" banner |
| `SecondClickOnARowOpensIt` | green on arrival: the old code also opens two quick clicks on one row (inside its 500 ms window) |
| `ReleasedOnAnotherRowCancels`, `StrayReleaseOpensNothing` | green on arrival (guards) |
| every other `TestMouse_*` | unchanged, green |

GREEN: the press on the selected row arms the open and its release performs it; the
double-click window and the `rootModel.now` clock seam are gone.
`go test -race -count=1 ./internal/tui/` ok, `go build ./...` and `golangci-lint run ./...`
clean. The live smoke below was re-run with a single Termius-shaped tap (press and release
in one write) on the selected row: all checks PASS, tracking off at byte 471 of the
response and the agent's screen at byte 1004.

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

## Pre-merge regression review, 2026-10-09

Four added tests reproduced failures against `9cab97e9`: an armed left click opened
on a right/middle release, a click survived opening and cancelling a form/confirm,
long section headings wrapped, and wall-clock banner expiry shifted click targets
before its expiry message was handled. Legacy releases without a button identity
were separately verified to remain supported.

After the fixes, all 22 `TestMouse_*` tests pass with Go 1.25.0. Section headings now
use the existing display-cell clamp, interrupted gestures are cancelled, and banner
expiry changes the layout in `Update`, with stale expiry messages preserving newer
banners. No dependencies were added.

The required container scan also found Go standard-library vulnerabilities published
on October 8: [GO-2026-6609](https://pkg.go.dev/vuln/GO-2026-6609) and
[GO-2026-6607](https://pkg.go.dev/vuln/GO-2026-6607). The push-gateway builder and both
the snapshot and publish release toolchains now pin the patched Go 1.26.9. The module
floor and its Go 1.25.0 lint compatibility check remain supported.
