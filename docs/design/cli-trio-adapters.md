# CLI Adapter Design: agy, opencode

> **Historical strategy-phase document.** The implemented v1.1 behavior is
> recorded in docs/verification/cli-duo-adapters-evidence.md and the adapter
> sources (internal/adapter/agy, internal/adapter/opencode) — where this
> document and the code/evidence differ, code and evidence win.

Status: strategy/exploration phase output (2026-07-18, issue agents-tracker-5gv).
Scope note: this phase originally evaluated three CLIs; **vibe was dropped by
decision on 2026-07-18** (see appendix). Integration targets are **agy**
(Antigravity CLI, Google — native Go/Bubble Tea binary, multi-backend: Gemini,
Claude, GPT-OSS) and **opencode** (anomalyco/opencode, Bun/TypeScript,
client-server).

Every claim below marked "verified" was established empirically on this machine
against the installed binaries; provenance is the recorded fixtures under
docs/verification/cli-trio-exploration/ and the temporary test packages
internal/adapter/trioproto/ and internal/engine/trio_exploration_test.go (both
to be deleted when implementation starts).

## 1. Spec note

system-spec.md T-7 currently names the v1.1 lineup as "Gemini CLI, OpenCode,
AGY". This design supersedes that: agy IS Google's successor to Gemini CLI
(Gemini CLI stops serving non-Enterprise requests 2026-06-18), so the v1.1
lineup is exactly agy + opencode. T-7 must be amended when implementation
starts.

## 2. What was verified per CLI

| | agy 1.1.4 | opencode 1.17.9 |
|---|---|---|
| Version banner | `1.1.4` | `1.17.9` |
| Probe latency | ~50-400ms | ~300ms warm, **1.8s loaded** |
| Launch argv | `agy [--model M] [--mode X] [--prompt-interactive P]` | `opencode [--model p/m] [--agent A] [--prompt P]` |
| Model flag | yes — accepts `agy models` display names (verified live) | yes (`provider/model`) |
| Resume argv | `agy --conversation <uuid>` (verified: context retained) | `opencode --session ses_<id>` (verified via `run -s`) |
| Id in PTY stream | yes — exit screen prints `agy --conversation=<uuid>` | yes — exit screen prints `Continue  opencode -s ses_<id>` |
| Busy signal on grid | trailing `⣷ Generating...` line — stock heuristic classifies **active** (verified) | spinner + "Thinking" mid-grid, never last line — stock heuristic **blind** |
| Idle signal on grid | `>` inside bordered box; border/footer is last line — **unknown** | bottom status bar is last line — **unknown** |
| Typed-event potential | `--output-format stream-json` (print mode), hooks in `.agents/hooks.json` | first-class HTTP+SSE server (`opencode serve`, `GET /event`, `session.status` busy/idle/retry, permission/question request objects) |
| Launch gotchas | first-run workspace-trust prompt (persisted in `~/.gemini/antigravity-cli/settings.json` `trustedWorkspaces`); background auto-updater bumps version silently; needs non-zero PTY winsize + capability-query replies (swarm-char/shim already provide both — verified) | ~1.5s cold start; terminal capability queries at startup (handled by internal/vt — verified) |

Storage (context only; pure adapters never read it): agy = SQLite per
conversation under `~/.gemini/antigravity-cli/conversations/`; opencode =
SQLite `~/.local/share/opencode/opencode.db`.

## 3. Adapter designs (prototyped and conformance-proven)

Each is a stateless pure strategy object per the frozen contract. At the time
this section was written, prototypes lived in internal/adapter/trioproto/ and
passed `adapter.Conformance`, real-binary `adapter.Detect` via `detect.Host`,
and extraction against the recorded captures; that package was **deleted in
Phase H** once the production adapters shipped — they now live in
internal/adapter/agy/ and internal/adapter/opencode/. Highlights (prototype
values; see the evidence file for the shipped ones where they differ):

- **agy**: options = model (string, Suggest = the 8 `agy models` names), mode
  (choice: accept-edits/plan), dangerously-skip-permissions (bool).
  InitialPrompt via `--prompt-interactive`. Extraction marker `--conversation=`
  with control-byte-aware token termination (a raw tail butts `\x1b[K` against
  the uuid — caught by the capture test; whitespace-only termination is a bug).
  SupportedVersions Min 1.1.0.
- **opencode**: options = model, agent (free strings). InitialPrompt via
  `--prompt`. Extraction = LAST `ses_<alnum>` token (length- and
  terminator-guarded); last occurrence because transcripts can mention child
  session ids. **Superseded (R-H4, 2026-07-18)**: the shipped extraction is
  anchored to the last `opencode -s ` exit-command marker — a bare prose
  `ses_` token could be captured write-once by the daemon's live transcript
  scan; see the evidence file's R-E6 amendment. **Descoped from this
  prototype description**: the shipped
  adapter is heuristics-only and declares no event sources — the invented
  flattened event names this bullet originally proposed (e.g.
  "session.status.busy") were ruled out as encoding a fake wire schema; see
  system-spec.md T-2 and the evidence file's R-E4 row. Min 1.0.0.

Registry: add both to `constructors` AND `production` in
internal/adapter/registry/registry.go — the only mandatory core-adjacent edit
(T-5 confirmed to hold in practice by a full coupling-point sweep).

## 4. Status-signal strategy

Finding: both TUIs anchor a status bar / input-box border at the bottom of the
screen, so the stock last-line heuristic (internal/engine/heuristic.go) sees:
agy = active-while-generating but never idle; opencode = permanently unknown.
Timeline evidence: TestTrioExploration_HeuristicTimeline.

v1 approach (DECIDED 2026-07-18): **descriptor-driven bottom-region grid
rules**, read by the engine from the adapter's declared heuristic
SignalSources — e.g. `busy-contains` (illustrative markers at design time;
the SHIPPED rules are `busy-contains` "esc interrupt" for opencode, and
`busy-contains` "esc to cancel" + "Generating..." plus `idle-line-equals` ">"
for agy — see the evidence file's frozen marker table) and `idle-prompt-in-box`
(bare `>` line above a box border within the bottom K
lines). This is the architecturally sanctioned slot ("per-CLI grid rules are
Epic 11 adapter work" — heuristic.go), leaves claude/codex behavior
byte-identical, and stays inside T-4 humility (unknown remains the fallback).
Alternative considered and rejected: widen the generic heuristic to scan the
bottom K lines for spinners — smaller change but touches all CLIs'
classification and risks false actives on scrolled agent output.

Typed events (v1.1+):
1. **opencode**: spawn-or-attach to `opencode serve` and consume `GET /event`
   SSE (`session.status` busy/idle/retry + permission/question requests, which
   map cleanly onto turn/interaction). The cleanest event source available.
2. **agy**: hooks exist (`.agents/hooks.json`, desktop-compatible format) but
   are file-configured, not argv-injectable — wiring them to `swarm hook`
   would mutate workspace config (violates the T-2 spirit); revisit if agy
   grows an inline-settings flag. Print-mode stream-json is not usable for
   interactive sessions.

## 5. Small core changes required (none break T-5)

1. **Raise `probeTimeout`** in internal/adapter/detect/detect.go from 2s to ~5s.
   Evidence: under load, interpreter-based CLIs blow the 2s bound (opencode hit
   1.8s; the dropped vibe actually timed out at 2.006s → version-less →
   greyed picker). Detection is already async with a "checking..." placeholder
   and generation-guarded staleness, so a larger bound costs no UX.
2. **swarm-char `-adapter` registry gap**: cmd/swarm-char/main.go's
   `adapterRegistry` never got real adapters registered (claude/codex fixtures
   were hand-curated). Add thin `func(adapter.Fixture) adapter.Adapter`
   wrappers so T-6 characterization is tool-assisted. (The fixtures here were
   recorded with `-adapter reference`, which works but derives a meaningless
   capability entry.)
3. Optional cosmetic: internal/tui/launch.go `authLine()` is claude-only; agy
   (Google OAuth) and opencode (opencode.ai account) could get equivalent
   auth-source hints later.

## 6. Risks and open questions

- **agy auto-updater** mutates the binary between launches (1.1.3 → 1.1.4 mid
  research); version pinning is impossible → keep SupportedVersions ceiling
  open and re-probe per form-open (already the case).
- **agy workspace trust**: launches in an untrusted cwd block on a TUI prompt.
  The attached user can answer it (swarm sessions are interactive), so v1 can
  ship without special handling; do NOT auto-seed `trustedWorkspaces` (user
  config mutation).
- **opencode `ses_` last-occurrence rule** could still grab a child-session id
  if a subagent id is printed after the exit screen; considered acceptable for
  v1 (exit screen is terminal output), revisit with real-world transcripts.
- Quit sequences for scripted characterization: agy = double Ctrl+C, opencode =
  Ctrl+C (verified working in the swarm-char drive scripts under
  docs/verification/cli-trio-exploration/).

## 7. Implementation plan (next phase, not this branch)

Per CLI, TDD, mirroring the claude/codex package conventions (conformance +
import-boundary + no-IO-source + fixture + capability tests):
1. Fix swarm-char registry (5.2) and raise probeTimeout (5.1) first.
2. Re-record clean fixtures per CLI with swarm-char (idle + turn + permission
   scenarios), commit under internal/adapter/<cli>/testdata/.
3. Port each trioproto adapter into internal/adapter/<cli>/ with its full test
   suite (failing-first evidence per GG-5).
4. Registry entries (constructors + production) + amend system-spec.md T-7 and
   the architecture diagram.
5. Descriptor-driven grid rules per section 4, fixture-proven per CLI.
6. Epic evidence file under docs/verification/, audit pass, delete
   internal/adapter/trioproto/ and internal/engine/trio_exploration_test.go.

## Appendix: vibe (Mistral) — evaluated and DROPPED (2026-07-18)

vibe 2.15.0 was fully researched and prototyped, then dropped by decision
("too shitty for now"). Findings retained for a future re-evaluation:
resume (`vibe --trust --resume <uuid>`) works and was verified live, but the
session id never appears in the PTY stream (no crash-resume), model selection
and update-modal suppression are env-only (no argv path), the TUI is
heuristic-blind after startup, a blocking update modal appears when the
install is stale, and it is a Python/Textual app whose version probe exceeded
the 2s detect bound under load. Its recorded fixture (fx_vibe.json) and drive
script remain under docs/verification/cli-trio-exploration/.
