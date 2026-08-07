# RED evidence — shared submit-boundary framing package (agents-tracker-abyz)

ADR-010 Amendment 1 A2 / A5 Phase 0b: extract the frozen r3p discipline (maximal-run
framing from `internal/phonecore/coalesce.go`'s `frameLen`/`isSubmit`, plus the
150ms pre-submit gap and `isSubmitOnly` from `internal/remotegw/lease.go`) into a
new leaf package `internal/submitframe`, so the coming daemon-side `send_input`
writer depends on one copy of the rule instead of duplicating it.

Date: 2026-08-07

`go test ./internal/submitframe/...`
```
# github.com/Nathandela/swarm/internal/submitframe [github.com/Nathandela/swarm/internal/submitframe.test]
internal/submitframe/submitframe_test.go:59:5: undefined: Gap
internal/submitframe/submitframe_test.go:60:90: undefined: Gap
internal/submitframe/submitframe_test.go:89:14: undefined: IsSubmit
internal/submitframe/submitframe_test.go:123:14: undefined: IsSubmitOnly
internal/submitframe/submitframe_test.go:173:14: undefined: FrameLen
internal/submitframe/submitframe_test.go:204:8: undefined: FrameLen
internal/submitframe/submitframe_test.go:227:13: undefined: IsSubmit
internal/submitframe/submitframe_test.go:229:7: undefined: IsSubmit
internal/submitframe/submitframe_test.go:255:8: undefined: FrameLen
internal/submitframe/submitframe_test.go:257:17: undefined: IsSubmit
internal/submitframe/submitframe_test.go:257:17: too many errors
FAIL	github.com/Nathandela/swarm/internal/submitframe [build failed]
FAIL
```

Compile-fail RED, for the right reason: `Gap`, `IsSubmit`, `IsSubmitOnly`, and
`FrameLen` do not exist yet (no `submitframe.go` in the package). No syntax error
in the test file itself (`gofmt -l` on it is clean). The original tests this
extraction ports vectors from —
`internal/phonecore/r3p_submit_boundary_test.go` and
`internal/remotegw/lease.go`'s `isSubmitOnly`/`submitGap` — are untouched.

## What the tests pin

`internal/submitframe/submitframe_test.go`:

- `TestGap_Is150Milliseconds` — `Gap == 150*time.Millisecond` (spike-SA finding #1,
  ported from `remotegw.submitGap`).
- `TestIsSubmit` — CR and LF are submit bytes; letters, digits, space, tab, ESC,
  NUL, and a high-bit byte are not (ported from `phonecore.isSubmit`).
- `TestIsSubmitOnly` — nil/empty is not submit-only; a pure CR/LF run is; any
  admixture of ordinary bytes (leading, trailing, or interior) is not (ported from
  `remotegw.isSubmitOnly`).
- `TestFrameLen_MaximalRun` — table of vectors ported from
  `r3p_submit_boundary_test.go`'s frame-boundary assertions: text stops dead
  before a trailing CR (`"git status\r"` -> 10); a lone submit byte after that
  text is consumed is its own run; a pure CR run and a mixed CR/LF run are each
  one homogeneous run; single-byte runs of either class; the run-length cap
  applies to both a text run and a submit run, and a run landing exactly at the
  cap is not truncated short of it.
- `TestFrameLen_WalkConsumesWholeBufferInMaximalRuns` — drives `FrameLen` the way
  a real caller does (phonecore's own `drain` loop, and the daemon-side
  `send_input` writer ADR-010 A2 describes): repeatedly slice off `FrameLen`'s
  run until the buffer is empty over `"ab\r\ncd\r\r\nefgh"`, asserting the exact
  run sequence, that every run is internally homogeneous, that no run ever comes
  back zero-length (which would spin the caller forever), and that reassembling
  the runs reproduces the original buffer byte for byte. This is the
  "empty-adjacent" case: a run ending exactly at the buffer's end must not
  provoke a further `FrameLen` call on an empty remainder.
- `TestFrameLen_RunsAgreeWithIsSubmitOnly` — every run `FrameLen` produces over
  the same mixed buffer is classified consistently by `IsSubmitOnly` (a run
  `FrameLen` calls "submit" is one `IsSubmitOnly` calls true, and vice versa) —
  the property the daemon-side writer's gap decision (ADR-010 A2) depends on.

## GREEN-state note (added post-implementation, 2026-08-07)

The RED-time claim above that remotegw's `isSubmitOnly`/`submitGap` were untouched was
true when written. In GREEN both call sites were switched to the shared package:
phonecore's `frameLen` now wraps `submitframe.FrameLen`, and remotegw's `isSubmitOnly`/
`submitGap` were deleted in favor of `submitframe.IsSubmitOnly`/`submitframe.Gap`.
`internal/phonecore/r3p_submit_boundary_test.go` and `internal/remotegw/r3p_submit_gap_test.go`
remain unmodified (comment-only reference updates aside) and pin the behavior end-to-end.
