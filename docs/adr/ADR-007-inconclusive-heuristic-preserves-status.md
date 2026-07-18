# ADR-007: An inconclusive grid heuristic preserves the committed status

**Status**: Accepted
**Date**: 2026-07-18

## Context

The status engine (Epic 10) has three signal paths: typed signals (hooks/events),
the grid heuristic (`OnOutput`, a deterministic read of the emulated screen on each
output batch), and the fallback poll (`Tick`). Epic 10 deliberately chose an
**apply-unknown** rule for the heuristic (engine.go:14, heuristic_test.go): an
inconclusive screen read maps to `turn=unknown`, "never a confident guess" (T-4).
The heuristic result — including that `unknown` — was committed by `OnOutput`
whenever no fresher typed signal outranked it (within `StalenessThreshold`, 30s).

Field test 4 proved this apply-unknown rule is the root cause of two P1
status-accuracy bugs (beads agents-tracker-q65, agents-tracker-dqh), confirmed by
replaying the users' live session transcripts through the production vt emulator:

- **Codex stuck on Working forever (q65).** Codex has no typed status signal in v1
  (the app-server event producer is deferred, D1) — the grid is its *sole* driver.
  Its real idle screen is a composer prompt (`› …`, U+203A) with the model/cwd
  footer (`gpt-5.6-sol medium …`) rendered *below* it as the last content line. The
  generic heuristic reads only that last line, sees the footer, returns
  `unknown`, and `Derive(running, unknown) = Working`. The session never leaves its
  `unknown` seed.

- **Claude decays done→Working 30s after Stop (dqh).** A `Stop` hook commits
  `turn=idle`. The typed signal outranks the grid for 30s; at the cliff, the next
  200ms grid tap reads Claude's idle screen — a composer box above a
  `✻ Brewed for Ns` footer (the last content line) — which the generic heuristic
  cannot classify, so it commits `unknown`, and the known idle renders as Working.

In both cases an **inconclusive read overwrote a known status** and manifested as a
confident-but-wrong "Working". The apply-unknown rule treats "I cannot classify
this frame" as if it were positive evidence that the turn changed. It is not.

## Decision

**An inconclusive grid evaluation preserves the previously committed status
instead of committing `unknown`.** A *conclusive* read (active or idle) still
applies normally, including after the typed-signal freshness window. Concretely, in
`OnOutput`: if the grid evaluation is inconclusive, the committed status is left
untouched (no change, no emit); only a conclusive reading mutates the status.

This is paired with **per-adapter grid signatures** (the second half of the fix,
below) so that the codex/claude idle and busy screens now read *conclusively*
rather than inconclusively — but the preserve rule is the load-bearing invariant:
some frames (tool output, pagers, a resize, mid-scroll prose) are inherently
unreadable for any signature, and each such frame must be a no-op, not a downgrade.

Per-adapter signatures are declared as data on the adapter's heuristic
`SignalSource.Descriptor["grid"]` (the existing shape, previously the ignored value
`"prompt-marker"`) and interpreted by the engine — the adapter stays I/O-free per
ADR-001, declaring only which signature to apply, never running any logic:

- **codex** (`"grid":"codex"`): a busy marker (`esc to interrupt`, or a braille
  spinner U+2800–U+28FF) in the bottom region ⇒ active; a composer prompt marker
  (`>`, `›`, `❯`) on the parked-cursor row with no busy marker ⇒ idle; else
  inconclusive.
- **claude** (`"grid":"claude"`): a busy marker ⇒ active; a composer prompt marker
  present in the bottom region with no busy marker ⇒ idle (Claude's idle footer
  sits below the composer, so it does not require the cursor on the composer row);
  else inconclusive.
- **generic** (any other value, incl. `"prompt-marker"`): the unchanged last-line
  sentinel rule, with U+203A `›` added to the prompt sentinels.

The multi-line scan is bounded to the last 12 non-blank rows so the 200ms output
tap stays cheap.

### Reconciliation with the staleness guard (the crux)

`Tick` retains its staleness guard (S7/L1): a session left `turn=active` with **no
output for the whole threshold and idle CPU** is downgraded to `unknown`. This is
*not* in tension with the preserve rule, because the two fire on opposite evidence:

- `OnOutput`-inconclusive fires **on an output event** — positive evidence the
  session is alive and emitting bytes. An unclassifiable frame here is the *absence
  of evidence* of a turn change, so preserving the known turn is correct, and the
  frame still refreshes the staleness clock (the session is not silent).
- `Tick`'s downgrade fires on the **absence of any output** for the threshold plus
  idle CPU — positive evidence that an `active` turn is *stuck or dead*. Leaving it
  active would be confidently wrong about liveness, so the downgrade to `unknown`
  stands.

The asymmetry lives only in the *trigger and the dimension*: `Tick` downgrades only
`active` (an idle/unknown turn at rest misleads no one and needs no liveness
rescue), while preserve applies to whatever dimension was committed. "Absence of
evidence" (an unreadable frame) and "evidence of absence" (prolonged silence) are
different inputs and correctly produce different outputs. Both uphold the same L1
intent: never confidently wrong. One residual is accepted with eyes open: a session
that emits CONTINUOUS but permanently inconclusive frames keeps refreshing the
liveness clock, so a stale preserved turn is never Tick-downgraded in that state.
For the supported adapters this requires a screen that is simultaneously busy
enough to stream output and unreadable to its own signature for the whole period -
codex's busy marker and claude's typed hooks make that pathological rather than
expected. The per-adapter signatures, not the preserve rule, carry the burden of
staying conclusive.

## Consequences

### Positive

- Codex escapes its `unknown` seed the moment a conclusive idle/active frame is
  read, and *holds* that status across the subsequent unreadable frames (q65).
- Claude's typed `idle` survives the 30s freshness cliff: the grid tap that used to
  clobber it now preserves it, and the claude signature reads the idle screen
  conclusively anyway (dqh) — belt and suspenders.
- `OnOutput` can no longer manufacture a confident-wrong "Working" from a frame it
  simply could not read. `unknown` now originates only from the seed and the
  deliberate `Tick` liveness downgrade, never from a can't-tell output tap.

### Negative

- A genuine idle→active transition whose *active* frame is unreadable is not
  reflected until a readable active frame (or a typed signal) arrives. Mitigated:
  active screens carry the loud `esc to interrupt`/spinner busy marker, which the
  signatures read; and claude's transitions are driven directly by typed hooks.
- The preserve rule does **not** resurrect "stuck on active forever": an active turn
  that then goes silent is still downgraded by the `Tick` staleness guard.
- One subtlety for reviewers: `OnOutput` still refreshes `lastSignalAt` on every
  output (including inconclusive), so a session that keeps emitting unreadable
  output is correctly treated as alive and is not downgraded by `Tick`.

## Superseded behavior and test

This supersedes the apply-unknown commitment documented at engine.go:14 and in
`heuristic_test.go`. The classification function `evaluateGrid` is unchanged — an
inconclusive grid still *classifies* as `(unknown, unknown)`; what changes is that
`OnOutput` no longer *commits* that unknown over a known status. The frozen test
`TestHeuristicInconclusiveMapsToUnknown` encoded exactly the superseded
apply-unknown behavior (active→inconclusive→unknown) and is replaced by a
preserve regression test (active→inconclusive→active held; idle→inconclusive→idle
held) plus the field-test screen fixtures.

## Alternatives Considered

- **Per-adapter signatures only, keep apply-unknown**: rejected. Better signatures
  reduce but cannot eliminate inconclusive frames (tool output, pagers, resize);
  each remaining one would still clobber a known status to Working. The preserve
  rule is what makes an unreadable frame safe.
- **Asymmetric preserve (downgrade `active`, preserve `idle`) to keep the old
  test green**: rejected. Incoherent with the "absence of evidence" principle,
  and redundant with the `Tick` guard, which already downgrades a truly stuck
  active on silence — the `OnOutput` downgrade only ever fired *early*, on a live
  session, which is precisely the bug.
- **Debounce/age the unknown (require N consecutive inconclusive reads)**: rejected
  — extra per-session state for the same end effect as preserve on the observed
  bugs; over-engineered.
