# Epic 2 — Evidence

**Epic**: VT emulator — grid + snapshot (`agents-tracker-onj`)
**Commits**: 9659ab7 (E2.1 spike + ADR-005), c4e20ca (wrapper), cef5c3f (review fixes).

## E2.1 risk gate (the plan's highest-risk assumption)

**PASSED.** Five candidates evaluated in a working spike; **charm x/vt** chosen and pinned (ADR-005). Deciding factor: correct double-width/grapheme cell geometry — the property snapshots and grid heuristics depend on for real CLI output (CJK, emoji, box drawing). Runner-up midterm; hinshun/vt10x rejected (dormant, no wide-char geometry). Validation fixture (scripted alt-screen + real vim PTY capture) retained at `internal/vt/spike_test.go`. Cost recorded: Go directive 1.22 → 1.24 (CI + docs aligned).

## TDD evidence (GG-5)

Designer suite (13 tests + fuzz) written first; red log at [epic-02-red/emulator-red.txt](epic-02-red/emulator-red.txt) — pure `undefined:` API symbols, verified genuine by the reviewer.

## Criterion walk (E2.2 – E2.6)

| Criterion | Evidence |
|---|---|
| E2.2 Emulator API | Feed/Resize/Snapshot wrapping x/vt; plain text, SGR attrs, cursor moves/visibility, alt-screen enter/draw/exit, resize dims — all tested |
| E2.3 snapshot fidelity (S10 content) | vim-capture fidelity vs ground truth; two-snapshot byte determinism; JSON round-trip DeepEqual |
| E2.4 hostile escapes (N-6) | Structured snapshot (never raw bytes) + printable-only sanitizer (IsPrint‖space, space-replacement preserves columns); normative fixture `testdata/hostile.raw`: OSC 52 (BEL+ST), OSC 0/2 with embedded controls, DCS/APC/PM, 8-bit C1, DEL-in-title, NBSP/RLO/ZWSP/U+2028 Trojan-source rows. Fg/Bg constrained to #rrggbb; hyperlinks not carried |
| E2.5 goldens + fuzz | scene + vim goldens with -update; FuzzFeedSplitConsistency in CI — strict whole==split on valid UTF-8 (uniseg cluster-boundary carve-out), no-panic/no-deadlock unconditional; 3 crasher-regression corpus files retained and passing |
| E2.6 versioned format | SnapshotVersion=1; DecodeSnapshot rejects unknown versions |

## Real bugs found by the process

1. **Fuzzing found a deadlock**: VT device queries (DA/DSR) block charm's unbuffered reply pipe — Feed hung forever. Fixed with a reply-drain; **Epic 4 must feed these replies back into the PTY** (recorded on the bead; wrapper grows Replies()/Close() there).
2. **Reviewer's live fuzz run falsified the original invariant**: grapheme clusters straddling Feed boundaries commit as separate cells (upstream flush-at-Write behavior). Property scoped honestly; limitation documented in ADR-005.

## Review outcome (protocol step 5)

**Opus (independent): FIX REQUIRED → APPROVE.** F1 (false fuzz invariant, would poison CI) and F2 (sanitizer weaker than the test's own contract) fixed and re-verified by the same reviewer (150s clean fuzz, corpus non-vacuous, xxd-verified fixture). F3 (finalizer lifecycle) deferred to Epic 4's explicit Close. Accepted deviation: per-cell runs, no equal-style merging (designer contract requires it; worst-case 200x60 snapshot ≈ 1.3 MB, within attach budget).

## Quality gates (GG-4)

gofmt · build · vet · `go test ./internal/vt/ -race -count=2` · whole-module race suite · 210s+150s fuzz clean · go mod tidy stable.
