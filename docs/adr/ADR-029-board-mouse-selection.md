# ADR-029: The session board takes mouse clicks, and only the board

- Status: Accepted
- Date: 2026-10-07
- Amends: nothing. V-6 (`system-spec.md`) says "no mouse required"; that stays true. Every action keeps its key, and the mouse is an additional way to reach some of them.
- Affects: `internal/tui` (`tui.go`, `general.go`)

## Context

The general board (the "main window") is driven entirely from the keyboard: `↑↓`/`jk`
move the selection and `⏎` attaches. The owner asked for clicks on it.

Two hard constraints shape how. First, while an agent is attached the terminal belongs
to the agent: `internal/attach` is a raw passthrough, and anything swarm asked the
terminal to report would arrive in the agent's input. ADR-019 is the record of what
stray mouse reports in that stream cost. Second, asking a terminal for mouse reports
takes the mouse away from the terminal itself: native click-drag text selection and
X11 middle-click paste stop working on that screen.

Bubble Tea v2 declares mouse tracking per frame on the `View` (`MouseMode`). Its
renderer turns tracking off when the program releases the terminal (`ReleaseTerminal`,
which is how the attach runner hands over), re-applies the last frame's modes on
`RestoreTerminal`, and diffs `MouseMode` between frames. So "off during an attach,
back on afterwards" is the renderer's own behaviour, provided the board declares the
mode per frame. That includes coming back after the attach's own input-mode reset on
detach (#47).

## Decision

**D1. Tracking is requested only in the board's plain navigation state.** The root
view sets `MouseModeCellMotion` (press, release and wheel; no idle motion) only when
the general board is showing with no pairing modal, no inline rename or tag edit, and
no kill/delete confirm. Every form (launch, handoff, options, accounts), the pairing
modal, the attach screen, and the board's text-entry and confirm states leave it at
`MouseModeNone`, so native selection and paste keep working wherever text is typed or
a decision is pending. A mouse event that arrives in any of those states (one already
in flight when the state opened) is dropped.

**D2. A left click selects the session row under the pointer.** The hit-test walks the
same line layout `view()` renders from, so it follows the banner, every sectioning
(status, repo, tag) and the bottom clip. A row the board clipped off the screen, the
status bar drawn where it would have been, headers, spacers and the banner select
nothing.

**D3. A double-click opens the row exactly as `⏎` does.** Two left presses on the same
row within 500 ms arm it, and the matching release opens it: an attach for a running
row, the "session has ended" banner for an ended one. It acts on the **release**, not
the press. Acting on the press would release the terminal to the agent with the
second release report still to come, and that report would be forwarded to the agent
as input, the ADR-019 leak. A release on a different row cancels. A third click starts
a new pair.

**D4. The wheel moves the selection one row and does not wrap.** The `↑↓` keys wrap
(V-3), but a wheel that wraps sends a long scroll cycling through the board.

**D5. Everything else is ignored:** right and middle buttons, and clicks on chrome.

## Consequences

### Positive

- The board can be driven by pointer: select, open, scroll.
- An attached agent never receives a report swarm asked for. Tracking is off before
  the passthrough starts and back on after it returns.
- Forms, inline edits and confirms behave exactly as before, including native text
  selection and paste.

### Negative

- On the board itself the terminal's native text selection needs the terminal's
  bypass modifier (Shift in most terminals, Option in iTerm2 and Terminal.app) for as
  long as the board is in plain navigation.
- The hit-test assumes one board line per terminal row. Every row, the header and
  the banner are clamped to the width. A section header naming a repo or tag wider
  than the terminal is not, so it wraps, and clicks below it land one row off until
  the window is wider. That wrap already breaks the board's fixed-height layout today.
- The banner's presence is decided by the wall clock at render time. A click in the
  instant between the banner's expiry and the repaint that removes it can land two
  rows off.

## Alternatives Considered

- **A click on the already-selected row attaches.** Deterministic and clock-free, but
  two unhurried clicks minutes apart would attach, and a single click would do
  different things depending on where the selection happened to be.
- **Mouse tracking on every screen.** It would take native selection and middle-click
  paste away from the launch form's text fields and the inline editors, for no action
  those screens need a pointer for.
- **`MouseModeAllMotion`.** Hover feedback would need it, and it streams a report for
  every pointer movement. Nothing here uses motion.
