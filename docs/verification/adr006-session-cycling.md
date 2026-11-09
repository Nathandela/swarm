# ADR-006 Amendment: session cycling keys (TDD evidence)

**Decision**: [ADR-006 Amendment: session cycling keys](../adr/ADR-006-field-test-ux-revisions.md).
**Baseline**: v0.25.1-2 (`62e85aac`). Date: 2026-11-09.

## Request

While attached, Ctrl+P and Ctrl+N should switch to the previous or next session without
returning to the board (the view Ctrl+Q leads to) first.

## RED

The regression surface, written before the implementation:

- `internal/attach/cycle_keys_test.go`:
  - with `PrevKey`/`NextKey` set, the legacy bytes 0x10 and 0x0e end the attach with
    `ReasonPrev`/`ReasonNext`, and only the bytes before the key are forwarded;
  - their Kitty CSI-u forms (`CSI 112;5u`, `CSI 110;5u`) do the same and are not forwarded;
  - a Kitty sequence split across two reads is recognized;
  - inside bracketed paste both bytes stay data;
  - Ctrl+Q still detaches;
  - without the keys configured both bytes reach the agent unchanged;
  - the reserved-row hint names the keys.
- `internal/tui/attach_cycle_test.go`:
  - the runner receives a ring of the running rows, in the order Down walks the board,
    wrapping in both directions, with ended rows left out;
  - a lone running row has no neighbour;
  - after cycling, the board selects the session last attached;
  - `attachChain` attaches the ring's answer on each key, stays on the session when there
    is no neighbour, and stops on a detach, a session end or an error.

First run, before any implementation: both packages failed to compile on the new symbols
only (`ReasonNext`, `ReasonPrev`, `Config.PrevKey`, `Config.NextKey`, `SessionRing`,
`attachChain`). The two existing test runner fakes (`epic8_test.go`, `repaint_test.go`)
gained the new `SessionRing` parameter, with no other change.

Behavioural RED: with the implementation in place but its decisions disabled (the filter
never registers the cycling keys, the ring never answers, the chain never moves and the
board never reselects), the suites fail on their own assertions:

```
--- FAIL: TestCycleKeys_LegacyBytesEndTheAttachWithTheirReason (6.00s)
--- FAIL: TestCycleKeys_KittyCSIUEndTheAttachAndAreNotForwarded (6.00s)
--- FAIL: TestAttachCycling_RingFollowsTheBoardOrderOfRunningRows (0.00s)
    attach_cycle_test.go:72: ring(endpoint/b, +1) = "", false; want "endpoint/c"
    attach_cycle_test.go:76: ring(endpoint/b, -1) = "", false; want "endpoint/a"
--- FAIL: TestAttachCycling_BoardSelectsTheSessionLastAttached (0.00s)
    attach_cycle_test.go:115: board selection after cycling = "endpoint/b", want the session last attached "endpoint/a" (started on "endpoint/b")
--- FAIL: TestAttachChain_FollowsTheKeysThroughTheRing (0.00s)
    attach_cycle_test.go:148: visited [a a a a a a], want [a b c b a c]
```

The paste, off-by-default, detach-key and lone-row cases passed in that run, as they
should: they pin behaviour the change must keep.

## GREEN

With the decisions restored, the new tests and the existing attach and TUI suites pass.
Full-suite results are recorded in the pull request.
