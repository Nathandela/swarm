# TUI repaint mechanism investigation — R4.1.3 / R4.1.5 (item 4.1, agents-tracker-nzh)

Investigation-only per the plan: no changes to the SGR-nonce + `tea.ClearScreen`
hack (`internal/tui/tui.go`, `repaintMsg` handling and `View()`) or to its test.
Findings below are grounded in the actual `charm.land/bubbletea/v2` renderer
source and `charmbracelet/x/exp/teatest/v2` source in the module cache, and in
a reproduced experiment (three variants of the hack tried locally, run against
the real test suite, then reverted — no trace left in the working tree).

## Environment

- Git commit at time of investigation: `66bd339756936c21aa7ec614d4899ab1bb87c720`
  (branch worktree-perf-audit, after the R4.1.1/R4.1.2 commits)
- `go version`: go1.26.1 darwin/amd64
- `charm.land/bubbletea/v2 v2.0.2`, `charmbracelet/x/exp/teatest/v2
  v2.0.0-20260713092006-0d683c34c74b` (both read from
  `$(go env GOMODCACHE)/charm.land/bubbletea/v2@v2.0.2/cursed_renderer.go` and
  `.../github.com/charmbracelet/x/exp/teatest/v2@.../teatest.go`)

## Code under investigation

`internal/tui/tui.go`:
- `repaintInterval` doc comment and the "F2 note" (lines ~90-106)
- `Update`'s `repaintMsg`/`bannerExpireMsg` cases (~lines 269-293): bump
  `m.repaintN` and return `tea.ClearScreen` alongside `repaintTick()`
- `View()` (~lines 308-332): appends
  `strings.Repeat("\x1b[m", m.repaintN%8+1)` to the rendered content — a
  trailing run of SGR-reset sequences that changes length as `repaintN` cycles
  0-7, making `View().Content` differ byte-for-byte between ticks even when
  the visible text is unchanged

## (a) Why the unchanged-frame early-return fires despite "the elapsed column changing every second"

It doesn't, for most sessions, most seconds. `compactElapsed`
(`internal/tui/tui.go:540-554`) buckets by granularity:

```
d < 1m   -> "41s"        (changes every second)
d < 1h   -> "12m"        (changes once a minute)
d < 24h  -> "1h"         (changes once an hour)
else     -> "3d"         (changes once a day)
```

The representative fixture (`fullBoard()`, general_test.go) has sessions at
12m/3m/1h/2h — all in the minutes/hours buckets. For any such session,
`compactElapsed`'s *output string* is identical for up to 59 consecutive
one-second ticks; only the *value passed in* changes every second. So
`generalModel.view()`'s output, and therefore `rootModel.View().Content`, is
genuinely byte-identical across most ticks whenever no session is under a
minute old — which is the common case for a board that isn't freshly launched.

`charm.land/bubbletea/v2`'s `cursedRenderer.flush()`
(`cursed_renderer.go:281-284`) gates on exactly that:

```go
if !s.starting && !closing && s.lastView != nil &&
    viewEquals(s.lastView, &view) && frameArea == s.cellbuf.Bounds() {
    // No changes, nothing to do.
    return nil
}
```

`viewEquals` (`cursed_renderer.go:790-830`) starts with a plain
`a.Content != b.Content` string comparison (plus a few metadata fields:
AltScreen, cursor, mouse mode, etc., none of which this app changes on a
tick). Since `Content` is unchanged for most ticks, `viewEquals` returns
`true`, and `flush()` returns before touching the cell buffer or writing
anything — a real, empty no-op, confirmed by reading the source rather than
inferred from the comment.

The SGR nonce defeats exactly this string comparison: `\x1b[m` repeated
`repaintN%8+1` times is invisible (SGR reset paints no cell) but makes
`Content` differ every tick, so `viewEquals` returns `false` and `flush()`
proceeds past the early return.

## (b) What the "frozen test" actually pins

Proceeding past `viewEquals` is necessary but **not sufficient** — this is the
part the code comment gestures at ("AND tea.ClearScreen to force a full
redraw rather than an empty cell diff") without spelling out the mechanism.
Two independent things are happening, and the second one is why the test
needs both pieces:

**Renderer mechanics.** Past the `viewEquals` gate, `flush()` draws the new
content into `s.cellbuf` and calls `s.scr.Render(s.cellbuf.RenderBuffer)`
(`cursed_renderer.go:305,458`), which hands off to
`uv.TerminalRenderer.Render` — a *second*, cell-level diff against the
renderer's own tracked previous-frame state. Since the nonce's bytes carry no
visible glyph, the actual cell content is unchanged, so this inner diff, left
alone, would still emit nothing. `tea.ClearScreen` -> `cursedRenderer.clearScreen()`
(`cursed_renderer.go:627-634`) calls `s.scr.Erase()`, which invalidates that
tracked state so the *next* `Render` call treats every cell as dirty and
re-emits the whole frame's bytes regardless of whether they visibly changed.
Verified empirically (see below): dropping either piece alone still produces
zero bytes written to the output stream on an unchanged-text tick.

**Why the test cares at all.** `TestLiveness_EventMovesRowGroup`
(`liveness_test.go:16-49`) opens with two sequential checks before the driving
event:

```go
waitContains(t, tm, "WORKING")
waitContains(t, tm, "compiling now")
```

`waitContains` wraps `teatest.WaitFor(t, tm.Output(), ...)`. Reading
`teatest/v2`'s source: `TestModel.Output()` returns the *same* underlying
`bytes.Buffer`-backed `io.ReadWriter` every call (`teatest.go:255-257`), and
`doWaitFor`'s poll loop (`teatest.go:100-111`) does
`io.ReadAll(io.TeeReader(r, &b))` on every iteration — an `io.ReadAll` on a
`bytes.Buffer` **drains** it. So the buffer is destructively shared across
calls: the first `waitContains("WORKING")` reads and drains the entire
initial frame (which, confirmed by capturing the raw bytes locally, already
contains both "WORKING" and "compiling now" — they're on the same row). By
the time the second `waitContains("compiling now")` starts polling, those
bytes are gone; it can only succeed if the *program* writes fresh bytes
containing "compiling now" again before its 3s budget expires. Nothing else
in the app would do that (no real event has fired yet) — only the
repaintTick's forced full re-emission does.

So the test's "two pre-event waits" don't pin the elapsed column refreshing
per se; they pin *periodic full re-emission of unchanged content*, because
the test harness drains its output buffer per check and a second check on
content already seen by an earlier check has nothing left to read unless the
program re-sends it.

**Reproduced.** Locally (three edits to `tui.go`, run against
`go test -run TestLiveness_EventMovesRowGroup`, then `git checkout` to
discard — no trace left):

| Nonce | ClearScreen | Result |
|---|---|---|
| present | present | PASS (baseline) |
| present | removed | FAIL — 2nd `waitContains` times out at 3s, empty output |
| removed | present | FAIL — same |
| removed | removed | FAIL — same |

Confirms the comment's claim ("dropping either yields zero re-emission and
regresses that frozen test") is accurate, and that both pieces are
independently necessary for the current implementation. No other test in the
package failed in any of the three variants.

## (c) Candidate replacement

The mechanism as built is doing more than the test needs and more than a
real terminal session benefits from:

- `tea.ClearScreen` forces a full-screen erase + redraw on **every** tick,
  once a second, on the general view, regardless of whether anything visible
  changed. That's the *opposite* of what incremental cell-diffing
  (`uv.TerminalRenderer`) exists to avoid — in real usage (not the test
  harness), most ticks have nothing new to say (see (a)), so this sends a
  full-frame repaint over the wire every second for no visible benefit. It
  actively defeats the efficiency the renderer would otherwise give for
  free.
- The elapsed column's actual correctness requirement is only: **when text
  genuinely changes** (a display-bucket boundary crossed — "59s"->"1m",
  "12m"->"13m", etc.), the new frame must reach the terminal. That already
  happens for free: a genuine `Content` change makes `viewEquals` return
  `false` with no forcing needed. The repaint *tick* still needs to keep
  firing every second so `Update`/`View` gets re-evaluated at all on an
  otherwise-idle board (bubbletea only renders when a `Msg` is processed) —
  but that doesn't require forcing the *content* or the *cell buffer* to lie
  about having changed.

**Targeted invalidation option:** drop `m.repaintN`/the SGR nonce/`tea.ClearScreen`
from production entirely; keep `repaintTick()` re-arming every second so
`View()` gets recomputed and any *genuine* elapsed-bucket transition is
naturally caught by the renderer's own `viewEquals` diff. Risk: low for
real-terminal behavior (this is strictly less work per tick, and the visible
result — a board whose elapsed column updates within one bucket-crossing
tick — is unchanged); the only currently-known consumer of the forcing
behavior is the one test identified in (b), and that test's requirement
("two sequential substring checks on content already delivered by the first
frame") is a self-inflicted test-harness problem, not a product requirement.
The fix stays in the test: fold the two `waitContains` calls into one that
checks for both substrings against a single drained read (e.g. a
`waitContainsAll` helper, or reorder to check the union once), so it no
longer depends on the program re-sending unchanged content. That is a test
change, out of scope for this investigation-only item (R4.1.3 forbids
touching the hack **or its test** in code) — noted here as the shape of the
fix, not implemented.

Residual risk if this were implemented: real terminals occasionally need a
full redraw to self-heal after an external artifact (resize races, another
program having written to the same terminal, etc.) — `tea.ClearScreen` was
possibly also serving as a cheap "self-heal" tick for such cases in addition
to the elapsed-column motivation. That benefit, if real, would be lost by
dropping it unconditionally; a resize already forces `s.scr.Erase()`
independently (`cursed_renderer.go:612-624`, `resize()`), so this residual
risk looks small but is not zero-evidence — it would need its own check
before shipping the change.

## (d) Recommendation

**Change, not keep** — but not in this item. The mechanism is measurably
wasteful for real usage (full-frame erase+redraw every second, unconditionally,
on the general view) for a correctness requirement (elapsed column staying
current) that the renderer already satisfies on its own whenever the
displayed text actually changes. The one thing actually pinned by keeping it
is a test polling pattern that has an independent, low-risk fix (merge the
two sequential `waitContains` calls). Recommend: file the targeted fix
(production: drop the forcing, keep the tick; test: merge the two
`waitContains` calls into one) as its own follow-up item under
agents-tracker-nzh, with its own TDD pass (failing-first: assert the general
view, once painted with all-elapsed sessions >1 minute old, receives zero
renderer bytes on a repaint tick with no content change — i.e. pin the
*absence* of forced re-emission — then land the removal). Per R4.1.5, since
this ends in "no code change" for R4.1.3 itself, the nzh bead stays open with
this note attached rather than closing silently.

## R4.1.4 — gates

- `go test -count=1 ./internal/tui/...`: PASS (includes
  `TestLiveness_EventMovesRowGroup`, `TestRepaint_*`,
  `TestStyleHoistPinnedOutput`, `TestApply_*`, and the full pre-existing
  suite; not modified as part of this investigation)
- `go test -race -count=1 ./internal/tui/...`: PASS
- Firstpaint gate, run once as instructed:
  `go test -run TestFirstPaintGate ./internal/tui/ -v`
  ```
  first-paint p95 over 25 runs @ 50 real sessions: 2.669459ms (raceEnabled=false)
  ```
  Well within the N-1 100ms p95 budget (~37x headroom).
