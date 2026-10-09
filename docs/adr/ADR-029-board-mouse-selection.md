# ADR-029: The session board takes mouse clicks, and only the board

- Status: Accepted
- Date: 2026-10-07, D3 revised 2026-10-09 before merge (see D3)
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

**D3. A click on the row that is already selected opens it exactly as `⏎` does:** an
attach for a running row, the "session has ended" banner for an ended one. A left
press on the selected row arms it, and the matching release opens it. It does not
matter how the row became selected (an earlier click, the keyboard, the initial
selection) or how long ago, so "click to select, click again to open" is the gesture,
and no timing is involved. It acts on the **release**, not the press. Acting on the
press would release the terminal to the agent with the release report still to come,
and that report would be forwarded to the agent as input, the ADR-019 leak. A release
on a different row cancels, and a release with no armed press opens nothing.
Keyboard input, wheel input and entering a modal cancel the armed click. SGR releases
must identify the left button; legacy releases may omit the button identity.

The first draft of this decision was a timed double-click (two presses on one row
within 500 ms). Field testing from Termius on iOS showed it cannot work there: a tap
arrives as a press and release at the same instant, and a double-tap arrives as a Tab
key with no position, in every tracking mode (1000, 1002, 1003, SGR or legacy). The
owner chose the clock-free rule, which works for a tap and a desktop click alike.

**D4. The wheel moves the selection one row and does not wrap.** The `↑↓` keys wrap
(V-3), but a wheel that wraps sends a long scroll cycling through the board.

**D5. Everything else is ignored:** right and middle buttons, and clicks on chrome.

## Consequences

### Positive

- The board can be driven by pointer: select, open, scroll, including by tap from a
  phone terminal.
- An attached agent never receives a report swarm asked for. Tracking is off before
  the passthrough starts and back on after it returns.
- Forms, inline edits and confirms behave exactly as before, including native text
  selection and paste.

### Negative

- A single click opens whenever it lands on the selected row, including the row
  selected at start-up and the row just detached from. Opening is what the click on
  a highlighted row is for, and `ctrl+q` returns from it.

- On the board itself the terminal's native text selection needs the terminal's
  bypass modifier (Shift in most terminals, Option in iTerm2 and Terminal.app) for as
  long as the board is in plain navigation.
- Section titles are clamped to the terminal width, so long repo or tag names are
  shortened. Banner expiry changes the layout only when the update loop handles its
  expiry message, keeping the rendered and clickable rows aligned.

## Alternatives Considered

- **A timed double-click (the first draft of D3).** The desktop convention, but Termius
  on iOS never delivers one: a double-tap becomes a Tab key. A board opened from a
  phone could select but never open.
- **Tab opens the selection.** It would make the Termius double-tap open, but Tab has
  no position, so a double-tap anywhere would open whatever row was selected, and the
  key would mean something on the board alone.
- **Mouse tracking on every screen.** It would take native selection and middle-click
  paste away from the launch form's text fields and the inline editors, for no action
  those screens need a pointer for.
- **`MouseModeAllMotion`.** Hover feedback would need it, and it streams a report for
  every pointer movement. Nothing here uses motion.
