# ADR-006: Field-test UX revisions — detach key, full-screen chrome, auth inheritance

**Status**: Accepted
**Date**: 2026-07-18

## Context

The first human field test (v0.1.0, 2026-07-18) surfaced usability defects the
automated verification could not see: every gate ran against fake PTYs and
in-memory terminals, so real-terminal rendering, input affordances, and
perceived latency went unexercised. Three of the findings are decision changes
rather than plain bugs, so they are recorded here per the orchestration
protocol (implementation-goals.md, step 6) instead of drifting silently from
the spec.

1. **Detach key**: the spec (scenario 8, requirement A-5) pinned Ctrl+\
   (0x1c). On Swiss/QWERTZ/AZERTY layouts the backslash requires a
   Shift+Alt/AltGr chord, making Ctrl+\ close to untypeable — the field test
   flagged it immediately.
2. **Screen chrome**: the board rendered inline in the scrollback with no
   alt-screen and only per-screen hint lines; the field test found mode
   boundaries and available keys undiscoverable.
3. **Agent auth/billing**: the launch env allowlist forwards
   `ANTHROPIC_API_KEY` (spec scenario 18: the agent sees the launching
   terminal's environment). In the field test that silently flipped a
   Max-subscription user onto organization API billing, because the Claude
   CLI prefers an env API key over its keychain OAuth token.

## Decision

1. **Detach key becomes Ctrl+q (0x11), configurable.** Single chord, the Q
   position is identical across US/Swiss/QWERTZ/AZERTY layouts, mnemonic
   ("quit the view"), unbound by Claude Code, Codex, and core readline
   editing, and delivered cleanly in raw mode (IXON is off, so no XON
   collision). The `attach.Config.DetachKey` seam stays, so the key remains
   configurable. Spec scenario 8 and A-5 are updated in the same change.
2. **The board becomes a full-screen alt-screen app with a persistent status
   bar** of context keys; the attached view keeps the full raw terminal and
   gains a persistent one-line chrome bar naming the session and the detach
   key. Mode boundaries are always visible.
3. **Agent CLIs inherit the machine's billing/auth setup untouched.** swarm
   forwards the launching environment faithfully and never strips, rewrites,
   or injects credential material (beyond its own `SWARM_HOOK_*` vars,
   ADR-004). The launch form gains a neutral, purely informational indicator
   of the auth source the agent will inherit (e.g. "auth: ANTHROPIC_API_KEY
   from env (API billing)"); it gives no advice and changes nothing.

## Consequences

### Positive

- The detach chord is typeable on every common layout, and discoverable via
  the persistent attach bar.
- A mis-hit of Cmd+Q (macOS terminal quit, adjacent to Ctrl+q) is contained
  by the architecture: sessions live in daemon-owned shims and survive
  terminal close (scenario 3), so the worst case is reopening the terminal.
- Billing surprises become visible at launch time instead of on the invoice,
  without swarm ever second-guessing the user's environment.

### Negative

- Ctrl+q cannot be typed *into* an attached agent without remapping the
  detach key (the same shadowing any escape key has; 0x11 is not used by the
  supported agent CLIs).
- Alt-screen hides the board from terminal scrollback; scrollback of agent
  output remains available inside sessions via the transcript.
- The attach chrome bar is painted once at attach time, not composited on every
  frame, so a full-screen agent (e.g. Claude Code's own TUI) that repaints the
  whole terminal can overdraw it. A truly persistent overlay would require
  client-side compositing of each agent frame, which the raw-passthrough latency
  decision (ADR-002) rules out for v1.0; the detach key stays in effect regardless
  of whether its hint is currently visible.
- The attach chrome now defaults OFF (v0.2): it overwrote snapshot row 1 content
  (DECSC/DECRC preserves the cursor, not the cells it drew over), so a session whose
  first row carries content lost it to the bar. Snapshot fidelity wins by default;
  the board's persistent bottom status bar carries the detach-key hint
  ("ctrl+q returns") instead, and the Chrome seam remains for callers that want it.
- Chrome is back ON by default (v0.3), but the overdraw problem above is designed
  out rather than tolerated: the hint gets its OWN row the agent cannot touch. When
  chrome is engaged the session PTY is sized to `rows-1` (both at attach and on every
  SIGWINCH), a DECSTBM scroll region of `1..rows-1` keeps normal-mode scrolling off the
  real bottom row, and the reverse-video hint is painted there under DECSC/DECRC so the
  cursor is preserved. The snapshot paint is clipped to `rows-1` for the same reason.
  The remaining trade-offs are small and bounded: (a) the agent sees one fewer row while
  attached; (b) a full reset (`ESC c`), an `ED2` clear, an alt-screen swap, or a bare
  `CSI r` from the agent can still transiently clobber the row, so the output pump
  re-asserts region+hint after each frame batch — immediately on one of those damage
  signatures (a cheap byte scan), otherwise throttled to at most once per ~250ms — which
  self-heals the bare-`CSI r` region reset; and (c) a terminal of `rows<=2` is too small
  to reserve a row, so the hint disables itself and the attach falls back to exactly the
  Chrome:false passthrough. On detach the region is reset to full (`CSI r`) and the hint
  row cleared before the board repaints.
- v0.3.0 hardening: the re-assert injections are now **ground-state-gated**. A minimal,
  allocation-free parser-state tracker is fed every agent output byte, and the pump (and
  the trailing heal timer) inject region+hint bytes ONLY when the tracker is between
  sequences (GROUND). PTY frames chunk at arbitrary byte offsets, so a frame can end mid
  escape sequence; injecting there would abort the agent's pending sequence and render its
  continuation as literal text. Gating on GROUND keeps raw-passthrough purity (A-1) intact
  even across split escape sequences. A one-shot ~300 ms trailing heal timer, re-armed
  after each frame batch and delivered through the main loop's nudge channel (never written
  from the timer goroutine), re-asserts the row when damage lands in the last frame of a
  burst with no further output — also bounding the one case DECSTBM cannot prevent, an
  absolute `CSI 999;1H` stomp past the scroll region, to one timer period. `chromeHint` now
  emits `CSI ?6l` (DECOM off) under the DECSC/DECRC save so the reserved-row CUP is absolute
  even when the agent enabled origin mode. Teardown keys on whether chrome actually engaged,
  so a Chrome:true run on a `rows<=2` terminal is byte-identical to Chrome:false. Known
  residual trade-offs remain tracked as beads: an agent's persistent sub-regions are
  re-asserted over (the row is ours while attached), a damage signature split across two
  frames heals via the trailing timer rather than immediately (the per-frame scan is not
  cross-frame), and the primary-screen DECSC/DECRC register is shared with the agent.

## Alternatives Considered

- **Keep Ctrl+\**: rejected — effectively untypeable on non-US layouts.
- **Ctrl+g / Ctrl+t / Ctrl+o / Ctrl+b / Ctrl+r**: rejected — bound by Claude
  Code (external editor, todos, verbose, background, history).
- **Ctrl+] (telnet-style)**: rejected — same AltGr problem as backslash.
- **Prefix chords (tmux-style Ctrl+q d)**: rejected — two-step detach is
  over-engineering for a single-action escape.
- **Stripping `ANTHROPIC_API_KEY` when keychain OAuth exists**: rejected —
  swarm must not assume or tamper with the machine's billing setup; faithful
  inheritance plus visibility is the contract (user directive, 2026-07-18).
