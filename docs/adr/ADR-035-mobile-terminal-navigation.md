# ADR-035: Responsive terminal navigation with explicit input ownership

- Status: Implemented; [local gates verified](../verification/mobile-terminal-touch/implementation.md); actual handset qualification open
- Date: 2026-10-25
- Amends: ADR-029 D1/D2/D4/D5, system-spec V-3/V-4 and Epic 7 E7.2/E7.4
- Preserves: ADR-029 D3 clock-free release activation, ADR-019 detach recognition, raw attach A-1/A-2, keyboard completeness and tag-only release CI

## Context

The owner uses Swarm over SSH from Termux; colleagues use an unidentified iOS terminal over SSH. Mouse reporting is already supported on the board. The missing capabilities are a real viewport, useful compact information, folding, and pointer access to existing commands. Nine retained [planning probes](../verification/mobile-terminal-touch/README.md) reproduced invisible activation, clipped identity/decisions and unnecessary redraws. The owner requested implementation of the [Sol/Astra-reviewed plan](../specifications/mobile-terminal-touch-plan.md).

The terminal application translates touch into coordinates, wheel reports, keys and dimensions. It owns native selection, clipboard interaction and pinch gestures. Mouse capture is frame-wide. Attached agent terminals remain raw passthrough; dashboard improvements must not take over their input modes.

## Decision

### A visible target precedes a session action

Viewport scrolling and semantic keyboard focus are separate. Wheel reports move the bounded content viewport without changing focus or wrapping. Keyboard navigation reveals its destination. Enter/Open, rename, tag, resume, handoff and kill/delete first reveal an offscreen selected session and consume that action; a later explicit action can operate on it.

More captures a visible session's ID at menu opening. Its readable named target context counts as visible when the menu covers the board. Actions use that captured ID and revalidate eligibility; deletion or reconciliation cannot redirect them to a neighbor. Global actions have no session target.

### Folding is navigable and attention remains discoverable

Collapse keys include grouping mode and the existing raw section key. The current repo-basename grouping is unchanged. Collapse state is ephemeral. Expanded headers are not additional keyboard stops; a collapsed group contributes one visible header stop. Left collapses the focused session's group; Right/Enter expands a focused header to its first member. Session commands cannot act on header focus.

Preserve both session and header focus through every roster refresh and grouping/tag/remove path. A session moving into a folded group focuses its header without opening it. A disappeared focus/anchor falls back deterministically in display order.

Pinned chrome retains current Needs input plus Ready for review counts. The `g` key and attention target explicitly reveal the next attention session, expanding its group and selecting without attaching. Background updates do not move the reader automatically.

### Geometry and input have one authority

Extend the existing shared board-line layout for paint, hit-testing, visible navigation and viewport bounds. Targets carry action identity and cell bounds. One-line compact rows prioritize useful name space and actionable state over agent/path/elapsed/summary; wide rows retain their established fields when useful minimum budgets fit. Important error/degraded states must not appear healthy through clipping.

New, Open/Show/Expand and More occupy a bounded action strip. More is itself touch- and keyboard-operated, with its own scrolling and no fall-through to the board.

Pointer transitions commit only after matching press/release. An unselected-row tap cannot open on its own release. Cancel armed actions on geometry changes, wheel/key navigation, actual drag motion, screen/capture transitions, disconnect or target invalidation. This extends ADR-029 D2/D4/D5. Keep the existing cell-motion mode; no hover, gesture inference, timed double-tap or application inertia is added.

These are model-level guarantees. A report arriving after a roster change carries no phone-visible frame identity. Geometry generations do not eliminate pre-press SSH ambiguity. Consequential actions retain named ID-bound context and confirmations; real handset trials remain necessary.

### Native editing and readable decisions are preserved

Capture is allowed only on plain board/menu navigation. Forms, text editors, confirmations, pairing, disconnected mutation states and attach leave capture off. An in-flight report cannot activate the destination screen after a transition. This is the narrow ADR-029 D1 extension; it does not add mouse-controlled forms.

Forms fit the height as well as the width. Keep the active field/cursor, target, errors and mandatory decision context accessible through resize and schema updates. A selected provider cannot be truncated out of its picker. Long decisions have a pinned target/action and an explicit visible confirmation stop after the text; confirmation keys do not operate on an earlier hidden state. Cancel/deny remains available.

The locally verified minimum is 24×4 for the board/menu and 32×8 for text/decision screens; these are character-cell contracts, not measured handset dimensions. Smaller sizes preserve drafts and provide bounded nonactivating fallback. Every initiating mutation is guarded, including non-Enter account and pairing operations. An already accepted asynchronous launch/resume result retains its automatic attach if the screen shrinks while it is pending; no operation is duplicated or replayed.

### Rendering follows actual changes

Keep elapsed reevaluation and small working-indicator diffs. Remove the SGR nonce and periodic forced clear after replacing the destructive test-output assumption with meaningful liveness assertions. Verify actual renderer bytes, elapsed boundaries and resize/restore behavior. Preserve wheel-event order and clamp each event; coalesce paints rather than summing away boundary semantics. No new rendering framework, dependency or protocol is introduced.

## Consequences

### Positive

Every session remains reachable on short screens. Folded groups stay folded during navigation; attention stays explicit. Existing commands become accessible through a small action surface, while keyboard and raw-agent workflows retain their ownership. The renderer can avoid retransmitting unchanged content over SSH.

### Negative

Wheel input intentionally changes meaning from selection movement to viewport movement. Offscreen contextual keyboard actions require a reveal step. Compact rows omit secondary fields. Text-entry workflows still need the software or hardware keyboard, and actual touch translation differs between terminal applications.

Physical qualification cannot be inferred from PTY tests, local latency or nominal character-cell sizes. Do not label the feature handset-qualified until both actual clients pass the recorded gate. Implementation verification and remaining gates belong in [the implementation verification record](../verification/mobile-terminal-touch/implementation.md).

## Alternatives considered

Keeping invisible sessions in folded keyboard navigation was smaller but reopened groups while browsing. A second hitbox framework or renderer migration added no needed capability. Global capture conflicted with native text selection. An attached Back overlay required another input owner and was excluded. Persisted folds, variable-height cards, hover and custom inertial gestures were unnecessary for the first release.
