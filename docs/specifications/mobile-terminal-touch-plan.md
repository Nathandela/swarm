# Mobile terminal dashboard interaction plan

Date: 2026-10-25. Status: implemented, locally verified and [published in v0.23.0](../verification/mobile-terminal-v0230-release.md); challenged by Sol and Astra. Actual handset qualification remains open. [ADR-035](../adr/ADR-035-mobile-terminal-navigation.md) records the intentional behavior amendments. [Implementation evidence](../verification/mobile-terminal-touch/implementation.md) records the completed local gates and remaining physical qualification boundary.

Make Swarm's terminal dashboard comfortable to navigate from Termux and an iOS terminal over SSH. The deliverable is a scrollable, responsive board with collapsible groups and touch access to existing commands. Preserve keyboard operation, terminal-native text selection during editing, and the raw attached-agent boundary.

The implementation feature is tracked in `agents-tracker-nzo8`. The planning and experimental evidence is in [mobile terminal touch verification](../verification/mobile-terminal-touch/README.md); the executable planning probes reproduce historical baseline shortcomings. Current acceptance tests and final gates are linked from the implementation record.

## Scope and release boundary

The first release includes independent viewport scrolling, compact board rows, collapsible groups, a New/Open/More action strip, an attention reveal action, and height-aware keyboard forms. New opens the existing keyboard-driven creation form. The release does not claim a completely touch-operated creation or account-login workflow.

The terminal app translates touch into mouse reports, wheel events, keys and dimensions. The primary deployment path is phone → SSH → Swarm on the remote Unix machine. Local Android execution, native Android remote control, Mosh and other unused transport combinations are outside this release's qualification claim. Test tmux if either user actually runs it.

Keep Bubble Tea, Lip Gloss, the existing session model, and existing backend commands. No new dependency, daemon API, adapter interface or wire format is required. Do not add a touch gesture recognizer, application-side fling inertia, hover dependency, timed double-tap, persisted collapse preferences, or an attached-agent overlay. Agent transcripts and the attached return hint retain their current ownership.

## Decisions established by the probes and review

Nine small Go probes against baseline `fb20750f8bc6d30b5f3379618712a58e62b9e2f0` reproduced invisible selection activation, lost narrow-screen identity, gesture activation after resize/drag, delayed-coordinate ambiguity, clipped form focus/errors, a clipped selected agent, changed scrolling semantics when wheel deltas are summed at bounds, and retransmission of unchanged content by the actual renderer. See the verification record for commands and exact results.

The resulting implementation choices are:

- Extend `boardLine` and the shared `layout` traversal in `internal/tui/general.go`; do not build a second hitbox tree or a general UI framework.
- Separate viewport position from semantic keyboard focus. Focus is a session ID or a collapsed section key, never a screen line number.
- Keep folded groups folded during keyboard browsing. Expanded headers are not additional navigation stops; collapsed headers are.
- Reveal an offscreen target before any session-scoped command can act. Apply this rule to every caller, not only Enter.
- Keep form mouse capture off in this release. Fix form geometry without replacing terminal-native selection.
- Coalesce paints, not the meaning or order of input events.

## Board geometry and responsive information

One geometry description must drive rendering, pointer targets, visible keyboard stops and visibility checks. Extend the current line kinds with a collapsed section target and bounded action regions. Each target carries its semantic identity and cell bounds. Reuse the existing traversal and cell-aware text helpers; introduce caching only after profiling shows a need.

Reserve a pinned header and one bottom action/status row. Scroll the grouped content between them. Notices and banners have an explicit height budget, never silently replace an actionable row or cover an input target. Preserve existing connection-loss priority and meaningful error messages. Prefer removing decorative spacer rows before removing useful content. Guarantee at least one navigable content line: drop the separate version notice before losing that line, and use a bounded nonactivating message/size state if mandatory error or decision context still cannot fit. Test attention, version skew and an error banner together at 24×4.

Use a one-line compact session row as the baseline for narrow or short terminals. The priority is selection indication, a useful name budget, actionable state, then agent/repository, then elapsed/summary. Preserve approval/question/error vocabulary and degraded-backend, resume-failure and supervision indicators ahead of decorative metadata. Shorten or omit secondary fields deliberately; never show a misleading healthy state because its error marker was clipped. Full diagnostic reasons remain available through existing diagnostic surfaces.

Keep the current wide table when its allocated fields meet useful minima: name at least 16 cells, repository at least 12, and summary at least 12. The exact transition follows the existing column allocator, with boundary tests; it is not a phone-brand heuristic. Compact rows use the full remaining width for identity and state. Do not add variable-height cards in the first implementation.

The initial supported geometry contract is a board and More menu at 24 columns × 4 rows and text/decision screens at 32 × 8. The latter includes Launch, Resume, Handoff, Options, Accounts, Pairing and inline rename/tag/kill/delete states. These minima are verified by the implementation's render tests, not measured handset dimensions. Below a screen's supported minimum, preserve drafts and show a bounded size notice, retain Escape/back/deny behavior, and refuse new session actions or approvals. Even 1×1, 1×2 and 2×3 output must remain bounded. If a mandatory target/decision cannot be represented above the nominal minimum, use a readable scrollable decision state before enabling its confirm action.

Truncated rows use an ellipsis, not an implicit claim of unique identity. Test duplicate names, unnamed sessions and long shared prefixes. Before a consequential action, make the full name, agent/repository and session ID inspectable in the menu/confirmation's scrollable target context. Do not add permanent identity columns merely for this case; target identity in code always remains the full session ID.

Test actual handset dimensions as soon as captured. The support claim follows those results; it cannot be inferred from cell count alone.

## Scrolling, focus and action semantics

| Input or event | Required behavior |
|---|---|
| Wheel/pan/fling reports inside grouped content | Apply each event in order to the bounded viewport; do not change focus, wrap, activate, or steal form focus |
| Wheel over header, footer or outside content | No content movement or activation |
| Up/Down or j/k | Move among sessions in expanded sections and one header per collapsed section; retain wrapping and reveal the destination |
| PageUp/PageDown | Move focus by the visible content budget, clamp at the ends, and reveal it |
| Home/End | Focus and reveal the first/last visible navigation target |
| Tap an unselected session | Select the whole row, including padded cells, without opening |
| Tap the already selected visible session | Arm on press; open only on a valid completed release, preserving ADR-029's clock-free rule |
| Enter/Open on an offscreen selected session | Reveal it and consume the action; a later explicit activation opens it |
| Rename, tag, resume, handoff or kill on an offscreen selection | Reveal it and consume the action; do not open an editor, submit a request or arm confirmation |
| Focus is a collapsed header | Session-scoped commands have no session target; Enter/Right expands the group |
| Status update or timer while browsing | Preserve focus and top content anchor; do not snap back to the selection |
| Return from attach or a form | Restore board focus, collapse state and reading position, resolving vanished targets deterministically |

Apply the reveal-first rule once in the board session-action path, including pointer actions. A More entry supplies its captured ID and readable menu context instead of resolving board focus; that named menu context counts as visible even when the menu covers the board or the underlying row moves. Global New, Options and Quit are not session-scoped. While disconnected, preserve local navigation and folding but retain the existing block on daemon actions; recovery must not replay an earlier action. Because disconnected states retain capture off, their local folding/navigation is keyboard-driven.

Keep a top-visible content identity; one-line board items need no nonzero within-item offset. On resize or roster mutation, resolve that anchor and clamp it within the available content budget. If the anchor disappears, choose a nearby surviving target in the previous display order. User keyboard movement intentionally reveals its destination; wheel scrolling and background updates do not.

## Collapse and attention

Collapse state is client-local and ephemeral, keyed by `(grouping mode, existing raw section key)`. Repo grouping currently uses directory basenames; this feature does not redefine it as full-path grouping. Identical labels in status, repo and tag groupings must not share collapse state accidentally.

Clicking the whole section header toggles it on release. Left collapses the focused session's section and focuses that collapsed header. Right or Enter on a collapsed header expands it and focuses its first member. This deterministic first-member rule avoids a new per-group selection history. Up/Down crosses folded groups as single stops, leaving them folded.

If a selected session moves into a folded group, focus that header without auto-expanding. If a focused section disappears, select a nearby visible target. Preserve header focus through status apply, removal, tag changes, grouping/order changes and authoritative roster reconciliation. A `selectedID()`-only snapshot is insufficient because headers have no session ID.

Headers show total membership and current Needs input/Ready for review counts. Keep a persistent aggregate in pinned chrome, prioritizing attention over grouping labels and build version. It must include both Needs input and Ready for review; errors already belong to Needs input and retain their state label. The count indicates current state, not newly delivered or unread events.

The aggregate is a release-activated target and has a `g` keyboard equivalent on the plain board. It reveals the next attention session in display order, expanding its group if necessary, and selects without attaching. Repeated use advances through current attention sessions; zero attention is a harmless no-op. This explicit route avoids attention becoming undiscoverable inside an offscreen folded group. Ordinary attention events update counts and retain existing notifications without automatically moving the user's viewport.

When space permits, a collapse/expand preserves the clicked header's viewport row. At the beginning/end of content, clamp instead of adding empty content merely to hold its position. Test every emitted frame within this feasible range, not just the final view.

## Touch actions and input ownership

The plain board's bottom strip exposes New, Open and More with separate bounds. Open reads Show when its selected session is offscreen and only reveals it; it reads Expand for a focused folded group. There is no Open target when no action is possible. Version text yields before these controls. The strip must fit the board's minimum width without overlapping content.

More is opened with `m` as well as a pointer target. It owns its own viewport and input routing: wheel scrolls only the menu, arrows/j/k select and reveal entries, Enter activates, Escape returns. Entries are full-width touch targets; a single completed press/release activates an entry, and Kill/Delete opens the existing confirmation. No event falls through to the board beneath it. At 24×4, a compact target label, one visible entry and return/status affordance must fit; every entry and longer target context remains reachable through scrolling. If required decision context cannot be represented, use the same nonactivating fallback policy. It exposes existing global Options/Quit and existing session Rename/Tag/Resume/Handoff/Kill or Delete as applicable. Reuse existing command implementations and checks. Do not introduce a generic action registry.

Opening More captures a session ID only when that session is visible. Session entries show the captured target's name/state and provide its disambiguating context; global entries remain independent. A target moving off the underlying board does not retarget or invalidate the named menu context. If it disappears or changes eligibility, close/disable the affected entries and explain why. Never dispatch a session command against a newly clamped neighboring selection. The existing kill/delete confirmation remains necessary and captures its own target. Text forms, inline editors, pairing and confirmations retain mouse capture off.

Enumerate capture states: plain board and More navigation request the existing cell-motion mode; free-text forms, inline editing, confirmations, pairing, attach and disconnected mutation states do not. Clear gesture state across every transition. Tab remains nonactivating on the board; do not interpret Termius double-tap's positionless Tab as an open command.

All new pointer actions commit on release. Store action kind, stable identity, bounds and a geometry generation for an armed action. An unselected-row press selects only; its release cannot become an open because selection changed during the same tap. Reject a release without its press; reject mismatched buttons/targets; cancel on wheel, keyboard navigation, actual drag motion, resize, regroup/collapse, geometry-changing roster or banner update, screen/modal transition, connection loss, or an invalidated target. Spinner or elapsed text changes that leave geometry unchanged need not cancel a click. Treat focus-loss events as optional cancellation when delivered, never a correctness prerequisite.

This is a model-level guarantee. SSH mouse reports carry no displayed-frame identity. An old-frame press arriving after a roster update can still resolve against newer geometry; the retained probe demonstrates that limit. Do not claim a geometry generation certifies what the phone displayed, and do not add a renderer acknowledgment protocol. Consequential commands require a named, ID-bound menu/editor/confirmation; inspect accidental attaches during handset trials. Preserve the accepted initial-selected-row tap behavior unless those trials establish a need to change it.

## Keyboard forms and decisions

Make Launch, Resume/relocation, Handoff, Options and Accounts height-aware, plus Pairing and inline rename/tag/kill/delete states. Reuse line editors, declarative option schemas and the existing compact picker fallback. In particular, an overflowing provider picker must still show its selected provider.

After navigation, validation, asynchronous detection/schema changes, resize or roster reconciliation, keep the active input and cursor visible. Also expose the target identity, validation error and required confirmation/disclosure context. A viewport that only reveals the field is incomplete. Reflow warning/decision text instead of clamping away the important tail. Never let a resized or regrouped inline target disappear while a confirmation remains actionable.

When mandatory decision text exceeds the viewport, use a scrollable decision with pinned target/action identity and an explicit confirmation navigation stop after the text. Confirmation keys work only when that stop is focused and visible; blanket y/Enter/second-Ctrl+X handling is disabled elsewhere in that state. Cancellation/deny always works. This establishes reachable context rather than claiming proof that a person read it. When all required context fits, preserve the existing immediate confirmation keys. Returning to a usable size restores focus and cursor rather than resetting the form.

Size/decision guards cover every initiating mutation, not just Enter: pairing approval, second Ctrl+X, inline rename/tag save, Options save, Accounts c/d/s and sign-in activation, and launch/resume/handoff submission. An already accepted asynchronous launch/resume success retains the current automatic attach even if the terminal shrinks while the request is in flight. This is completion of an earlier authorized action, not a new size-gated decision; resize cannot cancel or duplicate it. Likewise continue observing existing account work without initiating a new operation from a size fallback.

Do not reinterpret Enter as an edit-completion key: it already submits from launch fields. This release keeps that behavior and native selection/paste. Touch choice fields and click-to-position text editing are a later capture-policy feature, outside this implementation contract.

## Change map and delivery order

| Slice | Implementation reach | Exit evidence |
|---|---|---|
| 1 Visible board and bounded compact rows | `general.go` existing layout, selection/action callers, `tui.go` body budgets/resize/mouse routing | Every session reachable; all offscreen session actions reveal first; compact identity/state; geometry and interrupted-gesture tests |
| 2 Folding and attention | Same board focus/layout helpers; `reconnect.go` roster reconciliation; all refresh/remove/tag paths | Folded keyboard stops survive updates; hidden sessions cannot activate; attention reveal works after banners expire |
| 3 New/Open/More | Board/root routing and one small menu model if needed; existing action handlers | Separate hit bounds, release activation, captured menu target, stale/disappearing target cases and native capture transitions |
| 4 Forms fit the screen | `launch.go`, `resume.go`, `handoff.go`, `options.go`, `logins.go`, pairing and inline-state composition | All provider schemas, selected picker, target/error/disclosure visibility, cursor/drafts and minimum-size behavior |
| 5 Render efficiency and qualification | Existing `agents-tracker-nzh`; TUI renderer/test seams and evidence | Actual output-byte tests, elapsed boundaries, resize/restore recovery, both actual SSH clients |

Each slice starts with focused failing acceptance tests. The retained planning probes are isolated baseline characterizations: convert relevant cases to future-behavior tests and record their expected reversal, rather than adding baseline defect assertions to normal CI. Update directly affected goldens/style pins with an explained contract change. Review each slice independently before continuing.

Slice 5's redraw work may proceed independently after its test contract is reviewed. Keep elapsed ticks and existing 90 ms cell-diff working animation. Remove forced full-frame clears/SGR nonce only with actual renderer-byte assertions, elapsed bucket boundary tests and resize/restore recovery checks. Reuse the [existing investigation](../verification/tui-repaint-investigation.md); its historical numbers are not new measurements. Do not require zero bytes from a board with visible working animation.

Apply wheel events in their original order, clamping after each. Summing a burst can change its final position at boundaries. Let the framework coalesce paints; any further event optimization needs equivalence tests, not an unbounded queue or a fixed repaint loop. Stop animation when no working indicator is visible, including fully folded/offscreen working groups, without losing current counts/state.

Preserve the zero-allocation same-group update invariant unless a measured need and reviewed replacement justify changing it. Measure 50 sessions, then larger 500-session/grouping fixtures to expose nested section scans; add indexing or virtualization only if those results require it.

## Specification changes

The implementation must record deliberate changes to [ADR-029](../adr/ADR-029-board-mouse-selection.md) D1/D2/D4/D5: new board/menu targets, viewport wheel semantics, folded headers and action strip. D3's clock-free release rule and raw-attach isolation remain.

Use a proposed ADR through the repository's normal decision process to amend system-spec V-3 for folded navigation and reveal-first contextual actions, V-4 for compact field priority, and the affected Epic 7 acceptance wording. Do not silently weaken the existing wide-view fields, keyboard coverage, one-second status delivery, confirmations or native text-entry behavior. This planning artifact does not mark the new contracts implemented or an epic complete.

Documentation reach includes the TUI design preview, affected usage/keyboard help, documentation index, specification/implementation-goal cross-links, and per-slice verification. Release CI remains tag-only.

## Acceptance and qualification

Automated geometry covers 24×4, 32×8, 36×12, 40×18, 48×24, 72×12, 90×16, 80×24 and 120×40; each layout threshold at `b-1/b/b+1`; and below-minimum 1×1, 1×2, 2×3. Include names/paths/tags containing CJK, combining marks and emoji, unnamed sessions, all attention tokens, degraded backend/resume/supervision states, banners, version notices and disconnected states.

Exercise every session-scoped command while its target is offscreen; all groups folded; focus moving into a folded group; vanished/empty groups; status/repo/tag key collisions and shared repo basenames; captured More target deletion; and status/regroup/resize/banner changes before and during a gesture. A cancelled gesture can never execute later after returning to the board.

Exercise form schema growth while the prompt is focused, every provider's last option, selected-provider truncation, long validation errors, cross-CLI handoff disclosure, running-source warning, resume relocation context, pairing words, inline target regrouping, connection loss/recovery, keyboard open/close and rotation. Validate native select/paste and existing attach byte preservation/mode-reset tests across the new capture transitions.

Maintain N-1 first paint p95 under 100 ms at 50 sessions, N-2 local added attach overhead p95 under 10 ms, and existing status delivery requirements. Report input-to-model, model-to-output and handset-visible timing separately; SSH network RTT is part of the last measurement. No new end-to-end millisecond promise is established by the local probes.

Before a mobile compatibility claim, record on both actual phones: client/version/OS, SSH and tmux path if used, font and keyboard-open/closed cell dimensions, tap/release and swipe/wheel traces, long-press selection/paste, Ctrl+q availability, resize and attach/detach repetition. The iOS app is currently unidentified. Unsupported swipe translation cannot be repaired by guessing gestures in Swarm.

Run task trials finding/opening the bottom of a 50-session board, folding and restoring location, revealing attention inside folded groups, editing with the keyboard open, copying/pasting and returning from each used agent. Repeat with controlled RTT/jitter/bandwidth constraints and background/foreground transitions. Record pass/fail per actual client; desktop PTY tests do not establish thumb accuracy or phone rendering performance.

Product changes require `go build ./...`, `go vet ./...`, `go test ./...`, `golangci-lint run` and the applicable GG-4 race gates. Follow the repository test supervisor and shared-cache rules. Release publication and physical handset qualification remain explicit gates; local implementation does not establish either. Attach input-mode continuity remains a separate hypothesis to characterize; do not add generic mode replay to this feature.
