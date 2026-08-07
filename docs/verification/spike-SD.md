# SPIKE S-D: Live claude 2.1.224 — busy chrome, AskUserQuestion wait, plan-approval wait

Resolves the three open questions from the 2026-08-07 status investigation
(beads agents-tracker-h7s5, agents-tracker-d7vh, agents-tracker-fji item 2).
Run against real `claude` 2.1.224 on macOS arm64, driven by the unmodified
`swarm-char` harness (real PTY, real vt emulator, real hook sink), the same
method as spike-SB/SC. Fixtures under `fixtures/spike-sd/`.

## Method

`swarm-char -geometry 100x40 -adapter none -hook-sink <uds>` exec'ing claude
through a throwaway env-scrubbing wrapper (unsets the launching Claude Code
session's own `CLAUDE*` vars — without this the child inherits
`CLAUDE_CODE_CHILD_SESSION` and mislabels the capture) with
`--setting-sources ""` and an inline-file `--settings` wiring every hook event
(SessionStart, UserPromptSubmit, PreToolUse, PostToolUse, PermissionRequest,
Notification, Stop, SubagentStop) to a stdin-to-sink relay that emits no
decision output, so interactive dialogs stay up. Scripted input types the
prompt, then submits with a SEPARATE delayed `\r` — a trailing `\r` in the
same write is treated as a bracketed paste and lands in the composer without
submitting (first run failed exactly this way; kept as a harness note).

## Runs

1. `busy-stream.json` (haiku): "write 700 words, no tools" — a 15.3s
   busy window between UserPromptSubmit and Stop, then idle tail.
2. `askuser-wait.json` (haiku): "use the AskUserQuestion tool" — the
   question dialog left standing, unanswered, to timeout.
3. `plan-wait.json` (sonnet, `--permission-mode plan`): research then
   ExitPlanMode — the plan-approval dialog left standing to timeout.
   (A first sonnet attempt detoured into AskUserQuestion asking which
   sentence to add — an unprompted second confirmation of run 2's shapes.)

## Findings

### F1 — busy chrome (h7s5): the phrase is back, in a NEW row shape

Live 2.1.224 busy status row: `⏸ manual mode on · esc to interrupt · ← 3 agents`
— `esc to interrupt` flanked by U+00B7 separators, NOT inside a parenthesis
group. The 66dfbe4 anchoring (same-row parenthesis enclosure, derived from
codex captures and older claude) therefore REJECTS the live claude busy row,
and since the `❯` composer stays rendered during streaming, evaluateClaudeGrid
falls through to the composer rule and reads a working claude as conclusively
idle. Version note: 2.1.214 (spike-sc era) did not print the phrase at all;
2.1.224 prints it mid-dot-flanked. Both the parenthesized and mid-dot-flanked
shapes must count as busy; bare prose occurrences still must not.

The working line itself renders as `<spinner> <Gerund>… (Ns · thinking)` /
`(Ns · ↓ N tokens · thinking)` with spinner glyphs ✢ ✳ ✶ ✻ ✽ · — available as
a secondary anchor but not needed once the status row matches.

### F2 — braille (fji item 2): never on the grid, claude-side moot

Every braille glyph in the capture lives inside an OSC 0/2 title string
(`ESC ] 0 ; ⠐ Claude Code BEL`) which the vt emulator diverts to Snap.Title —
zero braille cells reach the grid. The fji braille-anchoring item is
not applicable to claude; codex grid braille stays covered by its existing
pinned screens.

### F3 — AskUserQuestion wait (d7vh): permission-shaped end to end

Hook order (askuser-wait.json, ms offsets from UserPromptSubmit):
PreToolUse{AskUserQuestion} +3.4s, PermissionRequest{AskUserQuestion} +250ms
after that, Notification{permission_prompt} +6.3s after that. With fix A
(529758e) the PermissionRequest maps to needs_input and the notification
subtype no longer clobbers it — the HOOK path is already correct. The GRID
dialog renders `❯ 1. Red` (the composer glyph) with the help row
`Enter to select · ↑/↓ to navigate · Esc to cancel`, so the grid fallback
needs that marker to avoid the same composer misread the approval dialog had.

### F4 — plan-approval wait (d7vh): permission-shaped end to end

Hook order (plan-wait.json): PreToolUse{ExitPlanMode},
PermissionRequest{ExitPlanMode} +150ms, Notification{permission_prompt} +6.0s.
Hook path correct with fix A, same as F3. The GRID dialog is a THIRD shape:
"Claude has written up a plan and is ready to execute. Would you like to
proceed?" with `❯ 1. Yes, and use auto mode` and the affordance rows
`shift+tab to approve with this feedback` / `ctrl+g to edit in VS Code · …`.

### F5 — incidental confirmations

- Notification{permission_prompt} trails PermissionRequest by ~6s in every
  run — the spike-sb timing reconfirmed on 2.1.224.
- Notification{idle_prompt} fires 60.0s after Stop (observed in the first
  plan run, haiku, which answered without planning: Stop at +16.7s,
  idle_prompt at +76.8s). Fix A's idle_prompt=none mapping is the correct
  read; that run was overwritten by the retry and is recorded here only.
- In `--permission-mode plan`, read-only tools (Bash ls, Read) fire
  PreToolUse/PostToolUse with NO PermissionRequest — plan mode auto-allows
  them; nothing for status to do.

## Consequences (implemented from these fixtures)

1. Busy anchoring accepts the mid-dot-flanked shape alongside the
   parenthesis-enclosed one; prose stays rejected (h7s5).
2. evaluateClaudeGrid gains two dialog markers checked with the existing
   approval-dialog short-circuit: the AskUserQuestion help row and the
   plan-approval affordance row, both conclusive (idle, permission) (d7vh).
3. Engine-level replay tests pin the captured hook sequences end to end:
   AskUserQuestion and ExitPlanMode waits derive needs_input and hold it
   through the trailing notification (d7vh).
4. fji item 2 (braille) is closed as not-applicable for claude (F2).
