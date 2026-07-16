# Epic 3 — Evidence

**Epic**: Transcript capture (`agents-tracker-kw9`)
**Commits**: 8a0ef53 (implementation), + review-fix commit (this one).

## TDD evidence (GG-5)

Designer wrote the 13-test failing suite first; red run (missing-implementation compile failure: `undefined: newSink/Config/New/...`) committed at [epic-03-red/transcript-red.txt](epic-03-red/transcript-red.txt). Review fixes followed the same discipline (hang reproduced red at 2 s timeout before the fix).

## Criterion walk (E3.1 – E3.5)

| Criterion | Evidence |
|---|---|
| E3.1 cap + rotation at boundaries | 4 tests: exact MaxBytes, MaxBytes+1, MaxFiles cap, path.N naming; a single Write is never split across generations |
| E3.2 spinner collapse | K=100 `\r`-frame fixture collapses to ≤C writes with the final frame byte-identical; ESC[H variant; embedded-`\n` negative case (a non-repaint frame can never be collapsed away — held frame is always a repaint frame by construction) |
| E3.3 0600 perms | current + rotated files asserted under forced umask 0 |
| E3.4 crash tolerance | readable-prefix after unclean stop (Flush is the deterministic sync point) |
| E3.5 disk-full / never-block | failing-sink and wedged-sink tests: Write always returns (len, nil), tail dropped past bufCap, loss counted via Dropped() |
| S9 (write half) | Write does in-memory work only; all sink calls live on one background drain goroutine; concurrency test with slow sink, `-race -count=3` stable |

Post-review hardening: New() rejects non-positive MaxBytes/MaxFiles (an unset Config can never silently produce an uncapped transcript — R-1); Flush() after Close() returns an error instead of hanging.

## Review outcome (protocol step 5)

**Opus (independent, saw no implementation): APPROVE** — S9 traced lock-by-lock; rotation, collapse, perms, crash, EARS re-derivation (S-5, R-1, N-3) all confirmed covered. 7 findings: F4 + F2 fixed (above); F1/F6 recorded as a **binding usage contract on the Epic 4 bead** (shim exit: Flush before Close, Close under timeout); F3/F5 accepted (safe degradation paths, documented); F7 resolved by this file.

## Accepted implementer judgment calls

1. Dropped() counts capacity/sink loss only, not spinner-collapsed bytes (collapse is a feature, not loss) — reviewer concurred.
2. Close() is bounded best-effort (merges backlog into one final write); Flush() is the deterministic barrier — reviewer concurred, with the Epic 4 carry-forward above.

## Quality gates (GG-4)

gofmt clean · build · vet · `go test ./internal/transcript/ -race -count=3` — 16 tests green. (Whole-module vet/test at this commit excludes internal/vt, which is mid-flight Epic 2 red by design.)
