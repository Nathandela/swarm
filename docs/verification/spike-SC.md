# SPIKE S-C: Remote-approval mechanism (minutes-later blocking)

Plan reference: D.18 R-SPK-C. Resolves opus's audit argument C3: "minutes-later
approval may be architecturally unavailable" for a synchronous in-PTY
permission prompt. Run against real `claude` (2.1.214) and `codex` (codex-cli
0.144.1) on macOS arm64.

## Decision rule (written BEFORE any probe ran — R-SPK-C.3)

Written: 2026-07-18T12:34:28Z, before executing any of R-SPK-C.1 / .2 / .4.

For each CLI's approval mechanism, define its **safe window** as the longest
staged delay (5s / 30s / 120s) at which the CLI still waits for the
decision — no timeout, no auto-deny, no error, no silent proceed — measured
from when the approval channel (hook subprocess / MCP tool call / JSON-RPC
request) receives the request to when it is allowed to answer.

Per-CLI verdict, applied mechanically to the observed safe window:

- **Safe window < 60s** (including "errors/denies immediately" or "no
  synchronous blocking channel exists at all"): one-tap-from-push CANNOT
  cover typical multi-minute human decision latency for this CLI. The
  **deep-link-to-peek + take-control fallback ships** as the ONLY mechanism
  for this CLI. This directly falsifies the optimistic reading of C3 and
  confirms opus's concern for this CLI.

- **60s <= safe window < 300s**: one-tap-from-push is viable for FAST
  responses only (the CLI can absorb a real network+decision round trip, but
  not a multi-minute one). Ship one-tap as the primary path AND the
  deep-link-to-peek + take-control fallback as the required safety net for
  slower responses — a hybrid, not a single mechanism. This partially
  confirms C3: the risk is real past the observed ceiling.

- **Safe window >= 300s (5 min) or no timeout observed up to the maximum
  tested delay (120s) with a documented/configurable ceiling beyond it**:
  one-tap-from-push is viable as the PRIMARY mechanism for this CLI. The
  deep-link fallback still ships as defense-in-depth (cheap, covers the
  channel-unavailable case) but is not load-bearing for the common case.
  This falsifies C3 for this CLI: minutes-later approval IS architecturally
  available.

- **NOT-APPLICABLE** (mechanism does not exist for the installed CLI version,
  or exists but is structurally unusable for the interactive PTY session
  swarm actually drives, e.g. restricted to a headless/print-only mode): the
  mechanism is excluded from that CLI's verdict; the verdict is decided from
  whichever mechanism(s) remain applicable. If NONE are applicable, the
  verdict is BLOCKED and the deep-link-to-peek + take-control fallback ships
  by default (fail toward the always-available mechanism, never toward an
  unverified blocking claim).

This rule is applied per CLI independently (Claude Code's verdict does not
depend on Codex's results and vice versa), and per mechanism within a CLI
where more than one applies (e.g. Claude Code has both a hook path and an
MCP-tool path — the CLI's overall verdict takes the BEST applicable
mechanism's tier, since swarm only needs one working channel per CLI).

The 60s / 300s thresholds are fixed before results are seen specifically
because opus's C3 argument is about "minutes-later" (plural) human decision
latency: 60s is the floor below which even a fast glance-and-tap can't
realistically close (push delivery + unlock + read + tap is rarely under a
minute end to end), and 300s is the bar for "typical" multi-minute coverage
without leaning on a fallback for the common case.

## R-SPK-C.1: Claude Code PermissionRequest hook — staged blocking probe

Method: `swarm-char` drives real `claude` 2.1.214 interactively (no
`--print`, the same PTY-owning mode the real shim uses) with a
`--settings`-injected `PermissionRequest` hook whose command is a
purpose-built relay binary (`hook-relay`, throwaway, under
`/Users/Nathan/.claude/jobs/6878515f/tmp/spike-sc/`). The relay logs the
payload arrival timestamp, sleeps a staged delay read from
`SPIKE_HOOK_DELAY_SECONDS`, then emits on stdout
`{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}`
and exits 0. `--permission-mode manual` forces every Bash call through a
permission decision. The driven command is `kill -0 99999` — deliberately a
command with **no file-path argument**, because an early probe iteration
found that any command referencing a specific path (even `echo x >
file.txt`, even `rm -f nonexistent.txt`) trips a SEPARATE "sensitive file"
interactive confirmation that the `PermissionRequest` hook's decision does
NOT resolve (Claude Code still shows a blocking "Do you want to proceed? 1.
Yes / 2. ... / 3. No" prompt requiring a real keystroke regardless of the
hook's `allow`). That secondary gate is a real, reproducible finding in its
own right (see below) and is out of scope for isolating the hook's own
blocking-duration behavior, so the probe command avoids it.

The hook-relay output format matters: the first probe iteration emitted the
decision object at the top level (`{"hookEventName":...,"decision":...}`)
and Claude Code silently ignored it, falling through to the interactive
prompt every time regardless of delay. Wrapping the identical payload inside
`"hookSpecificOutput"` (confirmed against the CLI's own internal Zod schema,
recovered via `strings` on the compiled binary) fixed this — the transcript
then shows `"Allowed by PermissionRequest hook"` and the command runs with
zero interactive keystrokes.

| Delay | Hook blocked for | CLI behavior after hook returns | Hard timeout / error / auto-deny observed |
| --- | --- | --- | --- |
| 5s | 5.002s (measured) | Ran immediately, "Allowed by PermissionRequest hook", zero keystrokes | None |
| 30s | 30.003s (measured) | Same — ran immediately, decision honored | None |
| 120s | 2m0.005s (measured) | Same — ran immediately, decision honored | None |
| 300s | 5m0.002s (measured) | Same — ran immediately, decision honored | None |

Every measured wait matches the staged delay to within ~15ms across all four
runs (5s to 300s) — Claude Code's tool call is a genuine synchronous block
on the hook subprocess's exit, not a polled or best-effort wait, and shows
no sign of degrading as the delay grows. No run showed a timeout banner, an
error, or an auto-deny at any tested delay including the full 5 minutes.

**Scope caveat, now precisely bounded by a follow-up probe**: a Bash command
that references a specific file path (`echo x > file.txt`, `rm -f
file.txt`, presumably `sed -i`/`mv`/`cp` of a named file too) trips a
SEPARATE "sensitive file" interactive confirmation that the
`PermissionRequest` hook's `allow` decision does NOT resolve — Claude Code
still shows a blocking "Do you want to proceed? 1. Yes / 2. ... / 3. No"
prompt requiring a real keystroke regardless of the hook. A follow-up probe
isolated whether this is a blanket restriction on all file operations or
specific to the Bash tool's free-text path parsing: driving Claude to use
its **native `Write` tool** (the actual primary mechanism for file changes,
not a Bash redirect) to create a file, with the identical hook wired at a 5s
delay, produced NO secondary gate — hook blocked exactly 5.003s, "Allowed by
PermissionRequest hook", file written, zero keystrokes. So the gap is
narrower than first appeared: native `Write`/`Edit` tool calls (the
dominant path for file changes) are fully covered by the hook; only Bash
commands that manipulate a file path directly are not. This is consistent
with the release notes visible on Claude Code's own welcome screen during
these probes ("Fixed a permission-check bypass affecting...", "Fixed Bash
permission checks to fail close...") — a recent, apparently deliberate
security hardening that closes exactly this class of hook-based bypass for
the free-text Bash path, while leaving the schema'd Write/Edit tools
hookable.

Fixture evidence: `docs/verification/fixtures/spike-sc/c1-delay{5,30,120,300}s/`
and `docs/verification/fixtures/spike-sc/c1-writetool-check/`
(fixture.json + hook.log + rendered.txt for each).

## R-SPK-C.2: MCP permission-prompt-tool support

Finding: `claude --help` does not list a permission-prompt-tool flag in
2.1.214, but `--permission-prompt-tool <tool>` exists as a hidden flag
(`.hideHelp()` in the CLI's own arg parser, recovered via `strings` on the
compiled binary), whose own description reads **"MCP tool to use for
permission prompts (only works with --print)"**.

Probe 1 (`--print` / headless mode): built a minimal hand-rolled MCP stdio
server (`mcp-relay`, throwaway) exposing one tool `approval_prompt` matching
the wire shape Claude Code calls (`{tool_name, input, tool_use_id}` in,
`{"behavior":"allow"}` JSON-text out — shape confirmed via the same
`strings` recovery). Wired via `--mcp-config` + `--strict-mcp-config` +
`--permission-prompt-tool mcp__spike__approval_prompt`. At a 5s staged
delay: the tool was called, blocked for exactly 5.002s, returned allow, and
`claude -p` completed successfully (exit 0) with the command's result in
its answer.

Probe 2 (interactive mode, the mode swarm actually drives): identical
wiring, same command, run through `swarm-char` with no `--print`. Result:
the MCP tool was **never invoked at all** (its log file, which the relay
creates unconditionally on first call, never appeared) — Claude Code fell
straight through to the ordinary interactive "Do you want to proceed?"
confirmation requiring a real keystroke, exactly as if
`--permission-prompt-tool` had not been passed.

Verdict for this sub-mechanism: **NOT-APPLICABLE to the interactive PTY
session swarm drives.** The flag is real and the MCP wiring genuinely works
as a synchronous remote-approval channel, but only in `--print` (one-shot,
non-interactive, no live PTY) mode — structurally incompatible with a
long-lived interactive session under a shim's PTY. This matches the flag's
own documented restriction, now confirmed empirically rather than assumed.

Fixture evidence: `docs/verification/fixtures/spike-sc/c2-print-delay5s/` and
`docs/verification/fixtures/spike-sc/c2-interactive-check/`.

## R-SPK-C.4: Codex requestApproval JSON-RPC staged-delay probe

Method: a minimal hand-rolled JSON-RPC client (`codex_probe.py`, throwaway)
drives `codex app-server` (codex-cli 0.144.1) over stdio: `initialize` ->
`thread/start` with `approvalPolicy: "untrusted"` (forces every command
through an approval round trip) -> `turn/start` asking it to run `kill -0
99999` (same no-file-path command as C.1, for consistency). When the
server-initiated `item/commandExecution/requestApproval` request (a
JSON-RPC request carrying a numeric `id`) arrives, the client logs an
arrival timestamp, sleeps the staged delay, then sends
`{"jsonrpc":"2.0","id":<id>,"result":{"decision":"accept"}}`.

Reused the exact wire shapes already proven working in spike-SB's fixtures
(`docs/verification/fixtures/spike-sb/codex-rpc-transcript-run1.ndjson`),
which established the request/response schema at zero staged delay; this
probe adds the staged-delay dimension SPIKE S-B didn't test.

| Delay | app-server held request open for | Outcome after response | Hard timeout / error / cancel observed |
| --- | --- | --- | --- |
| 5s | 5.004s (measured) | `item/completed`, command executed (exitCode 1 — `kill -0` on a nonexistent PID, expected) | None |
| 30s | 30.011s (measured) | Same — executed normally | None |
| 120s | 120.014s (measured) | Same — executed normally | None |

`ReviewDecision`/`CommandExecutionApprovalDecision` in the app-server's own
generated protocol types include a `"timed_out"` variant, meaning the
protocol has an explicit vocabulary for a timeout outcome — but no run in
this probe (up to the delay tested) actually produced one.

Fixture evidence: `docs/verification/fixtures/spike-sc/c4-delay{5,30,120}s-transcript.ndjson`
+ matching `-hook.log` files.

## R-SPK-C.5: VERDICT

Applying the decision rule (written before any probe ran, section above)
mechanically to the observed safe windows:

### Claude Code (2.1.214)

- **`PermissionRequest` hook**: safe window >= 300s, the maximum tested
  delay, with zero degradation across four staged points (5s/30s/120s/300s)
  all matching to within ~15ms. Tier: **>= 300s -> one-tap-from-push is
  viable as the PRIMARY mechanism.**
- **MCP `--permission-prompt-tool`**: NOT-APPLICABLE — confirmed empirically
  restricted to `--print`/headless mode, never invoked in the interactive
  PTY session swarm actually drives. Excluded from the verdict per the
  decision rule's NOT-APPLICABLE clause.
- Best applicable mechanism: the `PermissionRequest` hook, tier >= 300s.

**VERDICT — Claude Code: one-tap-from-push via the `PermissionRequest` hook
is the PRIMARY remote-approval mechanism**, correctly wired with the
`hookSpecificOutput` envelope. This falsifies the pessimistic reading of
opus's C3 for Claude Code's dominant case: a remote decision arriving
minutes later — up to at least 5 minutes, measured, with the underlying
mechanism showing no signs of a ceiling — is applied to the live in-PTY
session with zero additional local interaction, for native tool calls
(Write, Edit, Bash-without-a-file-path). **Carve-out**: Bash commands that
name a specific file path (shell redirects, `rm`, and presumably
`sed -i`/`mv`/`cp` of a named file) hit a second, hook-unresolvable
interactive confirmation — for that narrower class of action, the
deep-link-to-peek + take-control fallback is REQUIRED, not optional. Ship
both: the hook as primary, the fallback as the mandatory path for
Bash-with-file-path actions (and as defense-in-depth generally, per the
decision rule's baseline recommendation).

### Codex (codex-cli 0.144.1)

- **`item/commandExecution/requestApproval` JSON-RPC**: safe window >= 120s,
  the maximum tested delay, zero degradation across three staged points
  (5s/30s/120s), all matching to within ~15ms. The app-server's own
  `ReviewDecision`/`CommandExecutionApprovalDecision` protocol types define
  a `"timed_out"` variant (so the protocol has a real vocabulary for a
  timeout outcome), but none of the tested delays triggered it. Tier: this
  sits in the **60s <= window < 300s bracket** by the letter of the decision
  rule (120s was the maximum tested, not >= 300s, and there is no
  documented/configurable ceiling found beyond it to invoke the rule's
  "no timeout observed... with a documented ceiling beyond it" escape
  clause) — a 300s Codex run was not run (time-budget tradeoff; the 120s
  Claude result had already been cross-checked at 300s, and Codex's
  identical zero-degradation pattern at every tested point up to 120s makes
  a ceiling between 120s and 300s unlikely, but this spike did not measure
  it directly).

**VERDICT — Codex: hybrid.** Ship one-tap-from-push via the
`item/commandExecution/requestApproval` JSON-RPC response as the primary
path for fast responses (confirmed safe to 120s with no sign of an
approaching ceiling), AND ship the deep-link-to-peek + take-control fallback
as the required safety net for slower responses, per the decision rule's
60-300s bracket. Recommend a fast, cheap follow-up (one more staged run at
300s, ~5 minutes wall clock) before finalizing Codex as PRIMARY-only; this
spike stopped at 120s to stay within its time budget and the pattern
strongly suggests it would pass, but that is an extrapolation, not a
measurement.

### Cross-cutting

Both CLIs' synchronous approval channels are real, held open for at least
120s (Codex, measured) to 300s (Claude, measured) with zero timeout, error,
or auto-deny — directly falsifying the strong form of opus's C3 ("minutes-
later approval may be architecturally unavailable") for both CLIs' primary
tool-call paths. The risk C3 correctly anticipated is real but narrower than
"architecturally unavailable": it shows up as (a) Claude Code's
Bash-with-file-path carve-out, and (b) Codex's untested-but-plausible
60-300s ceiling. The deep-link-to-peek + take-control fallback should ship
regardless of these results — both as the mandatory path for the identified
carve-outs and as defense-in-depth for any approval channel that becomes
unavailable (network partition, hook crash, MCP server down, host process
restart) at the moment a decision needs to be applied.

### Additional observation (out of scope for this spike, worth a follow-up)

`claude --help` lists a NATIVE `--remote-control [name]` flag ("Start an
interactive session with Remote Control enabled") and an associated
`--remote-control-session-name-prefix`, and probe sessions in this spike
displayed a live "remote control -SWARM" status bar (inherited from this
job's own ambient environment, not something this spike configured). This
suggests Claude Code may already ship a first-party remote-control feature
that could be a more direct answer to the "minutes-later approval" question
than the from-scratch `PermissionRequest`-hook wiring this spike validated.
Not investigated here (out of scope for R-SPK-C.1/.2/.4) — flagging for a
possible fast follow-up spike, since if it already solves remote approval
natively it could simplify or replace the custom hook-relay approach.
